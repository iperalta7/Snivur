package main

import (
	"strings"
	"testing"
)

// ADR 0004 Consequences: the agent warns at startup when it advertises a
// localhost address to a non-loopback controller.
func TestAdvertiseWarning(t *testing.T) {
	cases := []struct {
		name       string
		controller string
		advertise  string
		warn       bool
	}{
		{"controller unset", "", "", false},
		{"controller unset, advertise set", "", "http://10.0.0.2:8000", false},
		{"localhost", "http://localhost:8080", "", false},
		{"LOCALHOST uppercase", "http://LOCALHOST:8080", "", false},
		{"127.0.0.1", "http://127.0.0.1:8080", "", false},
		{"127.0.0.2 (loopback range)", "http://127.0.0.2:8080", "", false},
		{"::1", "http://[::1]:8080", "", false},
		{"remote host", "http://ctrl.example.com:8080", "", true},
		{"remote IP", "http://10.0.0.5:8080", "", true},
		{"remote https no port", "https://ctrl.example.com", "", true},
		{"remote host, advertise set", "http://ctrl.example.com:8080", "http://10.0.0.2:8000", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{
				"SNIVUR_CONTROLLER_URL":      tc.controller,
				"SNIVUR_AGENT_ADVERTISE_URL": tc.advertise,
			}
			got := advertiseWarning(func(k string) string { return env[k] })
			if (got != "") != tc.warn {
				t.Fatalf("advertiseWarning = %q, want warn=%v", got, tc.warn)
			}
			if tc.warn && (!strings.Contains(got, "SNIVUR_AGENT_ADVERTISE_URL") || !strings.Contains(got, tc.controller)) {
				t.Errorf("warning %q should name SNIVUR_AGENT_ADVERTISE_URL and the controller URL", got)
			}
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost":             true,
		"LocalHost":             true,
		"127.0.0.1":             true,
		"127.1.2.3":             true,
		"::1":                   true,
		"":                      false,
		"0.0.0.0":               false,
		"10.0.0.1":              false,
		"example.com":           false,
		"localhost.example.com": false,
		"::":                    false,
	} {
		if got := isLoopbackHost(host); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}
