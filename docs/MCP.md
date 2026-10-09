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

The provider-backed constructor, `NewServerWithProvider(runtime, provider)`, builds the shared provider adapters for each control lifecycle. The standalone `cmd/ackos-mcp` binary keeps the synthetic demo as its default, and supports a configured Git provider when all of `--repo`, `--branch`, and `--path` are supplied. The branch must use a full ref such as `refs/heads/main`; the path is one existing repository-relative file.

### Execution errors

A provider-backed execution failure is returned as an MCP tool result with `isError: true`. The human-readable failure remains in the text `content` so clients that ignore metadata still receive the reason. Machine-readable classification is carried in the namespaced `_meta["io.github.jaredt87/ackos/error"]` object:

- `code: "stale_observation"` — the authorized pre-execution observation was stale.
- `code: "execution_failed"` — execution failed for another reason, including provider failure, mismatched execution ID, timeout, or cancellation.

The metadata also carries the same human-readable `message` supplied in the text content. Error results do not use `structuredContent`; successful `ackos_control` calls continue to advertise and return the existing `ControlResponse` output schema.

Recovery failures remain lifecycle errors rather than execution classifications. If a later recovery request fails, its recovery error is returned directly and is not relabeled as `stale_observation`.


### Standalone modes

With no Git flags, `cmd/ackos-mcp` uses the synthetic demo resource `demo-resource`.

For Git mode, configure all three flags:

```bash
go run ./cmd/ackos-mcp \
  --repo /absolute/path/to/repository \
  --branch refs/heads/target-branch \
  --path path/inside/repository.txt
```

The MCP `subject` must exactly equal the configured repository-relative `--path`. Requests for any other subject are rejected. The target must already exist as a regular file at startup; missing branch/path or unreadable blob fails startup rather than creating a file on first write. Use a normal, non-bare repository. Keep the configured target branch different from the branch currently checked out: the current typed SDK does not expose a reliable symbolic-HEAD query, so V0 documents this as an operator requirement rather than adding raw Git commands. The provider updates only the configured ref and does not modify the worktree or index.

### Git state and root drift

Git desired and observed states use the same encoding: `git-blob:v1:<blob-object-id>`. To create a desired blob without changing the working tree, write it into the repository's object database:

```bash
printf 'desired file contents\n' | git -C /absolute/path/to/repository hash-object -w --stdin
```

Use the resulting object ID after `git-blob:v1:` as `desired_state`. For the exact sample bytes above, a real `git hash-object --stdin` run printed `837cb4a8a4b9184863f237ce9e3ee318ce357fb3`, so the full desired state is `git-blob:v1:837cb4a8a4b9184863f237ce9e3ee318ce357fb3`. The object must exist and be a blob. In V0 the server obtains its own fresh observation; caller-supplied `observed_state` is ignored, and caller-held observations are not checked.

The kernel root is a single content-state string seeded from the configured file's actual blob at startup. An out-of-band target-content edit makes the observed state differ from that root. The next call fails at the kernel compare-and-swap before provider-level lineage checks, and the running process stays wedged until restart. The error explains that the repository changed outside ackOS and that restart is required to re-baseline.

**Restart means “trust the repository as it is now.”** Startup re-seeds the root from the current target blob without an authorization step, intentionally adopting whatever target content is present then. Review that content before restarting; restart is a trust decision, not neutral recovery. A commit that moves the branch tip without changing the target blob changes lineage Version but not State, and should not wedge the runtime. If a ref update succeeds but independent verification fails, Git may be ahead of the kernel root and restart/re-baseline is required.

### Manual-session test mapping

- A normal successful transition is suitable for the first manual test.
- Editing the target file behind ackOS before invoking the tool tests **kernel root-drift rejection** and the restart/re-baseline procedure. It does not test provider stale/ABA protection.
- Provider stale and ABA rejection require a test wrapper that mutates the branch after Observe but before Execute in the same call. Integration tests cover this race window and A→B→A.
- The provider explicitly rejects a no-op request rather than writing an empty commit; the kernel may reject an equal-state proposal even earlier.

## Transports

`integrations/mcp.Server` supports:

- stdio for local MCP clients such as Claude Code, Gemini, and other local agents;
- Streamable HTTP for remote MCP clients.

The standalone `cmd/ackos-mcp` binary uses an in-memory synthetic demo unless Git mode is configured. Git mode is an initial single-file V0 integration, not a general-purpose or multi-resource production adapter.

For real use, embed the integration and provide an observer that obtains the authorization observation from the target system, an executor that performs the actual side effect, an independent verifier that obtains fresh evidence from the target system, and an independent recovery observer that obtains provider-captured post-failure evidence.

## Security boundary

The MCP protocol is transport, not authority. The kernel remains authoritative for governance, single-use authority, execution, independent verification, recovery, and CAS commitment.

Verification is run independently of MCP request cancellation after execution has completed, so a disconnected caller cannot strand the shared V0 runtime in `STARTED`.

The V0 HTTP server does not provide authentication, authorization for arbitrary callers, durable state, or distributed coordination. Do not expose the demo server directly to an untrusted network.
