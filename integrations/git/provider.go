package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jaredt87/ackOS/control"
)

const blobStatePrefix = "git-blob:v1:"

var (
	ErrWrongSubject = errors.New("git provider: subject does not match configured target path")
	ErrStaleLineage = errors.New("git provider: stale observation; branch lineage or target blob changed")
	ErrNoop         = errors.New("git provider: desired blob is already current; refusing empty commit")
)

// Provider controls one existing regular file on one configured branch. It
// writes Git objects and moves only the configured ref; it never updates the
// working tree or index.
type Provider struct {
	repo   *Repository
	branch RefName
	path   string
	author Identity
}

type executionEvidence struct {
	Commit ObjectID `json:"commit"`
	Parent ObjectID `json:"parent"`
	Path   string   `json:"path"`
	Blob   ObjectID `json:"blob"`
}

func NewProvider(repo *Repository, branch RefName, targetPath string) (*Provider, error) {
	if repo == nil {
		return nil, fmt.Errorf("git provider: repository is required")
	}
	if err := validateRef(branch); err != nil {
		return nil, fmt.Errorf("git provider: branch must be a full refs/heads/... ref: %w", err)
	}
	if !strings.HasPrefix(string(branch), "refs/heads/") {
		return nil, fmt.Errorf("git provider: branch must use refs/heads/... form")
	}
	if err := validateTreePath(targetPath); err != nil {
		return nil, fmt.Errorf("git provider: invalid target path: %w", err)
	}
	if path.Clean(targetPath) != targetPath {
		return nil, fmt.Errorf("git provider: target path must be clean and repository-relative")
	}
	return &Provider{
		repo: repo, branch: branch, path: targetPath,
		author: Identity{Name: "ackOS", Email: "ackos@localhost"},
	}, nil
}

func (p *Provider) Subject() string { return p.path }

// State encodes a blob object ID in the same versioned format for observed
// and desired state. The contents of Version separately carry branch lineage.
func State(blob ObjectID) string { return blobStatePrefix + string(blob) }

func parseState(state string, repo *Repository) (ObjectID, error) {
	if !strings.HasPrefix(state, blobStatePrefix) {
		return "", fmt.Errorf("git provider: desired state must use %q blob encoding", blobStatePrefix)
	}
	id := ObjectID(strings.TrimPrefix(state, blobStatePrefix))
	if err := repo.validateObject(id); err != nil {
		return "", fmt.Errorf("git provider: invalid blob state: %w", err)
	}
	return id, nil
}

func lineageVersion(tip ObjectID) (uint64, error) {
	if len(tip) < 16 {
		return 0, fmt.Errorf("git provider: commit ID too short for lineage")
	}
	version, err := strconv.ParseUint(string(tip[:16]), 16, 64)
	if err != nil {
		return 0, fmt.Errorf("git provider: invalid commit lineage: %w", err)
	}
	return version, nil
}

func (p *Provider) Observe(ctx context.Context, req control.ObserveRequest) (control.Observation, error) {
	if req.Target.ID != p.path {
		return control.Observation{}, ErrWrongSubject
	}
	tip, err := p.repo.ReadRef(ctx, p.branch)
	if err != nil {
		return control.Observation{}, fmt.Errorf("git provider: read configured branch: %w", err)
	}
	commit, err := p.repo.ReadCommit(ctx, tip)
	if err != nil {
		return control.Observation{}, err
	}
	entry, err := p.readPath(ctx, commit.Tree, p.path)
	if err != nil {
		return control.Observation{}, err
	}
	if entry.Mode != "100644" && entry.Mode != "100755" {
		return control.Observation{}, fmt.Errorf("git provider: target %q must be an existing regular file", p.path)
	}
	if _, err := p.repo.ReadBlob(ctx, entry.Object); err != nil {
		return control.Observation{}, fmt.Errorf("git provider: target %q is not a readable blob: %w", p.path, err)
	}
	version, err := lineageVersion(tip)
	if err != nil {
		return control.Observation{}, err
	}
	return control.Observation{
		Resource: control.ResourceRef{ID: p.path, Fingerprint: State(entry.Object)},
		Version:  version, ObservedAt: time.Now().UTC(),
	}, nil
}

func (p *Provider) Execute(ctx context.Context, req control.ExecuteRequest) (control.Execution, error) {
	if req.ExecutionID == "" {
		return control.Execution{}, fmt.Errorf("git provider: execution ID is required")
	}
	if req.Target.ID != p.path || req.Before.Resource.ID != p.path {
		return control.Execution{}, ErrWrongSubject
	}
	desired, err := parseState(string(req.Payload), p.repo)
	if err != nil {
		return control.Execution{}, err
	}
	if _, err := p.repo.ReadBlob(ctx, desired); err != nil {
		return control.Execution{}, fmt.Errorf("git provider: desired object is not an existing blob: %w", err)
	}
	beforeBlob, err := parseState(req.Before.Resource.Fingerprint, p.repo)
	if err != nil {
		return control.Execution{}, err
	}
	tip, err := p.repo.ReadRef(ctx, p.branch)
	if err != nil {
		return control.Execution{}, err
	}
	version, err := lineageVersion(tip)
	if err != nil {
		return control.Execution{}, err
	}
	current, err := p.repo.ReadCommit(ctx, tip)
	if err != nil {
		return control.Execution{}, err
	}
	entry, err := p.readPath(ctx, current.Tree, p.path)
	if err != nil {
		return control.Execution{}, err
	}
	if entry.Object != beforeBlob || version != req.Before.Version {
		return control.Execution{}, ErrStaleLineage
	}
	if desired == entry.Object {
		return control.Execution{}, ErrNoop
	}

	newTree, err := p.replacePath(ctx, current.Tree, strings.Split(p.path, "/"), desired)
	if err != nil {
		return control.Execution{}, err
	}
	now := time.Now().UTC()
	identity := p.author
	identity.When = now
	message := fmt.Sprintf("ackOS: update %s\n\nAckOS-Execution: %s\nAckOS-Target: %s\n", p.path, req.ExecutionID, p.path)
	newCommit, err := p.repo.CommitTree(ctx, newTree, []ObjectID{tip}, message, identity, identity)
	if err != nil {
		return control.Execution{}, err
	}
	// Use the full tip read above as expected-old. This CAS protects the
	// read-to-write interval; the authorized lineage comparison is 64-bit.
	if err := p.repo.UpdateRef(ctx, p.branch, newCommit, tip); err != nil {
		return control.Execution{}, fmt.Errorf("git provider: branch update rejected: %w", err)
	}
	evidence, err := json.Marshal(executionEvidence{Commit: newCommit, Parent: tip, Path: p.path, Blob: desired})
	if err != nil {
		return control.Execution{}, err
	}
	return control.Execution{ExecutionID: req.ExecutionID, Evidence: evidence}, nil
}

func (p *Provider) Verify(ctx context.Context, req control.VerifyRequest) (control.Verification, error) {
	if req.ExecutionID == "" || req.Execution.ExecutionID != req.ExecutionID {
		return control.Verification{}, fmt.Errorf("git provider: execution ID mismatch")
	}
	if req.Expected.ID != p.path || req.Before.Resource.ID != p.path {
		return control.Verification{}, ErrWrongSubject
	}
	desired, err := parseState(req.Expected.Fingerprint, p.repo)
	if err != nil {
		return control.Verification{}, err
	}
	if _, err := p.repo.ReadBlob(ctx, desired); err != nil {
		return control.Verification{}, fmt.Errorf("git provider: expected state is not an existing blob: %w", err)
	}
	var evidence executionEvidence
	if err := json.Unmarshal(req.Execution.Evidence, &evidence); err != nil {
		return control.Verification{}, fmt.Errorf("git provider: invalid execution evidence: %w", err)
	}
	if evidence.Commit == "" || evidence.Parent == "" || evidence.Path != p.path || evidence.Blob != desired {
		return control.Verification{}, fmt.Errorf("git provider: execution evidence does not match request")
	}
	tip, err := p.repo.ReadRef(ctx, p.branch)
	if err != nil {
		return control.Verification{}, err
	}
	if tip != evidence.Commit {
		return control.Verification{}, fmt.Errorf("git provider: configured branch no longer points at claimed commit")
	}
	commit, err := p.repo.ReadCommit(ctx, evidence.Commit)
	if err != nil {
		return control.Verification{}, err
	}
	if len(commit.Parents) != 1 || commit.Parents[0] != evidence.Parent {
		return control.Verification{}, fmt.Errorf("git provider: claimed commit parent mismatch")
	}
	parentVersion, err := lineageVersion(evidence.Parent)
	if err != nil {
		return control.Verification{}, err
	}
	if parentVersion != req.Before.Version {
		return control.Verification{}, fmt.Errorf("git provider: claimed parent does not match authorized lineage")
	}
	if !hasExactlyOneTrailer(commit.Message, "AckOS-Execution", req.ExecutionID) ||
		!hasExactlyOneTrailer(commit.Message, "AckOS-Target", p.path) {
		return control.Verification{}, fmt.Errorf("git provider: commit trailer mismatch")
	}
	parent, err := p.repo.ReadCommit(ctx, evidence.Parent)
	if err != nil {
		return control.Verification{}, err
	}
	beforeBlob, err := parseState(req.Before.Resource.Fingerprint, p.repo)
	if err != nil {
		return control.Verification{}, err
	}
	if _, err := p.repo.ReadBlob(ctx, beforeBlob); err != nil {
		return control.Verification{}, err
	}
	beforeEntry, err := p.readPath(ctx, parent.Tree, p.path)
	if err != nil {
		return control.Verification{}, err
	}
	if beforeEntry.Object != beforeBlob {
		return control.Verification{}, fmt.Errorf("git provider: authorized target blob does not match claimed parent")
	}
	beforeFiles, err := p.flattenTree(ctx, parent.Tree, "")
	if err != nil {
		return control.Verification{}, err
	}
	afterFiles, err := p.flattenTree(ctx, commit.Tree, "")
	if err != nil {
		return control.Verification{}, err
	}
	changed := changedPaths(beforeFiles, afterFiles)
	if len(changed) != 1 || changed[0] != p.path {
		return control.Verification{}, fmt.Errorf("git provider: commit must change only target path %q", p.path)
	}
	afterEntry, err := p.readPath(ctx, commit.Tree, p.path)
	if err != nil {
		return control.Verification{}, err
	}
	if afterEntry.Object != desired || (afterEntry.Mode != "100644" && afterEntry.Mode != "100755") {
		return control.Verification{}, fmt.Errorf("git provider: committed target does not match expected blob")
	}
	observed, err := p.Observe(ctx, control.ObserveRequest{Target: control.ResourceRef{ID: p.path}})
	if err != nil {
		return control.Verification{}, err
	}
	return control.Verification{
		Resource:   observed.Resource,
		Version:    observed.Version,
		VerifiedAt: observed.ObservedAt,
	}, nil
}

func hasExactlyOneTrailer(message, key, value string) bool {
	count := 0
	for _, line := range strings.Split(message, "\n") {
		if strings.HasPrefix(line, key+": ") {
			count++
			if line != key+": "+value {
				return false
			}
		}
	}
	return count == 1
}

func (p *Provider) readPath(ctx context.Context, tree ObjectID, target string) (TreeEntry, error) {
	parts := strings.Split(target, "/")
	current := tree
	for i, part := range parts {
		entries, err := p.repo.ReadTree(ctx, current)
		if err != nil {
			return TreeEntry{}, err
		}
		found := false
		for _, entry := range entries {
			if entry.Path != part {
				continue
			}
			found = true
			if i == len(parts)-1 {
				return TreeEntry{Mode: entry.Mode, Path: target, Object: entry.Object}, nil
			}
			if entry.Mode != "040000" && entry.Mode != "40000" {
				return TreeEntry{}, fmt.Errorf("git provider: parent of target %q is not a directory", target)
			}
			current = entry.Object
			break
		}
		if !found {
			return TreeEntry{}, fmt.Errorf("git provider: target path %q does not exist", target)
		}
	}
	return TreeEntry{}, fmt.Errorf("git provider: invalid target path")
}

func (p *Provider) replacePath(ctx context.Context, tree ObjectID, parts []string, desired ObjectID) (ObjectID, error) {
	entries, err := p.repo.ReadTree(ctx, tree)
	if err != nil {
		return "", err
	}
	for i := range entries {
		if entries[i].Path != parts[0] {
			continue
		}
		if len(parts) == 1 {
			if entries[i].Mode != "100644" && entries[i].Mode != "100755" {
				return "", fmt.Errorf("git provider: target is not a regular file")
			}
			entries[i].Object = desired
		} else {
			if entries[i].Mode != "040000" && entries[i].Mode != "40000" {
				return "", fmt.Errorf("git provider: target parent is not a directory")
			}
			child, err := p.replacePath(ctx, entries[i].Object, parts[1:], desired)
			if err != nil {
				return "", err
			}
			entries[i].Object = child
		}
		return p.repo.WriteTree(ctx, entries)
	}
	return "", fmt.Errorf("git provider: target path %q does not exist", p.path)
}

type treeFile struct {
	mode string
	id   ObjectID
}

func (p *Provider) flattenTree(ctx context.Context, tree ObjectID, prefix string) (map[string]treeFile, error) {
	entries, err := p.repo.ReadTree(ctx, tree)
	if err != nil {
		return nil, err
	}
	result := make(map[string]treeFile)
	for _, entry := range entries {
		name := entry.Path
		if prefix != "" {
			name = prefix + "/" + name
		}
		if entry.Mode == "040000" || entry.Mode == "40000" {
			children, err := p.flattenTree(ctx, entry.Object, name)
			if err != nil {
				return nil, err
			}
			for key, value := range children {
				result[key] = value
			}
		} else {
			result[name] = treeFile{mode: entry.Mode, id: entry.Object}
		}
	}
	return result, nil
}

func changedPaths(before, after map[string]treeFile) []string {
	all := make(map[string]struct{}, len(before)+len(after))
	for key := range before {
		all[key] = struct{}{}
	}
	for key := range after {
		all[key] = struct{}{}
	}
	changed := make([]string, 0, 1)
	for key := range all {
		if before[key] != after[key] {
			changed = append(changed, key)
		}
	}
	return changed
}
