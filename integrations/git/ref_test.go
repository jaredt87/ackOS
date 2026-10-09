package git

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestUpdateRefUsesExpectedOld(t *testing.T) {
	r, err := Open(testRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	id := ObjectID(strings.Repeat("0", 40))
	newID, err := r.WriteBlob(context.Background(), []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateRef(
		context.Background(),
		RefName("refs/tags/test"),
		newID,
		id,
	); err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateRef(
		context.Background(),
		RefName("refs/tags/test"),
		id,
		id,
	); err == nil {
		t.Fatal("expected CAS failure")
	} else if !errors.Is(err, ErrRefCASConflict) {
		t.Fatalf("CAS error = %v, want ErrRefCASConflict", err)
	}
}

func TestUpdateRefClassifiesDeletedRefAsCASConflict(t *testing.T) {
	ctx := context.Background()
	root := testRepo(t)
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	old, err := r.WriteBlob(ctx, []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	newID, err := r.WriteBlob(ctx, []byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "update-ref", "refs/heads/race", string(old), strings.Repeat("0", len(old)))
	runGitTest(t, root, "update-ref", "-d", "refs/heads/race")
	err = r.UpdateRef(ctx, RefName("refs/heads/race"), newID, old)
	if err == nil {
		t.Fatal("UpdateRef accepted a concurrently deleted ref")
	}
	if !errors.Is(err, ErrRefCASConflict) {
		t.Fatalf("deleted-ref update error = %v, want ErrRefCASConflict", err)
	}
}
