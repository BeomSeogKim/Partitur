# Claude Code — project entrypoint

Partitur's agent guidance is **vendor-neutral** and lives in [`AGENTS.md`](AGENTS.md).
Read `AGENTS.md` first and follow it. This file only restates Claude-specific overrides.

## Overrides

- **Commit trailers:** do **not** add a `Claude-Session:` URL — or any per-session / per-user
  identifier — to commit messages. This repository is destined for open source; keep history free
  of personal data. A vendor-neutral `Co-Authored-By:` line is acceptable.
- **Git writes:** branch from the latest `main` and open a PR; never commit to `main` directly.
  One concern per commit and per PR.
- **Language:** all AI config docs (`CLAUDE.md`, `.claude/**`, `AGENTS.md`, `.codex/**`) are
  English only. Korean belongs in mapped domain docs under `docs/ko/`.
