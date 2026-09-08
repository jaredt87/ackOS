---
name: ackos-control
description: Use ackOS as the control boundary for side-effecting agent work. Preserve intent and constraints, require executable authorization and executor dispatch, independently verify the resulting state, and commit only after verification.
---

# ackOS control boundary

When a task can change an external system, treat the model as the intelligence layer and ackOS as the control boundary.

```text
ChatGPT / Agent
      ↓ proposal
ackOS integration
      ↓ authority + executor dispatch
 Executor / API
      ↓ action
Independent verifier
      ↓ evidence
    ackOS
      ↓
 COMMIT / REJECT
```

1. Observe and identify the subject.
2. Preserve the requested outcome, constraints, and prohibited actions.
3. Form the smallest transition that satisfies the intent.
4. Before any side effect, require an executable ackOS integration that owns the executor dispatch and provides an independent verifier.
5. Do not treat this skill, model reasoning, or a governance/reservation response as execution authority.
6. Execute only through the ackOS integration that owns the authorized executor call.
7. Never reuse expired, consumed, denied, or mismatched authority.
8. Independently verify the real resulting state using the integration's independent verifier.
9. Reject/recover if verification fails.
10. Commit only after ackOS accepts the verified result.

If an executable ackOS integration does not control both the executor and independent verifier, fail closed: prepare a proposal only and do not perform the side effect directly.

This skill is model-facing guidance, not the security boundary. A ChatGPT skill without an executable ackOS app/MCP integration cannot enforce these rules by itself.
