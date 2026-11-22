// Package dohnet gives an HTTP client a way round broken or poisoned system
// DNS. The web tools, the model providers and the Telegram bridge reach the
// network through a transport from NewTransport.
package dohnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Two ways a name can
// go wrong are covered here, and the third (a name the system resolves to the
// right address) needs nothing:
//
//   - The system resolver fails: DialWithFallback resolves over HTTPS.
//   - The system resolver answers with a wrong address, which is what an ISP
//     block page or a poisoned cache does: the connection reaches the wrong
//     host and fails in the TLS handshake, or is reset. retryTransport
//     retries such a GET once with the address taken from DoH.

const DefaultEndpoint = "https://cloudflare-dns.com/dns-query"

// Endpoint is the first resolver asked. A test or an operator that changes it
// gets exactly that resolver and no public ones behind it.
var Endpoint = DefaultEndpoint

// Control, when set, runs on every dialled address before the connection is
// made. The web tools use it to refuse metadata and link-local targets.
var Control func(network, address string, c syscall.RawConn) error

var queryClient = &http.Client{Timeout: 10 * time.Second}

// fallbackEndpoints are tried when the first resolver cannot be reached.
// Two of them are addressed by IP, so asking them needs no name lookup: a
// resolver that itself sits behind the blocked DNS is no help.
var fallbackEndpoints = []string{
	"https://1.1.1.1/dns-query",
	"https://dns.google/resolve",
	"https://8.8.8.8/resolve",
}

// EndpointList is the resolvers to ask, in order. A test or an operator
// that points Endpoint somewhere else gets exactly that resolver and no
// public ones behind it.
// EndpointList is the resolvers to ask, in order.
var EndpointList = func() []string {
	if Endpoint != DefaultEndpoint {
		return []string{Endpoint}
	}
	return append([]string{Endpoint}, fallbackEndpoints...)
}

var ErrNoName = errors.New("doh: the name does not exist")

type dohCacheEntry struct {
	ips     []string
	expires time.Time
}

var (
	dohCacheMu sync.Mutex
	dohCache   = map[string]dohCacheEntry{}
)

func dohCacheKey(host string) string { return Endpoint + "|" + strings.ToLower(host) }

func dohCacheGet(host string) ([]string, bool) {
	dohCacheMu.Lock()
	defer dohCacheMu.Unlock()
	entry, ok := dohCache[dohCacheKey(host)]
	if !ok || time.Now().After(entry.expires) {
		delete(dohCache, dohCacheKey(host))
		return nil, false
	}
	return append([]string(nil), entry.ips...), true
}

func dohCachePut(host string, ips []string, ttl time.Duration) {
	if ttl < 30*time.Second {
		ttl = 30 * time.Second
	}
	if ttl > 10*time.Minute {
		ttl = 10 * time.Minute
	}
	dohCacheMu.Lock()
	defer dohCacheMu.Unlock()
	dohCache[dohCacheKey(host)] = dohCacheEntry{ips: append([]string(nil), ips...), expires: time.Now().Add(ttl)}
}

// Resolve resolves a host over the DNS-over-HTTPS JSON API. It asks each
// resolver in turn, takes IPv4 then IPv6, and remembers an answer for as long
// as its TTL allows, bounded, so a page that pulls a dozen resources does not
// ask a dozen times.
func Resolve(ctx context.Context, host string) ([]string, error) {
	if ips, ok := dohCacheGet(host); ok {
		return ips, nil
	}
	var lastErr error
	for _, endpoint := range EndpointList() {
		ips, ttl, err := dohQuery(ctx, endpoint, host)
		if errors.Is(err, ErrNoName) {
			return nil, err
		}
		if err == nil && len(ips) > 0 {
			dohCachePut(host, ips, ttl)
			return ips, nil
		}
		if err != nil {
			lastErr = err
		}
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("doh: no resolver returned an address")
	}
	return nil, lastErr
}

func dohQuery(ctx context.Context, endpoint, host string) ([]string, time.Duration, error) {
	var ips []string
	var ttl time.Duration
	for _, recordType := range []string{"A", "AAAA"} {
		found, foundTTL, err := dohQueryType(ctx, endpoint, host, recordType)
		if err != nil {
			if len(ips) > 0 {
				break
			}
			return nil, 0, err
		}
		ips = append(ips, found...)
		if foundTTL > 0 && (ttl == 0 || foundTTL < ttl) {
			ttl = foundTTL
		}
		if len(ips) > 0 {
			break
		}
	}
	return ips, ttl, nil
}

func dohQueryType(ctx context.Context, endpoint, host, recordType string) ([]string, time.Duration, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	link := endpoint + "?name=" + url.QueryEscape(host) + "&type=" + recordType
	request, err := http.NewRequestWithContext(queryCtx, http.MethodGet, link, nil)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("accept", "application/dns-json")
	response, err := queryClient.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, 0, fmt.Errorf("doh returned %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, 0, err
	}
	var parsed struct {
		Status int `json:"Status"`
		Answer []struct {
			Type int    `json:"type"`
			TTL  int    `json:"TTL"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, 0, err
	}
	if parsed.Status == 3 {
		return nil, 0, ErrNoName
	}
	wanted := 1
	if recordType == "AAAA" {
		wanted = 28
	}
	var ips []string
	var ttl time.Duration
	for _, answer := range parsed.Answer {
		if answer.Type == wanted && net.ParseIP(answer.Data) != nil {
			ips = append(ips, answer.Data)
			if d := time.Duration(answer.TTL) * time.Second; d > 0 && (ttl == 0 || d < ttl) {
				ttl = d
			}
		}
	}
	return ips, ttl, nil
}

// DialDoHOnly dials a host by an address taken from DoH, never from the system
// resolver. It is what the retry after a wrong answer uses.
func DialDoHOnly(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second, Control: Control}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) != nil {
		return dialer.DialContext(ctx, network, addr)
	}
	ips, err := Resolve(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s over DoH: %w", host, err)
	}
	var lastErr error
	for _, ip := range ips {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

func newWebTransport(dial func(context.Context, string, string) (net.Conn, error), tune func(*http.Transport)) *http.Transport {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	if tune != nil {
		tune(transport)
		transport.DialContext = dial
	}
	return transport
}

// DialWithFallback dials an address, and when the host name cannot be resolved
// by the system resolver it resolves over HTTPS and dials the pinned IP. A
// literal address and a successful system lookup use the plain dialer, so the
// fallback only runs where DNS is actually broken. A connect failure on a
// resolved route is the answer: split-horizon DNS pointed it there on purpose.
func DialWithFallback(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second, Control: Control}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) != nil {
		return dialer.DialContext(ctx, network, addr)
	}
	conn, dialErr := dialer.DialContext(ctx, network, addr)
	if dialErr == nil {
		return conn, nil
	}
	if !IsDNSFailure(dialErr) {
		return nil, dialErr
	}
	ips, resolveErr := Resolve(ctx, host)
	if resolveErr != nil || len(ips) == 0 {
		return nil, dialErr
	}
	lastErr := dialErr
	for _, ip := range ips {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// IsDNSFailure reports a name-resolution failure. It must not be retried: a
// blocked or nonexistent name does not resolve on a second attempt.
func IsDNSFailure(err error) bool {
	if err == nil {
		return false
	}
	lowered := strings.ToLower(err.Error())
	for _, needle := range []string{"no such host", "getaddrinfo", "temporary failure in name resolution", "server misbehaving", "no addresses"} {
		if strings.Contains(lowered, needle) {
			return true
		}
	}
	return false
}

// retryTransport redoes a failed request once through a transport that
// resolves over DoH. The first error is the one returned when the retry fails
// too, so the operator sees what their own network did.
type retryTransport struct {
	base   http.RoundTripper
	pinned http.RoundTripper
}

// NewTransport returns a transport with DoH fallback. tune adjusts the
// timeouts and pooling of both underlying transports; its DialContext is
// ignored.
func NewTransport(tune func(*http.Transport)) http.RoundTripper {
	return &retryTransport{base: newWebTransport(DialWithFallback, tune), pinned: newWebTransport(DialDoHOnly, tune)}
}

func (t *retryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err == nil || !retryViaDoH(request, err) {
		return response, err
	}
	again := request
	if request.Body != nil && request.Body != http.NoBody {
		body, bodyErr := request.GetBody()
		if bodyErr != nil {
			return nil, err
		}
		again = request.Clone(request.Context())
		again.Body = body
	}
	retried, retryErr := t.pinned.RoundTrip(again)
	if retryErr != nil {
		return nil, err
	}
	return retried, nil
}

// retryViaDoH reports whether a failure looks like the system resolver handed
// back a wrong address: a certificate or TLS error, a reset or empty reply, or
// a blackholed connect. A request with a body is redone only when it provably
// never reached a server (handshake or connect failure) and its body can be
// rewound, so a message is never sent twice.
func retryViaDoH(request *http.Request, err error) bool {
	if request.Context().Err() != nil || net.ParseIP(request.URL.Hostname()) != nil || IsDNSFailure(err) {
		return false
	}
	needles := []string{"x509:", "certificate", "tls:", "dial tcp"}
	if request.Body == nil || request.Body == http.NoBody {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			return false
		}
		needles = append(needles, "remote error", "eof", "connection reset")
	} else if request.GetBody == nil {
		return false
	}
	lowered := strings.ToLower(err.Error())
	for _, needle := range needles {
		if strings.Contains(lowered, needle) {
			return true
		}
	}
	return false
}
