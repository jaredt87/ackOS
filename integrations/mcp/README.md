# MCP integration

The package adapts the ackOS kernel to MCP. Construct it with a runtime, executor, independent verifier, and independent recovery observer:

```go
server, err := mcp.NewServer(runtime, executor, verifier, recoveryObserver)
if err != nil {
    return err
}
```

Use `server.MCPServer()` with `mcp.StdioTransport` for a local client, or `server.StreamableHTTPHandler()` for an HTTP deployment.

The executor and verifier are intentionally injected by the host application. This keeps provider-specific side effects outside the kernel while ensuring the MCP tool itself owns the executor call and verification path.

After an execution or verification failure, recovery evidence is obtained from the independent recovery observer with its real provider capture timestamp. The MCP caller's `observed_state` is never promoted into post-failure evidence. If provider evidence reports a state that differs from the kernel's committed root, recovery remains fail-closed rather than guessing whether the previous side effect succeeded.

The V0 adapter serializes complete control lifecycles because the runtime is a single mutable state machine. Verification is run with a context that survives MCP transport cancellation so a disconnected caller cannot strand the runtime in `STARTED`.
