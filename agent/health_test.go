package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentHealthRequiresKey(t *testing.T) {
	h := newAgentServer(Config{APIKey: "k"}, nil)
	for _, tc := range []struct {
		key  string
		want int
	}{{"", http.StatusUnauthorized}, {"wrong", http.StatusUnauthorized}, {"k", http.StatusOK}} {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		if tc.key != "" {
			req.Header.Set("X-API-Key", tc.key)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("key %q: status = %d, want %d", tc.key, w.Code, tc.want)
		}
	}
}
