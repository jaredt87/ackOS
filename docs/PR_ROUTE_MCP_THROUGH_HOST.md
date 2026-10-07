# MCP-to-Host Routing Audit

This planning note records the pre-implementation contract for routing the one-shot MCP control tool through control.Host. The PR description is the canonical review summary; this file exists only because GitHub cannot create an empty pull request.

## Boundary shape

Host will gain a construction option for an independent recovery observer. When configured, recovery uses it; when unset, Host keeps using the registered provider's Observe. MCP will configure its existing independent recovery observer through Host and call Host.Control. No new Kernel orchestration API is planned.

## Required parity checks

Before implementation, compare Host and MCP for:

- caller supplied observed state versus fresh provider observation;
- authority TTL validation/conversion;
- reservation and serialization;
- timeouts;
- pre-execution observation context.

The accepted #19 MCP pre-execution observation behavior using context.WithoutCancel is a routing gate and must not change silently.

## Recovery gate

Existing MCP recovery tests prove recovery is exercised, but the fixture currently mirrors normal observer answers into the recovery observer. Add a characterization test where the two observers return different answers and recovery requires the independent observer.

## Synthetic split

Porting synthetic to control.Provider and routing MCP through Host are separable. Prefer synthetic port first if the diff is not trivial; otherwise keep the conceptual split visible in one PR. The shipped MCP binary currently wires synthetic Kernel Executor/Verifier adapters directly.

## Stop line

Host routing -> typed-error boundary -> Git provider -> REAL REPO -> STOP.
