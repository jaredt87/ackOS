package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDebugGitConfigInclude(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	configPath := filepath.Join(target.Repository, ".git", "config")
	includePath := filepath.Join(target.Repository, "tracked-config.inc")
	if err := os.WriteFile(includePath, []byte("[core]\n\tattributesFile = /tmp/unused\n"), 0o644); err != nil { t.Fatal(err) }
	gitTest(t, target.Repository, "add", "--", "tracked-config.inc")
	gitTest(t, target.Repository, "commit", "-m", "add tracked config include")
	gitTest(t, target.Repository, "config", "include.path", "../tracked-config.inc")
	config, _ := os.ReadFile(configPath)
	gitConfig, gitErr := runGit(context.Background(), target.Repository, "config", "--local", "--get-regexp", "^include")
	t.Fatalf("config=%q gitConfig=%q gitErr=%v", string(config), gitConfig, gitErr)
}
