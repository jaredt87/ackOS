package mcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitprovider "github.com/jaredt87/ackOS/integrations/git"
	"github.com/jaredt87/ackOS/kernel"
)

func TestControlWithGitProvider(t *testing.T) {
	dir := t.TempDir()
	gitCommand(t, dir, "init")
	gitCommand(t, dir, "config", "user.email", "ackos-test@example.invalid")
	gitCommand(t, dir, "config", "user.name", "ackOS test")
	path := filepath.Join(dir, "docs", "example.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, dir, "add", "--", "docs/example.md")
	gitCommand(t, dir, "commit", "-m", "initial")

	target, err := gitprovider.NewTarget(dir, "docs/example.md", "test-repo:docs/example.md")
	if err != nil {
		t.Fatal(err)
	}
	observer := gitprovider.Observer{Target: target}
	executor := gitprovider.Executor{Target: target}
	verifier := gitprovider.Verifier{Target: target}
	recovery := gitprovider.RecoveryObserver{Target: target}
	observation, err := observer.Observe(context.Background(), target.Subject)
	if err != nil {
		t.Fatal(err)
	}

	runtime := kernel.NewRuntime(observation.State, kernel.AllowPolicy{})
	server, err := NewServer(runtime, executor, verifier, recovery)
	if err != nil {
		t.Fatal(err)
	}
	_, response, err := server.control(context.Background(), nil, ControlRequest{
		Subject:        target.Subject,
		ObservedState:  observation.State,
		DesiredState:   "updated",
		AuthorityTTLMS: 60000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Committed || !response.Verified || response.Phase != kernel.PhaseCommitted {
		t.Fatalf("response = %+v", response)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "updated" {
		t.Fatalf("file = %q, want updated", content)
	}
	if marker := strings.TrimSpace(gitCommand(t, dir, "log", "-1", "--format=%B")); !strings.Contains(marker, response.Authority.ExecutionID) {
		t.Fatalf("commit marker = %q, want execution ID %q", marker, response.Authority.ExecutionID)
	}
}

func gitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
