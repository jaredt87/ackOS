package mcp

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jaredt87/ackOS/control"
	gitprovider "github.com/jaredt87/ackOS/integrations/git"
	"github.com/jaredt87/ackOS/kernel"
)

func setupGitProviderServer(t *testing.T, initial []byte) (string, *gitprovider.Repository, *gitprovider.Provider, *Server, gitprovider.ObjectID) {
	t.Helper()
	root := t.TempDir()
	runGitIntegrationTest(t, root, "init", "-q")
	runGitIntegrationTest(t, root, "symbolic-ref", "HEAD", "refs/heads/other")
	repo, err := gitprovider.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := repo.WriteBlob(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	otherBlob, err := repo.WriteBlob(context.Background(), []byte("untouched"))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := repo.WriteTree(context.Background(), []gitprovider.TreeEntry{
		{Mode: "100644", Path: "target.txt", Object: blob},
		{Mode: "100644", Path: "other.txt", Object: otherBlob},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	identity := gitprovider.Identity{Name: "Test", Email: "test@example.com", When: now}
	commit, err := repo.CommitTree(context.Background(), tree, nil, "initial", identity, identity)
	if err != nil {
		t.Fatal(err)
	}
	zero := strings.Repeat("0", len(commit))
	runGitIntegrationTest(t, root, "update-ref", "refs/heads/target", string(commit), zero)
	runGitIntegrationTest(t, root, "update-ref", "refs/heads/other", string(commit), zero)
	provider, err := gitprovider.NewProvider(repo, gitprovider.RefName("refs/heads/target"), "target.txt")
	if err != nil {
		t.Fatal(err)
	}
	initialObservation, err := provider.Observe(context.Background(), control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServerWithProvider(kernel.NewRuntime(initialObservation.Resource.Fingerprint, kernel.AllowPolicy{}), provider)
	if err != nil {
		t.Fatal(err)
	}
	return root, repo, provider, server, blob
}

func runGitIntegrationTest(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func makeExternalGitCommit(t *testing.T, repo *gitprovider.Repository, root, targetPath string, newBlob gitprovider.ObjectID, touchTarget bool) {
	t.Helper()
	ctx := context.Background()
	tip, err := repo.ReadRef(ctx, gitprovider.RefName("refs/heads/target"))
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
	if touchTarget {
		found := false
		for i := range entries {
			if entries[i].Path == targetPath {
				entries[i].Object = newBlob
				found = true
			}
		}
		if !found {
			t.Fatalf("target %q missing in test tree", targetPath)
		}
	} else {
		unrelated, err := repo.WriteBlob(ctx, []byte("external unrelated change"))
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, gitprovider.TreeEntry{Mode: "100644", Path: "unrelated.txt", Object: unrelated})
	}
	tree, err := repo.WriteTree(ctx, entries)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	identity := gitprovider.Identity{Name: "External", Email: "external@example.com", When: now}
	commit, err := repo.CommitTree(ctx, tree, []gitprovider.ObjectID{tip}, "out-of-band change", identity, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateRef(ctx, gitprovider.RefName("refs/heads/target"), commit, tip); err != nil {
		t.Fatal(err)
	}
	_ = root
}

func TestGitProviderTipOnlyCommitDoesNotWedgeRuntime(t *testing.T) {
	root, repo, provider, server, _ := setupGitProviderServer(t, []byte("A"))
	blobB, err := repo.WriteBlob(context.Background(), []byte("B"))
	if err != nil {
		t.Fatal(err)
	}
	makeExternalGitCommit(t, repo, root, "target.txt", blobB, false)

	blobC, err := repo.WriteBlob(context.Background(), []byte("C"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = server.control(context.Background(), nil, ControlRequest{
		Subject: provider.Subject(), ObservedState: "caller-value-is-ignored", DesiredState: gitprovider.State(blobC),
	})
	if err != nil {
		t.Fatalf("tip-only commit wedged runtime: %v", err)
	}
}

func TestGitProviderTargetDriftRequiresRestartAndRebaseline(t *testing.T) {
	root, repo, provider, server, _ := setupGitProviderServer(t, []byte("A"))
	blobB, err := repo.WriteBlob(context.Background(), []byte("out-of-band B"))
	if err != nil {
		t.Fatal(err)
	}
	blobC, err := repo.WriteBlob(context.Background(), []byte("desired C"))
	if err != nil {
		t.Fatal(err)
	}
	makeExternalGitCommit(t, repo, root, "target.txt", blobB, true)

	_, _, err = server.control(context.Background(), nil, ControlRequest{
		Subject: provider.Subject(), DesiredState: gitprovider.State(blobC),
	})
	if err == nil || !strings.Contains(err.Error(), "restart to re-baseline from current repository state") {
		t.Fatalf("target drift error = %v, want actionable restart/re-baseline message", err)
	}
	if !strings.Contains(err.Error(), "compare-and-swap conflict") {
		t.Fatalf("target drift error = %v, want underlying kernel CAS conflict", err)
	}

	// Restart deliberately trusts the repository as it now stands. Seed a new
	// runtime from the actual current provider observation, then execute.
	current, err := provider.Observe(context.Background(), control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewServerWithProvider(kernel.NewRuntime(current.Resource.Fingerprint, kernel.AllowPolicy{}), provider)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = restarted.control(context.Background(), nil, ControlRequest{
		Subject: provider.Subject(), DesiredState: gitprovider.State(blobC),
	})
	if err != nil {
		t.Fatalf("call after explicit restart/re-baseline failed: %v", err)
	}
}


type raceInjectGitProvider struct {
	*gitprovider.Provider
	inject func()
}

func (p *raceInjectGitProvider) Execute(ctx context.Context, req control.ExecuteRequest) (control.Execution, error) {
	if p.inject != nil {
		inject := p.inject
		p.inject = nil
		inject()
	}
	return p.Provider.Execute(ctx, req)
}

func TestGitProviderRejectsABAInjectedBetweenObserveAndExecute(t *testing.T) {
	root, repo, provider, _, blobA := setupGitProviderServer(t, []byte("A"))
	blobB, err := repo.WriteBlob(context.Background(), []byte("B"))
	if err != nil {
		t.Fatal(err)
	}
	racing := &raceInjectGitProvider{
		Provider: provider,
		inject: func() {
			makeExternalGitCommit(t, repo, root, "target.txt", blobB, true)
			makeExternalGitCommit(t, repo, root, "target.txt", blobA, true)
		},
	}
	initial, err := provider.Observe(context.Background(), control.ObserveRequest{Target: control.ResourceRef{ID: provider.Subject()}})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServerWithProvider(kernel.NewRuntime(initial.Resource.Fingerprint, kernel.AllowPolicy{}), racing)
	if err != nil {
		t.Fatal(err)
	}
	blobC, err := repo.WriteBlob(context.Background(), []byte("C"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = server.control(context.Background(), nil, ControlRequest{
		Subject: provider.Subject(), DesiredState: gitprovider.State(blobC),
	})
	if err == nil || !strings.Contains(err.Error(), "stale observation; branch lineage or target blob changed") {
		t.Fatalf("in-call A→B→A race error = %v, want provider stale-lineage rejection", err)
	}
}
