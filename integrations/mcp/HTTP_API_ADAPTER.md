# Production MCP-to-HTTP adapter

The production entry point is separate from `cmd/ackos-mcp`, which remains the synthetic demo:

```sh
export ACKOS_API_URL='https://ackos-api.example.internal'
export ACKOS_API_BEARER_TOKEN='...'
go run ./cmd/ackos-mcp-api -api-timeout=10s
```

`ACKOS_API_URL` is required. The URL must use HTTPS; HTTP is accepted only for a loopback test server. `ACKOS_API_BEARER_TOKEN` is required. `-api-timeout` sets the maximum duration for each HTTP request and must be positive. The adapter sends the bearer token only to the configured API origin. Deploy behind trusted TLS and protect the token as a secret.

## Tool contract

The production server exposes `ackos_control` with these required arguments:

- `repository_id`: server-configured allowlisted repository ID.
- `branch`: branch to inspect.
- `path`: existing tracked regular-file path.
- `mode`: `100644` or `100755`.
- `content_base64`: canonical padded Base64 for the exact replacement bytes.

The adapter posts repository/branch/path to `POST /v1/observations`, validates the returned resource identity, and passes the returned opaque `observation_token` unchanged to `POST /v1/changes`. It never constructs fingerprints, observation tokens, or observed state. A change is reported successful only if ackos-api returns an execution ID and explicitly confirms both verification and commitment.

Any authentication error, stale observation, non-2xx response, malformed or mismatched response, timeout, or ambiguous mutation result is surfaced as a tool error. The adapter does not retry mutation requests: after an ambiguous result, callers must inspect again and independently determine the repository state before attempting another change.

## Authority boundary and limitations

This adapter is an HTTP client, not an execution engine. It does not instantiate a kernel runtime, Git provider, executor, verifier, or recovery observer. All execution authority remains in ackos-api. The API currently supports replacing an existing file in an allowlisted local checkout; it does not create/delete files, run Terraform, push to GitHub, or merge branches.

The synthetic `cmd/ackos-mcp` demo is unchanged and is not production-backed. This adapter's HTTP test-server tests establish client behavior only; production end-to-end validation still requires a configured ackos-api, a disposable repository, a real MCP invocation, and independent verification of the resulting commit and file contents.
