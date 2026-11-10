package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestOllamaLoadedCapacity(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/ps" || r.Header.Get("Authorization") != "Bearer local-token" {
			t.Error("runtime request lost endpoint/authentication binding")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"other","context_length":131072},{"name":"qwen3-coder","context_length":32768}]}`))
	}))
	defer srv.Close()
	cred := Credential{BaseURL: srv.URL + "/v1", APIKey: "local-token"}
	got, err := DiscoverRuntimeCapacity(t.Context(), srv.Client(), cred, "ollama", "qwen3-coder")
	if err != nil || got.ContextWindow != 32768 {
		t.Fatalf("actual loaded capacity: %+v %v", got, err)
	}
	if _, err = DiscoverRuntimeCapacity(t.Context(), srv.Client(), cred, "ollama", "not-loaded"); err == nil {
		t.Fatal("borrowed another model's allocation")
	}
	before := calls.Load()
	if _, err = DiscoverRuntimeCapacity(t.Context(), srv.Client(), cred, "generic", "qwen3-coder"); err == nil || calls.Load() != before {
		t.Fatal("generic endpoint probed as Ollama")
	}
	cred.BaseURL = srv.URL + "/proxy/v1"
	if _, err = DiscoverRuntimeCapacity(t.Context(), srv.Client(), cred, "ollama", "qwen3-coder"); err == nil || calls.Load() != before {
		t.Fatal("unsupported proxy path was guessed")
	}
}
func TestOllamaCapacityDoesNotRedirectCredentials(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1); _, _ = w.Write([]byte(`{"models":[]}`)) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/api/ps", http.StatusFound)
	}))
	defer srv.Close()
	_, err := DiscoverRuntimeCapacity(context.Background(), srv.Client(), Credential{BaseURL: srv.URL + "/v1", APIKey: "secret"}, "ollama", "coder")
	if err == nil || leaked.Load() != 0 {
		t.Fatal("discovery followed a cross-origin redirect")
	}
}
