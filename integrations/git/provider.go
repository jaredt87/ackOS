package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jaredt87/ackOS/control"
)

const (
	fileResourcePrefix    = "git-file-v1:"
	tipFingerprintPrefix  = "git-tip-v1:"
	fileFingerprintPrefix = "git-file-v1:sha256:"
	refRecoveryTimeout    = 5 * time.Second
)

var ErrBranchAdvancedAfterExecution = errors.New("git branch advanced after execution")

// FileProvider replaces one existing regular file in a branch tree without
// changing a working tree or index.
type FileProvider struct {
	repo *Repository

	// These package-private seams allow deterministic boundary tests without
	// adding a general Git-operation abstraction.
	beforeWriteTree      func() error
	beforeUpdateRef      func() error
	afterUpdateRef       func(context.Context) error
	beforeFinalVerifyRef func()
}

var _ control.Provider = (*FileProvider)(nil)

func NewFileProvider(repo *Repository) (*FileProvider, error) {
	if repo == nil {
		return nil, fmt.Errorf("git: repository is required")
	}
	return &FileProvider{repo: repo}, nil
}

// FileResourceID encodes the canonical repository root, full branch ref, and
// repository-relative target path into the provider resource identity.
func (r *Repository) FileResourceID(ref RefName, targetPath string) (string, error) {
	if !utf8.ValidString(string(ref)) {
		return "", fmt.Errorf("git: branch ref must be UTF-8")
	}
	if err := validateBranchRef(ref); err != nil {
		return "", err
	}
	if !utf8.ValidString(targetPath) || strings.Contains(targetPath, "\\") {
		return "", fmt.Errorf("git: invalid UTF-8 or separator in target path")
	}
	if err := validateTreePath(targetPath); err != nil {
		return "", err
	}
	root, err := canonicalRoot(r.root)
	if err != nil {
		return "", err
	}
	identity, err := json.Marshal([]string{root, string(ref), targetPath})
	if err != nil {
		return "", err
	}
	return fileResourcePrefix + base64.RawURLEncoding.EncodeToString(identity), nil
}

// FileStateFingerprint returns the fingerprint for a desired regular-file
// mode and exact byte sequence.
func FileStateFingerprint(mode string, content []byte) (string, error) {
	if mode != "100644" && mode != "100755" {
		return "", fmt.Errorf("git: unsupported replacement file mode %q", mode)
	}
	input := append([]byte("git-file-v1\x00"+mode+"\x00"), content...)
	digest := sha256.Sum256(input)
	return fileFingerprintPrefix + hex.EncodeToString(digest[:]), nil
}

func (p *FileProvider) Observe(ctx context.Context, req control.ObserveRequest) (control.Observation, error) {
	identity, err := p.decodeResourceID(req.Target.ID)
	if err != nil {
		return control.Observation{}, err
	}
	tip, err := p.repo.ReadRef(ctx, identity.Ref)
	if err != nil {
		return control.Observation{}, err
	}
	commit, err := p.repo.ReadCommit(ctx, tip)
	if err != nil {
		return control.Observation{}, err
	}
	entry, err := p.readPath(ctx, commit.Tree, strings.Split(identity.Path, "/"))
	if err != nil {
		return control.Observation{}, err
	}
	content, err := p.repo.ReadBlob(ctx, entry.Object)
	if err != nil {
		return control.Observation{}, err
	}
	fingerprint, err := FileStateFingerprint(entry.Mode, content)
	if err != nil {
		return control.Observation{}, err
	}
	return control.Observation{
		Resource:         control.ResourceRef{ID: req.Target.ID, Fingerprint: fingerprint},
		ConcurrencyToken: tipFingerprintPrefix + string(tip),
		ObservedAt:       time.Now().UTC(),
	}, nil
}

func (p *FileProvider) Execute(ctx context.Context, req control.ExecuteRequest) (control.Execution, error) {
	if req.ExecutionID == "" || strings.ContainsAny(req.ExecutionID, "\x00\r\n") {
		return control.Execution{}, fmt.Errorf("git: execution ID is required and must be a single line")
	}
	identity, err := p.decodeResourceID(req.Target.ID)
	if err != nil {
		return control.Execution{}, err
	}
	if req.Desired.ID != req.Target.ID || req.Before.Resource.ID != req.Target.ID {
		return control.Execution{}, fmt.Errorf("git: target, desired, and authorized observation must identify the same file resource")
	}
	mode, content, err := decodeFilePayload(req.Payload)
	if err != nil {
		return control.Execution{}, err
	}
	desiredFingerprint, err := FileStateFingerprint(mode, content)
	if err != nil {
		return control.Execution{}, err
	}
	if req.Desired.Fingerprint != desiredFingerprint {
		return control.Execution{}, fmt.Errorf("git: payload does not match desired fingerprint")
	}
	beforeTip, err := parseTipFingerprint(p.repo, req.Before.ConcurrencyToken)
	if err != nil {
		return control.Execution{}, fmt.Errorf("git: invalid authorized tip: %w", err)
	}
	currentTip, err := p.repo.ReadRef(ctx, identity.Ref)
	if err != nil {
		return control.Execution{}, err
	}
	if currentTip != beforeTip {
		return control.Execution{}, fmt.Errorf("%w: branch tip is %s, authorized tip is %s", control.ErrStaleObservation, currentTip, beforeTip)
	}
	beforeCommit, err := p.repo.ReadCommit(ctx, beforeTip)
	if err != nil {
		return control.Execution{}, err
	}
	prior, err := p.readPath(ctx, beforeCommit.Tree, strings.Split(identity.Path, "/"))
	if err != nil {
		return control.Execution{}, err
	}
	if prior.Mode != "100644" && prior.Mode != "100755" {
		return control.Execution{}, fmt.Errorf("git: target must be an existing regular file")
	}
	priorContent, err := p.repo.ReadBlob(ctx, prior.Object)
	if err != nil {
		return control.Execution{}, fmt.Errorf("git: target regular-file entry does not reference a blob: %w", err)
	}
	beforeFingerprint, err := FileStateFingerprint(prior.Mode, priorContent)
	if err != nil {
		return control.Execution{}, err
	}
	if beforeFingerprint != req.Before.Resource.Fingerprint {
		return control.Execution{}, fmt.Errorf("git: authorized file state does not match its concurrency token")
	}
	blob, err := p.repo.WriteBlob(ctx, content)
	if err != nil {
		return control.Execution{}, err
	}
	newTree, err := p.replacePath(ctx, beforeCommit.Tree, strings.Split(identity.Path, "/"), blob, mode)
	if err != nil {
		return control.Execution{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	who := Identity{Name: "ackOS Git Provider", Email: "ackos-git-provider@localhost", When: now}
	newCommit, err := p.repo.CommitTree(ctx, newTree, []ObjectID{beforeTip}, executionCommitMessage(req.ExecutionID), who, who)
	if err != nil {
		return control.Execution{}, err
	}
	evidence, err := json.Marshal(executionEvidence{
		Schema:       "git-file-replace-v1",
		ExecutionID:  req.ExecutionID,
		BeforeTip:    string(beforeTip),
		ResultCommit: string(newCommit),
	})
	if err != nil {
		return control.Execution{}, fmt.Errorf("git: encode execution evidence before ref commit: %w", err)
	}
	if p.beforeUpdateRef != nil {
		if err := p.beforeUpdateRef(); err != nil {
			return control.Execution{}, err
		}
	}
	updateErr := p.repo.UpdateRef(ctx, identity.Ref, newCommit, beforeTip)
	if updateErr == nil && p.afterUpdateRef != nil {
		updateErr = p.afterUpdateRef(ctx)
	}
	if updateErr != nil {
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refRecoveryTimeout)
		current, readErr := p.repo.ReadRef(recoveryCtx, identity.Ref)
		cancel()
		if readErr != nil {
			return control.Execution{}, errors.Join(
				fmt.Errorf("git: update ref: %w", updateErr),
				fmt.Errorf("git: read ref after failed update: %w", readErr),
			)
		}
		if current == newCommit {
			// update-ref may have completed its atomic ref write before its
			// process reported an error (for example, cancellation at exit).
			// The ref itself proves this exact commit is installed.
			return control.Execution{ExecutionID: req.ExecutionID, Evidence: evidence}, nil
		}
		if current != beforeTip {
			return control.Execution{}, fmt.Errorf("%w: compare-and-swap failed: %w", control.ErrStaleObservation, updateErr)
		}
		return control.Execution{}, updateErr
	}
	return control.Execution{ExecutionID: req.ExecutionID, Evidence: evidence}, nil
}

func (p *FileProvider) Verify(ctx context.Context, req control.VerifyRequest) (control.Verification, error) {
	if req.ExecutionID == "" || req.Execution.ExecutionID != req.ExecutionID {
		return control.Verification{}, fmt.Errorf("git: execution ID mismatch during verification")
	}
	identity, err := p.decodeResourceID(req.Expected.ID)
	if err != nil {
		return control.Verification{}, err
	}
	if req.Before.Resource.ID != req.Expected.ID {
		return control.Verification{}, fmt.Errorf("git: before and expected resources differ")
	}
	beforeTip, err := parseTipFingerprint(p.repo, req.Before.ConcurrencyToken)
	if err != nil {
		return control.Verification{}, fmt.Errorf("git: invalid authorized tip in verification: %w", err)
	}
	evidence, err := decodeExecutionEvidence(req.Execution.Evidence)
	if err != nil {
		return control.Verification{}, err
	}
	if evidence.ExecutionID != req.ExecutionID || evidence.BeforeTip != string(beforeTip) {
		return control.Verification{}, fmt.Errorf("git: execution evidence does not match authorized execution")
	}
	resultCommitID := ObjectID(evidence.ResultCommit)
	if err := p.repo.validateObject(resultCommitID); err != nil {
		return control.Verification{}, fmt.Errorf("git: invalid result commit ID: %w", err)
	}
	currentTip, err := p.repo.ReadRef(ctx, identity.Ref)
	if err != nil {
		return control.Verification{}, err
	}
	if currentTip != resultCommitID {
		return control.Verification{}, fmt.Errorf("%w: current tip is %s, execution commit is %s", ErrBranchAdvancedAfterExecution, currentTip, resultCommitID)
	}
	beforeCommit, err := p.repo.ReadCommit(ctx, beforeTip)
	if err != nil {
		return control.Verification{}, err
	}
	beforeEntry, err := p.readPath(ctx, beforeCommit.Tree, strings.Split(identity.Path, "/"))
	if err != nil {
		return control.Verification{}, err
	}
	beforeContent, err := p.repo.ReadBlob(ctx, beforeEntry.Object)
	if err != nil {
		return control.Verification{}, err
	}
	beforeFingerprint, err := FileStateFingerprint(beforeEntry.Mode, beforeContent)
	if err != nil {
		return control.Verification{}, err
	}
	if beforeFingerprint != req.Before.Resource.Fingerprint {
		return control.Verification{}, fmt.Errorf("git: authorized file state does not match its concurrency token")
	}
	resultCommit, err := p.repo.ReadCommit(ctx, resultCommitID)
	if err != nil {
		return control.Verification{}, err
	}
	if resultCommit.Message != executionCommitMessage(req.ExecutionID) {
		return control.Verification{}, fmt.Errorf("git: result commit is not bound to this execution ID")
	}
	if len(resultCommit.Parents) != 1 || resultCommit.Parents[0] != beforeTip {
		return control.Verification{}, fmt.Errorf("git: result commit is not a single-child commit of the authorized tip")
	}
	beforeEntries, err := flattenTree(ctx, p.repo, beforeCommit.Tree, "")
	if err != nil {
		return control.Verification{}, err
	}
	resultEntries, err := flattenTree(ctx, p.repo, resultCommit.Tree, "")
	if err != nil {
		return control.Verification{}, err
	}
	if err := compareTreeLeaves(beforeEntries, resultEntries, identity.Path); err != nil {
		return control.Verification{}, err
	}
	entry, ok := resultEntries[identity.Path]
	if !ok {
		return control.Verification{}, fmt.Errorf("git: target path is missing from result tree")
	}
	content, err := p.repo.ReadBlob(ctx, entry.Object)
	if err != nil {
		return control.Verification{}, err
	}
	fingerprint, err := FileStateFingerprint(entry.Mode, content)
	if err != nil {
		return control.Verification{}, err
	}
	if fingerprint != req.Expected.Fingerprint {
		return control.Verification{}, fmt.Errorf("git: verified file does not match desired fingerprint")
	}
	if p.beforeFinalVerifyRef != nil {
		p.beforeFinalVerifyRef()
	}
	currentTip, err = p.repo.ReadRef(ctx, identity.Ref)
	if err != nil {
		return control.Verification{}, err
	}
	if currentTip != resultCommitID {
		return control.Verification{}, fmt.Errorf("%w: current tip is %s, execution commit is %s", ErrBranchAdvancedAfterExecution, currentTip, resultCommitID)
	}
	return control.Verification{
		Resource:         req.Expected,
		ConcurrencyToken: tipFingerprintPrefix + string(resultCommitID),
		VerifiedAt:       time.Now().UTC(),
	}, nil
}

func executionCommitMessage(executionID string) string {
	return "ackOS: replace tracked file\n\nAck-Execution-Id: " + executionID + "\n"
}

type fileIdentity struct {
	Root string
	Ref  RefName
	Path string
}

func (p *FileProvider) decodeResourceID(id string) (fileIdentity, error) {
	if !strings.HasPrefix(id, fileResourcePrefix) {
		return fileIdentity{}, fmt.Errorf("git: invalid file resource ID")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, fileResourcePrefix))
	if err != nil {
		return fileIdentity{}, fmt.Errorf("git: invalid file resource ID encoding: %w", err)
	}
	var parts []string
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) != 3 {
		return fileIdentity{}, fmt.Errorf("git: invalid file resource identity")
	}
	if !utf8.ValidString(parts[0]) || !utf8.ValidString(parts[1]) || !utf8.ValidString(parts[2]) {
		return fileIdentity{}, fmt.Errorf("git: file resource identity must be UTF-8")
	}
	canonical, err := json.Marshal(parts)
	if err != nil || !bytes.Equal(canonical, raw) {
		return fileIdentity{}, fmt.Errorf("git: non-canonical file resource identity")
	}
	root, err := canonicalRoot(parts[0])
	if err != nil {
		return fileIdentity{}, err
	}
	if parts[0] != root {
		return fileIdentity{}, fmt.Errorf("git: repository root in resource ID is not canonical")
	}
	if root != p.repo.root {
		return fileIdentity{}, fmt.Errorf("git: resource repository root does not match provider repository")
	}
	ref := RefName(parts[1])
	if err := validateBranchRef(ref); err != nil {
		return fileIdentity{}, err
	}
	if strings.Contains(parts[2], "\\") || !utf8.ValidString(parts[2]) {
		return fileIdentity{}, fmt.Errorf("git: invalid target path")
	}
	if err := validateTreePath(parts[2]); err != nil {
		return fileIdentity{}, err
	}
	return fileIdentity{Root: root, Ref: ref, Path: parts[2]}, nil
}

func validateBranchRef(ref RefName) error {
	if !strings.HasPrefix(string(ref), "refs/heads/") || len(string(ref)) == len("refs/heads/") {
		return fmt.Errorf("git: resource ref must be a full branch ref")
	}
	return validateRef(ref)
}

func canonicalRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	resolved = filepath.Clean(resolved)
	if !utf8.ValidString(resolved) {
		return "", fmt.Errorf("git: repository root must be UTF-8")
	}
	return resolved, nil
}

func parseTipFingerprint(repo *Repository, fingerprint string) (ObjectID, error) {
	if !strings.HasPrefix(fingerprint, tipFingerprintPrefix) {
		return "", fmt.Errorf("git: invalid branch-tip fingerprint")
	}
	id := ObjectID(strings.TrimPrefix(fingerprint, tipFingerprintPrefix))
	if err := repo.validateObject(id); err != nil {
		return "", err
	}
	return id, nil
}

type filePayload struct {
	Mode    *string `json:"mode"`
	Content *string `json:"content"`
}

// EncodeFilePayload builds the canonical payload for replacing an existing
// regular file. It keeps callers from duplicating the provider's wire format.
func EncodeFilePayload(mode string, content []byte) ([]byte, error) {
	if mode != "100644" && mode != "100755" {
		return nil, fmt.Errorf("git: unsupported replacement file mode %q", mode)
	}
	encodedContent := base64.StdEncoding.EncodeToString(content)
	payload, err := json.Marshal(filePayload{Mode: &mode, Content: &encodedContent})
	if err != nil {
		return nil, fmt.Errorf("git: encode file payload: %w", err)
	}
	return payload, nil
}

func decodeFilePayload(payload []byte) (string, []byte, error) {
	if !utf8.Valid(payload) {
		return "", nil, fmt.Errorf("git: file payload must be UTF-8 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var encoded filePayload
	if err := decoder.Decode(&encoded); err != nil {
		return "", nil, fmt.Errorf("git: invalid file payload: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return "", nil, fmt.Errorf("git: invalid file payload: %w", err)
	}
	if encoded.Mode == nil || encoded.Content == nil {
		return "", nil, fmt.Errorf("git: file payload requires mode and content")
	}
	if *encoded.Mode != "100644" && *encoded.Mode != "100755" {
		return "", nil, fmt.Errorf("git: unsupported replacement file mode %q", *encoded.Mode)
	}
	content, err := base64.StdEncoding.Strict().DecodeString(*encoded.Content)
	if err != nil {
		return "", nil, fmt.Errorf("git: invalid base64 file content: %w", err)
	}
	if base64.StdEncoding.EncodeToString(content) != *encoded.Content {
		return "", nil, fmt.Errorf("git: file content must use canonical padded base64")
	}
	return *encoded.Mode, content, nil
}

type executionEvidence struct {
	Schema       string `json:"schema"`
	ExecutionID  string `json:"execution_id"`
	BeforeTip    string `json:"before_tip"`
	ResultCommit string `json:"result_commit"`
}

func decodeExecutionEvidence(evidence []byte) (executionEvidence, error) {
	if !utf8.Valid(evidence) {
		return executionEvidence{}, fmt.Errorf("git: execution evidence must be UTF-8 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(evidence))
	decoder.DisallowUnknownFields()
	var result executionEvidence
	if err := decoder.Decode(&result); err != nil {
		return executionEvidence{}, fmt.Errorf("git: invalid execution evidence: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return executionEvidence{}, fmt.Errorf("git: invalid execution evidence: %w", err)
	}
	if result.Schema != "git-file-replace-v1" || result.ExecutionID == "" || result.BeforeTip == "" || result.ResultCommit == "" {
		return executionEvidence{}, fmt.Errorf("git: incomplete or unsupported execution evidence")
	}
	return result, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}

func (p *FileProvider) readPath(ctx context.Context, tree ObjectID, components []string) (TreeEntry, error) {
	entries, err := p.repo.ReadTree(ctx, tree)
	if err != nil {
		return TreeEntry{}, err
	}
	for _, entry := range entries {
		if entry.Path != components[0] {
			continue
		}
		if len(components) == 1 {
			return entry, nil
		}
		if entry.Mode != "040000" && entry.Mode != "40000" {
			return TreeEntry{}, fmt.Errorf("git: target path traverses a non-directory")
		}
		return p.readPath(ctx, entry.Object, components[1:])
	}
	return TreeEntry{}, fmt.Errorf("git: target path does not exist")
}

func (p *FileProvider) replacePath(ctx context.Context, tree ObjectID, components []string, blob ObjectID, mode string) (ObjectID, error) {
	entries, err := p.repo.ReadTree(ctx, tree)
	if err != nil {
		return "", err
	}
	found := false
	for i := range entries {
		if entries[i].Path != components[0] {
			continue
		}
		found = true
		if len(components) == 1 {
			if entries[i].Mode != "100644" && entries[i].Mode != "100755" {
				return "", fmt.Errorf("git: target must be an existing regular file")
			}
			entries[i].Mode = mode
			entries[i].Object = blob
			break
		}
		if entries[i].Mode != "040000" && entries[i].Mode != "40000" {
			return "", fmt.Errorf("git: target path traverses a non-directory")
		}
		entries[i].Object, err = p.replacePath(ctx, entries[i].Object, components[1:], blob, mode)
		if err != nil {
			return "", err
		}
		break
	}
	if !found {
		return "", fmt.Errorf("git: target path does not exist")
	}
	if p.beforeWriteTree != nil {
		if err := p.beforeWriteTree(); err != nil {
			return "", err
		}
	}
	return p.repo.WriteTree(ctx, entries)
}

func flattenTree(ctx context.Context, repo *Repository, tree ObjectID, prefix string) (map[string]TreeEntry, error) {
	entries, err := repo.ReadTree(ctx, tree)
	if err != nil {
		return nil, err
	}
	flat := make(map[string]TreeEntry)
	for _, entry := range entries {
		fullPath := entry.Path
		if prefix != "" {
			fullPath = prefix + "/" + fullPath
		}
		if entry.Mode == "040000" || entry.Mode == "40000" {
			nested, err := flattenTree(ctx, repo, entry.Object, fullPath)
			if err != nil {
				return nil, err
			}
			for nestedPath, nestedEntry := range nested {
				flat[nestedPath] = nestedEntry
			}
			continue
		}
		entry.Path = fullPath
		flat[fullPath] = entry
	}
	return flat, nil
}

func compareTreeLeaves(before, after map[string]TreeEntry, target string) error {
	for path, old := range before {
		current, ok := after[path]
		if !ok {
			return fmt.Errorf("git: unexpected removal of path %q", path)
		}
		if path != target && current != old {
			return fmt.Errorf("git: unexpected change to path %q", path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			return fmt.Errorf("git: unexpected addition of path %q", path)
		}
	}
	return nil
}
