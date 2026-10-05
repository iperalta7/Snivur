package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
	h := newControllerServer(NewStore(nil), NewRegistry(nil, HealthThresholds{}), nil, testKey, testControllerKey)
	routes := []struct{ method, path string }{
		{http.MethodGet, "/health"},
		{http.MethodPost, "/servers"},
		{http.MethodGet, "/servers"},
		{http.MethodGet, "/servers/abc"},
		{http.MethodPost, "/servers/abc/stop"},
		{http.MethodGet, "/agents"},
		{http.MethodGet, "/agents/abc"},
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
	h := withControllerKey(newControllerServer(NewStore(nil), NewRegistry(nil, HealthThresholds{}), nil, testKey, testControllerKey))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestHeartbeatRequiresAgentKeyNotControllerKey(t *testing.T) {
	h := newControllerServer(NewStore(nil), NewRegistry(nil, HealthThresholds{}), nil, testKey, testControllerKey)
	body := `{"agent_id":"a","address":"http://a:8081"}`
	for _, tc := range []struct {
		name, key string
		want      int
	}{
		{"controller key rejected", testControllerKey, http.StatusUnauthorized},
		{"agent key accepted", testKey, http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/agents/heartbeat", strings.NewReader(body))
			req.Header.Set("X-API-Key", tc.key)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}
