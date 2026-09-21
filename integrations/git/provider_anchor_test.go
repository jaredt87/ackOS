package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAtomicWriteTargetRejectsNestedSymlinkParent(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	target.Path = filepath.Join("docs", "nested", "example.md")

	nested := filepath.Join(target.Repository, "docs", "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "example.md"), []byte("initial"), 0o644); err != nil {
		t.Fatal(err)
	}

	externalDir := t.TempDir()
	externalFile := filepath.Join(externalDir, "example.md")
	if err := os.WriteFile(externalFile, []byte("external"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(nested); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalDir, nested); err != nil {
		t.Fatal(err)
	}

	err := atomicWriteTarget(target, []byte("initial"), []byte("updated"))
	if err == nil || !strings.Contains(err.Error(), "parent directory") {
		t.Fatalf("atomicWriteTarget error = %v, want nested symlink-parent rejection", err)
	}
	content, err := os.ReadFile(externalFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "external" {
		t.Fatalf("external file changed = %q", content)
	}
}
