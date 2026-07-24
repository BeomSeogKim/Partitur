# Partitur — Design v0.1

> Implementation-level design for the v0.1 core. Concept and principles:
> [`docs/CONCEPT.md`](CONCEPT.md). This document covers the score/cast/adapter contracts,
> the workspace and run state models, the acceptance runner, and the file layout.
> Decision-level policies (amendment approval details, verification semantics) will live
> in `docs/decisions/`.

## 0. Ground rules inherited from the concept

- The core is a small execution protocol in Go: a thin supervisor that owns state,
  authority, and evidence. Agent output is streamed incrementally with bounded protocol
  buffers; the core never accumulates an attempt's full output in memory.
- v0.1 executes movements **sequentially**. No parallel scheduling, no daemon.
- The core is the **only writer of persistent run state**. Adapters and user interfaces
  (CLI today, GUI later) may emit proposals or commands, but they never write state files
  directly; the core validates every transition and records it. Command semantics are
  owned by the core and shared by every interface.

## 1. File formats and layout

Score and cast are **YAML**, restricted to a safe subset: YAML 1.2; duplicate keys
rejected; anchors, aliases, merge keys, and custom tags rejected; no implicit type
coercion where a string is expected. Run state is **JSONL** (event log) plus YAML
manifests.

```text
<repo>/
  partitur.yaml                # the score (committed)
  .partitur/
    .gitignore                 # created by `partitur init`; contains `runs/`
    cast.yaml                  # project cast override (committed or ignored, user's choice)
    runs/<run-id>/             # ignored by default
      journal.jsonl            # append-only event log (single writer: core)
      manifest.yaml            # source score revision + hash, resolved cast pins,
                               # enforcement notes, artifact index
      scores/revision-<n>.yaml # immutable score snapshots (see below)
      resolved-cast.yaml       # the fully resolved cast used by this run
      artifacts/               # immutable artifact copies (hashed at record time)
      session/                 # session hints, mode 0600 (see §4 privacy)
      attempts/<attempt-id>/   # per-attempt capture: stderr, protocol trace
~/.config/partitur/cast.yaml   # user-global cast override
<install>/default-cast.yaml    # first-party factory cast (versioned data file with
                               # metadata: date, tested adapters/models, rationale)
```

**Authority within run state.** `journal.jsonl` is authoritative for lifecycle history.
`manifest.yaml` is a rebuildable projection/checkpoint of the journal. Immutable score
snapshots and artifact copies remain authoritative for their respective contents. Crash
recovery replays the journal and rebuilds the manifest.

**Artifact recording atomicity.** Recording an artifact follows a fixed order: copy to a
temporary file → compute hash (`sha256:<hex>`) and durably flush → atomic rename into
`artifacts/` → append `artifact.recorded` → update the manifest projection. On recovery,
an orphan artifact file without an event is quarantined; an event whose file is missing
is a recovery error that halts the run.

**Score snapshots and the root score.** `partitur.yaml` stays editable by the user, so a
revision number alone cannot reproduce a run:

- At run start the core snapshots the full score (and its hash) into
  `scores/revision-<n>.yaml`. Every approved amendment produces a new immutable snapshot.
- Amendments modify **only the run's snapshot chain** — the root `partitur.yaml` is never
  overwritten automatically. The explicit `promote-score` command copies a run revision
  back to the root score; promotion is a compare-and-swap against the root hash recorded
  at run start, and surfaces a conflict if the root has changed since.
- Resume works from the run's snapshot, never from the current root `partitur.yaml`.
  If the root score claims the same revision number but its hash differs from the
  snapshot, the core refuses to auto-resume and asks the user.
- The manifest records both the source revision and the content hash.

**Cast layering.** Resolution order is **project → user-global → factory default**, with
deliberately simple merge rules (no deep merge):

- `performers` entries replace **whole objects** per performer id.
- `bindings` entries replace whole objects per part id; `fallbacks` lists are replaced
  wholesale, never concatenated.
- Removal/tombstoning is not supported in v0.1.
- Adapters are resolved on demand for the adapter ids the resolved cast references —
  the core never scans `PATH` for everything that looks like an adapter.

The run manifest pins the resolved performer, adapter version, and model for every part.

## 2. Score schema v0.1 (`partitur.yaml`)

```yaml
score: "0.1"                    # schema version
name: rsvp-deadline-reminders
revision: 1                     # bumped only by amendments
status: draft                   # draft | finalized

goal: |                         # finalized user intent — prose, in the user's language
  Guests who have not answered their RSVP get a reminder 7 days before
  the deadline, once, by email.

context: |                      # project facts the user supplied (optional)
  Email goes through the existing `notifier` package. Timezone is Asia/Seoul.

draft:
  interview_movement: clarify   # the one movement allowed to run while status: draft

open_questions:                 # DRAFT gate — finalization requires every entry
  - id: q-1                     # resolved or explicitly waived
    question: "Should reminders also go to partially-answered groups?"
    resolution: "No — only fully unanswered."
  - id: q-2
    question: "Is there a quiet-hours window for sending?"
    waived: true                # explicit waiver, recorded forever

verification:                   # user intent about verification — resolved or waived
  expectation: write-basic-tests  # write-basic-tests | pass-existing-tests | none
                                  # during the interview; 'none' is always explicit

parts:                          # logical roles — never vendor or model names
  plan:
    capabilities: [repo_read]
  implement:
    capabilities: [repo_read, repo_write, shell]
  verify:
    capabilities: [repo_read, shell]
    read_only: true             # never receives a write grant

movements:                      # units of work; DAG via `needs`; each played by one part
  - id: clarify
    phase: draft                # runs while status: draft — see DRAFT phase contract
    part: plan
    grants: [repo_read]
    instruction: |
      Identify unresolved requirements and verification expectations.
      Raise each as a question.
  - id: design
    part: plan
    needs: []
    grants: [repo_read]
    instruction: |
      Identify where reminder scheduling belongs, list touched files,
      and produce a short design note.
    outputs:
      - id: design-note
        kind: document
  - id: build
    part: implement
    needs: [design]
    grants: [repo_read, repo_write, shell]
    instruction: |
      Implement the reminder flow per the design note.
    inputs: [design-note]
    outputs:
      - id: change-set
        kind: change_set
    acceptance:                 # a movement with a repo_write grant MUST have ≥1
      hard:                     # hard criterion or human_gate: always
        - run: ["go", "test", "./..."]
        - run: ["go", "vet", "./..."]
  - id: check
    part: verify
    needs: [build]
    grants: [repo_read, shell]
    instruction: |
      Review the change against the goal. Report findings with file, line,
      violated criterion, and a counter-example where possible.
    inputs: [change-set]
    outputs:
      - id: review-findings
        kind: findings
    acceptance:
      review:
        - findings: review-findings   # typed requirement: this findings artifact must
          rubric: [requirement_coverage, regression_risk]   # exist and be well-formed
      human_gate: on_contested  # always | on_contested | never

policy:
  allowed_paths: ["internal/**", "cmd/**", "**/*_test.go"]
  side_effects: []              # v0.1 accepts only an empty list; non-empty values are
                                # rejected until a typed side-effect registry is specified
  budget:
    active_wall_clock_min: 90   # active execution time only — adapter runs, acceptance,
                                # retries, fallbacks. WAITING_HUMAN and stopped time are
                                # excluded. Consumed time is persisted via journal events;
                                # each attempt receives the remainder at its start.
    retries_per_movement: 2     # quality-retry budget per movement — see §3
  amendment:
    auto: "off"                 # off | envelope; default off. envelope = only
                                # provably-monotone changes inside the bounds above are
                                # auto-approved (details in docs/decisions/); everything
                                # else waits for a human.
```

**Path policy semantics.** `allowed_paths` patterns are repository-relative POSIX-style
paths; `**` recurses into directories; paths are canonicalized before matching; case
sensitivity is fixed by core rule (case-sensitive), independent of the worktree
filesystem.

**DRAFT phase contract.** While `status: draft`, only the movement named by
`draft.interview_movement` may run. It is read-only (no write grant permitted). It may
emit `log` and `progress` events, but its only *semantic* outputs are `question` and
`proposal` — `artifact` events are forbidden entirely in draft movements. All other
movements refuse to start until `status: finalized`.

Human answers materialize into the score only through amendments — an answer is never a
direct score mutation:

1. The interview movement emits `question`(s); the attempt ends `BLOCKED`.
2. The human answers; the core records `decision.resolved`.
3. A new interview attempt receives the answers via `resolved_decisions` and emits a
   `proposal` amending the score (resolving the open questions, setting or waiving the
   verification expectation).
4. The approved proposal produces a new immutable snapshot.
5. When every open question and the verification expectation are materialized, the human
   explicitly approves the `draft → finalized` transition — finalization is itself a
   human decision, never automatic.

**Amendment format v0.1.** A `proposal` carries:

```text
{
  base_revision, base_hash,        # stale-rejected if either mismatches
  operations: [...],               # RFC 6902 JSON Patch, applied to the canonical JSON
                                   # representation of the YAML score
  reason,
  evidence?: [artifact_id],
  claimed_impact: { paths, grants, side_effects, budget_delta }
}
```

`claimed_impact` is the proposer's claim and carries no authority: the core recomputes
the authoritative impact by diffing the score before and after the patch, and rejects
the proposal if the claim is narrower than the actual impact. Approved patches apply
only to the run's snapshot chain (see §1 for promotion to the root score).

**Rules enforced by `partitur validate` (the score compiler):**

1. `status: finalized` requires every open question resolved or waived and
   `verification.expectation` present.
2. A movement that requests a `repo_write` grant must carry ≥1 `hard` criterion or
   `human_gate: always`. (Keyed on the movement's grant, not the part's capability —
   a write-capable part may still play read-only movements.)
3. `grants` ⊆ the part's `capabilities` ∩ what `policy` allows. A `read_only` part can
   never receive `repo_write`. Read-only-ness is never inferred from instruction text or
   output names.
4. `needs` must form a DAG; part references must exist; every `inputs` entry must be an
   `outputs` id of a movement reachable through `needs`; artifact ids are unique per run.
5. Artifact paths are canonicalized inside the attempt's writable areas (§5); `..`,
   symlinks escaping them, and absolute paths outside them are rejected.
6. Unknown fields in the core namespace are an error; adapter-specific data lives only
   under `extensions.<adapter-id>`.
7. An `acceptance.review` entry must reference a `findings`-kind output of the same
   movement.

## 3. Cast schema v0.1 (`cast.yaml`)

```yaml
cast: "0.1"
performers:
  fable:
    adapter: claude
    model: claude-fable-5
  sol:
    adapter: codex
    model: gpt-5.6-sol
    allow_advisory_enforcement: false   # default false; see §4 trust boundary
    extensions:
      codex:                    # adapter-namespaced, opaque to the core; the core
        effort: xhigh           # forwards only the namespace matching the performer's
                                # adapter id
bindings:
  plan:      { performer: fable, fallbacks: [sol] }
  implement: { performer: fable }
  verify:    { performer: sol }
```

- `partitur validate` checks every bound performer's probed capabilities against the
  part's `capabilities`, and the adapter's enforcement against the movement's grants
  (see §4, trust boundary).
- `allow_advisory_enforcement` defaults to `false`, applies per performer (including
  independently to each fallback performer), and when `true` the manifest records
  individually which constraints were advisory for which attempt.

**Retry and fallback semantics.** `retries_per_movement` is the movement's
**quality-retry budget**; infrastructure failures advance the fallback chain instead:

- **Infrastructure failure** (`adapter_unavailable`, `model_unavailable`,
  `provider_timeout`, `rate_limited`, `authentication`): move to the next fallback
  performer; no retry is consumed. `protocol_error` does **not** trigger fallback in
  v0.1 (uniform rule; a cast-level opt-in may come later).
- **Quality failure** (`task_failed` or acceptance failure): consume one retry and try
  again with the **same** performer from a clean base. Quality failures never trigger
  fallback — a different model is not the fix for a failed test; amendments and humans
  are.
- The movement fails when quality retries are exhausted, or when the fallback chain is
  exhausted by infrastructure failures. The chain never revisits an earlier performer.
- Maximum attempts per movement: `1 + retries_per_movement + fallback_count`.
- Every retry and fallback starts from the same clean base and the same input artifacts.
- `grant_denied`, review findings, and user cancellation trigger neither retry nor
  fallback.

## 4. Adapter protocol v0.1

**Packaging.** An adapter is an executable `partitur-adapter-<id>` found on `PATH` or via
explicit config. Adapters are explicitly enabled; nothing is auto-discovered.

**Process model and framing.** The core spawns the adapter **per attempt**. Transport is
JSON-RPC 2.0 messages as **UTF-8 JSON Lines on stdout**: one request, response, or
notification per line; newlines inside values are escaped; control frames have a fixed
size cap — large content travels as artifact files, never inline. `stderr` is
diagnostics only, captured to the attempt directory. The adapter must keep reading stdin
during `execute` so a `cancel` request can be received; after a grace timeout the core
terminates the process, and force-kills after a further timeout. No daemon in v0.1.

**Session hints and privacy.** Session continuity across attempts is carried by an
opaque `session_hint` the adapter may return and the core may pass back — always an
optimization, never required state (conformance test: resume with the hint removed).
Because hints are opaque they may contain resume tokens: the core never writes them to
the journal, manifest, or protocol trace; they live in `runs/<id>/session/` with mode
`0600` and are deleted with the run. Adapter conformance requires that hints carry no
long-lived credentials and that diagnostics never echo them.

**Methods.**

```text
probe() -> {
  protocol: 1,
  adapter: { id, version },
  capabilities: {
    repo_read, repo_write, shell, network: bool,
    resumable_sessions: bool,
    models: [ { id, aliases? } ]
  },
  enforcement: {                # what the adapter/vendor agent actually enforces
    path_grants: bool,          # confines writes/reads to granted paths
    read_only: bool,            # can run with all writes disabled
    network_grants: bool        # can disable network
  }
}

execute(request) -> streams `event` notifications, then returns result
  request: {
    run_id, movement_id, attempt_id, score_revision,
    model,
    brief: {                    # the score projection for this part — the intent,
      goal,                     # constraints, and invariants it needs (per CONCEPT)
      context,
      instruction,
      verification_expectation, # user intent, forwarded to relevant parts; whether it
      acceptance,               # was met is judged by acceptance, never by the adapter
      global_invariants         # deterministic projection computed by the core from
    },                          # goal, finalized resolutions, and policy — not a
                                # separate score field
    inputs: [ { artifact_id, kind, path, hash } ],
    feedback: [                 # diagnostics from prior failed attempts (see §7):
      { previous_attempt_id, kind, artifact_id }
    ],                          # read-only; never applied to the base
    resolved_decisions: [       # answers to earlier questions / resolved amendments
      { decision_id, answer }
    ],
    workdir,                    # the attempt worktree (see §5)
    output_dir,                 # always-writable artifact area (see §5)
    grants: { paths_rw, paths_ro, shell, network },
    budget: { active_wall_clock_min },   # remaining at attempt start
    session_hint?,
    extensions?                 # only the namespace matching this adapter's id
  }
  result: {
    outcome: completed | failed | cancelled | waiting_human,
    failure?: {                 # present when outcome = failed
      kind: adapter_unavailable | model_unavailable | provider_timeout |
            rate_limited | authentication | protocol_error |
            grant_denied | task_failed,
      detail?
    },
    pending_decision_ids?: [],  # present when outcome = waiting_human; an attempt may
                                # raise several questions and blocks on all of them
    session_hint?,
    detail?
  }
  # transport-level outcome only — the adapter NEVER judges success;
  # the core runs acceptance.

cancel(attempt_id) -> graceful stop; core terminates on grace timeout, then force-kills
```

**Event notifications** (adapter → core, during `execute`):

```text
log        { level, message }
progress   { message }
artifact   { artifact_id, path }        # path must be inside output_dir; the core
                                        # immediately copies the file into
                                        # runs/<id>/artifacts/ (see §1 atomicity) and
                                        # treats only that immutable copy as recorded
proposal   { amendment }                # structured amendment (§2 format); core validates
question   { id, question }             # see blocking handshake below
```

Code changes are never communicated as `artifact` events — the core itself captures them
from the worktree as a `change_set` (§5). Draft movements may not emit `artifact` at all.

**Blocking handshake for questions and proposals.** v0.1 has no daemon, so nothing waits
in memory for a human:

- An adapter that needs answers emits `question`(s) (or a `proposal` it cannot proceed
  without), returns `outcome: waiting_human` with `pending_decision_ids`, and exits. The
  attempt ends `BLOCKED` (a terminal attempt state — no process stays alive).
- The core records `decision.requested` per question and sets the movement to
  `WAITING_HUMAN`.
- When the human has answered **all** pending decisions (or the amendment is decided),
  the core records `decision.resolved` events and starts a **new attempt**, passing the
  resolutions in `resolved_decisions` (plus `session_hint` if available).
- An **auto-approved** proposal also changes the score revision, so the current attempt
  is superseded and restarted against the new revision — an attempt never keeps running
  across a revision change.

**Core protocol and state limits.** The core never requests wider grants than the
score's `policy` allows; run state lives outside the attempt workspace and the protocol
provides no direct state-mutation or adapter-chaining operation. A conforming adapter
must not modify score, cast, or run state files directly, judge success, or invoke other
adapters. Since adapters are trusted executables running with the user's privileges,
**malicious adapter behavior is outside the core's security boundary** — the limits
above are protocol conformance rules, not physical guarantees.

**Environment enforcement — the trust boundary stated honestly.** A JSON-RPC field
cannot physically stop a `shell: true` process from touching other paths. Real
filesystem/network confinement comes from the OS sandbox or the vendor agent's own
enforcement, which is why `probe` reports `enforcement`. If an adapter cannot enforce a
constraint a movement's grants require, the core **fails closed** — unless the resolved
cast sets `allow_advisory_enforcement: true` for that performer, in which case the run
proceeds and the manifest records, per attempt, which constraints were advisory.

## 5. Workspace model v0.1

**Run preconditions.** A run requires a Git repository. At run start the core records
the source base commit/tree id in the manifest, together with the score and resolved
cast hashes. If the source tree has tracked or untracked changes (beyond ignored
`.partitur/` run data), the run is refused — dirty-source support is future scope. This
guarantees the agents' base is exactly what the user sees.

Write attempts never modify the user's checkout directly. v0.1 uses **Git worktrees**:

- Each attempt gets a fresh worktree built from the **approved results of its dependency
  movements** (the clean base), plus an always-writable `output_dir` outside the
  worktree for declared artifacts. `read_only` means the repository worktree is
  read-only; ordinary artifacts (documents, findings) are written to `output_dir` only.
- **Fan-in.** When a movement depends on several movements, the clean base applies their
  transitive change sets without duplication, in deterministic topological order (ties
  broken by declaration order in the score). READY-movement scheduling likewise follows
  declaration order. If applying change sets conflicts, the core never merges
  silently — the movement goes `WAITING_HUMAN` (or fails with a structured error).
  Changes from failed, cancelled, or superseded attempts are never candidates.
- A `change_set` is a **core-created Git checkpoint commit** in the isolated worktree,
  recorded with its base tree and resulting tree ids. A patch may be exported for
  inspection or application, but it is not the canonical identity.
- Partial changes from failed, cancelled, or superseded attempts are discarded — they
  never leak into the next attempt. A fallback performer starts from the same clean
  base, never from the failed performer's dirty workspace.
- Applying the final result to the user's checkout is the explicit `apply` command —
  never a side effect of a movement finishing.

## 6. Run state model v0.1

Three levels, deliberately separate:

```text
Run:      RUNNING | WAITING_HUMAN | SUCCEEDED | FAILED | CANCELLED
Movement: PENDING | READY | RUNNING | WAITING_HUMAN | SUCCEEDED | FAILED | CANCELLED
Attempt:  STARTING | RUNNING | COMPLETED | BLOCKED | FAILED | CANCELLED | SUPERSEDED
```

- `BLOCKED` is a **terminal** attempt state: the attempt exited while waiting on human
  decisions; the follow-up work happens in a **new** attempt. Only movements and runs
  use `WAITING_HUMAN`.
- A movement failure fails the run only when retries and fallbacks are exhausted;
  `SUPERSEDED` exists only at the attempt level (steer, instruction revision, or an
  approved amendment → new attempt).

**Active-run exclusivity.** A run is *active* while nonterminal (`RUNNING` or
`WAITING_HUMAN`). v0.1 allows one active run per repository: `partitur run` refuses to
start while one exists (resume or cancel it first). Commands accept an explicit run id
or select the unique active run. A repository-scoped OS file lock guards state
mutations against concurrent CLI invocations; the lock is held only during mutations —
never across human waits — and the nonterminal journal state is the logical guard.

Event envelope (`journal.jsonl`, control-grade for a later GUI):

```json
{ "event_id": "evt-42", "seq": 42, "ts": "...",
  "run_id": "...", "score_revision": 3,
  "movement_id": "build", "part_id": "implement", "attempt_id": 2,
  "type": "artifact.recorded", "causation_id": "evt-40", "payload": {} }
```

- `movement_id`, `part_id`, `attempt_id` are optional — run-level events omit them.
- `causation_id` references another event's `event_id`.
- Pending decisions are represented as an append-only pair — `decision.requested` /
  `decision.resolved` — never as a mutable record; readers project the pair into a
  "currently pending" list.

## 7. Acceptance runner and CLI v0.1

When an attempt returns `completed`, the core runs acceptance **in a fixed order**,
before the worktree is removed:

```text
adapter completed
→ artifact events already recorded as immutable copies (§1)
→ core captures the provisional change_set checkpoint (§5)
→ hard command criteria
→ artifact integrity criteria
→ review findings processing
→ human gate (if required)
→ movement SUCCEEDED; artifacts and change_set approved
```

**Hard criteria** (core-executed, machine evidence):

- `run: [argv...]` — executed as an argv array (no shell interpretation) inside the
  attempt worktree with the candidate changes present. Exit 0 passes. Per-criterion
  `timeout_min` is optional; the effective timeout is
  `min(criterion timeout, remaining active budget)`. Output is captured with bounded
  streaming into the attempt directory and the event log. A failing criterion records
  `acceptance.failed` — it is the core's judgment, never the adapter's `task_failed`.
  If an acceptance command mutates tracked files in the worktree, the criterion fails
  with `acceptance_mutated_workspace` (ignored caches/outputs are permitted) — the
  change set that was verified must be the change set that ships.
- `artifact` — the declared output exists, its `kind` matches, and its recorded hash
  matches the immutable copy (manifest integrity). An optional `expected_hash` is
  allowed for pre-known content.

**Review criteria** (typed model evidence — never machine verification):

- `acceptance.review` requires that the referenced `findings` artifact exists and is
  well-formed. The rubric is forwarded to the movement's performer via `brief`; the core
  validates structure, not truth. A missing or malformed findings artifact is an
  acceptance execution failure.
- Findings schema (minimum): `id`, `rubric`, `summary`, `evidence: [{path, line}]`,
  `blocking: bool`.
- One or more `blocking: true` findings put the movement in **`CONTESTED`**;
  `human_gate: on_contested` then routes it to `WAITING_HUMAN`. `blocking` is reviewer
  judgment — it opens the human gate but never counts as machine verification.

**Failure feedback.** When an attempt fails quality acceptance, the core records
diagnostic artifacts — the rejected candidate change set, the acceptance report, adapter
failure detail, and test output references — and passes them to the next attempt via
`request.feedback`. Feedback is read-only diagnosis; rejected changes are never applied
to the base. Without this, a clean-base retry with no session hint repeats the same
mistake blind.

**CLI v0.1.**

```text
partitur init            # create .partitur/ and its .gitignore (runs/)
partitur validate        # compile the score (rules in §2)
partitur run             # start a run (interview first while draft)
partitur status          # current movement/attempt states, pending decisions
partitur logs --jsonl    # stream the journal
partitur answer          # answer pending questions
partitur approve         # approve/reject amendments, gates, finalization
partitur amend           # propose an amendment from the CLI
partitur cancel          # cancel an attempt or the run
partitur resume          # resume from snapshots after interruption
partitur promote-score   # copy a run's score revision to partitur.yaml (CAS, §1)
partitur apply           # apply the final change_set to the user's checkout (§5)
```

## 8. Out of scope for v0.1

Parallel scheduling, automatic casting/routing, GUI (a later client of the same core
commands — not v0.1), lifecycle hooks, vector stores, nested delegation, secret storage
(meaning: no vault; opaque session state is still handled per §4 privacy), prompt
libraries, dirty-source runs, non-empty `side_effects`. See CONCEPT's minimal-harness
test; each of these fails it or belongs to score/extension space.
