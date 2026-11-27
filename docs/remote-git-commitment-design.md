# Remote Git Commitment: Design and Proof Plan

## Status

Design investigation only. This document does not add remote mutation behavior, change the kernel lifecycle, or claim that GitHub publication is implemented.

## Problem

The current `integrations/git.FileProvider` operates against a configured local Git repository. Its observation, execution, and verification paths read local refs and Git objects; execution creates local objects and advances a local branch ref with an expected-old compare-and-swap. Verification independently rereads that local ref and object graph.

That is a sound local-repository boundary, but it does not establish that a corresponding branch update reached GitHub. A hosted agent workflow therefore cannot claim a remote GitHub change merely because the current provider reports a locally verified commit.

## Goal

Enable a governed ackOS operation to replace one existing regular file on an explicitly configured remote Git branch, while preserving the existing Observe → Normalize → Reconcile → Govern → Reserve → Start → Verify → Commit lifecycle.

The remote write must occur inside the ackOS provider execution boundary. The agent, Wingman, Fractal, workflow orchestration, and downstream safe-output job must not independently write the repository.

## Non-goals

- No kernel changes or new authority semantics.
- No generic execute endpoint or agent-held GitHub credential.
- No direct GitHub write from the Copilot agent or workflow job.
- No Terraform apply, cloud provisioning, merge, or default-branch mutation.
- No automatic retry against a newly observed remote tip.
- No claim of distributed/restart-safe authority beyond ackOS's documented V0 guarantees.
- No new external Go dependency without explicit approval.

## Required boundary

1. **Configuration binds the remote.** The provider must be configured by the operator with an exact repository identity, remote URL/host, branch allowlist, and target-path policy. A resource identifier supplied by an agent must not be able to select an arbitrary repository, host, branch, or credential.
2. **Observe is remote.** The authorized observation must capture the remote branch's full commit ID and the exact target file mode/content fingerprint from that commit. A local clone's possibly stale ref is not sufficient evidence of current remote state.
3. **Execute consumes the authorized Before.** Before any remote mutation, execution must freshly establish that the remote branch still equals the full commit ID in the authorized concurrency token and that the target file still matches the authorized resource fingerprint.
4. **Remote ref update is exact CAS.** The write mechanism must reject every remote-tip change since observation, including a remote branch that moved to a different descendant or was rewound. It must not silently retry using a newly read tip. The implementation must demonstrate the exact compare-and-swap semantics of the selected Git transport; a read-then-unconditional-write sequence is not acceptable.
5. **Execution evidence is exact.** Evidence must bind the execution ID, authorized remote tip, resulting commit ID, repository identity, branch ref, and target path sufficiently to prevent evidence substitution across resources or executions.
6. **Verify reads GitHub independently.** Verification must perform fresh remote reads after execution completion. It must establish that the remote branch points to this exact result commit, that the result commit is a single child of the authorized tip, that the recursive tree differs only at the requested path, and that the target blob bytes and mode match the governed desired fingerprint. It must perform a final remote-ref read after object verification and fail if the branch moved.
7. **Commit remains kernel-owned.** Only successful, fresh, execution-bound provider verification may allow the existing kernel commitment path to advance. A successful remote write alone is not commitment.
8. **Ambiguous outcomes fail closed.** If a network error occurs around the remote ref update, the provider must perform a bounded, cancellation-independent read of the remote ref. It may report execution success only if that read proves this exact result commit is installed. Otherwise it must return an outcome that does not falsely claim success and preserves enough error context for recovery.

## Transport investigation: unresolved decision

The implementation must compare the actual guarantees of the candidate transport(s), rather than assuming that a convenient API is a CAS primitive.

Questions to resolve before coding:

- Can the chosen GitHub-facing operation atomically require the ref to equal the exact authorized old commit, including rewind/race cases?
- Does the operation expose a stable success/failure boundary when the server accepts the update but the client loses the response?
- Can remote reads independently retrieve commit, tree, and blob content by immutable object ID?
- Can credentials be restricted to one repository and contents-write scope, held only by the ackOS service, and excluded from agent logs and artifacts?
- Can the design avoid adding dependencies while retaining bounded contexts, safe subprocess invocation, and testability?
- Does the mechanism work for both SHA-1 and SHA-256 object IDs, or must the supported format be explicitly constrained?

A plain read followed by an unconditional ref update is rejected. A fast-forward-only push is not assumed to prove exact-old-tip CAS unless the implementation also proves that it rejects a rewind to an ancestor. A force-with-lease or API conditional update must be tested against the exact semantics relied upon; do not infer those semantics from its name.

## Proposed implementation shape

Keep the kernel unchanged. Keep the existing local `FileProvider` behavior intact.

The first code change should be the smallest provider-side transport seam that can be tested against a disposable local bare remote and, separately, a real GitHub test repository. Avoid a broad provider abstraction or a generic remote execution framework. If the seam requires changing the concrete `*Repository` dependency, justify that change narrowly and preserve the current local implementation and tests.

Do not combine this work with ackos-api, Wingman, Fractal, or hosted-workflow changes. Those components should continue to call the existing ackOS control path; they do not own remote publication.

## Required test matrix

### Deterministic local tests

Use a disposable bare remote and a separate working clone. Exercise the complete Host.Control path with independent Git reads.

1. Remote observation returns the exact remote tip and target fingerprint.
2. Authorized replacement succeeds and the remote ref points to the exact result commit.
3. A concurrent remote descendant update after observation rejects execution and preserves the concurrent writer's tip.
4. A remote rewind to an ancestor after observation is rejected; this specifically distinguishes exact CAS from fast-forward-only behavior.
5. Only the requested path's mode/object changes in the recursive tree.
6. A branch advance after the remote write but before/during verification causes verification failure and is never rolled back.
7. A lost/failed response after server acceptance is resolved only by a bounded read proving this execution's exact result commit.
8. An ambiguous outcome that cannot be resolved does not report execution success or kernel commitment.
9. Cancellation and timeout behavior preserve the kernel's existing execution-completion and verification-freshness rules.
10. No test or implementation path mutates the local working tree or index unless a separately reviewed requirement explicitly changes that contract.

### GitHub integration tests

Use a dedicated disposable repository and branch, never the default branch.

- Verify least-privilege credentials and repository/branch/path allowlists.
- Prove the same concurrent-update and rewind rejection cases against the actual GitHub transport.
- Prove independent remote readback of commit, tree, and blob.
- Prove that the workflow/agent cannot access the credential or invoke a mutation outside ackOS.
- Record the exact remote commit SHA and independently inspect the GitHub branch after the run.

### Required repository checks

For changes under `integrations/`, run exactly:

```sh
test -z "$(gofmt -l .)"
go vet ./...
go test ./...
go test -race ./...
```

Inspect CI on the exact proposed head. Do not mark the capability complete based only on unit tests or a local bare-remote test.

## Acceptance gate

This feature is complete only when:

- the remote mutation is dispatched through the existing ackOS Host/provider lifecycle;
- the exact remote old-tip compare-and-swap property is demonstrated, including rewind;
- the remote branch and immutable objects are independently re-read during verification;
- the kernel's existing verification-before-commit boundary remains unchanged;
- all required Go checks and race tests pass;
- a dedicated GitHub test branch is updated through the ackOS path and independently confirmed on GitHub.

Until every gate passes, describe the capability as local Git execution only, not remote GitHub commitment.
