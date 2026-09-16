package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jaredt87/ackOS/kernel"
)

func TestExecutorRejectsMissingCommitIdentityBeforeMutation(t *testing.T) {
	target, _, executor, _, _ := newTestProvider(t, "initial")
	gitTest(t, target.Repository, "config", "--local", "user.name", "")
	gitTest(t, target.Repository, "config", "--local", "user.email", "")

	transition := kernel.Transition{Subject: target.Subject, Before: "initial", After: "updated"}
	result := executor.Execute(context.Background(), transition, kernel.Authority{ExecutionID: "attempt-missing-identity"})
	if result.Success || !strings.Contains(result.Message, "Git commit identity") {
		t.Fatalf("result = %+v, want missing identity rejection", result)
	}
	if got := readTestFile(t, target); got != "initial" {
		t.Fatalf("target mutated before identity validation: %q", got)
	}
	status := gitTest(t, target.Repository, "status", "--porcelain", "--untracked-files=all")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("worktree/index changed before identity validation: %q", status)
	}
	if _, err := os.Stat(filepath.Join(target.Repository, ".git", "index.lock")); err == nil {
		t.Fatal("index.lock left behind")
	}
}
