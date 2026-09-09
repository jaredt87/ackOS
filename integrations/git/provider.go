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
	return Target{Repository: absRepo, Path: cleanPath, Subject: subject}, nil
}

// Observer obtains fresh state directly from the Git working tree.
type Observer struct {
	Target Target
}

func (o Observer) Observe(ctx context.Context, _ string) (kernel.Observation, error) {
	if err := ctx.Err(); err != nil {
		return kernel.Observation{}, err
	}
	content, err := o.read(ctx)
	if err != nil {
		return kernel.Observation{}, err
	}
	return kernel.NewObservation(o.Target.Subject, content, 0, time.Now().UTC())
}

// Executor performs one exact file-content transition and records the
// execution attempt in the resulting Git commit. It re-reads the target
// immediately before mutation, so caller-supplied state cannot authorize a
// stale or substituted resource.
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

	before, err := e.read(ctx)
	if err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
	}
	if before != t.Before {
		return kernel.ExecutionResult{Message: "git file changed before execution"}
	}
	status, err := e.git(ctx, "status", "--porcelain")
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

	if err := os.WriteFile(filepath.Join(e.Target.Repository, e.Target.Path), []byte(t.After), 0o644); err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("write git file: %v", err)}
	}
	if _, err := e.git(ctx, "add", "--", e.Target.Path); err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("git add: %v", err)}
	}
	message := "ackOS: execute " + authority.ExecutionID
	if _, err := e.git(ctx, "commit", "-m", message); err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("git commit: %v", err)}
	}
	parent, err := e.git(ctx, "rev-parse", "HEAD^")
	if err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("read committed parent: %v", err)}
	}
	if parent != head {
		return kernel.ExecutionResult{Message: "git execution parent changed unexpectedly"}
	}
	return kernel.ExecutionResult{Success: true, Message: "git file transitioned and committed"}
}

// Verifier independently reads the working tree and Git history. It does not
// share executor state; its evidence is bound to the authority execution ID
// through the commit produced by the authorized attempt.
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
	content, err := v.read(ctx)
	if err != nil {
		return kernel.Observation{}, err
	}
	if content != t.After {
		return kernel.Observation{}, fmt.Errorf("git file state mismatch")
	}
	message, err := v.git(ctx, "log", "-1", "--format=%B")
	if err != nil {
		return kernel.Observation{}, fmt.Errorf("read git commit: %w", err)
	}
	if strings.TrimSpace(message) != "ackOS: execute "+authority.ExecutionID {
		return kernel.Observation{}, fmt.Errorf("git execution marker mismatch")
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
	return strings.TrimSpace(string(output)), nil
}
