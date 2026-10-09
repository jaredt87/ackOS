# Git Provider V0: Root and Lineage Audit

This decision record is committed before the Git provider implementation. The PR description carries the same contract.

## One configured resource

The kernel root is a single state string bound to one subject. The MCP process configures one repository, one full branch ref (`refs/heads/...`), and one repository-relative target path. The path must exist at startup; missing paths fail startup rather than creating a first-write resource. Requests for any other subject are rejected.

The root is seeded from the provider's actual observation of the configured path, not a constant.

## State and lineage

- State is a versioned encoding of the target blob ID. Observed and desired state use the same format.
- Version is the first eight bytes of the branch tip commit ID, interpreted consistently as a `uint64`.
- Execute cannot compare against a full authorized tip because the kernel observation does not carry it. Execute rereads the tip and target blob, checks the authorized blob state and 64-bit lineage prefix, and gives the full tip it just read to `UpdateRef` as expected-old for atomic read-to-write protection.
- This catches ordinary stale observations and read-to-write races. It does not cover a deliberately constructed 64-bit prefix collision.
- Verification independently checks the claimed commit, trailer, parent, and single-path tree diff.

## Root drift and restart semantics

The kernel does not adopt observations into its committed root. If target content changes out-of-band, a call is rejected at the kernel root compare-and-swap before the provider's lineage check. The running process remains wedged until restart. A commit that changes the branch tip without changing the target blob changes Version but not State and must not wedge the runtime. If a ref update succeeds but verification fails, the repository may be ahead of the kernel root and restart/re-baselining is likewise required.

**Restart means “trust the repository as it is now.”** Startup seeds the new in-memory root from the current target blob without an authorization step. This intentionally adopts any out-of-band state present at restart; it is not a neutral recovery action. Do not silently re-seed the root during a running process.

The root-drift error must explain that the repository changed outside ackOS since startup and that restart re-baselines from current repository state.

## Test plan distinctions

- A manual out-of-band edit before a call tests kernel root-drift rejection, not provider-level stale/ABA protection.
- Provider stale and ABA checks require race-injection wrappers that mutate the repository between Observe and Execute inside a single call.
- Integration coverage includes A→B→A lineage, no-op rejection without an empty commit, a tip-only commit, target-content drift with readable error, and an executable recovery sequence: out-of-band target edit → failed call → restart/re-seed → successful call.
- MCP V0 ignores caller-supplied `observed_state`; caller-held observations are not checked.

## Scope limits

Use a normal non-bare repository with another branch checked out. Update only the configured target ref; do not mutate the working tree or index. Bare-repository support and any SDK expansion are out of scope. If the SDK cannot reliably identify the checked-out branch, document the limitation rather than adding raw Git commands. Keep the old raw-Git provider implementation out of this change.
