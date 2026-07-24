# Partitur — Concept & Vocabulary

> This English document is **canonical**. Korean mapping: [`docs/ko/CONCEPT.md`](ko/CONCEPT.md).

## Why this project exists

AI models continue to improve, and each vendor's models work in noticeably different
styles. Partitur does not try to add another layer of model intelligence. It addresses a
different gap: **the lack of durable, vendor-neutral coordination across models, sessions,
and hand-offs.**

Partitur provides a structure in which users bind each part of development to the
performer they judge most suitable, while other performers challenge, complement, or
verify its work. It began as a tool its author needed for daily development, and it is
built for anyone who already runs multiple coding agents and knows their traits. Its first
commitment follows from that origin:

> **Never be bound to a single AI vendor.** Every vendor's model enters as just another
> performer, and users compose them to their own taste.

## Core value

Partitur is specialized for **coding**. Its core value:

> **Minimize the distance between the problem as stated and the problem as solved.**

A costly class of agent failure is not obviously broken code, but a solution to a subtly
different problem. Ambiguity or misinterpretation introduced early can amplify across
every hand-off. Partitur owns the context (the score) precisely to keep that distance
short, from the first clarifying question to the final verification.

Here, "the problem as stated" does not mean the user's first words taken literally. It
means the user's intent as clarified, resolved, and explicitly finalized in the score.
The distance Partitur minimizes is:

```text
finalized user intent ↔ verified outcome
```

When a design question stalls, one test settles it:

> "Does this shrink the distance between the stated problem and the solved one?"

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

1. **casts** — bind each part to the performer the user judges most suitable.
2. **cues** — dispatch work and trigger entrances at the right moment.
3. **keeps time** — coordinate concurrency and hand-offs between parts.
4. **holds the score** — own the single declarative plan every part is derived from.

## Structural model

Partitur separates the work from the performers that execute it:

- The **score** defines intent, constraints, movements, and logical parts.
- The **cast** binds each logical part to a performer. Partitur may provide defaults, but
  the user owns the final cast.
- An **adapter** connects a performer to Partitur's vendor-neutral execution protocol.

The score never hard-codes a universal lineup. A planning, implementation, or verification
part can be rebound without changing the meaning of the work itself.

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

Structural terms sit beside the musical vocabulary; they are not forced into the metaphor.

| Term          | Meaning                                                        |
|---------------|----------------------------------------------------------------|
| **performer** | the model or agent that actually executes work                 |
| **cast**      | the binding between parts and performers                       |
| **adapter**   | the vendor plugin connecting a performer to the core protocol  |
| **run**       | one execution of a specific score revision with a specific cast|
| **amendment** | an explicit, versioned change to the score or its instructions |

## Design principles

- Performers (agents) are replaceable. The score is **vendor-neutral** — Anthropic, OpenAI,
  Google, Moonshot, any camp enters as just one part.
- **Authority is split, and never hidden.** The score is the authoritative source of
  intent, constraints, and planned work. The event log is authoritative for execution
  history, and artifacts carry the produced evidence and outcomes. Vendor sessions are
  never authoritative state — only a cache for cheap resumption.
- Parts are derived from the score. Each part receives the intent, constraints, inputs,
  and global invariants needed for its work; no performer owns an independent
  reconstruction of the whole.
- Partitur is a **small execution protocol, not a library of reasoning recipes**. It owns
  the intent, state, authority, and evidence that models cannot reliably share on their
  own. It may deliver user intent, project facts, and part-specific instructions, but it
  does not encode generic thought patterns, personas, or vendor-specific reasoning
  techniques in the core.
- **Minimal harness.** Before anything enters the core, four questions: Can a better model
  replace it (intelligence)? Does it enforce who may do what (authority)? Must it survive
  process, vendor, or session death (persistence)? Does it observe success in the external
  world (evidence)? An element that only compensates for current model intelligence — and
  serves none of the other three functions — stays outside the core.
- **The runtime is independent.** It runs beside vendor agents, never inside one.
- **Intent is finalized before execution and amended explicitly afterward.** A score
  begins as `DRAFT`. Unresolved questions and verification expectations must be resolved
  or explicitly waived before it becomes `FINALIZED`. Discoveries made during execution
  never mutate the score silently; material changes are recorded as versioned amendments.
- **Verification is typed, never flattened into a single `passed` flag.** Machine
  evidence, human approval, and model review make different claims and remain
  distinguishable throughout the run. Model review can raise findings, but it is never
  presented as machine verification or human approval.
