package git

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCommitTreeUsesExplicitIdentity(t *testing.T) {
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
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("UTC", 0))
	id, err := r.CommitTree(
		context.Background(),
		tree,
		nil,
		"message",
		Identity{Name: "Author", Email: "author@example.com", When: when},
		Identity{Name: "Committer", Email: "committer@example.com", When: when},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.UpdateRef(
		context.Background(),
		RefName("refs/heads/test"),
		id,
		ObjectID(strings.Repeat("0", 40)),
	); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(
		"git",
		"-C",
		root,
		"show",
		"-s",
		"--format=%an <%ae>|%cn <%ce>|%ad",
		"--date=iso-strict",
		string(id),
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		string(out),
		"Author <author@example.com>|Committer <committer@example.com>",
	) {
		t.Fatalf("identity mismatch: %s", out)
	}
}
