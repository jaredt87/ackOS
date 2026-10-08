# ackOS MCP control plane

ackOS exposes an MCP server so an LLM or agent can call the control boundary as a tool instead of implementing the lifecycle itself.

```text
LLM / Agent
    |
    | MCP
    v
ackOS MCP integration
    |
    v
Observe -> Normalize -> Reconcile -> Govern -> Reserve
    |
    v
Executor owned by integration
    |
    v
Independent verifier
    |
    v
Verify -> CAS Commit / Recovery
```

## Tool

V0 exposes one high-level tool:

- `ackos_control` — accepts a subject, observed state, desired state, and optional authority lifetime, then runs the transition through the kernel.

The MCP adapter obtains the authorization observation from a configured provider observer, then owns the call to the configured `kernel.Executor` and requires a configured `kernel.Verifier`. A skill or LLM response cannot authorize a separate side-effecting call around the MCP server.

If execution or independent verification fails, the kernel enters `RECOVERY`. A later `ackos_control` call obtains post-failure evidence from an independent recovery observer. The caller's `observed_state` is not used as recovery evidence, and the failed transition's authority is never reused.

Recovery is deliberately fail-closed when provider evidence disagrees with the kernel's committed root. V0 does not guess whether an ambiguous external effect succeeded and does not silently adopt a provider state as a new committed root. That case requires an explicit higher-level reconciliation policy.

The adapter serializes complete control lifecycles because the V0 runtime is a single mutable state machine. This prevents concurrent MCP calls from invalidating each other's reserved authority.

The provider-backed constructor, `NewServerWithProvider(runtime, provider)`, builds the shared provider adapters for each control lifecycle. It is the integration path intended for a real resource provider; as of this PR it has no production caller. The shipped `cmd/ackos-mcp` binary still wires the synthetic Kernel executor, and the Git provider PR will be the first production wiring of this path.

### Execution errors

A provider-backed execution failure is returned as an MCP tool result with `isError: true`. The human-readable failure remains in the text `content` so clients that ignore metadata still receive the reason. Machine-readable classification is carried in the namespaced `_meta["io.github.jaredt87/ackos/error"]` object:

- `code: "stale_observation"` — the authorized pre-execution observation was stale.
- `code: "execution_failed"` — execution failed for another reason, including provider failure, mismatched execution ID, timeout, or cancellation.

The metadata also carries the same human-readable `message` supplied in the text content. Error results do not use `structuredContent`; successful `ackos_control` calls continue to advertise and return the existing `ControlResponse` output schema.

Recovery failures remain lifecycle errors rather than execution classifications. If a later recovery request fails, its recovery error is returned directly and is not relabeled as `stale_observation`.


### Standalone synthetic demo identity

The standalone `cmd/ackos-mcp` binary uses one synthetic resource whose stable subject identity is `demo-resource`. Calls to the standalone demo must use `subject: "demo-resource"`; this identity is intentionally fixed so the provider can exercise resource-substitution and identity enforcement. Real deployments must configure the MCP adapter with the subject identities accepted by their resource provider rather than relying on the synthetic demo identity.

## Transports

`integrations/mcp.Server` supports:

- stdio for local MCP clients such as Claude Code, Gemini, and other local agents;
- Streamable HTTP for remote MCP clients.

The standalone `cmd/ackos-mcp` binary uses in-memory demonstration executor, verifier, and recovery-observer adapters. It is intentionally a development/demo server, not a production infrastructure adapter.

For real use, embed the integration and provide an observer that obtains the authorization observation from the target system, an executor that performs the actual side effect, an independent verifier that obtains fresh evidence from the target system, and an independent recovery observer that obtains provider-captured post-failure evidence.

## Security boundary

The MCP protocol is transport, not authority. The kernel remains authoritative for governance, single-use authority, execution, independent verification, recovery, and CAS commitment.

Verification is run independently of MCP request cancellation after execution has completed, so a disconnected caller cannot strand the shared V0 runtime in `STARTED`.

The V0 HTTP server does not provide authentication, authorization for arbitrary callers, durable state, or distributed coordination. Do not expose the demo server directly to an untrusted network.
