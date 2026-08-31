# Partitur

> The full score for a room full of AI coding agents.

**Partitur** (German: *full score*) is an orchestration layer that sits **above** individual
AI coding agents — Claude Code, Codex, Gemini, Kimi, and others — and coordinates them by role.

In an orchestra, only the conductor reads the *Partitur*: the single document where every
instrument's part is stacked on one page. Each player sees only their own line. Partitur is that
role for a fleet of agents — it holds the whole picture, casts each agent into a part, cues their
entrances, and keeps them in time.

```
                        ┌─────────────────────────┐
                        │        Partitur         │   ← holds the score, casts the parts
                        └────────────┬────────────┘
              ┌──────────────────────┼──────────────────────┐
        ┌───────────┐          ┌───────────┐          ┌───────────┐
        │ Claude Code│          │Gemini, Kimi│          │   Codex   │
        └───────────┘          └───────────┘          └───────────┘
             a part                 a part                 a part
```

## Why

Agents are getting good at *playing*. Nobody is *conducting*. Partitur is the layer that:

- **casts** — binds each part to the performer the user judges most suitable
- **cues** — dispatches work and triggers entrances at the right moment
- **keeps time** — coordinates concurrency and hand-offs between parts
- **holds the score** — owns the single, declarative plan every part derives from

## Quickstart

Partitur is four binaries. The core resolves the adapters and the trampoline from `PATH`, so install
all four:

```bash
go install ./cmd/partitur ./cmd/partitur-adapter-codex ./cmd/partitur-adapter-claude ./cmd/partitur-trampoline
export PATH="$(go env GOPATH)/bin:$PATH"
```

An adapter is a thin shim, not the agent. Whichever performer a cast selects, that vendor's own CLI
has to be installed and on `PATH` as well — the adapter's probe resolves and runs it. `validate`
exits 3 either way, and the `adapter-environment` diagnostic distinguishes them: `executable_absent`
means the adapter binary is missing, while a missing vendor CLI surfaces as `error_response` naming
what the adapter could not resolve. Install the Codex CLI to follow this page as written.

Scaffold a repository:

```bash
partitur init
```

That writes `partitur.yaml` — a draft score with one interview movement — and `.partitur/`. It
deliberately writes no cast: a cast binds parts to the agents *you* choose, and the tool does not
choose for you. Write `.partitur/cast.yaml`:

```yaml
cast: "0.1"
performers:
  codex:
    adapter: codex
    model: gpt-5.6-sol
    allow_advisory_enforcement: true
bindings:
  interview:
    performer: codex
```

`allow_advisory_enforcement: true` is required today, and it concedes something real. The
withheld-authority table in [`docs/DESIGN.md`](docs/DESIGN.md) §4 demands `read_grants` when
`repo_read` is withheld and `path_grants` when it is granted, and no shipped adapter reports either
— reads cannot be confined to granted paths, and closing `shell_tool` still leaves another execution
route open. So the adapters report those dimensions as unmet, honestly, and the flag turns a
fail-closed refusal into a per-attempt advisory record. Without it `validate` exits 3.

```bash
partitur validate
```

Exit 0, with the advisory enforcement block on stderr, means the score and cast are sound and every
adapter was probed. From here the loop is:

```bash
partitur run       # execute; the draft interview stops to ask you something
partitur status    # what the run is waiting on, and the id of the open decision
partitur answer <decision-id> --answer "..."
partitur resume <run-id>
```

`answer` and `approve` take a **decision** id, not a run id, and `status` is where you read it.
Resolving a decision records it — it does not by itself start the next attempt, so `resume` is what
carries the run forward. Repeat that cycle until the interview settles; `approve <decision-id>
--approve` resolves a human gate or an amendment when one is what `status` reports. Then
`promote-score <run-id>` turns the draft into a finalized score, `run` executes it, and `apply
<run-id>` carries the verified result onto your checkout.

Each command's exact operands and exit codes are specified in [`docs/DESIGN.md`](docs/DESIGN.md) §7.
This page is verified as far as `validate`; the loop above is the specified surface, and the worked
transcript lands here once it has been executed rather than composed.

Working across several repositories? Cast layers `.partitur/cast.yaml` over
`~/.config/partitur/cast.yaml`, and `performers` and `bindings` layer independently. Put the
`performers` block in the user-global file once and leave each repository a `bindings` block of four
lines. Keeping the advisory entry a dedicated, separately named performer — selected per repository
rather than inherited — is what §3 asks for.

## Status

**Runnable, barely packaged.** The whole loop has executed end to end: a score compiles, movements
execute against real adapters, acceptance criteria are evaluated, a human gate stops the run, an
interrupted run recovers to a fixpoint, and `apply` carries the result onto the checkout. All
thirteen commands are dispatched.

[`docs/COMPLETION.md`](docs/COMPLETION.md) §5 is the row that asks whether the tool is *usable*
rather than whether it is *correct*. It is green on evidence recorded 2026-08-12: one run per
platform, macOS and Linux, against this repository with the real `codex` performer, carrying a
non-no-op write, a criterion that verified it, an approved human gate, a kill that left a genuine
recovery obligation, and an `apply` onto a checkout that initially differed. Both runs produced the
same candidate. Five of those six were confirmed with ordinary production-CLI commands; reaching the
kill deterministically needed a `-tags=faultprobe` build, and that row records which binary each
property required rather than blurring them together.

What that evidence does not cover is the distance between one deliberate reference score and daily
use. Two things follow. Enforcement is advisory in practice — see the Quickstart — so the
constraints a score declares are recorded per attempt rather than imposed. And the adapters shell
out to vendor CLIs that change on their own schedule, while the full real-CLI path has been
exercised deliberately rather than continuously.

The design is written down before it is built, and the gap between the two is tracked rather than
estimated:

| Document | What it owns |
|---|---|
| [`docs/CONCEPT.md`](docs/CONCEPT.md) | the design philosophy and the naming vocabulary |
| [`docs/DESIGN.md`](docs/DESIGN.md) | the normative specification — schemas, protocol, state model, recovery |
| [`docs/HARNESS.md`](docs/HARNESS.md) | which crash boundaries are injected at, and which are not yet |
| [`docs/COMPLETION.md`](docs/COMPLETION.md) | what "finished" means, as rows that can be checked rather than judged |

`COMPLETION.md` is the honest answer to "how far along is this?" Its nine sections now read green,
and its own closing rule says that is necessary and not sufficient: a set of prerequisites has to
become rows there before completion can be declared, and they have not.

## The vocabulary

Partitur borrows its whole domain language from the score, so the naming stays coherent as the
system grows:

| Term        | Musical meaning                    | In Partitur                          |
|-------------|------------------------------------|--------------------------------------|
| **score**   | the full written work              | the declarative plan                 |
| **part**    | one instrument's line              | one agent's assigned role            |
| **movement**| a section of the work              | a phase / stage of work              |
| **cue**     | the signal to enter                | a dispatch to an agent               |
| **tutti**   | everyone plays                     | broadcast / fan-out                  |
| **rest**    | a measured silence                 | an idle / waiting agent              |
| **tempo**   | the pace                           | concurrency & pacing                 |

## License

MIT
