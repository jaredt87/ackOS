package git

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestReadBlobReturnsBytesAndRejectsInvalidOrNonBlobObjects(t *testing.T) {
	ctx := context.Background()
	repo, err := Open(testRepo(t))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := repo.ReadBlob(ctx, ObjectID("invalid")); err == nil {
		t.Fatal("ReadBlob accepted an invalid object ID")
	}

	blob, err := repo.WriteBlob(ctx, []byte("blob contents"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.ReadBlob(ctx, blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "blob contents" {
		t.Fatalf("ReadBlob = %q, want blob contents", got)
	}

	tree, err := repo.WriteTree(ctx, []TreeEntry{{Mode: "100644", Path: "file.txt", Object: blob}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	identity := Identity{Name: "Test", Email: "test@example.com", When: now}
	commit, err := repo.CommitTree(ctx, tree, nil, "test", identity, identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReadBlob(ctx, tree); err == nil {
		t.Fatal("ReadBlob accepted a tree object")
	}
	if _, err := repo.ReadBlob(ctx, commit); err == nil {
		t.Fatal("ReadBlob accepted a commit object")
	}
	if _, err := repo.ReadBlob(ctx, ObjectID(strings.Repeat("f", len(blob)))); err == nil {
		t.Fatal("ReadBlob accepted a well-formed but missing object ID")
	}
}
