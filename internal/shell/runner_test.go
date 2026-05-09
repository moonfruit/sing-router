package shell

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestRunnerExecutesScriptWithEnv(t *testing.T) {
	r := NewRunner(RunnerConfig{
		Bash: "/bin/bash",
		Env:  map[string]string{"FOO": "bar", "BAZ": "qux"},
	})
	var stderr strings.Builder
	err := r.Run(context.Background(), "echo $FOO-$BAZ 1>&2", &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(stderr.String(), "bar-qux") {
		t.Fatalf("stderr missing expected line: %q", stderr.String())
	}
}

func TestRunnerRequiredEnvAbsentFails(t *testing.T) {
	r := NewRunner(RunnerConfig{Bash: "/bin/bash"})
	var stderr strings.Builder
	script := `set -eu; : "${MUST_EXIST:?MUST_EXIST not set}"; echo ok`
	err := r.Run(context.Background(), script, &stderr)
	if err == nil {
		t.Fatal("expected error from missing env")
	}
	var rerr *Error
	if !errors.As(err, &rerr) {
		t.Fatalf("err type %T", err)
	}
	if rerr.ExitCode == 0 {
		t.Fatal("exit code should be non-zero")
	}
}

func TestRunnerStreamsStderrLineByLine(t *testing.T) {
	r := NewRunner(RunnerConfig{Bash: "/bin/bash"})
	var mu sync.Mutex
	lines := []string{}
	r.OnStderr = func(line string) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, line)
	}
	var stderr strings.Builder
	script := "echo line1 1>&2; echo line2 1>&2; echo line3 1>&2"
	if err := r.Run(context.Background(), script, &stderr); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 3 || lines[0] != "line1" {
		t.Fatalf("stderr lines: %v", lines)
	}
}
