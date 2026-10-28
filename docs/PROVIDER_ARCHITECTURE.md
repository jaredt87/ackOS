# Provider Architecture

PR #14 established the small provider boundary used by the in-process Host.

The control layer treats resource fingerprints as opaque values. It does not interpret their
format or derive ordering, ancestry, or provider-specific lineage from them.

The in-process Host owns provider registration and binds the kernel-issued execution authority
to provider invocation. The kernel remains responsible for authorization, execution lifecycle,
verification gating, recovery, and CAS commitment.

Providers own domain-specific resource state and must independently observe and verify that state.
The Memory provider completes Observe -> Execute -> Verify without Git-specific concepts. Git's
file provider keeps the resource fingerprint (file mode and bytes) separate from its opaque
branch-tip concurrency token. Host and Kernel preserve both values; the Runtime CAS remains on
resource state, while the provider uses its branch-tip token for Git compare-and-swap.

Providers that do not need a separate concurrency token may leave it empty and continue using
their existing resource-state behavior.
