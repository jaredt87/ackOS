package git

import (
	"context"
	"fmt"
	"strings"
)

// PushRefWithLease updates one remote ref only when its current value equals
// expectedOld. The remote must be operator-configured; callers must not accept
// a remote URL from an agent-controlled resource identifier.
//
// The explicit expected object ID is essential. Git's implicit
// --force-with-lease form can derive its expectation from mutable local
// tracking refs and is not suitable for an authorization precondition.
func (r *Repository) PushRefWithLease(ctx context.Context, remote string, ref RefName, newValue, expectedOld ObjectID) error {
	if strings.TrimSpace(remote) == "" || strings.ContainsAny(remote, "\x00\r\n") || strings.HasPrefix(remote, "-") {
		return fmt.Errorf("git: invalid configured remote")
	}
	if err := validateBranchRef(ref); err != nil {
		return err
	}
	if err := r.validateObject(newValue); err != nil {
		return fmt.Errorf("git: invalid new ref value: %w", err)
	}
	if err := r.validateObject(expectedOld); err != nil {
		return fmt.Errorf("git: invalid expected old ref value: %w", err)
	}
	newCommit, err := r.ReadCommit(ctx, newValue)
	if err != nil {
		return fmt.Errorf("git: read new remote ref commit: %w", err)
	}
	if len(newCommit.Parents) != 1 || newCommit.Parents[0] != expectedOld {
		return fmt.Errorf("git: remote ref update must publish a single-child commit of the expected old value")
	}

	lease := "--force-with-lease=" + string(ref) + ":" + string(expectedOld)
	refspec := string(newValue) + ":" + string(ref)
	if _, err := r.exec(ctx, []string{"push", "--porcelain", lease, "--", remote, refspec}, nil); err != nil {
		return fmt.Errorf("git: push remote ref with exact lease: %w", err)
	}
	return nil
}
