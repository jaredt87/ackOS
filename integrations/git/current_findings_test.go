package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Current findings regression coverage is kept separate from the provider's integration tests.

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
	if err := os.WriteFile(filepath.Join(target.Repository, ".gitattributes"), []byte("*.md text eol=crlf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, target.Repository, "add", "--", ".gitattributes")
	gitTest(t, target.Repository, "commit", "-m", "configure eol normalization")

	err := rejectConfiguredNormalization(context.Background(), target)
	if err == nil || !strings.Contains(err.Error(), "normalization") {
		t.Fatalf("error = %v, want configured normalization rejection", err)
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

func TestRejectGitConfigTargetRejectsActiveGitConfigInclude(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	configPath := filepath.Join(target.Repository, "tracked-config.inc")
	if err := os.WriteFile(configPath, []byte("[core]\n\tattributesFile = /tmp/unused\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, target.Repository, "add", "--", "tracked-config.inc")
	gitTest(t, target.Repository, "commit", "-m", "add tracked config include")
	gitTest(t, target.Repository, "config", "include.path", "../tracked-config.inc")

	debugConfig, debugErr := runGit(context.Background(), target.Repository, "config", "--local", "--get-regexp", "^include")
	t.Logf("local include config: %q err=%v", debugConfig, debugErr)
	err := rejectGitConfigTarget(context.Background(), target)
	if err == nil || !strings.Contains(err.Error(), "configuration source") {
		t.Fatalf("error = %v, want active Git configuration source rejection", err)
	}
}

func TestRejectConfiguredNormalizationRejectsLegacyCRLF(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	if err := os.WriteFile(filepath.Join(target.Repository, ".gitattributes"), []byte(target.Path+" crlf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, target.Repository, "add", "--", ".gitattributes")
	gitTest(t, target.Repository, "commit", "-m", "configure legacy crlf normalization")

	err := rejectConfiguredNormalization(context.Background(), target)
	if err == nil || !strings.Contains(err.Error(), "crlf") {
		t.Fatalf("error = %v, want legacy crlf rejection", err)
	}
}

func TestRejectConfiguredNormalizationRejectsIdent(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	if err := os.WriteFile(filepath.Join(target.Repository, ".gitattributes"), []byte(target.Path+" ident\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, target.Repository, "add", "--", ".gitattributes")
	gitTest(t, target.Repository, "commit", "-m", "configure ident normalization")

	err := rejectConfiguredNormalization(context.Background(), target)
	if err == nil || !strings.Contains(err.Error(), "ident") {
		t.Fatalf("error = %v, want ident normalization rejection", err)
	}
}

func TestRejectConfiguredNormalizationRejectsWorkingTreeEncoding(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	if err := os.WriteFile(filepath.Join(target.Repository, ".gitattributes"), []byte(target.Path+" working-tree-encoding=UTF-8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, target.Repository, "add", "--", ".gitattributes")
	gitTest(t, target.Repository, "commit", "-m", "configure working tree encoding")

	err := rejectConfiguredNormalization(context.Background(), target)
	if err == nil || !strings.Contains(err.Error(), "working-tree-encoding") {
		t.Fatalf("error = %v, want working-tree-encoding rejection", err)
	}
}

func TestRejectGrafts(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	grafts, err := runGit(context.Background(), target.Repository, "rev-parse", "--git-path", "info/grafts")
	if err != nil {
		t.Fatal(err)
	}
	grafts = strings.TrimSpace(grafts)
	if !filepath.IsAbs(grafts) {
		grafts = filepath.Join(target.Repository, grafts)
	}
	if err := os.MkdirAll(filepath.Dir(grafts), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(grafts, []byte("deadbeefdeadbeefdeadbeefdeadbeefdeadbeef deadbeefdeadbeefdeadbeefdeadbeefdeadbeef\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rejectGrafts(context.Background(), target); err == nil || !strings.Contains(err.Error(), "graft") {
		t.Fatalf("error = %v, want graft rejection", err)
	}
}

func TestSanitizedGitEnvPreservesCommitIdentity(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "ackOS Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "author@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "ackOS Committer")
	t.Setenv("GIT_COMMITTER_EMAIL", "committer@example.invalid")
	t.Setenv("GIT_DIR", "/outside/repository")
	t.Setenv("GIT_WORK_TREE", "/outside/worktree")
	t.Setenv("GIT_INDEX_FILE", "/outside/index")

	env := sanitizedGitEnv()
	joined := strings.Join(env, "\x00")
	for _, want := range []string{
		"GIT_AUTHOR_NAME=ackOS Author",
		"GIT_AUTHOR_EMAIL=author@example.invalid",
		"GIT_COMMITTER_NAME=ackOS Committer",
		"GIT_COMMITTER_EMAIL=committer@example.invalid",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("sanitized environment missing %q", want)
		}
	}
	for _, blocked := range []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE="} {
		if strings.Contains(joined, blocked) {
			t.Fatalf("sanitized environment retained %q", blocked)
		}
	}
}
