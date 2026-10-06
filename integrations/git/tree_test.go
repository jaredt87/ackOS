package git

import (
	"context"
	"strings"
	"testing"
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

func TestWriteTreeRejectsUnsafePathsAndDuplicates(t *testing.T) {
	r, err := Open(testRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := r.WriteBlob(context.Background(), []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".", "..", ".git", "dir/../file"} {
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
	blob, err := r.WriteBlob(context.Background(), []byte("not-a-commit"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.WriteTree(context.Background(), []TreeEntry{
		{Mode: "160000", Path: "submodule", Object: blob},
	}); err != nil {
		if !strings.Contains(err.Error(), "not a valid object") {
			t.Fatal(err)
		}
	}
}
