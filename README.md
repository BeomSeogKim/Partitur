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
make install
```

which is exactly:

```bash
go install ./cmd/partitur ./cmd/partitur-adapter-codex ./cmd/partitur-adapter-claude ./cmd/partitur-trampoline
```

`go install` writes to `$(go env GOBIN)` when that is set and `$(go env GOPATH)/bin` otherwise. Put
whichever one applies on your `PATH`. The core resolves its siblings by name, so invoking `partitur`
by absolute path with that directory *off* `PATH` fails at the first probe rather than later:

```
adapter-environment: adapter="codex" kind="executable_absent" detail="partitur-adapter-codex is absent from PATH"
```

(`make help` lists the other targets — `make build`, `make battery` for the full CI test set, `make
check` for the whole gate.)

An adapter is a thin shim, not the agent. Whichever performer a cast selects, that vendor's own CLI
has to be installed and on `PATH` as well — the adapter's probe resolves and runs it. `validate`
exits 3 either way, and the `adapter-environment` diagnostic distinguishes them: `executable_absent`
means the adapter binary is missing, while a missing vendor CLI surfaces as `error_response` naming
what the adapter could not resolve. Everything below was executed against a fresh repository with
the Codex CLI installed.

### Scaffold and cast

```bash
partitur init
```

That writes `partitur.yaml` — a draft score with one interview movement — and `.partitur/`. It
deliberately writes no cast: a cast binds parts to the agents *you* choose, and the tool does not
choose for you. `validate` refuses, names the part it could not bind, and hands you the block:

```console
$ partitur validate
cast: rule="cast.score" origin="" pointer="/bindings/interview" detail="binding_missing" hint="write the missing binding in .partitur/cast.yaml (project) or ~/.config/partitur/cast.yaml (user-global): bindings.<part>.performer must name an entry in performers; paste this minimal cast into .partitur/cast.yaml:\ncast: \"0.1\"\nperformers:\n  performer:\n    adapter: codex\n    model: your-model\nbindings:\n  <part>:\n    performer: performer"
```

Substitute a model your adapter can reach and the part the pointer named. Pasting just that much
still exits 3, on a second hint — `unmet=["path_grants" "shell_grants"]`, offering
`allow_advisory_enforcement: true` or the grants that would clear each dimension. The final
`.partitur/cast.yaml`:

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

`allow_advisory_enforcement: true` concedes something real. [`docs/DESIGN.md`](docs/DESIGN.md) §4's
withheld-authority table pairs each authority a movement withholds or scopes with an enforcement
dimension the adapter must provide in its place. The scaffold withholds `shell` and declares no
`allowed_paths`; the Codex adapter reports neither `shell_grants` nor `path_grants`; so those two
rows go unmet. The flag turns a fail-closed refusal into a per-attempt advisory record.

Whether some other score can drop the flag is a question about that score, and `validate` answers it
by naming the dimensions it found unmet — §4's table and `internal/cast/enforcement.go` say which
grants and which `allowed_paths` would clear them. One rule there is more mechanical than it reads:
for a movement granting `repo_read` or `repo_write`, `path_grants` is demanded unless `allowed_paths`
is exactly `["**"]` — so a repository-granting movement declaring `["**", "src/**"]` still owes it,
even though that list narrows nothing.

```console
$ partitur validate
enforcement advisory: movement="interview" part="interview" performer="codex" unmet=["path_grants" "shell_grants"]
```

Exit 0, with the advisory enforcement block on stderr, means the score and cast are sound and every
adapter was probed.

### A score that does work

The scaffold's interview movement is a draft-phase placeholder. Replace `partitur.yaml` with a
finalized score — this one writes a file and then checks it:

```yaml
score: "0.2"
name: greeting
revision: 1
status: finalized

goal: Add a greeting file to the repository.

verification:
  expectation:
    intent: none
    apply_gate:
      require: [verified, approved]
  final_movement: check

parts:
  writer:
    capabilities: [repo_read, repo_write]
  verifier:
    capabilities: [repo_read]
    read_only: true

movements:
  - id: write-greeting
    part: writer
    needs: []
    grants: [repo_read, repo_write]
    instruction: |
      Create the file greeting.txt containing exactly one line:
      hello from partitur

      The line must end with one LF byte. Do not edit, add, remove, or rename
      any other file.
    outputs:
      - id: greeting-change
        kind: change_set
    acceptance:
      hard:
        - id: greeting-written
          run: ["sh", "-c", "printf 'hello from partitur\\n' | cmp -s - greeting.txt"]

  - id: check
    part: verifier
    needs: [write-greeting]
    grants: [repo_read]
    instruction: |
      Confirm that greeting.txt holds exactly the line `hello from partitur`
      followed by one LF byte, and nothing else. Report what you observed.
      Change no file.
    inputs: [greeting-change]
    acceptance:
      hard:
        - id: greeting-is-hello
          run: ["sh", "-c", "printf 'hello from partitur\\n' | cmp -s - greeting.txt"]
      human_gate: always

policy:
  allowed_paths: ["greeting.txt"]
  side_effects: []
  budget:
    active_wall_clock_min: 10
    retries_per_movement: 0
  amendment:
    auto: "off"
```

The parts changed, so the cast's `bindings` block gains `writer` and `verifier` and drops
`interview`. Two shapes here are load-bearing rather than stylistic. The final movement may not
grant `repo_write` — naming the writer as `final_movement` is refused with
`detail="final_movement_repo_write"` — so a verifying movement is what closes a writing score. And
`require: [verified, approved]` obliges the final movement to carry both a `hard` criterion and
`human_gate: always`, or `validate` reports the grade as unachievable.

### Run, gate, apply

```bash
partitur run
```

`run` prints the run id and executes until something needs a human. Here that is the gate on
`check`, and `status` is how you read it:

```console
$ partitur status
Run: 01a09d6e-d568-7158-a4a5-a1752d6bda71 (WAITING_HUMAN)
Journal: INTACT
Application: NOT_APPLIED
Candidate: sha256:e0abe119… (tree git-sha1:dc8fd174…, base git-sha1:bf3bf6d4…, rev 1)
Pending decision 01a09d70-0d3e-78dc-a33b-4ae6b7dfa8d7: human_gate (rev 1)
Movement write-greeting: SUCCEEDED
  Mark: VERIFIED (1 criteria: greeting-written […]; rev 1; after 0 failed attempts)
Movement check: WAITING_HUMAN
  Mark: VERIFIED (1 criteria: greeting-is-hello […]; rev 1; after 0 failed attempts)
```

Resolve the decision, then carry the run forward — two commands, because resolving is not resuming:

```console
$ partitur approve 01a09d70-0d3e-78dc-a33b-4ae6b7dfa8d7 --approve
run waiting: state="nonterminal" resume="partitur resume 01a09d6e-d568-7158-a4a5-a1752d6bda71"
$ partitur resume
```

The run is now `SUCCEEDED`, `check` carries a second `APPROVED` mark naming the gate decision — and
the working tree still has no `greeting.txt`. A candidate is not an application:

```console
$ partitur apply 01a09d6e-d568-7158-a4a5-a1752d6bda71
$ cat greeting.txt
hello from partitur
```

`status` then reads `Application: APPLIED`. Three things a reader would otherwise guess wrong:

- `answer` and `approve` take a **decision** id, not a run id, and resolving a decision does not by
  itself start the next attempt — `resume` is what does.
- Bare `status` finds only *active* runs. Once the run is terminal it refuses with `precondition
  refused: detail="no active run: found 0"` (exit 2); pass the run id.
- Promotion comes last, not first. Only the latest revision of a `SUCCEEDED` run may be promoted, at
  most once, and only after `apply.completed` for the same candidate — so `apply <run-id>` precedes
  `promote-score <run-id>`.

Each command's operands, exit codes, and refusal conditions are specified in
[`docs/DESIGN.md`](docs/DESIGN.md) §7, and the draft phase's own contract in §2.

Working across several repositories? Cast layers `.partitur/cast.yaml` over
`~/.config/partitur/cast.yaml`, and `performers` and `bindings` layer independently. Put the
`performers` block in the user-global file once and leave each repository a `bindings` block of four
lines. Keeping the advisory entry a dedicated, separately named performer — selected per repository
rather than inherited — is what §3 asks for.

### Solo: one part, no gate

Use this path for one well-specified task that should finish in one session when you will review the
diff yourself. The complete score is:

```yaml
score: "0.2"
name: document-validation
revision: 1
status: finalized

goal: Document how to validate a Partitur score and cast.

verification:
  expectation:
    intent: pass-existing-tests
    apply_gate:
      waived: true
      reason: The user will review the resulting diff before keeping it.

parts:
  writer:
    capabilities: [repo_read, repo_write, shell]

movements:
  - id: document-validation
    part: writer
    grants: [repo_read, repo_write, shell]
    instruction: |
      Add a concise README section explaining that `partitur validate` checks
      both partitur.yaml and the resolved cast before a run. Match the existing
      voice and edit no other file.
    outputs:
      - id: documentation-change
        kind: change_set
    acceptance:
      hard:
        - id: build
          run: ["go", "build", "./..."]
        - id: vet
          run: ["go", "vet", "./..."]
        - id: test
          run: ["go", "test", "./..."]

policy:
  allowed_paths: ["**"]
  budget:
    active_wall_clock_min: 20
```

A single Codex performer is enough for this score:

```yaml
cast: "0.1"
performers:
  codex:
    adapter: codex
    model: gpt-5.6-sol
bindings:
  writer:
    performer: codex
```

For a Claude performer, add `allow_advisory_enforcement: true` because that adapter reports
`network_grants: false` while this movement does not grant network access.

Run and apply it in two commands, using the run id printed by the first:

```bash
partitur run
partitur apply <run-id>
```

Bare `status` refuses once the run is terminal, so pass the run id: `partitur status <run-id>`.
Waiving the apply gate forbids a `final_movement`, so there is no verifier movement. This movement
declares no `human_gate` of its own (the default is `never`), so acceptance is the only check before
apply. Use the gated path above when you need the verifier movement or human approval.

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

The score lives at one committed path. `run` and `validate` read only `<repo>/partitur.yaml`, and
v0.2 has no `--score` operand or overlay file by design. A repository whose committed
`partitur.yaml` is itself a fixture — as this one's is — therefore drives any *other* task through
Partitur by temporarily overwriting that score and reverting it. The single-source contract accepts
that chore in exchange for one unambiguous score location to snapshot, resume-check against, and
promote back to (#435).

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
