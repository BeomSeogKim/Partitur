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

## Status

**Not usable yet.** The engine runs — a score compiles, movements execute against real adapters,
acceptance criteria are evaluated, and an interrupted run recovers to a fixpoint — but the commands
that would make it a tool you can pick up are still landing. `apply`, `amend`, and `init` do not
exist, so nothing Partitur produces reaches your checkout yet.

What does work today: `validate`, `run`, `resume`, `cancel`, `status`, `logs`, `answer`, `approve`.
A run can execute a plan, ask you a question and continue from your answer, run declared checks,
have its output reviewed by a model, and stop at a human gate.

The design is written down before it is built, and the gap between the two is tracked rather than
estimated:

| Document | What it owns |
|---|---|
| [`docs/CONCEPT.md`](docs/CONCEPT.md) | the design philosophy and the naming vocabulary |
| [`docs/DESIGN.md`](docs/DESIGN.md) | the normative specification — schemas, protocol, state model, recovery |
| [`docs/HARNESS.md`](docs/HARNESS.md) | which crash boundaries are injected at, and which are not yet |
| [`docs/COMPLETION.md`](docs/COMPLETION.md) | what "finished" means, as rows that can be checked rather than judged |

`COMPLETION.md` is the honest answer to "how far along is this?" — most of its rows are red, and it
says so.

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
