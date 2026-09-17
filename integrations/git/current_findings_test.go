package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectConfiguredNormalizationRejectsAutocrlf(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	gitTest(t, target.Repository, "config", "core.autocrlf", "true")

	err := rejectConfiguredNormalization(context.Background(), target)
	if err == nil || !strings.Contains(err.Error(), "core.autocrlf") {
		t.Fatalf("error = %v, want core.autocrlf normalization rejection", err)
	}
}

func TestRejectConfiguredNormalizationRejectsEOLAttribute(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	if err := os.WriteFile(filepath.Join(target.Repository, ".gitattributes"), []byte("docs/example.md text eol=crlf\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := rejectConfiguredNormalization(context.Background(), target)
	if err == nil || !strings.Contains(err.Error(), "eol attribute") {
		t.Fatalf("error = %v, want eol normalization rejection", err)
	}
}

func TestRejectAttributesTargetResolvesGitPathname(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Skip("HOME is required for Git tilde pathname expansion")
	}
	dir, err := os.MkdirTemp(home, ".ackos-attributes-test-*")
	if err != nil {
		t.Skipf("cannot create test repository under HOME: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	gitTest(t, dir, "init")
	gitTest(t, dir, "config", "user.email", "ackos-test@example.invalid")
	gitTest(t, dir, "config", "user.name", "ackOS test")
	path := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(path, []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "--", "target.txt")
	gitTest(t, dir, "commit", "-m", "initial")

	target, err := NewTarget(dir, "target.txt", "test-repo:target.txt")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(home, path)
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "config", "core.attributesFile", "~/"+filepath.ToSlash(rel))

	err = rejectAttributesTarget(context.Background(), target)
	if err == nil || !strings.Contains(err.Error(), "active attributes file") {
		t.Fatalf("error = %v, want active attributes file rejection", err)
	}
}
