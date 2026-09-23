package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jaredt87/ackOS/kernel"
)

func TestTransitionEndToEndUsesBlobIDsAndLeavesWorkingTreeAlone(t *testing.T) {
	repo, subject, p := testRepo(t, "initial")
	before := observeBlob(t, p, subject)
	after := hashBlob(t, repo, "updated")
	if before == after {
		t.Fatal("test blobs unexpectedly match")
	}

	if err := os.WriteFile(filepath.Join(repo, "unrelated.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	transition := observedTransition(t, p, subject, before, after)
	authority := kernel.Authority{ExecutionID: "exec-1"}
	result := (Executor{Provider: p}).Execute(context.Background(), transition, authority)
	if !result.Success {
		t.Fatal(result.Message)
	}

	if got := readGitWorktreeFile(t, repo, subject); got != "initial" {
		t.Fatalf("working tree changed: %q", got)
	}
	if got := readGitWorktreeFile(t, repo, "unrelated.txt"); got != "dirty" {
		t.Fatalf("unrelated working tree changed: %q", got)
	}

	observation, err := (Verifier{Provider: p}).Verify(context.Background(), transition, authority)
	if err != nil {
		t.Fatal(err)
	}
	if observation.State != after {
		t.Fatalf("verified state = %s, want %s", observation.State, after)
	}

	head := strings.TrimSpace(git(t, repo, "rev-parse", "refs/heads/main"))
	parent := strings.TrimSpace(git(t, repo, "rev-parse", head+"^1"))
	changed := strings.TrimSuffix(git(t, repo, "diff-tree", "--no-commit-id", "--name-only", "-r", parent, head), "\n")
	if changed != subject {
		t.Fatalf("changed paths = %q, want only %q", changed, subject)
	}
	body := git(t, repo, "cat-file", "-p", head)
	if !strings.Contains(body, "Ack-Execution-Id: exec-1") {
		t.Fatalf("commit missing trailer: %q", body)
	}
}

func TestExecutorRejectsStaleBeforeAfterBranchMoves(t *testing.T) {
	repo, subject, p := testRepo(t, "initial")
	before := observeBlob(t, p, subject)
	after := hashBlob(t, repo, "updated")
	transition := observedTransition(t, p, subject, before, after)
	git(t, repo, "commit", "--allow-empty", "-m", "unrelated")

	result := (Executor{Provider: p}).Execute(context.Background(), transition, kernel.Authority{ExecutionID: "exec-stale"})
	if result.Success {
		t.Fatal("stale transition succeeded")
	}
	if !strings.Contains(result.Message, "branch tip changed") {
		t.Fatalf("result = %q", result.Message)
	}
}

func TestExecutorRejectsMissingObservedBranchTip(t *testing.T) {
	repo, subject, p := testRepo(t, "initial")
	before := observeBlob(t, p, subject)
	after := hashBlob(t, repo, "updated")
	transition := observedTransition(t, p, subject, before, after)
	git(t, repo, "commit", "--allow-empty", "-m", "newer branch tip")
	result := (Executor{Provider: p}).Execute(context.Background(), transition, kernel.Authority{ExecutionID: "exec-branch"})
	if result.Success {
		t.Fatal("transition succeeded after observed branch tip moved")
	}
	if !strings.Contains(result.Message, "branch tip changed") {
		t.Fatalf("result = %q", result.Message)
	}
}

func TestBranchCASCannotOverwriteNewerTip(t *testing.T) {
	repo, _, p := testRepo(t, "initial")
	old := strings.TrimSpace(git(t, repo, "rev-parse", "refs/heads/main"))
	git(t, repo, "commit", "--allow-empty", "-m", "newer")
	newer := strings.TrimSpace(git(t, repo, "rev-parse", "refs/heads/main"))
	candidate := strings.TrimSpace(git(t, repo, "commit-tree", newer+"^{tree}", "-p", newer, "-m", "candidate"))
	if err := updateBranchCAS(context.Background(), p.Repository, p.Branch, old, candidate); err == nil {
		t.Fatal("stale branch CAS succeeded")
	}
	if got := strings.TrimSpace(git(t, repo, "rev-parse", "refs/heads/main")); got != newer {
		t.Fatalf("branch overwritten: %s", got)
	}
}

func TestVerifierRejectsFalseExecutorSuccess(t *testing.T) {
	repo, subject, p := testRepo(t, "initial")
	before := observeBlob(t, p, subject)
	after := hashBlob(t, repo, "updated")
	assertFalseVerifier(t, p, subject, before, after)
}

func TestRunGitIgnoresRepositorySelectionEnvironment(t *testing.T) {
	repo, _, _ := testRepo(t, "initial")
	other, _, _ := testRepo(t, "other")
	want := strings.TrimSpace(git(t, repo, "rev-parse", "refs/heads/main"))

	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)

	got, err := runGit(context.Background(), repo, "rev-parse", "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("branch head = %s, want %s", got, want)
	}
}

func TestTreeBlobIgnoresReplacementRefs(t *testing.T) {
	repo, subject, p := testRepo(t, "initial")
	before := observeBlob(t, p, subject)
	replacementBlob := hashBlob(t, repo, "replacement")
	replacementTree := buildTestTree(t, repo, strings.TrimSpace(git(t, repo, "rev-parse", "refs/heads/main")), subject, replacementBlob)
	head := strings.TrimSpace(git(t, repo, "rev-parse", "refs/heads/main"))
	replacement := strings.TrimSpace(git(t, repo, "commit-tree", replacementTree, "-p", head, "-m", "replacement"))
	git(t, repo, "replace", head, replacement)
	t.Cleanup(func() { _ = exec.Command("git", "-C", repo, "replace", "-d", head).Run() })

	got, err := treeBlob(context.Background(), repo, head, subject)
	if err != nil {
		t.Fatal(err)
	}
	if got != before {
		t.Fatalf("tree blob = %s, want original %s", got, before)
	}
}

func TestObservationCacheIsBounded(t *testing.T) {
	repo, _, p := testRepo(t, "initial")
	for i := 0; i < maxCachedObservations+17; i++ {
		p.rememberObservation(fmt.Sprintf("fingerprint-%d", i), "head")
	}
	if got := len(p.state.observations); got > maxCachedObservations {
		t.Fatalf("observation cache size = %d, want <= %d", got, maxCachedObservations)
	}
	_ = repo
}

func TestVerifierRejectsBranchMoveAfterContentVerification(t *testing.T) {
	repo, subject, p := testRepo(t, "initial")
	before := observeBlob(t, p, subject)
	after := hashBlob(t, repo, "updated")
	authority := kernel.Authority{ExecutionID: "exec-final-head"}
	transition := observedTransition(t, p, subject, before, after)
	if result := (Executor{Provider: p}).Execute(context.Background(), transition, authority); !result.Success {
		t.Fatal(result.Message)
	}

	calls := 0
	verifier := Verifier{
		Provider: p,
		branchHead: func(ctx context.Context, repo, branch string) (string, error) {
			calls++
			head, err := branchHead(ctx, repo, branch)
			if err != nil {
				return "", err
			}
			if calls == 2 {
				git(t, repo, "commit", "--allow-empty", "-m", "verification race")
				head, err = branchHead(ctx, repo, branch)
				if err != nil {
					return "", err
				}
			}
			return head, nil
		},
	}
	if _, err := verifier.Verify(context.Background(), transition, authority); err == nil {
		t.Fatal("verification accepted a branch move after content verification")
	} else if !strings.Contains(err.Error(), "branch tip changed during verification") {
		t.Fatalf("error = %q", err)
	}
}

func TestVerifierRejectsPostExecutionMutation(t *testing.T) {
	repo, subject, p := testRepo(t, "initial")
	before := observeBlob(t, p, subject)
	after := hashBlob(t, repo, "updated")
	authority := kernel.Authority{ExecutionID: "exec-post"}
	transition := observedTransition(t, p, subject, before, after)
	if result := (Executor{Provider: p}).Execute(context.Background(), transition, authority); !result.Success {
		t.Fatal(result.Message)
	}
	git(t, repo, "commit", "--allow-empty", "-m", "post-execution mutation")
	if _, err := (Verifier{Provider: p}).Verify(context.Background(), transition, authority); err == nil {
		t.Fatal("post-execution branch mutation was accepted")
	}
}

func TestVerifierRejectsWrongResultingState(t *testing.T) {
	repo, subject, p := testRepo(t, "initial")
	before := observeBlob(t, p, subject)
	after := hashBlob(t, repo, "updated")
	wrong := hashBlob(t, repo, "wrong")
	authority := kernel.Authority{ExecutionID: "exec-wrong"}
	transition := kernel.Transition{Subject: subject, Before: before, After: after}
	p.rememberParent(authority.ExecutionID, before)
	parent := strings.TrimSpace(git(t, repo, "rev-parse", "refs/heads/main"))
	tree := buildTestTree(t, repo, parent, subject, wrong)
	commit := strings.TrimSpace(git(t, repo, "commit-tree", tree, "-p", parent, "-m", "Ack-Execution-Id: "+authority.ExecutionID))
	git(t, repo, "update-ref", "refs/heads/main", commit, parent)
	if _, err := (Verifier{Provider: p}).Verify(context.Background(), transition, authority); err == nil {
		t.Fatal("wrong resulting state accepted")
	}
}

func TestExecutorUsesTheExactObservationFingerprint(t *testing.T) {
	repo, subject, p := testRepo(t, "initial")
	before := observeBlob(t, p, subject)
	after := hashBlob(t, repo, "updated")
	first, err := (Observer{Provider: p}).Observe(context.Background(), subject)
	if err != nil {
		t.Fatal(err)
	}
	git(t, repo, "commit", "--allow-empty", "-m", "same target, new tip")
	second, err := (Observer{Provider: p}).Observe(context.Background(), subject)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != second.State || first.Fingerprint == second.Fingerprint {
		t.Fatal("observations unexpectedly matched")
	}
	result := (Executor{Provider: p}).Execute(context.Background(), kernel.Transition{
		Subject: subject, Before: before, After: after, ObservationFingerprint: first.Fingerprint,
	}, kernel.Authority{ExecutionID: "exec-observation-fingerprint"})
	if result.Success {
		t.Fatal("stale observation fingerprint was accepted")
	}
	if !strings.Contains(result.Message, "branch tip changed") {
		t.Fatalf("result = %q", result.Message)
	}
}

func TestVerifierRejectsMissingExecutionCommit(t *testing.T) {
	repo, subject, p := testRepo(t, "initial")
	before := observeBlob(t, p, subject)
	after := hashBlob(t, repo, "updated")
	authority := kernel.Authority{ExecutionID: "exec-missing-commit"}
	transition := kernel.Transition{Subject: subject, Before: before, After: after}
	parent := strings.TrimSpace(git(t, repo, "rev-parse", "refs/heads/main"))
	if err := p.rememberParent(authority.ExecutionID, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := (Verifier{Provider: p}).Verify(context.Background(), transition, authority); err == nil {
		t.Fatal("verification succeeded without an execution commit")
	} else if !strings.Contains(err.Error(), "execution commit is unavailable") {
		t.Fatalf("error = %q", err)
	}
}

func TestVerifierRejectsSiblingCommitWithMatchingContents(t *testing.T) {
	repo, subject, p := testRepo(t, "initial")
	before := observeBlob(t, p, subject)
	after := hashBlob(t, repo, "updated")
	transition := observedTransition(t, p, subject, before, after)
	authority := kernel.Authority{ExecutionID: "exec-exact-commit"}
	if result := (Executor{Provider: p}).Execute(context.Background(), transition, authority); !result.Success {
		t.Fatal(result.Message)
	}
	produced := strings.TrimSpace(git(t, repo, "rev-parse", "refs/heads/main"))
	parent := strings.TrimSpace(git(t, repo, "rev-parse", produced+"^1"))
	tree := strings.TrimSpace(git(t, repo, "rev-parse", produced+"^{tree}"))
	sibling := strings.TrimSpace(gitWithEnv(t, repo, map[string]string{"GIT_AUTHOR_DATE": "2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE": "2000-01-01T00:00:00Z"}, "commit-tree", tree, "-p", parent, "-m", "ackOS execution", "-m", "Ack-Execution-Id: "+authority.ExecutionID))
	git(t, repo, "update-ref", "refs/heads/main", sibling, produced)
	if _, err := (Verifier{Provider: p}).Verify(context.Background(), transition, authority); err == nil {
		t.Fatal("sibling commit was accepted as execution result")
	}
}

func TestRecoveryObservationReadsFreshGitState(t *testing.T) {
	repo, subject, p := testRepo(t, "initial")
	before := observeBlob(t, p, subject)
	after := hashBlob(t, repo, "updated")
	git(t, repo, "update-index", "--add", "--cacheinfo", "100644,"+after+","+subject)
	git(t, repo, "commit", "-m", "external recovery state")
	obs, err := (RecoveryObserver{Provider: p}).Observe(context.Background(), subject)
	if err != nil {
		t.Fatal(err)
	}
	if obs.State != after || obs.State == before {
		t.Fatalf("recovery state = %s, want fresh blob %s", obs.State, after)
	}
}

func TestPathSafety(t *testing.T) {
	cases := []string{"../outside", "/absolute", "a/../../outside", ".", "a\\b"}
	for _, subject := range cases {
		if err := validateSubject(subject); err == nil {
			t.Fatalf("accepted unsafe path %q", subject)
		}
	}
	if err := validateSubject("docs/example.md"); err != nil {
		t.Fatal(err)
	}
}

func testRepo(t *testing.T, content string) (string, string, Provider) {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-b", "main")
	git(t, repo, "config", "user.name", "ackOS test")
	git(t, repo, "config", "user.email", "ackos@example.invalid")
	subject := "docs/example.md"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, subject)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, subject), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "--", subject)
	git(t, repo, "commit", "-m", "initial")
	p, err := NewProvider(repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	return repo, subject, p
}

func observedTransition(t *testing.T, p Provider, subject, before, after string) kernel.Transition {
	t.Helper()
	o, err := (Observer{Provider: p}).Observe(context.Background(), subject)
	if err != nil {
		t.Fatal(err)
	}
	if o.State != before {
		t.Fatalf("observed state = %s, want %s", o.State, before)
	}
	return kernel.Transition{Subject: subject, Before: before, After: after, ObservationFingerprint: o.Fingerprint}
}

func observeBlob(t *testing.T, p Provider, subject string) string {
	t.Helper()
	o, err := (Observer{Provider: p}).Observe(context.Background(), subject)
	if err != nil {
		t.Fatal(err)
	}
	return o.State
}

func hashBlob(t *testing.T, repo, content string) string {
	t.Helper()
	cmd := exec.Command("git", "hash-object", "-w", "--stdin")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(content)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func buildTestTree(t *testing.T, repo, parent, subject, blob string) string {
	t.Helper()
	tree, err := buildTree(context.Background(), repo, parent, subject, blob)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

func readGitWorktreeFile(t *testing.T, repo, subject string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(subject)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func gitWithEnv(t *testing.T, repo string, env map[string]string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return string(out)
}

func git(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return string(out)
}

func assertFalseVerifier(t *testing.T, p Provider, subject, before, after string) {
	t.Helper()
	authority := kernel.Authority{ExecutionID: "false"}
	transition := kernel.Transition{Subject: subject, Before: before, After: after}
	if _, err := (Verifier{Provider: p}).Verify(context.Background(), transition, authority); err == nil {
		t.Fatal("false executor success was accepted")
	}
}
