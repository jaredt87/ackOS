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

## V0 guarantees and boundaries

V0 is a single-process, in-memory implementation. Authority consumption is atomic within the runtime and concurrent attempts cannot both cross the same execution boundary. CAS is atomic within the in-memory state store.

V0 does not claim restart-safe single-use authority. The research program established that restart-safe authority requires durable consumption semantics and safe crash ordering. Distributed concurrency likewise requires an explicit coordination/authority mechanism beyond this runtime.

Recovery never reuses forward execution authority. It returns to an observation boundary so subsequent reconciliation and authorization are based on fresh evidence.

## Design rule

The provider executes; ackOS governs. Provider success is evidence about an execution attempt, not permission to commit. Only verified evidence for the exact governed transition can advance the authoritative root through the CAS boundary.
