---
name: ackos-control
description: Use ackOS as the control boundary whenever an agent is about to make a side-effecting change. Route intent, constraints, execution authority, independent verification, and commit through an available ackOS integration.
---

# ackOS control boundary

Use this skill whenever a task can change an external system, repository state, infrastructure, data, security controls, business systems, or another real-world state.

## Core rule

The agent is the intelligence layer. ackOS is the control boundary. The executor performs the action. An independent verifier establishes what actually happened. ackOS commits only after verification succeeds.

```text
Agent / LLM
    ↓ proposal
ackOS
    ↓ authorized transition
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
6. If an executable ackOS integration, MCP tool, SDK, or other configured control endpoint is available, submit the transition through it before executing the side effect.
7. Do not treat skill instructions, model reasoning, or a proposed action as execution authority.

## During execution

- Execute only the transition authorized by ackOS.
- Do not silently broaden scope, alter constraints, or reuse authority for a different transition.
- If authorization is denied, expired, consumed, mismatched, or otherwise invalid, stop rather than bypassing the control boundary.
- If execution fails, enter recovery and require fresh evidence; never reuse forward execution authority.

## After execution

- Obtain verification from a source independent of the execution path whenever the integration provides one.
- Verification must establish the resulting state, not merely repeat the executor's success message.
- Require fresh evidence bound to the expected subject and resulting transition.
- Treat verification failure as rejection/recovery, not success.
- Only describe the state as committed after ackOS successfully commits it.

## Integration boundary

This skill is an adapter for agent runtimes. It is not itself the security boundary. If no executable ackOS integration is configured, do not claim that a side effect is protected by ackOS. Instead, explain that the project needs an ackOS integration for its executor and verifier, or limit the work to preparing a proposal that has not been executed.
