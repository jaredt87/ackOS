package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func remotePushGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func remotePushGitDir(t *testing.T, gitDir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir", gitDir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git --git-dir %s %v: %v: %s", gitDir, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func remotePushFixture(t *testing.T) (string, string, *Repository, ObjectID) {
	t.Helper()
	work := testRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	remotePushGit(t, work, "config", "user.name", "ackOS test")
	remotePushGit(t, work, "config", "user.email", "ackos-test@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "tracked.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	remotePushGit(t, work, "add", "tracked.txt")
	remotePushGit(t, work, "commit", "-m", "base")
	before := ObjectID(remotePushGit(t, work, "rev-parse", "HEAD"))
	cmd := exec.Command("git", "init", "--bare", "-q", remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	remotePushGit(t, work, "push", remote, "HEAD:refs/heads/main")
	remotePushGitDir(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	repo, err := Open(work)
	if err != nil {
		t.Fatal(err)
	}
	return work, remote, repo, before
}

func TestPushRefWithLeaseRejectsConcurrentDescendant(t *testing.T) {
	work, remote, repo, before := remotePushFixture(t)
	if err := os.WriteFile(filepath.Join(work, "tracked.txt"), []byte("candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	remotePushGit(t, work, "commit", "-am", "candidate")
	candidate := ObjectID(remotePushGit(t, work, "rev-parse", "HEAD"))

	concurrent := filepath.Join(t.TempDir(), "concurrent")
	cmd := exec.Command("git", "clone", "-q", remote, concurrent)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v: %s", err, out)
	}
	remotePushGit(t, concurrent, "config", "user.name", "concurrent writer")
	remotePushGit(t, concurrent, "config", "user.email", "writer@example.invalid")
	if err := os.WriteFile(filepath.Join(concurrent, "tracked.txt"), []byte("concurrent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	remotePushGit(t, concurrent, "commit", "-am", "concurrent")
	concurrentTip := ObjectID(remotePushGit(t, concurrent, "rev-parse", "HEAD"))
	remotePushGit(t, concurrent, "push", "origin", "HEAD:refs/heads/main")

	err := repo.PushRefWithLease(context.Background(), remote, RefName("refs/heads/main"), candidate, before)
	if err == nil {
		t.Fatal("accepted a remote descendant update after observation")
	}
	actual := ObjectID(remotePushGitDir(t, remote, "rev-parse", "refs/heads/main"))
	if actual != concurrentTip {
		t.Fatalf("remote tip = %s, want concurrent tip %s", actual, concurrentTip)
	}
}

func TestPushRefWithLeaseRejectsRewindToAncestor(t *testing.T) {
	work, remote, repo, before := remotePushFixture(t)
	if err := os.WriteFile(filepath.Join(work, "tracked.txt"), []byte("observed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	remotePushGit(t, work, "commit", "-am", "observed")
	observed := ObjectID(remotePushGit(t, work, "rev-parse", "HEAD"))
	remotePushGit(t, work, "push", remote, "HEAD:refs/heads/main")
	if err := os.WriteFile(filepath.Join(work, "tracked.txt"), []byte("candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	remotePushGit(t, work, "commit", "-am", "candidate")
	candidate := ObjectID(remotePushGit(t, work, "rev-parse", "HEAD"))

	remotePushGitDir(t, remote, "update-ref", "refs/heads/main", string(before), string(observed))
	err := repo.PushRefWithLease(context.Background(), remote, RefName("refs/heads/main"), candidate, observed)
	if err == nil {
		t.Fatal("accepted a remote rewind to an ancestor of the authorized tip")
	}
	actual := ObjectID(remotePushGitDir(t, remote, "rev-parse", "refs/heads/main"))
	if actual != before {
		t.Fatalf("remote tip = %s, want rewound ancestor %s", actual, before)
	}
}

func TestPushRefWithLeaseDocumentsABALimitation(t *testing.T) {
	work, remote, repo, before := remotePushFixture(t)
	if err := os.WriteFile(filepath.Join(work, "tracked.txt"), []byte("candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	remotePushGit(t, work, "commit", "-am", "candidate")
	candidate := ObjectID(remotePushGit(t, work, "rev-parse", "HEAD"))

	concurrent := filepath.Join(t.TempDir(), "concurrent")
	cmd := exec.Command("git", "clone", "-q", remote, concurrent)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v: %s", err, out)
	}
	remotePushGit(t, concurrent, "config", "user.name", "concurrent writer")
	remotePushGit(t, concurrent, "config", "user.email", "writer@example.invalid")
	if err := os.WriteFile(filepath.Join(concurrent, "tracked.txt"), []byte("intervening\n"), 0600); err != nil {
		t.Fatal(err)
	}
	remotePushGit(t, concurrent, "commit", "-am", "intervening")
	intervening := ObjectID(remotePushGit(t, concurrent, "rev-parse", "HEAD"))
	remotePushGit(t, concurrent, "push", "origin", "HEAD:refs/heads/main")
	remotePushGitDir(t, remote, "update-ref", "refs/heads/main", string(before), string(intervening))

	if err := repo.PushRefWithLease(context.Background(), remote, RefName("refs/heads/main"), candidate, before); err != nil {
		t.Fatalf("exact-current-value lease rejected A→B→A: %v", err)
	}
	actual := ObjectID(remotePushGitDir(t, remote, "rev-parse", "refs/heads/main"))
	if actual != candidate {
		t.Fatalf("remote tip = %s, want candidate %s", actual, candidate)
	}
}

func TestPushRefWithLeaseDoesNotFollowTags(t *testing.T) {
	work, remote, repo, before := remotePushFixture(t)
	if err := os.WriteFile(filepath.Join(work, "tracked.txt"), []byte("candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	remotePushGit(t, work, "commit", "-am", "candidate")
	candidate := ObjectID(remotePushGit(t, work, "rev-parse", "HEAD"))
	remotePushGit(t, work, "tag", "-a", "release-test", "-m", "release test", string(candidate))
	remotePushGit(t, work, "config", "push.followTags", "true")

	if err := repo.PushRefWithLease(context.Background(), remote, RefName("refs/heads/main"), candidate, before); err != nil {
		t.Fatalf("push leased branch: %v", err)
	}
	actual := ObjectID(remotePushGitDir(t, remote, "rev-parse", "refs/heads/main"))
	if actual != candidate {
		t.Fatalf("remote branch tip = %s, want candidate %s", actual, candidate)
	}
	cmd := exec.Command("git", "--git-dir", remote, "show-ref", "--verify", "--quiet", "refs/tags/release-test")
	if err := cmd.Run(); err == nil {
		t.Fatal("leased single-ref push unexpectedly published an annotated tag")
	}
}

func TestPushRefWithLeaseRejectsAlreadyPublishedCandidate(t *testing.T) {
	work, remote, repo, before := remotePushFixture(t)
	if err := os.WriteFile(filepath.Join(work, "tracked.txt"), []byte("candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	remotePushGit(t, work, "commit", "-am", "candidate")
	candidate := ObjectID(remotePushGit(t, work, "rev-parse", "HEAD"))
	remotePushGit(t, work, "push", remote, "HEAD:refs/heads/main")

	err := repo.PushRefWithLease(context.Background(), remote, RefName("refs/heads/main"), candidate, before)
	if err == nil {
		t.Fatal("accepted an up-to-date push without proving the expected-old lease")
	}
	actual := ObjectID(remotePushGitDir(t, remote, "rev-parse", "refs/heads/main"))
	if actual != candidate {
		t.Fatalf("remote tip = %s, want already-published candidate %s", actual, candidate)
	}
}

func TestPushRefWithLeaseRejectsInvalidInputs(t *testing.T) {
	_, remote, repo, before := remotePushFixture(t)
	for _, tc := range []struct {
		name   string
		remote string
		ref    RefName
		next   ObjectID
		old    ObjectID
	}{
		{name: "empty remote", remote: " ", ref: "refs/heads/main", next: before, old: before},
		{name: "option remote", remote: "--upload-pack=bad", ref: "refs/heads/main", next: before, old: before},
		{name: "invalid ref", remote: remote, ref: "main", next: before, old: before},
		{name: "invalid result", remote: remote, ref: "refs/heads/main", next: "bad", old: before},
		{name: "invalid expected", remote: remote, ref: "refs/heads/main", next: before, old: "bad"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := repo.PushRefWithLease(context.Background(), tc.remote, tc.ref, tc.next, tc.old); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
}
