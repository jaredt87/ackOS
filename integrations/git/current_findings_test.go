package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jaredt87/ackOS/kernel"
)

// Current findings regression coverage is kept separate from the provider's integration tests.

func TestRejectConfiguredFiltersRejectsSentinelNamedProcessDrivers(t *testing.T) {
	for _, driver := range []string{"unspecified", "unset"} {
		t.Run(driver, func(t *testing.T) {
			target, _, _, _, _ := newTestProvider(t, "initial")
			if err := os.WriteFile(filepath.Join(target.Repository, ".gitattributes"), []byte(target.Path+" filter="+driver+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitTest(t, target.Repository, "config", "filter."+driver+".process", "cat")
			gitTest(t, target.Repository, "add", "--", ".gitattributes")
			gitTest(t, target.Repository, "commit", "-m", "configure sentinel-named process filter")

			err := rejectConfiguredFilters(context.Background(), target)
			if err == nil || !strings.Contains(err.Error(), "configured clean filter") {
				t.Fatalf("error = %v, want sentinel-named process filter rejection", err)
			}
		})
	}
}

func TestRejectConfiguredFiltersRejectsSentinelNamedDrivers(t *testing.T) {
	for _, driver := range []string{"unspecified", "unset"} {
		t.Run(driver, func(t *testing.T) {
			target, _, _, _, _ := newTestProvider(t, "initial")
			if err := os.WriteFile(filepath.Join(target.Repository, ".gitattributes"), []byte(target.Path+" filter="+driver+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitTest(t, target.Repository, "config", "filter."+driver+".clean", "cat")
			gitTest(t, target.Repository, "add", "--", ".gitattributes")
			gitTest(t, target.Repository, "commit", "-m", "configure sentinel-named filter")

			err := rejectConfiguredFilters(context.Background(), target)
			if err == nil || !strings.Contains(err.Error(), "configured clean filter") {
				t.Fatalf("error = %v, want sentinel-named clean filter rejection", err)
			}
		})
	}
}

func TestRejectGitConfigTargetFollowsEmptyIncludedConfig(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "config", "user.email", "ackos-test@example.invalid")
	gitTest(t, dir, "config", "user.name", "ackOS test")
	path := filepath.Join(dir, "tracked-config.inc")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "--", "tracked-config.inc")
	gitTest(t, dir, "commit", "-m", "add empty included config")

	target := Target{Repository: dir, Path: "tracked-config.inc", Subject: "test-repo:tracked-config.inc"}
	gitTest(t, dir, "config", "include.path", "../tracked-config.inc")

	if err := rejectGitConfigTarget(context.Background(), target); err == nil || !strings.Contains(err.Error(), "configuration source") {
		t.Fatalf("error = %v, want empty included configuration source rejection", err)
	}
}

func TestUpdateCapturedRefDoesNotDereferenceSymbolicBranch(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	parent := target.capturedHead
	gitTest(t, target.Repository, "branch", "other", parent)
	if _, err := runGit(context.Background(), target.Repository, "symbolic-ref", target.capturedHeadRef, "refs/heads/other"); err != nil {
		t.Fatal(err)
	}

	commit := strings.TrimSpace(gitTest(t, target.Repository, "commit-tree", parent+"^{tree}", "-p", parent, "-m", "ackOS: execute ref-race"))
	if err := updateCapturedRef(context.Background(), target, target.capturedHeadRef, commit, parent); err != nil {
		t.Fatalf("updateCapturedRef failed: %v", err)
	}
	otherHead := strings.TrimSpace(gitTest(t, target.Repository, "rev-parse", "refs/heads/other"))
	if otherHead != parent {
		t.Fatalf("symbolic target branch advanced: got %s, want %s", otherHead, parent)
	}
	capturedHead := strings.TrimSpace(gitTest(t, target.Repository, "rev-parse", target.capturedHeadRef))
	if capturedHead != commit {
		t.Fatalf("captured ref was not updated directly: got %s, want %s", capturedHead, commit)
	}
}

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

func TestRejectAttributesTargetRejectsDefaultPerUserAttributesFile(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	dir := filepath.Join(xdg, "git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	gitTest(t, dir, "init")
	gitTest(t, dir, "config", "user.email", "ackos-test@example.invalid")
	gitTest(t, dir, "config", "user.name", "ackOS test")
	path := filepath.Join(dir, "attributes")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "--", "attributes")
	gitTest(t, dir, "commit", "-m", "add default attributes file")

	target, err := NewTarget(dir, "attributes", "test-repo:attributes")
	if err != nil {
		t.Fatal(err)
	}

	if err := rejectAttributesTarget(context.Background(), target); err == nil || !strings.Contains(err.Error(), "active attributes file") {
		t.Fatalf("error = %v, want default attributes file rejection", err)
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
	configTarget, err := NewTarget(target.Repository, "tracked-config.inc", "test-repo:tracked-config.inc")
	if err != nil {
		t.Fatal(err)
	}

	err = rejectGitConfigTarget(context.Background(), configTarget)
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

func TestVerifyCommitRejectsTargetModeChange(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	blob := strings.TrimSpace(gitTest(t, target.Repository, "hash-object", "-w", "--stdin"))
	gitTest(t, target.Repository, "update-index", "--add", "--cacheinfo", "120000,"+blob+","+target.Path)
	tree := strings.TrimSpace(gitTest(t, target.Repository, "write-tree"))
	parent := strings.TrimSpace(gitTest(t, target.Repository, "rev-parse", "HEAD"))
	commit := strings.TrimSpace(gitTest(t, target.Repository, "commit-tree", tree, "-p", parent, "-m", "ackOS: execute mode-test"))
	gitTest(t, target.Repository, "update-ref", "HEAD", commit)

	transition := kernel.Transition{Subject: target.Subject, Before: "initial", After: "initial"}
	err := verifyCommitAt(context.Background(), target, func(args ...string) (string, error) {
		return runGit(context.Background(), target.Repository, args...)
	}, parent, transition, "mode-test")
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("error = %v, want non-regular target mode rejection", err)
	}
}

func TestVerifyCommitReadsMarkerFromCapturedCommit(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	parent := strings.TrimSpace(gitTest(t, target.Repository, "rev-parse", "HEAD"))
	afterHash, err := runGitInput(context.Background(), target.Repository, []byte("updated"), "hash-object", "-w", "--no-filters", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	afterHash = strings.TrimSpace(afterHash)
	headRef := strings.TrimSpace(gitTest(t, target.Repository, "symbolic-ref", "-q", "HEAD"))
	if err := commitVerifiedTree(context.Background(), target, parent, headRef, afterHash, []byte("updated"), "ackOS: execute marker-test"); err != nil {
		t.Fatal(err)
	}

	transition := kernel.Transition{Subject: target.Subject, Before: "initial", After: "updated"}
	git := func(args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "--no-replace-objects" && args[1] == "log" && args[2] == "-1" {
			return "ackOS: execute wrong-marker", nil
		}
		return runGit(context.Background(), target.Repository, args...)
	}
	err = verifyCommitAt(context.Background(), target, git, parent, transition, "marker-test")
	if err == nil || !strings.Contains(err.Error(), "marker") {
		t.Fatalf("error = %v, want captured-commit marker verification failure", err)
	}
}

func TestVerifierRejectsCommitWithWrongPreExecutionParent(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	capturedParent := target.capturedHead
	if _, err := target.lifecycle.capture(context.Background(), target, "wrong-parent"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(target.Repository, "other.txt"), []byte("unrelated"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, target.Repository, "add", "--", "other.txt")
	gitTest(t, target.Repository, "commit", "-m", "advance history")

	afterHash, err := runGitInput(context.Background(), target.Repository, []byte("updated"), "hash-object", "-w", "--no-filters", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	afterHash = strings.TrimSpace(afterHash)
	gitTest(t, target.Repository, "write-tree")
	gitTest(t, target.Repository, "update-index", "--add", "--cacheinfo", "100644,"+afterHash+","+target.Path)
	tree := strings.TrimSpace(gitTest(t, target.Repository, "write-tree"))
	parent := strings.TrimSpace(gitTest(t, target.Repository, "rev-parse", "HEAD"))
	commit := strings.TrimSpace(gitTest(t, target.Repository, "commit-tree", tree, "-p", parent, "-m", "ackOS: execute wrong-parent"))
	gitTest(t, target.Repository, "update-ref", "HEAD", commit)
	if err := os.WriteFile(filepath.Join(target.Repository, target.Path), []byte("updated"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, target.Repository, "add", "--", target.Path)

	verifier := Verifier{Target: target}
	transition := kernel.Transition{Subject: target.Subject, Before: "initial", After: "updated"}
	_, err = verifier.Verify(context.Background(), transition, kernel.Authority{ExecutionID: "wrong-parent"})
	if err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("error = %v, want captured pre-execution parent rejection (captured %s)", err, capturedParent)
	}
}

func TestVerifierRejectsDetachedHead(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	parent := target.capturedHead
	if _, err := target.lifecycle.capture(context.Background(), target, "detached-test"); err != nil {
		t.Fatal(err)
	}
	afterHash, err := gitBlobHash(context.Background(), target, "updated")
	if err != nil {
		t.Fatal(err)
	}
	if err := commitVerifiedTree(context.Background(), target, parent, target.capturedHeadRef, afterHash, []byte("updated"), "ackOS: execute detached-test"); err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(gitTest(t, target.Repository, "rev-parse", "HEAD"))
	gitTest(t, target.Repository, "checkout", "--detach", commit)

	verifier := Verifier{Target: target}
	transition := kernel.Transition{Subject: target.Subject, Before: "initial", After: "updated"}
	_, err = verifier.Verify(context.Background(), transition, kernel.Authority{ExecutionID: "detached-test"})
	if err == nil || !strings.Contains(err.Error(), "authorized branch") {
		t.Fatalf("error = %v, want authorized-branch rejection", err)
	}
}

func TestNewTargetRejectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init")
	gitTest(t, dir, "config", "user.email", "ackos-test@example.invalid")
	gitTest(t, dir, "config", "user.name", "ackOS test")
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "--", "empty.txt")
	gitTest(t, dir, "commit", "-m", "empty target")
	if _, err := NewTarget(dir, "empty.txt", "test-repo:empty.txt"); err == nil {
		t.Fatal("NewTarget accepted an empty tracked file")
	}
}

func TestRejectSubmodulesRejectsGitlinkEntries(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	object := strings.TrimSpace(gitTest(t, target.Repository, "rev-parse", "HEAD"))
	gitTest(t, target.Repository, "update-index", "--add", "--cacheinfo", "160000,"+object+","+target.Path)
	if err := rejectSubmodules(context.Background(), target); err == nil || !strings.Contains(err.Error(), "submodules") {
		t.Fatalf("error = %v, want submodule rejection", err)
	}
}

func TestLifecycleDiscardRemovesExecutionParent(t *testing.T) {
	state := &lifecycleState{parents: map[string]executionParent{"execution": {head: "head", ref: "refs/heads/main"}}}
	state.discard("execution")
	if _, err := state.parent("execution"); err == nil {
		t.Fatal("discarded execution parent remains available")
	}
}

func TestRejectCommandScopeConfigEnvironment(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "include.path")
	if err := rejectCommandScopeConfigEnvironment(); err == nil {
		t.Fatal("command-scope Git configuration environment was accepted")
	}
}
