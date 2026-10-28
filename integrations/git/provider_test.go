package git

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jaredt87/ackOS/control"
	"github.com/jaredt87/ackOS/kernel"
)

const testBranch RefName = "refs/heads/main"

func TestFileProviderControlReplacesAuthorizedFile(t *testing.T) {
	f := newFileFixture(t)
	provider, err := NewFileProvider(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	newContent := []byte("new contents\x00\xff")
	result, err := runFileControl(t, f, provider, "100755", newContent)
	if err != nil {
		t.Fatal(err)
	}
	if result.Execution.ExecutionID == "" || result.Execution.ExecutionID != result.Authority.ExecutionID {
		t.Fatalf("execution ID = %q, authority ID = %q", result.Execution.ExecutionID, result.Authority.ExecutionID)
	}
	current, err := f.repo.ReadRef(context.Background(), testBranch)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verification.ConcurrencyToken != "git-tip-v1:"+string(current) {
		t.Fatalf("verification concurrency token = %q, want result branch tip %s", result.Verification.ConcurrencyToken, current)
	}
	commit, err := f.repo.ReadCommit(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	wantTrailer := "Ack-Execution-Id: " + result.Execution.ExecutionID + "\n"
	if !strings.HasSuffix(commit.Message, wantTrailer) {
		t.Fatalf("commit message %q does not bind execution trailer %q", commit.Message, wantTrailer)
	}
	if len(commit.Parents) != 1 || commit.Parents[0] != f.tip {
		t.Fatalf("result parents = %v, want [%s]", commit.Parents, f.tip)
	}
	entries, err := flattenTree(context.Background(), f.repo, commit.Tree, "")
	if err != nil {
		t.Fatal(err)
	}
	entry := entries["dir/file.txt"]
	if entry.Mode != "100755" {
		t.Fatalf("target mode = %s, want 100755", entry.Mode)
	}
	got, err := f.repo.ReadBlob(context.Background(), entry.Object)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newContent) {
		t.Fatalf("target bytes = %v, want %v", got, newContent)
	}
}

func TestFileProviderObservationSeparatesFileStateFromBranchTip(t *testing.T) {
	f := newFileFixture(t)
	provider, err := NewFileProvider(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := provider.Observe(context.Background(), control.ObserveRequest{
		Target: control.ResourceRef{ID: f.id},
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed.Resource.Fingerprint != "git-file-v1:sha256:d5b4c1132e99afa63a53ff4de7ff3507d37f55f52ac3b846bc6c4ad08eeae5a9" {
		t.Fatalf("resource fingerprint = %q, want exact initial file-state fingerprint", observed.Resource.Fingerprint)
	}
	if observed.ConcurrencyToken != "git-tip-v1:"+string(f.tip) {
		t.Fatalf("concurrency token = %q, want observed branch tip %s", observed.ConcurrencyToken, f.tip)
	}
}

func TestFileProviderTwoSequentialReplacementsUseOneHostAndRuntime(t *testing.T) {
	f := newFileFixture(t)
	provider, err := NewFileProvider(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := provider.Observe(context.Background(), control.ObserveRequest{Target: control.ResourceRef{ID: f.id}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := kernel.NewRuntime(initial.Resource.Fingerprint, kernel.AllowPolicy{})
	host, err := control.NewHost(runtime, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Register("git", provider); err != nil {
		t.Fatal(err)
	}
	firstContent := []byte("first replacement\x00")
	firstFingerprint, err := FileStateFingerprint("100755", firstContent)
	if err != nil {
		t.Fatal(err)
	}
	first, err := host.Control(context.Background(), "git", control.ControlRequest{
		Target:  control.ResourceRef{ID: f.id, Fingerprint: "ignored"},
		Desired: control.ResourceRef{ID: f.id, Fingerprint: firstFingerprint},
		Payload: encodeFilePayload("100755", firstContent),
	})
	if err != nil {
		t.Fatal(err)
	}
	firstTip := ObjectID(first.Verification.ConcurrencyToken[len(tipFingerprintPrefix):])
	secondContent := []byte("second replacement\xff")
	secondFingerprint, err := FileStateFingerprint("100644", secondContent)
	if err != nil {
		t.Fatal(err)
	}
	second, err := host.Control(context.Background(), "git", control.ControlRequest{
		Target:  control.ResourceRef{ID: f.id, Fingerprint: "ignored"},
		Desired: control.ResourceRef{ID: f.id, Fingerprint: secondFingerprint},
		Payload: encodeFilePayload("100644", secondContent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Observation.Resource.Fingerprint != firstFingerprint {
		t.Fatalf("second observation resource state = %q, want first desired state %q", second.Observation.Resource.Fingerprint, firstFingerprint)
	}
	if second.Observation.ConcurrencyToken != first.Verification.ConcurrencyToken {
		t.Fatalf("second before token = %q, want first verified token %q", second.Observation.ConcurrencyToken, first.Verification.ConcurrencyToken)
	}
	if runtime.Root() != secondFingerprint {
		t.Fatalf("Runtime root = %q, want desired resource state %q", runtime.Root(), secondFingerprint)
	}
	current, err := f.repo.ReadRef(context.Background(), testBranch)
	if err != nil {
		t.Fatal(err)
	}
	if second.Verification.ConcurrencyToken != tipFingerprintPrefix+string(current) {
		t.Fatalf("second verified token = %q, want current tip %s", second.Verification.ConcurrencyToken, current)
	}
	commit, err := f.repo.ReadCommit(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	if len(commit.Parents) != 1 || commit.Parents[0] != firstTip {
		t.Fatalf("second commit parents = %v, want [%s]", commit.Parents, firstTip)
	}
	entries, err := flattenTree(context.Background(), f.repo, commit.Tree, "")
	if err != nil {
		t.Fatal(err)
	}
	entry := entries["dir/file.txt"]
	got, err := f.repo.ReadBlob(context.Background(), entry.Object)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Mode != "100644" || string(got) != string(secondContent) {
		t.Fatalf("final target = mode %s, bytes %v; want mode 100644 and bytes %v", entry.Mode, got, secondContent)
	}
	initialCommit, err := f.repo.ReadCommit(context.Background(), f.tip)
	if err != nil {
		t.Fatal(err)
	}
	initialEntries, err := flattenTree(context.Background(), f.repo, initialCommit.Tree, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := compareTreeLeaves(initialEntries, entries, "dir/file.txt"); err != nil {
		t.Fatalf("unrelated tree contents changed across sequential replacements: %v", err)
	}
}

func TestFileProviderStaleHostObservationRecoversWithFreshTip(t *testing.T) {
	f := newFileFixture(t)
	base, err := NewFileProvider(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := base.Observe(context.Background(), control.ObserveRequest{Target: control.ResourceRef{ID: f.id}})
	if err != nil {
		t.Fatal(err)
	}
	var externalTip ObjectID
	provider := providerWrapper{Provider: base, afterObserve: func() {
		if externalTip == "" {
			externalTip = commitOnTip(t, f.repo, testBranch, f.tip, "external update after Host observation")
		}
	}}
	runtime := kernel.NewRuntime(initial.Resource.Fingerprint, kernel.AllowPolicy{})
	host, err := control.NewHost(runtime, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Register("git", provider); err != nil {
		t.Fatal(err)
	}
	content := []byte("replacement after recovery")
	fingerprint, err := FileStateFingerprint("100644", content)
	if err != nil {
		t.Fatal(err)
	}
	request := control.ControlRequest{
		Target:  control.ResourceRef{ID: f.id, Fingerprint: "ignored"},
		Desired: control.ResourceRef{ID: f.id, Fingerprint: fingerprint},
		Payload: encodeFilePayload("100644", content),
	}
	if _, err := host.Control(context.Background(), "git", request); !errors.Is(err, control.ErrStaleObservation) {
		t.Fatalf("first Control() error = %v, want stale observation", err)
	}
	if got := runtime.Phase(); got != kernel.PhaseRecovery {
		t.Fatalf("phase after stale execution = %s, want %s", got, kernel.PhaseRecovery)
	}
	result, err := host.Control(context.Background(), "git", request)
	if err != nil {
		t.Fatalf("fresh Control() after recovery failed: %v", err)
	}
	if result.Observation.ConcurrencyToken != tipFingerprintPrefix+string(externalTip) {
		t.Fatalf("recovery observation token = %q, want fresh tip %s", result.Observation.ConcurrencyToken, externalTip)
	}
	commitID := ObjectID(resultEvidenceCommit(t, result.Execution))
	commit, err := f.repo.ReadCommit(context.Background(), commitID)
	if err != nil {
		t.Fatal(err)
	}
	if len(commit.Parents) != 1 || commit.Parents[0] != externalTip {
		t.Fatalf("recovered commit parents = %v, want [%s]", commit.Parents, externalTip)
	}
}

func TestFileProviderVerificationFailureRecoveryDoesNotAdoptCommittedState(t *testing.T) {
	f := newFileFixture(t)
	base, err := NewFileProvider(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := base.Observe(context.Background(), control.ObserveRequest{Target: control.ResourceRef{ID: f.id}})
	if err != nil {
		t.Fatal(err)
	}
	var laterTip ObjectID
	provider := providerWrapper{Provider: base, afterExecute: func(result control.Execution) {
		if laterTip == "" {
			laterTip = commitOnTip(t, f.repo, testBranch, ObjectID(resultEvidenceCommit(t, result)), "later writer")
		}
	}}
	runtime := kernel.NewRuntime(initial.Resource.Fingerprint, kernel.AllowPolicy{})
	host, err := control.NewHost(runtime, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Register("git", provider); err != nil {
		t.Fatal(err)
	}
	firstContent := []byte("committed but not verified as current")
	firstFingerprint, err := FileStateFingerprint("100644", firstContent)
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Control(context.Background(), "git", control.ControlRequest{
		Target:  control.ResourceRef{ID: f.id, Fingerprint: "ignored"},
		Desired: control.ResourceRef{ID: f.id, Fingerprint: firstFingerprint},
		Payload: encodeFilePayload("100644", firstContent),
	})
	if !errors.Is(err, ErrBranchAdvancedAfterExecution) {
		t.Fatalf("first Control() error = %v, want branch advanced after execution", err)
	}
	if got := runtime.Phase(); got != kernel.PhaseRecovery {
		t.Fatalf("phase after failed verification = %s, want %s", got, kernel.PhaseRecovery)
	}
	secondFingerprint, err := FileStateFingerprint("100644", []byte("second change"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = host.Control(context.Background(), "git", control.ControlRequest{
		Target:  control.ResourceRef{ID: f.id, Fingerprint: "ignored"},
		Desired: control.ResourceRef{ID: f.id, Fingerprint: secondFingerprint},
		Payload: encodeFilePayload("100644", []byte("second change")),
	})
	if !errors.Is(err, kernel.ErrCASConflict) {
		t.Fatalf("recovered Control() error = %v, want local CAS conflict", err)
	}
	current, err := f.repo.ReadRef(context.Background(), testBranch)
	if err != nil {
		t.Fatal(err)
	}
	if current != laterTip {
		t.Fatalf("current tip = %s, want later writer tip %s", current, laterTip)
	}
}

func TestFileProviderRejectsAuthorizedStateThatDoesNotMatchTip(t *testing.T) {
	f := newFileFixture(t)
	provider, err := NewFileProvider(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	desired := []byte("replacement")
	fingerprint, err := FileStateFingerprint("100644", desired)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Execute(context.Background(), control.ExecuteRequest{
		ExecutionID: "exec-invalid-before-state",
		Target:      control.ResourceRef{ID: f.id},
		Desired:     control.ResourceRef{ID: f.id, Fingerprint: fingerprint},
		Before: control.Observation{
			Resource:         control.ResourceRef{ID: f.id, Fingerprint: "git-file-v1:forged"},
			ConcurrencyToken: "git-tip-v1:" + string(f.tip),
		},
		Payload: encodeFilePayload("100644", desired),
	})
	if err == nil || !strings.Contains(err.Error(), "authorized file state") {
		t.Fatalf("Execute() error = %v, want authorized state mismatch", err)
	}
}

func TestFileResourceIDRequiresBranchRef(t *testing.T) {
	f := newFileFixture(t)
	if _, err := f.repo.FileResourceID(RefName("refs/tags/release"), "dir/file.txt"); err == nil {
		t.Fatal("accepted a tag ref as a branch resource")
	}
	if _, err := f.repo.FileResourceID(RefName("refs/heads/\xff"), "dir/file.txt"); err == nil {
		t.Fatal("accepted a non-UTF-8 branch ref")
	}
}

func TestDecodeFilePayloadRejectsLineBreaksInBase64(t *testing.T) {
	payload := []byte(`{"mode":"100644","content":"YQ==\r\n"}`)
	if _, _, err := decodeFilePayload(payload); err == nil {
		t.Fatal("accepted line breaks in Base64 content")
	}
}

func TestFileProviderRejectsRegularModePointingToNonBlob(t *testing.T) {
	f := newFileFixture(t)
	ctx := context.Background()
	commit, err := f.repo.ReadCommit(ctx, f.tip)
	if err != nil {
		t.Fatal(err)
	}
	rootEntries, err := f.repo.ReadTree(ctx, commit.Tree)
	if err != nil {
		t.Fatal(err)
	}
	var dirEntry int = -1
	for i := range rootEntries {
		if rootEntries[i].Path == "dir" {
			dirEntry = i
			break
		}
	}
	if dirEntry < 0 {
		t.Fatal("fixture is missing dir tree")
	}
	badObject, err := hex.DecodeString(string(commit.Tree))
	if err != nil {
		t.Fatal(err)
	}
	malformedTreeBytes := append([]byte("100644 file.txt\x00"), badObject...)
	malformedTreeOutput, err := f.repo.exec(ctx, []string{"hash-object", "-t", "tree", "-w", "--stdin"}, malformedTreeBytes)
	if err != nil {
		t.Fatal(err)
	}
	malformedDirTree := ObjectID(strings.TrimSpace(string(malformedTreeOutput)))
	rootEntries[dirEntry].Object = malformedDirTree
	malformedRoot, err := f.repo.WriteTree(ctx, rootEntries)
	if err != nil {
		t.Fatal(err)
	}
	malformedCommit, err := f.repo.CommitTree(ctx, malformedRoot, []ObjectID{f.tip}, "malformed file entry\n", testIdentity(100), testIdentity(100))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.UpdateRef(ctx, testBranch, malformedCommit, f.tip); err != nil {
		t.Fatal(err)
	}
	f.tip = malformedCommit
	provider, err := NewFileProvider(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Observe(ctx, control.ObserveRequest{Target: control.ResourceRef{ID: f.id}})
	if err == nil || !strings.Contains(err.Error(), "not a blob") {
		t.Fatalf("Observe() error = %v, want non-blob target rejection", err)
	}
	current, err := f.repo.ReadRef(ctx, testBranch)
	if err != nil {
		t.Fatal(err)
	}
	if current != malformedCommit {
		t.Fatalf("ref = %s, want unchanged malformed tip %s", current, malformedCommit)
	}
}

func TestFileProviderRejectsStaleObservationBeforeWrite(t *testing.T) {
	f := newFileFixture(t)
	base, err := NewFileProvider(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	var externalTip ObjectID
	observeCalls := 0
	provider := providerWrapper{Provider: base, afterObserve: func() {
		observeCalls++
		if observeCalls == 2 {
			externalTip = commitOnTip(t, f.repo, testBranch, f.tip, "external advance")
		}
	}}
	_, err = runFileControl(t, f, provider, "100644", []byte("replacement"))
	if externalTip == "" {
		t.Fatal("external writer did not run after observation")
	}
	if !errors.Is(err, control.ErrStaleObservation) {
		t.Fatalf("error = %v, want stale observation", err)
	}
	current, readErr := f.repo.ReadRef(context.Background(), testBranch)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if current != externalTip {
		t.Fatalf("current ref = %s, want external writer tip %s", current, externalTip)
	}
}

func TestFileProviderVerifyRechecksTipAfterObjectReads(t *testing.T) {
	f := newFileFixture(t)
	provider, err := NewFileProvider(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	var concurrentTip ObjectID
	provider.beforeFinalVerifyRef = func() {
		current, readErr := f.repo.ReadRef(context.Background(), testBranch)
		if readErr != nil {
			t.Fatal(readErr)
		}
		concurrentTip = commitOnTip(t, f.repo, testBranch, current, "verification race winner")
	}
	result, err := runFileControl(t, f, provider, "100644", []byte("replacement"))
	if !errors.Is(err, ErrBranchAdvancedAfterExecution) {
		t.Fatalf("control error = %v, want branch movement detected by final verification read", err)
	}
	if concurrentTip == "" {
		t.Fatal("verification race did not advance the branch")
	}
	if result.Execution.ExecutionID == "" {
		t.Fatal("ControlResult did not retain committed execution")
	}
	current, err := f.repo.ReadRef(context.Background(), testBranch)
	if err != nil {
		t.Fatal(err)
	}
	if current != concurrentTip {
		t.Fatalf("current ref = %s, want concurrent tip %s", current, concurrentTip)
	}
}

func TestFileProviderRecoversCASCommitWithCanceledContext(t *testing.T) {
	f := newFileFixture(t)
	provider, err := NewFileProvider(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider.afterUpdateRef = func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	}
	before, err := provider.Observe(context.Background(), control.ObserveRequest{Target: control.ResourceRef{ID: f.id}})
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("replacement")
	fingerprint, err := FileStateFingerprint("100644", content)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := provider.Execute(ctx, control.ExecuteRequest{
		ExecutionID: "canceled-after-cas",
		Target:      control.ResourceRef{ID: f.id},
		Desired:     control.ResourceRef{ID: f.id, Fingerprint: fingerprint},
		Before:      before,
		Payload:     encodeFilePayload("100644", content),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v, want recovery of committed CAS", err)
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("test did not cancel the execution context after CAS")
	}
	if execution.ExecutionID != "canceled-after-cas" || len(execution.Evidence) == 0 {
		t.Fatalf("execution = %+v, want evidence for the committed operation", execution)
	}
	current, err := f.repo.ReadRef(context.Background(), testBranch)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != resultEvidenceCommit(t, execution) {
		t.Fatalf("ref = %s, want operation commit %s", current, resultEvidenceCommit(t, execution))
	}
}

func TestFileProviderHostReceivesCASRecoveryAfterDeadline(t *testing.T) {
	f := newFileFixture(t)
	provider, err := NewFileProvider(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	provider.afterUpdateRef = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	content := []byte("replacement after deadline")
	fingerprint, err := FileStateFingerprint("100644", content)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := provider.Observe(context.Background(), control.ObserveRequest{Target: control.ResourceRef{ID: f.id}})
	if err != nil {
		t.Fatal(err)
	}
	host, err := control.NewHost(kernel.NewRuntime(initial.Resource.Fingerprint, kernel.AllowPolicy{}), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Register("git", provider); err != nil {
		t.Fatal(err)
	}
	result, err := host.Control(context.Background(), "git", control.ControlRequest{
		Target:  control.ResourceRef{ID: f.id, Fingerprint: "caller-supplied-value-ignored"},
		Desired: control.ResourceRef{ID: f.id, Fingerprint: fingerprint},
		Payload: encodeFilePayload("100644", content),
	})
	if err != nil {
		t.Fatalf("Control() error = %v, want recovered CAS execution and verification", err)
	}
	if result.Execution.ExecutionID == "" || result.Verification.Resource.Fingerprint != fingerprint {
		t.Fatalf("ControlResult = %+v, want execution evidence and verified desired state", result)
	}
	current, err := f.repo.ReadRef(context.Background(), testBranch)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != resultEvidenceCommit(t, result.Execution) {
		t.Fatalf("ref = %s, want operation commit %s", current, resultEvidenceCommit(t, result.Execution))
	}
}

func TestFileProviderChangesOnlyRequestedLeaf(t *testing.T) {
	f := newFileFixture(t)
	provider, err := NewFileProvider(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runFileControl(t, f, provider, "100644", []byte("changed"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.repo.ReadCommit(context.Background(), f.tip)
	if err != nil {
		t.Fatal(err)
	}
	afterID, err := f.repo.ReadRef(context.Background(), testBranch)
	if err != nil {
		t.Fatal(err)
	}
	after, err := f.repo.ReadCommit(context.Background(), afterID)
	if err != nil {
		t.Fatal(err)
	}
	oldEntries, err := flattenTree(context.Background(), f.repo, before.Tree, "")
	if err != nil {
		t.Fatal(err)
	}
	newEntries, err := flattenTree(context.Background(), f.repo, after.Tree, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(oldEntries) != len(newEntries) {
		t.Fatalf("leaf count changed: before=%d after=%d", len(oldEntries), len(newEntries))
	}
	for path, old := range oldEntries {
		got, ok := newEntries[path]
		if !ok {
			t.Fatalf("path %q was removed", path)
		}
		if path != "dir/file.txt" && got != old {
			t.Fatalf("unrequested path %q changed: before=%+v after=%+v", path, old, got)
		}
	}
	if newEntries["dir/file.txt"] == oldEntries["dir/file.txt"] {
		t.Fatal("requested path did not change")
	}
}

func TestFileProviderPreservesLineBreakSiblingNames(t *testing.T) {
	f := newFileFixture(t)
	ctx := context.Background()
	commit, err := f.repo.ReadCommit(ctx, f.tip)
	if err != nil {
		t.Fatal(err)
	}
	rootEntries, err := f.repo.ReadTree(ctx, commit.Tree)
	if err != nil {
		t.Fatal(err)
	}
	dirIndex := -1
	for i := range rootEntries {
		if rootEntries[i].Path == "dir" {
			dirIndex = i
			break
		}
	}
	if dirIndex < 0 {
		t.Fatal("fixture is missing dir tree")
	}
	dirEntries, err := f.repo.ReadTree(ctx, rootEntries[dirIndex].Object)
	if err != nil {
		t.Fatal(err)
	}
	lineBlob, err := f.repo.WriteBlob(ctx, []byte("preserve line name"))
	if err != nil {
		t.Fatal(err)
	}
	carriageBlob, err := f.repo.WriteBlob(ctx, []byte("preserve carriage name"))
	if err != nil {
		t.Fatal(err)
	}
	dirEntries = append(dirEntries,
		TreeEntry{Mode: "100644", Path: "line\nbreak.txt", Object: lineBlob},
		TreeEntry{Mode: "100644", Path: "carriage\rreturn.txt", Object: carriageBlob},
	)
	var rawTree strings.Builder
	for _, entry := range dirEntries {
		fmt.Fprintf(&rawTree, "%s blob %s\t%s\x00", entry.Mode, entry.Object, entry.Path)
	}
	dirTreeOutput, err := f.repo.exec(ctx, []string{"mktree", "--missing", "-z"}, []byte(rawTree.String()))
	if err != nil {
		t.Fatal(err)
	}
	rootEntries[dirIndex].Object = ObjectID(strings.TrimSpace(string(dirTreeOutput)))
	rootTree, err := f.repo.WriteTree(ctx, rootEntries)
	if err != nil {
		t.Fatal(err)
	}
	newTip, err := f.repo.CommitTree(ctx, rootTree, []ObjectID{f.tip}, "add legal sibling names\n", testIdentity(50), testIdentity(50))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.UpdateRef(ctx, testBranch, newTip, f.tip); err != nil {
		t.Fatal(err)
	}
	f.tip = newTip
	provider, err := NewFileProvider(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runFileControl(t, f, provider, "100644", []byte("replacement")); err != nil {
		t.Fatalf("Control() failed replacing target beside legal sibling names: %v", err)
	}
	current, err := f.repo.ReadRef(ctx, testBranch)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.repo.ReadCommit(ctx, current)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := flattenTree(ctx, f.repo, result.Tree, "")
	if err != nil {
		t.Fatal(err)
	}
	for path, wantObject := range map[string]ObjectID{
		"dir/line\nbreak.txt":      lineBlob,
		"dir/carriage\rreturn.txt": carriageBlob,
	} {
		entry, ok := entries[path]
		if !ok || entry.Mode != "100644" || entry.Object != wantObject {
			t.Fatalf("sibling %q = %+v, want regular file object %s", path, entry, wantObject)
		}
	}
}

func TestFileProviderCASUsesAuthorizedTipAndPreservesUnderlyingError(t *testing.T) {
	f := newFileFixture(t)
	provider, err := NewFileProvider(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	provider.beforeUpdateRef = func() error {
		called++
		commitOnTip(t, f.repo, testBranch, f.tip, "concurrent update")
		return nil
	}
	_, err = runFileControl(t, f, provider, "100644", []byte("replacement"))
	if called != 1 {
		t.Fatalf("pre-CAS seam called %d times, want 1", called)
	}
	if !errors.Is(err, control.ErrStaleObservation) {
		t.Fatalf("error = %v, want stale observation", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error %v does not preserve underlying Git command error", err)
	}
	current, readErr := f.repo.ReadRef(context.Background(), testBranch)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if current == f.tip {
		t.Fatal("concurrent ref update was overwritten")
	}
}

func TestFileProviderFailureOutcomesFollowCASBoundary(t *testing.T) {
	t.Run("object construction failure", func(t *testing.T) {
		f := newFileFixture(t)
		provider, err := NewFileProvider(f.repo)
		if err != nil {
			t.Fatal(err)
		}
		provider.beforeWriteTree = func() error { return errors.New("injected tree construction failure") }
		before := localStateSnapshot(t, f.root)
		_, err = runFileControl(t, f, provider, "100644", []byte("replacement"))
		if err == nil || !strings.Contains(err.Error(), "injected tree construction failure") {
			t.Fatalf("error = %v, want injected construction failure", err)
		}
		current, readErr := f.repo.ReadRef(context.Background(), testBranch)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if current != f.tip {
			t.Fatalf("ref = %s, want unchanged %s", current, f.tip)
		}
		if after := localStateSnapshot(t, f.root); after != before {
			t.Fatal("index or working tree changed before CAS")
		}
	})

	t.Run("stale compare-and-swap preserves concurrent writer", func(t *testing.T) {
		f := newFileFixture(t)
		provider, err := NewFileProvider(f.repo)
		if err != nil {
			t.Fatal(err)
		}
		var concurrentTip ObjectID
		provider.beforeUpdateRef = func() error {
			concurrentTip = commitOnTip(t, f.repo, testBranch, f.tip, "CAS race winner")
			return nil
		}
		beforeLocal := localStateSnapshot(t, f.root)
		_, err = runFileControl(t, f, provider, "100644", []byte("replacement"))
		if !errors.Is(err, control.ErrStaleObservation) {
			t.Fatalf("error = %v, want stale observation", err)
		}
		current, readErr := f.repo.ReadRef(context.Background(), testBranch)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if current != concurrentTip {
			t.Fatalf("current ref = %s, want concurrent tip %s", current, concurrentTip)
		}
		if after := localStateSnapshot(t, f.root); after != beforeLocal {
			t.Fatal("index or working tree changed after stale CAS")
		}
	})

	t.Run("payload is bound to authorized desired state", func(t *testing.T) {
		f := newFileFixture(t)
		provider, err := NewFileProvider(f.repo)
		if err != nil {
			t.Fatal(err)
		}
		_, bad := runFileControlWithPayload(t, f, provider, "100644", []byte("different"), encodeFilePayload("100644", []byte("authorized")))
		if bad == nil || !strings.Contains(bad.Error(), "desired fingerprint") {
			t.Fatalf("error = %v, want desired fingerprint mismatch", bad)
		}
		current, readErr := f.repo.ReadRef(context.Background(), testBranch)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if current != f.tip {
			t.Fatalf("ref = %s, want unchanged %s", current, f.tip)
		}
	})

	t.Run("verification rejects later branch movement without rollback", func(t *testing.T) {
		f := newFileFixture(t)
		base, err := NewFileProvider(f.repo)
		if err != nil {
			t.Fatal(err)
		}
		provider := providerWrapper{Provider: base, afterExecute: func(result control.Execution) {
			commitOnTip(t, f.repo, testBranch, ObjectID(resultEvidenceCommit(t, result)), "later writer")
		}}
		result, controlErr := runFileControl(t, f, provider, "100644", []byte("replacement"))
		if !errors.Is(controlErr, ErrBranchAdvancedAfterExecution) {
			t.Fatalf("control error = %v, want post-commit verification failure", controlErr)
		}
		if result.Execution.ExecutionID == "" {
			t.Fatal("ControlResult did not retain successful execution")
		}
		current, readErr := f.repo.ReadRef(context.Background(), testBranch)
		if readErr != nil {
			t.Fatal(readErr)
		}
		commit, readErr := f.repo.ReadCommit(context.Background(), current)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(commit.Parents) != 1 || commit.Parents[0] == f.tip {
			t.Fatalf("current ref does not retain later writer commit: %+v", commit)
		}
	})
}

type fileFixture struct {
	root string
	repo *Repository
	tip  ObjectID
	id   string
}

func newFileFixture(t *testing.T) fileFixture {
	t.Helper()
	root := testRepo(t)
	repo, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	targetBlob, err := repo.WriteBlob(ctx, []byte("old contents"))
	if err != nil {
		t.Fatal(err)
	}
	otherBlob, err := repo.WriteBlob(ctx, []byte("untouched sibling"))
	if err != nil {
		t.Fatal(err)
	}
	readmeBlob, err := repo.WriteBlob(ctx, []byte("readme"))
	if err != nil {
		t.Fatal(err)
	}
	dirTree, err := repo.WriteTree(ctx, []TreeEntry{
		{Mode: "100644", Path: "file.txt", Object: targetBlob},
		{Mode: "100644", Path: "sibling.txt", Object: otherBlob},
	})
	if err != nil {
		t.Fatal(err)
	}
	rootTree, err := repo.WriteTree(ctx, []TreeEntry{
		{Mode: "100644", Path: "README", Object: readmeBlob},
		{Mode: "040000", Path: "dir", Object: dirTree},
	})
	if err != nil {
		t.Fatal(err)
	}
	tip, err := repo.CommitTree(ctx, rootTree, nil, "initial\n", testIdentity(1), testIdentity(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateRef(ctx, testBranch, tip, ObjectID(strings.Repeat("0", repo.objectHash))); err != nil {
		t.Fatal(err)
	}
	id, err := repo.FileResourceID(testBranch, "dir/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	return fileFixture{root: root, repo: repo, tip: tip, id: id}
}

func runFileControl(t *testing.T, f fileFixture, provider control.Provider, mode string, content []byte) (control.ControlResult, error) {
	t.Helper()
	return runFileControlWithPayload(t, f, provider, mode, content, encodeFilePayload(mode, content))
}

func runFileControlWithPayload(t *testing.T, f fileFixture, provider control.Provider, desiredMode string, desiredContent, payload []byte) (control.ControlResult, error) {
	t.Helper()
	initial, err := provider.Observe(context.Background(), control.ObserveRequest{Target: control.ResourceRef{ID: f.id}})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := FileStateFingerprint(desiredMode, desiredContent)
	if err != nil {
		t.Fatal(err)
	}
	host, err := control.NewHost(kernel.NewRuntime(initial.Resource.Fingerprint, kernel.AllowPolicy{}), time.Second*5)
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Register("git", provider); err != nil {
		t.Fatal(err)
	}
	return host.Control(context.Background(), "git", control.ControlRequest{
		Target:  control.ResourceRef{ID: f.id, Fingerprint: "caller-supplied-value-ignored"},
		Desired: control.ResourceRef{ID: f.id, Fingerprint: fingerprint},
		Payload: payload,
	})
}

func encodeFilePayload(mode string, content []byte) []byte {
	b, _ := json.Marshal(struct {
		Mode    string `json:"mode"`
		Content string `json:"content"`
	}{mode, base64.StdEncoding.EncodeToString(content)})
	return b
}

func commitOnTip(t *testing.T, repo *Repository, ref RefName, parent ObjectID, message string) ObjectID {
	t.Helper()
	commit, err := repo.ReadCommit(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	next, err := repo.CommitTree(context.Background(), commit.Tree, []ObjectID{parent}, message+"\n", testIdentity(int(time.Now().UnixNano()%1000000+1)), testIdentity(int(time.Now().UnixNano()%1000000+1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateRef(context.Background(), ref, next, parent); err != nil {
		t.Fatal(err)
	}
	return next
}

func testIdentity(offset int) Identity {
	when := time.Date(2026, 1, 2, 3, 4, 5+offset, 0, time.UTC)
	return Identity{Name: "AckOS Test", Email: "ackos@example.invalid", When: when}
}

func localStateSnapshot(t *testing.T, root string) string {
	t.Helper()
	index := filepath.Join(root, ".git", "index")
	indexBytes, err := os.ReadFile(index)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var state strings.Builder
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == filepath.Join(root, ".git") {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		state.WriteString(rel)
		state.WriteByte('\x00')
		info, err := entry.Info()
		if err != nil {
			return err
		}
		state.WriteString(info.Mode().String())
		state.WriteByte('\x00')
		if info.Mode().IsRegular() {
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			state.Write(contents)
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			state.WriteString(target)
		}
		state.WriteByte('\x00')
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if indexBytes == nil {
		state.WriteString("index:absent")
	} else {
		state.WriteString("index:present\x00")
		state.Write(indexBytes)
	}
	return state.String()
}

type providerWrapper struct {
	control.Provider
	afterObserve func()
	afterExecute func(control.Execution)
}

func (p providerWrapper) Observe(ctx context.Context, req control.ObserveRequest) (control.Observation, error) {
	result, err := p.Provider.Observe(ctx, req)
	if err == nil && p.afterObserve != nil {
		p.afterObserve()
	}
	return result, err
}

func (p providerWrapper) Execute(ctx context.Context, req control.ExecuteRequest) (control.Execution, error) {
	result, err := p.Provider.Execute(ctx, req)
	if err == nil && p.afterExecute != nil {
		p.afterExecute(result)
	}
	return result, err
}

func (p providerWrapper) Verify(ctx context.Context, req control.VerifyRequest) (control.Verification, error) {
	return p.Provider.Verify(ctx, req)
}

func resultEvidenceCommit(t *testing.T, result control.Execution) string {
	t.Helper()
	var evidence struct {
		ResultCommit string `json:"result_commit"`
	}
	if err := json.Unmarshal(result.Evidence, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.ResultCommit == "" {
		t.Fatal("execution evidence omitted result commit")
	}
	return evidence.ResultCommit
}
