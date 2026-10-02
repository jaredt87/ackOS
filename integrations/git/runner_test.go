package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func repo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil { t.Skip("git unavailable") }
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil { t.Fatalf("git init: %v: %s", err, out) }
	return dir
}

func TestRunnerRejectsNonRepository(t *testing.T) {
	if _, err := NewRunner(t.TempDir()); err == nil { t.Fatal("expected non-repository rejection") }
}

func TestRunnerAnchorsAtRepositoryRoot(t *testing.T) {
	root := repo(t)
	r, err := NewRunner(root)
	if err != nil { t.Fatal(err) }
	sub := filepath.Join(root, "child")
	if err := os.Mkdir(sub, 0755); err != nil { t.Fatal(err) }
	if _, err := NewRunner(sub); err == nil { t.Fatal("accepted repository subdirectory") }
	if _, err := r.Run(context.Background(), "rev-parse", "--show-toplevel"); err != nil { t.Fatal(err) }
}

func TestRunnerRejectsUnsupportedAndOverrideArguments(t *testing.T) {
	r, err := NewRunner(repo(t))
	if err != nil { t.Fatal(err) }
	for _, args := range [][]string{
		{}, {"push", "origin", "main"}, {"-C", "/tmp", "status"},
		{"rev-parse", "--git-dir=/tmp"},
		{"rev-parse", "--work-tree=/tmp"},
		{"rev-parse", "--config-env=x=y"},
	} {
		if _, err := r.Run(context.Background(), args...); err == nil { t.Errorf("accepted %q", args) }
	}
}

func TestRunnerUsesCleanEnvironmentAndCancellation(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/tmp/hostile-config")
	env := cleanEnv()
	for _, item := range env {
		if item == "GIT_CONFIG_GLOBAL=/tmp/hostile-config" { t.Fatal("ambient git config leaked") }
	}
	r, err := NewRunner(repo(t))
	if err != nil { t.Fatal(err) }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Run(ctx, "rev-parse", "--show-toplevel"); err == nil { t.Fatal("expected cancellation") }
}
