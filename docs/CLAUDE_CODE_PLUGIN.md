# Claude Code plugin

ackOS can be distributed as a Claude Code plugin so an agent can use the ackOS control lifecycle without replacing the agent, model, or existing workflow.

The plugin follows Claude Code's standard plugin layout:

```text
.claude-plugin/
  plugin.json
skills/
  control/
    SKILL.md
```

Claude Code namespaces the skill as `/ackos:control` and can also invoke it automatically when a task involves side effects. Claude Code supports project, personal, and marketplace-distributed plugins; plugins are intended for reusable, versioned distribution. See the [Claude Code plugin documentation](https://code.claude.com/docs/en/plugins).

## Try it locally

From a checkout of ackOS:

```bash
claude --plugin-dir .
```

Then invoke:

```text
/ackos:control
```

Or ask Claude to perform a task that changes an external system and let the skill load automatically.

## What the plugin does

The skill teaches the agent to treat the control boundary as:

```text
Agent / LLM
    ↓
ackOS
    ↓
Executor / API
    ↓
Independent verification
    ↓
ackOS
    ↓
Commit / Reject
```

It preserves the distinction between **intelligence and authority**. The agent may propose what should happen, but the action is not considered authorized merely because the model decided to perform it.

## Important V0 boundary

The plugin is an **adapter and agent-facing workflow**, not a replacement for the ackOS kernel. A `SKILL.md` file is model-facing instruction and cannot by itself prevent an agent from bypassing its instructions.

For a real control boundary, the environment must provide an executable ackOS integration for the relevant executor and verifier. Until such an integration is configured, the skill must not claim that a side effect is protected by ackOS.

This separation is intentional:

- **Skill/plugin:** makes ackOS easy for agents to discover and use.
- **Kernel:** implements the researched authorization, verification, recovery, and commitment lifecycle.
- **Executor:** performs the actual external action.
- **Independent verifier:** establishes the resulting state.

The next integration layers can expose the same kernel through MCP, a service/API, or a purpose-built executor adapter without changing the kernel's safety boundary.

## Why this exists

The plugin is a direct response to the first public usability feedback: people already use LLM agents to perform work, so ackOS should not ask them to replace those agents. The intended adoption path is to keep the existing intelligence layer and place ackOS at the point where an action crosses into a real system.
