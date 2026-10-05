package shared

import (
	"regexp"
	"strings"
	"testing"
)

func TestNewID(t *testing.T) {
	hex32 := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := NewID()
		if !hex32.MatchString(id) {
			t.Fatalf("NewID() = %q, want 32 lowercase hex chars", id)
		}
		if seen[id] {
			t.Fatalf("NewID() produced duplicate %q", id)
		}
		seen[id] = true
	}
}

// AC3 (validation half): flag-like values must be rejected before docker.
func TestValidateLaunch(t *testing.T) {
	img := func(s string) map[string]string { return map[string]string{"image": s} }
	tests := []struct {
		name    string
		req     LaunchRequest
		wantErr bool
	}{
		{"valid simple", LaunchRequest{Name: "mc1", Config: img("itzg/minecraft-server")}, false},
		{"valid with tag and registry", LaunchRequest{Name: "a", Config: img("ghcr.io/foo/bar:1.2.3")}, false},
		{"valid digest", LaunchRequest{Name: "a", Config: img("alpine@sha256:abc")}, false},
		{"valid name with _.-", LaunchRequest{Name: "A9_b.c-d", Config: img("alpine")}, false},
		{"valid name 63 chars", LaunchRequest{Name: strings.Repeat("a", 63), Config: img("alpine")}, false},
		{"valid extra config keys ignored", LaunchRequest{Name: "a", Config: map[string]string{"image": "alpine", "x": "-y"}}, false},

		{"AC3 name --privileged", LaunchRequest{Name: "--privileged", Config: img("alpine")}, true},
		{"name leading dash", LaunchRequest{Name: "-a", Config: img("alpine")}, true},
		{"name leading underscore", LaunchRequest{Name: "_a", Config: img("alpine")}, true},
		{"name leading dot", LaunchRequest{Name: ".a", Config: img("alpine")}, true},
		{"name empty", LaunchRequest{Name: "", Config: img("alpine")}, true},
		{"name 64 chars", LaunchRequest{Name: strings.Repeat("a", 64), Config: img("alpine")}, true},
		{"name with space", LaunchRequest{Name: "a b", Config: img("alpine")}, true},
		{"name with slash", LaunchRequest{Name: "a/b", Config: img("alpine")}, true},
		{"name with equals", LaunchRequest{Name: "a=b", Config: img("alpine")}, true},
		{"name trailing newline", LaunchRequest{Name: "abc\n", Config: img("alpine")}, true},
		{"name unicode", LaunchRequest{Name: "é", Config: img("alpine")}, true},

		{"AC3 image -v/:/host", LaunchRequest{Name: "a", Config: img("-v/:/host")}, true},
		{"image --privileged", LaunchRequest{Name: "a", Config: img("--privileged")}, true},
		{"image empty", LaunchRequest{Name: "a", Config: img("")}, true},
		{"image missing key", LaunchRequest{Name: "a", Config: map[string]string{}}, true},
		{"image nil config", LaunchRequest{Name: "a"}, true},
		{"image with space", LaunchRequest{Name: "a", Config: img("alpine sh")}, true},
		{"image with tab", LaunchRequest{Name: "a", Config: img("alpine\tsh")}, true},
		{"image with newline", LaunchRequest{Name: "a", Config: img("alpine\n")}, true},
		{"image leading space", LaunchRequest{Name: "a", Config: img(" alpine")}, true},
		{"image unicode space", LaunchRequest{Name: "a", Config: img("alpine x")}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateLaunch(tt.req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateLaunch(%+v) err = %v, wantErr %v", tt.req, err, tt.wantErr)
			}
			if err != nil && err.Error() == "" {
				t.Fatal("error message should be descriptive, got empty")
			}
		})
	}
}
