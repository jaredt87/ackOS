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

## The idea

Use the AI agent you already have.

```text
LLM / AGENT
    ↓ proposal
ackOS CONTROL BOUNDARY
    ↓ authorized transition
EXECUTOR / API
    ↓ real-world action
INDEPENDENT VERIFIER
    ↓ evidence
ackOS
    ↓
COMMIT / REJECT
```

The agent provides intelligence. ackOS provides the control boundary. You can therefore put the same kernel underneath an AI coding agent, cloud automation, DevOps workflow, security system, business application, data system, robotics controller, or another tool that can produce and act on intent.

## Agent integrations

ackOS is designed to plug into the agent you already use rather than requiring you to replace it.

### Claude Code

The repository includes a Claude Code plugin and `/ackos:control` skill.

```bash
claude --plugin-dir .
```

See [`docs/CLAUDE_CODE_PLUGIN.md`](docs/CLAUDE_CODE_PLUGIN.md).

### Gemini CLI

The repository is also a Gemini CLI extension. Install it directly from GitHub:

```bash
gemini extensions install https://github.com/jaredt87/ackOS
```

Then restart Gemini CLI and inspect the installed extension/skills:

```text
/extensions list
/skills list
```

See [`docs/GEMINI_EXTENSION.md`](docs/GEMINI_EXTENSION.md).

### ChatGPT

ChatGPT uses the current **Apps/MCP** integration model rather than the old ChatGPT plugin model. ackOS includes a ChatGPT-facing skill bundle at [`integrations/chatgpt/SKILL.md`](integrations/chatgpt/SKILL.md), and the intended executable integration is an MCP-backed ackOS app.

A skill alone is model-facing guidance; it is **not** the security boundary. For actual side-effect protection, ChatGPT must be connected to an executable ackOS integration that owns authorization, execution authority, independent verification, and commitment.

See [`docs/AGENT_INSTALL.md`](docs/AGENT_INSTALL.md) for the integration paths.

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

## Important boundary

Agent skills and plugins are **adapters, not the security boundary**. They teach an agent how to route side effects through ackOS, but model-facing instructions cannot by themselves prevent an agent from bypassing them.

The executable ackOS kernel remains the authority for authorization, verification, recovery, and commitment semantics. An integration should never collapse those stages into one model action.

## Status

ackOS V0 is the implementation phase following the completed ACK research program (E11–E17).

The initial V0 goal is to turn the researched control model into a small, coherent, usable Go kernel without prematurely adding schedulers, provider-specific machinery, distributed coordination, or other mechanisms that are not required by the kernel boundary.

## Repository

The project is intentionally starting small. Architecture, formal models, implementation, tests, and documentation will be added incrementally as the V0 kernel is built.

## License

The ackOS core is licensed under the **Apache License 2.0**. See [`LICENSE`](LICENSE) and [`LICENSING.md`](LICENSING.md).

Copyright 2026 Jared Thomson.
