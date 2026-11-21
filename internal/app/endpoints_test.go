package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func modelServer(t *testing.T, ids ...string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var parts []string
		for _, id := range ids {
			parts = append(parts, `{"id":"`+id+`"}`)
		}
		_, _ = w.Write([]byte(`{"data":[` + strings.Join(parts, ",") + `]}`))
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1"
}

// Several endpoints live side by side, each with its own URL, key and models,
// and a model picked from one resolves to that endpoint.
func TestSeveralEndpointsSideBySide(t *testing.T) {
	application := newTestApp(t)
	lab := modelServer(t, "qwen3-coder")
	gateway := modelServer(t, "gpt-oss-120b", "llama-4")

	labID, err := application.AddEndpoint("Lab Box", lab, "")
	if err != nil || labID != "openai-compatible/lab-box" {
		t.Fatalf("add lab: %q %v", labID, err)
	}
	gatewayID, err := application.AddEndpoint("gateway", gateway, "sk-test")
	if err != nil {
		t.Fatalf("add gateway: %v", err)
	}
	for _, name := range []string{"lab box", "gateway"} {
		if _, _, err := application.RefreshEndpointModels(context.Background(), name); err != nil {
			t.Fatalf("refresh %s: %v", name, err)
		}
	}

	endpoints := application.Endpoints()
	if len(endpoints) != 2 || endpoints[0].ID != gatewayID || endpoints[1].ID != labID {
		t.Fatalf("endpoints = %+v", endpoints)
	}
	if !endpoints[0].HasKey || endpoints[1].HasKey {
		t.Fatalf("only the gateway has a key: %+v", endpoints)
	}
	if len(endpoints[0].Models) != 2 || endpoints[1].Models[0] != "qwen3-coder" {
		t.Fatalf("models = %+v", endpoints)
	}

	application.mu.Lock()
	gotLab := application.guessProvider("qwen3-coder")
	gotGateway := application.guessProvider("llama-4")
	application.mu.Unlock()
	if gotLab != labID || gotGateway != gatewayID {
		t.Fatalf("models should resolve to their own endpoint: %q %q", gotLab, gotGateway)
	}

	if _, err := application.RemoveEndpoint("lab-box"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := application.Endpoints(); len(got) != 1 || got[0].ID != gatewayID {
		t.Fatalf("after remove = %+v", got)
	}
}

// With several named endpoints and an unknown model, the same endpoint is
// chosen every time.
func TestGuessProviderIsDeterministicAcrossEndpoints(t *testing.T) {
	application := newTestApp(t)
	for _, name := range []string{"zeta", "alpha", "mid"} {
		if _, err := application.AddEndpoint(name, "https://"+name+".example/v1", ""); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 20; i++ {
		application.mu.Lock()
		got := application.guessProvider("some-unknown-model")
		application.mu.Unlock()
		if got != "openai-compatible/alpha" {
			t.Fatalf("run %d chose %q", i, got)
		}
	}
}
