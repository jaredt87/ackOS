# Provider Resource State and Concurrency Token

## Intent

Keep Host and Runtime state in the resource-state domain while preserving each provider's independent concurrency version. This is required for repeated Git file replacements through one Host and Runtime.

## Goals

- `Resource.Fingerprint` represents observed resource state; for Git files it is the fingerprint of exact file mode and bytes.
- Providers may attach an opaque concurrency token to an observation and its verification. For Git this token is the full branch-tip fingerprint.
- Host and the kernel preserve the token in authorized before and independently verified after evidence.
- Runtime's existing state CAS continues to compare resource state. FileProvider continues to use a full-tip Git CAS and rejects a tip change after Host observation.
- Recovery accepts only fresh, subject-bound provider evidence and does not change the Runtime's authoritative resource root.

## Non-goals

- No MCP tool, server configuration, or new Git mutation path.
- No change to authority consumption, verification ordering, Runtime lifecycle phases, or provider CAS semantics.
- No automatic adoption of externally changed resource state during recovery.
- No Runtime bootstrap API. A Git-backed Runtime must be initialized by trusted server-side setup with a provider-observed resource-state fingerprint; a caller request cannot supply that value.

## Approaches

1. Keep the Git tip in `Resource.Fingerprint` and add a separate post-operation token path through Host/Runtime. This is a smaller API change but leaves transition before/after values in different domains.
2. Keep `Resource.Fingerprint` semantic and add an opaque concurrency token through provider observations, kernel observations, execution, verification, and recovery. **Selected:** this preserves the meaning of Runtime's authoritative root and keeps Git's branch-tip CAS provider-owned.

## Design

Add an optional opaque concurrency-token field to provider and kernel observations and verifications. Existing providers may omit it and retain their current behavior. FileProvider Observe will read the target entry from the observed commit, derive its resource fingerprint from mode and exact blob bytes, and return the branch-tip fingerprint separately as the concurrency token. Execute will use only that token for its authorized-tip CAS. Verify will independently return the resulting tip token after its final freshness check. The Host adapters will preserve both values.

Runtime verification will require the post-execution evidence to retain a token whenever the authorized before observation contained one. Runtime Commit continues to CAS its authoritative resource root from observed resource state to desired resource state. Recovery carries fresh resource state and token forward but leaves the authoritative root unchanged, so changed target state remains a conflict.

The Runtime constructor remains explicit. A future server integration that accepts dynamic resource paths must obtain the initial file-state fingerprint from the configured provider and create or select a Runtime for that resource. This change makes the seed's domain explicit but does not add lazy Host bootstrap.

## Validation

Use a disposable Git repository and one Host/Runtime for at least two sequential replacements. Also cover deterministic stale-tip rejection and recovery with a fresh unchanged target state; preserve verification failure and unrelated-tree protections. Existing generic providers and synthetic behavior must remain unchanged.
