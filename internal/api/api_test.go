package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/99apps-id/termixgo/internal/app"
)

type fakeRunner struct {
	answer   string
	err      error
	gotInput string
	gotAgent string
	gotSteps int
	active   string
}

func (f *fakeRunner) RunIsolatedTurn(_ context.Context, prompt, agentName string, maxSteps int) (string, error) {
	f.gotInput = prompt
	f.gotAgent = agentName
	f.gotSteps = maxSteps
	return f.answer, f.err
}

func (f *fakeRunner) ModelLabel() string    { return "test-model" }
func (f *fakeRunner) ModelID() string       { return "test:model" }
func (f *fakeRunner) ModelProvider() string { return "test" }
func (f *fakeRunner) ActiveAgent() string {
	if f.active == "" {
		return "default"
	}
	return f.active
}
func (f *fakeRunner) Workspace() string   { return "/work" }
func (f *fakeRunner) Running() bool       { return false }
func (f *fakeRunner) QueueStatus() string { return "idle" }
func (f *fakeRunner) ToolNames() []string { return []string{"read_file", "run_command"} }

func newTestAPI(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	server := New(&fakeRunner{answer: "done"}, "127.0.0.1:0", "secret-token")
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return server, httpServer
}

func request(t *testing.T, httpServer *httptest.Server, method, path, token, body string) *http.Response {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, httpServer.URL+path, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return response
}

func TestRequiresBearerToken(t *testing.T) {
	_, httpServer := newTestAPI(t)
	for _, token := range []string{"", "wrong"} {
		response := request(t, httpServer, http.MethodGet, "/v1/health", token, "")
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q => %d, want 401", token, response.StatusCode)
		}
		response.Body.Close()
	}
	response := request(t, httpServer, http.MethodGet, "/v1/health", "secret-token", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("valid token => %d", response.StatusCode)
	}
	defer response.Body.Close()
	var health healthResponse
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !health.OK || health.Model != "test-model" || health.Agent != "default" || health.Tools != 2 {
		t.Fatalf("health = %+v", health)
	}
}

func TestLivenessNeedsNoToken(t *testing.T) {
	_, httpServer := newTestAPI(t)
	response := request(t, httpServer, http.MethodGet, "/healthz", "", "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestTurnReturnsTheAnswer(t *testing.T) {
	server, httpServer := newTestAPI(t)
	fake := server.runner.(*fakeRunner)
	fake.answer = "the answer"
	response := request(t, httpServer, http.MethodPost, "/v1/turn", "secret-token",
		`{"prompt":"  do the thing  ","max_steps":7}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var body turnResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.OK || body.Answer != "the answer" || body.Agent != "default" {
		t.Fatalf("body = %+v", body)
	}
	if fake.gotInput != "do the thing" || fake.gotSteps != 7 || fake.gotAgent != "" {
		t.Fatalf("runner got %q %q %d", fake.gotInput, fake.gotAgent, fake.gotSteps)
	}
}

func TestTurnValidatesInput(t *testing.T) {
	_, httpServer := newTestAPI(t)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"empty", `{"prompt":"   "}`, http.StatusBadRequest},
		{"bad json", `{`, http.StatusBadRequest},
		{"steps", `{"prompt":"x","max_steps":999}`, http.StatusBadRequest},
		{"too large", `{"prompt":"` + strings.Repeat("x", maxPromptBytes+1) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for _, testCase := range cases {
		response := request(t, httpServer, http.MethodPost, "/v1/turn", "secret-token", testCase.body)
		if response.StatusCode != testCase.want {
			t.Errorf("%s => %d, want %d", testCase.name, response.StatusCode, testCase.want)
		}
		response.Body.Close()
	}
}

func TestTurnMapsBusyAndMissingModel(t *testing.T) {
	server, httpServer := newTestAPI(t)
	fake := server.runner.(*fakeRunner)
	fake.err = app.ErrBusy
	response := request(t, httpServer, http.MethodPost, "/v1/turn", "secret-token", `{"prompt":"x"}`)
	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("busy => %d", response.StatusCode)
	}
	var busy turnResponse
	_ = json.NewDecoder(response.Body).Decode(&busy)
	response.Body.Close()
	if !busy.Busy {
		t.Fatalf("body = %+v", busy)
	}

	fake.err = app.ErrNoModel
	response = request(t, httpServer, http.MethodPost, "/v1/turn", "secret-token", `{"prompt":"x"}`)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no model => %d", response.StatusCode)
	}
	response.Body.Close()
}

func TestRateLimit(t *testing.T) {
	server, httpServer := newTestAPI(t)
	server.rateLimit = 1
	first := request(t, httpServer, http.MethodPost, "/v1/turn", "secret-token", `{"prompt":"one"}`)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first => %d", first.StatusCode)
	}
	second := request(t, httpServer, http.MethodPost, "/v1/turn", "secret-token", `{"prompt":"two"}`)
	second.Body.Close()
	if second.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second => %d, want 429", second.StatusCode)
	}
}

func TestToolsEndpoint(t *testing.T) {
	_, httpServer := newTestAPI(t)
	response := request(t, httpServer, http.MethodGet, "/v1/tools", "secret-token", "")
	defer response.Body.Close()
	var body struct {
		Tools []string `json:"tools"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Tools) != 2 || body.Tools[0] != "read_file" {
		t.Fatalf("tools = %v", body.Tools)
	}
}

func TestUnknownEndpoint(t *testing.T) {
	_, httpServer := newTestAPI(t)
	response := request(t, httpServer, http.MethodGet, "/nope", "secret-token", "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestIsLoopback(t *testing.T) {
	loopback := []string{"127.0.0.1:8787", "localhost:8787", "[::1]:8787"}
	for _, address := range loopback {
		if !IsLoopback(address) {
			t.Fatalf("%s should be loopback", address)
		}
	}
	public := []string{":8787", "0.0.0.0:8787", "192.168.1.5:8787", "example.com:443"}
	for _, address := range public {
		if IsLoopback(address) {
			t.Fatalf("%s should not be loopback", address)
		}
	}
}

func TestGenerateToken(t *testing.T) {
	first, err := GenerateToken()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	second, err := GenerateToken()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if len(first) != 64 || first == second {
		t.Fatalf("tokens = %q %q", first, second)
	}
}

func TestStartWithoutTokenIsRefused(t *testing.T) {
	server := New(&fakeRunner{}, "127.0.0.1:0", "")
	if err := server.Start(); err == nil {
		_ = server.Shutdown(context.Background())
		t.Fatal("a server without a token must refuse to start")
	}
}
