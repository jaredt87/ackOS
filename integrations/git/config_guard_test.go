package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectGitConfigTargetAllowsOrdinaryTrackedFile(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	if err := rejectGitConfigTarget(context.Background(), target); err != nil {
		t.Fatalf("ordinary tracked target rejected: %v", err)
	}
}

func TestRejectGitConfigTargetRejectsRecursiveCommentOnlyInclude(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	parent := filepath.Join(target.Repository, "parent.inc")
	leaf := filepath.Join(target.Repository, "leaf.inc")
	if err := os.WriteFile(parent, []byte("[include]\n\tpath = leaf.inc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(leaf, []byte("# comment-only config source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, target.Repository, "add", "--", "parent.inc", "leaf.inc")
	gitTest(t, target.Repository, "commit", "-m", "add recursive config includes")
	gitTest(t, target.Repository, "config", "include.path", "../parent.inc")

	leafTarget, err := NewTarget(target.Repository, "leaf.inc", "test-repo:leaf.inc")
	if err != nil {
		t.Fatal(err)
	}
	if err := rejectGitConfigTarget(context.Background(), leafTarget); err == nil || !strings.Contains(err.Error(), "configuration source") {
		t.Fatalf("error = %v, want recursive active Git configuration source rejection", err)
	}
}

func TestRejectAttributesTargetResolvesSymlinkAlias(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	attrsLink := filepath.Join(target.Repository, "attrs-link")
	if err := os.Symlink(target.Path, attrsLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	gitTest(t, target.Repository, "config", "core.attributesFile", "attrs-link")

	if err := rejectAttributesTarget(context.Background(), target); err == nil || !strings.Contains(err.Error(), "active attributes file") {
		t.Fatalf("error = %v, want symlinked active attributes file rejection", err)
	}
}
