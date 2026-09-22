package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func rejectSystemAttributesTargetSource(ctx context.Context, target Target) error {
	// GIT_ATTR_NOSYSTEM disables the system attributes source. Git's
	// GIT_ATTR_SYSTEM query then reports no pathname, which is an inactive
	// source rather than a provider configuration error.
	if _, disabled := os.LookupEnv("GIT_ATTR_NOSYSTEM"); disabled {
		return nil
	}

	resolved, err := filepath.EvalSymlinks(filepath.Join(target.Repository, target.Path))
	if err != nil {
		return nil
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return fmt.Errorf("resolve target for system attributes check: %w", err)
	}

	systemAttrs, err := runGitTarget(ctx, target, "var", "GIT_ATTR_SYSTEM")
	if err != nil {
		return fmt.Errorf("inspect system Git attributes file: %w", err)
	}
	systemAttrs = strings.TrimSpace(systemAttrs)
	if systemAttrs == "" {
		return nil
	}
	systemAttrs, err = filepath.Abs(systemAttrs)
	if err != nil {
		return fmt.Errorf("resolve system Git attributes file: %w", err)
	}
	systemResolved, err := filepath.EvalSymlinks(systemAttrs)
	if err != nil {
		return nil
	}
	systemResolved, err = filepath.Abs(systemResolved)
	if err != nil {
		return fmt.Errorf("resolve system Git attributes identity: %w", err)
	}
	if resolved == systemResolved {
		return fmt.Errorf("git target is configured as the active system attributes file")
	}
	return nil
}
