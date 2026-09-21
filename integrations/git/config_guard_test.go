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

func TestSanitizedGitEnvBlocksCommandScopeConfigInjection(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "include.path")
	t.Setenv("GIT_CONFIG_VALUE_0", "/tmp/attacker")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'include.path'='/tmp/attacker'")
	t.Setenv("ACKOS_TEST_ENV", "preserved")

	env := sanitizedGitEnv()
	joined := "\n" + strings.Join(env, "\n") + "\n"
	for _, key := range []string{"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_PARAMETERS"} {
		if strings.Contains(joined, "\n"+key+"=") {
			t.Fatalf("sanitized environment retained %s", key)
		}
	}
	if !strings.Contains(joined, "\nACKOS_TEST_ENV=preserved\n") {
		t.Fatal("sanitized environment removed unrelated variables")
	}
}
