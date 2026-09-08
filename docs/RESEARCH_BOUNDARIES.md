# ackOS Governing Specification

## Research-Derived Implementation Constitution

ackOS is the implementation/refinement of the research performed in:

https://github.com/jaredt87/autonomous-control-kernel

That repository is the authoritative research source for the control, governance, evidence, lifecycle, concurrency, authority, verification, recovery, and commitment boundaries of ackOS.

The research repository is not optional background material. ackOS must remain inside the boundaries established by the TLA+/TLC work and subsequent E-series research conclusions.

## 1. Fundamental Rule

The research defines the allowed state machine and safety boundaries. ackOS implements those boundaries.

When implementation convenience conflicts with a research invariant, the research invariant wins.

When a test conflicts with proven lifecycle semantics, fix the test or implementation so that it matches the research. Do not weaken an invariant merely to obtain green CI.

When a proposed feature cannot be demonstrated to preserve established invariants, do not add it.

When uncertain, inspect the research repository, the relevant TLA+/TLC specification and findings, and the E-series implementation/review history before changing control semantics.

## 2. Non-Negotiable Safety Boundaries

### 2.1 Evidence is execution-bound

Evidence used to justify a transition must be bound to the specific execution/transition being evaluated. Evidence cannot become valid merely because a provider version increased, state happens to match, a timestamp changed, an observation was replayed, newer-looking observation data was substituted, or a verifier claims success without execution-bound evidence.

Observation identity, transition identity, subject identity, timestamps, fingerprints, and lifecycle state must remain appropriately bound. Evidence must not be detachable from the execution it is supposed to prove.

## 3. Verification Must Precede Commitment

Execution is not commitment. The lifecycle is conceptually:

```text
Observe
  ↓
Normalize
  ↓
Reconcile
  ↓
Govern
  ↓
Reserve Authority
  ↓
Execute
  ↓
Independent Verification
  ↓
CAS Commit
```

Execution success is evidence about an attempted operation, not proof that the desired authoritative state has been established.

Therefore execution must complete before verification; verification must independently establish the required postcondition; verification evidence must be fresh and correspond to the exact governed transition; failed verification cannot commit; commitment cannot precede successful verification; and CAS failure cannot become successful commitment.

## 4. Recovery Cannot Mint or Reuse Authority

Recovery is a boundary, not an alternate execution path. Recovery must never reuse forward execution authority, revive consumed authority, mint new authority implicitly, treat failed execution as successful execution, skip governance, skip observation, skip reconciliation, or inherit authorization from the failed attempt.

Recovery returns the system to a fresh observation/reconciliation boundary:

```text
failed execution
      ↓
   RECOVERY
      ↓
 fresh evidence
      ↓
   OBSERVE
```

Authority for a subsequent execution must be established through the normal authorization path.

## 5. Authority Is Single-Use

Execution authority is consumed at the execution boundary. The runtime must prevent authority replay, concurrent reuse, execution after consumption, execution after expiration, transition/observation mismatch, and execution when the authoritative root no longer matches the transition's expected Before state.

Authority must be validated before crossing the external execution boundary. The authoritative root must be checked before authority consumption and before calling the executor so the runtime cannot execute an external A→B mutation while authoritative local state has already diverged from A.

## 6. Lifecycle Boundaries Are Explicit

The lifecycle is a safety mechanism, not merely bookkeeping. Invalid phase transitions must be rejected.

In particular, observation cannot overwrite an active execution boundary; observation cannot reset a verified-but-uncommitted execution; verification cannot begin before execution completion; failed execution cannot enter successful verification; verification cannot race with another verification attempt; commit cannot occur before verification; recovery cannot occur from an arbitrary phase; and recovery cannot reuse forward authority.

Concurrency must not create a second path through the lifecycle that is impossible in the serialized model.

## 7. Verification Concurrency

Only one verification operation may be active for an execution attempt. A stale verifier must never modify or clear state belonging to a newer execution lifecycle.

Verification operations therefore need lifecycle identity/binding sufficient to ensure stale asynchronous work cannot mutate a newer lifecycle. Cancellation must release only the reservation belonging to the verification operation that was cancelled.

## 8. Temporal Evidence Rules

Evidence has temporal meaning.

For verification:

```text
execution completion time
        <
evidence observation time
        <=
evidence receipt/current runtime time
```

Pre-execution evidence is stale. Higher-version pre-execution evidence is still stale. Future evidence is invalid. Changing an observation timestamp without changing its fingerprint must fail. Verification evidence must be independently captured after execution completion.

Recovery follows the same boundary:

```text
execution completion
        <
recovery observation
        <=
current runtime time
```

Do not weaken these checks because a test uses a frozen clock. Fix the test clock/lifecycle setup instead.

## 9. Observation Identity

An observation is not merely subject + state + version. Its evidence identity includes its timestamp.

The observation fingerprint must bind the relevant observation fields, including ObservedAt. Mutating the timestamp after creation must invalidate the observation. Evidence must not be transformable from stale to fresh by modifying metadata outside its fingerprint.

## 10. Subject Binding

The authoritative state store must not be treated as a globally interchangeable state value when the runtime has an established subject.

Once an execution lifecycle is associated with a subject, another subject cannot replace the observation, provide recovery evidence, silently reuse the same lifecycle, or exploit a state-only CAS to permit cross-subject execution.

Subject identity is part of the governance/evidence boundary.

## 11. Governance

Governance is authoritative. A governance decision must correspond exactly to the transition it authorizes.

The runtime must reject governance decisions whose transition, observation, or proposal fingerprint differs.

A NOOP transition is not an execution authorization. The runtime must not reserve execution authority for a transition that does not require execution. Governance must fail closed.

## 12. Authority Lifetime

Authority lifetime semantics must be explicit. A negative lifetime is invalid. It must never be interpreted as unlimited or otherwise silently converted into stronger authority.

Authority expiration must be checked at the execution boundary.

## 13. CAS Commitment

Authoritative commitment is a compare-and-swap boundary:

```text
expected authoritative state
        ↓
verify postcondition
        ↓
CAS(expected, desired)
        ↓
commit
```

If the authoritative state changed, CAS must report conflict. The runtime must not report successful commitment and must not overwrite the newer authoritative state.

Execution success plus verification success does not eliminate the authoritative CAS boundary.

## 14. No Nested if Statements in Go

Do not introduce nested `if` statements in Go. Use guard clauses, early returns, small helpers, and explicit state transitions.

Prefer:

```go
if invalidCondition {
    return err
}
if anotherInvalidCondition {
    return err
}
```

over nested conditionals. This rule applies to new implementation code and review fixes.

## 15. KISS / Minimal Refinement

ackOS V0 is intentionally small. Do not introduce speculative abstractions, distributed coordination, durable authority systems, provider integrations, workflow engines, AI orchestration, enterprise governance systems, or mechanisms not required by the established research.

The goal is a small executable refinement of the proven control boundary.

If a change is not required by the research, a demonstrated safety invariant, a concrete review finding, or a necessary implementation dependency, do not add it.

## 16. Tests Are Evidence of the Invariants

Tests must test the research invariants. Important regression classes include authority replay; concurrent authority execution; execution/verification ordering; observation during execution; verification concurrency; stale verifier behavior; pre-execution evidence; future-dated evidence; timestamp tampering; recovery freshness; recovery subject binding; failed execution; failed verification; CAS conflict; authoritative-root mismatch before execution; authority expiration; negative authority lifetime; governance binding; NOOP governance; and stale lifecycle transitions.

Do not weaken production safety checks because an incorrectly constructed test fails. Determine whether the test clock, lifecycle setup, expected phase, evidence timestamp, or assertion contradicts the research.

## 17. TLA+/TLC Is the Boundary Authority

The TLA+/TLC work in the research repository establishes the formal safety model. The Go implementation must not silently create behavior outside that model.

When changing lifecycle behavior:

1. identify the corresponding formal state/transition;
2. determine which invariant applies;
3. verify that the implementation preserves it;
4. add or update a regression test;
5. run the full Go test suite;
6. run race detection;
7. inspect CI;
8. request Codex review against the exact current HEAD.

If a proposed implementation requires a new semantic not represented by the research model, stop and analyze the research before implementing it.

## 18. Repository Governance

This document is the canonical ackOS statement of the research boundary. It must remain in the repository rather than existing only in chat history.

It establishes that:

- `autonomous-control-kernel` is the research authority;
- TLA+/TLC-derived invariants are binding;
- ackOS is an implementation/refinement of those boundaries;
- implementation must not expand beyond those boundaries without corresponding research;
- review and testing must enforce the invariants.

## 19. Development Procedure

For every implementation task:

1. Inspect the research and identify the invariant/boundary involved.
2. Inspect the current ackOS implementation rather than assuming it matches the research.
3. Inspect tests and determine whether they encode the invariant.
4. Implement the smallest safe change.
5. Add a focused regression demonstrating the invariant.
6. Run `gofmt`, `go vet ./...`, `go test ./...`, and `go test -race ./...`.
7. Inspect the actual CI result.
8. Request Codex review against the exact current HEAD.
9. Fix review findings directly, push the commit, rerun CI, and request fresh review.
10. Merge only when CI and race tests are green, research invariants remain intact, Codex has no unresolved findings, no nested Go `if` statements were introduced, and the implementation remains inside the V0 research boundary.

## 20. Absolute Rule

ackOS must never become a system whose behavior is more permissive than the research proved safe.

When there is uncertainty, choose fail-closed behavior. When a test conflicts with an invariant, investigate the test. When implementation convenience conflicts with an invariant, change the implementation. When a proposed capability requires a new authority, lifecycle, evidence, concurrency, or recovery semantic, return to the research before adding it.

The objective is not merely to make ackOS CI pass. The objective is an executable implementation that remains faithful to the control boundaries established by the autonomous-control-kernel TLA+/TLC research.
