package git

import (
	"context"
	"fmt"
	"strings"
)

func (r *Repository) CommitTree(ctx context.Context, tree ObjectID, parents []ObjectID, message string, author, committer Identity) (ObjectID, error) {
	if err := r.validateObject(tree); err != nil {
		return "", err
	}
	if message == "" {
		return "", fmt.Errorf("git: empty commit message")
	}
	if err := validateIdentity(author); err != nil {
		return "", fmt.Errorf("author: %w", err)
	}
	if err := validateIdentity(committer); err != nil {
		return "", fmt.Errorf("committer: %w", err)
	}
	args := []string{"commit-tree", string(tree)}
	for _, p := range parents {
		if err := r.validateObject(p); err != nil {
			return "", err
		}
		args = append(args, "-p", string(p))
	}
	env := []string{
		"GIT_AUTHOR_NAME=" + author.Name,
		"GIT_AUTHOR_EMAIL=" + author.Email,
		"GIT_AUTHOR_DATE=" + author.When.Format("2006-01-02T15:04:05-0700"),
		"GIT_COMMITTER_NAME=" + committer.Name,
		"GIT_COMMITTER_EMAIL=" + committer.Email,
		"GIT_COMMITTER_DATE=" + committer.When.Format("2006-01-02T15:04:05-0700"),
	}
	out, err := r.exec(ctx, args, []byte(message), env...)
	if err != nil {
		return "", err
	}
	id := ObjectID(strings.TrimSpace(string(out)))
	if err := r.validateObject(id); err != nil {
		return "", err
	}
	return id, nil
}

func validateIdentity(i Identity) error {
	if strings.TrimSpace(i.Name) == "" || strings.TrimSpace(i.Email) == "" || i.When.IsZero() {
		return fmt.Errorf("missing commit identity")
	}
	if strings.ContainsAny(i.Name+i.Email, "\r\n\x00<>") ||
		strings.Trim(i.Name, " \t,;:\"'\\") != i.Name ||
		strings.Trim(i.Email, " \t,;:\"'\\") != i.Email {
		return fmt.Errorf("invalid commit identity")
	}
	return nil
}
