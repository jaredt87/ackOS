package git

import "time"

type ObjectID string
type RefName string

type TreeEntry struct {
	Mode   string
	Path   string
	Object ObjectID
}

type Identity struct {
	Name  string
	Email string
	When  time.Time
}

type Repository struct {
	root       string
	gitDir     string
	git        string
	objectHash int
}
