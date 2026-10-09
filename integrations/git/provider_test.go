package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jaredt87/ackOS/control"
)

func providerTestRepo(t *testing.T, targetPath string, initial []byte) (*Repository, *Provider, ObjectID) {
	t.Helper()
	root := testRepo(t)
	runGitTest(t, root, "symbolic-ref", "HEAD", "refs/heads/other")
	repo, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := repo.WriteBlob(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := providerTestTree(repo, targetPath, blob)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	identity := Identity{Name: "Test", Email: "test@example.com", When: now}
	commit, err := repo.CommitTree(context.Background(), tree, nil, "initial", identity, identity)
	if err != nil {
		t.Fatal(err)
	}
	zero := strings.Repeat("0", len(commit))
	runGitTest(t, root, "update-ref", "refs/heads/target", string(commit), zero)
	runGitTest(t, root, "update-ref", "refs/heads/other", string(commit), zero)
	runGitTest(t, root, "checkout", "-f", "other")
	provider, err := NewProvider(repo, RefName("refs/heads/target"), targetPath)
	if err != nil {
		t.Fatal(err)
	}
	return repo, provider, blob
}

func providerTestTree(repo *Repository, targetPath string, blob ObjectID) (ObjectID, error) {
	parts := strings.Split(targetPath, "/")
	var tree ObjectID
	var err error
	tree, err = repo.WriteTree(context.Background(), []TreeEntry{{Mode: "100644", Path: parts[len(parts)-1], Object: blob}})
	if err != nil {
		return "", err
	}
	for i := len(parts) - 2; i >= 0; i-- {
		tree, err = repo.WriteTree(context.Background(), []TreeEntry{{Mode: "040000", Path: parts[i], Object: tree}})
		if err != nil {
			return "", err
		}
	}
	return tree, nil
}

func runGitTest(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func performProviderTransition(t *testing.T, provider *Provider, before control.Observation, desired ObjectID, executionID string) control.Verification {
	t.Helper()
	ctx := context.Background()
	execution, err := provider.Execute(ctx, control.ExecuteRequest{
		ExecutionID: executionID,
		Target:      control.ResourceRef{ID: provider.Subject()},
		Before:      before,
		Payload:     []byte(State(desired)),
	})
	if err != nil {
		t.Fatal(err)
	}
	verification, err := provider.Verify(ctx, control.VerifyRequest{
		ExecutionID: executionID,
		Expected:    control.ResourceRef{ID: provider.Subject(), Fingerprint: State(desired)},
		Before:      before,
		Execution:   execution,
	})
	if err != nil {
		t.Fatal(err)
	}
	return verification
}

func TestProviderAtoBtoARejectsStaleOriginalObservation(t *testing.T) {
	ctx := context.Background()
	repo, provider, blobA := providerTestRepo(t, "nested/target.txt", []byte("A"))
	blobB, err := repo.WriteBlob(ctx, []byte("B"))
	if err != nil {
		t.Fatal(err)
	}
	beforeA, err := provider.Observe(ctx, control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
	if err != nil {
		t.Fatal(err)
	}
	performProviderTransition(t, provider, beforeA, blobB, "exec-A-B")
	head, err := exec.Command("git", "-C", repo.root, "symbolic-ref", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(head)) != "refs/heads/other" {
		t.Fatalf("checked-out branch changed: %q", head)
	}
	worktree, err := os.ReadFile(filepath.Join(repo.root, "nested", "target.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(worktree) != "A" {
		t.Fatalf("provider mutated working tree: got %q, want original content A", worktree)
	}
	beforeB, err := provider.Observe(ctx, control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
	if err != nil {
		t.Fatal(err)
	}
	performProviderTransition(t, provider, beforeB, blobA, "exec-B-A")

	current, err := provider.Observe(ctx, control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
	if err != nil {
		t.Fatal(err)
	}
	if current.Resource.Fingerprint != State(blobA) {
		t.Fatalf("state after A→B→A = %q, want %q", current.Resource.Fingerprint, State(blobA))
	}
	if current.Version == beforeA.Version {
		t.Fatal("lineage version did not change after A→B→A")
	}
	_, err = provider.Execute(ctx, control.ExecuteRequest{
		ExecutionID: "stale-A",
		Target:      control.ResourceRef{ID: provider.Subject()},
		Before:      beforeA,
		Payload:     []byte(State(blobB)),
	})
	if !errors.Is(err, ErrStaleLineage) {
		t.Fatalf("stale A/C1 execution error = %v, want ErrStaleLineage", err)
	}
	if !errors.Is(err, control.ErrStaleObservation) {
		t.Fatalf("stale A/C1 execution error = %v, want control.ErrStaleObservation", err)
	}
}

func TestProviderRejectsNoopWithoutCreatingCommit(t *testing.T) {
	ctx := context.Background()
	_, provider, blob := providerTestRepo(t, "target.txt", []byte("same"))
	before, err := provider.Observe(ctx, control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
	if err != nil {
		t.Fatal(err)
	}
	beforeTip, err := provider.repo.ReadRef(ctx, provider.branch)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Execute(ctx, control.ExecuteRequest{
		ExecutionID: "noop",
		Target:      control.ResourceRef{ID: provider.Subject()},
		Before:      before,
		Payload:     []byte(State(blob)),
	})
	if !errors.Is(err, ErrNoop) {
		t.Fatalf("no-op error = %v, want ErrNoop", err)
	}
	if errors.Is(err, control.ErrStaleObservation) {
		t.Fatalf("non-stale no-op error %v unexpectedly matches stale-observation sentinel", err)
	}
	tip, err := provider.repo.ReadRef(ctx, provider.branch)
	if err != nil {
		t.Fatal(err)
	}
	if tip != beforeTip {
		t.Fatalf("no-op moved branch tip from %s to %s", beforeTip, tip)
	}
	if _, err := provider.repo.ReadBlob(ctx, blob); err != nil {
		t.Fatal(err)
	}
}

func TestProviderRejectsMissingTargetAtObservation(t *testing.T) {
	repo, existing, _ := providerTestRepo(t, "existing.txt", []byte("present"))
	provider, err := NewProvider(repo, RefName("refs/heads/target"), filepath.ToSlash("missing.txt"))
	if err != nil {
		t.Fatal(err)
	}
	_ = existing
	_, err = provider.Observe(context.Background(), control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
	if err == nil || !strings.Contains(err.Error(), "target path") || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing target error = %v, want explicit missing-path failure", err)
	}
}

func TestProviderRejectsCommitObjectAsDesiredBlob(t *testing.T) {
	ctx := context.Background()
	_, provider, _ := providerTestRepo(t, "target.txt", []byte("A"))
	before, err := provider.Observe(ctx, control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
	if err != nil {
		t.Fatal(err)
	}
	commitID, err := provider.repo.ReadRef(ctx, provider.branch)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Execute(ctx, control.ExecuteRequest{
		ExecutionID: "wrong-object-type",
		Target:      control.ResourceRef{ID: provider.Subject()},
		Before:      before,
		Payload:     []byte(State(commitID)),
	})
	if err == nil || !strings.Contains(err.Error(), "desired object is not an existing blob") {
		t.Fatalf("commit object desired state error = %v, want blob-type rejection", err)
	}
}

func TestProviderClassifiesOnlyRefCASAsStaleObservation(t *testing.T) {
	casErr := fmt.Errorf("%w: git update-ref: cannot lock ref: is at current but expected stale", ErrRefCASConflict)
	got := refUpdateError(casErr)
	if !errors.Is(got, control.ErrStaleObservation) || !errors.Is(got, ErrRefCASConflict) {
		t.Fatalf("CAS update error = %v, want stale-observation and ref-CAS sentinels", got)
	}

	ioErr := errors.New("git update-ref: permission denied")
	got = refUpdateError(ioErr)
	if errors.Is(got, control.ErrStaleObservation) {
		t.Fatalf("non-CAS update error %v unexpectedly matches stale-observation sentinel", got)
	}
	if !strings.Contains(got.Error(), ioErr.Error()) {
		t.Fatalf("non-CAS update error = %v, want original I/O detail", got)
	}
}
