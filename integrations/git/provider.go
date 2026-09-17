// Package git provides a domain-specific Git resource provider for ackOS.
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
	if filepath.IsAbs(cleanPath) || cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
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
	fail := func(err error) kernel.ExecutionResult { return kernel.ExecutionResult{Message: err.Error()} }
	if authority.ExecutionID == "" {
		return fail(fmt.Errorf("execution authority ID is required"))
	}
	if t.Subject != e.Target.Subject {
		return fail(fmt.Errorf("git subject mismatch: got %q, want %q", t.Subject, e.Target.Subject))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := requireWorktreeRoot(ctx, e.Target); err != nil {
		return fail(err)
	}
	if err := validateNoSymlinks(e.Target); err != nil {
		return fail(err)
	}
	if err := e.requireTracked(ctx); err != nil {
		return fail(err)
	}
	before, err := e.read(ctx)
	if err != nil {
		return fail(err)
	}
	if before != t.Before {
		return fail(fmt.Errorf("git file changed before execution"))
	}
	if err := requireNoInProgressGitOperation(ctx, e.Target); err != nil {
		return fail(err)
	}
	if err := rejectConfiguredFilters(ctx, e.Target); err != nil {
		return fail(err)
	}
	if err := rejectConfiguredNormalization(ctx, e.Target); err != nil {
		return fail(err)
	}
	if err := rejectAttributesTarget(ctx, e.Target); err != nil {
		return fail(err)
	}
	if err := rejectGitConfigTarget(ctx, e.Target); err != nil {
		return fail(err)
	}
	if err := rejectGrafts(ctx, e.Target); err != nil {
		return fail(err)
	}
	status, err := e.git(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fail(fmt.Errorf("read git status: %w", err))
	}
	if status != "" {
		return fail(fmt.Errorf("git worktree is not clean"))
	}
	if err := requireCommitIdentity(ctx, e.Target); err != nil {
		return fail(err)
	}
	if err := requireIndexUnlocked(ctx, e.Target); err != nil {
		return fail(err)
	}
	if err := validateMutationBoundary(ctx, e.Target, t.Before); err != nil {
		return fail(err)
	}
	head, err := e.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return fail(fmt.Errorf("read git HEAD: %w", err))
	}
	beforeHash, err := gitBlobHash(ctx, e.Target, t.Before)
	if err != nil {
		return fail(fmt.Errorf("normalize authorized Git parent state: %w", err))
	}
	afterHash, err := gitBlobHash(ctx, e.Target, t.After)
	if err != nil {
		return fail(fmt.Errorf("normalize authorized Git target state: %w", err))
	}
	if beforeHash == afterHash {
		return fail(fmt.Errorf("authorized transition is not representable as a distinct Git blob"))
	}
	headHash, err := e.git(ctx, "--no-replace-objects", "rev-parse", "HEAD:./"+e.Target.Path)
	if err != nil {
		return fail(fmt.Errorf("read Git parent state: %w", err))
	}
	if headHash != beforeHash {
		return fail(fmt.Errorf("Git parent does not match authorized state"))
	}
	if err := atomicWriteTarget(e.Target, []byte(t.After)); err != nil {
		return fail(fmt.Errorf("write git file: %w", err))
	}
	if _, err := e.git(ctx, "add", "--", literalPathspec(e.Target.Path)); err != nil {
		return fail(fmt.Errorf("git add: %w", err))
	}
	cachedPaths, err := e.git(ctx, "diff", "--cached", "--name-only", "-z")
	if err != nil {
		return fail(fmt.Errorf("inspect staged Git diff: %w", err))
	}
	if !exactNULPathList(cachedPaths, e.Target.Path) {
		return fail(fmt.Errorf("staged Git diff contains an unauthorized path"))
	}
	cachedHash, err := e.git(ctx, "--no-replace-objects", "rev-parse", ":./"+e.Target.Path)
	if err != nil {
		return fail(fmt.Errorf("read staged Git target: %w", err))
	}
	if cachedHash != afterHash {
		return fail(fmt.Errorf("staged Git target does not match authorized state"))
	}
	if err := requireIndexUnlocked(ctx, e.Target); err != nil {
		return fail(err)
	}
	message := "ackOS: execute " + authority.ExecutionID
	if err := commitVerifiedTree(ctx, e.Target, head, afterHash, []byte(t.After), message); err != nil {
		return fail(err)
	}
	if err := verifyCommit(e, ctx, head, t, authority.ExecutionID); err != nil {
		return fail(err)
	}
	verifiedHead, err := e.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return fail(fmt.Errorf("re-read Git HEAD after verification: %w", err))
	}
	finalContent, err := e.read(ctx)
	if err != nil {
		return fail(fmt.Errorf("re-read Git target after verification: %w", err))
	}
	if finalContent != t.After {
		return fail(fmt.Errorf("git target changed after commit verification"))
	}
	finalHead, err := e.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return fail(fmt.Errorf("re-read Git HEAD at return boundary: %w", err))
	}
	if finalHead != verifiedHead {
		return fail(fmt.Errorf("Git HEAD changed after commit verification"))
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
	if err := rejectConfiguredFilters(ctx, v.Target); err != nil {
		return kernel.Observation{}, err
	}
	if err := rejectConfiguredNormalization(ctx, v.Target); err != nil {
		return kernel.Observation{}, err
	}
	if err := rejectGitConfigTarget(ctx, v.Target); err != nil {
		return kernel.Observation{}, err
	}
	if err := rejectGrafts(ctx, v.Target); err != nil {
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
	verifiedHead, err := v.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return kernel.Observation{}, fmt.Errorf("re-read verified Git HEAD: %w", err)
	}
	finalContent, err := v.read(ctx)
	if err != nil {
		return kernel.Observation{}, err
	}
	if finalContent != t.After {
		return kernel.Observation{}, fmt.Errorf("git file changed during verification")
	}
	finalHead, err := v.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return kernel.Observation{}, fmt.Errorf("re-read Git HEAD at verification return boundary: %w", err)
	}
	if finalHead != verifiedHead {
		return kernel.Observation{}, fmt.Errorf("Git HEAD changed during verification")
	}
	return kernel.NewObservation(v.Target.Subject, finalContent, 0, time.Now().UTC())
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
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
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
	readDone := make(chan struct{})
	var content []byte
	var readErr error
	go func() {
		content, readErr = io.ReadAll(file)
		close(readDone)
	}()
	select {
	case <-readDone:
		if readErr != nil {
			return "", fmt.Errorf("read git file: %w", readErr)
		}
	case <-ctx.Done():
		_ = file.Close()
		return "", ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return "", err
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

func validateMutationBoundary(ctx context.Context, target Target, expected string) error {
	content, err := readFile(ctx, target)
	if err != nil {
		return fmt.Errorf("revalidate git target: %w", err)
	}
	if content != expected {
		return fmt.Errorf("git file changed at mutation boundary")
	}
	return nil
}

func atomicWriteTarget(target Target, content []byte) error {
	path := filepath.Join(target.Repository, target.Path)
	dir := filepath.Dir(path)
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		if err := rejectUnpreservableMetadata(path, info); err != nil {
			return err
		}
		mode = info.Mode()
	}
	tmp, err := os.CreateTemp(dir, ".ackos-write-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func rejectUnpreservableMetadata(path string, info os.FileInfo) error {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if uint32(os.Geteuid()) != stat.Uid || uint32(os.Getegid()) != stat.Gid {
			return fmt.Errorf("git target ownership cannot be preserved by atomic replacement")
		}
	}
	for size := 256; ; size *= 2 {
		buf := make([]byte, size)
		n, err := syscall.Listxattr(path, buf)
		if err == syscall.ENOTSUP || err == syscall.EOPNOTSUPP {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect git target extended attributes: %w", err)
		}
		if n == 0 {
			return nil
		}
		if n < len(buf) {
			return fmt.Errorf("git target has extended attributes or ACLs that cannot be preserved by atomic replacement")
		}
	}
}

func requireCommitIdentity(ctx context.Context, target Target) error {
	for _, identity := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
		if _, err := runGit(ctx, target.Repository, "var", identity); err != nil {
			return fmt.Errorf("Git commit identity is not configured: %s: %w", identity, err)
		}
	}
	return nil
}

func requireIndexUnlocked(ctx context.Context, target Target) error {
	path, err := runGit(ctx, target.Repository, "rev-parse", "--git-path", "index.lock")
	if err != nil {
		return fmt.Errorf("inspect Git index lock: %w", err)
	}
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(target.Repository, path)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("Git index is locked")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect Git index lock: %w", err)
	}
	return nil
}

func requireNoInProgressGitOperation(ctx context.Context, target Target) error {
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "sequencer", "rebase-merge", "rebase-apply"} {
		path, err := runGit(ctx, target.Repository, "rev-parse", "--git-path", marker)
		if err != nil {
			return fmt.Errorf("inspect Git operation state: %w", err)
		}
		path = strings.TrimSpace(path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(target.Repository, path)
		}
		if info, err := os.Stat(path); err == nil {
			if marker == "MERGE_HEAD" || marker == "CHERRY_PICK_HEAD" || marker == "REVERT_HEAD" || marker == "REBASE_HEAD" || info.IsDir() {
				return fmt.Errorf("Git operation is already in progress: %s", marker)
			}
		} else if !os.IsNotExist(err) {
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

func rejectAttributesTarget(ctx context.Context, target Target) error {
	if strings.EqualFold(filepath.Base(target.Path), ".gitattributes") {
		return fmt.Errorf("git .gitattributes targets are not supported because the target can change its own filter environment")
	}
	attrs, err := runGit(ctx, target.Repository, "config", "--path", "--get", "core.attributesFile")
	if err != nil {
		return nil
	}
	attrs = strings.TrimSpace(attrs)
	if attrs == "" {
		return nil
	}
	configured, err := filepath.Abs(filepath.Join(target.Repository, target.Path))
	if err != nil {
		return fmt.Errorf("resolve configured target path: %w", err)
	}
	if !filepath.IsAbs(attrs) {
		attrs = filepath.Join(target.Repository, attrs)
	}
	actual, err := filepath.Abs(attrs)
	if err != nil {
		return fmt.Errorf("resolve configured attributes file: %w", err)
	}
	if configured == actual {
		return fmt.Errorf("git target is configured as the active attributes file")
	}
	return nil
}

func rejectGitConfigTarget(ctx context.Context, target Target) error {
	output, err := runGit(ctx, target.Repository, "config", "--includes", "--show-origin", "--list")
	if err != nil {
		return fmt.Errorf("inspect Git configuration sources: %w", err)
	}
	configured, err := filepath.Abs(filepath.Join(target.Repository, target.Path))
	if err != nil {
		return fmt.Errorf("resolve configured Git target path: %w", err)
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		origin, _, ok := strings.Cut(line, "\t")
		if !ok || !strings.HasPrefix(origin, "file:") {
			continue
		}
		path := strings.TrimPrefix(origin, "file:")
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(target.Repository, path)
		}
		actual, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve Git configuration source: %w", err)
		}
		if actual == configured {
			return fmt.Errorf("git target is an active Git configuration source")
		}
	}
	includes, includeErr := runGit(ctx, target.Repository, "config", "--local", "--get-regexp", "^include")
	if includeErr == nil {
		for _, line := range strings.Split(includes, "\n") {
			fields := strings.SplitN(strings.TrimSpace(line), " ", 2)
			if len(fields) != 2 {
				continue
			}
			include := strings.TrimSpace(fields[1])
			if !filepath.IsAbs(include) {
				include = filepath.Join(target.Repository, ".git", include)
			}
			include, resolveErr := filepath.Abs(include)
			if resolveErr != nil {
				return fmt.Errorf("resolve Git include: %w", resolveErr)
			}
			if include == configured {
				return fmt.Errorf("git target is an active Git configuration source")
			}
		}
	}
	gitConfig, configErr := runGit(ctx, target.Repository, "rev-parse", "--git-path", "config")
	if configErr == nil {
		configPath := strings.TrimSpace(gitConfig)
		if !filepath.IsAbs(configPath) {
			configPath = filepath.Join(target.Repository, configPath)
		}
		configPath, resolveErr := filepath.Abs(configPath)
		if resolveErr != nil {
			return fmt.Errorf("resolve Git config path: %w", resolveErr)
		}
		data, readErr := os.ReadFile(configPath)
		if readErr == nil {
			section := ""
			for _, raw := range strings.Split(string(data), "\n") {
				line := strings.TrimSpace(raw)
				if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
					section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
					continue
				}
				if !strings.HasPrefix(section, "include") {
					continue
				}
				key, value, ok := strings.Cut(line, "=")
				if !ok || strings.ToLower(strings.TrimSpace(key)) != "path" {
					continue
				}
				include := strings.TrimSpace(value)
				if !filepath.IsAbs(include) {
					include = filepath.Join(filepath.Dir(configPath), include)
				}
				include, resolveErr = filepath.Abs(include)
				if resolveErr != nil {
					return fmt.Errorf("resolve Git include: %w", resolveErr)
				}
				if include == configured {
					return fmt.Errorf("git target is an active Git configuration source")
				}
				if matches, globErr := filepath.Glob(include); globErr == nil {
					for _, match := range matches {
						match, _ = filepath.Abs(match)
						if match == configured {
							return fmt.Errorf("git target is an active Git configuration source")
						}
					}
				}
			}
		}
	}
	return nil
}

func rejectGrafts(ctx context.Context, target Target) error {
	path, err := runGit(ctx, target.Repository, "rev-parse", "--git-path", "info/grafts")
	if err != nil {
		return fmt.Errorf("inspect Git graft file: %w", err)
	}
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(target.Repository, path)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("Git graft file is not supported")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect Git graft file: %w", err)
	}
	return nil
}

func rejectConfiguredFilters(ctx context.Context, target Target) error {
	paths, err := runGit(ctx, target.Repository, "ls-files", "-z", "--cached")
	if err != nil {
		return fmt.Errorf("inspect tracked Git paths: %w", err)
	}
	if strings.TrimSuffix(paths, "\x00") == "" {
		return nil
	}
	attrs, err := runGitInput(ctx, target.Repository, []byte(paths), "check-attr", "-z", "--stdin", "filter")
	if err != nil {
		return fmt.Errorf("inspect Git clean filters: %w", err)
	}
	parts := strings.Split(strings.TrimSuffix(attrs, "\x00"), "\x00")
	if len(parts)%3 != 0 {
		return fmt.Errorf("unexpected Git clean filter metadata")
	}
	for i := 0; i < len(parts); i += 3 {
		if parts[i+1] != "filter" || (parts[i+2] != "unspecified" && parts[i+2] != "unset") {
			return fmt.Errorf("git repository uses a configured clean filter; filtered repositories are not supported")
		}
	}
	return nil
}

func targetGitAttr(ctx context.Context, target Target) (string, error) {
	output, err := runGit(ctx, target.Repository, "check-attr", "-z", "filter", "--", target.Path)
	if err != nil {
		return "", fmt.Errorf("inspect Git clean filter: %w", err)
	}
	parts := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	if len(parts) != 3 || parts[0] != target.Path || parts[1] != "filter" {
		return "", fmt.Errorf("unexpected Git clean filter metadata")
	}
	return parts[2], nil
}

func rejectConfiguredNormalization(ctx context.Context, target Target) error {
	autocrlf, err := runGit(ctx, target.Repository, "config", "--get", "core.autocrlf")
	if err == nil {
		raw := strings.ToLower(strings.TrimSpace(autocrlf))
		if raw == "input" {
			return fmt.Errorf("git target uses core.autocrlf normalization; normalized targets are not supported")
		}
		parsed, boolErr := runGit(ctx, target.Repository, "config", "--bool", "--get", "core.autocrlf")
		if boolErr == nil && strings.EqualFold(strings.TrimSpace(parsed), "true") {
			return fmt.Errorf("git target uses core.autocrlf normalization; normalized targets are not supported")
		}
	}
	output, err := runGit(ctx, target.Repository, "check-attr", "-z", "text", "eol", "crlf", "ident", "working-tree-encoding", "--", target.Path)
	if err != nil {
		return fmt.Errorf("inspect Git text normalization: %w", err)
	}
	parts := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	if len(parts) != 15 || parts[0] != target.Path {
		return fmt.Errorf("unexpected Git text normalization metadata")
	}
	values := map[string]string{
		parts[1]:  parts[2],
		parts[4]:  parts[5],
		parts[7]:  parts[8],
		parts[10]: parts[11],
		parts[13]: parts[14],
	}
	if values["text"] != "unspecified" && values["text"] != "unset" {
		return fmt.Errorf("git target uses a configured text normalization attribute; normalized targets are not supported")
	}
	if values["eol"] != "unspecified" && values["eol"] != "unset" {
		return fmt.Errorf("git target uses a configured eol attribute; normalized targets are not supported")
	}
	if values["crlf"] != "unspecified" && values["crlf"] != "unset" {
		return fmt.Errorf("git target uses a configured crlf attribute; normalized targets are not supported")
	}
	if values["ident"] != "unspecified" && values["ident"] != "unset" {
		return fmt.Errorf("git target uses a configured ident attribute; normalized targets are not supported")
	}
	if values["working-tree-encoding"] != "unspecified" && values["working-tree-encoding"] != "unset" {
		return fmt.Errorf("git target uses working-tree-encoding; encoded targets are not supported")
	}
	return nil
}

func commitVerifiedTree(ctx context.Context, target Target, parent, afterHash string, content []byte, message string) error {
	blob, err := runGitInput(ctx, target.Repository, content, "hash-object", "-w", "--stdin")
	if err != nil {
		return fmt.Errorf("store authorized Git blob: %w", err)
	}
	blob = strings.TrimSpace(blob)
	if blob != afterHash {
		return fmt.Errorf("authorized Git blob hash changed before commit")
	}
	modeOutput, err := runGit(ctx, target.Repository, "ls-files", "--format=%(objectmode)", "--", literalPathspec(target.Path))
	if err != nil {
		return fmt.Errorf("read target Git mode: %w", err)
	}
	mode := strings.TrimSpace(modeOutput)
	if mode == "" {
		return fmt.Errorf("target Git mode is missing")
	}
	indexFile, err := os.CreateTemp(target.Repository, ".ackos-index-*")
	if err != nil {
		return fmt.Errorf("create temporary Git index: %w", err)
	}
	indexPath := indexFile.Name()
	if err := indexFile.Close(); err != nil {
		_ = os.Remove(indexPath)
		return fmt.Errorf("close temporary Git index: %w", err)
	}
	defer os.Remove(indexPath)
	env := map[string]string{"GIT_INDEX_FILE": indexPath}
	if _, err := runGitWithEnv(ctx, target.Repository, env, "read-tree", parent); err != nil {
		return fmt.Errorf("capture Git parent index: %w", err)
	}
	if _, err := runGitWithEnv(ctx, target.Repository, env, "update-index", "--add", "--cacheinfo", mode, blob, target.Path); err != nil {
		return fmt.Errorf("install authorized target in temporary Git index: %w", err)
	}
	tree, err := runGitWithEnv(ctx, target.Repository, env, "write-tree")
	if err != nil {
		return fmt.Errorf("write authorized Git tree: %w", err)
	}
	commit, err := runGit(ctx, target.Repository, "commit-tree", strings.TrimSpace(tree), "-p", parent, "-m", message, "--no-gpg-sign")
	if err != nil {
		return fmt.Errorf("create authorized Git commit: %w", err)
	}
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return fmt.Errorf("Git commit object is missing")
	}
	if _, err := runGit(ctx, target.Repository, "update-ref", "HEAD", commit, parent); err != nil {
		return fmt.Errorf("atomically install authorized Git commit: %w", err)
	}
	if _, err := runGit(ctx, target.Repository, "add", "--", literalPathspec(target.Path)); err != nil {
		return fmt.Errorf("synchronize Git index after commit: %w", err)
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
	safeGit := func(args ...string) (string, error) { return git(append([]string{"--no-replace-objects"}, args...)...) }
	head, err := safeGit("rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("read committed Git HEAD: %w", err)
	}
	parents, err := safeGit("rev-list", "--parents", "-n", "1", head)
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
	message, err := safeGit("log", "-1", "--format=%B")
	if err != nil {
		return fmt.Errorf("read Git commit message: %w", err)
	}
	if strings.TrimSpace(message) != "ackOS: execute "+executionID {
		return fmt.Errorf("git execution marker mismatch")
	}
	paths, err := safeGit("diff-tree", "--no-commit-id", "--name-only", "-r", "-z", head)
	if err != nil {
		return fmt.Errorf("inspect committed Git diff: %w", err)
	}
	if !exactNULPathList(paths, target.Path) {
		return fmt.Errorf("committed Git diff contains an unauthorized path")
	}
	beforeHash, err := safeGit("rev-parse", head+"^:./"+target.Path)
	if err != nil {
		return fmt.Errorf("read committed Git parent state: %w", err)
	}
	expectedBeforeHash, err := gitBlobHash(ctx, target, t.Before)
	if err != nil {
		return fmt.Errorf("normalize authorized Git parent state: %w", err)
	}
	if beforeHash != expectedBeforeHash {
		return fmt.Errorf("committed Git parent does not match authorized state")
	}
	afterHash, err := safeGit("rev-parse", head+":./"+target.Path)
	if err != nil {
		return fmt.Errorf("read committed Git target state: %w", err)
	}
	expectedAfterHash, err := gitBlobHash(ctx, target, t.After)
	if err != nil {
		return fmt.Errorf("normalize authorized Git target state: %w", err)
	}
	if afterHash != expectedAfterHash {
		return fmt.Errorf("committed Git target does not match authorized state")
	}
	return nil
}

func (e Executor) requireTracked(ctx context.Context) error {
	tracked, err := e.git(ctx, "ls-files", "-z", "--error-unmatch", "--", literalPathspec(e.Target.Path))
	if err != nil {
		return fmt.Errorf("git target is not tracked: %v", err)
	}
	if !exactNULPathList(tracked, e.Target.Path) {
		return fmt.Errorf("git target is not tracked")
	}
	status, err := e.git(ctx, "ls-files", "-v", "-z", "--error-unmatch", "--", literalPathspec(e.Target.Path))
	if err != nil {
		return fmt.Errorf("inspect Git target index state: %v", err)
	}
	if !validIndexPathStatus(status, e.Target.Path) {
		return fmt.Errorf("git target index state is not stageable")
	}
	return nil
}

func (v Verifier) requireTracked(ctx context.Context) error {
	tracked, err := v.git(ctx, "ls-files", "-z", "--error-unmatch", "--", literalPathspec(v.Target.Path))
	if err != nil {
		return fmt.Errorf("git target is not tracked: %v", err)
	}
	if !exactNULPathList(tracked, v.Target.Path) {
		return fmt.Errorf("git target is not tracked")
	}
	status, err := v.git(ctx, "ls-files", "-v", "-z", "--error-unmatch", "--", literalPathspec(v.Target.Path))
	if err != nil {
		return fmt.Errorf("inspect Git target index state: %v", err)
	}
	if !validIndexPathStatus(status, v.Target.Path) {
		return fmt.Errorf("git target index state is not stageable")
	}
	return nil
}

func gitBlobHash(ctx context.Context, target Target, content string) (string, error) {
	tmp, err := os.CreateTemp("", "ackos-git-blob-*")
	if err != nil {
		return "", fmt.Errorf("create temporary Git blob input: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write temporary Git blob input: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close temporary Git blob input: %w", err)
	}
	hash, err := runGit(ctx, target.Repository, "hash-object", "--no-filters", name)
	if err != nil {
		return "", fmt.Errorf("hash Git blob content: %w", err)
	}
	return strings.TrimSpace(hash), nil
}

func literalPathspec(path string) string { return ":(literal)" + path }

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
	return runGitWithInput(ctx, repository, nil, nil, args...)
}

func runGitInput(ctx context.Context, repository string, input []byte, args ...string) (string, error) {
	return runGitWithInput(ctx, repository, input, nil, args...)
}

func runGitWithEnv(ctx context.Context, repository string, env map[string]string, args ...string) (string, error) {
	return runGitWithInput(ctx, repository, nil, env, args...)
}

func runGitWithInput(ctx context.Context, repository string, input []byte, overrides map[string]string, args ...string) (string, error) {
	gitArgs := append([]string{"-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.Command("git", gitArgs...)
	cmd.Dir = repository
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = sanitizedGitEnv()
	for key, value := range overrides {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start git: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case runErr := <-done:
		if runErr != nil {
			return "", fmt.Errorf("%w: %s", runErr, strings.TrimSpace(stderr.String()))
		}
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		select {
		case <-done:
		case <-time.After(cmd.WaitDelay):
		}
		return "", ctx.Err()
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

func sanitizedGitEnv() []string {
	blocked := map[string]struct{}{
		"GIT_DIR":                          {},
		"GIT_WORK_TREE":                    {},
		"GIT_INDEX_FILE":                   {},
		"GIT_OBJECT_DIRECTORY":             {},
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": {},
		"GIT_COMMON_DIR":                   {},
		"GIT_NAMESPACE":                    {},
		"GIT_CEILING_DIRECTORIES":          {},
		"GIT_DISCOVERY_ACROSS_FILESYSTEM":  {},
		"GIT_GRAFT_FILE":                   {},
	}
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if key, _, ok := strings.Cut(entry, "="); ok {
			if _, blocked := blocked[key]; blocked {
				continue
			}
		}
		env = append(env, entry)
	}
	return env
}
