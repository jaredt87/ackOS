# Agent integrations

ackOS is designed to sit underneath the AI agent you already use. The agent decides what should happen; ackOS provides the control boundary around execution, independent verification, and commitment.

```text
Your agent / LLM
       ↓
 ackOS integration
       ↓
 executor / API
       ↓
 independent verifier
       ↓
 ackOS commit / reject
```

For a side effect to be protected by ackOS, the executable integration must control the executor dispatch and provide an independent verifier. A model-facing skill or extension alone cannot enforce that boundary.

## Claude Code

Install the plugin from this repository using Claude Code's plugin installation flow, then use the `/ackos:control` skill for side-effecting work.

The repository contains:

- `.claude-plugin/plugin.json` — plugin manifest
- `skills/control/SKILL.md` — Claude-facing control skill

The skill is guidance, not the enforcement boundary. For actual protection, connect Claude Code to an executable ackOS integration that owns the executor call and verifier. If that integration is not present, the skill should limit the work to preparing a proposal rather than executing the side effect directly.

## Gemini CLI

Gemini CLI supports extensions that can bundle skills, MCP servers, custom commands, hooks, and other agent capabilities. Install this repository as an extension:

```bash
gemini extensions install https://github.com/jaredt87/ackOS
```

Then restart the Gemini CLI session and verify the extension/skill is available:

```text
/extensions list
/skills list
```

The repository provides `gemini-extension.json`, `GEMINI.md`, and the shared `skills/ackos-control/SKILL.md`.

For local development:

```bash
gemini extensions link /path/to/ackOS
gemini extensions validate /path/to/ackOS
```

The extension provides model-facing guidance. It does not protect a side effect unless an executable ackOS integration owns the executor and independent verifier.

## ChatGPT

ChatGPT uses **Apps/MCP** for executable integrations rather than the old-style ChatGPT plugin model. A custom app can expose approved MCP tools that ChatGPT can call.

The repository includes a ChatGPT-facing skill bundle at:

```text
integrations/chatgpt/SKILL.md
```

For an executable ChatGPT integration, expose ackOS through a remote MCP server/app. The app should keep the same boundary as the kernel: proposal → authorization → executor dispatch → independent verification → commit/reject.

A skill by itself does not enforce the boundary. If the executable app/MCP integration does not own both the executor and independent verifier, do not perform the side effect directly.

## What installation gives you

Installing a skill or extension does **not** turn the agent into a magically protected executor. It gives the agent the correct workflow and makes ackOS easy to adopt.

The actual security boundary remains the ackOS kernel plus an executable integration that preserves its authority, verification, recovery, and commitment semantics. The next integration layer is an MCP/service adapter that exposes that kernel to agents without moving execution outside the boundary.
