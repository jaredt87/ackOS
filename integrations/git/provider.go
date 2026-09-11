// Package git provides a domain-specific Git resource provider for ackOS.
// Git semantics stay entirely inside this package; the kernel only sees the
// generic Executor, Verifier, and RecoveryObserver contracts.
package git

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jaredt87/ackOS/kernel"
)

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
	if err := requireWorktreeRoot(context.Background(), target); err != nil {
		return Target{}, err
	}
	if err := validateNoSymlinks(target); err != nil {
		return Target{}, err
	}
	return target, nil
}

type Observer struct{ Target Target }

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

type Executor struct{ Target Target }

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
	if err := requireWorktreeRoot(ctx, e.Target); err != nil {
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
	if err := requireNoInProgressGitOperation(ctx, e.Target); err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
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
	if err := requireWorktreeRoot(ctx, e.Target); err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
	}
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
	if err := requireNoInProgressGitOperation(ctx, e.Target); err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
	}
	if filepath.Base(e.Target.Path) == ".gitattributes" {
		return kernel.ExecutionResult{Message: "git .gitattributes targets are not supported because the target can change its own filter environment"}
	}
	if err := os.WriteFile(filepath.Join(e.Target.Repository, e.Target.Path), []byte(t.After), 0o644); err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("write git file: %v", err)}
	}
	if _, err := e.git(ctx, "add", "--", e.Target.Path); err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("git add: %v", err)}
	}
	cachedPaths, err := e.git(ctx, "diff", "--cached", "--name-only", "-z")
	if err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("inspect staged Git diff: %v", err)}
	}
	if !exactNULPathList(cachedPaths, e.Target.Path) {
		return kernel.ExecutionResult{Message: "staged Git diff contains an unauthorized path"}
	}
	cachedBeforeHash, err := e.git(ctx, "rev-parse", "HEAD:"+e.Target.Path)
	if err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("read staged Git parent: %v", err)}
	}
	expectedBeforeHash, err := gitFilteredBlobHash(ctx, e.Target, t.Before)
	if err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("normalize authorized Git parent state: %v", err)}
	}
	if cachedBeforeHash != expectedBeforeHash {
		return kernel.ExecutionResult{Message: "staged Git parent does not match authorized state"}
	}
	cachedAfterHash, err := e.git(ctx, "rev-parse", ":"+e.Target.Path)
	if err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("read staged Git target: %v", err)}
	}
	expectedAfterHash, err := gitFilteredBlobHash(ctx, e.Target, t.After)
	if err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("normalize authorized Git target state: %v", err)}
	}
	if cachedAfterHash != expectedAfterHash {
		return kernel.ExecutionResult{Message: "staged Git target does not match authorized state"}
	}
	message := "ackOS: execute " + authority.ExecutionID
	if _, err := e.git(ctx, "-c", "core.hooksPath=/dev/null", "commit", "--no-verify", "-m", message); err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("git commit: %v", err)}
	}
	if err := verifyCommit(e, ctx, head, t, authority.ExecutionID); err != nil {
		return kernel.ExecutionResult{Message: err.Error()}
	}
	finalContent, err := e.read(ctx)
	if err != nil {
		return kernel.ExecutionResult{Message: fmt.Sprintf("re-read Git target after verification: %v", err)}
	}
	if finalContent != t.After {
		return kernel.ExecutionResult{Message: "git target changed after commit verification"}
	}
	return kernel.ExecutionResult{Success: true, Message: "git file transitioned and committed"}
}

type Verifier struct{ Target Target }

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
	if err := requireWorktreeRoot(ctx, v.Target); err != nil {
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

type RecoveryObserver struct{ Target Target }

func (o RecoveryObserver) Observe(ctx context.Context, subject string) (kernel.Observation, error) {
	if subject != o.Target.Subject {
		return kernel.Observation{}, fmt.Errorf("git recovery subject mismatch: got %q, want %q", subject, o.Target.Subject)
	}
	return (Observer{Target: o.Target}).Observe(ctx, subject)
}

func (o Observer) read(ctx context.Context) (string, error) { return readFile(ctx, o.Target) }
func (e Executor) read(ctx context.Context) (string, error) { return readFile(ctx, e.Target) }
func (v Verifier) read(ctx context.Context) (string, error) { return readFile(ctx, v.Target) }

func readFile(ctx context.Context, target Target) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path := filepath.Join(target.Repository, target.Path)
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("open git file: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return "", fmt.Errorf("open git file: invalid file descriptor")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat git file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("git target is not a regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
		return "", fmt.Errorf("git target has multiple hard links")
	}
	content, err := io.ReadAll(file)
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
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("stat git target: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("git target is not a regular file")
	}
	return nil
}

func requireNoInProgressGitOperation(ctx context.Context, target Target) error {
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD"} {
		path, err := runGit(ctx, target.Repository, "rev-parse", "--git-path", marker)
		if err != nil {
			return fmt.Errorf("inspect Git operation state: %w", err)
		}
		path = strings.TrimSpace(path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(target.Repository, path)
		}
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("Git operation is already in progress: %s", marker)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect Git operation state %s: %w", marker, err)
		}
	}
	for _, marker := range []string{"sequencer", "rebase-merge", "rebase-apply"} {
		path, err := runGit(ctx, target.Repository, "rev-parse", "--git-path", marker)
		if err != nil {
			return fmt.Errorf("inspect Git operation state: %w", err)
		}
		path = strings.TrimSpace(path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(target.Repository, path)
		}
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return fmt.Errorf("Git operation is already in progress: %s", marker)
		} else if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("inspect Git operation state %s: %w", marker, err)
		}
	}
	return nil
}

func requireWorktreeRoot(ctx context.Context, target Target) error {
	root, err := runGit(ctx, target.Repository, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("resolve Git worktree root: %w", err)
	}
	configured, err := filepath.Abs(target.Repository)
	if err != nil {
		return fmt.Errorf("resolve configured repository: %w", err)
	}
	gitRoot, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return fmt.Errorf("resolve Git worktree root path: %w", err)
	}
	if configured != gitRoot {
		return fmt.Errorf("repository must be the Git worktree root")
	}
	return nil
}

func verifyCommit(e Executor, ctx context.Context, parent string, t kernel.Transition, executionID string) error {
	return verifyCommitAt(ctx, e.Target, func(args ...string) (string, error) { return e.git(ctx, args...) }, parent, t, executionID)
}
func verifyLatestCommit(v Verifier, ctx context.Context, t kernel.Transition, executionID string) error {
	return verifyCommitAt(ctx, v.Target, func(args ...string) (string, error) { return v.git(ctx, args...) }, "", t, executionID)
}

func verifyCommitAt(ctx context.Context, target Target, git func(...string) (string, error), expectedParent string, t kernel.Transition, executionID string) error {
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
	paths, err := git("diff-tree", "--no-commit-id", "--name-only", "-r", "-z", head)
	if err != nil {
		return fmt.Errorf("inspect committed Git diff: %w", err)
	}
	if !exactNULPathList(paths, target.Path) {
		return fmt.Errorf("committed Git diff contains an unauthorized path")
	}
	beforeHash, err := git("rev-parse", head+"^:"+target.Path)
	if err != nil {
		return fmt.Errorf("read committed Git parent state: %w", err)
	}
	expectedBeforeHash, err := gitFilteredBlobHash(ctx, target, t.Before)
	if err != nil {
		return fmt.Errorf("normalize authorized Git parent state: %w", err)
	}
	if beforeHash != expectedBeforeHash {
		return fmt.Errorf("committed Git parent does not match authorized state")
	}
	afterHash, err := git("rev-parse", head+":"+target.Path)
	if err != nil {
		return fmt.Errorf("read committed Git target state: %w", err)
	}
	expectedAfterHash, err := gitFilteredBlobHash(ctx, target, t.After)
	if err != nil {
		return fmt.Errorf("normalize authorized Git target state: %w", err)
	}
	if afterHash != expectedAfterHash {
		return fmt.Errorf("committed Git target does not match authorized state")
	}
	return nil
}

func (e Executor) requireTracked(ctx context.Context) error {
	tracked, err := e.git(ctx, "ls-files", "-z", "--error-unmatch", "--", e.Target.Path)
	if err != nil {
		return fmt.Errorf("git target is not tracked: %v", err)
	}
	if !exactNULPathList(tracked, e.Target.Path) {
		return fmt.Errorf("git target is not tracked")
	}
	status, err := e.git(ctx, "ls-files", "-v", "-z", "--error-unmatch", "--", e.Target.Path)
	if err != nil {
		return fmt.Errorf("inspect Git target index state: %v", err)
	}
	if !validIndexPathStatus(status, e.Target.Path) {
		return fmt.Errorf("git target index state is not stageable")
	}
	return nil
}
func (v Verifier) requireTracked(ctx context.Context) error {
	tracked, err := v.git(ctx, "ls-files", "-z", "--error-unmatch", "--", v.Target.Path)
	if err != nil {
		return fmt.Errorf("git target is not tracked: %v", err)
	}
	if !exactNULPathList(tracked, v.Target.Path) {
		return fmt.Errorf("git target is not tracked")
	}
	status, err := v.git(ctx, "ls-files", "-v", "-z", "--error-unmatch", "--", v.Target.Path)
	if err != nil {
		return fmt.Errorf("inspect Git target index state: %v", err)
	}
	if !validIndexPathStatus(status, v.Target.Path) {
		return fmt.Errorf("git target index state is not stageable")
	}
	return nil
}

func gitFilteredBlobHash(ctx context.Context, target Target, content string) (string, error) {
	tmp, err := os.CreateTemp("", "ackos-git-filter-*")
	if err != nil {
		return "", fmt.Errorf("create temporary Git filter input: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write temporary Git filter input: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temporary Git filter input: %w", err)
	}
	hash, err := runGit(ctx, target.Repository, "hash-object", "--path="+target.Path, name)
	if err != nil {
		return "", fmt.Errorf("hash Git-filtered content: %w", err)
	}
	return strings.TrimSpace(hash), nil
}

func validIndexPathStatus(output, expected string) bool {
	output = strings.TrimSuffix(output, "\x00")
	records := strings.Split(output, "\x00")
	if len(records) != 1 || len(records[0]) < 3 || records[0][1] != ' ' || records[0][2:] != expected {
		return false
	}
	status := records[0][0]
	return status != 'S' && !(status >= 'a' && status <= 'z')
}

func exactNULPathList(output, expected string) bool {
	output = strings.TrimSuffix(output, "\x00")
	if output == "" {
		return false
	}
	paths := strings.Split(output, "\x00")
	return len(paths) == 1 && paths[0] == expected
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
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	output := stdout.String()
	for _, arg := range args {
		if arg == "-z" {
			return output, nil
		}
	}
	if len(args) > 0 && args[0] == "show" {
		return output, nil
	}
	return strings.TrimSpace(output), nil
}
