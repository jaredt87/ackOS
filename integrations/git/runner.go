// Package git provides a narrowly scoped, repository-bound Git plumbing runner.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	ErrUnsafeArgument = errors.New("git: unsafe argument")
	ErrNotRepoRoot    = errors.New("git: path is not a git repository root")
)

type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type Runner struct {
	root   string
	gitDir string
	git    string
}

var commands = map[string]bool{
	"rev-parse":   true,
	"hash-object": true,
	"cat-file":   true,
	"write-tree":  true,
	"commit-tree": true,
	"update-ref":  true,
}

func NewRunner(root string) (*Runner, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git: locate executable: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(git, "-C", root, "rev-parse", "--show-toplevel", "--absolute-git-dir")
	cmd.Env = cleanEnv()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrNotRepoRoot, root, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		return nil, ErrNotRepoRoot
	}
	top, _ := filepath.EvalSymlinks(lines[0])
	if filepath.Clean(top) != filepath.Clean(root) {
		return nil, fmt.Errorf("%w: %s", ErrNotRepoRoot, root)
	}
	return &Runner{root: root, gitDir: lines[1], git: git}, nil
}

func cleanEnv() []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "TZ=UTC"}
}

// Run accepts only explicitly supported plumbing commands. Callers must pass
// structured, validated operands; this method never invokes a shell.
func (r *Runner) Run(ctx context.Context, args ...string) (Result, error) {
	if len(args) == 0 || !commands[args[0]] {
		return Result{}, fmt.Errorf("%w: unsupported command", ErrUnsafeArgument)
	}
	for _, a := range args {
		if a == "" || strings.HasPrefix(a, "--git-dir") || strings.HasPrefix(a, "--work-tree") ||
			a == "-C" || a == "-c" || strings.HasPrefix(a, "--config") ||
			strings.HasPrefix(a, "--exec-path") || strings.HasPrefix(a, "--replace-objects") ||
			a == "--no-replace-objects" {
			return Result{}, fmt.Errorf("%w: %q", ErrUnsafeArgument, a)
		}
	}
	full := []string{"--no-replace-objects", "-C", r.root,
		"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
		"-c", "commit.gpgSign=false", "--git-dir", r.gitDir}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, r.git, full...)
	cmd.Dir, cmd.Env = r.root, cleanEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err == nil {
		return res, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		res.ExitCode = exit.ExitCode()
		return res, fmt.Errorf("git %s: %w: %s", args[0], err, res.Stderr)
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	return res, fmt.Errorf("git %s: %w", args[0], err)
}
