package git

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestWriteTree(t *testing.T) {
	r, err := Open(testRepo(t))
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
	if err := r.validateObject(tree); err != nil {
		t.Fatal(err)
	}
}

func TestWriteTreePreservesQuotedPath(t *testing.T) {
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
		{Mode: "100644", Path: "\"file\"", Object: blob},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", root, "ls-tree", "-z", "--name-only", string(tree)).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("\"file\"\x00")
	if string(out) != string(want) {
		t.Fatalf("path mismatch: got %q want %q", out, want)
	}
}

func TestWriteTreeRejectsUnsafePathsAndDuplicates(t *testing.T) {
	r, err := Open(testRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := r.WriteBlob(context.Background(), []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".", "..", ".git", ".GIT", "dir/../file"} {
		if _, err := r.WriteTree(context.Background(), []TreeEntry{
			{Mode: "100644", Path: path, Object: blob},
		}); err == nil {
			t.Fatalf("accepted unsafe tree path %q", path)
		}
	}
	if _, err := r.WriteTree(context.Background(), []TreeEntry{
		{Mode: "100644", Path: "file.txt", Object: blob},
		{Mode: "100644", Path: "file.txt", Object: blob},
	}); err == nil {
		t.Fatal("accepted duplicate tree path")
	}
}

func TestWriteTreeAcceptsGitlink(t *testing.T) {
	r, err := Open(testRepo(t))
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
	when := time.Unix(0, 0).UTC()
	commit, err := r.CommitTree(
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
	if _, err := r.WriteTree(context.Background(), []TreeEntry{
		{Mode: "160000", Path: "submodule", Object: commit},
	}); err != nil {
		t.Fatal(err)
	}
}
