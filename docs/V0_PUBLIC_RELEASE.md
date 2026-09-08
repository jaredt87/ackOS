# ackOS V0 — Public Research Release

## What ackOS is

ackOS is a **research-derived, provider-neutral kernel for controlled infrastructure execution**.

V0 is the first executable implementation of the control boundaries established through the ACK research program (E1–E17).

The purpose of V0 is deliberately narrow:

> **Make the control boundary executable, inspectable, and independently testable.**

The kernel separates observation, proposal, normalization, reconciliation, governance, execution authority, execution, independent verification, commitment, and recovery into explicit lifecycle boundaries.

The central rule is simple:

> **Execution is not commitment.**

An executor reporting success does not by itself authorize state commitment. Commitment requires independent, fresh, transition-bound verification followed by an authoritative compare-and-swap against the expected state.

Recovery cannot reuse forward execution authority.

Authority is explicit, single-use, lifecycle-bound, and subject to expiration.

## This is a research-derived release

ackOS V0 is **not presented as a production-ready autonomous infrastructure platform**, and it does not claim universal correctness or a mathematical proof of the entire implementation.

The implementation intentionally remains within the boundaries established by the ACK research program.

V0 currently uses in-memory authority and state. It does **not** claim:

- durable authority consumption across restart;
- crash-safe persistence ordering;
- distributed coordination;
- provider-specific infrastructure correctness; or
- universal correctness outside the documented assumptions.

These are deliberate boundaries, not accidental omissions. The research identified durability, crash ordering, and concurrency as distinct safety boundaries that must be addressed explicitly rather than hidden behind implementation complexity.

## What testing has been done?

Yes — the testing history is part of the project's provenance and should be visible to users. It is provided as **evidence and context, not as a guarantee and not as a substitute for independent testing**.

The ACK research program used a progression of adversarial experiments and formal-model checking through E1–E17. The research attacked failure modes including stale evidence, authority misuse and replay, lifecycle violations, recovery, restart boundaries, crash/atomicity ordering, concurrent authority, and composed adversarial behavior.

The implementation refinement then carried those boundaries into the V0 kernel with automated tests covering, among other things:

- replay and authority reuse;
- stale and future-dated evidence;
- observation, subject, proposal, and transition binding;
- authority expiry and single-use behavior;
- execution versus independent verification;
- failed verification and recovery;
- stale asynchronous verification;
- CAS conflicts;
- concurrent authority behavior; and
- lifecycle ordering.

The Go CI suite also exercises formatting, vet, tests, and race detection.

### Does documenting the existing tests bias independent testing?

It can if presented as a checklist of **the only things that matter**. That is not how this release presents them.

The existing test suite tells you what we have already thought about. That makes it useful evidence, but it also tells an adversarial tester where our attention has already been concentrated.

**Independent testers should not limit themselves to these cases.** In particular, we want attacks that are orthogonal to the known test suite, challenge its assumptions, compose multiple failure modes, or demonstrate that an invariant is weaker than the research model suggests.

A useful mindset is:

> **Treat the existing tests as evidence of what has been attempted, not as the definition of what is safe.**

If you discover a failure outside the existing tests, that is valuable research rather than a failure of the project's purpose.

## Research and implementation

The research/provenance repository contains the formal models, experiments, safety contract, and research conclusions behind the kernel:

**ACK research:** https://github.com/jaredt87/autonomous-control-kernel

This repository contains the executable kernel:

**ackOS:** https://github.com/jaredt87/ackOS

The intended architecture is one-way: research establishes and challenges the safety boundary; ackOS implements the resulting kernel boundary; future enterprise/control-plane capabilities consume the kernel rather than duplicating its behavior.

## What we want from the public release

The next phase is independent scrutiny.

We want people to:

- inspect the implementation;
- run the tests and race detector;
- construct adversarial cases;
- challenge the relationship between the implementation and the formal model;
- attempt attacks we have not considered;
- identify hidden assumptions;
- build provider integrations around the kernel boundary; and
- report evidence that the documented boundary is incomplete or incorrect.

In particular, useful attack surfaces include replay, stale evidence, subject confusion, authority reuse, concurrency, verification/commit races, recovery, CAS behavior, lifecycle ordering, and interactions between multiple failure modes.

**The goal of V0 is not to claim that the boundary cannot be broken.**

**The goal is to make the boundary concrete enough that someone other than its authors can try to break it.**

## Scope boundary

V0 intentionally does not include:

- provider integrations such as AWS, Kubernetes, or Terraform;
- durable authority storage;
- crash-safe durable execution semantics;
- distributed coordination;
- a scheduler or workflow engine;
- AI orchestration; or
- an enterprise control plane.

Those capabilities may be developed later only when their requirements and safety boundaries are understood.

## Reporting findings

If you find a potential correctness or safety issue, please provide a minimal reproduction and explain which invariant or boundary you believe is violated. Distinguish between failures within the documented V0 assumptions and failures that cross an explicitly documented boundary.

Security-sensitive vulnerabilities should be reported through the repository's security reporting process rather than disclosed publicly first.
