# Agent integrations

ackOS is designed to sit underneath the AI agent you already use. The agent decides what should happen; ackOS provides the control boundary around execution, independent verification, and commitment.

```text
Your agent / LLM
       ↓
     ackOS
       ↓
 executor / API
       ↓
 independent verifier
       ↓
 ackOS commit / reject
```

## Claude Code

Install the plugin from this repository using Claude Code's plugin installation flow, then use the `/ackos:control` skill for side-effecting work.

The repository contains:

- `.claude-plugin/plugin.json` — plugin manifest
- `skills/control/SKILL.md` — Claude-facing control skill

The skill is guidance, not the enforcement boundary. For actual protection, connect Claude Code to an executable ackOS integration for the executor and verifier.

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

## ChatGPT

ChatGPT uses **Apps/MCP** for executable integrations rather than the old-style ChatGPT plugin model. A custom app can expose approved MCP tools that ChatGPT can call.

The repository includes a ChatGPT-facing skill bundle at:

```text
integrations/chatgpt/SKILL.md
```

For an executable ChatGPT integration, expose ackOS through a remote MCP server/app. The app should keep the same boundary as the kernel: proposal → authorization → execution → independent verification → commit/reject.

A skill by itself does not enforce the boundary. Do not claim that a ChatGPT skill protects a side effect unless an executable ackOS integration is actually connected.

## What installation gives you

Installing a skill or extension does **not** turn the agent into a magically protected executor. It gives the agent the correct workflow and makes ackOS easy to adopt.

The actual security boundary remains the ackOS kernel. The next integration layer is an MCP/service adapter that exposes that kernel to agents while preserving its authority, verification, recovery, and commitment semantics.
