package git

import (
	"context"
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
