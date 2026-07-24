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

Early scaffold. Concept and vocabulary first; implementation to follow.
See [`docs/CONCEPT.md`](docs/CONCEPT.md) for the design philosophy and naming vocabulary.

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
