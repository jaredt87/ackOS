// Package git provides a domain-specific Git resource provider for ackOS.
package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jaredt87/ackOS/kernel"
	"golang.org/x/sys/unix"
)

type Target struct {
	Repository string
	Path       string
	Subject    string

	repositoryDev    uint64
	repositoryIno    uint64
	capturedHead     string
	capturedHeadRef  string
	gitDirPath       string
	gitDirDev        uint64
	gitDirIno        uint64
	gitCommonDirPath string
	gitCommonDirDev  uint64
	gitCommonDirIno  uint64
	lifecycle        *lifecycleState
}

type executionParent struct {
	head string
	ref  string
}

type lifecycleState struct {
	mu      sync.Mutex
	parents map[string]executionParent
}

func (s *lifecycleState) capture(ctx context.Context, target Target, executionID string) (executionParent, error) {
	if s == nil {
		return executionParent{}, fmt.Errorf("Git lifecycle state is unavailable")
	}
	head, err := runGitTarget(ctx, target, "rev-parse", "HEAD")
	if err != nil {
		return executionParent{}, fmt.Errorf("capture Git execution parent: %w", err)
	}
	head = strings.TrimSpace(head)
	if head == "" {
		return executionParent{}, fmt.Errorf("capture Git execution parent: empty revision")
	}
	ref, err := runGitTarget(ctx, target, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return executionParent{}, fmt.Errorf("capture Git execution branch: %w", err)
	}
	ref = strings.TrimSpace(ref)
	if ref == "" || !strings.HasPrefix(ref, "refs/heads/") {
		return executionParent{}, fmt.Errorf("Git HEAD must remain attached to a branch")
	}
	parent := executionParent{head: head, ref: ref}
	s.mu.Lock()
	if s.parents == nil {
		s.parents = make(map[string]executionParent)
	}
	s.parents[executionID] = parent
	s.mu.Unlock()
	return parent, nil
}

func (s *lifecycleState) parent(executionID string) (executionParent, error) {
	if s == nil {
		return executionParent{}, fmt.Errorf("Git lifecycle state is unavailable")
	}
	s.mu.Lock()
	parent, ok := s.parents[executionID]
	s.mu.Unlock()
	if !ok {
		return executionParent{}, fmt.Errorf("Git execution lifecycle state is unavailable")
	}
	return parent, nil
}

func (s *lifecycleState) discard(executionID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.parents, executionID)
	s.mu.Unlock()
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
	target := Target{Repository: absRepo, Path: cleanPath, Subject: subject, lifecycle: &lifecycleState{parents: make(map[string]executionParent)}}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Dev == 0 || stat.Ino == 0 {

		return Target{}, fmt.Errorf("capture configured repository identity")

	}
	target.repositoryDev = uint64(stat.Dev)
	target.repositoryIno = uint64(stat.Ino)
	head, err := runGit(context.Background(), target.Repository, "rev-parse", "HEAD")
	if err != nil {

		return Target{}, fmt.Errorf("capture Git HEAD: %w", err)

	}
	target.capturedHead = strings.TrimSpace(head)
	if target.capturedHead == "" {

		return Target{}, fmt.Errorf("capture Git HEAD: empty revision")

	}
	headRef, err := runGit(context.Background(), target.Repository, "symbolic-ref", "-q", "HEAD")
	if err != nil {

		return Target{}, fmt.Errorf("capture Git HEAD branch: %w", err)

	}
	target.capturedHeadRef = strings.TrimSpace(headRef)
	if target.capturedHeadRef == "" || !strings.HasPrefix(target.capturedHeadRef, "refs/heads/") {

		return Target{}, fmt.Errorf("Git HEAD must remain attached to a branch")

	}
	metadata, err := captureGitMetadataIdentity(context.Background(), target)
	if err != nil {
		return Target{}, err
	}
	target.gitDirPath = metadata.gitDirPath
	target.gitDirDev = metadata.gitDirDev
	target.gitDirIno = metadata.gitDirIno
	target.gitCommonDirPath = metadata.gitCommonDirPath
	target.gitCommonDirDev = metadata.gitCommonDirDev
	target.gitCommonDirIno = metadata.gitCommonDirIno
	if err := requireWorktreeRoot(context.Background(), target); err != nil {

		return Target{}, err

	}
	if err := validateNoSymlinks(target); err != nil {

		return Target{}, err

	}
	targetInfo, err := os.Stat(filepath.Join(target.Repository, target.Path))
	if err != nil {
		return Target{}, fmt.Errorf("stat git target: %w", err)
	}
	if !targetInfo.Mode().IsRegular() {
		return Target{}, fmt.Errorf("git target is not a regular file")
	}
	if targetInfo.Mode()&os.ModeSetuid != 0 || targetInfo.Mode()&os.ModeSetgid != 0 || targetInfo.Mode()&os.ModeSticky != 0 {
		return Target{}, fmt.Errorf("git target uses unsupported special permission bits")
	}
	return target, nil
}

type Observer struct{ Target Target }

func (o Observer) Observe(ctx context.Context, subject string) (kernel.Observation, error) {
	if subject != o.Target.Subject {
		return kernel.Observation{}, fmt.Errorf("git subject mismatch: got %q, want %q", subject, o.Target.Subject)
	}
	if err := ctx.Err(); err != nil {

		return kernel.Observation{}, err

	}
	if err := requireWorktreeRoot(ctx, o.Target); err != nil {

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
	unlock, err := acquireTargetLock(ctx, e.Target)
	if err != nil {

		return fail(err)

	}
	defer unlock()
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
	if err := rejectReplaceRefs(ctx, e.Target); err != nil {

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
	if err := rejectSubmodules(ctx, e.Target); err != nil {
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
	expectedParent, err := e.Target.lifecycle.capture(ctx, e.Target, authority.ExecutionID)
	if err != nil {
		return fail(err)
	}
	lifecycleComplete := false
	defer func() {
		if !lifecycleComplete {
			e.Target.lifecycle.discard(authority.ExecutionID)
		}
	}()

	head := expectedParent.head
	headRef := expectedParent.ref
	currentHead, err := e.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return fail(fmt.Errorf("re-read Git execution parent: %w", err))
	}
	if strings.TrimSpace(currentHead) != head {
		return fail(fmt.Errorf("Git HEAD changed before execution"))
	}
	currentRef, err := e.git(ctx, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return fail(fmt.Errorf("re-read Git execution branch: %w", err))
	}
	if strings.TrimSpace(currentRef) != headRef {
		return fail(fmt.Errorf("Git HEAD branch changed before execution"))
	}
	expectedMode, err := gitTreeMode(ctx, e.Target, head)
	if err != nil {

		return fail(fmt.Errorf("read Git parent target mode: %w", err))

	}
	liveMode, err := liveTargetMode(e.Target)
	if err != nil {

		return fail(fmt.Errorf("read live Git target mode: %w", err))

	}
	if liveMode != expectedMode {

		return fail(fmt.Errorf("Git target mode does not match parent before mutation"))

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
	indexHashBefore, err := gitIndexHash(ctx, e.Target)
	if err != nil {
		return fail(fmt.Errorf("read Git target index before mutation: %w", err))
	}
	if indexHashBefore != beforeHash {
		return fail(fmt.Errorf("Git target index changed before mutation"))
	}
	indexModeBefore, err := gitIndexMode(ctx, e.Target)
	if err != nil {
		return fail(fmt.Errorf("read Git target index mode before mutation: %w", err))
	}
	if indexModeBefore != expectedMode {
		return fail(fmt.Errorf("Git target index mode changed before mutation"))
	}
	if err := atomicWriteTarget(e.Target, []byte(t.Before), []byte(t.After)); err != nil {

		return fail(fmt.Errorf("write git file: %w", err))

	}
	currentIndexHash, err := gitIndexHash(ctx, e.Target)
	if err != nil {
		return fail(fmt.Errorf("re-read Git target index before staging: %w", err))
	}
	if currentIndexHash != indexHashBefore {
		return fail(fmt.Errorf("Git target index changed during mutation"))
	}
	currentIndexMode, err := gitIndexMode(ctx, e.Target)
	if err != nil {
		return fail(fmt.Errorf("re-read Git target index mode before staging: %w", err))
	}
	if currentIndexMode != indexModeBefore {
		return fail(fmt.Errorf("Git target index mode changed during mutation"))
	}
	if _, err := e.git(ctx, "update-index", "--add", "--cacheinfo", indexModeBefore+","+afterHash+","+e.Target.Path); err != nil {

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
	if err := commitVerifiedTree(ctx, e.Target, head, headRef, afterHash, []byte(t.After), message); err != nil {

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
	finalInfo, err := os.Stat(filepath.Join(e.Target.Repository, e.Target.Path))
	if err != nil {
		return fail(fmt.Errorf("revalidate Git target metadata: %w", err))
	}
	if err := rejectUnpreservableMetadata(filepath.Join(e.Target.Repository, e.Target.Path), finalInfo); err != nil {
		return fail(fmt.Errorf("Git target metadata changed after commit verification: %w", err))
	}
	finalHead, err := e.git(ctx, "rev-parse", "HEAD")
	if err != nil {

		return fail(fmt.Errorf("re-read Git HEAD at return boundary: %w", err))

	}
	if finalHead != verifiedHead {

		return fail(fmt.Errorf("Git HEAD changed after commit verification"))

	}
	lifecycleComplete = true
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
	expectedParent, err := v.Target.lifecycle.parent(authority.ExecutionID)
	if err != nil {
		return kernel.Observation{}, err
	}
	defer v.Target.lifecycle.discard(authority.ExecutionID)
	currentHead, err := v.git(ctx, "rev-parse", "HEAD")
	if err != nil {

		return kernel.Observation{}, fmt.Errorf("read captured Git HEAD: %w", err)

	}
	if strings.TrimSpace(currentHead) == "" {

		return kernel.Observation{}, fmt.Errorf("captured Git HEAD is unavailable")

	}
	currentRef, err := v.git(ctx, "symbolic-ref", "-q", "HEAD")
	if err != nil {

		return kernel.Observation{}, fmt.Errorf("Git HEAD is not on the authorized branch: %w", err)

	}
	if strings.TrimSpace(currentRef) != expectedParent.ref {

		return kernel.Observation{}, fmt.Errorf("Git HEAD is not on the authorized branch")

	}
	if err := validateNoSymlinks(v.Target); err != nil {

		return kernel.Observation{}, err

	}
	if err := v.requireTracked(ctx); err != nil {

		return kernel.Observation{}, err

	}
	if err := requireNoInProgressGitOperation(ctx, v.Target); err != nil {

		return kernel.Observation{}, err

	}
	if err := rejectReplaceRefs(ctx, v.Target); err != nil {
		return kernel.Observation{}, err
	}
	if err := rejectConfiguredFilters(ctx, v.Target); err != nil {
		return kernel.Observation{}, err
	}
	if err := rejectConfiguredNormalization(ctx, v.Target); err != nil {
		return kernel.Observation{}, err
	}
	if err := rejectAttributesTarget(ctx, v.Target); err != nil {
		return kernel.Observation{}, err
	}
	if err := rejectGitConfigTarget(ctx, v.Target); err != nil {
		return kernel.Observation{}, err
	}
	if err := rejectSubmodules(ctx, v.Target); err != nil {
		return kernel.Observation{}, err
	}
	if err := rejectGrafts(ctx, v.Target); err != nil {
		return kernel.Observation{}, err
	}
	status, err := v.git(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {

		return kernel.Observation{}, fmt.Errorf("read git status during verification: %w", err)

	}
	if status != "" {

		return kernel.Observation{}, fmt.Errorf("git worktree is not clean during verification")

	}
	content, err := v.read(ctx)
	if err != nil {

		return kernel.Observation{}, err

	}
	if content != t.After {

		return kernel.Observation{}, fmt.Errorf("git file state mismatch")
	}
	if err := verifyLatestCommit(v, ctx, expectedParent.head, t, authority.ExecutionID); err != nil {
		return kernel.Observation{}, err

	}
	verifiedHead, err := v.git(ctx, "rev-parse", "HEAD")
	if err != nil {

		return kernel.Observation{}, fmt.Errorf("re-read verified Git HEAD: %w", err)

	}
	verifiedHead = strings.TrimSpace(verifiedHead)
	if verifiedHead == "" {

		return kernel.Observation{}, fmt.Errorf("verified Git HEAD is unavailable")

	}
	finalContent, err := v.read(ctx)
	if err != nil {

		return kernel.Observation{}, err

	}
	if finalContent != t.After {

		return kernel.Observation{}, fmt.Errorf("git file changed during verification")

	}
	finalInfo, err := os.Stat(filepath.Join(v.Target.Repository, v.Target.Path))
	if err != nil {
		return kernel.Observation{}, fmt.Errorf("revalidate Git target metadata: %w", err)
	}
	if err := rejectUnpreservableMetadata(filepath.Join(v.Target.Repository, v.Target.Path), finalInfo); err != nil {
		return kernel.Observation{}, fmt.Errorf("Git target metadata changed during verification: %w", err)
	}
	expectedMode, err := gitTreeMode(ctx, v.Target, verifiedHead)
	if err != nil {

		return kernel.Observation{}, fmt.Errorf("read verified Git target mode: %w", err)

	}
	liveMode, err := liveTargetMode(v.Target)
	if err != nil {

		return kernel.Observation{}, fmt.Errorf("read live Git target mode: %w", err)

	}
	if liveMode != expectedMode {

		return kernel.Observation{}, fmt.Errorf("Git target mode changed during verification")

	}
	indexMode, err := gitIndexMode(ctx, v.Target)
	if err != nil {

		return kernel.Observation{}, fmt.Errorf("read live Git index target mode: %w", err)

	}
	if indexMode != expectedMode {

		return kernel.Observation{}, fmt.Errorf("Git index target mode changed during verification")

	}
	indexHash, err := gitIndexHash(ctx, v.Target)
	if err != nil {

		return kernel.Observation{}, fmt.Errorf("read live Git index target blob: %w", err)

	}
	expectedHash, err := v.git(ctx, "--no-replace-objects", "rev-parse", verifiedHead+":./"+v.Target.Path)
	if err != nil {

		return kernel.Observation{}, fmt.Errorf("read verified Git target blob: %w", err)

	}
	if indexHash != expectedHash {

		return kernel.Observation{}, fmt.Errorf("Git index target blob changed during verification")

	}
	if err := verifyLiveIndexMatchesHead(ctx, v.Target, verifiedHead); err != nil {

		return kernel.Observation{}, err

	}
	finalHead, err := v.git(ctx, "rev-parse", "HEAD")
	if err != nil {

		return kernel.Observation{}, fmt.Errorf("re-read Git HEAD at verification return boundary: %w", err)

	}
	if finalHead != verifiedHead {

		return kernel.Observation{}, fmt.Errorf("Git HEAD changed during verification")

	}
	finalRef, err := v.git(ctx, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return kernel.Observation{}, fmt.Errorf("re-read Git HEAD branch at verification return boundary: %w", err)
	}
	if strings.TrimSpace(finalRef) != expectedParent.ref {
		return kernel.Observation{}, fmt.Errorf("Git HEAD branch changed during verification")
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
	parentFD, err := openParentDirNoSymlink(target)
	if err != nil {
		return "", err
	}
	defer syscall.Close(parentFD)
	base := filepath.Base(filepath.Clean(target.Path))
	fd, err := syscall.Openat(parentFD, base, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("open git file: %w", err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(target.Repository, target.Path))
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

func acquireTargetLock(ctx context.Context, target Target) (func(), error) {
	sum := sha256.Sum256([]byte(target.Repository + "\x00" + target.Path))
	path := filepath.Join(os.TempDir(), fmt.Sprintf("ackos-target-%x.lock", sum))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {

		return nil, fmt.Errorf("open ackOS target lock: %w", err)

	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)

		if err == nil {

			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()

			}, nil

		}

		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			_ = file.Close()

			return nil, fmt.Errorf("acquire ackOS target lock: %w", err)

		}
		select {
		case <-ctx.Done():
			_ = file.Close()

			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):

		}

	}
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

func openRepositoryRoot(target Target) (int, error) {
	if target.repositoryDev == 0 || target.repositoryIno == 0 {
		return -1, fmt.Errorf("configured repository identity is unavailable")
	}
	fd, err := syscall.Open(target.Repository, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open git repository directory: %w", err)
	}
	var rootStat syscall.Stat_t
	if err := syscall.Fstat(fd, &rootStat); err != nil {
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("stat opened git repository directory: %w", err)
	}
	if uint64(rootStat.Dev) != target.repositoryDev || uint64(rootStat.Ino) != target.repositoryIno {
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("configured repository identity changed")
	}
	return fd, nil
}

func openParentDirNoSymlink(target Target) (int, error) {
	cleanPath := filepath.Clean(target.Path)
	if filepath.IsAbs(cleanPath) || cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {

		return -1, fmt.Errorf("git target path escapes repository")

	}
	fd, err := openRepositoryRoot(target)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(filepath.Dir(cleanPath), string(filepath.Separator)) {

		if part == "." || part == "" {
			continue

		}
		next, err := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)

		if err != nil {
			_ = syscall.Close(fd)

			return -1, fmt.Errorf("open git target parent directory: %w", err)

		}
		_ = syscall.Close(fd)
		fd = next

	}
	return fd, nil
}

func atomicWriteTarget(target Target, expected, content []byte) error {
	parentFD, err := openParentDirNoSymlink(target)
	if err != nil {
		return err
	}
	defer syscall.Close(parentFD)

	name := filepath.Base(filepath.Clean(target.Path))
	fd, err := syscall.Openat(parentFD, name, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open git target for update: %w", err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(target.Repository, target.Path))
	if file == nil {
		_ = syscall.Close(fd)
		return fmt.Errorf("open git target for update: invalid file descriptor")
	}
	defer file.Close()

	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("lock git target for update: %w", err)
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat git target for update: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("git target is not a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("inspect git target identity")
	}
	if stat.Nlink > 1 {
		return fmt.Errorf("git target has multiple hard links")
	}
	path := filepath.Join(target.Repository, target.Path)
	if err := rejectUnpreservableMetadata(path, info); err != nil {
		return err
	}
	mode := info.Mode()
	current, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("read git target before replacement: %w", err)
	}
	if !bytes.Equal(current, expected) {
		return fmt.Errorf("git target changed before replacement")
	}
	if bytes.Equal(current, content) {
		return fmt.Errorf("git target already contains requested state")
	}

	tmpName := "." + name + ".ackos-tmp"
	tmpFD, err := syscall.Openat(parentFD, tmpName, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return fmt.Errorf("create git target replacement: %w", err)
	}
	cleanup := true
	defer func() {
		if tmpFD >= 0 {
			_ = syscall.Close(tmpFD)
		}
		if cleanup {
			_ = syscall.Unlinkat(parentFD, tmpName)
		}
	}()
	for len(content) > 0 {
		n, err := syscall.Write(tmpFD, content)
		if err != nil {
			return fmt.Errorf("write git target replacement: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("write git target replacement: short write")
		}
		content = content[n:]
	}
	if err := syscall.Fsync(tmpFD); err != nil {
		return fmt.Errorf("sync git target replacement: %w", err)
	}
	if err := syscall.Fchmod(tmpFD, uint32(mode.Perm())); err != nil {
		return fmt.Errorf("restore git target mode: %w", err)
	}
	if err := syscall.Close(tmpFD); err != nil {
		return fmt.Errorf("close git target replacement: %w", err)
	}
	tmpFD = -1
	// Exchange the prepared inode with the current directory entry atomically.
	// The exchanged-out inode is then compared with the inode we validated before
	// the write. A concurrent replacement therefore fails without being clobbered.
	if err := unix.Renameat2(parentFD, tmpName, parentFD, name, unix.RENAME_EXCHANGE); err != nil {
		return fmt.Errorf("atomically compare-and-replace git target: %w", err)
	}
	exchangedInfo, err := os.Stat(filepath.Join(filepath.Dir(path), tmpName))
	if err != nil {
		return fmt.Errorf("inspect exchanged git target: %w", err)
	}
	exchangedStat, ok := exchangedInfo.Sys().(*syscall.Stat_t)
	if !ok || uint64(exchangedStat.Dev) != uint64(stat.Dev) || uint64(exchangedStat.Ino) != uint64(stat.Ino) {
		if err := unix.Renameat2(parentFD, tmpName, parentFD, name, unix.RENAME_EXCHANGE); err != nil {
			return fmt.Errorf("restore concurrently replaced git target: %w", err)
		}
		return fmt.Errorf("git target changed before atomic replacement")
	}
	exchangedFD, err := syscall.Openat(parentFD, tmpName, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open exchanged git target: %w", err)
	}
	exchangedFile := os.NewFile(uintptr(exchangedFD), filepath.Join(filepath.Dir(path), tmpName))
	if exchangedFile == nil {
		_ = syscall.Close(exchangedFD)
		return fmt.Errorf("open exchanged git target: invalid file descriptor")
	}
	exchangedContent, readErr := io.ReadAll(exchangedFile)
	closeErr := exchangedFile.Close()
	if readErr != nil {
		return fmt.Errorf("read exchanged git target: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close exchanged git target: %w", closeErr)
	}
	if !bytes.Equal(exchangedContent, expected) {
		if err := unix.Renameat2(parentFD, tmpName, parentFD, name, unix.RENAME_EXCHANGE); err != nil {
			return fmt.Errorf("restore concurrently modified git target: %w", err)
		}
		return fmt.Errorf("git target content changed before atomic replacement")
	}
	if err := syscall.Unlinkat(parentFD, tmpName); err != nil {
		return fmt.Errorf("remove exchanged git target: %w", err)
	}
	cleanup = false
	if err := syscall.Fsync(parentFD); err != nil {
		return fmt.Errorf("sync git target directory: %w", err)
	}
	return nil
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

		if _, err := runGitTarget(ctx, target, "var", identity); err != nil {

			return fmt.Errorf("Git commit identity is not configured: %s: %w", identity, err)

		}

	}
	return nil
}

func requireIndexUnlocked(ctx context.Context, target Target) error {
	if target.gitDirPath == "" {
		return fmt.Errorf("captured Git metadata identity is unavailable")
	}
	path := filepath.Join(target.gitDirPath, "index.lock")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("Git index is locked")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect Git index lock: %w", err)
	}
	return nil
}

func requireNoInProgressGitOperation(ctx context.Context, target Target) error {
	if target.gitDirPath == "" {
		return fmt.Errorf("captured Git metadata identity is unavailable")
	}
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "sequencer", "rebase-merge", "rebase-apply"} {
		path := filepath.Join(target.gitDirPath, marker)
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

type gitMetadataIdentity struct {
	gitDirPath       string
	gitDirDev        uint64
	gitDirIno        uint64
	gitCommonDirPath string
	gitCommonDirDev  uint64
	gitCommonDirIno  uint64
}

func captureGitMetadataIdentity(ctx context.Context, target Target) (gitMetadataIdentity, error) {
	return readGitMetadataIdentity(ctx, target)
}

func requireGitMetadataIdentity(ctx context.Context, target Target) error {
	if target.gitDirPath == "" || target.gitCommonDirPath == "" || target.gitDirDev == 0 || target.gitDirIno == 0 || target.gitCommonDirDev == 0 || target.gitCommonDirIno == 0 {
		return fmt.Errorf("captured Git metadata identity is unavailable")
	}
	current, err := readGitMetadataIdentity(ctx, target)
	if err != nil {
		return err
	}
	if current.gitDirPath != target.gitDirPath ||
		current.gitDirDev != target.gitDirDev ||
		current.gitDirIno != target.gitDirIno ||
		current.gitCommonDirPath != target.gitCommonDirPath ||
		current.gitCommonDirDev != target.gitCommonDirDev ||
		current.gitCommonDirIno != target.gitCommonDirIno {
		return fmt.Errorf("Git metadata directory identity changed")
	}
	return nil
}

func readGitMetadataIdentity(ctx context.Context, target Target) (gitMetadataIdentity, error) {
	resolve := func(raw string) (string, uint64, uint64, error) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return "", 0, 0, fmt.Errorf("Git metadata directory is empty")
		}
		if !filepath.IsAbs(raw) {
			raw = filepath.Join(target.Repository, raw)
		}
		resolved, err := filepath.EvalSymlinks(raw)
		if err != nil {
			return "", 0, 0, fmt.Errorf("resolve Git metadata directory identity: %w", err)
		}
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return "", 0, 0, fmt.Errorf("resolve Git metadata directory path: %w", err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return "", 0, 0, fmt.Errorf("stat Git metadata directory: %w", err)
		}
		if !info.IsDir() {
			return "", 0, 0, fmt.Errorf("Git metadata path is not a directory")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Dev == 0 || stat.Ino == 0 {
			return "", 0, 0, fmt.Errorf("Git metadata directory identity is unavailable")
		}
		return resolved, uint64(stat.Dev), uint64(stat.Ino), nil
	}
	gitDir, err := runGit(ctx, target.Repository, "rev-parse", "--git-dir")
	if err != nil {
		return gitMetadataIdentity{}, fmt.Errorf("resolve Git metadata directory: %w", err)
	}
	commonDir, err := runGit(ctx, target.Repository, "rev-parse", "--git-common-dir")
	if err != nil {
		return gitMetadataIdentity{}, fmt.Errorf("resolve Git common metadata directory: %w", err)
	}
	gitPath, gitDev, gitIno, err := resolve(gitDir)
	if err != nil {
		return gitMetadataIdentity{}, err
	}
	commonPath, commonDev, commonIno, err := resolve(commonDir)
	if err != nil {
		return gitMetadataIdentity{}, err
	}
	return gitMetadataIdentity{
		gitDirPath: gitPath, gitDirDev: gitDev, gitDirIno: gitIno, gitCommonDirPath: commonPath, gitCommonDirDev: commonDev, gitCommonDirIno: commonIno,
	}, nil
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
	info, err := os.Stat(configured)
	if err != nil {

		return fmt.Errorf("revalidate configured repository identity: %w", err)

	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Dev == 0 || stat.Ino == 0 || target.repositoryDev == 0 || target.repositoryIno == 0 {

		return fmt.Errorf("configured repository identity is unavailable")

	}
	if uint64(stat.Dev) != target.repositoryDev || uint64(stat.Ino) != target.repositoryIno {

		return fmt.Errorf("configured repository identity changed")

	}
	if err := requireGitMetadataIdentity(ctx, target); err != nil {
		return err
	}
	return nil
}
func rejectAttributesTarget(ctx context.Context, target Target) error {
	if strings.EqualFold(filepath.Base(target.Path), ".gitattributes") {

		return fmt.Errorf("git .gitattributes targets are not supported because the target can change its own filter environment")

	}
	configured, err := filepath.Abs(filepath.Join(target.Repository, target.Path))
	if err != nil {

		return fmt.Errorf("resolve configured target path: %w", err)

	}
	configuredResolved, resolveErr := filepath.EvalSymlinks(configured)
	if resolveErr == nil {
		configuredResolved, resolveErr = filepath.Abs(configuredResolved)
		if resolveErr != nil {
			return fmt.Errorf("resolve configured target identity path: %w", resolveErr)
		}
	}

	// Git loads a default per-user attributes file even when
	// core.attributesFile is unset. A tracked target at either default
	// location must therefore be rejected before mutation.
	defaultAttrs := make([]string, 0, 2)
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		defaultAttrs = append(defaultAttrs, filepath.Join(xdg, "git", "attributes"))
	} else if home, homeErr := os.UserHomeDir(); homeErr == nil {
		defaultAttrs = append(defaultAttrs, filepath.Join(home, ".config", "git", "attributes"))
	}
	for _, candidate := range defaultAttrs {
		candidateAbs, absErr := filepath.Abs(candidate)
		if absErr != nil {
			return fmt.Errorf("resolve default attributes file: %w", absErr)
		}
		candidateResolved, candidateErr := filepath.EvalSymlinks(candidateAbs)
		if candidateErr == nil {
			candidateResolved, candidateErr = filepath.Abs(candidateResolved)
			if candidateErr != nil {
				return fmt.Errorf("resolve default attributes identity path: %w", candidateErr)
			}
			if configuredResolved == candidateResolved {
				return fmt.Errorf("git target is configured as the active attributes file")
			}
		} else if candidateAbs == configured {
			return fmt.Errorf("git target is configured as the active attributes file")
		}
	}

	attrs, err := runGitTarget(ctx, target, "config", "--path", "--get", "core.attributesFile")
	if err != nil {
		return nil
	}
	attrs = strings.TrimSpace(attrs)
	if attrs == "" {
		return nil
	}
	if !filepath.IsAbs(attrs) {
		attrs = filepath.Join(target.Repository, attrs)

	}
	actual, err := filepath.Abs(attrs)
	if err != nil {

		return fmt.Errorf("resolve configured attributes file: %w", err)

	}
	configuredResolved, err = filepath.EvalSymlinks(configured)
	if err != nil {

		return fmt.Errorf("resolve configured target identity: %w", err)

	}
	actualResolved, err := filepath.EvalSymlinks(actual)
	if err != nil {

		return fmt.Errorf("resolve configured attributes identity: %w", err)

	}
	configuredResolved, err = filepath.Abs(configuredResolved)
	if err != nil {

		return fmt.Errorf("resolve configured target identity path: %w", err)

	}
	actualResolved, err = filepath.Abs(actualResolved)
	if err != nil {

		return fmt.Errorf("resolve configured attributes identity path: %w", err)

	}
	if configuredResolved == actualResolved {

		return fmt.Errorf("git target is configured as the active attributes file")

	}
	return nil
}

func rejectGitConfigTarget(ctx context.Context, target Target) error {
	configured, err := filepath.Abs(filepath.Join(target.Repository, target.Path))
	if err != nil {

		return fmt.Errorf("resolve configured Git target path: %w", err)

	}
	configured, err = filepath.EvalSymlinks(configured)
	if err != nil {

		return fmt.Errorf("resolve configured Git target identity: %w", err)

	}
	configured, err = filepath.Abs(configured)
	if err != nil {

		return fmt.Errorf("resolve configured Git target identity path: %w", err)

	}

	queue := make([]string, 0, 8)
	seen := make(map[string]struct{})
	addSource := func(path string) {

		if path == "" {

			return

		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(target.Repository, path)

		}

		if abs, absErr := filepath.Abs(path); absErr == nil {
			path = abs

		}

		if _, ok := seen[path]; ok {

			return

		}
		seen[path] = struct{}{}
		queue = append(queue, path)

	}

	// Empty global/system config files do not appear in --show-origin --list,
	// but they remain active configuration sources. Enumerate their active
	// file locations explicitly so an empty target cannot become dangerous
	// after it is replaced.
	if global := os.Getenv("GIT_CONFIG_GLOBAL"); global != "" {
		addSource(global)
	}
	if os.Getenv("GIT_CONFIG_NOSYSTEM") == "" {
		if system := os.Getenv("GIT_CONFIG_SYSTEM"); system != "" {
			addSource(system)
		} else {
			addSource("/etc/gitconfig")
		}
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		addSource(filepath.Join(home, ".gitconfig"))
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			addSource(filepath.Join(xdg, "git", "config"))
		} else {
			addSource(filepath.Join(home, ".config", "git", "config"))
		}
	}

	output, err := runGitTarget(ctx, target, "config", "--includes", "--show-origin", "--list")
	if err != nil {

		return fmt.Errorf("inspect Git configuration sources: %w", err)

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

		addSource(strings.TrimPrefix(origin, "file:"))

	}
	addSource(filepath.Join(target.gitDirPath, "config"))
	addSource(filepath.Join(target.gitCommonDirPath, "config"))

	for len(queue) > 0 {
		source := queue[0]
		queue = queue[1:]
		resolvedSource, resolveErr := filepath.EvalSymlinks(source)

		if resolveErr != nil {
			continue

		}
		resolvedSource, resolveErr = filepath.Abs(resolvedSource)

		if resolveErr != nil {

			return fmt.Errorf("resolve Git configuration source identity: %w", resolveErr)

		}

		if resolvedSource == configured {

			return fmt.Errorf("git target is an active Git configuration source")

		}

		includeOutput, includeErr := runGitTarget(ctx, target, "config", "--file", resolvedSource, "--path", "--null", "--get-regexp", `^include.*\.path$`)

		if includeErr != nil {
			continue

		}
		includeParts := strings.Split(strings.TrimSuffix(includeOutput, "\x00"), "\x00")
		for _, record := range includeParts {
			_, include, ok := strings.Cut(record, "\n")
			if !ok {
				return fmt.Errorf("unexpected Git include path metadata")
			}
			include = strings.TrimSpace(include)
			if include == "" {
				continue
			}
			if !filepath.IsAbs(include) {
				include = filepath.Join(filepath.Dir(resolvedSource), include)
			}
			addSource(include)
		}
	}
	return nil
}

func rejectSubmodules(ctx context.Context, target Target) error {
	output, err := runGitTarget(ctx, target, "ls-files", "-z", "--stage")
	if err != nil {
		return fmt.Errorf("inspect Git submodules: %w", err)
	}
	parts := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	for _, record := range parts {
		if record == "" {
			continue
		}
		meta, path, ok := strings.Cut(record, "	")
		if !ok {
			return fmt.Errorf("unexpected Git index metadata")
		}
		fields := strings.Fields(meta)
		if len(fields) >= 3 && fields[0] == "160000" && fields[2] == "0" {
			return fmt.Errorf("Git submodules are not supported")
		}
		_ = path
	}
	return nil
}

func rejectGrafts(ctx context.Context, target Target) error {
	if target.gitCommonDirPath == "" {
		return fmt.Errorf("captured Git metadata identity is unavailable")
	}
	path := filepath.Join(target.gitCommonDirPath, "info", "grafts")
	if _, err := os.Stat(path); err == nil {

		return fmt.Errorf("Git graft file is not supported")

	} else if !os.IsNotExist(err) {

		return fmt.Errorf("inspect Git graft file: %w", err)

	}
	return nil
}

func rejectReplaceRefs(ctx context.Context, target Target) error {
	output, err := runGitTarget(ctx, target, "replace", "-l")
	if err != nil {
		return fmt.Errorf("inspect Git replacement refs: %w", err)
	}
	if strings.TrimSpace(output) != "" {
		return fmt.Errorf("git replacement refs are active; replacement objects are not supported")
	}
	return nil
}

func rejectConfiguredFilters(ctx context.Context, target Target) error {
	paths, err := runGitTarget(ctx, target, "ls-files", "-z", "--cached")
	if err != nil {

		return fmt.Errorf("inspect tracked Git paths: %w", err)

	}
	if strings.TrimSuffix(paths, "\x00") == "" {

		return nil

	}
	attrs, err := runGitTargetInput(ctx, target, []byte(paths), "check-attr", "-z", "--stdin", "filter")
	if err != nil {

		return fmt.Errorf("inspect Git clean filters: %w", err)

	}
	parts := strings.Split(strings.TrimSuffix(attrs, "\x00"), "\x00")
	if len(parts)%3 != 0 {

		return fmt.Errorf("unexpected Git clean filter metadata")

	}
	configuredDrivers := make(map[string]struct{})
	filterDrivers, filterErr := runGitTarget(ctx, target, "config", "--includes", "--name-only", "--get-regexp", "^filter\\..*\\.(clean|process)$")
	if filterErr == nil {
		for _, name := range strings.Split(filterDrivers, "\n") {
			name = strings.TrimSpace(name)
			if !strings.HasPrefix(name, "filter.") {
				continue
			}
			name = strings.TrimPrefix(name, "filter.")
			if driver, _, ok := strings.Cut(name, "."); ok {
				configuredDrivers[driver] = struct{}{}
			}
		}
	}
	for i := 0; i < len(parts); i += 3 {
		if parts[i+1] != "filter" {
			return fmt.Errorf("unexpected Git clean filter metadata")
		}
		value := parts[i+2]
		if value == "unspecified" || value == "unset" {
			if _, configured := configuredDrivers[value]; !configured {
				continue
			}
		}
		return fmt.Errorf("git repository uses a configured clean filter; filtered repositories are not supported")
	}
	return nil
}

func targetGitAttr(ctx context.Context, target Target) (string, error) {
	output, err := runGitTarget(ctx, target, "check-attr", "-z", "filter", "--", target.Path)
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
	autocrlf, err := runGitTarget(ctx, target, "config", "--get", "core.autocrlf")
	if err == nil {
		raw := strings.ToLower(strings.TrimSpace(autocrlf))

		if raw == "input" {

			return fmt.Errorf("git target uses core.autocrlf normalization; normalized targets are not supported")

		}
		parsed, boolErr := runGitTarget(ctx, target, "config", "--bool", "--get", "core.autocrlf")

		if boolErr == nil && strings.EqualFold(strings.TrimSpace(parsed), "true") {

			return fmt.Errorf("git target uses core.autocrlf normalization; normalized targets are not supported")

		}

	}
	output, err := runGitTarget(ctx, target, "check-attr", "-z", "text", "eol", "crlf", "ident", "working-tree-encoding", "--", target.Path)
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

func commitVerifiedTree(ctx context.Context, target Target, parent, headRef, afterHash string, content []byte, message string) error {
	blob, err := runGitTargetInput(ctx, target, content, "hash-object", "-w", "--stdin")
	if err != nil {

		return fmt.Errorf("store authorized Git blob: %w", err)

	}
	blob = strings.TrimSpace(blob)
	if blob != afterHash {

		return fmt.Errorf("authorized Git blob hash changed before commit")

	}
	mode, err := gitTreeMode(ctx, target, parent)
	if err != nil {

		return fmt.Errorf("read parent target Git mode: %w", err)

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
	if _, err := runGitTargetWithEnv(ctx, target, env, "--no-replace-objects", "read-tree", parent); err != nil {

		return fmt.Errorf("capture Git parent index: %w", err)

	}
	if _, err := runGitTargetWithEnv(ctx, target, env, "update-index", "--add", "--cacheinfo", mode, blob, target.Path); err != nil {

		return fmt.Errorf("install authorized target in temporary Git index: %w", err)

	}
	tree, err := runGitTargetWithEnv(ctx, target, env, "write-tree")
	if err != nil {

		return fmt.Errorf("write authorized Git tree: %w", err)

	}
	commit, err := runGitTarget(ctx, target, "commit-tree", strings.TrimSpace(tree), "-p", parent, "-m", message, "--no-gpg-sign")
	if err != nil {

		return fmt.Errorf("create authorized Git commit: %w", err)

	}
	commit = strings.TrimSpace(commit)
	if commit == "" {

		return fmt.Errorf("Git commit object is missing")
	}
	currentRef, err := runGitTarget(ctx, target, "symbolic-ref", "-q", "HEAD")
	if err != nil {

		return fmt.Errorf("re-read Git HEAD branch before commit: %w", err)

	}
	if strings.TrimSpace(currentRef) != headRef {

		return fmt.Errorf("Git HEAD branch changed before commit")

	}
	if err := updateCapturedRef(ctx, target, headRef, commit, parent); err != nil {

		return fmt.Errorf("atomically install authorized Git commit on captured branch: %w", err)
	}
	currentRef, err = runGitTarget(ctx, target, "symbolic-ref", "-q", "HEAD")
	if err != nil {

		return fmt.Errorf("re-read Git HEAD branch after commit: %w", err)

	}
	if strings.TrimSpace(currentRef) != headRef {

		return fmt.Errorf("Git HEAD branch changed during commit")

	}
	if _, err := runGitTarget(ctx, target, "add", "--", literalPathspec(target.Path)); err != nil {

		return fmt.Errorf("synchronize Git index after commit: %w", err)

	}
	return nil
}

func updateCapturedRef(ctx context.Context, target Target, headRef, commit, parent string) error {
	if _, err := runGitTarget(ctx, target, "update-ref", "--no-deref", headRef, commit, parent); err != nil {
		return err
	}
	return nil
}

func verifyCommit(e Executor, ctx context.Context, parent string, t kernel.Transition, executionID string) error {
	return verifyCommitAt(ctx, e.Target, func(args ...string) (string, error) { return e.git(ctx, args...) }, parent, t, executionID)
}

func verifyLatestCommit(v Verifier, ctx context.Context, expectedParent string, t kernel.Transition, executionID string) error {
	return verifyCommitAt(ctx, v.Target, func(args ...string) (string, error) { return v.git(ctx, args...) }, expectedParent, t, executionID)
}
func verifyLiveIndexMatchesHead(ctx context.Context, target Target, head string) error {
	status, err := runGitTarget(ctx, target, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("read Git worktree status for verification: %w", err)
	}
	if status != "" {
		return fmt.Errorf("Git worktree is not clean during verification")
	}
	indexTree, err := runGitTarget(ctx, target, "write-tree")
	if err != nil {
		return fmt.Errorf("write live Git index tree for verification: %w", err)
	}
	expectedTree, err := runGitTarget(ctx, target, "--no-replace-objects", "rev-parse", head+"^{tree}")
	if err != nil {
		return fmt.Errorf("read verified Git tree for index verification: %w", err)
	}
	if strings.TrimSpace(indexTree) != strings.TrimSpace(expectedTree) {
		return fmt.Errorf("Git index contains unauthorized staged content")
	}
	return nil
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
	message, err := safeGit("log", "-1", "--format=%B", head)
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
	parentMode, err := gitTreeMode(ctx, target, head+"^")
	if err != nil {

		return fmt.Errorf("read committed Git parent mode: %w", err)

	}
	targetMode, err := gitTreeMode(ctx, target, head)
	if err != nil {

		return fmt.Errorf("read committed Git target mode: %w", err)

	}
	if parentMode != targetMode {

		return fmt.Errorf("committed Git target mode changed unexpectedly")

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

func liveTargetMode(target Target) (string, error) {
	parentFD, err := openParentDirNoSymlink(target)
	if err != nil {

		return "", err

	}
	defer syscall.Close(parentFD)
	fd, err := syscall.Openat(parentFD, filepath.Base(filepath.Clean(target.Path)), syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {

		return "", fmt.Errorf("open Git target for mode check: %w", err)

	}
	file := os.NewFile(uintptr(fd), filepath.Join(target.Repository, target.Path))
	if file == nil {
		_ = syscall.Close(fd)

		return "", fmt.Errorf("open Git target for mode check: invalid file descriptor")

	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {

		return "", fmt.Errorf("stat Git target for mode check: %w", err)
	}
	if !info.Mode().IsRegular() {

		return "", fmt.Errorf("Git target is not a regular file")

	}
	if info.Mode().Perm()&0o111 != 0 {

		return "100755", nil

	}
	return "100644", nil
}

func gitIndexMode(ctx context.Context, target Target) (string, error) {
	output, err := runGitTarget(ctx, target, "ls-files", "--stage", "-z", "--", literalPathspec(target.Path))
	if err != nil {

		return "", err

	}
	output = strings.TrimSuffix(output, "\x00")
	records := strings.Split(output, "\x00")
	if len(records) != 1 {

		return "", fmt.Errorf("unexpected Git index metadata")

	}
	meta, path, ok := strings.Cut(records[0], "\t")
	if !ok || path != target.Path {

		return "", fmt.Errorf("unexpected Git index target metadata")

	}
	fields := strings.Fields(meta)
	if len(fields) != 3 || fields[2] != "0" || (fields[0] != "100644" && fields[0] != "100755") {

		return "", fmt.Errorf("target Git index entry is not a regular file")

	}
	return fields[0], nil
}

func gitIndexHash(ctx context.Context, target Target) (string, error) {
	output, err := runGitTarget(ctx, target, "ls-files", "--stage", "-z", "--", literalPathspec(target.Path))
	if err != nil {

		return "", err

	}
	output = strings.TrimSuffix(output, "\x00")
	meta, path, ok := strings.Cut(output, "\t")
	if !ok || path != target.Path {

		return "", fmt.Errorf("unexpected Git index target metadata")

	}
	fields := strings.Fields(meta)
	if len(fields) != 3 || fields[2] != "0" {

		return "", fmt.Errorf("target Git index entry is not stage 0")

	}
	return fields[1], nil
}

func gitTreeMode(ctx context.Context, target Target, tree string) (string, error) {
	output, err := runGitTarget(ctx, target, "--no-replace-objects", "ls-tree", "--format=%(objectmode)", tree, "--", literalPathspec(target.Path))
	if err != nil {

		return "", err

	}
	lines := strings.Fields(output)
	if len(lines) != 1 || (lines[0] != "100644" && lines[0] != "100755") {

		return "", fmt.Errorf("target Git tree entry is not a regular file")

	}
	return lines[0], nil
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
	hash, err := runGitTarget(ctx, target, "hash-object", "--no-filters", name)
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

func openGitMetadataDir(path string, expectedDev, expectedIno uint64) (int, error) {
	if path == "" || expectedDev == 0 || expectedIno == 0 {
		return -1, fmt.Errorf("captured Git metadata identity is unavailable")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open captured Git metadata directory: %w", err)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("stat captured Git metadata directory: %w", err)
	}
	if uint64(stat.Dev) != expectedDev || uint64(stat.Ino) != expectedIno {
		_ = syscall.Close(fd)
		return -1, fmt.Errorf("Git metadata directory identity changed")
	}
	return fd, nil
}

func runGitTarget(ctx context.Context, target Target, args ...string) (string, error) {
	return runGitTargetWithInput(ctx, target, nil, nil, args...)
}

func runGitTargetInput(ctx context.Context, target Target, input []byte, args ...string) (string, error) {
	return runGitTargetWithInput(ctx, target, input, nil, args...)
}

func runGitTargetWithEnv(ctx context.Context, target Target, env map[string]string, args ...string) (string, error) {
	return runGitTargetWithInput(ctx, target, nil, env, args...)
}

func runGitTargetWithInput(ctx context.Context, target Target, input []byte, overrides map[string]string, args ...string) (string, error) {
	rootFD, err := openRepositoryRoot(target)
	if err != nil {
		return "", err
	}
	defer syscall.Close(rootFD)
	gitFD, err := openGitMetadataDir(target.gitDirPath, target.gitDirDev, target.gitDirIno)
	if err != nil {
		return "", err
	}
	defer syscall.Close(gitFD)
	commonFD, err := openGitMetadataDir(target.gitCommonDirPath, target.gitCommonDirDev, target.gitCommonDirIno)
	if err != nil {
		return "", err
	}
	defer syscall.Close(commonFD)
	anchored := make(map[string]string, len(overrides)+2)
	for key, value := range overrides {
		anchored[key] = value
	}
	anchored["GIT_DIR"] = fmt.Sprintf("/proc/self/fd/%d", gitFD)
	anchored["GIT_COMMON_DIR"] = fmt.Sprintf("/proc/self/fd/%d", commonFD)
	anchored["GIT_WORK_TREE"] = fmt.Sprintf("/proc/self/fd/%d", rootFD)
	return runGitWithInput(ctx, fmt.Sprintf("/proc/self/fd/%d", rootFD), input, anchored, args...)
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
