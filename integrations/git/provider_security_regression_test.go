package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jaredt87/ackOS/kernel"
)

func TestReadFileRejectsHardlinkedTarget(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "target.txt")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(inside, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(inside, outside); err != nil {
		t.Fatal(err)
	}

	_, err := readFile(context.Background(), Target{Repository: dir, Path: "target.txt", Subject: "test"})
	if err == nil || !strings.Contains(err.Error(), "multiple hard links") {
		t.Fatalf("readFile error = %v, want hardlink rejection", err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "initial" {
		t.Fatalf("outside hardlink target changed: got %q, err=%v", got, err)
	}
}

func TestExecutorRejectsSelfFilteringGitAttributesTargetBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "config", "user.email", "ackos-test@example.invalid")
	gitTest(t, dir, "config", "user.name", "ackOS test")
	attrs := filepath.Join(dir, ".gitattributes")
	if err := os.WriteFile(attrs, []byte("*.txt text eol=lf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", ".gitattributes")
	gitTest(t, dir, "commit", "-m", "initial attributes")

	target, err := NewTarget(dir, ".gitattributes", "test-repo:.gitattributes")
	if err != nil {
		t.Fatal(err)
	}
	result := (Executor{Target: target}).Execute(context.Background(), kernel.Transition{
		Subject: target.Subject,
		Before:  "*.txt text eol=lf\n",
		After:   "*.txt text eol=crlf\n",
	}, kernel.Authority{ExecutionID: "attempt-self-filter"})
	if result.Success || !strings.Contains(result.Message, "self-filter") {
		t.Fatalf("result = %+v, want self-filter rejection", result)
	}
	if got := readTestFile(t, target); got != "*.txt text eol=lf\n" {
		t.Fatalf(".gitattributes changed during rejection: %q", got)
	}
}
