# Git branch file replacement: contract and test design

Status: approved contract; implementation is present in this worktree. This
document specifies one operation only: replace the contents and mode of one
existing tracked file on a branch through `control.Host.Control`.

## Scope and boundaries

- The operation writes Git objects and compare-and-swaps one branch ref.
- The typed Git SDK needs one missing primitive, `ReadBlob`, to support
  independent verification of exact file bytes. Recursive tree walking is
  composed from the existing one-level `ReadTree`; no generic tree abstraction
  is required.
- It does not update a working tree or index.
- The target path must already exist as a regular blob entry. Adding, deleting,
  renaming, symlinking, or replacing a submodule is outside this operation.
- No retry, reflog-based ABA detection, generic transaction framework, CI
  workflow, or changes to PR #10 / PR #28 are included.
- Git objects written before the ref update may remain unreachable after a
  failed operation. Refs, index, and working tree must not be modified by the
  provider before the ref update succeeds.

## Request contract

The Host request needs one minimal API addition: `ControlRequest.Payload []byte`,
forwarded unchanged to `ExecuteRequest.Payload`. The Host also forwards the
same authorized `Desired ResourceRef` into `ExecuteRequest.Desired`; the current
execution request lacks that field, and the provider needs it to check that the
payload matches the fingerprint governance approved. The current Host instead
passes `[]byte(Desired.Fingerprint)` as the payload, which cannot carry file
bytes and mode.

The existing request fields are used as follows:

| Field | Contract |
|---|---|
| `Target.ID` | Canonical resource identity described below. |
| `Target.Fingerprint` | Required non-empty by the current Host API; ignored as a caller claim. The provider observes the current tip itself. |
| `Desired.ID` | Must equal `Target.ID`. |
| `Desired.Fingerprint` | `git-file-v1:sha256:<lowercase-hex>` for desired mode and raw content. |
| `Payload` | UTF-8 JSON object `{"mode":"100644","content":"<base64-standard>"}`. `mode` is exactly `100644` or `100755`; `content` is canonical padded standard Base64 of the exact desired bytes, with no line breaks. Unknown fields and trailing JSON values are rejected. |

The Host's governance decision is based on the exact `Desired.Fingerprint`
passed in this request. `Target.Fingerprint` is only required to satisfy the
current generic Host validation; it is not an authorization token and the Git
provider does not use it to select the expected branch tip. The provider's
fresh observation, preserved as `Before`, supplies that tip.

The desired fingerprint is SHA-256 over the byte sequence
`"git-file-v1\x00" + mode + "\x00" + content`, where `mode` is the six ASCII
bytes above and `content` is the decoded raw byte sequence. The provider checks
that the payload decodes and matches `Desired.Fingerprint` before writing Git
objects. Host governance authorizes the desired fingerprint, and this check
binds forwarded payload bytes and mode to that authorized desired state;
payload bytes must never be executed when their computed fingerprint differs.
This fingerprint names desired file state, not a predicted commit.

The resource ID is `git-file-v1:` followed by unpadded base64url encoding of
the exact UTF-8 bytes returned by Go `encoding/json.Marshal` on the string array
`[canonicalRepositoryRoot, fullRefName, path]`. The repository root is
canonicalized by absolute-path resolution, symlink evaluation, and cleaning,
and must equal the Git top-level directory; this matches the existing
`integrations/git.Open` root requirement. The ref is a full branch ref such as
`refs/heads/main`, under `refs/heads/`. The path is a slash-separated
repository-relative path with no empty, `.`, or `..` component, no `.git`
component, no backslash, and no NUL. Other valid UTF-8 Git filename bytes,
including newline and carriage return, are supported. Root, ref, and path must
be valid UTF-8. The path and ref are validated before any object writes.

## Observation and authorization

The provider observation fingerprint is
`git-tip-v1:<full-lowercase-object-id>`. The ID is the full 40- or 64-character
commit ID returned by `ReadRef`, depending on repository object format. The
provider does not use a short ID, branch name, path blob ID, or tree ID as the
authorization token.

At Execute, the provider freshly reads the branch ref and compares it with the
authorized `Before.Resource.Fingerprint` tip. A mismatch rejects execution as
stale and does not update the ref. The exact prior entry at the target path is
also read from the authorized tip's tree and must be a regular blob. It is
additional evidence for computing and verifying the replacement, not a
substitute for the full-tip comparison.

An A-to-B-to-A movement is considered the same authorized state if the ref is
again at the exact same commit ID A when Execute performs its compare-and-swap.
This contract does not inspect reflogs or maintain a generation counter.

## Execution and CAS

Execution reads the authorized commit and recursively walks its tree, including
nested directories. It writes the replacement blob and rebuilt tree objects,
then creates a commit whose sole parent is the authorized tip. It does not
touch the index or working tree. The commit message is
`ackOS: replace tracked file`, followed by an `Ack-Execution-Id: <id>` trailer
containing the Host-issued execution ID. This makes the resulting commit
distinctly attributable to this exact execution. It then calls the typed SDK's
`UpdateRef(ref, newCommit, expectedOldTip)` operation; the expected old value is
the full authorized tip ID.

The compare-and-swap is the commit point. Failure behavior is divided by that
boundary:

- **Failure before CAS succeeds:** the provider has not changed a ref, index,
  or working tree. Object writes may leave unreachable objects.
- **CAS rejects a stale expected tip:** do not retry against a newly observed
  tip and do not overwrite the newer ref. If a fresh read shows a different
  tip, return an error that matches `control.ErrStaleObservation` and also
  wraps the underlying `UpdateRef` error, so callers can inspect both with
  `errors.Is`/`errors.As`. If the reread shows the new result commit itself,
  treat the ref write as committed and return successful execution evidence;
  the atomic ref write may have completed before the Git process reported an
  error. Perform this recovery read with a fresh context detached from request
  cancellation but bounded to five seconds. If the reread cannot establish a
  changed tip, preserve the underlying Git error (and any reread error). The
  provider itself leaves the ref, index, and working tree unchanged on a
  rejected CAS.
- If the execution context expires while CAS outcome recovery is in progress,
  the Host waits up to six additional seconds for the Execute callback to return
  its result. This allows the provider's five-second bounded ref inspection to
  deliver evidence for an already-committed CAS. If the callback exceeds that
  grace, Host reports the execution timeout while keeping the provider gate
  occupied until the callback returns.
- Once CAS succeeds, Execute must return success and evidence for that commit;
  it must not report an execution failure after the ref has advanced.
- A failed CAS may leave unreachable blob/tree/commit objects. Those objects
  are permitted and are not a partial branch update.

Host returns execution and verification failures with their provider causes
wrapped, so callers can inspect stale and branch-advanced sentinels with
`errors.Is` and underlying Git command failures with `errors.As`.

## Execution result and exact execution binding

`Execution.ExecutionID` must equal the request's `ExecutionID` (also enforced
by the existing provider adapter). `Execution.Evidence` is UTF-8 JSON with
exactly these fields:

```json
{"schema":"git-file-replace-v1","execution_id":"...","before_tip":"<full-id>","result_commit":"<full-id>"}
```

The verifier rejects malformed or unknown-schema evidence, an execution ID
different from `VerifyRequest.ExecutionID`, a before tip different from
`VerifyRequest.Before.Resource.Fingerprint`, or an invalid full object ID. The
result commit is the commit actually created by Execute; it is not inferred
from matching file contents. The verifier also requires the commit's exact
execution-ID trailer to match, so reused or unrelated commits with matching
file contents do not pass.

## Independent verification

Verify performs fresh reads; it does not trust cached Execute tree data. It:

1. Reads the branch ref and requires it to equal `result_commit` before object
   verification. If the branch has already advanced, verification fails even
   if the new tip contains matching content.
2. Reads `result_commit`, requiring its sole parent to be `before_tip`.
3. Reads the before and result trees recursively, flattens non-tree entries to
   complete path/mode/object maps, and compares them. The only permitted
   difference is the requested path; ancestor directory tree IDs are omitted
   from this leaf-entry comparison because they necessarily change.
4. Reads the requested result blob bytes independently using the typed SDK's
   `ReadBlob`, then recomputes the desired fingerprint from those bytes and the
   result mode and compares it with `VerifyRequest.Expected.Fingerprint`.
5. After all commit, tree, and blob reads succeed, reads the branch ref again
   and requires it still to equal `result_commit`. This final read is the
   freshness check for successful verification; a branch advance during object
   verification fails even if the earlier ref read passed.
6. Returns the expected desired `ResourceRef` and a verification time taken
   after the final ref read.

If verification fails after CAS succeeded, the provider does not roll back the
ref. For example, a later external ref update remains in place and causes
verification to fail. The operation's successful CAS is the mutation boundary.
The returned `ControlResult` retains the successful `Execution`; the returned
error is the verification failure. Together these mean “the mutation committed,
but verification did not establish that this exact execution remains the
current branch tip,” not “the ref was never changed.” No rollback or overwrite
of a subsequent writer is attempted.

## Five invariant tests

Each test uses a fresh disposable repository, registers the Git provider with a
real `control.Host`, invokes `Host.Control`, and inspects the repository using
independent Git reads rather than provider state.

1. **Authorized replacement succeeds.** Start with an existing nested tracked
   file. Replace bytes and mode; require successful Control, a new branch tip
   whose sole parent is the authorized tip, and the requested content/mode.
2. **Stale authorization never overwrites newer state.** After the Host has
   observed the tip but before Execute's CAS, advance the branch independently.
   Require stale failure and confirm the newer tip and its tree remain intact.
3. **Only the requested path changes.** Compare full recursive path/mode/object
   maps at the authorized tip and resulting commit. Require exactly the target
   path to differ, and independently read its bytes and mode.
4. **Ref update is exact compare-and-swap.** Arrange for the ref to move between
   Execute's initial read and `UpdateRef`. Require CAS failure, no retry, and
   the concurrent writer's tip to remain current. Also verify the implementation
   supplied the full authorized tip as `expectedOld`.
5. **Failure outcomes match the CAS boundary.** Exercise and assert these
   distinct outcomes (the stale-CAS case also supplies the synchronization
   needed to prove invariant 4):
   - Before CAS succeeds, inject an object-construction failure; require refs,
     index, and working tree unchanged. Unreachable objects are allowed.
   - Have another writer advance the branch before CAS; require stale failure,
     no retry or overwrite, and preservation of that writer's ref and tree.
   - After this operation's CAS succeeds but before Verify reads the ref, have
     another writer advance it again; require accurate verification failure
     and preservation of the subsequent writer's ref. Do not require the ref
     to equal its pre-operation value or roll back this operation's commit.
     Index and working tree remain unchanged throughout.

## Acceptance criteria

- Host forwards the request payload and the authorized desired resource
  reference to Execute.
- The provider binds payload bytes/mode to the governed desired fingerprint.
- Execution uses the full observed tip in an expected-old CAS and never retries
  automatically.
- Verification independently checks the exact execution commit, recursive tree
  delta, result blob bytes/mode, and current branch tip.
- The five invariant tests above pass against disposable repositories through
  `Host.Control`.

Focused regression tests additionally pin down concurrency and path boundaries:

- Advance the branch after all object checks but before the final verification
  ref read; require verification failure and preserve the concurrent writer's
  ref.
- Let the ref update succeed, then cancel the execution context before
  reporting its error; require the bounded recovery read to confirm this
  operation's exact result commit and return execution evidence.
- Through `Host.Control`, let the ref update succeed and hold the provider until
  the execution deadline expires; require the Host to receive the recovered
  execution evidence, independently verify it, and return success.
- Replace the target beside unrelated tracked names containing newline and
  carriage return; require both sibling entries to remain unchanged.
