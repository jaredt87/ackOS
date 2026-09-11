package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jaredt87/ackOS/kernel"
)

func TestProviderLifecycleCommitsExactTransition(t *testing.T) {
	target, observer, executor, verifier, recovery := newTestProvider(t, "initial")
	observation, err := observer.Observe(context.Background(), target.Subject)
	if err != nil {
		t.Fatal(err)
	}
	runtime := kernel.NewRuntime(observation.State, kernel.AllowPolicy{})
	if err := runtime.Observe(observation); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Normalize(kernel.Proposal{Subject: target.Subject, TargetState: "updated"}); err != nil {
		t.Fatal(err)
	}
	transition, err := runtime.Reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Govern(); err != nil {
		t.Fatal(err)
	}
	authority, err := runtime.Reserve(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Start(context.Background(), executor)
	if err != nil || !result.Success {
		t.Fatalf("execution failed: result=%+v err=%v", result, err)
	}
	if err := runtime.Verify(context.Background(), verifier); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, target); got != "updated" {
		t.Fatalf("file = %q, want updated", got)
	}
	message := gitTest(t, target.Repository, "log", "-1", "--format=%B")
	if strings.TrimSpace(message) != "ackOS: execute "+authority.ExecutionID {
		t.Fatalf("commit marker = %q", message)
	}
	_ = transition
	_ = recovery
}

func TestProviderLifecyclePreservesBlobWhitespace(t *testing.T) {
	target, observer, executor, verifier, _ := newTestProvider(t, "initial\n")
	observation, err := observer.Observe(context.Background(), target.Subject)
	if err != nil {
		t.Fatal(err)
	}
	transition := kernel.Transition{Subject: target.Subject, Before: observation.State, After: "updated\n"}
	authority := kernel.Authority{ExecutionID: "attempt-whitespace"}
	result := executor.Execute(context.Background(), transition, authority)
	if !result.Success {
		t.Fatal(result.Message)
	}
	if _, err := verifier.Verify(context.Background(), transition, authority); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, target); got != "updated\n" {
		t.Fatalf("file = %q, want trailing newline preserved", got)
	}
}

func TestExecutorRejectsResourceSubstitution(t *testing.T) {
	target, _, executor, _, _ := newTestProvider(t, "initial")
	transition := kernel.Transition{Subject: "different-resource", Before: "initial", After: "updated"}
	authority := kernel.Authority{ExecutionID: "attempt-1"}
	result := executor.Execute(context.Background(), transition, authority)
	if result.Success || !strings.Contains(result.Message, "subject mismatch") {
		t.Fatalf("result = %+v, want subject mismatch", result)
	}
	if got := readTestFile(t, target); got != "initial" {
		t.Fatalf("file changed during substitution test: %q", got)
	}
}

func TestExecutorRejectsTOCTOUStateChange(t *testing.T) {
	target, observer, executor, _, _ := newTestProvider(t, "initial")
	observation, err := observer.Observe(context.Background(), target.Subject)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target.Repository, target.Path), []byte("changed-outside-ackos"), 0o644); err != nil {
		t.Fatal(err)
	}
	transition := kernel.Transition{Subject: target.Subject, Before: observation.State, After: "updated"}
	result := executor.Execute(context.Background(), transition, kernel.Authority{ExecutionID: "attempt-2"})
	if result.Success || !strings.Contains(result.Message, "changed before execution") {
		t.Fatalf("result = %+v, want stale-state rejection", result)
	}
}

func TestVerifierRejectsFalseExecutorSuccess(t *testing.T) {
	target, observer, _, verifier, _ := newTestProvider(t, "initial")
	observation, err := observer.Observe(context.Background(), target.Subject)
	if err != nil {
		t.Fatal(err)
	}
	transition := kernel.Transition{Subject: target.Subject, Before: observation.State, After: "updated"}
	authority := kernel.Authority{ExecutionID: "attempt-3"}
	if result := (falseExecutor{}).Execute(context.Background(), transition, authority); !result.Success {
		t.Fatal("false executor test setup did not report success")
	}
	if _, err := verifier.Verify(context.Background(), transition, authority); err == nil {
		t.Fatal("verifier accepted false executor success")
	}
}

func TestVerifierRejectsPostExecutionMutation(t *testing.T) {
	target, observer, executor, verifier, _ := newTestProvider(t, "initial")
	observation, err := observer.Observe(context.Background(), target.Subject)
	if err != nil {
		t.Fatal(err)
	}
	transition := kernel.Transition{Subject: target.Subject, Before: observation.State, After: "updated"}
	authority := kernel.Authority{ExecutionID: "attempt-4"}
	result := executor.Execute(context.Background(), transition, authority)
	if !result.Success {
		t.Fatal(result.Message)
	}
	if err := os.WriteFile(filepath.Join(target.Repository, target.Path), []byte("mutated-after-execution"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(context.Background(), transition, authority); err == nil {
		t.Fatal("verifier accepted post-execution mutation")
	}
}

func TestVerifierRejectsCommitWithWrongTargetDiff(t *testing.T) {
	target, observer, _, verifier, _ := newTestProvider(t, "initial")
	observation, err := observer.Observe(context.Background(), target.Subject)
	if err != nil {
		t.Fatal(err)
	}
	transition := kernel.Transition{Subject: target.Subject, Before: observation.State, After: "updated"}
	authority := kernel.Authority{ExecutionID: "attempt-5"}
	if err := os.WriteFile(filepath.Join(target.Repository, target.Path), []byte(transition.After), 0o644); err != nil {
		t.Fatal(err)
	}
	wrongPath := filepath.Join(target.Repository, "docs", "other.md")
	if err := os.WriteFile(wrongPath, []byte("wrong-change"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, target.Repository, "add", "--", "docs/other.md")
	gitTest(t, target.Repository, "commit", "--no-verify", "-m", "ackOS: execute "+authority.ExecutionID)
	if _, err := verifier.Verify(context.Background(), transition, authority); err == nil {
		t.Fatal("verifier accepted commit with wrong target diff")
	}
}

func TestExecutorRejectsUntrackedTarget(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "config", "user.email", "ackos-test@example.invalid")
	gitTest(t, dir, "config", "user.name", "ackOS test")
	path := filepath.Join(dir, "docs", "ignored.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	target, err := NewTarget(dir, "docs/ignored.md", "test-repo:docs/ignored.md")
	if err != nil {
		t.Fatal(err)
	}
	result := (Executor{Target: target}).Execute(context.Background(), kernel.Transition{Subject: target.Subject, Before: "initial", After: "updated"}, kernel.Authority{ExecutionID: "attempt-untracked"})
	if result.Success || !strings.Contains(result.Message, "not tracked") {
		t.Fatalf("result = %+v, want untracked rejection", result)
	}
	if got := readTestFile(t, target); got != "initial" {
		t.Fatalf("untracked target changed during rejection: %q", got)
	}
}

func TestExecutorRejectsPendingGitMerge(t *testing.T) {
	target, observer, executor, _, _ := newTestProvider(t, "initial")
	observation, err := observer.Observe(context.Background(), target.Subject)
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, target.Repository, "checkout", "-b", "merge-test")
	gitTest(t, target.Repository, "checkout", "main")
	gitTest(t, target.Repository, "merge", "--no-commit", "merge-test")
	transition := kernel.Transition{Subject: target.Subject, Before: observation.State, After: "updated"}
	result := executor.Execute(context.Background(), transition, kernel.Authority{ExecutionID: "attempt-pending-merge"})
	if result.Success || !strings.Contains(result.Message, "MERGE_HEAD") {
		t.Fatalf("result = %+v, want pending-merge rejection", result)
	}
	if got := readTestFile(t, target); got != "initial" {
		t.Fatalf("target changed during pending merge rejection: %q", got)
	}
	gitTest(t, target.Repository, "merge", "--abort")
}

func TestNewTargetRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewTarget(dir, "../outside.txt", "repo:file"); err == nil {
		t.Fatal("accepted path traversal")
	}
}

func TestNewTargetRejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "target.txt")
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTarget(dir, "target.txt", "repo:target.txt"); err == nil {
		t.Fatal("accepted symlink target")
	}
}

type falseExecutor struct{}

func (falseExecutor) Execute(context.Context, kernel.Transition, kernel.Authority) kernel.ExecutionResult {
	return kernel.ExecutionResult{Success: true, Message: "claimed success without mutation"}
}

func newTestProvider(t *testing.T, initial string) (Target, Observer, Executor, Verifier, RecoveryObserver) {
	t.Helper()
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "config", "user.email", "ackos-test@example.invalid")
	gitTest(t, dir, "config", "user.name", "ackOS test")
	path := filepath.Join(dir, "docs", "example.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "--", "docs/example.md")
	gitTest(t, dir, "commit", "-m", "initial")
	target, err := NewTarget(dir, "docs/example.md", "test-repo:docs/example.md")
	if err != nil {
		t.Fatal(err)
	}
	return target, Observer{Target: target}, Executor{Target: target}, Verifier{Target: target}, RecoveryObserver{Target: target}
}

func readTestFile(t *testing.T, target Target) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(target.Repository, target.Path))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
