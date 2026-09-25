# ackOS V0 Architecture

ackOS is a small, provider-neutral autonomous control kernel. Its job is to control the boundary between observed reality, governed intent, external execution, independent verification, and authoritative commitment.

## Lifecycle

```text
Observe → Normalize → Reconcile → Govern → Reserve → Start → Verify → Commit
                                      │
                                      └── failure → Recover → Observe
```

Execution is not commitment. A provider may report successful execution without proving the desired postcondition. ackOS therefore requires independent post-execution evidence before commitment.

## Kernel owns

- observation and evidence identity
- deterministic normalization
- reconciliation
- governance decisions
- execution authority and single-use reservation
- execution lifecycle state
- independent verification boundary
- CAS-oriented authoritative commitment
- explicit recovery boundary

## Kernel does not own

- AWS, Kubernetes, Terraform, databases, or other providers
- cloud credentials
- provider-specific mutation logic
- enterprise SSO/RBAC
- distributed control planes
- durable authority storage in V0
- workflow scheduling
- AI agent orchestration

Providers implement the execution and observation boundaries. The kernel decides whether an execution is authorized and whether verified evidence permits commitment.

## Evidence freshness

Verification is gated on completion of the execution attempt. A verifier cannot advance the lifecycle while the provider execution is still running, and a failed execution moves the runtime directly to recovery. Observations are also rejected while an execution is in flight so a new observation cannot discard an active execution boundary.

Post-execution verification evidence must be temporally captured after the recorded execution completion time. A higher provider version by itself is insufficient because that version could have been observed before the attempt. The observation timestamp is included in the evidence fingerprint, so callers cannot make an old observation appear fresh by mutating only its timestamp.

Recovery applies the same temporal boundary. An observation used to recover from a failed or abandoned attempt must be captured after the completed execution attempt. Recovery therefore cannot simply replay or relabel an observation that predates the abandoned execution.

Only one verification callback may be active for an execution attempt. This prevents competing verifiers from racing one another and moving a committed lifecycle backward into recovery.

## Git Execution Architecture & Security Boundary

The `git.Runner` package provides low-level, sandboxed subprocess execution for Git plumbing commands against a single repository root. It is an integration boundary, not a Provider or kernel abstraction.

### Guarantees Enforced by Runner

- **Repository Root Anchoring:** Validates that target paths resolve directly to a repository root and rejects any `.git` metadata symlink that escapes the canonical Git metadata root, including nested refs and object fan-out paths. Metadata is also revalidated immediately before each Git execution to mitigate post-construction symlink mutations; this is defense-in-depth, not an OS-level lifetime confinement guarantee.
- **Clean-Room Environment:** Constructs a minimal, explicit process environment (`sanitizedEnv`), discarding ambient environment variables.
- **Hardened Execution Flags:** Enforces `core.hooksPath=/dev/null`, `core.fsmonitor=false`, `core.editor=false`, and `--no-replace-objects`; `diff` also receives `--no-ext-diff`. to prevent common repository-configured subprocess execution.
- **Argument Boundaries:** Rejects caller-supplied global override flags including `-C`, `--git-dir`, `--work-tree`, `-c`, `--config`, `--exec-path`, `--config-env`, `--bare`, and replacement-object controls. It also rejects command options that can invoke external programs, including `cat-file --filters`, `cat-file --textconv`, `hash-object --path`, and `commit -e`/`--edit`. Editor environment variables are neutralized as defense-in-depth.
- **Subcommand Allowlist:** `Runner.Run` accepts only the explicitly supported Git commands (`rev-parse`, `hash-object`, `cat-file`, `write-tree`, `commit-tree`, `diff`, `status`, and `commit`). `update-ref` is intentionally excluded from the generic Runner surface. Repository-defined aliases and other Git subcommands are rejected before process dispatch.
- **Deterministic Executable Resolution:** Resolves the `git` binary path once at construction time (`exec.LookPath`) and executes via that resolved path.
- **Direct Process Cancellation:** Uses `exec.CommandContext` so cancellation terminates the direct Git subprocess. A successfully completed command remains successful even if the context expires in the race window after process completion.

### Intentionally Deferred Boundaries

- **Process-Group Cleanup:** `Runner` manages and cancels the direct Git process spawned via `exec.CommandContext`. Cleanup of descendant process groups is deferred.
- **Pathspec Interpretation:** `Runner` passes arguments directly to Git without evaluating pathspec magic (`:`, `!`, `*`). Path sanitization remains the responsibility of caller call sites. The generic Runner rejects help options, `hash-object --path`, `cat-file --filters`, and `cat-file --textconv` so Git help viewers, repository `.gitattributes` filter drivers, and text conversion drivers cannot execute through this boundary. Interactive commit editing is likewise rejected with `-e`/`--edit`. When the eventual Git Provider needs path-based hashing or filtered object access, it must establish an explicit safe filtering policy rather than relying on the generic Runner.
- **Provider & Abstraction Layers:** Higher-level Provider interfaces, working-tree operations, and plugin abstractions are deferred to PR #16.

## V0 guarantees and boundaries

V0 is a single-process, in-memory implementation. Authority consumption is atomic within the runtime and concurrent attempts cannot both cross the same execution boundary. CAS is atomic within the in-memory state store.

V0 does not claim restart-safe single-use authority. The research program established that restart-safe authority requires durable consumption semantics and safe crash ordering. Distributed concurrency likewise requires an explicit coordination/authority mechanism beyond this runtime.

Recovery never reuses forward execution authority. It returns to an observation boundary so subsequent reconciliation and authorization are based on fresh evidence.

## Design rule

The provider executes; ackOS governs. Provider success is evidence about an execution attempt, not permission to commit. Only fresh, independently verified evidence for the exact governed transition can advance the authoritative root through the CAS boundary.
