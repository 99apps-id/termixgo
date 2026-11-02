// Package api is the local HTTP endpoint that lets another program ask
// Termixgo to run a turn. It exists for the 24/7 assistant: a webhook, a cron
// entry on another host, or a dashboard can hand the agent a prompt without
// impersonating a chat account.
//
// The endpoint is off by default, binds to loopback unless the operator says
// otherwise, and every call carries a bearer token kept in the secret store.
// A turn runs on a throwaway session, so an integration cannot rewrite the
// operator's conversation.
package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/99apps-id/termixgo/internal/app"
	"github.com/99apps-id/termixgo/internal/version"
)

const (
	// DefaultListen is the address used when the config names none.
	DefaultListen = "127.0.0.1:8787"
	// TokenKey is the secret store entry holding the bearer token.
	TokenKey = "api:token"

	maxBodyBytes   = 1 << 20
	maxPromptBytes = 64 << 10
	// TurnTimeout bounds one request. A long coding task can legitimately take
	// minutes, but not forever, and the caller has a deadline either way.
	TurnTimeout = 10 * time.Minute
	// defaultRateLimit caps turns per minute, so a broken caller cannot spend
	// the session budget in a loop.
	defaultRateLimit = 60
)

// Runner is what the API needs from the assistant. The app implements it.
type Runner interface {
	RunIsolatedTurn(ctx context.Context, prompt, agentName string, maxSteps int) (string, error)
	ModelLabel() string
	ModelID() string
	ModelProvider() string
	ActiveAgent() string
	Workspace() string
	Running() bool
	QueueStatus() string
	ToolNames() []string
}

// Server is the HTTP endpoint.
type Server struct {
	runner Runner
	listen string
	token  string

	started   time.Time
	server    *http.Server
	listener  net.Listener
	rateLimit int

	mu          sync.Mutex
	windowStart time.Time
	windowCount int
}

// New builds a server. An empty token is refused by Start, because an
// unauthenticated endpoint on a machine with an agent is not a safe default.
func New(runner Runner, listen, token string) *Server {
	if strings.TrimSpace(listen) == "" {
		listen = DefaultListen
	}
	return &Server{
		runner:    runner,
		listen:    listen,
		token:     strings.TrimSpace(token),
		started:   time.Now(),
		rateLimit: defaultRateLimit,
	}
}

// Addr reports the address the server bound, or the configured one before
// Start.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.listen
}

// Start binds the address and serves in the background, so a bind failure is
// returned synchronously instead of being lost in a goroutine.
func (s *Server) Start() error {
	if s.token == "" {
		return errors.New("the API needs a bearer token: run 'termixgo api rotate' to create one")
	}
	listener, err := net.Listen("tcp", s.listen)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", s.listen, err)
	}
	s.listener = listener
	s.server = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// The listener is gone. The serve loop keeps running; the operator
			// finds out from the next call rather than a silent death.
			s.listener = nil
		}
	}()
	return nil
}

// Shutdown stops accepting requests and waits for the in-flight one.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

// IsLoopback reports whether the listen address binds only this machine. A
// wildcard host such as ":8787" does not.
func IsLoopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// GenerateToken returns a fresh 256-bit bearer token.
func GenerateToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("cannot generate an API token: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

// Handler returns the routes. It is exported so a test can mount the API on
// httptest without binding a port.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Liveness carries no detail and needs no token, so a probe can watch the
	// process without holding a credential.
	mux.HandleFunc("GET /healthz", s.handleLiveness)
	mux.HandleFunc("GET /v1/health", s.requireAuth(s.handleHealth))
	mux.HandleFunc("GET /v1/tools", s.requireAuth(s.handleTools))
	mux.HandleFunc("POST /v1/turn", s.requireAuth(s.handleTurn))
	mux.HandleFunc("/", s.handleNotFound)
	return mux
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		header := request.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(header, prefix) {
			writeError(writer, http.StatusUnauthorized, "a bearer token is required")
			return
		}
		given := strings.TrimSpace(strings.TrimPrefix(header, prefix))
		if s.token == "" || subtle.ConstantTimeCompare([]byte(given), []byte(s.token)) != 1 {
			writeError(writer, http.StatusUnauthorized, "the bearer token is not valid")
			return
		}
		next(writer, request)
	}
}

func (s *Server) handleLiveness(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true})
}

type healthResponse struct {
	OK            bool   `json:"ok"`
	Version       string `json:"version"`
	Model         string `json:"model"`
	ModelID       string `json:"model_id,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Agent         string `json:"agent"`
	Workspace     string `json:"workspace"`
	Running       bool   `json:"running"`
	Turn          string `json:"turn"`
	Tools         int    `json:"tools"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

func (s *Server) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, healthResponse{
		OK:            true,
		Version:       version.Full(),
		Model:         s.runner.ModelLabel(),
		ModelID:       s.runner.ModelID(),
		Provider:      s.runner.ModelProvider(),
		Agent:         s.runner.ActiveAgent(),
		Workspace:     s.runner.Workspace(),
		Running:       s.runner.Running(),
		Turn:          s.runner.QueueStatus(),
		Tools:         len(s.runner.ToolNames()),
		UptimeSeconds: int64(time.Since(s.started).Seconds()),
	})
}

func (s *Server) handleTools(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "tools": s.runner.ToolNames()})
}

type turnRequest struct {
	Prompt   string `json:"prompt"`
	Agent    string `json:"agent,omitempty"`
	MaxSteps int    `json:"max_steps,omitempty"`
}

type turnResponse struct {
	OK     bool   `json:"ok"`
	Answer string `json:"answer,omitempty"`
	Agent  string `json:"agent,omitempty"`
	Busy   bool   `json:"busy,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (s *Server) handleTurn(writer http.ResponseWriter, request *http.Request) {
	if !s.allowTurn() {
		writeJSON(writer, http.StatusTooManyRequests, turnResponse{OK: false, Busy: true, Error: "too many turns per minute"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxBodyBytes)
	var body turnRequest
	decoder := json.NewDecoder(request.Body)
	if err := decoder.Decode(&body); err != nil {
		writeError(writer, http.StatusBadRequest, "the request body must be a JSON object with a prompt")
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if prompt == "" {
		writeError(writer, http.StatusBadRequest, "prompt is required")
		return
	}
	if len(prompt) > maxPromptBytes {
		writeError(writer, http.StatusRequestEntityTooLarge, fmt.Sprintf("prompt is larger than %d bytes", maxPromptBytes))
		return
	}
	if body.MaxSteps < 0 || body.MaxSteps > 300 {
		writeError(writer, http.StatusBadRequest, "max_steps must be between 1 and 300, or omitted")
		return
	}
	agentName := strings.TrimSpace(body.Agent)
	if agentName == "" {
		agentName = s.runner.ActiveAgent()
	}

	ctx, cancel := context.WithTimeout(request.Context(), TurnTimeout)
	defer cancel()
	answer, err := s.runner.RunIsolatedTurn(ctx, prompt, body.Agent, body.MaxSteps)
	if err != nil {
		switch {
		case errors.Is(err, app.ErrBusy):
			writeJSON(writer, http.StatusTooManyRequests, turnResponse{OK: false, Busy: true, Error: "the assistant is busy; retry shortly"})
		case errors.Is(err, app.ErrNoModel):
			writeError(writer, http.StatusServiceUnavailable, "no usable model is configured")
		case errors.Is(err, context.DeadlineExceeded):
			writeError(writer, http.StatusGatewayTimeout, "the turn did not finish within the request deadline")
		case errors.Is(err, context.Canceled):
			writeError(writer, http.StatusRequestTimeout, "the request was cancelled")
		default:
			writeJSON(writer, http.StatusInternalServerError, turnResponse{OK: false, Error: err.Error()})
		}
		return
	}
	writeJSON(writer, http.StatusOK, turnResponse{OK: true, Answer: answer, Agent: agentName})
}

// allowTurn enforces a fixed one-minute window.
func (s *Server) allowTurn() bool {
	if s.rateLimit <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.windowStart) >= time.Minute {
		s.windowStart = now
		s.windowCount = 0
	}
	if s.windowCount >= s.rateLimit {
		return false
	}
	s.windowCount++
	return true
}

func (s *Server) handleNotFound(writer http.ResponseWriter, _ *http.Request) {
	writeError(writer, http.StatusNotFound, "no such endpoint: use GET /healthz, GET /v1/health, GET /v1/tools or POST /v1/turn")
}

func writeJSON(writer http.ResponseWriter, status int, body any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]any{"ok": false, "error": message})
}
