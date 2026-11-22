package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// dohServer answers the JSON API from a table of "type" to the body to send.
func dohServer(t *testing.T, hits *atomic.Int32, answers map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/dns-json")
		body, ok := answers[r.URL.Query().Get("type")]
		if !ok {
			http.Error(w, "no", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func useDoHEndpoints(t *testing.T, endpoints ...string) {
	t.Helper()
	old := dohEndpointList
	dohEndpointList = func() []string { return endpoints }
	dohCacheMu.Lock()
	dohCache = map[string]dohCacheEntry{}
	dohCacheMu.Unlock()
	t.Cleanup(func() { dohEndpointList = old })
}

func TestDoHFailsOverToTheNextResolver(t *testing.T) {
	var badHits, goodHits atomic.Int32
	bad := dohServer(t, &badHits, map[string]string{})
	good := dohServer(t, &goodHits, map[string]string{"A": `{"Status":0,"Answer":[{"type":1,"TTL":300,"data":"203.0.113.7"}]}`})
	useDoHEndpoints(t, bad.URL, good.URL)
	ips, err := dohResolve(context.Background(), "example.test")
	if err != nil || len(ips) != 1 || ips[0] != "203.0.113.7" {
		t.Fatalf("ips=%v err=%v", ips, err)
	}
	if badHits.Load() == 0 {
		t.Fatal("the first resolver should have been tried first")
	}
}

func TestDoHAnswersIPv6OnlyHostsAndIsCached(t *testing.T) {
	var hits atomic.Int32
	server := dohServer(t, &hits, map[string]string{
		"A":    `{"Status":0}`,
		"AAAA": `{"Status":0,"Answer":[{"type":28,"TTL":120,"data":"2001:db8::1"}]}`,
	})
	useDoHEndpoints(t, server.URL)
	ips, err := dohResolve(context.Background(), "v6only.test")
	if err != nil || len(ips) != 1 || ips[0] != "2001:db8::1" {
		t.Fatalf("an IPv6-only name should resolve: ips=%v err=%v", ips, err)
	}
	after := hits.Load()
	if _, err := dohResolve(context.Background(), "V6ONLY.test"); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != after {
		t.Fatal("a repeated lookup must come from the cache")
	}
}

func TestDoHStopsAtANameThatDoesNotExist(t *testing.T) {
	var firstHits, secondHits atomic.Int32
	first := dohServer(t, &firstHits, map[string]string{"A": `{"Status":3}`})
	second := dohServer(t, &secondHits, map[string]string{"A": `{"Status":0,"Answer":[{"type":1,"data":"203.0.113.9"}]}`})
	useDoHEndpoints(t, first.URL, second.URL)
	if _, err := dohResolve(context.Background(), "nope.test"); !errors.Is(err, errDoHNoName) {
		t.Fatalf("err = %v", err)
	}
	if secondHits.Load() != 0 {
		t.Fatal("NXDOMAIN is the answer; the other resolvers must not be asked")
	}
}

func TestDialDoHOnlyIgnoresTheSystemResolver(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var hits atomic.Int32
	server := dohServer(t, &hits, map[string]string{"A": `{"Status":0,"Answer":[{"type":1,"TTL":60,"data":"127.0.0.1"}]}`})
	useDoHEndpoints(t, server.URL)
	conn, err := dialDoHOnly(context.Background(), "tcp", fmt.Sprintf("poisoned.invalid:%d", listener.Addr().(*net.TCPAddr).Port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()
	if hits.Load() == 0 {
		t.Fatal("the address must come from DoH")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func okResponse(r *http.Request) *http.Response {
	return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r, Header: http.Header{}}
}

// A wrong address from the system resolver shows up as a certificate error or
// a reset; the same GET is then tried with the address from DoH.
func TestRetryTransportRetriesAWrongAddressOnce(t *testing.T) {
	var baseCalls, pinnedCalls atomic.Int32
	transport := &dohRetryTransport{
		base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			baseCalls.Add(1)
			return nil, errors.New("Get: tls: failed to verify certificate: x509: certificate is valid for block.example, not site.example")
		}),
		pinned: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			pinnedCalls.Add(1)
			return okResponse(r), nil
		}),
	}
	request, _ := http.NewRequest(http.MethodGet, "https://site.example/page", nil)
	response, err := transport.RoundTrip(request)
	if err != nil || response.StatusCode != 200 || baseCalls.Load() != 1 || pinnedCalls.Load() != 1 {
		t.Fatalf("resp=%v err=%v base=%d pinned=%d", response, err, baseCalls.Load(), pinnedCalls.Load())
	}
}

func TestRetryTransportKeepsTheFirstErrorWhenTheRetryFails(t *testing.T) {
	first := errors.New("x509: certificate signed by unknown authority")
	transport := &dohRetryTransport{
		base: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, first }),
		pinned: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("doh: no resolver returned an address")
		}),
	}
	request, _ := http.NewRequest(http.MethodGet, "https://site.example/", nil)
	if _, err := transport.RoundTrip(request); err != first {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryViaDoHDecision(t *testing.T) {
	get := func(target string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, target, nil)
		return r
	}
	post, _ := http.NewRequest(http.MethodPost, "https://site.example/", strings.NewReader("x"))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	gone := get("https://site.example/").WithContext(cancelled)
	cases := []struct {
		name    string
		request *http.Request
		err     string
		want    bool
	}{
		{"certificate", get("https://site.example/"), "x509: certificate is valid for a, not b", true},
		{"reset", get("https://site.example/"), "read tcp: connection reset by peer", true},
		{"empty reply", get("https://site.example/"), "EOF", true},
		{"blackhole", get("https://site.example/"), "dial tcp 10.0.0.1:443: i/o timeout", true},
		{"dns failure is the dialer's job", get("https://site.example/"), "dial tcp: lookup site.example: no such host", false},
		{"a POST is not repeated", post, "x509: certificate", false},
		{"an IP literal has no name to resolve", get("https://203.0.113.5/"), "x509: certificate", false},
		{"a cancelled request", gone, "x509: certificate", false},
		{"an HTTP status is not a transport error", get("https://site.example/"), "server returned 500", false},
	}
	for _, c := range cases {
		if got := retryViaDoH(c.request, errors.New(c.err)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
