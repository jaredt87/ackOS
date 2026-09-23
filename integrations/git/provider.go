// Package git provides a small local Git resource provider for ackOS.
package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jaredt87/ackOS/kernel"
)

type Provider struct {
	Repository string
	Branch     string
	repoDev    uint64
	repoIno    uint64
	state      *executionState
}

type executionState struct {
	mu           sync.Mutex
	parents      map[string]string
	commits      map[string]string
	observations map[string]string
}

func NewProvider(repository, branch string) (Provider, error) {
	if repository == "" || branch == "" {
		return Provider{}, fmt.Errorf("repository and branch are required")
	}
	info, err := os.Stat(repository)
	if err != nil || !info.IsDir() {
		return Provider{}, fmt.Errorf("invalid Git repository: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Provider{}, fmt.Errorf("cannot identify Git repository")
	}
	if _, err := runGit(context.Background(), repository, "rev-parse", "--git-dir"); err != nil {
		return Provider{}, fmt.Errorf("invalid Git repository: %w", err)
	}
	branch = strings.TrimSpace(branch)
	if strings.HasPrefix(branch, "refs/heads/") {
		branch = strings.TrimPrefix(branch, "refs/heads/")
	}
	if _, err := runGit(context.Background(), repository, "check-ref-format", "--branch", branch); err != nil {
		return Provider{}, fmt.Errorf("invalid Git branch: %w", err)
	}
	return Provider{
		Repository: repository,
		Branch:     branch,
		repoDev:    uint64(stat.Dev),
		repoIno:    uint64(stat.Ino),
		state:      &executionState{parents: make(map[string]string), commits: make(map[string]string), observations: make(map[string]string)},
	}, nil
}

func (p Provider) validateRepository() error {
	info, err := os.Stat(p.Repository)
	if err != nil {
		return fmt.Errorf("configured Git repository unavailable: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != p.repoDev || uint64(stat.Ino) != p.repoIno {
		return fmt.Errorf("configured Git repository changed")
	}
	return nil
}

func observationKey(fingerprint string) string {
	return fingerprint
}

func (p Provider) rememberObservation(fingerprint, head string) {
	if p.state == nil {
		return
	}
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	p.state.observations[observationKey(fingerprint)] = head
}

func (p Provider) observationHead(fingerprint string) (string, bool) {
	if p.state == nil {
		return "", false
	}
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	head, ok := p.state.observations[observationKey(fingerprint)]
	return head, ok
}

func (p Provider) forgetObservation(fingerprint string) {
	if p.state == nil {
		return
	}
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	delete(p.state.observations, observationKey(fingerprint))
}

func (p Provider) rememberParent(executionID, parent string) error {
	if p.state == nil {
		return fmt.Errorf("Git execution state is unavailable")
	}
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	if executionID == "" {
		return fmt.Errorf("execution authority ID is required")
	}
	p.state.parents[executionID] = parent
	return nil
}

func (p Provider) parent(executionID string) (string, bool) {
	if p.state == nil {
		return "", false
	}
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	parent, ok := p.state.parents[executionID]
	return parent, ok
}

func (p Provider) rememberCommit(executionID, commit string) {
	if p.state == nil {
		return
	}
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	p.state.commits[executionID] = commit
}

func (p Provider) commit(executionID string) (string, bool) {
	if p.state == nil {
		return "", false
	}
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	commit, ok := p.state.commits[executionID]
	return commit, ok
}

func (p Provider) forgetExecution(executionID string) {
	if p.state == nil {
		return
	}
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	delete(p.state.parents, executionID)
	delete(p.state.commits, executionID)
}

func (p Provider) forgetParent(executionID string) {
	if p.state == nil {
		return
	}
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	delete(p.state.parents, executionID)
}

type Observer struct{ Provider Provider }
type Executor struct{ Provider Provider }
type Verifier struct{ Provider Provider }
type RecoveryObserver struct{ Provider Provider }

func (o Observer) Observe(ctx context.Context, subject string) (kernel.Observation, error) {
	if err := o.Provider.validateRepository(); err != nil {
		return kernel.Observation{}, err
	}
	head, blob, err := o.Provider.observe(ctx, subject)
	if err != nil {
		return kernel.Observation{}, err
	}
	observation, err := kernel.NewObservation(subject, blob, 0, time.Now().UTC())
	if err != nil {
		return kernel.Observation{}, err
	}
	o.Provider.rememberObservation(observation.Fingerprint, head)
	return observation, nil
}

func (o RecoveryObserver) Observe(ctx context.Context, subject string) (kernel.Observation, error) {
	return (Observer{Provider: o.Provider}).Observe(ctx, subject)
}

func (e Executor) Execute(ctx context.Context, t kernel.Transition, a kernel.Authority) kernel.ExecutionResult {
	fail := func(err error) kernel.ExecutionResult { return kernel.ExecutionResult{Message: err.Error()} }
	if a.ExecutionID == "" {
		return fail(fmt.Errorf("execution authority ID is required"))
	}
	if err := validateSubject(t.Subject); err != nil {
		return fail(err)
	}
	if err := e.Provider.validateRepository(); err != nil {
		return fail(err)
	}
	expectedHead, ok := e.Provider.observationHead(t.ObservationFingerprint)
	if !ok {
		return fail(fmt.Errorf("Git observation is unavailable"))
	}
	head, err := branchHead(ctx, e.Provider.Repository, e.Provider.Branch)
	if err != nil {
		e.Provider.forgetObservation(t.ObservationFingerprint)
		return fail(err)
	}
	if head != expectedHead {
		e.Provider.forgetObservation(t.ObservationFingerprint)
		return fail(fmt.Errorf("Git branch tip changed since observation"))
	}
	before, err := treeBlob(ctx, e.Provider.Repository, head, t.Subject)
	if err != nil {
		e.Provider.forgetObservation(t.ObservationFingerprint)
		return fail(err)
	}
	if before != t.Before {
		e.Provider.forgetObservation(t.ObservationFingerprint)
		return fail(fmt.Errorf("Git target does not match Transition.Before"))
	}
	if err := materializeBlob(ctx, e.Provider.Repository, t.After); err != nil {
		e.Provider.forgetObservation(t.ObservationFingerprint)
		return fail(err)
	}
	if err := e.Provider.rememberParent(a.ExecutionID, head); err != nil {
		e.Provider.forgetObservation(t.ObservationFingerprint)
		return fail(err)
	}
	tree, err := buildTree(ctx, e.Provider.Repository, head, t.Subject, t.After)
	if err != nil {
		e.Provider.forgetParent(a.ExecutionID)
		e.Provider.forgetObservation(t.ObservationFingerprint)
		return fail(err)
	}
	commit, err := createCommit(ctx, e.Provider.Repository, tree, head, a.ExecutionID)
	if err != nil {
		e.Provider.forgetParent(a.ExecutionID)
		e.Provider.forgetObservation(t.ObservationFingerprint)
		return fail(err)
	}
	if err := updateBranchCAS(ctx, e.Provider.Repository, e.Provider.Branch, head, commit); err != nil {
		e.Provider.forgetParent(a.ExecutionID)
		e.Provider.forgetObservation(t.ObservationFingerprint)
		return fail(err)
	}
	e.Provider.rememberCommit(a.ExecutionID, commit)
	e.Provider.forgetObservation(t.ObservationFingerprint)
	return kernel.ExecutionResult{Success: true, Message: "Git transition committed"}
}

func (v Verifier) verifyExecutionCommit(executionID, head string) error {
	expectedCommit, ok := v.Provider.commit(executionID)
	if !ok {
		return fmt.Errorf("Git execution commit is unavailable")
	}
	if head != expectedCommit {
		return fmt.Errorf("verified Git commit is not the commit produced by execution")
	}
	return nil
}

func (v Verifier) Verify(ctx context.Context, t kernel.Transition, a kernel.Authority) (kernel.Observation, error) {
	if a.ExecutionID == "" {
		return kernel.Observation{}, fmt.Errorf("execution authority ID is required")
	}
	if err := validateSubject(t.Subject); err != nil {
		return kernel.Observation{}, err
	}
	if err := v.Provider.validateRepository(); err != nil {
		return kernel.Observation{}, err
	}
	head, err := branchHead(ctx, v.Provider.Repository, v.Provider.Branch)
	if err != nil {
		return kernel.Observation{}, err
	}
	parentExpected, ok := v.Provider.parent(a.ExecutionID)
	if !ok {
		return kernel.Observation{}, fmt.Errorf("Git execution parent is unavailable")
	}
	if err := v.verifyExecutionCommit(a.ExecutionID, head); err != nil {
		return kernel.Observation{}, err
	}
	defer v.Provider.forgetExecution(a.ExecutionID)
	parent, err := commitParent(ctx, v.Provider.Repository, head)
	if err != nil {
		return kernel.Observation{}, err
	}
	if parent != parentExpected {
		return kernel.Observation{}, fmt.Errorf("verified Git commit parent changed")
	}
	got, err := treeBlob(ctx, v.Provider.Repository, head, t.Subject)
	if err != nil {
		return kernel.Observation{}, err
	}
	if got != t.After {
		return kernel.Observation{}, fmt.Errorf("verified Git target does not match Transition.After")
	}
	if err := verifySinglePathChange(ctx, v.Provider.Repository, parent, head, t.Subject, t.Before, t.After); err != nil {
		return kernel.Observation{}, err
	}
	msg, err := commitMessage(ctx, v.Provider.Repository, head)
	if err != nil {
		return kernel.Observation{}, err
	}
	if !hasTrailer(msg, a.ExecutionID) {
		return kernel.Observation{}, fmt.Errorf("execution trailer missing")
	}
	return kernel.NewObservation(t.Subject, got, 0, time.Now().UTC())
}

func (p Provider) observe(ctx context.Context, subject string) (string, string, error) {
	if err := validateSubject(subject); err != nil {
		return "", "", err
	}
	head, err := branchHead(ctx, p.Repository, p.Branch)
	if err != nil {
		return "", "", err
	}
	blob, err := treeBlob(ctx, p.Repository, head, subject)
	if err != nil {
		return "", "", err
	}
	return head, blob, nil
}

func validateSubject(s string) error {
	if s == "" || strings.IndexByte(s, 0) >= 0 {
		return fmt.Errorf("invalid Git path")
	}
	if strings.Contains(s, "\\") {
		return fmt.Errorf("invalid Git path")
	}
	clean := path.Clean(s)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return fmt.Errorf("Git path must be repository-relative")
	}
	if clean != s {
		return fmt.Errorf("Git path must be normalized")
	}
	return nil
}

func runGit(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func branchHead(ctx context.Context, repo, branch string) (string, error) {
	return runGit(ctx, repo, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
}

func treeBlob(ctx context.Context, repo, head, subject string) (string, error) {
	blob, err := runGit(ctx, repo, "rev-parse", "--verify", head+":./"+subject)
	if err != nil {
		return "", fmt.Errorf("read Git target: %w", err)
	}
	typ, err := runGit(ctx, repo, "cat-file", "-t", blob)
	if err != nil || typ != "blob" {
		return "", fmt.Errorf("Git target is not a blob")
	}
	return blob, nil
}

func ensureBlob(ctx context.Context, repo, blob string) error {
	typ, err := runGit(ctx, repo, "cat-file", "-t", blob)
	if err != nil || typ != "blob" {
		return fmt.Errorf("Transition.After is not an existing Git blob")
	}
	return nil
}

func materializeBlob(ctx context.Context, repo, blob string) error {
	if err := ensureBlob(ctx, repo, blob); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "cat-file", "blob", blob)
	cmd.Dir = repo
	content, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("read Transition.After blob: %w", err)
	}
	cmd = exec.CommandContext(ctx, "git", "hash-object", "-w", "--stdin")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(string(content))
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("write authorized Git blob: %w", err)
	}
	if strings.TrimSpace(string(out)) != blob {
		return fmt.Errorf("authorized Git blob ID mismatch")
	}
	return nil
}

func treeMode(ctx context.Context, repo, head, subject string) (string, error) {
	out, err := runGit(ctx, repo, "ls-tree", "-z", head, "--", subject)
	if err != nil {
		return "", fmt.Errorf("read Git target mode: %w", err)
	}
	entry := strings.TrimSuffix(out, "\x00")
	parts := strings.SplitN(entry, "	", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("Git target tree entry is unavailable")
	}
	fields := strings.Fields(parts[0])
	if len(fields) != 3 || (fields[1] != "blob" && fields[1] != "commit") {
		return "", fmt.Errorf("Git target is not a file")
	}
	return fields[0], nil
}

func buildTree(ctx context.Context, repo, head, subject, blob string) (string, error) {
	idxDir, err := os.MkdirTemp("", "ackos-git-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(idxDir)
	idx := idxDir + "/index"
	env := append(os.Environ(), "GIT_INDEX_FILE="+idx)
	if _, err := runGitEnv(ctx, repo, env, "read-tree", head); err != nil {
		return "", err
	}
	mode, err := treeMode(ctx, repo, head, subject)
	if err != nil {
		return "", err
	}
	if _, err := runGitEnv(ctx, repo, env, "update-index", "--add", "--cacheinfo", mode+","+blob+","+subject); err != nil {
		return "", err
	}
	return runGitEnv(ctx, repo, env, "write-tree")
}

func runGitEnv(ctx context.Context, repo string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func createCommit(ctx context.Context, repo, tree, parent, id string) (string, error) {
	msg := "ackOS execution\n\nAck-Execution-Id: " + id + "\n"
	cmd := exec.CommandContext(ctx, "git", "commit-tree", tree, "-p", parent)
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(msg)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("create Git commit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func updateBranchCAS(ctx context.Context, repo, branch, old, new string) error {
	_, err := runGit(ctx, repo, "update-ref", "refs/heads/"+branch, new, old)
	if err != nil {
		return fmt.Errorf("Git branch CAS failed: %w", err)
	}
	return nil
}

func commitParent(ctx context.Context, repo, commit string) (string, error) {
	return runGit(ctx, repo, "rev-parse", "--verify", commit+"^1")
}

func commitMessage(ctx context.Context, repo, commit string) (string, error) {
	return runGit(ctx, repo, "cat-file", "-p", commit)
}

func hasTrailer(message, id string) bool {
	want := "Ack-Execution-Id: " + id
	for _, line := range strings.Split(message, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func verifySinglePathChange(ctx context.Context, repo, parent, head, subject, before, after string) error {
	if before == after {
		return fmt.Errorf("transition must change the Git blob")
	}
	if parent == "" {
		return fmt.Errorf("resulting commit has no parent")
	}
	p, err := treeBlob(ctx, repo, parent, subject)
	if err != nil {
		return err
	}
	if p != before {
		return fmt.Errorf("commit parent target does not match Transition.Before")
	}
	out, err := runGit(ctx, repo, "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", parent, head)
	if err != nil {
		return err
	}
	names := strings.Split(out, "\x00")
	var changed []string
	for _, n := range names {
		if n != "" {
			changed = append(changed, n)
		}
	}
	if len(changed) != 1 || changed[0] != subject {
		return fmt.Errorf("Git commit changes paths outside configured target")
	}
	return nil
}
