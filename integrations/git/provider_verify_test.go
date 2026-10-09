package git

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jaredt87/ackOS/control"
)

func providerWithSibling(t *testing.T) (*Repository, *Provider, ObjectID) {
	t.Helper()
	repo, provider, _ := providerTestRepo(t, "target.txt", []byte("A"))
	ctx := context.Background()
	tip, err := repo.ReadRef(ctx, provider.branch)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := repo.ReadCommit(ctx, tip)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := repo.ReadTree(ctx, parent.Tree)
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := repo.WriteBlob(ctx, []byte("sibling original"))
	if err != nil {
		t.Fatal(err)
	}
	entries = append(entries, TreeEntry{Mode: "100644", Path: "sibling.txt", Object: sibling})
	tree, err := repo.WriteTree(ctx, entries)
	if err != nil {
		t.Fatal(err)
	}
	side := providerTestCommit(t, repo, tree, []ObjectID{tip}, "add sibling")
	if err := repo.UpdateRef(ctx, provider.branch, side, tip); err != nil {
		t.Fatal(err)
	}
	return repo, provider, sibling
}

func providerTestCommit(t *testing.T, repo *Repository, tree ObjectID, parents []ObjectID, message string) ObjectID {
	t.Helper()
	now := time.Now().UTC()
	identity := Identity{Name: "Test", Email: "test@example.com", When: now}
	commit, err := repo.CommitTree(context.Background(), tree, parents, message, identity, identity)
	if err != nil {
		t.Fatal(err)
	}
	return commit
}

func TestProviderVerifyRejectsClaimedCommitMismatch(t *testing.T) {
	ctx := context.Background()
	repo, provider, _ := providerWithSibling(t)
	before, err := provider.Observe(ctx, control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
	if err != nil {
		t.Fatal(err)
	}
	desired, err := repo.WriteBlob(ctx, []byte("B"))
	if err != nil {
		t.Fatal(err)
	}
	tip, err := repo.ReadRef(ctx, provider.branch)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := repo.ReadCommit(ctx, tip)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := repo.ReadTree(ctx, parent.Tree)
	if err != nil {
		t.Fatal(err)
	}
	for i := range entries {
		if entries[i].Path == "target.txt" {
			entries[i].Object = desired
		}
	}
	tree, err := repo.WriteTree(ctx, entries)
	if err != nil {
		t.Fatal(err)
	}
	commit := providerTestCommit(t, repo, tree, []ObjectID{tip},
		"ackOS: update target.txt\n\nAckOS-Execution: forged\nAckOS-Target: target.txt\n")
	if err := repo.UpdateRef(ctx, provider.branch, commit, tip); err != nil {
		t.Fatal(err)
	}
	evidence, err := json.Marshal(executionEvidence{Commit: tip, Parent: tip, Path: provider.Subject(), Blob: desired})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Verify(ctx, control.VerifyRequest{
		ExecutionID: "forged",
		Expected: control.ResourceRef{ID: provider.Subject(), Fingerprint: State(desired)},
		Before: before,
		Execution: control.Execution{ExecutionID: "forged", Evidence: evidence},
	})
	if err == nil || !strings.Contains(err.Error(), "branch no longer points at claimed commit") {
		t.Fatalf("Verify error = %v, want claimed-commit mismatch", err)
	}
}

func TestProviderVerifyRejectsWrongParentDespiteMatchingTrailers(t *testing.T) {
	ctx := context.Background()
	repo, provider, _ := providerWithSibling(t)
	before, err := provider.Observe(ctx, control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
	if err != nil {
		t.Fatal(err)
	}
	desired, err := repo.WriteBlob(ctx, []byte("B"))
	if err != nil {
		t.Fatal(err)
	}
	tip, err := repo.ReadRef(ctx, provider.branch)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := repo.ReadCommit(ctx, tip)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := repo.ReadTree(ctx, parent.Tree)
	if err != nil {
		t.Fatal(err)
	}
	for i := range entries {
		if entries[i].Path == "target.txt" {
			entries[i].Object = desired
		}
	}
	tree, err := repo.WriteTree(ctx, entries)
	if err != nil {
		t.Fatal(err)
	}
	wrongParent := providerTestCommit(t, repo, parent.Tree, []ObjectID{tip}, "unrelated parent")
	commit := providerTestCommit(t, repo, tree, []ObjectID{wrongParent},
		"ackOS: update target.txt\n\nAckOS-Execution: forged-parent\nAckOS-Target: target.txt\n")
	if err := repo.UpdateRef(ctx, provider.branch, commit, tip); err != nil {
		t.Fatal(err)
	}
	evidence, err := json.Marshal(executionEvidence{Commit: commit, Parent: tip, Path: provider.Subject(), Blob: desired})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Verify(ctx, control.VerifyRequest{
		ExecutionID: "forged-parent",
		Expected: control.ResourceRef{ID: provider.Subject(), Fingerprint: State(desired)},
		Before: before,
		Execution: control.Execution{ExecutionID: "forged-parent", Evidence: evidence},
	})
	if err == nil || !strings.Contains(err.Error(), "claimed commit parent mismatch") {
		t.Fatalf("Verify error = %v, want wrong-parent rejection", err)
	}
}

func TestProviderVerifyRejectsNonSinglePathAndModeChanges(t *testing.T) {
	cases := []struct {
		name string
		mutate func(t *testing.T, repo *Repository, entries []TreeEntry, desired ObjectID) []TreeEntry
	}{
		{
			name: "extra file",
			mutate: func(t *testing.T, repo *Repository, entries []TreeEntry, desired ObjectID) []TreeEntry {
				t.Helper()
				extra, err := repo.WriteBlob(context.Background(), []byte("extra"))
				if err != nil { t.Fatal(err) }
				entries = append(entries, TreeEntry{Mode: "100644", Path: "extra.txt", Object: extra})
				return entries
			},
		},
		{
			name: "sibling modified",
			mutate: func(t *testing.T, repo *Repository, entries []TreeEntry, desired ObjectID) []TreeEntry {
				t.Helper()
				sibling, err := repo.WriteBlob(context.Background(), []byte("sibling modified"))
				if err != nil { t.Fatal(err) }
				for i := range entries {
					if entries[i].Path == "sibling.txt" { entries[i].Object = sibling }
				}
				return entries
			},
		},
		{
			name: "target mode changed",
			mutate: func(t *testing.T, _ *Repository, entries []TreeEntry, _ ObjectID) []TreeEntry {
				t.Helper()
				for i := range entries {
					if entries[i].Path == "target.txt" { entries[i].Mode = "100755" }
				}
				return entries
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo, provider, _ := providerWithSibling(t)
			before, err := provider.Observe(ctx, control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
			if err != nil { t.Fatal(err) }
			desired, err := repo.WriteBlob(ctx, []byte("B"))
			if err != nil { t.Fatal(err) }
			tip, err := repo.ReadRef(ctx, provider.branch)
			if err != nil { t.Fatal(err) }
			parent, err := repo.ReadCommit(ctx, tip)
			if err != nil { t.Fatal(err) }
			entries, err := repo.ReadTree(ctx, parent.Tree)
			if err != nil { t.Fatal(err) }
			for i := range entries {
				if entries[i].Path == "target.txt" { entries[i].Object = desired }
			}
			entries = tc.mutate(t, repo, entries, desired)
			tree, err := repo.WriteTree(ctx, entries)
			if err != nil { t.Fatal(err) }
			executionID := "forged-diff"
			commit := providerTestCommit(t, repo, tree, []ObjectID{tip},
				"ackOS: update target.txt\n\nAckOS-Execution: "+executionID+"\nAckOS-Target: target.txt\n")
			if err := repo.UpdateRef(ctx, provider.branch, commit, tip); err != nil { t.Fatal(err) }
			evidence, err := json.Marshal(executionEvidence{Commit: commit, Parent: tip, Path: provider.Subject(), Blob: desired})
			if err != nil { t.Fatal(err) }
			_, err = provider.Verify(ctx, control.VerifyRequest{
				ExecutionID: executionID,
				Expected: control.ResourceRef{ID: provider.Subject(), Fingerprint: State(desired)},
				Before: before,
				Execution: control.Execution{ExecutionID: executionID, Evidence: evidence},
			})
			if err == nil { t.Fatal("Verify accepted a forged tree change") }
			if tc.name == "target mode changed" && !strings.Contains(err.Error(), "mode") {
				t.Fatalf("Verify error = %v, want explicit mode mismatch", err)
			}
			if tc.name != "target mode changed" && !strings.Contains(err.Error(), "change only target path") {
				t.Fatalf("Verify error = %v, want single-path diff rejection", err)
			}
		})
	}
}

func TestProviderRejectsMissingBranchAndPathTraversal(t *testing.T) {
	repo, provider, _ := providerTestRepo(t, "target.txt", []byte("A"))
	missingBranch, err := NewProvider(repo, RefName("refs/heads/missing"), "target.txt")
	if err != nil { t.Fatal(err) }
	_, err = missingBranch.Observe(context.Background(), control.ObserveRequest{Target: control.ResourceRef{ID: missingBranch.Subject()}})
	if err == nil { t.Fatal("Observe accepted missing branch") }

	for _, target := range []string{"../outside", "nested/../../outside", "/absolute/path"} {
		if _, err := NewProvider(repo, provider.branch, target); err == nil {
			t.Errorf("NewProvider accepted unsafe target path %q", target)
		}
	}
}

func TestProviderRejectsMissingDesiredBlobBeforeRefWrite(t *testing.T) {
	ctx := context.Background()
	_, provider, _ := providerTestRepo(t, "target.txt", []byte("A"))
	before, err := provider.Observe(ctx, control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
	if err != nil { t.Fatal(err) }
	tipBefore, err := provider.repo.ReadRef(ctx, provider.branch)
	if err != nil { t.Fatal(err) }
	missing := ObjectID(strings.Repeat("f", len(tipBefore)))
	_, err = provider.Execute(ctx, control.ExecuteRequest{
		ExecutionID: "missing-blob",
		Target: control.ResourceRef{ID: provider.Subject()},
		Before: before,
		Payload: []byte(State(missing)),
	})
	if err == nil || !strings.Contains(err.Error(), "desired object is not an existing blob") {
		t.Fatalf("Execute error = %v, want missing blob rejection", err)
	}
	tipAfter, err := provider.repo.ReadRef(ctx, provider.branch)
	if err != nil { t.Fatal(err) }
	if tipAfter != tipBefore { t.Fatalf("rejected missing blob moved ref from %s to %s", tipBefore, tipAfter) }
}

func TestProviderVerifyRejectsBranchMovementAfterExecute(t *testing.T) {
	ctx := context.Background()
	repo, provider, _ := providerTestRepo(t, "target.txt", []byte("A"))
	before, err := provider.Observe(ctx, control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
	if err != nil {
		t.Fatal(err)
	}
	desired, err := repo.WriteBlob(ctx, []byte("B"))
	if err != nil {
		t.Fatal(err)
	}
	executionID := "move-after-execute"
	execution, err := provider.Execute(ctx, control.ExecuteRequest{
		ExecutionID: executionID,
		Target: control.ResourceRef{ID: provider.Subject()},
		Before: before,
		Payload: []byte(State(desired)),
	})
	if err != nil {
		t.Fatal(err)
	}
	tip, err := repo.ReadRef(ctx, provider.branch)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repo.ReadCommit(ctx, tip)
	if err != nil {
		t.Fatal(err)
	}
	moved := providerTestCommit(t, repo, commit.Tree, []ObjectID{tip}, "external tip movement")
	if err := repo.UpdateRef(ctx, provider.branch, moved, tip); err != nil {
		t.Fatal(err)
	}
	_, err = provider.Verify(ctx, control.VerifyRequest{
		ExecutionID: executionID,
		Expected: control.ResourceRef{ID: provider.Subject(), Fingerprint: State(desired)},
		Before: before,
		Execution: execution,
	})
	if err == nil || !strings.Contains(err.Error(), "branch no longer points at claimed commit") {
		t.Fatalf("Verify error = %v, want post-execute branch movement rejection", err)
	}
}

func TestUpdateRefRejectsStaleExpectedOld(t *testing.T) {
	ctx := context.Background()
	repo, provider, _ := providerTestRepo(t, "target.txt", []byte("A"))
	current, err := repo.ReadRef(ctx, provider.branch)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := repo.WriteBlob(ctx, []byte("B"))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := repo.WriteTree(ctx, []TreeEntry{{Mode: "100644", Path: "target.txt", Object: blob}})
	if err != nil {
		t.Fatal(err)
	}
	candidate := providerTestCommit(t, repo, tree, []ObjectID{current}, "candidate")
	staleExpected := ObjectID(strings.Repeat("0", len(current)))
	if err := repo.UpdateRef(ctx, provider.branch, candidate, staleExpected); err == nil {
		t.Fatal("UpdateRef accepted a stale expected-old object ID")
	}
	after, err := repo.ReadRef(ctx, provider.branch)
	if err != nil {
		t.Fatal(err)
	}
	if after != current {
		t.Fatalf("failed CAS moved ref from %s to %s", current, after)
	}
}
