---
name: control
description: Use ackOS as the control boundary whenever an agent is about to make a side-effecting change. Route intent, constraints, execution, independent verification, and commit through an executable ackOS integration instead of treating the model's own reasoning as authority.
---

# ackOS control boundary

Use this skill whenever the task can change an external system, repository state, infrastructure, data, security controls, business systems, or another real-world state.

## Core rule

The agent is the intelligence layer. ackOS is the control boundary. The ackOS integration must own the executor dispatch. An independent verifier establishes what actually happened. ackOS commits only after verification succeeds.

```text
Agent / LLM
    ↓ proposal
ackOS integration
    ↓ authorized transition + executor dispatch
Executor / API
    ↓ actual change
Independent verifier
    ↓ evidence
ackOS
    ↓
COMMIT or REJECT
```

## Before execution

1. Identify the subject being changed.
2. Establish the current observed state and its freshness.
3. State the intended outcome.
4. Preserve the user's constraints and prohibited actions exactly.
5. Construct the smallest concrete transition that satisfies the intent.
6. Before any side effect, require an executable ackOS integration that both controls the executor dispatch and provides an independent verifier for the resulting state.
7. Do not treat the skill instructions, the model's own reasoning, a governance/reservation response, or a proposed action as execution authority.
8. If the integration does not own the executor call and independent verification path, fail closed: prepare a proposal only and do not perform the side effect directly.

## During execution

- Execute the transition only through the ackOS integration that owns the executor call.
- Do not silently broaden scope, alter constraints, or reuse authority for a different transition.
- If authorization is denied, expired, consumed, mismatched, or otherwise invalid, stop rather than bypassing the control boundary.
- If execution fails, enter recovery and require fresh evidence; never reuse forward execution authority.

## After execution

- Obtain verification from the independent verifier configured as part of the executable integration.
- Verification must establish the resulting state, not merely repeat the executor's success message.
- Require fresh evidence bound to the expected subject and resulting transition.
- Treat verification failure as rejection/recovery, not success.
- Only describe the state as committed after ackOS successfully commits it.

## Integration boundary

This skill is an adapter for agent runtimes. It is not itself the security boundary. A skill cannot make a direct agent tool call safe merely by instructing the model to obtain authorization first. The executable ackOS integration must wrap or dispatch the relevant executor and must provide an independent verification path.

If no such executable integration is configured, do not execute the side effect and do not claim that it is protected by ackOS. Limit the work to preparing a proposal, or explain that the project needs an ackOS integration for its executor and verifier.

When a project already contains an ackOS integration, prefer that integration over inventing a parallel policy mechanism. Preserve the kernel's lifecycle and error semantics rather than collapsing authorization, execution, verification, and commitment into one agent step.
