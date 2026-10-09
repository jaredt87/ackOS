package git

import (
	"context"
	"errors"
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
		Target: control.ResourceRef{ID: provider.Subject()},
		Before: before,
		Payload: []byte(State(desired)),
	})
	if err != nil {
		t.Fatal(err)
	}
	verification, err := provider.Verify(ctx, control.VerifyRequest{
		ExecutionID: executionID,
		Expected: control.ResourceRef{ID: provider.Subject(), Fingerprint: State(desired)},
		Before: before,
		Execution: execution,
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
		Target: control.ResourceRef{ID: provider.Subject()},
		Before: beforeA,
		Payload: []byte(State(blobB)),
	})
	if !errors.Is(err, ErrStaleLineage) {
		t.Fatalf("stale A/C1 execution error = %v, want ErrStaleLineage", err)
	}
}

func TestProviderRejectsNoopWithoutCreatingCommit(t *testing.T) {
	ctx := context.Background()
	_, provider, blob := providerTestRepo(t, "target.txt", []byte("same"))
	before, err := provider.Observe(ctx, control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Execute(ctx, control.ExecuteRequest{
		ExecutionID: "noop",
		Target: control.ResourceRef{ID: provider.Subject()},
		Before: before,
		Payload: []byte(State(blob)),
	})
	if !errors.Is(err, ErrNoop) {
		t.Fatalf("no-op error = %v, want ErrNoop", err)
	}
	tip, err := provider.repo.ReadRef(ctx, provider.branch)
	if err != nil {
		t.Fatal(err)
	}
	if tip != ObjectID(strings.TrimSpace(string(tip))) {
		t.Fatal("unexpected tip representation")
	}
	if _, err := provider.repo.ReadBlob(ctx, blob); err != nil {
		t.Fatal(err)
	}
}

func TestProviderRejectsMissingTargetAtObservation(t *testing.T) {
	root := testRepo(t)
	runGitTest(t, root, "symbolic-ref", "HEAD", "refs/heads/main")
	repo, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	// A missing ref is a startup/observation error before a path can be read.
	provider, err := NewProvider(repo, RefName("refs/heads/main"), filepath.ToSlash("missing.txt"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Observe(context.Background(), control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
	if err == nil || !strings.Contains(err.Error(), "read configured branch") {
		t.Fatalf("missing branch error = %v, want branch-read failure", err)
	}
}
