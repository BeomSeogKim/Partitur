# Partitur — Agent & Contributor Harness

Partitur orchestrates a fleet of AI coding agents (Claude Code, Codex, Gemini, Kimi, …).
It is **vendor-neutral by design**: no rule, tool, or document here may assume a single AI vendor.

This file is the **canonical** guidance for every agent and human contributor. Vendor-specific
entrypoints (`CLAUDE.md`, `.codex/**`, …) must only point here — never fork or restate the rules
beyond a short set of tool-specific overrides.

## Documentation language

- All project documentation defaults to **English**.
- AI/agent configuration docs — `AGENTS.md`, `CLAUDE.md`, `.claude/**`, `.codex/**` — are
  **English only**. No Korean in these files.
- Domain or concept material that needs Korean lives as a **mapped translation**, never mixed
  into an English document:
  - English canonical: [`docs/CONCEPT.md`](docs/CONCEPT.md)
  - Korean mapping: [`docs/ko/CONCEPT.md`](docs/ko/CONCEPT.md)
- A Korean-audience project description (a reader-facing overview) is allowed as its own file,
  kept in sync with its English canonical.

## Version control

- Every change is tracked in git. No out-of-band edits.
- Branch from the latest `main`, then open a PR. Do **not** commit directly to `main`.
- **One concern per commit and per PR.** If a PR carries several unrelated changes, the scope was
  drawn wrong — split it.
- Commit messages and PR descriptions state the core change and its intent, nothing incidental.

## Privacy — this repository will be open-sourced

- No secrets, tokens, credentials, or personal identifiers may enter git history — including in
  commit messages and PR bodies.
- **Commit trailers:** a vendor-neutral `Co-Authored-By:` line is acceptable. Do **not** include
  session URLs, machine paths, or any per-user/per-session identifier.
- A local pre-commit hook scans staged changes for common secret shapes. Enable it once per clone:
  ```sh
  git config core.hooksPath .githooks
  ```
  Add personal patterns (e.g. private emails) to a git-ignored `.githooks/deny.local`, one regex
  per line. Never commit that file.

## Vendor neutrality

- Agents are interchangeable performers; the **score** is the single source of truth
  (see [`docs/CONCEPT.md`](docs/CONCEPT.md)).
- Never hard-code assumptions that tie the orchestration layer to one vendor's API, auth flow, or
  model naming. Any vendor must be able to enter as just another part.
