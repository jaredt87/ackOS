package git

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
)

func (r *Repository) WriteTree(ctx context.Context, entries []TreeEntry) (ObjectID, error) {
	copyEntries := append([]TreeEntry(nil), entries...)
	for _, e := range copyEntries {
		if err := validateMode(e.Mode); err != nil {
			return "", err
		}
		if err := validateTreePath(e.Path); err != nil {
			return "", err
		}
		if err := r.validateObject(e.Object); err != nil {
			return "", err
		}
	}
	sort.Slice(copyEntries, func(i, j int) bool { return treeKey(copyEntries[i]) < treeKey(copyEntries[j]) })
	for i := 1; i < len(copyEntries); i++ {
		if copyEntries[i-1].Path == copyEntries[i].Path {
			return "", fmt.Errorf("git: duplicate tree path %q", copyEntries[i].Path)
		}
	}
	var b bytes.Buffer
	for _, e := range copyEntries {
		typ := "blob"
		if e.Mode == "040000" || e.Mode == "40000" {
			typ = "tree"
		} else if e.Mode == "160000" {
			typ = "commit"
		}
		fmt.Fprintf(&b, "%s %s %s\t%s\x00", e.Mode, typ, e.Object, e.Path)
	}
	out, err := r.exec(ctx, []string{"mktree", "--missing", "-z"}, b.Bytes())
	if err != nil {
		return "", err
	}
	id := ObjectID(strings.TrimSpace(string(out)))
	if err := r.validateObject(id); err != nil {
		return "", err
	}
	return id, nil
}

func treeKey(e TreeEntry) string {
	if e.Mode == "040000" || e.Mode == "40000" {
		return e.Path + "/"
	}
	return e.Path
}

func validateTreePath(path string) error {
	if path == "" || strings.ContainsAny(path, "\x00\n\r") || strings.HasPrefix(path, "/") || strings.Contains(path, "//") {
		return fmt.Errorf("git: invalid tree path")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." || strings.EqualFold(part, ".git") {
			return fmt.Errorf("git: invalid tree path")
		}
	}
	return nil
}

func validateMode(m string) error {
	switch m {
	case "100644", "100755", "120000", "160000", "040000", "40000":
		return nil
	default:
		return fmt.Errorf("git: unsupported tree mode %q", m)
	}
}
