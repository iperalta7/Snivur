package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const testControllerKey = "controller-key"

// withControllerKey adds the controller API key to requests that carry no
// X-API-Key, so handler tests can focus on behaviour rather than auth.
func withControllerKey(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") == "" {
			r.Header.Set("X-API-Key", testControllerKey)
		}
		h.ServeHTTP(w, r)
	})
}

func TestControllerRequiresAPIKey(t *testing.T) {
	h := newControllerServer(Config{AgentURL: "http://unused", AgentAPIKey: testKey, ControllerAPIKey: testControllerKey}, http.DefaultClient)
	routes := []struct{ method, path string }{
		{http.MethodGet, "/health"},
		{http.MethodPost, "/servers"},
	}
	keys := []struct {
		name, key string
		want      int
	}{
		{"missing", "", http.StatusUnauthorized},
		{"wrong", "nope", http.StatusUnauthorized},
		{"agent key is not a controller key", testKey, http.StatusUnauthorized},
	}
	for _, rt := range routes {
		for _, k := range keys {
			t.Run(rt.method+" "+rt.path+" "+k.name, func(t *testing.T) {
				req := httptest.NewRequest(rt.method, rt.path, nil)
				if k.key != "" {
					req.Header.Set("X-API-Key", k.key)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if w.Code != k.want {
					t.Fatalf("status = %d, want %d", w.Code, k.want)
				}
			})
		}
	}
}

func TestControllerHealth(t *testing.T) {
	h := withControllerKey(newControllerServer(Config{AgentAPIKey: testKey, ControllerAPIKey: testControllerKey}, http.DefaultClient))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}
