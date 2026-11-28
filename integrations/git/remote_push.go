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
	out, err := r.exec(ctx, []string{"push", "--porcelain", "--no-follow-tags", lease, "--", remote, refspec}, nil)
	if err != nil {
		return fmt.Errorf("git: push remote ref with exact lease: %w", err)
	}
	if err := validateLeasedPushStatus(string(out), ref); err != nil {
		return err
	}
	return nil
}

// validateLeasedPushStatus rejects a successful no-op. Git's push lease is
// not evaluated when the destination already equals the proposed new value,
// so an "[up to date]" response cannot prove expectedOld was current.
func validateLeasedPushStatus(output string, ref RefName) error {
	want := string(ref) + ":" + string(ref)
	for _, line := range strings.Split(output, "\n") {
		if len(line) < 2 || line[1] != '\t' {
			continue
		}
		fields := strings.Fields(line[2:])
		if len(fields) < 1 || fields[0] != want {
			continue
		}
		switch line[0] {
		case '=':
			return fmt.Errorf("git: remote ref already equals proposed value; expected-old lease was not established")
		case ' ', '+', '*':
			return nil
		default:
			return fmt.Errorf("git: unexpected remote ref push status %q", line[0])
		}
	}
	return fmt.Errorf("git: push response did not confirm the requested remote ref update")
}
