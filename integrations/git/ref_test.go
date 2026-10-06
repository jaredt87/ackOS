package git

import (
	"context"
	"strings"
	"testing"
)

func TestUpdateRefUsesExpectedOld(t *testing.T) {
	r, err := Open(testRepo(t))
	if err != nil { t.Fatal(err) }
	id := ObjectID(strings.Repeat("0", 40))
	newID, err := r.WriteBlob(context.Background(), []byte("x"))
	if err != nil { t.Fatal(err) }
	if err := r.UpdateRef(context.Background(), RefName("refs/heads/test"), newID, id); err != nil { t.Fatal(err) }
	if err := r.UpdateRef(context.Background(), RefName("refs/heads/test"), id, id); err == nil { t.Fatal("expected CAS failure") }
}
