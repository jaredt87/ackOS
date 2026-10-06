package git

import (
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
		if e.Path == "" || strings.ContainsAny(e.Path, "\x00\n\r") || strings.HasPrefix(e.Path, "/") || strings.Contains(e.Path, "//") {
			return "", fmt.Errorf("git: invalid tree path")
		}
		if err := r.validateObject(e.Object); err != nil {
			return "", err
		}
	}
	sort.Slice(copyEntries, func(i, j int) bool { return treeKey(copyEntries[i]) < treeKey(copyEntries[j]) })
	var b strings.Builder
	for _, e := range copyEntries {
		typ := "blob"
		if e.Mode == "040000" || e.Mode == "40000" {
			typ = "tree"
		}
		fmt.Fprintf(&b, "%s %s %s\t%s\n", e.Mode, typ, e.Object, e.Path)
	}
	out, err := r.exec(ctx, []string{"mktree", "--missing"}, []byte(b.String()))
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

func validateMode(m string) error {
	switch m {
	case "100644", "100755", "120000", "160000", "040000", "40000":
		return nil
	default:
		return fmt.Errorf("git: unsupported tree mode %q", m)
	}
}
