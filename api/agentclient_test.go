package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"snivur/v0/shared"
)

type recordedReq struct {
	method, path, rawPath, key, contentType string
	body                                    []byte
}

// stubAgent replies with a fixed status and body and records the request.
func stubAgent(t *testing.T, status int, body string) (*httptest.Server, *recordedReq) {
	t.Helper()
	rec := &recordedReq{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*rec = recordedReq{r.Method, r.URL.Path, r.URL.EscapedPath(), r.Header.Get("X-API-Key"), r.Header.Get("Content-Type"), b}
		if body != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func TestHTTPAgentClientLaunch(t *testing.T) {
	req := shared.LaunchRequest{ServerID: "sid", Name: "mc1", Game: "minecraft", Config: map[string]string{"image": "alpine"}}
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string // substring; "" means success
	}{
		{"200", 200, `{"server_id":"sid","container_id":"cid42"}`, ""},
		{"404", 404, `{"error":"not here"}`, "not here"},
		{"500 error body", 500, `{"error":"docker run: boom"}`, "docker run: boom"},
		{"500 plain body", 500, `upstream exploded`, "upstream exploded"},
		{"200 malformed", 200, `{"server_id":`, "decode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, rec := stubAgent(t, tt.status, tt.body)
			c := newHTTPAgentClient(testKey, srv.Client())
			resp, err := c.Launch(context.Background(), srv.URL, req)
			if rec.method != http.MethodPost || rec.path != "/launch" {
				t.Errorf("request = %s %s, want POST /launch", rec.method, rec.path)
			}
			if rec.key != testKey {
				t.Errorf("X-API-Key = %q", rec.key)
			}
			if rec.contentType != "application/json" {
				t.Errorf("Content-Type = %q", rec.contentType)
			}
			var sent shared.LaunchRequest
			if err := json.Unmarshal(rec.body, &sent); err != nil || sent.ServerID != "sid" || sent.Config["image"] != "alpine" {
				t.Errorf("sent body %s (%v)", rec.body, err)
			}
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if resp.ContainerID != "cid42" || resp.ServerID != "sid" {
					t.Errorf("resp = %+v", resp)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
			if tt.status != 200 && !strings.Contains(err.Error(), http.StatusText(tt.status)) && !strings.Contains(err.Error(), itoa(tt.status)) {
				t.Errorf("err %q does not mention status %d", err, tt.status)
			}
		})
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestHTTPAgentClientStop(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"204", 204, ``, ""},
		{"200", 200, `{}`, ""},
		{"404 already gone", 404, `{"error":"no container"}`, ""},
		{"500", 500, `{"error":"docker stop: boom"}`, "docker stop: boom"},
		{"401", 401, `{"error":"invalid API key"}`, "invalid API key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, rec := stubAgent(t, tt.status, tt.body)
			c := newHTTPAgentClient(testKey, srv.Client())
			err := c.Stop(context.Background(), srv.URL+"/", "sid1")
			if rec.method != http.MethodPost || rec.path != "/servers/sid1/stop" {
				t.Errorf("request = %s %s, want POST /servers/sid1/stop", rec.method, rec.path)
			}
			if rec.key != testKey {
				t.Errorf("X-API-Key = %q", rec.key)
			}
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestHTTPAgentClientStopEscapesID(t *testing.T) {
	srv, rec := stubAgent(t, 204, "")
	c := newHTTPAgentClient(testKey, srv.Client())
	if err := c.Stop(context.Background(), srv.URL, "a/../b"); err != nil {
		t.Fatal(err)
	}
	if rec.rawPath != "/servers/a%2F..%2Fb/stop" {
		t.Errorf("escaped path = %q", rec.rawPath)
	}
}

func TestHTTPAgentClientUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := newHTTPAgentClient(testKey, nil)
	if err := c.Stop(context.Background(), url, "x"); err == nil {
		t.Error("Stop on closed server: nil error")
	}
	if _, err := c.Launch(context.Background(), url, shared.LaunchRequest{}); err == nil {
		t.Error("Launch on closed server: nil error")
	}
}

func TestHTTPAgentClientHonorsContext(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := newHTTPAgentClient(testKey, srv.Client())
	if err := c.Stop(ctx, srv.URL, "x"); err == nil {
		t.Error("Stop with canceled ctx: nil error")
	}
}
