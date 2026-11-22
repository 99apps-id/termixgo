package agent

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
	"time"
)

// The web tools reach the network through one transport. Two ways a name can
// go wrong are covered here, and the third (a name the system resolves to the
// right address) needs nothing:
//
//   - The system resolver fails: dialWithDoHFallback resolves over HTTPS.
//   - The system resolver answers with a wrong address, which is what an ISP
//     block page or a poisoned cache does: the connection reaches the wrong
//     host and fails in the TLS handshake, or is reset. dohRetryTransport
//     retries such a GET once with the address taken from DoH.

const defaultDoHEndpoint = "https://cloudflare-dns.com/dns-query"

// dohFallbackEndpoints are tried when the first resolver cannot be reached.
// Two of them are addressed by IP, so asking them needs no name lookup: a
// resolver that itself sits behind the blocked DNS is no help.
var dohFallbackEndpoints = []string{
	"https://1.1.1.1/dns-query",
	"https://dns.google/resolve",
	"https://8.8.8.8/resolve",
}

// dohEndpointList is the resolvers to ask, in order. A test or an operator
// that points dohEndpoint somewhere else gets exactly that resolver and no
// public ones behind it.
var dohEndpointList = func() []string {
	if dohEndpoint != defaultDoHEndpoint {
		return []string{dohEndpoint}
	}
	return append([]string{dohEndpoint}, dohFallbackEndpoints...)
}

var errDoHNoName = errors.New("doh: the name does not exist")

type dohCacheEntry struct {
	ips     []string
	expires time.Time
}

var (
	dohCacheMu sync.Mutex
	dohCache   = map[string]dohCacheEntry{}
)

func dohCacheKey(host string) string { return dohEndpoint + "|" + strings.ToLower(host) }

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

// dohResolve resolves a host over the DNS-over-HTTPS JSON API. It asks each
// resolver in turn, takes IPv4 then IPv6, and remembers an answer for as long
// as its TTL allows, bounded, so a page that pulls a dozen resources does not
// ask a dozen times.
func dohResolve(ctx context.Context, host string) ([]string, error) {
	if ips, ok := dohCacheGet(host); ok {
		return ips, nil
	}
	var lastErr error
	for _, endpoint := range dohEndpointList() {
		ips, ttl, err := dohQuery(ctx, endpoint, host)
		if errors.Is(err, errDoHNoName) {
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
	response, err := dohClient.Do(request)
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
		return nil, 0, errDoHNoName
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

// dialDoHOnly dials a host by an address taken from DoH, never from the system
// resolver. It is what the retry after a wrong answer uses.
func dialDoHOnly(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second, Control: blockMetadataAddress}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) != nil {
		return dialer.DialContext(ctx, network, addr)
	}
	ips, err := dohResolve(ctx, host)
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

func newWebTransport(dial func(context.Context, string, string) (net.Conn, error)) *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}

// dohRetryTransport retries a failed GET once through a transport that
// resolves over DoH. The first error is the one returned when the retry fails
// too, so the operator sees what their own network did.
type dohRetryTransport struct {
	base   http.RoundTripper
	pinned http.RoundTripper
}

func newDoHRetryTransport() http.RoundTripper {
	return &dohRetryTransport{base: newWebTransport(dialWithDoHFallback), pinned: newWebTransport(dialDoHOnly)}
}

func (t *dohRetryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err == nil || !retryViaDoH(request, err) {
		return response, err
	}
	retried, retryErr := t.pinned.RoundTrip(request)
	if retryErr != nil {
		return nil, err
	}
	return retried, nil
}

// retryViaDoH reports whether a failure looks like the system resolver handed
// back a wrong address: a certificate or TLS error, a reset or empty reply, or
// a blackholed connect. A DNS failure is not retried here, because the dialer
// already asked DoH for it.
func retryViaDoH(request *http.Request, err error) bool {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		return false
	}
	if request.Body != nil && request.Body != http.NoBody {
		return false
	}
	if request.Context().Err() != nil || net.ParseIP(request.URL.Hostname()) != nil || isDNSFailure(err) {
		return false
	}
	lowered := strings.ToLower(err.Error())
	for _, needle := range []string{"x509:", "certificate", "tls:", "remote error", "eof", "connection reset", "dial tcp"} {
		if strings.Contains(lowered, needle) {
			return true
		}
	}
	return false
}
