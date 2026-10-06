package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

var ErrNotRepoRoot = errors.New("git: path is not a git repository root")

func Open(path string) (*Repository, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git: locate executable: %w", err)
	}
	root, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	out, err := runRaw(context.Background(), git, root, "rev-parse", "--show-toplevel", "--absolute-git-dir", "--git-common-dir", "--show-object-format")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNotRepoRoot, root)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 4 || lines[2] == "" || lines[3] == "" {
		return nil, ErrNotRepoRoot
	}
	top, err := filepath.EvalSymlinks(lines[0])
	if err != nil || filepath.Clean(top) != filepath.Clean(root) {
		return nil, fmt.Errorf("%w: %s", ErrNotRepoRoot, root)
	}
	common := lines[2]
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	common, err = filepath.EvalSymlinks(common)
	if err != nil || !pathWithin(root, common) {
		return nil, fmt.Errorf("%w: %s", ErrNotRepoRoot, root)
	}
	hashLen := map[string]int{"sha1": 40, "sha256": 64}[lines[3]]
	if hashLen == 0 {
		return nil, fmt.Errorf("git: unsupported object format %q", lines[3])
	}
	gitDir, err := filepath.EvalSymlinks(lines[1])
	if err != nil || !pathWithin(root, gitDir) {
		return nil, fmt.Errorf("%w: %s", ErrNotRepoRoot, root)
	}
	return &Repository{root: root, gitDir: gitDir, git: git, objectHash: hashLen}, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ""
}

func (r *Repository) exec(ctx context.Context, args []string, stdin []byte, env ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fixed := []string{"--no-replace-objects", "-C", r.root, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "commit.gpgSign=false", "--git-dir", r.gitDir}
	fixed = append(fixed, args...)
	cmd := exec.CommandContext(ctx, r.git, fixed...)
	cmd.Dir = r.root
	cmd.Env = cleanEnv(env...)
	cmd.Stdin = bytes.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func runRaw(ctx context.Context, git, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, git, "--no-replace-objects", "-C", root, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false")
	cmd.Args = append(cmd.Args, args...)
	cmd.Env = cleanEnv()
	return cmd.Output()
}

func cleanEnv(extra ...string) []string {
	return append([]string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "LC_ALL=C", "TZ=UTC"}, extra...)
}
