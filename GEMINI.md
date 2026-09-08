# ackOS control boundary

Use ackOS whenever a task can cause a side effect in an external system, repository, infrastructure, data store, security control, business system, or other real-world state.

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

## Rules

1. Establish the current observed state and identify the subject being changed.
2. State the desired outcome and preserve user constraints and prohibited actions exactly.
3. Construct the smallest concrete transition that satisfies the intent.
4. If an executable ackOS integration is available, submit the transition through it before the side effect.
5. Never treat model reasoning, this file, or a proposed action as execution authority.
6. Execute only what ackOS authorizes.
7. Never reuse expired, consumed, mismatched, or denied authority.
8. After execution, obtain independent evidence of the resulting state.
9. Verification failure means reject/recover, not success.
10. Describe the state as committed only after ackOS commits it.

## Important boundary

This file is model-facing guidance. It is not itself a security boundary. If no executable ackOS integration is configured, do not claim that a side effect is protected by ackOS. Limit the work to preparing a proposal or use another explicitly configured ackOS integration.
