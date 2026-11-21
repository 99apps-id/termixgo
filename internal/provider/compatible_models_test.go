package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListCompatibleModelsReadsBothShapes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"bad key"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen3"},{"id":"llama-4"},{"id":"qwen3"}],"models":[{"name":"gemma:2b"}]}`))
	}))
	defer server.Close()

	models, err := ListCompatibleModels(context.Background(), server.URL+"/v1/", "secret")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "gemma:2b,llama-4,qwen3" {
		t.Fatalf("models = %v", models)
	}
	if _, err := ListCompatibleModels(context.Background(), server.URL+"/v1", "wrong"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("a refused key should be reported, got %v", err)
	}
}

func TestSlugifyEndpointName(t *testing.T) {
	for in, want := range map[string]string{"My Lab  Server": "my-lab-server", "vLLM_01": "vllm_01", "  ": "", "a/b": "ab"} {
		if got := SlugifyEndpointName(in); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
}
