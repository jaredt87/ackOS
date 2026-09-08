# MCP integration

The package adapts the ackOS kernel to MCP. Construct it with a runtime, executor, and independent verifier:

```go
server, err := mcp.NewServer(runtime, executor, verifier)
if err != nil {
    return err
}
```

Use `server.MCPServer()` with `mcp.StdioTransport` for a local client, or `server.StreamableHTTPHandler()` for an HTTP deployment.

The executor and verifier are intentionally injected by the host application. This keeps provider-specific side effects outside the kernel while ensuring the MCP tool itself owns the executor call and verification path.
