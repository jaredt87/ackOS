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
