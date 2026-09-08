---
name: ackos-control
description: Use ackOS as the control boundary for side-effecting agent work. Preserve intent and constraints, obtain execution authority, execute only the authorized transition, independently verify the resulting state, and commit only after verification.
---

# ackOS control boundary

When a task can change an external system, treat the model as the intelligence layer and ackOS as the control boundary.

```text
ChatGPT / Agent
      ↓ proposal
    ackOS
      ↓ authority
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
4. Submit it to an executable ackOS integration when one is configured.
5. Do not treat this skill or model reasoning as execution authority.
6. Execute only an ackOS-authorized transition.
7. Never reuse expired, consumed, denied, or mismatched authority.
8. Independently verify the real resulting state.
9. Reject/recover if verification fails.
10. Commit only after ackOS accepts the verified result.

This skill is model-facing guidance, not the security boundary. A ChatGPT skill without an executable ackOS app/MCP integration cannot enforce these rules by itself.
