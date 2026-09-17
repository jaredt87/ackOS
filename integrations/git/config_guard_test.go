package git

import (
	"context"
	"testing"
)

func TestRejectGitConfigTargetAllowsOrdinaryTrackedFile(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	if err := rejectGitConfigTarget(context.Background(), target); err != nil {
		t.Fatalf("ordinary tracked target rejected: %v", err)
	}
}
