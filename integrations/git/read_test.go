package git

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestReadRefCommitAndTree(t *testing.T) {
	root := testRepo(t)
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}

	blob, err := r.WriteBlob(context.Background(), []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := r.WriteTree(context.Background(), []TreeEntry{
		{Mode: "100644", Path: "file.txt", Object: blob},
	})
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	commit, err := r.CommitTree(
		context.Background(),
		tree,
		nil,
		"message\n\nAck-Execution-Id: test\n",
		Identity{Name: "Author", Email: "author@example.com", When: when},
		Identity{Name: "Committer", Email: "committer@example.com", When: when},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateRef(context.Background(), RefName("refs/heads/test"), commit, ObjectID(strings.Repeat("0", 40))); err != nil {
		t.Fatal(err)
	}

	gotRef, err := r.ReadRef(context.Background(), RefName("refs/heads/test"))
	if err != nil {
		t.Fatal(err)
	}
	if gotRef != commit {
		t.Fatalf("ref = %q, want %q", gotRef, commit)
	}

	gotCommit, err := r.ReadCommit(context.Background(), commit)
	if err != nil {
		t.Fatal(err)
	}
	if gotCommit.ID != commit || gotCommit.Tree != tree || len(gotCommit.Parents) != 0 {
		t.Fatalf("commit = %+v", gotCommit)
	}
	if gotCommit.Message != "message\n\nAck-Execution-Id: test\n" {
		t.Fatalf("message = %q", gotCommit.Message)
	}

	gotTree, err := r.ReadTree(context.Background(), tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotTree) != 1 || gotTree[0].Mode != "100644" || gotTree[0].Path != "file.txt" || gotTree[0].Object != blob {
		t.Fatalf("tree = %+v", gotTree)
	}
}

func TestReadRejectsInvalidInputs(t *testing.T) {
	r, err := Open(testRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadRef(context.Background(), RefName("main")); err == nil {
		t.Fatal("accepted unqualified ref")
	}
	if _, err := r.ReadRef(context.Background(), RefName("refs/heads/main@{1}")); err == nil {
		t.Fatal("accepted reflog selector")
	}
	if _, err := r.ReadCommit(context.Background(), ObjectID("bad")); err == nil {
		t.Fatal("accepted invalid commit id")
	}
	if _, err := r.ReadTree(context.Background(), ObjectID("bad")); err == nil {
		t.Fatal("accepted invalid tree id")
	}

	blob, err := r.WriteBlob(context.Background(), []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := r.WriteTree(context.Background(), []TreeEntry{{Mode: "100644", Path: "file.txt", Object: blob}})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := r.CommitTree(context.Background(), tree, nil, "message\n", Identity{Name: "Author", Email: "author@example.com"}, Identity{Name: "Committer", Email: "committer@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadTree(context.Background(), commit); err == nil {
		t.Fatal("accepted commit as tree")
	}
}
