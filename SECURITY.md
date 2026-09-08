# Security Policy

## Scope

ackOS V0 is a research-derived kernel and is **not presented as production-ready autonomous infrastructure control software**.

The project is nevertheless intended to be developed with explicit security and safety boundaries. Reports that demonstrate authority bypass, replay, stale-evidence acceptance, incorrect commitment, verification bypass, lifecycle violations, or other security-relevant behavior are valuable and should be reported responsibly.

## Reporting a vulnerability

For a security-sensitive vulnerability, please use GitHub's private vulnerability reporting mechanism for this repository when available rather than opening a public issue with exploit details.

When reporting, include:

- a concise description of the issue;
- the affected commit or version;
- a minimal reproduction or proof of concept where safe to provide;
- the expected safety boundary; and
- the observed behavior and potential impact.

Please do not include credentials, private infrastructure details, or other sensitive information in a report.

## Research boundary

ackOS V0 intentionally does not claim durable restart-safe authority consumption, crash-safe persistence ordering, distributed coordination, provider-specific correctness, or universal correctness. A behavior that occurs outside those documented assumptions should be clearly distinguished from a defect in the V0 boundary itself.

The project's public release documentation explains the research and testing history while explicitly inviting independent testing beyond the cases already covered.
