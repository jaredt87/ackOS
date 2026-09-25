// Package git provides an isolated Runner for executing git subprocesses.
//
// This package intentionally does NOT provide a Provider, a plugin
// interface, or working-tree operations. It proves one thing: that git
// subprocess execution can be sandboxed against the failure modes PR #13
// surfaced. PR #16 decides what shape the real Provider needs on top of
// this — building that shape now would be guessing at a caller that
// doesn't exist yet.
//
// Path arguments are intentionally not interpreted by Runner. Run accepts
// git command arguments, not typed path arguments, so pathspec-magic
// safety (leading `:`, `!`, `*` in a path being reinterpreted by git)
// belongs at the eventual Git provider call sites, where the caller knows
// a given argument is a literal path. See PR #15 notes for the reasoning.
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

// ErrUnsafeArgument is returned when a caller-supplied git argument would
// override one of the Runner's security-boundary settings.
var ErrUnsafeArgument = errors.New("git: argument would override a runner security boundary")

// ErrNotRepoRoot is returned by NewRunner when the supplied path does not
// resolve to a git repository root — e.g. a subdirectory of a repo, an
// unrelated directory, or a path git can't identify at all.
var ErrNotRepoRoot = errors.New("git: path is not a git repository root")

var unsafeArgPrefixes = []string{
	"-C",
	"--git-dir",
	"--work-tree",
	"-c",
	"--config",
	"--no-replace-objects",
	"--replace-objects",
}

// Result is the outcome of one Runner.Run call.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner executes git subprocesses against exactly one repository, inside a
// clean-room environment. It supports plumbing-style, working-tree-free
// execution (hash-object, write-tree, commit-tree, update-ref, and
// similar) — the same execution model the existing Git provider already
// uses.
type Runner struct {
	repoPath string
	gitPath  string
	baseArgs []string
	env      []string
}

// NewRunner resolves the git executable, validates that repoPath is
// actually a git repository root (not a subdirectory, not merely a path
// that exists), and anchors a Runner to it.
func NewRunner(repoPath string) (*Runner, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git: resolving git executable: %w", err)
	}
	return newRunnerWithGitPath(repoPath, gitPath)
}

func newRunnerWithGitPath(repoPath, gitPath string) (*Runner, error) {
	resolved, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		return nil, fmt.Errorf("git: resolving repo path: %w", err)
	}
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return nil, fmt.Errorf("git: making repo path absolute: %w", err)
	}

	if err := validateRepoRoot(gitPath, abs); err != nil {
		return nil, err
	}

	return &Runner{
		repoPath: abs,
		gitPath:  gitPath,
		baseArgs: []string{
			"--no-replace-objects",
			"-C", abs,
			"-c", "core.hooksPath=/dev/null",
			"-c", "core.fsmonitor=false",
		},
		env: sanitizedEnv(),
	}, nil
}

func validateRepoRoot(gitPath, abs string) error {
	cmd := exec.Command(gitPath, "-C", abs, "rev-parse", "--git-dir")
	cmd.Env = sanitizedEnv()
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%w: %s (%v)", ErrNotRepoRoot, abs, err)
	}

	gitDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(abs, gitDir)
	}
	gitDirAbs, err := filepath.Abs(gitDir)
	if err != nil {
		return fmt.Errorf("git: resolving git-dir: %w", err)
	}

	bareForm := abs
	nonBareForm := filepath.Join(abs, ".git")
	if gitDirAbs != bareForm && gitDirAbs != nonBareForm {
		return fmt.Errorf("%w: %s resolves to git-dir %s, not its own root", ErrNotRepoRoot, abs, gitDirAbs)
	}
	return nil
}

func sanitizedEnv() []string {
	return []string{
		"PATH=/usr/bin:/bin",
		"HOME=/nonexistent",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"TZ=UTC",
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0",
	}
}

func (r *Runner) Run(ctx context.Context, args ...string) (Result, error) {
	if err := checkSafeArgs(args); err != nil {
		return Result{}, err
	}

	fullArgs := make([]string, 0, len(r.baseArgs)+len(args))
	fullArgs = append(fullArgs, r.baseArgs...)
	fullArgs = append(fullArgs, args...)

	cmd := exec.CommandContext(ctx, r.gitPath, fullArgs...)
	cmd.Env = r.env
	cmd.Dir = r.repoPath

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	result := Result{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if ctx.Err() != nil {
		return result, ctx.Err()
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), runErr, result.Stderr)
	}
	if runErr != nil {
		return result, fmt.Errorf("git %s: %w", strings.Join(args, " "), runErr)
	}

	return result, nil
}

func checkSafeArgs(args []string) error {
	for _, a := range args {
		for _, prefix := range unsafeArgPrefixes {
			if a == prefix || strings.HasPrefix(a, prefix+"=") {
				return fmt.Errorf("%w: %q", ErrUnsafeArgument, a)
			}
		}
	}
	return nil
}
