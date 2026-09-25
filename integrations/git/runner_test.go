package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH; skipping")
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)

	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	return dir
}

func writeBlob(t *testing.T, repo, content string) string {
	t.Helper()
	cmd := exec.Command("git", "hash-object", "-w", "--stdin")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("hash-object: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func writeAndCommit(t *testing.T, repo string) string {
	t.Helper()
	blob := writeBlob(t, repo, "initial content")
	tree := runGit(t, repo, "write-tree")
	commit := runGit(t, repo, "commit-tree", tree, "-m", "test commit")
	runGit(t, repo, "update-ref", "refs/heads/main", commit)
	return blob
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestNewRunner_RejectsSubdirectoryOfRepo(t *testing.T) {
	repo := initRepo(t)
	writeAndCommit(t, repo)

	sub := filepath.Join(repo, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, err := NewRunner(sub); err == nil {
		t.Fatal("expected NewRunner to reject a subdirectory of a repo as not being the root")
	}
}

func TestNewRunner_RejectsNonRepoPath(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewRunner(dir); err == nil {
		t.Fatal("expected NewRunner to reject a non-repository path")
	}
}

func TestNewRunner_AcceptsActualRoot(t *testing.T) {
	repo := initRepo(t)
	if _, err := NewRunner(repo); err != nil {
		t.Fatalf("expected NewRunner to accept the actual repo root, got: %v", err)
	}
}

func TestRun_HooksPathBlocksHostileHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook shebang execution assumed on unix-like systems")
	}
	repo := initRepo(t)
	writeAndCommit(t, repo)

	marker := filepath.Join(repo, "hook-fired")
	hookPath := filepath.Join(repo, ".git", "hooks", "post-commit")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatalf("writing hook: %v", err)
	}

	os.Remove(marker)
	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "trigger hook")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if err := cmd.Run(); err != nil {
		t.Fatalf("baseline commit: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected baseline git to fire the hook, marker missing: %v", err)
	}
	os.Remove(marker)

	r, err := NewRunner(repo)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if _, err := r.Run(context.Background(), "commit", "--allow-empty", "-m", "should not fire hook"); err != nil {
		t.Fatalf("Runner commit: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("hook fired through the Runner; core.hooksPath=/dev/null did not hold")
	}
}

func TestRun_FsmonitorDisabledBlocksHostileExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell shim assumed on unix-like systems")
	}
	repo := initRepo(t)
	writeAndCommit(t, repo)

	marker := filepath.Join(repo, "fsmonitor-fired")
	script := filepath.Join(repo, "fake-fsmonitor.sh")
	body := "#!/bin/sh\ntouch " + marker + "\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("writing fsmonitor shim: %v", err)
	}
	runGit(t, repo, "config", "core.fsmonitor", script)

	os.Remove(marker)
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	_ = cmd.Run()
	if _, err := os.Stat(marker); err != nil {
		t.Skipf("baseline git did not invoke core.fsmonitor on this git version; test not applicable: %v", err)
	}
	os.Remove(marker)

	r, err := NewRunner(repo)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	_, _ = r.Run(context.Background(), "status", "--porcelain")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("fsmonitor executable fired through the Runner; core.fsmonitor=false did not hold")
	}
}

func TestRun_NoReplaceObjectsIgnoresReplacementRefs(t *testing.T) {
	repo := initRepo(t)
	original := writeBlob(t, repo, "original content")
	replacement := writeBlob(t, repo, "replacement content")
	runGit(t, repo, "replace", original, replacement)

	plainOut := runGit(t, repo, "cat-file", "-p", original)
	if plainOut != "replacement content" {
		t.Fatalf("expected baseline git to resolve the replacement ref, got %q", plainOut)
	}

	r, err := NewRunner(repo)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	result, err := r.Run(context.Background(), "cat-file", "-p", original)
	if err != nil {
		t.Fatalf("Runner cat-file: %v", err)
	}
	got := strings.TrimSpace(result.Stdout)
	if got != "original content" {
		t.Fatalf("Runner honored the replacement ref: got %q want %q", got, "original content")
	}
}

func TestRun_RejectsRepositoryEscapeArguments(t *testing.T) {
	repo := initRepo(t)
	other := initRepo(t)

	r, err := NewRunner(repo)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	escapeAttempts := [][]string{
		{"-C", other, "status"},
		{"--git-dir=" + filepath.Join(other, ".git"), "status"},
		{"--work-tree=" + other, "status"},
		{"-c", "core.hooksPath=/tmp", "status"},
		{"--config", "core.hooksPath=/tmp", "status"},
		{"--replace-objects", "status"},
	}
	for _, args := range escapeAttempts {
		if _, err := r.Run(context.Background(), args...); err == nil {
			t.Fatalf("expected Run to reject escape attempt %v, got no error", args)
		}
	}
}

func TestSanitizedEnv_DoesNotInheritAmbientEnvironment(t *testing.T) {
	t.Setenv("GIT_RUNNER_TEST_CANARY", "should-not-leak")

	env := sanitizedEnv()
	for _, e := range env {
		if strings.Contains(e, "GIT_RUNNER_TEST_CANARY") {
			t.Fatalf("ambient environment leaked into sanitizedEnv: %s", e)
		}
	}
	if len(env) >= len(os.Environ()) {
		t.Fatalf("sanitizedEnv looks like it inherited the ambient environment (len %d >= os.Environ len %d)",
			len(env), len(os.Environ()))
	}
}

func TestRun_CancellationTerminatesDirectSubprocess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell shim assumed on unix-like systems")
	}
	repo := initRepo(t)

	shim := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "rev-parse" ]; then
    echo ".git"
    exit 0
  fi
done
sleep 10
`
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatalf("writing git shim: %v", err)
	}

	r, err := newRunnerWithGitPath(repo, shim)
	if err != nil {
		t.Fatalf("newRunnerWithGitPath: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = r.Run(ctx, "status")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected Run to return an error on context cancellation")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Run did not return promptly on cancellation: took %v", elapsed)
	}
}
