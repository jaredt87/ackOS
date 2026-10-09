# Real Repository Smoke Test (V0)

This is a manual operator drill for the first real-repository session after the Git provider is merged. Use a **disposable, non-bare repository** and a target branch that is not checked out in the worktree. Do not run this against a repository whose current branch or target contents you cannot afford to leave unchanged.

## Setup

1. Create a disposable repository with an initial commit and a regular target file.
2. Keep a different branch checked out (for example, check out `worktree` while targeting `refs/heads/target`).
3. Record the target branch tip, the target blob ID, the checked-out branch, and the target file's worktree contents.
4. Start the server:

   ```bash
   go run ./cmd/ackos-mcp \
     --repo /absolute/path/to/disposable-repo \
     --branch refs/heads/target \
     --path nested/target.txt
   ```

   The configured path must already exist as a regular file in the target branch. The MCP subject must exactly equal `nested/target.txt`.

## Test 1 — One authorized file update (manual)

1. Create a desired blob without touching the worktree:

   ```bash
   printf 'desired file contents\n' | git -C /absolute/path/to/disposable-repo hash-object -w --stdin
   ```

2. Copy the exact object ID printed by Git. The `desired_state` value is `git-blob:v1:<printed-object-id>`; do not type or invent a different object ID. For these exact sample bytes, a recorded `git hash-object --stdin` run printed `837cb4a8a4b9184863f237ce9e3ee318ce357fb3`, so the literal state is `git-blob:v1:837cb4a8a4b9184863f237ce9e3ee318ce357fb3`.
3. Call `ackos_control` with:
   - `subject`: `nested/target.txt`
   - `desired_state`: the versioned value above
   - `observed_state`: any placeholder (V0 obtains its own observation and ignores this input)
   - a valid positive authority TTL
4. Confirm the call succeeds and returns a committed result.
5. Confirm `refs/heads/target` moved to a new commit whose parent is the previously recorded tip.
6. Confirm the new commit changes only `nested/target.txt`, with the target blob equal to the ID printed by `hash-object`.
7. Confirm the checked-out branch is still `refs/heads/worktree` and the worktree file and index are unchanged.
8. Save the old/new commit IDs, desired blob ID, and control response as the smoke-test record.

## Test 2 — Out-of-band target edit and explicit restart/re-baseline

1. Stop the server after recording its startup root.
2. Create a new target blob and an out-of-band commit on `refs/heads/target` that changes `nested/target.txt`.
3. Restart the server with the same three flags. Startup deliberately trusts the current repository target blob and seeds a new in-memory root without authorization. Inspect the repository before doing this; restart is an explicit trust decision.
4. Call `ackos_control` to request another valid target blob. Confirm the transition succeeds.
5. Confirm the ref-only behavior and working-tree/index isolation still hold.

## Expected negative checks

- Changing the target content out-of-band while the original process is running makes the next control call fail with a readable root compare-and-swap conflict explaining that restart is required to re-baseline.
- A commit that changes only an unrelated path does not wedge the runtime.
- A no-op desired blob is rejected without creating a commit.
- A missing target at startup is rejected; the provider does not create a file on first write.
- Do not treat this smoke test as proof of race safety; the unit/integration tests inject provider races inside a single control call.
