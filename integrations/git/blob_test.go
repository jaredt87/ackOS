package git

import (
	"context"
	"testing"
)

func TestWriteBlob(t *testing.T) {
	r, err := Open(testRepo(t))
	if err != nil { t.Fatal(err) }
	id, err := r.WriteBlob(context.Background(), []byte("hello"))
	if err != nil { t.Fatal(err) }
	if err := r.validateObject(id); err != nil { t.Fatal(err) }
}
