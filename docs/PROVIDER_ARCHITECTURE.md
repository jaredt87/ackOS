# Provider Architecture Spike

PR #14 establishes a small provider boundary without moving production Git behavior.

The control layer treats resource fingerprints as opaque values. It does not interpret their
format or derive ordering, ancestry, or provider-specific lineage from them.

The in-process Host owns provider registration and binds the kernel-issued execution authority
to provider invocation. The kernel remains responsible for authorization, execution lifecycle,
verification gating, recovery, and CAS commitment.

Providers own domain-specific resource state and must independently observe and verify that state.
The Memory provider is the first architectural proof: it completes Observe -> Execute -> Verify
without any Git concepts.

Git is intentionally not part of this phase.
