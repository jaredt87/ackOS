package git

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// ReadBlob returns the bytes of an existing blob object. Requiring the blob
// type prevents callers from treating arbitrary Git objects as file content.
func (r *Repository) ReadBlob(ctx context.Context, id ObjectID) ([]byte, error) {
	if err := r.validateObject(id); err != nil {
		return nil, err
	}
	typeOut, err := r.exec(ctx, []string{"cat-file", "-t", string(id)}, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(typeOut)) != "blob" {
		return nil, fmt.Errorf("git: object %q is not a blob", id)
	}
	out, err := r.exec(ctx, []string{"cat-file", "blob", string(id)}, nil)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repository) ReadRef(ctx context.Context, ref RefName) (ObjectID, error) {
	if err := validateRef(ref); err != nil {
		return "", err
	}
	out, err := r.exec(ctx, []string{"rev-parse", "--verify", "--end-of-options", string(ref)}, nil)
	if err != nil {
		return "", err
	}
	id := ObjectID(strings.TrimSpace(string(out)))
	if err := r.validateObject(id); err != nil {
		return "", err
	}
	return id, nil
}

func (r *Repository) ReadCommit(ctx context.Context, id ObjectID) (Commit, error) {
	if err := r.validateObject(id); err != nil {
		return Commit{}, err
	}
	out, err := r.exec(ctx, []string{"cat-file", "commit", string(id)}, nil)
	if err != nil {
		return Commit{}, err
	}
	header, message, ok := bytes.Cut(out, []byte("\n\n"))
	if !ok {
		return Commit{}, fmt.Errorf("git: invalid commit object")
	}
	var commit Commit
	commit.ID = id
	for _, line := range bytes.Split(header, []byte("\n")) {
		fields := bytes.SplitN(line, []byte(" "), 2)
		if len(fields) != 2 {
			return Commit{}, fmt.Errorf("git: invalid commit header")
		}
		switch string(fields[0]) {
		case "tree":
			commit.Tree = ObjectID(fields[1])
		case "parent":
			commit.Parents = append(commit.Parents, ObjectID(fields[1]))
		}
	}
	if err := r.validateObject(commit.Tree); err != nil {
		return Commit{}, fmt.Errorf("git: invalid commit tree: %w", err)
	}
	for _, parent := range commit.Parents {
		if err := r.validateObject(parent); err != nil {
			return Commit{}, fmt.Errorf("git: invalid commit parent: %w", err)
		}
	}
	commit.Message = string(message)
	return commit, nil
}

func (r *Repository) ReadTree(ctx context.Context, id ObjectID) ([]TreeEntry, error) {
	if err := r.validateObject(id); err != nil {
		return nil, err
	}
	typeOut, err := r.exec(ctx, []string{"cat-file", "-t", string(id)}, nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(typeOut)) != "tree" {
		return nil, fmt.Errorf("git: object %q is not a tree", id)
	}
	out, err := r.exec(ctx, []string{"ls-tree", "-z", string(id)}, nil)
	if err != nil {
		return nil, err
	}
	var entries []TreeEntry
	for _, record := range bytes.Split(out, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		meta, path, ok := bytes.Cut(record, []byte("\t"))
		if !ok {
			return nil, fmt.Errorf("git: invalid tree entry")
		}
		fields := bytes.Split(meta, []byte(" "))
		if len(fields) != 3 {
			return nil, fmt.Errorf("git: invalid tree entry")
		}
		object := ObjectID(fields[2])
		if err := r.validateObject(object); err != nil {
			return nil, fmt.Errorf("git: invalid tree object: %w", err)
		}
		entries = append(entries, TreeEntry{
			Mode:   string(fields[0]),
			Object: object,
			Path:   string(path),
		})
	}
	return entries, nil
}
