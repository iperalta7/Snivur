package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// AC5: run main() in a subprocess with SNIVUR_AGENT_API_KEY unset and assert
// it exits non-zero with a clear log line.
func TestMainExitsWithoutAPIKey(t *testing.T) {
	if os.Getenv("SNIVUR_TEST_RUN_MAIN") == "1" {
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainExitsWithoutAPIKey$")
	env := []string{"SNIVUR_TEST_RUN_MAIN=1", "SNIVUR_AGENT_ADDR=127.0.0.1:0"}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SNIVUR_") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() == 0 {
		t.Fatalf("expected non-zero exit, got err=%v output=%q", err, out)
	}
	if !strings.Contains(string(out), "SNIVUR_AGENT_API_KEY") {
		t.Errorf("log output %q should mention SNIVUR_AGENT_API_KEY", out)
	}
}
