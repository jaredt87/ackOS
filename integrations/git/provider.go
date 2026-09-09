// Package git provides a domain-specific Git resource provider for ackOS.
// Git semantics stay entirely inside this package; the kernel only sees the
// generic Executor, Verifier, and RecoveryObserver contracts.
package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jaredt87/ackOS/kernel"
)

// Target identifies one text file in one local Git working tree. Subject is an
// opaque, stable identity chosen by the caller; it is never interpreted by the
// kernel.
type Target struct {
	Repository string
	Path       string
	Subject    string
}

func NewTarget(repository, path, subject string) (Target, error) {
	if repository == "" || path == "" || subject == "" {
		return Target{}, fmt.Errorf("repository, path, and subject are required")
	}
	absRepo, err := filepath.Abs(repository)
	if err != nil {
		return Target{}, fmt.Errorf("resolve repository: %w", err)
	}
	info, err := os.Stat(absRepo)
	if err != nil {
		return Target{}, fmt.Errorf("stat repository: %w", err)
	}
	if !info.IsDir() {
		return Target{}, fmt.Errorf("repository is not a directory")
	}
	cleanPath := filepath.Clean(path)
	if filepath.IsAbs(cleanPath) || cleanPath == "." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) || cleanPath == ".." {
		return Target{}, fmt.Errorf("path must be repository-relative")
	}
	target := Target{Repository: absRepo, Path: cleanPath, Subject: subject}
	if err := validateNoSymlinks(target); err != nil {
		return Target{}, err
	}
	return target, nil
}

// Observer obtains fresh state directly from the Git working tree.
type Observer struct {
	Target Target
}

func (o Observer) Observe(ctx context.Context, _ string) (kernel.Observation, error) {
	if err := ctx.Err(); err != nil {
		return kernel.Observation{}, err
	}
	if err := validateNoSymlinks(o.Target); err != nil {
		return kernel.Observation{}, err
	}
	content, err := o.read(ctx)
	if err != nil {
		return kernel.Observation{}, err
	}
	return kernel.NewObservation(o.Target.Subject, content, 0, time.Now().UTC())
}

// Executor performs one exact file-content transition and records the
// execution attempt in the resulting Git commit. It re-reads the target at
// the mutation boundary, rejects symlinked targets, requires a clean
// worktree, verifies the target is tracked, and verifies that the resulting
// commit contains exactly the authorized target diff.
type Executor struct {
	Target Target
}

func (e Executor) Execute(ctx context.Context, t kernel.Transition, authority kernel.Authority) kernel.ExecutionResult {
	if authority.ExecutionID == "" {
		return kernel.ExecutionResult{Message: "execution authority ID is required"}
	}
	if t.Subject != e.Target.Subject {
		return kernel.ExecutionResult{Message: fmt.Sprintf("git subject mismatch: got %q, want %q", t.Subject, e.Target.Subject)}
	}
	if err := ctx.Err(); err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
	}
	if err := validateNoSymlinks(e.Target); err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
	}
	if err := e.requireTracked(ctx); err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
	}

	before, err := e.read(ctx)
	if err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
	}
	if before != t.Before {
		return kernel.ExecutionResult{Message: "git file changed before execution"}
	}
	status, err := e.git(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("read git status: %v", err)}
	}
	if status != "" {
		return kernel.ExecutionResult{Message: "git worktree is not clean"}
	}
	head, err := e.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("read git HEAD: %v", err)}
	}

	// Revalidate immediately before the side effect. This closes the window
	// between the initial observation/status checks and the actual write for
	// ordinary out-of-band mutations; filesystem-level concurrency is still a
	// provider-specific concern and is intentionally not hidden by the kernel.
	if err := validateNoSymlinks(e.Target); err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
	}
	if err := e.requireTracked(ctx); err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
	}
	current, err := e.read(ctx)
	if err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
	}
	if current != t.Before {
		return kernel.ExecutionResult{Message: "git file changed at mutation boundary"}
	}
	if err := os.WriteFile(filepath.Join(e.Target.Repository, e.Target.Path), []byte(t.After), 0o644); err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("write git file: %v", err)}
	}
	if _, err := e.git(ctx, "add", "--", e.Target.Path); err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("git add: %v", err)}
	}
	cachedPaths, err := e.git(ctx, "diff", "--cached", "--name-only")
	if err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("inspect staged Git diff: %v", err)}
	}
	if strings.TrimSpace(cachedPaths) != e.Target.Path {
		return kernel.ExecutionResult{Message: "staged Git diff contains an unauthorized path"}
	}
	cachedBefore, err := e.git(ctx, "show", "HEAD:"+e.Target.Path)
	if err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("read staged Git parent: %v", err)}
	}
	if cachedBefore != t.Before {
		return kernel.ExecutionResult{Message: "staged Git parent does not match authorized state"}
	}
	cachedAfter, err := e.git(ctx, "show", ":"+e.Target.Path)
	if err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("read staged Git target: %v", err)}
	}
	if cachedAfter != t.After {
		return kernel.ExecutionResult{Message: "staged Git target does not match authorized state"}
	}
	message := "ackOS: execute " + authority.ExecutionID
	if _, err := e.git(ctx, "commit", "--no-verify", "-m", message); err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("git commit: %v", err)}
	}
	if err := verifyCommit(e, ctx, head, t, authority.ExecutionID); err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
	}
	return kernel.ExecutionResult{Success: true, Message: "git file transitioned and committed"}
}

// Verifier independently reads the working tree and Git history. It does not
// share executor state; its evidence is bound to the authority execution ID
// and the exact target diff in the resulting commit.
type Verifier struct {
	Target Target
}

func (v Verifier) Verify(ctx context.Context, t kernel.Transition, authority kernel.Authority) (kernel.Observation, error) {
	if authority.ExecutionID == "" {
		return kernel.Observation{}, fmt.Errorf("execution authority ID is required")
	}
	if t.Subject != v.Target.Subject {
		return kernel.Observation{}, fmt.Errorf("git subject mismatch: got %q, want %q", t.Subject, v.Target.Subject)
	}
	if err := ctx.Err(); err != nil {
		return kernel.Observation{}, err
	}
	if err := validateNoSymlinks(v.Target); err != nil {
		return kernel.Observation{}, err
	}
	if err := v.requireTracked(ctx); err != nil {
		return kernel.Observation{}, err
	}
	content, err := v.read(ctx)
	if err != nil {
		return kernel.Observation{}, err
	}
	if content != t.After {
		return kernel.Observation{}, fmt.Errorf("git file state mismatch")
	}
	if err := verifyLatestCommit(v, ctx, t, authority.ExecutionID); err != nil {
		return kernel.Observation{}, err
	}
	return kernel.NewObservation(v.Target.Subject, content, 0, time.Now().UTC())
}

// RecoveryObserver obtains fresh state from the Git working tree after a
// failed execution. The subject is checked against the configured target.
type RecoveryObserver struct {
	Target Target
}

func (o RecoveryObserver) Observe(ctx context.Context, subject string) (kernel.Observation, error) {
	if subject != o.Target.Subject {
		return kernel.Observation{}, fmt.Errorf("git recovery subject mismatch: got %q, want %q", subject, o.Target.Subject)
	}
	return (Observer{Target: o.Target}).Observe(ctx, subject)
}

func (o Observer) read(ctx context.Context) (string, error) {
	return readFile(ctx, o.Target)
}

func (e Executor) read(ctx context.Context) (string, error) {
	return readFile(ctx, e.Target)
}

func (v Verifier) read(ctx context.Context) (string, error) {
	return readFile(ctx, v.Target)
}

func readFile(ctx context.Context, target Target) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path := filepath.Join(target.Repository, target.Path)
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read git file: %w", err)
	}
	return string(content), nil
}

func validateNoSymlinks(target Target) error {
	if target.Repository == "" || target.Path == "" {
		return fmt.Errorf("git target is incomplete")
	}
	root, err := filepath.EvalSymlinks(target.Repository)
	if err != nil {
		return fmt.Errorf("resolve git repository: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve git repository path: %w", err)
	}
	cleanPath := filepath.Clean(target.Path)
	if filepath.IsAbs(cleanPath) || cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return fmt.Errorf("git target path escapes repository")
	}
	current := root
	for _, part := range strings.Split(cleanPath, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect git target path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("git target path contains a symlink: %s", current)
		}
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, cleanPath))
	if err != nil {
		return fmt.Errorf("resolve git target: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("resolve git target path: %w", err)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("git target path escapes repository")
	}
	return nil
}

func verifyCommit(e Executor, ctx context.Context, parent string, t kernel.Transition, executionID string) error {
	return verifyCommitAt(e.Target, func(args ...string) (string, error) { return e.git(ctx, args...) }, parent, t, executionID)
}

func verifyLatestCommit(v Verifier, ctx context.Context, t kernel.Transition, executionID string) error {
	return verifyCommitAt(v.Target, func(args ...string) (string, error) { return v.git(ctx, args...) }, "", t, executionID)
}

func verifyCommitAt(target Target, git func(...string) (string, error), expectedParent string, t kernel.Transition, executionID string) error {
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("read committed Git HEAD: %w", err)
	}
	parents, err := git("rev-list", "--parents", "-n", "1", head)
	if err != nil {
		return fmt.Errorf("read committed Git parents: %w", err)
	}
	fields := strings.Fields(parents)
	if len(fields) != 2 || fields[0] != head {
		return fmt.Errorf("authorized Git execution did not produce one parent commit")
	}
	if expectedParent != "" && fields[1] != expectedParent {
		return fmt.Errorf("authorized Git execution parent changed unexpectedly")
	}
	message, err := git("log", "-1", "--format=%B")
	if err != nil {
		return fmt.Errorf("read Git commit message: %w", err)
	}
	if strings.TrimSpace(message) != "ackOS: execute "+executionID {
		return fmt.Errorf("git execution marker mismatch")
	}
	paths, err := git("diff-tree", "--no-commit-id", "--name-only", "-r", head)
	if err != nil {
		return fmt.Errorf("inspect committed Git diff: %w", err)
	}
	if strings.TrimSpace(paths) != target.Path {
		return fmt.Errorf("committed Git diff contains an unauthorized path")
	}
	before, err := git("show", head+"^:"+target.Path)
	if err != nil {
		return fmt.Errorf("read committed Git parent state: %w", err)
	}
	if before != t.Before {
		return fmt.Errorf("committed Git parent does not match authorized state")
	}
	after, err := git("show", head+":"+target.Path)
	if err != nil {
		return fmt.Errorf("read committed Git target state: %w", err)
	}
	if after != t.After {
		return fmt.Errorf("committed Git target does not match authorized state")
	}
	return nil
}

func (e Executor) requireTracked(ctx context.Context) error {
	tracked, err := e.git(ctx, "ls-files", "--error-unmatch", "--", e.Target.Path)
	if err != nil {
		return fmt.Errorf("git target is not tracked: %v", err)
	}
	if strings.TrimSpace(tracked) != e.Target.Path {
		return fmt.Errorf("git target is not tracked")
	}
	return nil
}

func (v Verifier) requireTracked(ctx context.Context) error {
	tracked, err := v.git(ctx, "ls-files", "--error-unmatch", "--", v.Target.Path)
	if err != nil {
		return fmt.Errorf("git target is not tracked: %v", err)
	}
	if strings.TrimSpace(tracked) != v.Target.Path {
		return fmt.Errorf("git target is not tracked")
	}
	return nil
}

func (e Executor) git(ctx context.Context, args ...string) (string, error) {
	return runGit(ctx, e.Target.Repository, args...)
}

func (v Verifier) git(ctx context.Context, args ...string) (string, error) {
	return runGit(ctx, v.Target.Repository, args...)
}

func runGit(ctx context.Context, repository string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repository
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	// `git show <rev>:<path>` returns blob bytes. Preserve them exactly so
	// authorized states such as "updated\n" remain distinguishable from
	// "updated". Other commands in this provider consume metadata, where
	// trimming command framing whitespace is appropriate.
	if len(args) > 0 && args[0] == "show" {
		return string(output), nil
	}
	return strings.TrimSpace(string(output)), nil
}
