# ackOS

**Autonomous Control Kernel**

ackOS is a small, safety-oriented control kernel for autonomous state transitions.

It is called an "OS" because it provides a foundational control boundary—not because it is a traditional operating system.

## What ackOS does

ackOS provides the kernel boundary between **observed reality, governed intent, execution, verification, and committed state**.

The core lifecycle is:

```text
Observe
   ↓
Normalize
   ↓
Reconcile
   ↓
Govern
   ↓
Reserve
   ↓
Start
   ↓
Verify
   ↓
Commit
```

Execution itself remains outside the kernel. ackOS governs whether an execution may proceed and whether its resulting evidence is sufficient to commit a state transition.

## Design principles

- Deterministic reconciliation
- Evidence-bound decisions
- Explicit execution authority
- Verification before commitment
- Recovery without authority creation
- Atomic/CAS-oriented state transitions
- Explicit durability and concurrency boundaries
- Provider-neutral kernel semantics

The research program behind ackOS established an evidence-backed safety theory and documented the boundaries where stronger guarantees require additional mechanisms. It does **not** claim universal correctness or a mathematical proof of the entire implementation.

## Status

ackOS V0 is the implementation phase following the completed ACK research program (E11–E17).

The initial V0 goal is to turn the researched control model into a small, coherent, usable Go kernel without prematurely adding schedulers, provider-specific machinery, distributed coordination, or other mechanisms that are not required by the kernel boundary.

## Repository

The project is intentionally starting small. Architecture, formal models, implementation, tests, and documentation will be added incrementally as the V0 kernel is built.

## License

The ackOS core is licensed under the **Apache License 2.0**. See [`LICENSE`](LICENSE) and [`LICENSING.md`](LICENSING.md).

Copyright 2026 Jared Thomson.
