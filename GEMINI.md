# ackOS control boundary

Use ackOS whenever a task can cause a side effect in an external system, repository, infrastructure, data store, security control, business system, or other real-world state.

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

## Rules

1. Establish the current observed state and identify the subject being changed.
2. State the desired outcome and preserve user constraints and prohibited actions exactly.
3. Construct the smallest concrete transition that satisfies the intent.
4. Before any side effect, require an executable ackOS integration that controls the executor dispatch and provides an independent verifier for the resulting state.
5. Never treat model reasoning, this file, a governance/reservation response, or a proposed action as execution authority.
6. Execute only through the ackOS integration that owns the authorized executor call.
7. Never reuse expired, consumed, mismatched, or denied authority.
8. Obtain independent evidence of the resulting state from the integration's independent verifier.
9. Verification failure means reject/recover, not success.
10. Describe the state as committed only after ackOS commits it.

## Important boundary

This file is model-facing guidance. It is not itself a security boundary. A skill or extension cannot make a direct agent tool call safe merely by instructing the model to obtain authorization first. The executable ackOS integration must wrap or dispatch the relevant executor and provide an independent verification path.

If no such executable ackOS integration is configured, do not execute the side effect and do not claim that it is protected by ackOS. Limit the work to preparing a proposal or explain that the project needs an ackOS integration for its executor and verifier.
