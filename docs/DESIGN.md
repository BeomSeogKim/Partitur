# Partitur — Design v0.2

> Implementation-level design for the core. Concept and principles:
> [`docs/CONCEPT.md`](CONCEPT.md). This document covers the score/cast/adapter contracts,
> the workspace and run state models, the acceptance runner, verification and shipping,
> and the file layout.

**Normative status.** This document is the **sole normative specification**. It
supersedes Design v0.1 and incorporates the full normative content of decisions
[0001](decisions/0001-amendment-envelope.md) and
[0002](decisions/0002-verification-semantics.md), which are retained **unchanged as
historical rationale only**. Their "this document governs" clauses are discharged: where
this document and a decision record differ, this document governs. Consult the decisions
for *why* a rule exists, never for what the rule is.

**Version relationships.** Independently versioned surfaces:

| Surface | Version | Note |
|---|---|---|
| This document | 0.2 | supersedes 0.1 |
| Score schema (`score:`) | `"0.2"` | **breaking** — `verification` and acceptance-criterion shapes are incompatible with 0.1 |
| Cast schema (`cast:`) | `"0.1"` | unchanged |
| Adapter protocol (`protocol:`) | `1` | monotonically extended in §4; absent booleans decode as `false`, so no bump |
| Adapter result envelope | `1` | unchanged (adapter-internal) |

**Rules awaiting a bounded implementation spike.** Three rules below are specified as
the intended contract but must be confirmed against real behaviour before they are
frozen; each is marked **[SPIKE]** at its definition: canonical encoding and numeric
range (Appendix A), fan-in tree composition (§5), and the execution-driver lease and
wake-up mechanism (§6). A spike that contradicts the specified rule amends this document
before implementation proceeds.

## 0. Ground rules inherited from the concept

- The core is a small execution protocol in Go: a thin supervisor that owns state,
  authority, and evidence. Agent output is streamed incrementally with bounded protocol
  buffers; the core never accumulates an attempt's full output in memory.
- v0.2 executes movements **sequentially**. No parallel scheduling, no daemon.
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
      manifest.yaml            # rebuildable projection: score revision + hash, resolved
                               # cast pins, per-attempt enforcement record, artifact index
      scores/revision-<n>.yaml # immutable score snapshots (see below)
      resolved-cast.yaml       # the fully resolved cast used by this run
      artifacts/<logical-output-id>/<attempt-id>
                               # immutable artifact instances (§2 identity, hashed at
                               # record time); a retry never overwrites earlier evidence
      session/                 # session hints, mode 0600 (see §4 privacy)
      driver.lease             # execution-driver lease (§6); absent when no driver runs
      attempts/<attempt-id>/
        stderr                 # sanitized vendor/adapter diagnostics (§4 privacy)
        trace.jsonl            # protocol trace
        output/                # the attempt's always-writable output_dir (§5)
~/.config/partitur/cast.yaml   # user-global cast override
<install>/default-cast.yaml    # first-party factory cast (versioned data file with
                               # metadata: date, tested adapters/models, rationale)
```

Git refs the core owns — never user-visible branches, and never garbage-collected while
the run exists:

```text
refs/partitur/runs/<run-id>/attempts/<attempt-id>/changeset   # storage handle (§5)
```

**Run data retention.** v0.2 defines no run-deletion command. Run directories and
`refs/partitur/**` are retained; pruning is future scope. When pruning is specified it
must refuse an active run, a run whose application projection is `APPLYING` or
`RECOVERY_REQUIRED`, and must require an explicit discard for a `SUCCEEDED` run whose
candidate is not yet `APPLIED`.

**Authority within run state.** `journal.jsonl` is authoritative for lifecycle history.
`manifest.yaml` is a rebuildable projection/checkpoint of the journal. Immutable score
snapshots and artifact instances remain authoritative for their respective contents. Crash
recovery replays the journal and rebuilds the manifest. Every state a command reads —
run/movement/attempt states, marks, pending decisions, budget consumption, the
application and promotion projections — is a **pure function of the journal** (Appendix
B). Nothing mutable is stored, so no projection can drift from the evidence.

**Journal durability.** Each event class declares whether its append must be fsynced
before the core proceeds (Appendix B, `sync` column). Fsync is required for every event
that authorizes an irreversible effect or that a recovery rule keys on; ordinary
`log`/`progress` mirroring is not synced.

**Torn tails.** A crash can leave a partially written final line. On replay:

- A trailing line that fails to parse is truncated **only if it is the last line** in the
  file, and the truncation is recorded as `journal.tail_truncated` at the next append.
- An unparseable line anywhere else halts recovery — this is corruption, not something to
  repair silently.
- `seq` is contiguous and strictly increasing within a run. A truncated tail therefore
  never leaves a gap, because the truncated event was never observed by anything.

**Artifact instances.** Score-declared output ids are *logical* ids. Each emission is a
distinct immutable instance identified by `(logical_output_id, attempt_id)` — the
`artifact_instance_id` — stored at
`artifacts/<logical-output-id>/<attempt-id>`. A logical output may be emitted **at most
once per attempt**; a second emission is the deterministic protocol error
`duplicate_artifact_instance`. A declared output never emitted is caught by the
artifact-integrity criterion (§7). Journal entries, hashes, marks, and finding overrides
reference instances. A downstream movement's logical input resolves to the instance from
the **completing successful, non-superseded attempt**, so retries and revision re-runs
never collide with earlier immutable evidence.

**Artifact recording atomicity.** Recording an artifact follows a fixed order: copy to a
temporary file → compute hash (`sha256:<hex>`) and durably flush → atomic rename into
`artifacts/<logical-output-id>/<attempt-id>` → append `artifact.recorded` (fsynced) →
update the manifest projection. The core copies with a stable-stat check and rejects an
artifact notification that is undeclared, duplicated, or whose file changes during
copying. On recovery, an orphan artifact file without an event is quarantined; an event
whose file is missing is a recovery error that halts the run.

**Ref/journal ordering.** A change-set ref (§5) is created before its authorizing event
is appended. On recovery, a ref with no authorizing event is quarantined and cleaned; an
event whose ref is absent is a recovery error that halts the run — symmetric with
artifacts and snapshots.

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
- Removal/tombstoning is not supported in v0.2.
- Adapters are resolved on demand for the adapter ids the resolved cast references —
  the core never scans `PATH` for everything that looks like an adapter.

The run manifest pins the resolved performer, adapter version, and model for every part —
the *intended* binding. Because an infrastructure fallback changes who actually served an
attempt, the manifest additionally records, **per attempt**, the adapter id and version
that served it, the model used, and the enforcement posture in effect, including which
constraints were advisory (§3, §4). Marks, execution-dependency hashes, and audit output
read the per-attempt record, never the part-level binding.

## 2. Score schema v0.2 (`partitur.yaml`)

```yaml
score: "0.2"                    # schema version
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

verification:                   # two separate concepts — see below
  expectation:
    intent: write-basic-tests   # write-basic-tests | pass-existing-tests | none
                                #   work intent; forwarded to briefs as
                                #   brief.verification_expectation. 'none' is always
                                #   explicit. NOT ship policy.
    apply_gate:                 # which evidence `apply` demands — require XOR waived
      require: [verified, reviewed]     # non-empty, duplicate-free subset of
                                        # {verified, approved, reviewed}
      predicates: [no_unresolved_blocking_findings]   # optional; closed enum, §8
  final_movement: check         # required iff apply_gate is not waived; forbidden when
                                # waived. The run's terminal sink — §8.
  # — or, for a deliberately ungated run —
  #  expectation:
  #    intent: none
  #    apply_gate:
  #      waived: true
  #      reason: "spike; will be rewritten"   # non-empty reason mandatory

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
        - id: unit-tests        # explicit, unique within the movement — marks bind to it
          run: ["go", "test", "./..."]
        - id: vet
          run: ["go", "vet", "./..."]
  - id: check                   # verification.final_movement — the run's terminal sink:
    part: verify                #   transitively needs every non-draft movement, has no
    needs: [build]              #   downstream, and never holds repo_write
    grants: [repo_read, shell]
    instruction: |
      Review the change against the goal. Report findings with file, line,
      violated criterion, and a counter-example where possible.
    inputs: [change-set]
    outputs:
      - id: review-findings
        kind: findings
    acceptance:                 # apply_gate.require is [verified, reviewed], so this
      hard:                     # movement must carry ≥1 hard AND ≥1 review criterion
        - id: full-suite        #   (achievability, §8)
          run: ["go", "test", "./..."]
      review:
        - id: goal-review
          findings: review-findings   # typed requirement: this findings artifact must
          rubric: [requirement_coverage, regression_risk]   # exist and be well-formed
      human_gate: on_contested  # always | on_contested | never

policy:
  allowed_paths: ["internal/**", "cmd/**", "**/*_test.go"]
                                # unordered union of positive glob patterns; duplicates
                                # are a compiler error and order carries no semantics
  side_effects: []              # v0.2 accepts only an empty list; non-empty values are
                                # rejected until a typed side-effect registry is specified
  budget:
    active_wall_clock_min: 90   # active execution time only — adapter runs, acceptance,
                                # retries, fallbacks. WAITING_HUMAN and stopped time are
                                # excluded. Consumed time is persisted via journal events;
                                # each attempt receives the remainder at its start.
    retries_per_movement: 2     # quality-retry budget per movement — see §3
  amendment:
    auto: "off"                 # off | envelope; default off. envelope = only
                                # provably-monotone changes inside the bounds below are
                                # auto-approved (§9); everything else waits for a human.
```

**Path policy semantics.** `allowed_paths` patterns are repository-relative POSIX-style
paths; `**` recurses into directories; paths are canonicalized before matching; case
sensitivity is fixed by core rule (case-sensitive), independent of the worktree
filesystem. Negation and exclusion syntax are **not** part of the language — `!` is a
literal character — which is what makes element removal a monotone narrowing (§9).

**Protected paths.** Independent of `allowed_paths`, no change set may modify the source
score, any cast file, anything under `.partitur/**`, the journal, `refs/partitur/**`, or
any control artifact. This is enforced **post hoc** on every candidate change set even
when an `allowed_paths` glob would admit the path; a violation fails the attempt in the
`grant_denied` class with no quality retry and no fallback. Without this, a score could
authorize movements that rewrite the authority that governs them.

**DRAFT phase contract.** While `status: draft`, only the movement named by
`draft.interview_movement` may run. It is read-only (no write grant permitted). It may
emit `log` and `progress` events, but its only *semantic* outputs are `question` and
`proposal` — `artifact` events are forbidden entirely in draft movements. All other
movements refuse to start until `status: finalized`.

`phase: draft` and `draft.interview_movement` are one fact expressed twice, so the
compiler reconciles them: **exactly one** movement may carry `phase: draft`, and it must
be the movement `draft.interview_movement` names (rule 8). A draft movement is excluded
from the final movement's dependency closure and contributes no candidate change set.

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

**Amendment format v0.2.** A `proposal` carries:

```text
{
  base_revision, base_hash,        # stale-rejected if either mismatches
  operations: [...],               # RFC 6902 JSON Patch, applied to the canonical JSON
                                   # representation of the YAML score (Appendix A)
  reason,
  evidence?: [artifact_instance_id],
  claimed_impact: { ... }          # same shape as actual_impact, §9
}
```

`claimed_impact` carries no authority: the core recomputes the authoritative
`actual_impact` by **typed comparison of the two validated score ASTs** — never by
inspecting the RFC 6902 operations or a generic JSON diff — and rejects the proposal if
the claim is narrower on any component. The shape and the containment rules are defined
in §9. Approved patches apply only to the run's snapshot chain (see §1 for promotion to
the root score). The full admissibility pipeline, the auto-approval envelope, and the
effects of approval on a running episode are §9.

**Rules enforced by `partitur validate` (the score compiler).** All rules are checked and
reported together; validation is not short-circuited at the first error.

1. `status: finalized` requires every open question resolved or waived,
   `verification.expectation.intent` present, and a well-formed `apply_gate`.
2. A movement that requests a `repo_write` grant must carry ≥1 `hard` criterion or
   `human_gate: always`. (Keyed on the movement's grant, not the part's capability —
   a write-capable part may still play read-only movements.) This is a per-movement
   **floor**; the ship judgment is made on the application candidate, never summed over
   movements (§8).
3. `grants` ⊆ the part's `capabilities`. A `read_only` part can never receive
   `repo_write`. Read-only-ness is never inferred from instruction text or output names.
   (`policy` scopes *where* an authority applies — `allowed_paths`, `side_effects` — and
   never grants or withholds a grant kind; v0.1's "∩ what `policy` allows" referenced a
   ceiling the schema does not define.)
4. `needs` must form a DAG; part references must exist; every `inputs` entry must be an
   `outputs` id of a movement reachable through `needs`; **logical** output ids are unique
   within the score. (Runtime emissions are instances, §1 — uniqueness is a declaration
   rule, not a storage rule.) No output id may use the reserved `partitur.` prefix, which
   belongs to core-supplied inputs (§4).
5. Artifact paths are canonicalized inside the attempt's writable areas (§5); `..`,
   symlinks escaping them, and absolute paths outside them are rejected.
6. Unknown fields in the core namespace are an error; adapter-specific data lives only
   under `extensions.<adapter-id>`.
7. An `acceptance.review` entry must reference a `findings`-kind output of the same
   movement.
8. Exactly one movement carries `phase: draft`, and it is the one
   `draft.interview_movement` names. A draft movement may not hold `repo_write`.
9. Every acceptance criterion carries an `id` unique within its movement.
10. `allowed_paths` contains no duplicate pattern.
11. **Apply-gate achievability** — a finalized score whose gate can never be satisfied is
    rejected (§8): `require ∋ verified` ⇒ the final movement declares ≥1 hard criterion;
    `require ∋ reviewed` or any predicate present ⇒ it declares ≥1 review criterion with a
    typed `findings` output; `require ∋ approved` ⇒ it declares `human_gate: always`.
12. **Final-movement closure** — `apply_gate.waived` ⇒ `verification.final_movement` must
    be omitted; otherwise it must be declared, must not hold `repo_write`, must
    transitively depend on every non-draft movement via `needs`, must have no downstream
    movement, and no non-draft movement may sit outside its dependency closure.
13. No numeric value anywhere in the score falls outside the canonical safe range
    (Appendix A).

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
  v0.2 (uniform rule; a cast-level opt-in may come later).
- **Quality failure** (`task_failed` or acceptance failure): consume one retry and try
  again with the **same** performer from a clean base. Quality failures never trigger
  fallback — a different model is not the fix for a failed test; amendments and humans
  are.
- The movement fails when quality retries are exhausted, or when the fallback chain is
  exhausted by infrastructure failures. The chain never revisits an earlier performer.
- **Attempts per movement.** There is no closed-form bound:

  ```text
  total attempts = initial
                 + quality retries consumed          (≤ retries_per_movement)
                 + infrastructure fallbacks consumed (≤ fallback_count)
                 + revision-triggered superseded restarts   (unbounded by retry policy)
  ```

  Revision-triggered restarts (§9) consume no quality retry and are limited **only by the
  remaining active wall-clock budget**. `remaining_retries == 0` forbids only new
  *quality-retry* attempts; `remaining_time == 0` starts **no** new attempt of any kind.
- `retries_per_movement` is a per-movement total shared across the fallback chain — a
  fallback performer does not receive a fresh retry budget.
- Every retry and fallback starts from the same clean base and the same input artifact
  instances.
- `grant_denied` (including `candidate_mismatch` and protected-path violations), review
  findings, human-gate rejection, and user cancellation trigger neither retry nor
  fallback.

## 4. Adapter protocol v1

**Packaging.** An adapter is an executable `partitur-adapter-<id>` found on `PATH` or via
explicit config. Adapters are explicitly enabled; nothing is auto-discovered. v0.2
supports macOS and Linux; Windows is out of scope.

**Process model and framing.** The core spawns the adapter **per attempt**. Transport is
JSON-RPC 2.0 messages as **UTF-8 JSON Lines on stdout**: one request, response, or
notification per line; newlines inside values are escaped; control frames have a fixed
size cap — large content travels as artifact files, never inline. `stderr` is
diagnostics only, captured to the attempt directory. The adapter must keep reading stdin
during `execute` so a `cancel` request can be received; after a grace timeout the core
terminates the process, and force-kills after a further timeout. No daemon in v0.2.

**Session hints and privacy.** Session continuity across attempts is carried by an
opaque `session_hint` the adapter may return and the core may pass back — always an
optimization, never required state (conformance test: resume with the hint removed).
Because hints are opaque they may contain resume tokens: the core never writes them to
the journal, manifest, or protocol trace; they live in `runs/<id>/session/` with mode
`0600` and are deleted with the run. Adapter conformance requires that hints carry no
long-lived credentials and that diagnostics never echo them.

**Methods.** On the wire, `run_id`, `movement_id`, and `attempt_id` are opaque JSON
strings; the journal's numeric attempt counter (§6) is a core-internal projection, not
the protocol identifier.

```text
probe() -> {
  protocol: 1,
  adapter: { id, version },
  capabilities: {
    repo_read, repo_write, shell, network: bool,
    resumable_sessions: bool,
    models: [ { id, aliases? } ]
  },
  enforcement: {                # what the adapter/vendor agent actually enforces.
                                # Absent booleans decode as false — fail closed.
    path_grants: bool,          # confines writes AND reads to granted paths
    read_only: bool,            # can run with all repository writes disabled
    network_grants: bool,       # can disable data-plane network
    shell_grants: bool,         # can disable command execution entirely — true only if
                                #   EVERY command-execution route is closed
    read_grants: bool           # can deny repository reads
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
      global_invariants,        # deterministic projection computed by the core from
                                # goal, finalized resolutions, and policy — not a
                                # separate score field
      outputs: [                # this movement's declared outputs — the only
        { artifact_id, kind }   # artifact ids the attempt may emit
      ]
    },
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
proposal   { id, amendment,             # structured amendment (§2 format); core
             requires_decision }        # validates; id is stable within the attempt
question   { id, question }             # see blocking handshake below
```

Code changes are never communicated as `artifact` events — the core itself captures them
from the worktree as a `change_set` (§5). Draft movements may not emit `artifact` at all.

**Blocking handshake for questions and proposals.** v0.2 has no daemon, so nothing waits
in memory for a human:

- An adapter that needs answers emits `question`(s) (or a `proposal` it cannot proceed
  without, marked `requires_decision: true`), returns `outcome: waiting_human` with
  `pending_decision_ids`, and exits. A blocking proposal's `id` is a valid member of
  `pending_decision_ids`, alongside question ids. The attempt ends `BLOCKED` (a terminal
  attempt state — no process stays alive).
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
`grants.network` governs the agent's data-plane access — tools, spawned commands, web
search, MCP and similar — never the vendor's own control-plane connection to its model
provider, which is always permitted.

**The fail-closed predicate.** A capability being *available* never implies its denial is
*enforceable*, so each withheld authority names the enforcement dimension that must back
it. For a given movement's effective grants, the attempt may start only if every row that
applies is satisfied:

| Withheld authority | Required enforcement |
|---|---|
| `repo_write` not granted | `read_only` |
| `repo_write` granted but path-scoped | `path_grants` |
| `repo_read` not granted | `read_grants` |
| `repo_read` granted but path-scoped | `path_grants` |
| `shell` not granted | `shell_grants` |
| `network` not granted | `network_grants` |

An unsatisfied row fails closed, or — with `allow_advisory_enforcement: true` for that
performer — proceeds with the exact unmet dimensions recorded for that attempt and
surfaced by `validate` and `status`. `validate` evaluates this predicate against probed
adapters, which is why genuine validation requires cast resolution and probing (§7).

**Reserved input artifacts.** Two contracts the performer cannot reconstruct on its own
are delivered through the existing `inputs` mechanism rather than as new wire fields, so
the frame stays small and the 1 MiB cap is never at risk. Both are read-only, live outside
the worktree, and are never declared in the score:

```text
artifact_id: partitur.score-base
kind:        partitur/score-canonical+json;v=1
hash:        <base_hash>            # the snapshot head's canonical hash
                                    # Supplied to any movement that may propose an
                                    # amendment. A proposal's base_revision/base_hash
                                    # MUST be the ones carried here — this is what makes
                                    # a proposal stale-checkable (§9).

artifact_id: partitur.subject-tree
kind:        partitur/subject-tree+json;v=1
                                    # Supplied to any movement with a review criterion.
                                    # Carries the CORE-OBSERVED subject tree hash, the
                                    # findings schema version, and the rubric ids with
                                    # their required coverage. A review performer cannot
                                    # compute the tree itself — especially with no shell.
```

The reserved `partitur.` artifact-id prefix is forbidden to score-declared outputs. The
core-observed subject tree is always authoritative; a findings artifact's own claim is
compared against it and never trusted in its place (§8).

**Diagnostics privacy.** Vendor and adapter `stderr` may contain a session id the adapter
has not yet recognized, so raw passthrough would violate the guarantee that diagnostics
never echo hints. Adapters MUST buffer `stderr` to a bounded size and sanitize it against
every known sensitive value **before** it is written anywhere the core persists. The core
treats received diagnostics as sensitive regardless, and never copies them into the
journal, the manifest, or the protocol trace.

**Process supervision.** The adapter and the vendor process it spawns are separate process
groups, so hard-killing a wedged adapter can orphan the vendor group. Termination is
therefore layered: the adapter MUST handle `SIGTERM` by terminating its vendor process
group before exiting, and the core's outer grace period MUST exceed the adapter's own
`SIGTERM`→`SIGKILL` grace. If the adapter is fully wedged and is force-killed, the core
performs a documented process-tree sweep of the descendants it recorded at spawn time —
an orphaned vendor agent holding repository authority is not an acceptable end state.

## 5. Workspace model v0.2

**Run preconditions.** A run requires a Git repository. At run start the core records
the source base commit/tree id in the manifest, together with the score and resolved
cast hashes. If the source tree has tracked or untracked changes (beyond ignored
`.partitur/` run data), the run is refused — dirty-source support is future scope. This
guarantees the agents' base is exactly what the user sees.

Write attempts never modify the user's checkout directly. v0.2 uses **Git worktrees**:

- Each attempt gets a fresh worktree built from the **approved results of its dependency
  movements** (the clean base), plus an always-writable `output_dir` at
  `runs/<run-id>/attempts/<attempt-id>/output/` — outside the worktree — for declared
  artifacts. `read_only` means the repository worktree is read-only; ordinary artifacts
  (documents, findings) are written to `output_dir` only.
- **Change-set identity.** A `change_set` is captured as a **core-created Git checkpoint
  commit** in the isolated worktree, but the commit OID is only a *storage handle*: commit
  metadata (timestamps, message, parents) makes it a non-content identity. The semantic
  identity is

  ```text
  change_set_id = H("partitur/change-set", { base_tree, result_tree })   # Appendix A
  ```

  recorded together with the producing attempt id and the ref that pins the commit
  (`refs/partitur/runs/<run-id>/attempts/<attempt-id>/changeset`, §1). A patch may be
  exported for inspection or application, but it is never the identity.
- **Fan-in. [SPIKE]** When a movement depends on several movements, contributing change
  sets are composed in a deterministic order — topological, ties broken by declaration
  order in the score — each merged **against its own recorded base**, not against a shared
  common base, because a dependency's change set was itself produced on top of its
  dependencies:

  ```text
  T := the run base tree
  for each contributing change set C, in deterministic order:
      T := three_way_merge(base = C.base_tree, ours = T, theirs = C.result_tree)
  ```

  "Without duplication" means skipping a change set whose `change_set_id` has already been
  applied in this composition — **content identity, not commit ancestry**, since
  checkpoint commits are storage artifacts whose topology may carry unrelated history.
  READY-movement scheduling likewise follows declaration order.

  A merge conflict yields exactly one outcome: the core records
  `composition.conflicted` and puts the movement in `WAITING_HUMAN`. It never merges
  silently and never picks a side. Changes from failed, cancelled, or superseded attempts
  are never candidates.

  Because `candidate_id` is a *content* identity (§8), the composition algorithm and the
  Git merge implementation version are recorded in
  `candidate_composition_dependency_hash` — otherwise a Git upgrade could silently alter
  the candidate tree for identical inputs. **[SPIKE]** the exact merge invocation must be
  confirmed against rename/rename, rename/delete, file modes, symlinks, submodules,
  `.gitattributes`, binary files, configured merge drivers, and macOS/Linux consistency
  before this rule is frozen.
- Partial changes from failed, cancelled, or superseded attempts are discarded — they
  never leak into the next attempt. A fallback performer starts from the same clean
  base, never from the failed performer's dirty workspace.
- **Read-only post-hoc verification.** For every movement whose effective grants exclude
  `repo_write`, the core verifies at adapter exit that the worktree is unchanged. A Git
  tree comparison alone is insufficient: the check covers tracked content, untracked
  files, symlink targets, file modes, and the protected paths of §2. A violation is
  classified in the `grant_denied` class with no quality retry and no fallback. For the
  final movement the same check is expressed as `candidate_mismatch` against the recorded
  candidate `result_tree` (§8).
- A movement that produces no repository changes records no change set. The final
  verification movement and other read-only movements therefore never produce a
  provisional change set — the acceptance chain of §7 captures one only for movements
  holding `repo_write`.
- Applying the final result to the user's checkout is the explicit `apply` command —
  never a side effect of a movement finishing (§8).

## 6. Run state model v0.2

Three lifecycle levels, deliberately separate:

```text
Run:      RUNNING | WAITING_HUMAN | SUCCEEDED | FAILED | CANCELLED
Movement: PENDING | READY | RUNNING | WAITING_HUMAN | SUCCEEDED | FAILED | CANCELLED
Attempt:  STARTING | RUNNING | VERIFYING | COMPLETED | BLOCKED | FAILED | CANCELLED
          | SUPERSEDED
```

Plus two **per-run projections on separate axes**, which are not lifecycle states — a run
stays terminal `SUCCEEDED` forever regardless of whether its result has shipped (§8):

```text
Application: NOT_APPLIED | APPLYING | APPLIED | FAILED_CLEAN | RECOVERY_REQUIRED
Promotion:   NOT_PROMOTED | PROMOTING | PROMOTED | RECOVERY_REQUIRED
```

- **`VERIFYING` separates vendor completion from attempt success.** The adapter's
  `outcome: completed` means only that vendor execution ended — the adapter never judges
  success (§4). The core records `performer.completed` and the attempt enters `VERIFYING`
  while the change set is captured, acceptance runs, and any required human gate is
  decided. `attempt.completed` — and thus `COMPLETED` — is recorded **only** after all of
  that succeeds. Conflating the two would let a movement look successful on the strength
  of an adapter's self-report.
- `BLOCKED` is a **terminal** attempt state: the attempt exited while waiting on human
  decisions; the follow-up work happens in a **new** attempt. Only movements and runs
  use `WAITING_HUMAN`.
- A movement failure fails the run only when retries and fallbacks are exhausted;
  `SUPERSEDED` exists only at the attempt level (steer, instruction revision, or an
  approved amendment → new attempt).
- `CONTESTED` is **not** a state at any level. It is a value of the `review_outcome`
  projection (§8); a contested movement reaches `WAITING_HUMAN` through its human gate
  like any other.

**Attempt identity.** `attempt_id` is an opaque string, unique within the run, and is the
identifier used on the wire (§4), in journal events, and in every mark binding.
`attempt_number` is a separate per-movement ordinal used only for display. The two are
never interchanged: v0.1's journal example implied a numeric `attempt_id`, which
contradicts the protocol's opaque-string requirement.

**Budget accounting.** `active_wall_clock_min` bounds active execution only — adapter runs,
acceptance, retries, fallbacks, and revision restarts — excluding `WAITING_HUMAN` and
stopped time. Because revision restarts are bounded by time alone (§3), the accounting must
be exact and crash-safe:

- Active time is delimited by paired, fsynced `execution.started` / `execution.stopped`
  events carrying a monotonic-clock reading and a wall-clock timestamp. Consumption is the
  sum of completed intervals; an in-flight interval is charged against the remainder for
  admission decisions.
- Consumed history is never retroactively edited. A budget decrease (§9) yields
  `remaining_time = max(0, new_cap − consumed)`.
- On recovery, an `execution.started` with no matching `execution.stopped` is an uncertain
  interval. It is closed with `execution.stopped {reason: recovered, charged: conservative}`
  and charged at its **maximum plausible duration** — the wall-clock gap to the recovery
  moment. Fail-closed: an uncertain crash interval costs budget rather than silently
  granting free execution time.
- Each attempt receives the remainder at its start (`request.budget`).

**Active-run exclusivity and the two locks.** These are different concerns and must not
share a mechanism:

- A **repository state lock** (OS file lock) guards state mutations — journal appends, ref
  updates, snapshot writes, CAS operations. It is held only for the duration of a mutation,
  never across a human wait and never across an adapter execution.
- A per-run **execution-driver lease** (`runs/<run-id>/driver.lease`, §1) is held for the
  whole active execution episode by whichever process is driving attempts. **[SPIKE]** A
  PID alone is insufficient — PIDs are reused and a stale file would masquerade as a live
  owner — so the lease records verified owner identity: PID plus process-start identity
  plus a random incarnation token, all re-verified before the lease is honoured.

A run is *active* while nonterminal (`RUNNING` or `WAITING_HUMAN`). v0.2 allows one active
run per repository: `partitur run` refuses to start while one exists (resume or cancel it
first). Commands accept an explicit run id or select the unique active run. The nonterminal
journal state is the logical guard; the lease guards concurrent *drivers* of the same run.

**The control channel. [SPIKE]** With no daemon, nothing waits in memory, so out-of-band
control — cancellation, and revision approval that supersedes a running attempt — needs a
durable path that reaches a driver mid-execution:

1. The requesting command appends the authoritative control event under the state lock
   (`cancel.requested`, or `amendment.approved` for a supersede).
2. It then wakes the verified current lease holder as a **best-effort latency
   optimization** — never as the mechanism of record.
3. The driver watches control state continuously while an adapter execution is pending.
   Polling only between criteria cannot interrupt a long `execute`, so this is a
   continuous watch, not a checkpoint.
4. On observing the request the driver issues protocol `cancel`, awaits the `execute`
   response, then applies the outer termination grace and process-tree sweep (§4).
5. If no driver is alive, the next `resume` observes the durable request and closes the
   attempt out terminally **without launching another attempt**.

The journal is the authority; the signal only reduces latency. Because supersede needs the
identical mechanism, this is one general control channel rather than a cancel-only feature.

Event envelope (`journal.jsonl`, control-grade for a later GUI):

```json
{ "event_id": "evt-42", "seq": 42, "ts": "...",
  "run_id": "...", "score_revision": 3,
  "movement_id": "build", "part_id": "implement", "attempt_id": "att-7f3c",
  "type": "artifact.recorded", "causation_id": "evt-40", "payload": {} }
```

- `movement_id`, `part_id`, `attempt_id` are optional — run-level events omit them.
- `attempt_id` is an opaque string, never the ordinal.
- `causation_id` references another event's `event_id`, and is the **idempotency key** for
  every derived or recovery-synthesized event (Appendix B): appending a derived event twice
  for the same causation id is a no-op, which is what makes recovery replayable.
- `event_id` and `seq` are allocated together by the single writer; `seq` is contiguous and
  strictly increasing within the run.
- Pending decisions are represented as an append-only pair — `decision.requested` /
  `decision.resolved` — never as a mutable record; readers project the pair into a
  "currently pending" list. Amendment decisions are the one exception: their terminal
  events resolve their own decision directly and no separate `decision.resolved` is ever
  appended on any amendment path (§9).

The complete registry of authoritative events — payload, fsync requirement, idempotency
key, legal source state, and resulting projection — is **Appendix B**, which is normative.
An event type absent from Appendix B does not exist.

## 7. Acceptance runner and CLI v0.2

When an attempt returns `completed`, the core runs acceptance **in a fixed order**,
before the worktree is removed. The attempt is in `VERIFYING` throughout (§6):

```text
performer.completed                       (vendor execution ended — not success)
→ artifact instances already recorded as immutable copies (§1)
→ core captures the provisional change_set checkpoint (§5) — repo_write movements only
→ read-only / protected-path post-hoc verification (§5)
→ acceptance.started        (binds subject_tree + acceptance_spec_hash, fsynced)
→ per criterion: criterion.started → criterion.completed {PASS | FAIL | ERROR}
→ acceptance.failed  (first FAIL/ERROR — short-circuits)
   | acceptance.evaluation_completed  (every criterion ran and passed)
→ human gate (if required)
→ attempt.completed; movement SUCCEEDED; artifacts and change_set approved
```

**Criterion identity.** Every criterion carries an explicit `id`, unique within its
movement (§2 rule 9). Its `criterion_spec_hash` is the canonical hash of the criterion's
semantic fields — id, kind, argv, timeout, artifact reference or `expected_hash`, rubric,
as applicable. The movement's `acceptance_spec_hash` is the canonical hash of the whole
acceptance block: ordered criteria plus `human_gate`. Both are recorded with the evidence
so a mark says exactly which specification it proved (Appendix A).

**Subject binding.** `acceptance.started` records the `subject_tree` — the tree hash the
acceptance runs against — before any criterion runs, and every criterion event repeats it.
Marks are therefore bound to at least
`(run, movement, attempt, score_revision, subject_tree, acceptance_spec_hash)`. Grades are
derived **only** from attempts carrying `acceptance.evaluation_completed`; evidence from
`FAILED`, `CANCELLED`, or `SUPERSEDED` attempts contributes nothing to current marks,
though it remains in the journal as history.

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
- `artifact` — the declared output was emitted, its `kind` matches, and its recorded hash
  matches the immutable instance (manifest integrity). An optional `expected_hash` is
  allowed for pre-known content. A declared output never emitted fails here.

**Acceptance runner authority.** The runner is core-executed and its environment is fixed
by the core, not inherited from the user's shell: the working directory is the attempt
worktree; the environment is a minimal allowlist plus the criterion's declared additions;
network access follows the movement's `network` grant; no shell interpretation occurs;
temporary files go under the attempt directory. The runner holds **no repository write
authority** beyond what the criterion command itself does, and its own side effects are
never classified as the movement's. A criterion that cannot be spawned, times out, or
whose runner crashes yields `ERROR`, not `FAIL`.

**Criterion outcomes and the no-override rule.** `ERROR` means the criterion produced no
verdict — spawn failure, timeout, runner crash. Both `FAIL` and `ERROR` block movement
completion and block VERIFIED, and **neither is human-overridable**: a human cannot
complete a movement over a red or errored hard criterion. The paths out are a retry (both
consume the movement's quality-retry budget, §3) or an audited score amendment changing the
criterion — never envelope-eligible, applying only to a new revision and a new attempt, and
subject to §9's prohibition on rewriting a succeeded movement's dependencies. A later
clean-base attempt whose criteria pass may become VERIFIED; earlier failed attempts stay
visible in `status`, so flakiness is surfaced rather than laundered.

**Review criteria** (typed model evidence — never machine verification):

- `acceptance.review` requires that the referenced `findings` artifact instance exists and
  is well-formed. The rubric and the core-observed subject tree are forwarded to the
  performer via the reserved `partitur.subject-tree` input (§4). The core validates subject
  binding, rubric completeness, and schema — **never truth**.
- The findings artifact is JSON, `kind: findings`, schema
  `partitur/findings+json;v=1`:

  ```text
  {
    schema: "partitur/findings+json;v=1",
    subject_tree,                    # MUST equal the core-observed tree — compared, not trusted
    coverage: [                      # one entry per DECLARED rubric — no omissions
      { rubric, conclusion: examined_none_found | findings_raised, note? }
    ],
    findings: [
      { id, rubric, summary, evidence: [{path, line}], blocking: bool }
    ],
    provenance: { reviewer?, model?, adapter? }   # experiment metadata only; no rule may
  }                                              # condition on reviewer identity
  ```

- A findings artifact whose `subject_tree` disagrees with the core-observed tree, that
  omits a declared rubric, or that fails schema validation is **malformed** → acceptance
  execution failure. Zero findings still requires a typed per-rubric conclusion.
- `blocking: true` findings are reviewer *judgment*. They set `review_outcome` (§8) and
  open the human gate under `human_gate: on_contested`; they never count as machine
  verification, and unlike a red hard criterion they *are* legitimately human-overridable
  with a recorded reason.

**Failure feedback.** When an attempt fails quality acceptance, the core records
diagnostic artifacts — the rejected candidate change set, the acceptance report, adapter
failure detail, and test output references — and passes them to the next attempt via
`request.feedback`. Feedback is read-only diagnosis; rejected changes are never applied
to the base. Without this, a clean-base retry with no session hint repeats the same
mistake blind.

**Recovery.** Acceptance is interruptible at every step, so its resumption is governed by
the normative table in **Appendix C**, evaluated top-down with the first matching row
winning. It is fail-closed: before resuming any criterion the core verifies the worktree
tree still equals the `subject_tree` recorded in `acceptance.started`, and on mismatch
records `acceptance.failed {reason: recovery_subject_mismatch}`.

**Command authority.** Exactly one process drives attempts. The distinction is between
*recording a transition* and *launching execution* — several commands legitimately mutate
state, but only two ever start an adapter:

| Command | May mutate state | May launch an attempt |
|---|---|---|
| `init`, `validate`, `status`, `logs` | no | no |
| `answer` | records `decision.resolved` only | no |
| `approve` (gate) | records the gate transition; may atomically complete or fail an already-evaluated movement | no |
| `approve` / `amend` (amendment) | may atomically write a snapshot and supersede attempts (§9) | no |
| `cancel` | appends the durable cancel request (§6) | no |
| `apply`, `promote-score` | own transactions (§8) | no |
| `run`, `resume` | full | **yes** — acquires the driver lease |

So §4's "the core starts a new attempt once decisions resolve" reads precisely as:
*resolution makes the run eligible to continue.* A live driver continues it; otherwise a
later `resume` does. `answer` never blocks waiting for work to finish.

**CLI v0.2.**

```text
partitur init            # create .partitur/, its .gitignore (runs/), and — only when no
                         #   score exists — a minimal draft partitur.yaml with an
                         #   interview movement. Never overwrites an existing score.
partitur validate        # compile the score (§2) AND resolve the cast and probe its
                         #   adapters, evaluating the fail-closed predicate (§4)
partitur run             # start a run (interview first while draft); takes the lease
partitur status          # states, pending decisions, marks with provenance (§8)
partitur logs --jsonl    # stream the journal
partitur answer          # answer pending questions
partitur approve         # approve/reject amendments, gates, finalization
partitur amend           # propose an amendment from the CLI
partitur cancel          # cancel an attempt or the run (§6 control channel)
partitur resume          # resume after interruption; takes the lease
partitur apply           # apply the candidate to the checkout (§8)
partitur apply --recover           # only from APPLYING | RECOVERY_REQUIRED
partitur promote-score             # copy a run revision to partitur.yaml (CAS, §1, §8)
partitur promote-score --recover   # only from PROMOTING | RECOVERY_REQUIRED
```

`--recover` is refused outside the two states that admit it, and the normal form of each
command is refused inside them. `apply` before `promote-score` is an enforced precondition,
not a convention (§8).

## 8. Verification and shipping

Verification produces **marks**; shipping applies one **application candidate**. Both are
projections of the journal (§6), computed on demand by `status` and `apply`, so they can
never drift from the evidence.

**Three grades, a set and never a ladder.** No ordering, no substitution: REVIEWED never
counts toward anything that asks for VERIFIED or APPROVED.

- **VERIFIED** — the attempt has `acceptance.evaluation_completed`, the movement declares
  ≥1 `hard` criterion, and every declared hard criterion is `PASS`. Zero hard criteria can
  never yield VERIFIED.
- **APPROVED** — a `decision.resolved` with `decision_type: human_gate` approves this
  movement, carrying `gate_id`, movement/attempt/`score_revision`, the exact `subject_tree`,
  the approval scope, and any overridden finding instance ids. An approval never carries
  over to another attempt, revision, or subject tree.
- **REVIEWED** — the movement declares ≥1 `review` criterion (compiler-enforced, §2 rule
  11, so it can never hold by vacuous truth) and every declared review criterion is
  satisfied by a well-formed, subject-bound findings artifact (§7). REVIEWED means "typed
  model review completed" — a *kind* marker, deliberately silent about the outcome.

The outcome lives in a separate projection, `review_outcome`: `CLEAN` (zero blocking
findings), `CONTESTED` (≥1 unresolved blocking finding, including partial overrides), or
`OVERRIDDEN` (blocking findings were raised and **all** were overridden by exact-subject
human decisions). Findings are immutable; an override links to the finding instance through
events and yields `REVIEWED + APPROVED` with `review_outcome: OVERRIDDEN` — it never makes
the blocker look like it was not raised.

**A human gate resolved as reject** terminates the movement:
`movement.failed {reason: human_gate_rejected, decision_id, subject_tree}`. It consumes no
quality retry and no fallback; for the final movement the same authoritative transition
takes the run to `FAILED`.

**Marks always carry their provenance.** A naked `VERIFIED` overstates a trivial criterion
(`["true"]` passes machinery, not goals), so `status` and `apply` always attach the
criterion count and ids/spec hashes, the `subject_tree`, and the `score_revision` — e.g.
`VERIFIED (2 criteria, tree abc123, rev 4)` — plus attempt history (`after 1 failed
attempt`), the findings instance id and `review_outcome` for REVIEWED, and the gate decision
id for APPROVED.

**One application candidate per run.** Two write movements, each VERIFIED on its own tree,
prove nothing about their combined result. The subject of shipping is therefore the
candidate, and every run reaching `SUCCEEDED` has exactly one — waived or not.

Composition takes the approved change sets of every successful, non-superseded `repo_write`
movement, deduplicated, in the deterministic fan-in order of §5, into
`(base_tree, result_tree)`:

```text
candidate_id = H("partitur/candidate",
                 { base_tree, result_tree, ordered_change_sets })      # Appendix A
```

It is a **content** identity, independent of score revision. A run with no write movements
records `result_tree == base_tree`. A composition conflict is handled **before** recording —
`WAITING_HUMAN` per §5 — never discovered at `apply` time.

Materialization is always **one authoritative journal event**, never a batch that could
tear on crash:

- **Non-waived:** the core composes the candidate after every `repo_write` movement has
  succeeded and before the final movement becomes READY.
  `application_candidate.recorded` itself constitutes the initial binding to the revision
  in its payload — no separate binding event is appended for it. It carries the candidate
  id, both trees, the ordered contributing change sets, the
  `candidate_composition_dependency_hash`, and the score revision at materialization.
- **Waived:** materialization is deferred until every non-draft movement has succeeded and
  is folded into a single `run.succeeded` event carrying the full candidate payload and
  binding. An active waived run therefore never holds a recorded candidate.
- The binding fact `{candidate_id, score_revision}` is **always a projection**, never its
  own authoritative event — initially from `application_candidate.recorded`, and thereafter
  from each candidate-compatible `amendment.approved` (§9). A crash can never leave a new
  revision permanently unbound.

**The final verification movement** (`verification.final_movement`, required unless the gate
is waived) is the run's **terminal sink**: it transitively depends on every non-draft
movement, has no downstream, and no non-draft movement sits outside its closure (§2 rule
12). Its effective grants exclude `repo_write`, and read-only enforcement applies to its
attempts regardless of the part's capabilities. Its worktree *is* the candidate
`result_tree`, so its marks bind to it naturally, and **its successful completion is the
run's transition to `SUCCEEDED`** — one atomic journal transition. There is consequently no
window in which the final movement has succeeded while the run is still amendable.

At adapter exit the core verifies the movement's worktree tree equals the recorded candidate
`result_tree`; a mismatch is `candidate_mismatch`, classified in the `grant_denied` class —
an unauthorized write to the tree under verification — failing the attempt with no quality
retry and no fallback. If the core's own composition disagrees with what it recorded, that
is internal corruption handled by recovery, never attributed to the performer.

**Apply-gate predicates** (closed enum, optional, meaningful only with review evidence
bound to the candidate): `no_unresolved_blocking_findings` passes when `review_outcome` ∈
`{CLEAN, OVERRIDDEN}`; `no_blocking_findings` passes only for `CLEAN`.

**The `apply` judgment** branches explicitly on the gate, and the schema makes the branching
exhaustive (a waived score has no final movement; a non-waived score must declare one):

- **`require` path** — succeeds only when, for every grade in `require` and every
  predicate, evidence exists whose `subject_tree` equals the candidate's `result_tree`,
  **and** the candidate binding, the expectation, and the final movement's marks all belong
  to the current head revision.
- **`waived` path** — grade, predicate, and final-mark checks are skipped entirely;
  `apply` requires only a current-head candidate binding and the validly recorded waiver.

Per-movement floors are unchanged: §2 rule 2 still guarantees that every **succeeded**
`repo_write` movement carries VERIFIED or APPROVED evidence under its completing,
non-superseded attempt. The apply judgment, however, is made on the candidate — never summed
over movements.

**`apply` preconditions** (under the state lock): no other active run exists in the
repository; the selected run is terminal `SUCCEEDED`; the checkout is clean and its
**computed working tree** equals the candidate `base_tree`, computed with a temporary Git
index over current tracked contents (modes, additions, deletions included) — the user's
index and `HEAD` are never modified.

Applying is not a run state. The run stays terminal `SUCCEEDED` forever and the separate
per-run **application projection** tracks shipping (§6). A normal `apply` starts a
transaction only from `NOT_APPLIED` or `FAILED_CLEAN`; in `APPLIED` it returns an idempotent
"already applied" result and records nothing — including for a no-write candidate.

```text
1. record + fsync apply.started {candidate_id, before_tree, touched_paths, recovery info}
                                                                        → APPLYING
2. dry-run the candidate patch, then apply it to the working checkout
3. recompute the working tree:
     == result_tree  → apply.completed                                  → APPLIED
     otherwise       → restore exactly the touched paths to the base tree,
                       re-verify the base hash, record apply.failed      → FAILED_CLEAN
4. if a crash interrupts and recovery cannot verify the base was restored → RECOVERY_REQUIRED
```

`RECOVERY_REQUIRED` is never inferred silently: after a crash the core inspects the checkout
under the lock and **appends the explicit recovery-required event** before recovery
proceeds, so the projection always has an authoritative cause (Appendix B).
`partitur apply --recover` then re-examines the checkout under the lock — tree equals
`base_tree` → `apply.recovery_resolved {outcome: rolled_back}` → `FAILED_CLEAN`; tree equals
`result_tree` → `apply.completed` → `APPLIED`; anything else stays `RECOVERY_REQUIRED`. The
core never claims "nothing was applied" unless it verified it.

**`promote-score`.** Only the latest revision of a `SUCCEEDED` run may be promoted, at most
one *successful* promotion, and only after `apply.completed` for the same candidate.
Promotion creates no marks and grants no apply permission, but it edits a tracked file
(`partitur.yaml`) and would otherwise move the checkout away from the candidate `base_tree`,
permanently blocking `apply` — which is why **`apply` then `promote-score`** is an enforced
precondition. Promotion is its own journaled transaction with a projection symmetric to
application (§6):

```text
score.promotion_started { expected_root_hash, target_snapshot_hash, candidate_id }
→ temp write + fsync + atomic rename of the root score
→ score.promoted + fsync
```

`promote-score --recover`, under the lock: root hash == target → complete `score.promoted`
idempotently under the original transaction id; root hash == expected → resume the *same*
transaction (a repeated `score.promotion_started` is an idempotent resume, never a second
promotion); anything else stays `RECOVERY_REQUIRED`, because the root was changed by
something else and the CAS can no longer decide.

`apply` evaluates the selected run's candidate against the expectation of **the same run
revision** — never against the root score.

## 9. Amendments

An amendment is the only way a score changes during a run. Validity is not an envelope
concern: a malformed proposal is rejected no matter who would approve it.

**Admissibility pipeline**, under the repository state lock, in this order — first failure
wins:

1. **Run lifecycle** — the run must be `RUNNING` or `WAITING_HUMAN`; a terminal run rejects
   with `run_terminal`. Score changes after a run ends happen by editing the root score or
   starting a new run; reopening a terminal run is out of scope.
2. **Stale re-check** — `base_revision` / `base_hash` must match the snapshot head.
3. **Reserved fields** — the v0.2 reserved set is exactly `{ /revision }`. An operation
   *touches* it if its `path` (or `from` for `move`/`copy`) equals it, descends from it, or
   is an ancestor of it. `test` operations on reserved pointers are permitted.
4. **Patch application** to the canonical JSON; any RFC 6902 error rejects.
5. **No-op check** — canonical equality of before/after rejects.
6. **Compiler validation** of the patched score (§2); invalid rejects.
7. **Impact computation and claim containment** — a claim narrower than the actual impact on
   any component rejects with `claim_narrower`.

Then two **feasibility checks**, applying to *every* approval path, auto and human alike:

8. **Executed-dependency feasibility** — failure is
   `amendment.rejected(executed_dependency_changed)`.
9. **Candidate compatibility**, when a recorded candidate exists — failure is
   `amendment.rejected(candidate_incompatible)`.

Only then does the **approval policy** apply:

| Condition (on the base snapshot) | Outcome |
|---|---|
| `base.status == draft` | route to human (`draft_phase`) |
| `base.policy.amendment.auto == off` | route to human (`auto_disabled`) |
| Patch changes `policy.amendment` semantics | route to human (`recognized_non_monotone`) |
| Otherwise (`auto == envelope`) | typed classification + state guards, below |

When a human later decides a routed proposal, the core re-runs steps 1–9 **under the lock at
decision time**, because the head, the compiler result, and the runtime state can all have
changed while the proposal waited. The envelope guards are recomputed *for the audit record
only*: they never re-route or block a human approval — routing a guard failure back to the
deciding human would loop. Rejection is permanent; a corrected proposal is a new proposal.
The proposal's origin (adapter or CLI) never affects any rule.

**Typed comparison, never a JSON diff.** Neither classification nor impact computation
inspects RFC 6902 operations or a generic diff — array-index ambiguity would make the same
before/after pair yield different results per diff algorithm. The core compares the two
validated score **ASTs**:

- `movements` and `parts` are matched by immutable id; criteria by their `id`. For envelope
  classification, movement count, ids, and order must be identical or the change is
  unclassified.
- `policy.allowed_paths`: only order-preserving element removal qualifies.
- `movements[].grants`: a duplicate-free set; only a strict subset qualifies.
- `budget.active_wall_clock_min`, `budget.retries_per_movement`: only a strict decrease
  between valid integers qualifies.
- Every other field must be canonically equal, or the change is unclassified.

**`actual_impact`** — the normative shape, which `claimed_impact` also uses (§2):

```text
actual_impact = {
  score_changes: [{ selector, operation: add | remove | replace,
                    before_hash?, after_hash? }],
  authority: {
    allowed_paths: {added: [...], removed: [...]},     # exact pattern strings
    grants: [{movement_id, added: [...], removed: [...]}],
    side_effects: {added: [...], removed: [...]}       # added must be [] in v0.2
  },
  budget: { active_wall_clock_min?: {from, to}, retries_per_movement?: {from, to} }
}
```

`selector` is a **stable semantic selector**, not a raw numeric JSON Pointer: id-keyed
collections are addressed by id (`/movements[id=build]/grants`), because a numeric pointer
becomes ambiguous the moment a collection is reordered. Id-keyed collections are sorted by id
before diffing and hashing. An id-less array that differs in any way is recorded as a single
`replace` of the whole field — one coarse selector, so a movement reorder is one `replace` of
`/movements`. Where raw order is semantically meaningful, the schema carries an explicit
order field rather than relying on position.

Containment (`actual ⊆ claimed`) is component-wise: every actual `(selector, operation)`
must appear in the claim (hashes are informative); patterns compare as exact strings with no
glob-subset reasoning; grant changes compare per movement id as set inclusion, separately for
`added` and `removed`; a budget claim contains the actual change iff it declares the same
direction and a magnitude ≥ the actual.

**Whitelist classes with runtime state guards.** Structural narrowing is *not* monotone with
respect to results that already exist — a grant removed after a movement produced its change
set does not un-produce it:

| Class | Structural rule | State guard |
|---|---|---|
| `NARROW_PATHS` | Order-preserving removal of an `allowed_paths` entry | No attempt of **any** movement whose effective `paths_ro` or `paths_rw` would change has started — `allowed_paths` scopes reads as well as writes |
| `NARROW_GRANTS` | Removal of a grant from one movement | That movement **and its transitive downstream** have not started |
| `BUDGET_DECREASE` | Strict decrease of a budget cap | Always allowed; remainders per §3 and §6 |

Guard failure routes to `runtime_scope_started`. Evaluation is a deterministic function of
*(base snapshot, patch, run execution state)* observed under the lock. **No model is ever
involved.**

**Executed-dependency feasibility.** An approval, whoever grants it, does not create evidence
that already-produced results satisfy the new score. The identity is bound to the *attempt*,
not abstractly to the movement:

```text
attempt.execution_dependency_hash =
  H("partitur/execution-dependency",
    { projection_version, actual_adapter_id,
      canonical score-derived semantic execute-request projection,
        using extensions.<actual_adapter_id> })
```

The projection is **exhaustively enumerated in Appendix A** — "includes at least" is not an
identity definition. It is keyed by the adapter that actually served the attempt (primary or
fallback), so changing `extensions.<fallback>` after a fallback attempt succeeded is detected.
It excludes per-attempt values: runtime identity, filesystem paths, `session_hint`,
`request.feedback`, and remaining budget.

**v0.2 rule:** an amendment after which any movement with a completed successful
(non-superseded) attempt has a different `execution_dependency_hash` — or which removes such a
movement — cannot be approved in place. The paths out are cancelling the run and starting a new
one, or amending something else. Changing `verification.expectation.intent` is always an
execution-dependency change, because it is forwarded into briefs. An atomic
invalidation-and-replay contract is future work.

**Candidate compatibility.** Once a candidate is recorded (§8), every approval must
additionally satisfy:

```text
candidate-compatible iff
  1. no movement with a completed successful (non-superseded) attempt has a different
     execution_dependency_hash under the patched score, and none is removed;
  2. the candidate composition identity is unchanged —
       candidate_composition_dependency_hash =
         H("partitur/candidate-composition",
           { base_tree, ordered contributing movement ids,
             corresponding change_set_ids, composition_algorithm_version })
     must equal the hash recorded with the candidate. Changes altering the composition —
     movement order, `needs`, contributor membership — are incompatible even if the
     resulting tree would coincidentally be identical;
  3. the patched score remains non-waived and its designated (or redesignated) final
     movement has no completed successful attempt.

failure reasons: succeeded_dependency_changed | composition_changed |
                 verification_episode_finished | verification_mode_changed
```

`require ↔ waived` transitions are permitted only **before** a candidate is recorded. After
it exists, changing the gate mode requires a new run — a mid-flight switch to `waived` would
leave an active run holding a candidate with no final verifier and no defined `SUCCEEDED`
transition.

This deliberately admits: `apply_gate.require`/`predicates` changes; redesignating
`final_movement` to a movement that has not started; the final movement's own `acceptance`
block while it has no completed successful attempt; `BUDGET_DECREASE`; `policy.amendment`
changes; and changes to not-yet-started read-only movements.

**Approval effects.** The core alone assigns the new revision as exactly
`base_revision + 1`. An approved revision change invalidates the **current execution
episode**, not merely running attempts:

- RUNNING attempts are superseded: the core records the approval, cancels/terminates the
  adapter through the §6 control channel, accepts no further protocol or artifact output
  from the superseded attempt once the new head is recorded, and charges the time until
  actual termination against the active budget.
- In `WAITING_HUMAN`, terminal attempts keep their history, but every pending decision or
  gate raised on the old revision is closed with `decision.obsoleted`. Gate approvals never
  carry across revisions.
- Execution resumes only in a new attempt on the new revision. Nothing keeps running, and no
  old-revision decision stays answerable under the old authority or budget.
- A candidate-compatible approval re-binds the candidate to the new revision and the final
  verification movement re-runs against the unchanged `result_tree` before `apply` can pass.
  If the remaining budget cannot fund that re-run, the candidate stays bound but unmarked and
  the run fails through the normal exhaustion rules — **a bound candidate is never an apply
  permission by itself**.
- Revision-triggered restarts consume no quality retry (§3).

**Journal taxonomy.** Routing is not an outcome; approval is a single authoritative event.
`amendment.approved {mode: auto | human}` is the **only** authoritative transition, carrying
the proposal id (and the human decision id when `mode: human`), base and new snapshot
hashes, the new revision, superseded attempt ids, obsoleted decision ids, and the re-bound
`candidate_id` where one exists. `attempt.superseded`, `decision.obsoleted`, and any
candidate-binding bookkeeping are **derived idempotently** from that one event, including at
recovery — a crash after its fsync can never leave the new head with stale pending decisions
or an unbound candidate. Pending-decision closure is symmetric across all three terminals
(`amendment.approved`, `amendment.human_rejected`, and a decision-time
`amendment.rejected`): each carries the `decision_id` and terminally resolves the amendment's
own decision in projection. No separate `decision.resolved` is appended on any amendment
path. Every event records the base hash and the classifier version. The canonical typed delta
is recorded only once a validated patched AST exists (step 6 on); earlier rejections record
the patch-operations hash and the error location instead.

**Persistence.** The approved snapshot is written temp → fsync → atomic rename; then the
single `amendment.approved` event is appended and fsynced — head change and logical supersede
become visible together as one replayable transition — and only then is the manifest
projection updated. A snapshot file with no corresponding `amendment.approved` event is
quarantined at recovery and never becomes head. An `amendment.approved` event with no
snapshot file is a recovery halt: the journal is the authority and this is corruption, not
something to repair.

## 10. Out of scope for v0.2

Parallel scheduling, automatic casting/routing, GUI (a later client of the same core
commands), lifecycle hooks, vector stores, nested delegation, secret storage
(meaning: no vault; opaque session state is still handled per §4 privacy), prompt
libraries, dirty-source runs, non-empty `side_effects`, run deletion and ref pruning (§1),
reopening a terminal run, invalidation-and-replay of successful attempts (§9), widening the
auto-approval envelope with glob-subset or criterion-strengthening proofs, and an OS-sandbox
wrapper for enforcement (a separate security milestone — §4 fails closed without it). See
CONCEPT's minimal-harness test; each of these fails it or belongs to score/extension space.

---

# Appendix A — Canonical encoding and identity domains

Normative. Every identity in Partitur is a hash of a canonical encoding of an exactly
specified projection. Two implementations of the encoding is a silent correctness bug, so
there is one encoder and every caller names a domain.

## A.1 Canonical encoding **[SPIKE]**

Canonical JSON is **RFC 8785 (JCS)**: UTF-8, object keys sorted by UTF-16 code unit,
no insignificant whitespace, ES6 number serialization.

- **Numeric range.** Every number in a hashed value must be an integer in the I-JSON safe
  range `[-(2^53 - 1), 2^53 - 1]`. Score-schema numbers are restricted to that range by
  compiler rule 13. Adapter `extensions` namespaces participate in
  `execution_dependency_hash` and may legitimately contain fractional numbers, so the
  encoder implements **full JCS number serialization** for them rather than rejecting
  non-integers outright — a blanket integer-only rule would make legal extension payloads
  unhashable.
- **Unicode.** No normalization is applied; byte-identical input yields byte-identical
  output. Sorting is by UTF-16 code unit per JCS, not by code point.
- **YAML → JSON mapping.** `yamlsafe` decoding produces only strings, integers within the
  range above, booleans, null, sequences, and mappings. Block scalars keep YAML clip
  semantics for the trailing newline and become plain JSON strings. Timestamp, binary,
  sexagesimal, and every other implicit tag is **rejected at parse time**, so no
  ambiguously typed value ever reaches the encoder. Duplicate keys, anchors, aliases, merge
  keys, and custom tags are rejected (§1).
- **Omitted vs explicit defaults.** A projection is built from the **validated AST after
  defaults are applied**, so an omitted field and an explicitly written default produce the
  identical hash. Optional fields with no default are omitted from the projection entirely
  rather than encoded as `null`.
- **Ordered lists vs id-keyed sets.** Id-keyed collections (`movements`, `parts`, acceptance
  criteria) are **sorted by id** before encoding, so a reorder cannot change an identity
  that does not depend on order. Genuinely ordered lists (`allowed_paths`,
  `ordered_change_sets`) keep their order, and that order is part of the identity.

**[SPIKE]** the encoder must be validated against the published JCS conformance vectors and
against a YAML corpus exercising block scalars, quoting forms, and the rejected tag set
before this section is frozen.

## A.2 Hash construction

```text
H(domain, value) = "sha256:" + hex(sha256(JCS({
  "domain":  <domain>,
  "version": <that domain's projection version>,
  "value":   <the exact normalized projection>
})))
```

The domain and version are **inside** the hashed value, so a domain confusion or a
projection change can never produce a colliding identity.

## A.3 Independent versions

A single global hash version would force every identity to churn whenever any one
projection changed. Four versions move independently:

| Version | Governs | v0.2 value |
|---|---|---|
| `canonical_encoding_version` | A.1 — the encoder itself | 1 |
| per-domain `projection_version` | what each domain feeds the encoder (A.4) | 1 |
| `amendment_classifier_version` | §9 typed classification and impact rules | 1 |
| `composition_algorithm_version` | §5 fan-in merge algorithm and Git merge implementation | 1 |

Every journal event that records an identity also records the versions it used.

## A.4 Domain registry

| Domain | Projection |
|---|---|
| `partitur/score` | The whole validated score AST after defaults. Used for `base_hash` and snapshot hashes. |
| `partitur/criterion-spec` | `{id, kind, argv?, timeout_min?, artifact_ref?, expected_hash?, rubric?}` — exactly the semantic fields of that criterion kind; nothing positional. |
| `partitur/acceptance-spec` | `{criteria: [criterion-spec hashes in declaration order], human_gate}` |
| `partitur/change-set` | `{base_tree, result_tree}` |
| `partitur/candidate` | `{base_tree, result_tree, ordered_change_sets: [change_set_id]}` |
| `partitur/candidate-composition` | `{base_tree, ordered_contributing_movement_ids, ordered_change_set_ids, composition_algorithm_version}` |
| `partitur/execution-dependency` | A.5 — exhaustively enumerated |
| `partitur/patch-operations` | The raw RFC 6902 operations array, for pre-validation rejection records only (§9). |

Artifact and tree hashes are **not** in this registry: artifact instances are hashed as raw
file bytes (`sha256:<hex>`, §1) and tree ids are Git-native. Mixing a canonical-AST hash
with a raw-byte hash is a category error; each identity states which kind it is.

## A.5 The execution-dependency projection

Exhaustive. "Includes at least" is not an identity definition — a field absent from this
list is absent from the hash, and adding one is a `projection_version` bump.

```text
{
  actual_adapter_id,                  # the adapter that SERVED the attempt (§1 per-attempt
                                      #   record), not the part's intended binding
  movement: {
    id, part,
    instruction,
    needs                             # sorted
    inputs,                           # sorted logical output ids
    outputs,                          # sorted {artifact_id, kind}
    grants,                           # effective grants, as a sorted set
    acceptance                        # the acceptance AST (id-keyed, sorted per A.1)
  },
  part: { capabilities, read_only },   # capabilities sorted
  score: {
    goal, context,
    global_invariants,                # the deterministic core projection (§4 brief)
    verification_expectation_intent,  # forwarded into briefs, hence a dependency
    policy: { allowed_paths, side_effects }   # the fields that scope authority
  },
  extensions: <canonical extensions.<actual_adapter_id> payload, or omitted>
}
```

Explicitly **excluded**, because they vary per attempt without changing what the score asked
for: `run_id`, `movement_id`'s attempt ordinal, `attempt_id`, filesystem paths (`workdir`,
`output_dir`), `session_hint`, `request.feedback`, remaining budget, and wall-clock time.

---

# Appendix B — Journal event registry

Normative. **An event type absent from this table does not exist.** `sync` marks appends
that must be fsynced before the core proceeds. `idem key` is the value that makes a repeated
append a no-op — essential for the derived and recovery-synthesized events. Derived events
(marked *derived*) are never appended independently; they are projected idempotently from
their source event, including at recovery.

## B.1 Run and movement lifecycle

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `run.started` | ✓ | run_id | — | Run → `RUNNING`; records base commit/tree, score snapshot hash, resolved-cast hash, root-score hash for CAS |
| `run.succeeded` | ✓ | run_id | Run `RUNNING` | Run → `SUCCEEDED`. On the **waived** path also carries the full candidate payload and binding (§8) |
| `run.failed` | ✓ | run_id | Run `RUNNING`/`WAITING_HUMAN` | Run → `FAILED`; carries reason (Appendix D) |
| `run.cancelled` | ✓ | run_id | Run nonterminal | Run → `CANCELLED` |
| `movement.ready` | | movement_id | Movement `PENDING`, deps succeeded | Movement → `READY` |
| `movement.started` | ✓ | movement_id | Movement `READY` | Movement → `RUNNING` |
| `movement.waiting_human` | ✓ | causation_id | Movement `RUNNING` | Movement → `WAITING_HUMAN`; Run → `WAITING_HUMAN` |
| `movement.succeeded` | ✓ | movement_id + attempt_id | Attempt `COMPLETED` | Movement → `SUCCEEDED`; approves its artifacts and change set. For the **final movement** this same event carries the run's `SUCCEEDED` transition (§8) |
| `movement.failed` | ✓ | movement_id | Movement `RUNNING`/`WAITING_HUMAN` | Movement → `FAILED`; reason ∈ Appendix D. `human_gate_rejected` is keyed on the gate decision id |
| `movement.cancelled` | ✓ | movement_id | Movement nonterminal | Movement → `CANCELLED` |

## B.2 Attempt lifecycle and performer selection

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `performer.selected` | ✓ | attempt_id | before `attempt.started` | Records the chosen performer, adapter id/version, model, enforcement posture and advisory dimensions for this attempt (§1); records whether this attempt consumes a quality retry, a fallback, or neither |
| `attempt.started` | ✓ | attempt_id | Movement `RUNNING` | Attempt → `STARTING`→`RUNNING`; records `attempt_number`, `execution_dependency_hash`, granted authority |
| `performer.completed` | ✓ | attempt_id | Attempt `RUNNING` | Attempt → `VERIFYING`. **Vendor execution ended; says nothing about success** (§6) |
| `attempt.completed` | ✓ | attempt_id | Attempt `VERIFYING`, acceptance and gate passed | Attempt → `COMPLETED` |
| `attempt.blocked` | ✓ | attempt_id | Attempt `RUNNING` | Attempt → `BLOCKED` (terminal); carries `pending_decision_ids` |
| `attempt.failed` | ✓ | attempt_id | Attempt `RUNNING`/`VERIFYING` | Attempt → `FAILED`; carries the failure kind (Appendix D) |
| `attempt.cancelled` | ✓ | attempt_id | Attempt nonterminal | Attempt → `CANCELLED` |
| `attempt.superseded` *derived* | — | source event_id + attempt_id | — | Attempt → `SUPERSEDED`; projected from `amendment.approved` (§9) |
| `execution.started` | ✓ | attempt_id + phase | — | Opens a budget interval; carries monotonic + wall-clock readings (§6) |
| `execution.stopped` | ✓ | matching `execution.started` event_id | open interval | Closes the interval and charges it. `{reason: recovered, charged: conservative}` closes an uncertain crash interval at maximum plausible duration |

## B.3 Evidence

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `artifact.recorded` | ✓ | `(logical_output_id, attempt_id)` | Attempt `RUNNING`/`VERIFYING` | Registers the immutable instance and its byte hash (§1). A second append for the same key is `duplicate_artifact_instance` |
| `change_set.recorded` | ✓ | attempt_id | Attempt `VERIFYING` | Records `change_set_id`, `base_tree`, `result_tree`, and the pinning ref (§5). Only for `repo_write` movements |
| `composition.conflicted` | ✓ | movement_id + ordered contributors | fan-in or candidate composition | Movement → `WAITING_HUMAN`; records the conflicting contributors (§5) |
| `application_candidate.recorded` | ✓ | candidate_id | every `repo_write` movement succeeded | Records the candidate and **constitutes its initial binding** (§8) |
| `acceptance.started` | ✓ | attempt_id | Attempt `VERIFYING` | Binds `subject_tree` + `acceptance_spec_hash` before any criterion runs (§7) |
| `criterion.started` | ✓ | attempt_id + criterion_id | after `acceptance.started` | Carries `criterion_spec_hash` and the same subject binding |
| `criterion.completed` | ✓ | attempt_id + criterion_id | matching `criterion.started` | `outcome` ∈ `{PASS, FAIL, ERROR}` |
| `acceptance.failed` | ✓ | attempt_id | any `FAIL`/`ERROR`, or recovery | Terminal for this acceptance; reason ∈ Appendix D. Projects retry consumption exactly once, keyed on this event's causation id |
| `acceptance.evaluation_completed` | ✓ | attempt_id | all criteria `PASS` | The **only** gateway to grade derivation (§8) |

## B.4 Decisions

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `decision.requested` | ✓ | decision_id | — | Adds to pending; `decision_type ∈ {question, human_gate, amendment, finalization}` |
| `decision.resolved` | ✓ | decision_id | pending | Removes from pending. Carries the answer, or for `human_gate` the `gate_id`, `subject_tree`, scope, and overridden finding instance ids (§8). **Never appended on any amendment path** (§9) |
| `decision.obsoleted` *derived* | — | source event_id + decision_id | — | Terminally closes a pending decision raised on a superseded revision; projected from `amendment.approved` |

## B.5 Amendments

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `amendment.rejected` | ✓ | proposal_id | Run nonterminal | Terminal. Reason ∈ Appendix D. Records base hash and classifier version; records the typed delta only from pipeline step 6 on, else the patch-operations hash and error location |
| `amendment.routed_human` | ✓ | proposal_id | admissible | **Non-terminal** routing marker; appends `decision.requested` for the amendment |
| `amendment.approved` | ✓ | proposal_id | passed 1–9 | The **single authoritative transition**: new snapshot head, new revision, superseded attempt ids, obsoleted decision ids, re-bound `candidate_id`. Resolves its own decision directly |
| `amendment.human_rejected` | ✓ | proposal_id | routed | Terminal; carries proposal id, decision id, human reason; resolves its own decision |

## B.6 Shipping

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `apply.started` | ✓ | candidate_id + txn id | `NOT_APPLIED`/`FAILED_CLEAN` | Application → `APPLYING`; records before-tree, touched paths, recovery info |
| `apply.completed` | ✓ | txn id | `APPLYING`/`RECOVERY_REQUIRED` | Application → `APPLIED` |
| `apply.failed` | ✓ | txn id | `APPLYING` | Application → `FAILED_CLEAN` — rollback **verified** |
| `apply.recovery_required` | ✓ | txn id | `APPLYING` | Application → `RECOVERY_REQUIRED`. Appended explicitly after inspection under the lock; the state is never inferred silently (§8) |
| `apply.recovery_resolved` | ✓ | txn id | `RECOVERY_REQUIRED` | `{outcome: rolled_back}` → `FAILED_CLEAN` |
| `score.promotion_started` | ✓ | txn id | `NOT_PROMOTED`/`PROMOTING` | Promotion → `PROMOTING`; a repeat is an idempotent resume, never a second promotion |
| `score.promoted` | ✓ | txn id | `PROMOTING`/`RECOVERY_REQUIRED` | Promotion → `PROMOTED`. At most one *successful* promotion per run |
| `score.promotion_recovery_required` | ✓ | txn id | `PROMOTING` | Promotion → `RECOVERY_REQUIRED` |

## B.7 Control and diagnostics

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `cancel.requested` | ✓ | run_id or attempt_id | Run nonterminal | The durable cancellation authority (§6). Observed by a live driver mid-execution, or by the next `resume` |
| `journal.tail_truncated` | ✓ | truncated seq | recovery | Records that an unparseable final line was discarded (§1) |
| `log` | | — | — | Mirrored adapter diagnostics; sanitized (§4) |
| `progress` | | — | — | Mirrored adapter progress |

---

# Appendix C — Acceptance recovery table

Normative (§7). Rows are evaluated **top-down; the first matching row wins**. Before
resuming any criterion the core verifies that the worktree's current tree still equals the
`subject_tree` recorded in `acceptance.started`; on mismatch it records
`acceptance.failed {reason: recovery_subject_mismatch}`, consuming exactly one quality
retry keyed on that event's causation id.

| Last durable state | Recovery action |
|---|---|
| `acceptance.failed` present | Terminal — synthesize no further criterion results; project retry consumption and scheduling exactly once, keyed on that event's causation id |
| Any `criterion.completed` is `FAIL` or `ERROR`, no `acceptance.failed` | Append `acceptance.failed` idempotently; start no further criterion |
| `criterion.started` without `criterion.completed` | Close it as `ERROR` — **including when the command in fact passed but crashed before the event was written** — then append `acceptance.failed` |
| All criteria completed, all `PASS`, no `acceptance.evaluation_completed` | Append `acceptance.evaluation_completed` idempotently |
| `acceptance.evaluation_completed`, required human gate not yet requested | Resume at the gate step; append one `decision.requested` idempotently |
| `decision.requested` (gate) unresolved | Restore the `WAITING_HUMAN` projection; append nothing |
| Gate resolved approve, movement success event missing | Complete the movement success idempotently — including, for the final movement, the run's `SUCCEEDED` transition |
| Gate resolved reject, terminal failure event missing | Record `movement.failed {reason: human_gate_rejected, decision_id, subject_tree}` idempotently, keyed on the gate decision id |
| `acceptance.evaluation_completed`, no gate required, movement success event missing | Complete the movement idempotently |
| `acceptance.started`, no criterion events | Resume with the first criterion |
| Some `criterion.completed` (all `PASS`), none in flight, criteria remaining | Resume with the next unstarted criterion |

---

# Appendix D — Closed enums

Normative. A value outside its enum is a protocol or internal error, never a passthrough.

**Adapter failure kinds** (§4, wire): `adapter_unavailable`, `model_unavailable`,
`provider_timeout`, `rate_limited`, `authentication`, `protocol_error`, `grant_denied`,
`task_failed`.

**Failure classification priority** — evaluated in this order, so a cancelled attempt is
never reported as a task failure:

```text
cancelled → adapter_unavailable → protocol_error → grant_denied
          → typed vendor failure → task_failed → result-envelope validation
```

**`grant_denied` sub-reasons** (core-determined; no quality retry, no fallback):
`candidate_mismatch`, `protected_path_violation`, `read_only_violation`,
`path_grant_violation`, `enforcement_unavailable`.

**Infrastructure vs quality** (§3): infrastructure = `adapter_unavailable`,
`model_unavailable`, `provider_timeout`, `rate_limited`, `authentication` → advance the
fallback chain, consume no retry. Quality = `task_failed` or any acceptance failure →
consume one retry, same performer, clean base. `protocol_error` triggers **neither** in
v0.2.

**`acceptance.failed` reasons:** `criterion_failed`, `criterion_errored`,
`acceptance_mutated_workspace`, `artifact_missing`, `artifact_kind_mismatch`,
`artifact_hash_mismatch`, `findings_malformed`, `findings_subject_mismatch`,
`findings_rubric_incomplete`, `recovery_subject_mismatch`.

**`movement.failed` reasons:** `retries_exhausted`, `fallbacks_exhausted`,
`budget_exhausted`, `human_gate_rejected`, `grant_denied`, `composition_unresolvable`.

**`run.failed` reasons:** `movement_failed`, `budget_exhausted`, `recovery_halted`.

**`amendment.rejected` reasons:** `run_terminal`, `stale`, `patch_error`, `invalid_score`,
`reserved_field`, `no_op`, `claim_narrower`, `executed_dependency_changed`,
`candidate_incompatible`.

**`amendment.routed_human` reasons:** `draft_phase`, `auto_disabled`,
`unclassified_change`, `recognized_non_monotone`, `runtime_scope_started`.

**`candidate_incompatible` conditions** (§9): `succeeded_dependency_changed`,
`composition_changed`, `verification_episode_finished`, `verification_mode_changed`.

**Envelope classes** (§9): `NARROW_PATHS`, `NARROW_GRANTS`, `BUDGET_DECREASE`.

**Criterion outcomes** (§7): `PASS`, `FAIL`, `ERROR`.

**Review outcomes** (§8): `CLEAN`, `CONTESTED`, `OVERRIDDEN`.

**Apply-gate grades** (§8): `verified`, `approved`, `reviewed`.

**Apply-gate predicates** (§8): `no_unresolved_blocking_findings`, `no_blocking_findings`.

**Verification intents** (§2): `write-basic-tests`, `pass-existing-tests`, `none`.

**Human gate modes** (§2): `always`, `on_contested`, `never`.

**Decision types** (Appendix B): `question`, `human_gate`, `amendment`, `finalization`.

**Artifact kinds** (§2): `document`, `findings`, `change_set`.

**Protocol error codes** (§4): `-32700` parse error, `-32600` invalid request, `-32601`
method not found, `-32602` invalid params, `-32000` duplicate `execute`, `-32001` frame too
large (skip the frame and continue; `id` is null), `-32002`
`duplicate_artifact_instance`.
