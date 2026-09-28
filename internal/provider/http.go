package provider

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// httpClient is the shared plumbing: one tuned transport, one JSON POST, one
// SSE reader. The per-provider clients only build payloads and decode chunks.
type httpClient struct {
	info    Provider
	baseURL string
	apiKey  string
	http    *http.Client
}

func newHTTPClient(info Provider, baseURL, apiKey string) (Client, error) {
	shared := &http.Client{
		// No overall timeout: a stream legitimately runs for minutes. A
		// stalled connection still fails fast because the header timeout
		// below only guards the start of the response.
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          16,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   20 * time.Second,
			ResponseHeaderTimeout: 120 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
	base := &httpClient{info: info, baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: shared}
	switch info.Kind {
	case KindAnthropic:
		return &anthropicClient{httpClient: base}, nil
	case KindGoogle:
		return &googleClient{httpClient: base}, nil
	default:
		return &openAIClient{httpClient: base}, nil
	}
}

func (c *httpClient) ID() string { return c.info.ID }

// statusError turns a provider error body into an actionable message.
func (c *httpClient) statusError(response *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
	message := strings.TrimSpace(string(raw))
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &parsed) == nil {
		switch {
		case parsed.Error.Message != "":
			message = parsed.Error.Message
		case parsed.Message != "":
			message = parsed.Message
		}
	}
	if len(message) > 500 {
		message = message[:500] + "..."
	}
	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	hint := ""
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		hint = " (check the API key with /setup)"
	case http.StatusTooManyRequests:
		hint = " (rate limited; retry shortly)"
	case http.StatusNotFound:
		hint = " (check the model id and base URL)"
	}
	return fmt.Errorf("%s returned %d: %s%s", c.info.Label, response.StatusCode, message, hint)
}

// sseReader reads Server-Sent Events and yields each event's joined data.
type sseReader struct {
	scanner *bufio.Scanner
}

func newSSEReader(body io.Reader) *sseReader {
	scanner := bufio.NewScanner(body)
	// Provider chunks can be large: a tool call argument or a long reasoning
	// block arrives in one event.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return &sseReader{scanner: scanner}
}

// next returns the next event payload, or io.EOF when the stream ends.
func (r *sseReader) next() (string, error) {
	var data []string
	for r.scanner.Scan() {
		line := strings.TrimRight(r.scanner.Text(), "\r")
		if line == "" {
			if len(data) == 0 {
				continue
			}
			return strings.Join(data, "\n"), nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(after, " "))
		}
	}
	if err := r.scanner.Err(); err != nil {
		return "", err
	}
	if len(data) > 0 {
		return strings.Join(data, "\n"), nil
	}
	return "", io.EOF
}
