### AGENTS.md

### 1. Project Context

* **Core Purpose:** ackOS is a small, provider-neutral Go kernel that governs the boundary between observed state, governed intent, execution, independent verification, and authoritative commitment (the Observe → Normalize → Reconcile → Govern → Reserve → Start → Verify → Commit lifecycle).
* **Target Audience:** Capable software engineers working on a safety-critical control kernel. Prioritize explicit, provable lifecycle logic over implicit "magic" — every phase transition must be traceable and evidence-backed.
* **Scope note:** This file governs agents *developing this repository*. It is unrelated to `GEMINI.md` and `skills/*/SKILL.md`, which are runtime instructions for agents *using* an ackOS integration to gate their own side effects. Do not conflate the two.

### 2. Environment & Commands

* Go 1.25. Run these exact commands to verify changes — they mirror `.github/workflows/ci.yml` exactly. Do not substitute tools or omit flags.
* **Format check:** `test -z "$(gofmt -l .)"`
* **Vet:** `go vet ./...`
* **Unit Testing:** `go test ./...`
* **Race Testing:** `go test -race ./...` (required — the kernel's atomicity/CAS guarantees are meaningless if this doesn't pass)
* **Agent-integration manifest check:** if you touch `.claude-plugin/plugin.json`, `gemini-extension.json`, or any `SKILL.md`, re-run the "Validate agent integrations" block in CI locally; it validates every tracked `SKILL.md` plus the required JSON and `description:` frontmatter checks before proposing a PR.

### 3. Behavioral Boundaries

### Always Do

* Run `gofmt`, `go vet ./...`, `go test ./...`, and `go test -race ./...` after any change to `kernel/`, `integrations/`, or `cmd/`.
* Keep the kernel provider-neutral: nothing under `kernel/` may import a specific cloud/provider SDK or contain provider-specific mutation logic (see "Kernel does not own" in `docs/ARCHITECTURE.md`).
* Preserve the lifecycle invariants documented in `docs/ARCHITECTURE.md`: post-execution verification evidence must postdate recorded execution completion; observations are rejected while an execution is in flight; only one verification callback may be active per execution attempt; recovery returns to Observe and never reuses forward execution authority.
* When changing `kernel/kernel.go`, check `kernel/kernel_test.go` and `kernel/review_regression_test.go` for the specific bypass/replay/stale-evidence scenarios they pin down, and add a regression test for any new invariant.
* Update the relevant doc (`docs/ARCHITECTURE.md`, `docs/MCP.md`, `docs/AGENT_INSTALL.md`, etc.) in the same PR as any behavioral change it describes.

### Ask First

* Do not add new external dependencies to `go.mod` beyond the existing MCP SDK stack without explicit authorization.
* Do not introduce schedulers, distributed coordination, durable authority storage, or other V0-out-of-scope mechanisms (see `docs/ARCHITECTURE.md`, "V0 guarantees and boundaries") without discussing scope first.
* Refrain from refactoring code outside the specific scope of the current task, especially inside `kernel/`.

### Never Do

* Never collapse authorization, execution dispatch, verification, and commitment into a single step, in the kernel or in any integration (`integrations/synthetic`, `integrations/mcp`, `integrations/chatgpt`) — this is the exact boundary-collapse `GEMINI.md` and `SECURITY.md` warn against.
* Never let an execution result alone (provider "success") authorize a commit — commitment requires independently captured, freshness-checked verification evidence.
* Never weaken or bypass the CAS-oriented commit path or single-use authority consumption to "simplify" a fix.
* Never hardcode or commit credentials, tokens, or provider secrets (relevant where integrations handle external credentials).
* Do not rewrite history or force-push to `main`.

### 4. Architecture & Style Pointers

* **Kernel purity:** `kernel/` is the authority boundary — deterministic, in-memory, single-process for V0. It owns observation/evidence identity, normalization, reconciliation, governance, reservation, verification gating, and CAS commitment. It does not own provider SDKs, credentials, RBAC, or orchestration. See `docs/ARCHITECTURE.md` for the full "owns / does not own" split.
* **Providers/integrations:** `integrations/synthetic` and `integrations/mcp` implement executable integration behavior against concrete backends; they call into the kernel rather than duplicating its decisions. `integrations/chatgpt` is a model-facing skill bundle, not an executable backend. New providers follow the executable integration pattern.
* **Error handling:** use the existing sentinel errors in `kernel/kernel.go` (`ErrStaleEvidence`, `ErrGovernanceDenied`, `ErrAuthorityExpired`, etc.) rather than introducing ad hoc error strings; wrap with `fmt.Errorf("...: %w", err)` to preserve the sentinel.
* **Living documentation:** read `docs/ARCHITECTURE.md` and `docs/RESEARCH_BOUNDARIES.md` before guessing at intended kernel behavior — this project came out of a research program (E11–E17) with specific, deliberate non-guarantees; don't "fix" a documented boundary without flagging it.

### 5. Git & PR Workflow

* **Branch Naming:** `feature/` or `fix/` followed by a concise hyphenated description.
* **Commit Style:** Semantic commits (`feat:`, `fix:`, `docs:`, `test:`).
* **PR Descriptions:** State the problem solved, the changes made, which lifecycle/invariant guarantees (if any) are affected, and paste the exact verification commands run (`gofmt`, `go vet`, `go test`, `go test -race`).


### 6. Pilot use and deployment guidance

This repository may be used by developers and agents setting up or operating an ackOS pilot. Read the repository README, `docs/ARCHITECTURE.md`, `docs/AGENT_INSTALL.md`, and relevant integration documentation before configuring or changing the system.

- The repository is licensed under Apache License 2.0. Review `LICENSE` and `LICENSING.md`; these instructions do not add to or alter the license.
- For a pilot, use a clearly designated test resource and explicitly configured provider and permissions. Do not assume a repository, resource, or operation is authorized merely because it is accessible.
- Preserve ackOS as the authority for authorization, execution lifecycle, verification, recovery, and commitment. Do not create a parallel or synthetic execution path or bypass the kernel/provider boundary.
- Do not weaken authorization, verification, evidence freshness, single-use authority, CAS commitment, or documented safety boundaries to make a pilot easier to configure.
- Keep secrets out of source control, logs, prompts, documentation examples, and test artifacts. Use local environment configuration or a suitable secrets manager.
- Do not perform real cloud provisioning, Terraform apply, production mutations, pushes, merges, or other consequential operations unless the pilot owner explicitly authorizes that exact scope and the documented execution path supports it.
- Prefer the documented quickstart and existing supported integration. If a required capability, configuration, or boundary is unclear or missing, stop and explain the gap instead of inventing a workaround.
- Report pilot results accurately, distinguishing verified and committed outcomes from attempted, partial, blocked, or unverified operations.
