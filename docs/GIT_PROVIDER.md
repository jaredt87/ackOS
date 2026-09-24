# Git Provider

The Git provider reconciles a single file in a local Git repository.

## Configuration contract

- `Repository` must identify the actual Git repository root. A subdirectory of a repository is rejected.
- `Branch` identifies the local branch whose tip is reconciled.
- Transition subjects are repository-relative, normalized paths. They must not escape the repository.
- `Transition.Before` and `Transition.After` are existing Git blob IDs. The provider verifies the current target against `Before` and materializes `After` before building the resulting tree.
- The provider creates execution commits with the fixed identity `ackOS Git Provider <ackos@localhost>`; it does not depend on repository or global Git user configuration.

The provider verifies the exact commit produced by execution and confirms the branch tip remains unchanged through verification before returning verification evidence.

## Scope

The provider operates on an existing local Git repository and branch. It does not configure Git remotes, credentials, or repository-wide user identity.
