# Gemini CLI extension

ackOS can be installed as a Gemini CLI extension. Gemini CLI extensions can bundle skills, context, MCP servers, commands, hooks, and other agent capabilities.

This repository provides:

```text
gemini-extension.json
GEMINI.md
skills/
  ackos-control/
    SKILL.md
```

## Install

```bash
gemini extensions install https://github.com/jaredt87/ackOS
```

Restart the CLI session, then inspect the extension and discovered skills:

```text
/extensions list
/skills list
```

## Local development

```bash
gemini extensions link /path/to/ackOS
gemini extensions validate /path/to/ackOS
```

## Boundary

The extension teaches Gemini how to route side effects through ackOS. It does not itself enforce authorization. For actual protection, connect Gemini to an executable ackOS integration for the executor and independent verifier.
