# Partitur — Concept & Vocabulary

> This English document is **canonical**. Korean mapping: [`docs/ko/CONCEPT.md`](ko/CONCEPT.md).

## Why this name

**Partitur** is German for *full score* — the sheet in which every instrument's part is stacked
vertically on one page. Only one person reads it: the **conductor**. Each player sees only their
own line.

That is exactly where this project sits:

> Every agent (Claude Code, Codex, Gemini, Kimi, …) only needs to know its own part.
> **The single seat that holds the whole picture and casts each performer into a role** — that is
> Partitur.

The name was chosen deliberately, because here the name drives the design. Whenever we get stuck,
one question sets the direction:

> "How would the holder of the full score see this?"

That question converges naturally on an architecture of **a central declarative plan (the score)
plus part distribution**. Agents are only performers; the one that knows the whole is Partitur.

## The four core roles

1. **casts** — place each agent in the part it plays best.
2. **cues** — dispatch work and trigger entrances at the right moment.
3. **keeps time** — coordinate concurrency and hand-offs between parts.
4. **holds the score** — own the single declarative plan every part is derived from.

## Vocabulary

The domain language is borrowed wholesale from the musical score, so naming stays coherent as the
system grows.

| Term         | Musical meaning              | In Partitur                     |
|--------------|------------------------------|---------------------------------|
| **score**    | the finished full score      | the declarative plan            |
| **part**     | one instrument's line        | one agent's assigned role       |
| **movement** | a section of the work        | a phase / stage of work         |
| **cue**      | the signal to enter          | a dispatch to an agent          |
| **tutti**    | everyone plays               | broadcast / fan-out             |
| **rest**     | a measured silence           | an idle / waiting agent         |
| **tempo**    | the pace                     | concurrency & pacing            |

## Design principles (draft)

- Performers (agents) are replaceable. The score is **vendor-neutral** — Anthropic, OpenAI,
  Google, Moonshot, any camp enters as just one part.
- The score is the **single source of truth**. Parts are derived from it; a part never
  reconstructs the whole on its own.
