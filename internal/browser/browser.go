// Package browser drives a real Chromium page over the DevTools Protocol: it
// launches one headless browser, keeps it for the session, and exposes the
// actions an agent needs to use a web application rather than read it. The
// browser stays alive between tool calls, so a click can follow a navigation
// and a form can be filled across several steps.
//
// The protocol runs on a WebSocket to the page target. Nothing here uses a
// shell: JavaScript that carries model text is built with a JSON-quoted
// literal, and every input event is dispatched through the protocol.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// startupTimeout bounds how long the browser may take to publish its debugging
// port. A cold start on a small machine is a few seconds; a minute means it is
// not going to answer.
const startupTimeout = 20 * time.Second

// idleTimeout closes a browser nobody used. A headless Chromium holds a
// renderer and its caches, which is real memory on a machine the project
// targets at 2 GB.
const idleTimeout = 5 * time.Minute

// Target is one DevTools target, as /json/list reports it.
type Target struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	Title                string `json:"title"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

// Session is one browser process and its page connection.
type Session struct {
	binary      string
	port        int
	userDataDir string
	cmd         *exec.Cmd

	conn *cdpConn

	// actionMu serialises one high-level action. A click is three protocol
	// commands, and interleaving two actions would land the events in a state
	// neither caller expected.
	actionMu sync.Mutex

	mu       sync.Mutex
	lastUsed time.Time
	closed   bool
}

var (
	sharedMu sync.Mutex
	shared   *Session
)

// Open returns the shared session, starting the browser if it is not running.
func Open(ctx context.Context) (*Session, error) {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if shared != nil {
		shared.touch()
		return shared, nil
	}
	session, err := start(ctx)
	if err != nil {
		return nil, err
	}
	shared = session
	return session, nil
}

// Close shuts the shared browser down, if one is running.
func Close() error {
	sharedMu.Lock()
	session := shared
	shared = nil
	sharedMu.Unlock()
	if session == nil {
		return nil
	}
	return session.Close()
}

// IsOpen reports whether a browser is running.
func IsOpen() bool {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	return shared != nil
}

// closeIfShared closes the session only when it is still the shared one, so a
// stale idle timer cannot kill a browser another caller just opened.
func closeIfShared(session *Session) {
	sharedMu.Lock()
	if shared != session {
		sharedMu.Unlock()
		return
	}
	shared = nil
	sharedMu.Unlock()
	_ = session.Close()
}

// FindExecutable locates an installed Chromium-based browser.
func FindExecutable() (string, error) {
	if fromEnv := os.Getenv("TERMIXGO_BROWSER_BIN"); fromEnv != "" {
		if _, err := os.Stat(fromEnv); err == nil {
			return fromEnv, nil
		}
	}

	switch runtime.GOOS {
	case "windows":
		candidates := []string{
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
		}
		if localApp := os.Getenv("LOCALAPPDATA"); localApp != "" {
			candidates = append(candidates,
				filepath.Join(localApp, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(localApp, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(localApp, `BraveSoftware\Brave-Browser\Application\brave.exe`),
			)
		}
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
		for _, name := range []string{"msedge.exe", "chrome.exe", "brave.exe"} {
			if path, err := exec.LookPath(name); err == nil {
				return path, nil
			}
		}

	case "darwin":
		candidates := []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
		for _, name := range []string{"google-chrome", "chromium", "microsoft-edge"} {
			if path, err := exec.LookPath(name); err == nil {
				return path, nil
			}
		}

	default:
		for _, name := range []string{"google-chrome-stable", "google-chrome", "chromium-browser", "chromium", "microsoft-edge", "brave-browser"} {
			if path, err := exec.LookPath(name); err == nil {
				return path, nil
			}
		}
	}

	return "", errors.New("no compatible browser executable (Chrome, Edge, Chromium or Brave) found on this host")
}

// start launches the browser and connects to its first page.
func start(ctx context.Context) (*Session, error) {
	binary, err := FindExecutable()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "termixgo-browser-")
	if err != nil {
		return nil, fmt.Errorf("cannot create the browser profile: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil && runtime.GOOS != "windows" {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("cannot secure the browser profile: %v", err)
	}

	// The port stays 0 so the browser picks a free one and publishes it in
	// DevToolsActivePort. Reading that file is more reliable than guessing a
	// port and racing whatever else the machine binds.
	args := []string{
		"--headless=new",
		"--remote-debugging-port=0",
		"--user-data-dir=" + dir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-sync",
		"--disable-default-apps",
		"--metrics-recording-only",
		"--mute-audio",
		"--hide-scrollbars",
		"about:blank",
	}
	command := exec.Command(binary, args...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("cannot start %s: %v", filepath.Base(binary), err)
	}

	port, _, err := readDevToolsPort(dir, time.Now().Add(startupTimeout))
	if err != nil {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		_ = os.RemoveAll(dir)
		return nil, err
	}

	session := &Session{
		binary:      binary,
		port:        port,
		userDataDir: dir,
		cmd:         command,
		lastUsed:    time.Now(),
	}

	target, err := session.firstPageTarget(ctx)
	if err != nil {
		session.kill()
		return nil, err
	}
	target, err = session.ensurePage(ctx, target)
	if err != nil {
		session.kill()
		return nil, err
	}
	conn, err := dialCDP(ctx, target.WebSocketDebuggerURL)
	if err != nil {
		session.kill()
		return nil, err
	}
	session.conn = conn
	// A fixed viewport makes a screenshot and a coordinate click agree. The
	// headless default has changed between Chrome releases.
	_, _ = session.conn.call(ctx, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width": 1280, "height": 800, "deviceScaleFactor": 1, "mobile": false,
	})
	go session.watchIdle()
	return session, nil
}

// firstPageTarget lists targets and takes the first page, creating one when the
// launch left no page open.
func (s *Session) firstPageTarget(ctx context.Context) (Target, error) {
	targets, err := s.listTargets(ctx)
	if err == nil {
		for _, target := range targets {
			if target.Type == "page" && target.WebSocketDebuggerURL != "" {
				return target, nil
			}
		}
	}
	return Target{}, nil
}

// ensurePage creates a page target when the launch produced none.
func (s *Session) ensurePage(ctx context.Context, target Target) (Target, error) {
	if target.WebSocketDebuggerURL != "" {
		return target, nil
	}
	created, err := s.newTarget(ctx, "about:blank")
	if err != nil {
		return Target{}, fmt.Errorf("the browser started but has no page: %v", err)
	}
	return created, nil
}

func (s *Session) watchIdle() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		closed := s.closed
		last := s.lastUsed
		s.mu.Unlock()
		if closed {
			return
		}
		if time.Since(last) > idleTimeout {
			closeIfShared(s)
			return
		}
	}
}

func (s *Session) touch() {
	s.mu.Lock()
	s.lastUsed = time.Now()
	s.mu.Unlock()
}

// TargetID reports the page target this session is attached to.
func (s *Session) TargetID() string {
	if s == nil {
		return ""
	}
	// The tab tools read this between actions, so it takes the same lock the
	// attach path holds rather than reading the pointer unlocked.
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	if s.conn == nil {
		return ""
	}
	return s.conn.targetID
}

// Close shuts the protocol connection and the browser process down.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	// Taking actionMu serialises the connection teardown with an in-flight
	// command. The caller's context bounds that command, so a stuck page
	// cannot hold the shutdown for longer than its own deadline.
	s.actionMu.Lock()
	conn := s.conn
	s.conn = nil
	s.actionMu.Unlock()
	if conn != nil {
		conn.Close()
	}
	s.kill()
	return nil
}

// kill terminates the process and removes the profile directory.
func (s *Session) kill() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		done := make(chan struct{})
		go func() {
			_, _ = s.cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
	if s.userDataDir != "" {
		_ = os.RemoveAll(s.userDataDir)
	}
}

// readDevToolsPort waits for the browser to publish its port.
func readDevToolsPort(dir string, deadline time.Time) (int, string, error) {
	path := filepath.Join(dir, "DevToolsActivePort")
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) >= 1 {
				port, err := strconv.Atoi(strings.TrimSpace(lines[0]))
				if err == nil && port > 0 {
					browserPath := ""
					if len(lines) > 1 {
						browserPath = strings.TrimSpace(lines[1])
					}
					return port, browserPath, nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, "", errors.New("the browser did not publish a debugging port: it may be blocked by policy or already running with another profile")
}

// endpoint builds one DevTools HTTP URL.
func (s *Session) endpoint(path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", s.port, path)
}

// requestJSON performs one DevTools HTTP request and decodes the result.
func (s *Session) requestJSON(ctx context.Context, method, path string, out any) error {
	request, err := http.NewRequestWithContext(ctx, method, s.endpoint(path), nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("the browser refused %s %s: %s", method, path, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (s *Session) listTargets(ctx context.Context) ([]Target, error) {
	var targets []Target
	if err := s.requestJSON(ctx, http.MethodGet, "/json/list", &targets); err != nil {
		return nil, err
	}
	return targets, nil
}

// newTarget opens a tab. Newer Chrome requires PUT on /json/new and older
// builds only accept GET, so both are tried.
func (s *Session) newTarget(ctx context.Context, rawURL string) (Target, error) {
	path := "/json/new?" + url.QueryEscape(rawURL)
	var target Target
	err := s.requestJSON(ctx, http.MethodPut, path, &target)
	if err == nil && target.WebSocketDebuggerURL != "" {
		return target, nil
	}
	target = Target{}
	if getErr := s.requestJSON(ctx, http.MethodGet, path, &target); getErr != nil {
		if err != nil {
			return Target{}, err
		}
		return Target{}, getErr
	}
	return target, nil
}

// NormalizeURL accepts an absolute http, https, file, about or data URL, and
// adds https to a bare host. Link-local metadata endpoints are refused: a
// browser is a fine way to read a page, not a way to reach the cloud's own
// credentials endpoint.
func NormalizeURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("url is required")
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "about:") || strings.HasPrefix(lower, "data:") {
		return trimmed, nil
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("cannot read %q as a URL", raw)
	}
	if parsed.Scheme == "file" {
		if parsed.Path == "" {
			return "", errors.New("a file URL needs a path")
		}
		return trimmed, nil
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("cannot read %q as a URL", raw)
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("scheme %q is not supported: use http, https or file", parsed.Scheme)
	}
	if isMetadataHost(parsed.Hostname()) {
		return "", errors.New("that host is a link-local or cloud metadata address, which the browser does not open")
	}
	return trimmed, nil
}

// metadataHosts are the credential endpoints a page must never reach, by name
// or by address.
var metadataHosts = map[string]bool{
	"metadata.google.internal": true,
	"metadata":                 true,
	"169.254.169.254":          true,
	"169.254.170.2":            true,
	"fd00:ec2::254":            true,
	"100.100.100.200":          true,
}

func isMetadataHost(host string) bool {
	trimmed := strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
	if metadataHosts[trimmed] {
		return true
	}
	if ip := net.ParseIP(trimmed); ip != nil {
		return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
	}
	return false
}
