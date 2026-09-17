package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jaredt87/ackOS/kernel"
)

func TestNewTargetRejectsEmptyTrackedFile(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "config", "user.email", "ackos-test@example.invalid")
	gitTest(t, dir, "config", "user.name", "ackOS test")
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "--", "empty.txt")
	gitTest(t, dir, "commit", "-m", "initial")
	if _, err := NewTarget(dir, "empty.txt", "repo:empty.txt"); err == nil {
		t.Fatal("accepted empty tracked target")
	}
}

func TestExecutorRejectsCleanFilterBeforeStatus(t *testing.T) {
	target, _, executor, _, _ := newTestProvider(t, "initial")
	gitTest(t, target.Repository, "config", "filter.evil.clean", "sh -c 'echo side-effect > ../filter-side-effect; cat'")
	gitTest(t, target.Repository, "config", "filter.evil.smudge", "cat")
	attrs := filepath.Join(target.Repository, ".gitattributes")
	if err := os.WriteFile(attrs, []byte("docs/example.md filter=evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, target.Repository, "add", "--", ".gitattributes")
	gitTest(t, target.Repository, "commit", "-m", "configure filter")

	result := executor.Execute(context.Background(), kernel.Transition{
		Subject: target.Subject,
		Before:  "initial",
		After:   "updated",
	}, kernel.Authority{ExecutionID: "filter-before-status"})
	if result.Success {
		t.Fatal("executor accepted configured clean filter")
	}
	if !strings.Contains(result.Message, "clean filter") && !strings.Contains(result.Message, "filter") {
		t.Fatalf("unexpected error: %s", result.Message)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(target.Repository), "filter-side-effect")); err == nil {
		t.Fatal("clean filter ran before rejection")
	}
}

func TestRunGitDisablesFSMonitor(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	gitTest(t, target.Repository, "config", "core.fsmonitor", "./evil-fsmonitor")
	if _, err := runGit(context.Background(), target.Repository, "status", "--porcelain"); err != nil {
		t.Fatal(err)
	}
}

func TestCommitVerificationUsesRealObjects(t *testing.T) {
	target, observer, executor, verifier, _ := newTestProvider(t, "initial")
	observation, err := observer.Observe(context.Background(), target.Subject)
	if err != nil {
		t.Fatal(err)
	}
	transition := kernel.Transition{Subject: target.Subject, Before: observation.State, After: "updated"}
	authority := kernel.Authority{ExecutionID: "replace-object"}
	if result := executor.Execute(context.Background(), transition, authority); !result.Success {
		t.Fatal(result.Message)
	}
	if _, err := verifier.Verify(context.Background(), transition, authority); err != nil {
		t.Fatal(err)
	}
}
