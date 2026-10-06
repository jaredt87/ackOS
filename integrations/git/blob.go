package git

import (
	"context"
	"fmt"
	"strings"
)

func (r *Repository) WriteBlob(ctx context.Context, content []byte) (ObjectID, error) {
	out, err := r.exec(ctx, []string{"hash-object", "-w", "--stdin"}, content)
	if err != nil {
		return "", err
	}
	id := ObjectID(strings.TrimSpace(string(out)))
	if err := r.validateObject(id); err != nil {
		return "", err
	}
	return id, nil
}

func (r *Repository) validateObject(id ObjectID) error {
	if len(id) != r.objectHash {
		return fmt.Errorf("git: invalid object id length")
	}
	for _, c := range string(id) {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return fmt.Errorf("git: invalid object id")
		}
	}
	return nil
}
