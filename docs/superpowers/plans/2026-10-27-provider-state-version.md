# Provider Resource State and Concurrency Token Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep resource state and provider concurrency tokens separate so sequential Git Host operations work without weakening either CAS boundary.

**Architecture:** Add an optional opaque token to control and kernel observation evidence. FileProvider reports the file-state fingerprint as resource state and the full Git tip as its concurrency token; Host preserves both through execution, verification, and recovery. Runtime's existing root CAS remains on resource state.

**Tech Stack:** Go 1.25, existing typed Git SDK, existing control Host and kernel Runtime.

**Spec:** `docs/superpowers/specs/2026-10-27-provider-state-version-design.md`

## Global Constraints

- Keep the kernel provider-neutral.
- Preserve verification-before-commit, single-use authority, subject binding, fresh recovery evidence, and local CAS.
- Keep FileProvider's full-tip expected-old Git CAS and never retry a stale update.
- Do not add dependencies or MCP functionality.

## Review Focus

- Provider observations with a token must retain it in the authorized Execute before evidence.
- Provider verification with a token must retain the post-operation token and remain bound to exact execution evidence.
- Recovery after a stale branch tip may retry only after a fresh observation and only if resource state still matches the Runtime root.
- Recovery after an external target-state change must not adopt that state or bypass local CAS.
- Providers without tokens must preserve existing Host/Runtime behavior.
- A Git-backed Runtime is seeded server-side with an observed file-state fingerprint; no caller-supplied token or state enters initialization.

---

### Task 1: Add token fields to evidence contracts

**Files:**
- Modify: `control/provider.go`
- Modify: `control/provider_adapters.go`
- Modify: `kernel/kernel.go`
- Test: `control/provider_adapters_test.go`
- Test: `kernel/kernel_test.go`

**Interfaces:**
- Add optional `ConcurrencyToken string` to `control.Observation`, `control.Verification`, and `kernel.Observation`.
- Add `kernel.NewObservationWithConcurrencyToken(subject, state string, version uint64, concurrencyToken string, observedAt time.Time)`; keep `NewObservation` source-compatible by delegating with an empty token.
- Runtime verification must require a non-empty, changed post-operation token whenever its authorized before observation contained a token.

- [x] **Step 1: Write tests** that a token participates in observation validation/fingerprinting and that the provider adapter preserves it in before and verification evidence; cover missing and unchanged post-operation tokens.
- [x] **Step 2: Run focused tests**; they failed with missing token fields/constructor as expected.
- [x] **Step 3: Implement the optional fields, token-aware constructor, and provider adapter mapping**, including token in the kernel observation fingerprint.
- [x] **Step 4: Run `go test ./kernel ./control`**; both packages passed and legacy tokenless observations remain green.

### Task 2: Preserve tokens through Host lifecycle and recovery

**Files:**
- Modify: `control/host.go`
- Modify: `control/provider_adapters.go`
- Test: `control/host_test.go`

**Interfaces:**
- `Host.Control` builds kernel observations from both provider resource state and concurrency token.
- `beforeObservation` copies both state fingerprint and token into `control.Observation`.
- Provider verification returns a kernel observation carrying its post-operation token.

- [x] **Step 1: Add Host tests** proving the token reaches Execute, Verify, and a fresh recovery attempt; assert changed resource state remains blocked by the existing Runtime CAS.
- [x] **Step 2: Run the focused Host tests**; token-loss assertion failed as expected.
- [x] **Step 3: Implement token forwarding**; Runtime also requires a non-empty, changed post-operation token when the before observation contained one.
- [x] **Step 4: Run `go test ./control -count=1`**; passed.

### Task 3: Separate Git file state from branch-tip token

**Files:**
- Modify: `integrations/git/provider.go`
- Modify: `integrations/git/provider_test.go`
- Modify: `docs/git-file-replace-contract.md`
- Modify: `docs/PROVIDER_ARCHITECTURE.md`

**Interfaces:**
- `FileProvider.Observe` returns `FileStateFingerprint(mode, bytes)` as `Resource.Fingerprint` and `git-tip-v1:<full commit>` as `ConcurrencyToken`.
- `FileProvider.Execute` and `Verify` read the authorized tip from `Before.ConcurrencyToken`.
- `FileProvider.Verify` returns the exact verified result tip as its post-operation `ConcurrencyToken`.

- [x] **Step 1: Add provider tests** asserting observation contains exact file state and full tip separately, and verification returns the result tip.
- [x] **Step 2: Run those tests**; observation, verification-token, and before-state binding assertions failed against the conflated contract.
- [x] **Step 3: Update Observe, Execute, and Verify** to use the separate fields without changing Git CAS/recovery behavior; update contract documentation.
- [x] **Step 4: Run `go test ./integrations/git -count=1`**, including existing stale-tip and unrelated-tree tests; passed.

### Task 4: Prove sequential Host operations and failure behavior

**Files:**
- Modify: `integrations/git/provider_test.go`
- Modify: `control/host_test.go`

- [x] **Step 1: Add a same-Host/same-Runtime test** for two successful replacements, with the second operation obtaining a fresh observation through Host; verify bytes, modes, tip ancestry, and unrelated files.
- [x] **Step 2: Add deterministic stale-tip and recovery assertions**; stale update fails, preserves the competing ref, and a fresh retry succeeds only with unchanged target state.
- [x] **Step 3: Add post-CAS verification-failure coverage** proving recovery does not silently adopt the uncommitted Runtime state.
- [x] **Step 4: Run focused regressions**; all three integration regressions passed, as did the complete Git provider package.

### Task 5: Validate complete change

**Files:**
- Review all changed files and documentation.

- [ ] Run `test -z "$(gofmt -l .)"`.
- [ ] Run `go vet ./...`.
- [ ] Run `go test ./...`.
- [ ] Run `go test -race ./...`.
- [ ] Review exact branch HEAD, diff scope, and PR Review Loop state before commit/push/PR actions.
