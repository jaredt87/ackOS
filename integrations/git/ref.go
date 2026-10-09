package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrRefCASConflict = errors.New("git: ref compare-and-swap conflict")

func (r *Repository) UpdateRef(ctx context.Context, ref RefName, newValue, expectedOld ObjectID) error {
	if err := validateRef(ref); err != nil {
		return err
	}
	if err := r.validateObject(newValue); err != nil {
		return err
	}
	if err := r.validateObject(expectedOld); err != nil {
		return err
	}
	_, err := r.exec(ctx, []string{"update-ref", "--no-deref", string(ref), string(newValue), string(expectedOld)}, nil)
	if err != nil && strings.Contains(err.Error(), "is at ") && strings.Contains(err.Error(), " but expected ") {
		return fmt.Errorf("%w: %w", ErrRefCASConflict, err)
	}
	return err
}

func validateRef(ref RefName) error {
	s := string(ref)
	if s == "" || !strings.HasPrefix(s, "refs/") || strings.Contains(s, "@{") || strings.ContainsAny(s, "\x00\n\r ~^:?*[\\") {
		return fmt.Errorf("git: invalid ref name")
	}
	parts := strings.Split(s, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.HasSuffix(p, ".") || strings.HasSuffix(p, ".lock") || strings.Contains(p, "..") {
			return fmt.Errorf("git: invalid ref name")
		}
	}
	return nil
}
