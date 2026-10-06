package git

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func testRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil { t.Skip("git unavailable") }
	d := t.TempDir()
	cmd := exec.Command("git", "init", "-q", d)
	if out, err := cmd.CombinedOutput(); err != nil { t.Fatalf("git init: %v: %s", err, out) }
	return d
}

func TestOpenRequiresRepositoryRoot(t *testing.T) {
	root := testRepo(t)
	if _, err := Open(root); err != nil { t.Fatal(err) }
	child := filepath.Join(root, "child")
	if err := exec.Command("mkdir", child).Run(); err != nil { t.Fatal(err) }
	if _, err := Open(child); err == nil { t.Fatal("accepted non-root path") }
}
