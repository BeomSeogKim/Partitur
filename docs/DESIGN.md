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

**Three rules were gated on a bounded implementation spike.** All three have now run against real
behaviour:

1. **Canonical encoding and numeric range** (Appendix A.1) — against JCS conformance vectors
   and a YAML corpus.
2. **Fan-in tree composition** (§5) — against renames, modes, symlinks, submodules,
   `.gitattributes`, binaries, merge drivers, and macOS/Linux consistency.
3. **Execution-driver lease, wake-up, and process supervision** (§6, §4) — lease owner
   verification, mid-`execute` interruption, and portable vendor-descendant cleanup.

Each is now stated as its spike confirmed or corrected it, and the `[SPIKE]` markers are gone.
Full method, matrices, and reproduction: [`spikes/REPORT.md`](../spikes/REPORT.md).

What the spikes **changed** rather than confirmed: A.1's YAML
rejection point and its negative-zero/underflow ingress policy; §5's merge invocation, Git floor,
custom-driver policy, and the fact that Git version alone does not determine the composed tree;
§6's fencing, which requires a per-mutation compare-and-swap rather than a single act; and §4's
descendant-cleanup claim, which was absolute and is now correctly scoped to conforming adapters.

Six questions remain undetermined and are recorded as such rather than assumed: the exact Git
compatibility floor between 2.43 and 2.47, whether any Git upgrade changes a clean result tree,
absolute macOS descendant containment, kernel-forced PID reuse under churn, Intel-macOS execution
of the start-identity path, and the full 100-million-record JCS number corpus.

**Sections not yet exhaustive.** The places below state their contracts in prose sufficient to
review the design but **not** sufficient to implement serialization against. They are completed
in a follow-up PR, which is a **hard merge gate before any score/compiler, identity, journal,
recovery, acceptance, or orchestration implementation begins** — that PR defines compiler rules
and orchestration-critical payloads, so starting any of those first would build against a
contract that is still moving:

- Appendix A.4/A.5 — the `partitur/criterion-spec`, `partitur/acceptance-spec`, and
  `partitur/execution-dependency` projections, as exact tagged unions.
- Appendix A.4 — the missing `partitur/resolved-cast` and `partitur/score-subtree` domains, and
  qualifying Git-native identities by object format (`git-sha1:` / `git-sha256:`) so a
  repository-format migration cannot silently alias two different trees.
- Appendix A — criterion generation and ordering (§7) as a hashed projection, and the effective
  `may_propose` value plus the score-base dependency it implies.
- **Runtime emitted-id scoping** — question and proposal ids are only guaranteed unique
  *within an attempt* by the adapter kit, so decision ids must be scoped
  `(attempt_id, emitted_id)` or mapped to core-generated ids. Nothing may key a decision on the
  raw emitted id alone.
- Appendix B — per-event payload schemas, and structured handling of `protocol_error`
  sub-reasons.
- The **movement fan-in** `composition_dependency_hash` (§5), which the candidate-level hash
  does not cover.
- The deliberate difference between the candidate's two contributor lists, which a no-op change
  set makes observable: `candidate.ordered_change_sets` is the **content-deduplicated applied
  sequence** (a no-op appears once), while the candidate-composition dependency hashes the
  **full pre-dedup ordered `(movement_id, change_set_id)` sequence**, so deleting or reordering
  an identical or no-op writer stays detectable (§8, §9).
- The **effective acceptance-plan projection** that `acceptance_spec_hash` now names (§7).
- Two compiler rules the identity work depends on: at most one declared `artifact` criterion per
  ordinary output within a movement, and rejection of an `artifact` criterion referencing a
  `change_set` output.

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

**Authoritative state and writable staging are separate roots.** Anything an attempt can
write must sit outside authoritative run storage, so a misbehaving performer cannot
corrupt evidence by writing where evidence lives:

```text
<repo>/
  partitur.yaml                # the score (committed)
  .partitur/
    .gitignore                 # created by `partitur init`; contains `runs/` and `work/`
                               #   (both are run-local; neither is ever committed)
    cast.yaml                  # project cast override (committed or ignored, user's choice)

    runs/<run-id>/             # AUTHORITATIVE — core-written only, never agent-writable
      journal.jsonl            # append-only event log (single writer: core)
      manifest.yaml            # rebuildable projection: score revision + hash, resolved
                               # cast pins, per-attempt enforcement record, artifact index
      scores/revision-<n>.yaml # immutable score snapshots (see below)
      resolved-cast.yaml       # the fully resolved cast used by this run
      artifacts/<logical-output-id>/<attempt-id>
                               # immutable artifact instances (identity and atomicity
                               # below); a retry never overwrites earlier evidence
      session/                 # session hints, mode 0600 (see §4 privacy)
      driver.lease             # execution-driver lease (§6); absent when no driver runs
      authority.json           # execution-authority checkpoint: current epoch + token (§6).
                               #   A PROJECTION of authority.granted / run.* events, like the
                               #   manifest — rebuildable, never the authority itself. The
                               #   token is the one value not journaled (§6)
      attempts/<attempt-id>/
        stderr                 # sanitized vendor/adapter diagnostics (§4 privacy)
        trace.jsonl            # protocol trace

    work/<run-id>/<attempt-id>/   # NON-AUTHORITATIVE staging — agent-writable
      output/                  # the attempt's output_dir (§5); artifacts are copied out
                               # of here into runs/.../artifacts/ and only the copy counts

~/.config/partitur/cast.yaml   # user-global cast override
<install>/default-cast.yaml    # first-party factory cast (versioned data file with
                               # metadata: date, tested adapters/models, rationale)
```

Git refs the core owns — never user-visible branches, and never garbage-collected while
the run exists:

```text
refs/partitur/runs/<run-id>/attempts/<attempt-id>/changeset   # storage handle (§5)
```

**Identifier grammar.** Run ids, attempt ids, and score-declared ids are interpolated into
filesystem paths, Git ref names, and semantic selectors (§9), so an unconstrained string
would permit traversal, ref-injection, and selector collisions. Ids are constrained at the
source rather than escaped at each use site:

- **Score-declared ids** (movement, part, output, criterion, rubric, decision-facing ids)
  match `[A-Za-z0-9][A-Za-z0-9_-]{0,127}` — a bounded ASCII slug. Anything else is a
  compiler error (§2 rule 16). This grammar is simultaneously path-safe, ref-safe, and
  selector-safe, so no escaping layer is needed anywhere.
- **`run_id` and `attempt_id`** are **core-generated** UUIDv7 — time-ordered, collision-free
  without coordination, and trivially path- and ref-safe. They remain opaque strings on the
  wire (§4): the protocol never constrains their form, and the core never parses meaning out
  of them.
- The reserved `partitur.` prefix (containing a `.`, hence outside the slug grammar) is
  therefore unusable as a score-declared id by construction, which is what makes the
  core-supplied inputs of §4 unspoofable.

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
`artifacts/<logical-output-id>/<attempt-id>` — both components are safe path segments by
the identifier grammar above, so no encoding layer intervenes. A logical output may be
emitted **at most once per attempt**; a second notification for the same logical id is
rejected before any append, and the attempt fails with `protocol_error`
(`duplicate_artifact_instance`). A declared output never emitted is caught by the
artifact-integrity criterion (§7). Journal entries, hashes, marks, and finding overrides
reference instances. A downstream movement's logical input resolves to the instance from
the **completing successful, non-superseded attempt**, so retries and revision re-runs
never collide with earlier immutable evidence.

`kind: change_set` is the one exception: it is never an artifact instance. See §5.

**Artifact publication and immutability.** The file at an announced path must be complete and
must not change afterwards. Concretely, in the merged design the vendor process closes its
files before exiting, the adapter validates and emits `artifact` events only after that exit,
and the core then performs a stable-stat copy. The conformance obligation is therefore stated
as an outcome — *an announced artifact path is complete and immutable from the moment it is
announced* — rather than as an atomic-publish step the adapter does not currently perform. The
core's stable-stat/hash copy stays an independent defensive check regardless.

**Artifact recording atomicity.** Recording an artifact follows a fixed order: copy to a
temporary file → compute hash (`sha256:<hex>`) and durably flush → atomic rename into
`artifacts/<logical-output-id>/<attempt-id>` → append `artifact.recorded` (fsynced) →
update the manifest projection. The core copies with a stable-stat check and rejects an
artifact notification that is undeclared, duplicated, whose path escapes `output_dir`, is
not a regular file, or whose file changes during copying. On recovery, an orphan artifact
file without an event is quarantined; an event whose file is missing is a recovery error
that halts the run.

**Ref/journal ordering.** A change-set ref (§5) is created before its authorizing event
is appended. On recovery, a ref with no authorizing event is quarantined and cleaned; an
event whose ref is absent is a recovery error that halts the run — symmetric with
artifacts and snapshots.

**Two kinds of score hash — never interchanged.** A score has a *meaning* and a *file*, and
conflating them would let the core silently destroy a user's comments or formatting:

| Hash | Over | Used for |
|---|---|---|
| `score_hash` (semantic) | the canonical score **AST** (`partitur/score`, Appendix A) | `base_revision`/`base_hash` staleness, snapshot identity, amendment no-op detection, `execution_dependency_hash` |
| `score_file_hash` (raw) | the **exact bytes** of the file (`sha256:<hex>`) | the `promote-score` compare-and-swap and the byte-exact write |

The semantic hash deliberately ignores comments and formatting, which is correct for
"did the meaning change?" and **wrong** for "may I overwrite this file?". Promotion
therefore compares `expected_root_file_hash` and writes the pinned snapshot's exact bytes
(§8), so a user's formatting-only edit surfaces as a conflict rather than being clobbered.

**Score snapshots and the root score.** `partitur.yaml` stays editable by the user, so a
revision number alone cannot reproduce a run:

- At run start the core snapshots the full score into `scores/revision-<n>.yaml` and records
  **both** hashes: the snapshot's semantic `score_hash` and the root file's
  `score_file_hash` at that moment.
- Amendments modify **only the run's snapshot chain** — the root `partitur.yaml` is never
  overwritten automatically. The explicit `promote-score` command copies a run revision
  back to the root score; promotion is a compare-and-swap against the recorded root
  **file** hash, and surfaces a conflict if the root file has changed since.
- Resume works from the run's snapshot, never from the current root `partitur.yaml`.
  If the root score claims the same revision number but its semantic hash differs from the
  snapshot, the core refuses to auto-resume and asks the user.
- The manifest records the source revision and both hashes.

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
      - id: change-set         # kind: change_set is CORE-SYNTHESIZED — the performer never
        kind: change_set       #   emits it as an artifact; the core captures it from the
                               #   worktree. See §5.
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
any control artifact. Without this, a score could authorize movements that rewrite the
authority that governs them.

Two different mechanisms, because a tree comparison cannot see everything:

- **Tracked paths inside the worktree** are rejected from candidate change sets, checked post
  hoc even when an `allowed_paths` glob would admit them. A violation fails the attempt in the
  `grant_denied` class (`protected_path_violation`) with no quality retry and no fallback.
- **Shared Git refs and authoritative run state** live outside the worktree, so no candidate
  check can detect a `git update-ref` or a direct journal write. These are guarded by
  *isolation* — the worktree and its staging root are separate from run storage (§1) — plus
  integrity and CAS checks at every core read and write. Under an unconfined advisory adapter
  (§4), physical prevention is **not** claimed; detection and fail-closed recovery are.

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

**Finalization is an amendment, not a bare flag flip.** `status` lives in the score, so
changing it must produce a snapshot and a revision like any other score change; otherwise
the run would carry a `finalized` status that no snapshot records and no revision explains.
Finalization therefore reuses the amendment transaction (§9) rather than inventing a parallel
one:

1. The core — not a performer — constructs a **reserved finalization amendment** whose patch
   changes only `/status` from `draft` to `finalized`.
2. It routes to the human with `decision_type: finalization` (reason `draft_phase`, since the
   base is still a draft, so it is never envelope-eligible).
3. Human approval is recorded as `amendment.approved {mode: human}`, which atomically writes
   the new snapshot, increments the revision, and **resolves the finalization decision
   itself** — no separate `decision.resolved` is appended, exactly as for every other
   amendment path.
4. **The same event closes the draft phase**, projecting the interview movement to
   `SUCCEEDED`. Without this the movement would return to `RUNNING` when its last blocking
   decision resolved while all its attempts stayed terminal `BLOCKED`, and the run could
   finish with a nonterminal draft movement. This projection manufactures **no** evidence: no
   `attempt.completed`, no VERIFIED, no APPROVED. The interview movement succeeds because the
   draft phase is over, not because anything was verified.
5. Rejection is `amendment.human_rejected`; the score stays a draft and the interview may
   continue.

The interview movement can never have a *successful completed* attempt before finalization.
This is **enforced**, not merely expected, because if it had one the finalization patch to
`/status` would change that attempt's `execution_dependency_hash` and finalization would reject
itself under §9's executed-dependency rule. While `status: draft`:

- A draft attempt's result **must block**: at least one `question`, or a `proposal` with
  `requires_decision: true`.
- A `proposal` with `requires_decision: false` from a draft movement is **rejected**
  (`protocol_error`). Non-blocking proposals exist to let an ordinary movement suggest
  something without stopping; in draft the interview's entire purpose is to reach a human, and
  every draft amendment routes to one anyway (§9).
- A result that is otherwise `completed` with no blocking semantic output is the quality failure
  `draft_no_blocking_output` — a valid zero-output envelope is `completed` in general (§4), and
  without this rule that would walk the interview movement straight into ordinary acceptance.
  The event path is explicit, and **acceptance never starts**:

  ```text
  performer.completed
  → attempt.failed { kind: task_failed, reason: draft_no_blocking_output,
                     recorded quality disposition }
  ```

  If a retry is admissible it proceeds as an ordinary `quality_retry`; otherwise the movement
  and run fail without charging past the cap (§3).
- **Ordinary acceptance can never make the interview movement succeed.** Only the finalization
  amendment projects it to `SUCCEEDED`.

A patch touching anything besides `/status` is not a finalization amendment and is rejected as
an ordinary proposal would be.

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
2. A movement that requests a `repo_write` grant must **declare** ≥1 `hard` criterion or
   `human_gate: always`. Core-generated integrity checks (§7) never satisfy this — the
   score itself has to say what counts as done. An explicitly declared `artifact`
   criterion does count. (Keyed on the movement's grant, not the part's capability —
   a write-capable part may still play read-only movements.)
   This is a per-movement
   **floor**; the ship judgment is made on the application candidate, never summed over
   movements (§8).
3. `grants` ⊆ the part's `capabilities`. A `read_only` part can never receive
   `repo_write`. Read-only-ness is never inferred from instruction text or output names.
   (`policy` scopes *where* an authority applies — `allowed_paths`, `side_effects` — and
   never grants or withholds a grant kind; v0.1's "∩ what `policy` allows" referenced a
   ceiling the schema does not define.)
4. **Movement ids are unique**, as are part ids and score-declared question/decision ids —
   uniqueness is per collection and checked explicitly, since ids are the matching key for
   typed comparison and hashing (§9, Appendix A). `needs` must form a DAG; part references
   must exist; every `inputs` entry must be an
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
9. Every **score-declared** acceptance criterion carries an `id` unique within its movement.
   Core-generated integrity checks (§7) live in the reserved `partitur.` namespace and are
   outside this uniqueness scope by construction.
10. `allowed_paths` contains no duplicate pattern.
11. **Apply-gate achievability** — a finalized score whose gate can never be satisfied is
    rejected (§8): `require ∋ verified` ⇒ the final movement **declares** ≥1 hard criterion;
    `require ∋ reviewed` or any predicate present ⇒ it declares ≥1 review criterion with a
    typed `findings` output; `require ∋ approved` ⇒ it declares `human_gate: always`.
12. **Final-movement closure** — `apply_gate.waived` ⇒ `verification.final_movement` must
    be omitted; otherwise it must be declared, must not hold `repo_write`, must
    transitively depend on every non-draft movement via `needs`, must have no downstream
    movement, and no non-draft movement may sit outside its dependency closure.
13. No numeric value anywhere in the score falls outside the canonical safe range
    (Appendix A).
14. A `phase: draft` movement declares **no ordinary artifact outputs** — draft movements may
    not emit `artifact` at all (§2 draft contract), so declaring one would be unsatisfiable.
    It may declare no `change_set` output either, since it holds no `repo_write`.
15. A movement's `repo_write` grant and its `change_set` output must agree: a movement holding
    `repo_write` declares **exactly one** `kind: change_set` output, and a movement without
    `repo_write` declares none. This is what makes "which movements contribute to the
    candidate" a compile-time fact rather than a runtime discovery (§8).
16. Every score-declared id matches the identifier grammar of §1
    (`[A-Za-z0-9][A-Za-z0-9_-]{0,127}`), which excludes the reserved `partitur.` prefix by
    construction.
17. `may_propose` (below) defaults to `false` and is permitted on **any** movement. The
    draft interview movement has it implicitly. The effective value — including that
    implicit default — is materialized in the canonical AST, so it is visible to hashing.

**Amendment-proposal authority.** A performer can only propose an amendment if it has been
given the base score to patch, and receiving the base score has a cost: the score becomes
part of that attempt's execution dependencies, so **any** later amendment invalidates the
attempt (§9). If every movement received it, no amendment could ever be approved mid-run
after any movement succeeded — the envelope would be dead on arrival.

Proposal authority is therefore opt-in per movement:

```yaml
  - id: design
    part: plan
    may_propose: true           # default false. Receives the reserved
                                #   partitur.score-base input (§4); its
                                #   execution dependency includes the score base, so a
                                #   later amendment invalidates this attempt.
```

It is permitted on **any** movement, write-capable included. A proposal carries no mutation
authority of its own — approval is a separate human or envelope decision that supersedes the
active attempt anyway — so barring write movements would only silence the performers most
likely to discover a score defect while implementing against it. The cost is the invalidation
above, and the score author decides whether to pay it.

The draft interview movement carries it implicitly, since proposing is its entire purpose,
and draft-phase amendments always route to a human anyway.

The field governs **adapter-originated** proposals only: a movement without it receives no
`partitur.score-base`, and a `proposal` event from such an attempt is a `protocol_error`
(`proposal_without_authority`). A proposal a human submits through `partitur amend` needs no
movement authority — the human already has it.

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

> **This example is illustrative, not runnable.** Under the fail-closed predicate of §4 it
> does not pass `partitur validate`: `fable` (claude) reports all-false enforcement, and
> `sol` (codex) cannot enforce glob-granularity path grants. A runnable cast must either set
> `allow_advisory_enforcement: true` on the affected performers — accepting that the
> manifest records which constraints were advisory per attempt — or bind write movements to
> a performer whose enforcement covers the movement's grants. What the factory cast ships is
> deliberately left open until the §4 enforcement dimensions are implemented.

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
- **Quality failure** (`task_failed` or acceptance failure): consume one retry **if another
  quality attempt is admissible** and try again with the **same** performer from a clean base;
  otherwise terminalize the movement without charging past the cap. Quality failures never trigger
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
                 + decision resumes                        (unbounded by retry policy)
  ```

  Revision-triggered restarts (§9) and **decision resumes** (§4) consume neither a quality
  retry nor a fallback and are limited **only by the remaining active wall-clock budget**. A
  decision resume additionally preserves the blocked attempt's performer and its position in
  the fallback chain — answering a question is not evidence that the performer was wrong. `remaining_retries == 0` forbids only new
  *quality-retry* attempts; `remaining_time == 0` starts **no** new attempt of any kind.
- `retries_per_movement` is a per-movement total shared across the fallback chain — a
  fallback performer does not receive a fresh retry budget.
- **A failure charges only if it authorizes another attempt.** A quality failure consumes a
  retry only when a further quality attempt is actually available; an infrastructure failure
  advances the fallback position only when a further fallback exists. Otherwise the failure
  terminalizes the movement **without** consuming past the cap, and the failure event records
  that disposition atomically. Charging the terminal failure too would let
  `retries_consumed` exceed `retries_per_movement`, making the projection contradict its own
  bound.
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
JSON-RPC 2.0 messages as **UTF-8 JSON Lines**, one per line, directional: **requests travel on
the adapter's stdin; responses and event notifications travel on its stdout.** newlines inside values are escaped; control frames have a fixed
size cap — large content travels as artifact files, never inline. `stderr` is
diagnostics only, captured to the attempt directory. The adapter must keep reading stdin
during `execute` so a `cancel` request can be received; after a grace timeout the core
terminates the process, and force-kills after a further timeout. No daemon in v0.2.

**Frozen wire rules.** These are normative for both sides, and "frozen" means "the contract
will not change" — **not** "every line already exists". Implementation status:

- Most rules below are implemented and conformance-tested in the adapter kit.
- The two marked ⚠ are specified target behaviour on the §4 code follow-up.
- The **core-side client is not implemented at all yet.**
- Adapter-side follow-ups are closed: `shell_grants` / `read_grants` reporting and
  pre-persistence stderr sanitization are implemented. Process supervision is settled below —
  session ownership plus conformance, not containment.

`stderr` is diagnostics only and never carries protocol.

- **Frame cap 1 MiB** (1048576 bytes) per line, both directions, and the behaviour is
  **directional**:
  - core → adapter oversized: the adapter answers `-32001` with `id: null`, then **skips the
    frame and continues**. Not fatal.
  - adapter → core oversized: the core **terminalizes the attempt** as `protocol_error`. The
    core cannot skip content it was supposed to act on, and large content is supposed to
    travel as an artifact file.
- **Strict decoding.** Unknown fields are rejected, trailing content after the JSON value is
  rejected, and every frame must be valid UTF-8. Malformed JSON is `-32700`.
- ⚠ **Duplicate JSON keys are rejected.** *Not yet implemented:*
  `protocol.DecodeStrict` currently rejects unknown fields and trailing values but accepts
  duplicate keys, because Go's `encoding/json` silently keeps the last one. Closing this is on
  the §4 code follow-up — duplicate-key tolerance is a parser-differential hazard, and the
  result-envelope parser already rejects them, so the two paths must agree.
- **Event caps.** At most 10,000 events per attempt; `log`/`progress` messages are truncated
  to 4 KiB on a valid UTF-8 boundary.
- **EOF behaviour**, stated once, per direction:
  - *Clean* EOF on the adapter's stdin — the core is done. The adapter cancels in-flight work,
    drains, exits **zero**.
  - *Partial frame* at EOF on the adapter's stdin — the core died mid-write. The adapter
    cancels, drains, exits **nonzero**.
  - *Partial frame* at EOF on the adapter's stdout — the adapter died mid-write. **Fatal to the
    attempt:** `protocol_error` (`partial_frame_eof`). This is deliberately stricter than the
    oversized-frame rule, because a truncated frame carries unknown content, whereas an
    oversized one is at least fully delimited.
- **`cancel` params are exactly `{attempt_id}`**, and the success result is an empty object —
  including for an unknown or already-finished attempt, which is what makes it idempotent.
- **Blank lines are skipped**, not errors.
- **Request `id` may be a string or a number** and is echoed back **verbatim** — never
  reformatted, never renumbered.
- **One concurrent `execute` per adapter process.** A second `execute` while one is in
  flight is `-32000`.
- **`cancel` is idempotent** and always acknowledged, including after the target has already
  finished — the core may legitimately race a cancel against completion.
- **Events precede the response.** Every `event` notification for an `execute` is emitted
  before that call's response; no event is emitted afterwards. This is what lets the core
  treat the response as a completeness marker.
- **Events use the single JSON-RPC method `"event"`** with flat params and a required
  `type` discriminant. There is no per-event-type method name.
- Error codes: `-32700` parse, `-32600` invalid request, `-32601` method not found,
  `-32602` invalid params, `-32603` internal error, `-32000` execute already in progress,
  `-32001` frame too large.

**Session hints and privacy.** Session continuity across attempts is carried by an
opaque `session_hint` the adapter may return and the core may pass back — always an
optimization, never required state (conformance test: resume with the hint removed).

Hints are stored and retrieved under a **compatibility key** of
`(performer, adapter id, model)`. A hint is never passed across an adapter fallback or to a
different model: a resume token from one vendor's session is meaningless — at best — to
another. If no compatible hint exists, or the adapter rejects a stale one, the attempt
proceeds with a single fresh invocation. Because a hint is never required state, none of
this can affect correctness, only latency and cost.
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
from the worktree as a `change_set` (§5), and `change_set` outputs never appear in
`brief.outputs`. Draft movements may not emit `artifact` at all.

**Blocking handshake for questions and proposals.** v0.2 has no daemon, so nothing waits
in memory for a human:

- An adapter that needs answers emits `question`(s) (or a `proposal` it cannot proceed
  without, marked `requires_decision: true`), returns `outcome: waiting_human` with
  `pending_decision_ids`, and exits. A blocking proposal's `id` is a valid member of
  `pending_decision_ids`, alongside question ids. The attempt ends `BLOCKED` (a terminal
  attempt state — no process stays alive).
- The core records `decision.requested` per pending decision. An unresolved blocking
  decision projects the movement and the run to `WAITING_HUMAN` (§6) — it is a projection
  of the outstanding decision, not a separately appended state event.
- **Resolution makes execution eligible; it never launches it.** The two decision kinds
  resolve differently and neither starts an adapter:
  - A *question* resolves through `decision.resolved`.
  - An *amendment* (including a blocking `proposal`) resolves through its own amendment
    terminal event — `amendment.approved`, `amendment.human_rejected`, or a decision-time
    `amendment.rejected`. **No `decision.resolved` is ever appended on an amendment path**
    (§9).

  When the last blocking decision is resolved or obsoleted, the movement and run project
  back to `RUNNING`, and the resume reason depends on whether the revision moved:

  | Resolution | Reason | Effect on the blocked attempt |
  |---|---|---|
  | No revision change (questions answered) | `decision_resume` | It stays terminal `BLOCKED` history — a terminal attempt is never superseded |
  | Approved amendment, revision changed | `revision_restart` | Also supersedes every **nonterminal** attempt (§9); the `BLOCKED` one is already terminal and stays history |
  | Finalization approved | — | Completes the draft movement (§2); **no new attempt is launched** |

  In the first two cases a live driver continues into a new attempt — `performer.selected`
  first, then `attempt.started`, as for every attempt — passing the resolutions in
  `resolved_decisions` (plus a compatible `session_hint`); if no driver holds the lease, a
  later `resume` does it (§7 command authority).

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

`inputs[].hash` always means **raw file integrity** — the SHA-256 of the exact bytes at
`path` — for reserved inputs exactly as for ordinary artifacts. A *semantic* hash is never
placed there; when a semantic identity must reach the performer it is a field **inside**
the file:

```text
artifact_id: partitur.score-base           # supplied to any movement whose `may_propose`
kind:        partitur/score-base+json;v=1  #   is true (§2)
hash:        <sha256 of the file bytes>
file content:
  { schema: "partitur/score-base+json;v=1",
    base_revision,                  # a proposal's base_revision MUST equal this
    base_hash,                      # ... and its base_hash MUST equal this — semantic
                                    #     (partitur/score, Appendix A), which is what
                                    #     makes the proposal stale-checkable (§9)
    score }                         # the canonical JSON score at that revision

artifact_id: partitur.subject-tree          # supplied to any movement declaring a review
kind:        partitur/subject-tree+json;v=1 #   criterion
hash:        <sha256 of the file bytes>
file content:
  { schema: "partitur/subject-tree+json;v=1",
    subject_tree,                   # the CORE-OBSERVED tree — a review performer cannot
                                    #   compute this itself, especially with no shell
    findings_schema: "partitur/findings+json;v=1",
    rubrics: [{ id, required_coverage: true }] }
```

The reserved `partitur.` prefix is unusable as a score-declared output id by construction
(§1 identifier grammar), so these inputs cannot be spoofed. The core-observed subject tree
is always authoritative; a findings artifact's own claim is compared against it and never
trusted in its place (§8).

**Result envelope v1 (adapter-internal, normative).** The kit-level contract between an
adapter and its vendor process is not part of the JSON-RPC wire, but it is a conformance
surface every adapter shares, so it is specified rather than left implicit:

- The adapter instructs its vendor to write exactly one strict JSON file named
  `partitur-result.json` in `output_dir` before exiting. The name is reserved: it can never
  itself be a declared artifact.
- Shape: `{ version: 1, artifacts, questions, proposal, summary }`. All four payload members
  must be **present**; absent is malformed. Only `proposal` may be `null` — `artifacts` and
  `questions` must be present arrays (possibly empty) and `summary` a present string. Strict
  decoding: unknown fields and duplicate keys are rejected. Maximum 1 MiB. ⚠ *Valid-UTF-8
  rejection is target behaviour, not yet implemented* — on the §4 follow-up with duplicate-key
  rejection.
- `artifacts[].path` is a **non-empty relative** path containing no `..` segment, which must
  still resolve inside `output_dir` **after symlink resolution**, and must name a **regular
  file**. Ids across `artifacts`, `questions`, and `proposal` are all non-empty and mutually
  unique.
- Question text is non-blank; `proposal` is `null` or an object
  `{id, amendment, requires_decision: bool}`. The envelope file itself must be a regular file,
  not a symlink.
- Whether an artifact id was *declared* is checked by the **core** at the event boundary, not
  by the kit while parsing — the kit does not know the score.
- A **valid envelope with empty outputs is `completed`** — producing nothing is a legitimate
  result. A **missing or invalid envelope is `task_failed`**, never `protocol_error`: the
  adapter spoke correctly, the vendor did not deliver.

**Diagnostics privacy.** Vendor and adapter `stderr` may contain a session id the adapter
has not yet recognized, so raw passthrough would violate the guarantee that diagnostics
never echo hints. Adapters MUST buffer `stderr` to a bounded size and sanitize it against
every known sensitive value **before** it is written anywhere the core persists.

Two channels with different rules, which v0.2 keeps strictly apart:

| Channel | Destination | In the journal? |
|---|---|---|
| Raw `stderr` | `attempts/<id>/stderr`, sanitized | **Never** |
| Protocol `log` / `progress` events | mirrored | Yes, as **non-authoritative observations** (Appendix B `authority` column) — sanitized and bounded |

So "diagnostics never enter the journal" means the raw stream. Typed, sanitized, bounded
protocol events are journaled deliberately, because a later GUI needs them; they carry no
authority and no projection ever reads them.

**Process supervision.** The adapter and the vendor process it spawns are separate process
groups, so hard-killing a wedged adapter can orphan the vendor group. Termination is layered:
the adapter MUST handle `SIGTERM` by terminating its vendor process group before exiting, and the
core's outer grace period MUST exceed the adapter's own `SIGTERM`→`SIGKILL` grace.

Ownership is established by **session**, which the core can do at spawn time even though it
cannot enumerate descendants that do not exist yet:

1. The core starts each adapter as a **new POSIX session leader** (`setsid`).
2. A conforming adapter may put the vendor in its own **process group**, but MUST NOT create —
   or permit a vendor descendant to create — another **session**.
3. On outer termination the core enumerates processes, selects the adapter's session by SID,
   signals `SIGTERM` to every group and verified PID, waits the outer grace, then repeatedly
   `SIGKILL`s and re-enumerates until no live session member remains.
4. PID and start identity (§6) are re-checked before each individual signal, to narrow the
   PID-reuse race.

**This is conformance cleanup, not containment — stated honestly because the difference matters.**
A descendant that calls `setsid()` receives a new SID and drops out of the outer session's
selectable set on both platforms; process enumeration cannot close that race once a child has
daemonized and lost its ancestry relationship. So:

- Against a **conforming** adapter chain — including a wedged adapter whose vendor sits in a
  separate process group — the sweep leaves zero survivors on macOS and Linux.
- Against a descendant that deliberately escapes its session, it does not. That is not a gap to
  patch with another process-group tweak: it is the trust boundary this section already states.
  Adapters are trusted executables running with the user's privileges, so **an adapter that
  deliberately escapes supervision is malicious adapter behaviour, which is explicitly outside
  the core's security boundary.** Rule 2 above is therefore an adapter *conformance* requirement.
- Where an absolute ownership set is available it should be used: Linux **cgroup v2**, when
  Partitur has permission to create one. macOS has no unprivileged equivalent, so an absolute
  guarantee there would need a separately designed privileged containment backend — out of scope
  for v0.2 (§10).
- **Unverifiable state fails closed for that execution.** If process enumeration or start-identity
  verification is unavailable — sandbox policy, cross-user restriction, a read that errors — the
  core reports the sweep as *incomplete* and fails the attempt rather than claiming zero survivors.
  Conformance cleanup being the design's ceiling is one thing; asserting a clean result from state
  the core could not read would be another, and it is the assertion that would be dangerous.

## 5. Workspace model v0.2

**Run preconditions.** A run requires a Git repository. At run start the core records
the source base commit/tree id in the manifest, together with the score and resolved
cast hashes. If the source tree has tracked or untracked changes (beyond ignored
`.partitur/` run data), the run is refused — dirty-source support is future scope. This
guarantees the agents' base is exactly what the user sees.

Write attempts never modify the user's checkout directly. v0.2 uses **Git worktrees**:

- Each attempt gets a fresh worktree built from the **approved results of its dependency
  movements** (the clean base), plus an always-writable `output_dir` at
  `.partitur/work/<run-id>/<attempt-id>/output/` — outside both the worktree and
  authoritative run storage (§1) — for declared
  artifacts. `read_only` means the repository worktree is read-only; ordinary artifacts
  (documents, findings) are written to `output_dir` only.
- **`kind: change_set` is a core-synthesized logical output, never an artifact.** A
  performer cannot emit one and must never be asked to. Concretely:

  - It is **excluded** from `brief.outputs`, so the performer is never told to produce it.
  - It is **excluded** from the adapter result envelope and from `artifact.recorded`; an
    `artifact` event naming a `change_set` output is a `protocol_error`.
  - It is **excluded** from the artifact-integrity criterion (§7). The declaration is
    satisfied only by `change_set.recorded`, which the core appends after capturing the
    worktree.
  - A downstream movement consuming it receives the **composed worktree** built from it
    (§5 fan-in) — not a file path in `inputs`. Its `inputs` entry is a dependency
    declaration, not an artifact delivery.

  Declaring it in `outputs` remains how a score states "this movement produces repository
  changes", which is what makes it available to `needs`/`inputs` and to candidate
  composition. Only the delivery mechanism differs from ordinary artifacts.
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
- **Fan-in.** When a movement depends on several movements, contributing change
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

  A merge conflict yields exactly one outcome, and it is **terminal, not a wait**: the core
  records `composition.conflicted` with the conflicting contributors and the conflicted paths
  as **evidence only**, then fails the affected movement — or, for candidate composition, the
  run — through the ordinary `movement.failed` / `run.failed` path with reason
  `composition_unresolvable`, so the conflict never bypasses the normal lifecycle and recovery
  cascade. It never merges silently and never picks a side.
  Interactive resolution of tree conflicts is future scope — a `WAITING_HUMAN` here would
  be a wait with no decision to answer and no resolution event, so v0.2 fails deterministically
  instead. Changes from failed, cancelled, or superseded attempts are never candidates.

  **The exact invocation is normative**, because the composed tree depends on it. Each step runs,
  in a **non-bare** repository with system and global Git config isolated:

  ```sh
  GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_GLOBAL=/dev/null \
  GIT_ATTR_SOURCE=<T> \
  git --git-dir=<composition-git-dir> --work-tree=<empty-work-tree> \
      merge-tree --write-tree --merge-base=<C.base_tree> \
      --name-only -z --no-messages <T> <C.result_tree>
  ```

  - Exit `0`: the first NUL-delimited field is the result tree.
  - Exit `1`: the first field is Git's conflict tree, the rest are the exact conflicted paths —
    NUL delimiting is required because a path may contain a newline.
  - Any other exit is an **infrastructure failure**, not a composition conflict, and must not be
    reported as one.

  `GIT_ATTR_SOURCE=<T>` is required so attributes are read from the composed *ours* tree rather
  than an unrelated checkout. The repository must be **non-bare** — a bare one fails this
  invocation outright (`BUG: attr.c: non-INDEX attr direction in a bare repo`) — and it is always
  the **core-created temporary** repository described below, never the source repository, whose
  configuration must never be consulted. `git read-tree -m` is **not** sufficient: it provides
  neither the modern content merge and rename handling nor conflict evidence.

  The supported **Git floor is 2.47** until a narrower floor is separately proved.

  **External custom merge drivers are rejected, and the rejection is enforced at runtime.** A
  repository-configured driver changes the result tree for identical base/ours/theirs trees, and it
  makes composition execute an arbitrary command named by repository configuration — from outside
  the input trees entirely. v0.2 refuses rather than trying to pin driver identity.

  Isolating system and global config is **not sufficient**: repository-local `.git/config` and its
  includes, worktree config, and `$GIT_DIR/info/attributes` are all still consulted, and the last
  takes precedence over in-tree `.gitattributes`. Git also lets `merge.default` select a driver
  where an attribute is unspecified. So a validation-time check followed by a merge in the live
  source repository would be a TOCTOU hole — a driver added mid-run would apply.

  The enforcement is therefore structural rather than a check:

  1. **Composition never reads the live repository's configuration.** Each step runs in a
     core-created non-bare temporary repository with core-owned local config, populated only with
     the objects it needs. `.git/config`, its includes, worktree config, and
     `$GIT_DIR/info/attributes` are the core's, not the project's.
  2. `merge.default` is forced to an allowed built-in or left unset.
  3. Over the **union of paths** in `C.base_tree`, `T`, and `C.result_tree`, the core runs
     `git check-attr --source=<T> --stdin -z merge` and permits only *unspecified*, *set*, *unset*,
     and the built-ins `text`, `binary`, `union`. Any other value is rejected before the merge
     runs.
  4. The normative merge then runs against exactly that immutable configuration.

  Consequently a driver added to the project **after** run start cannot affect the current
  composition at all, because the live config is never consulted. `validate` and run-start report
  any `merge.<name>.driver`, unsafe `merge.default`, or non-built-in `merge=<name>` they find, but
  that report is **early feedback, not the enforcement** — the enforcement is that the value could
  never have been read.

  Because `candidate_id` is a *content* identity (§8), the composition dependency records **more
  than the Git version**: the exact Git build, object format, merge invocation and strategy
  options, `merge.renormalize`, and every applicable repository merge-config input. Version alone
  is demonstrably insufficient — `merge.renormalize` alone changes the result tree for identical
  inputs.
- Partial changes from failed, cancelled, or superseded attempts are discarded — they
  never leak into the next attempt. A fallback performer starts from the same clean
  base, never from the failed performer's dirty workspace.
- **Read-only post-hoc verification.** For every movement whose effective grants exclude
  `repo_write`, the core verifies at adapter exit that the worktree is unchanged. A Git
  tree comparison alone is insufficient: the check covers tracked content, **non-ignored
  untracked files, symlink targets, and file modes**, plus the protected paths of §2. A violation is
  classified in the `grant_denied` class with no quality retry and no fallback. For the
  final movement the same check is expressed as `candidate_mismatch` against the recorded
  candidate `result_tree` (§8).
- **Every successful `repo_write` attempt records exactly one change set**, including a
  **no-op** one where `base_tree == result_tree` — a movement that was authorized to write and
  chose to change nothing still declared a `change_set` output (§2 rule 15), and that
  declaration must be satisfied rather than silently vanishing. Movements without `repo_write`
  record none, so the final verification movement and other read-only movements never produce a
  provisional change set.
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
- A movement fails when **no further execution path is authorized** — retries and fallbacks
  exhausted, budget exhausted, or an immediate terminal cause such as `grant_denied`,
  `protocol_error`, human-gate rejection, or `composition_unresolvable`;
  `SUPERSEDED` exists only at the attempt level (steer, instruction revision, or an
  approved amendment → new attempt).
- `CONTESTED` is **not** a state at any level. It is a value of the `review_outcome`
  projection (§8); a contested movement reaches `WAITING_HUMAN` through its human gate
  like any other.

**`WAITING_HUMAN` is a projection, and it has a defined exit.** There is no
`movement.waiting_human` event: a separate state event would be a second authority
competing with the decisions themselves, and a crash between the two would leave the run
stuck in a state no decision explains. Instead:

- **Entry** — a movement or run is `WAITING_HUMAN` exactly while it has ≥1 **unresolved
  blocking decision** (a `decision.requested` with no terminal counterpart), together with
  `attempt.blocked` where an attempt raised it.
- **Exit** — when the last blocking decision becomes terminal, whether by
  `decision.resolved`, by `decision.obsoleted`, or by an amendment terminal event that
  resolves its own decision (§9), the movement and run project back to `RUNNING`. A
  subsequent `attempt.started` is therefore legal without any intervening event.
- A decision that opens a gate but cannot be answered — for example a *rejected* gate —
  terminalizes through the movement's failure instead, so no decision is left dangling.

This is why terminal run events must obsolete every remaining pending decision: otherwise a
terminal run would still project `WAITING_HUMAN`.

**Attempt identity.** `attempt_id` is an opaque string, unique within the run, and is the
identifier used on the wire (§4), in journal events, and in every mark binding.
`attempt_number` is a separate per-movement ordinal used only for display. The two are
never interchanged: v0.1's journal example implied a numeric `attempt_id`, which
contradicts the protocol's opaque-string requirement.

**Budget accounting.** `active_wall_clock_min` bounds active execution only — adapter runs,
acceptance, **composition**, retries, fallbacks, revision restarts, and decision resumes —
excluding `WAITING_HUMAN` and stopped time. The three phases of Appendix D (`adapter`,
`acceptance`, `composition`) are exactly the phases that consume it; composition counts because
a large fan-in is real work the run must be able to run out of time during. Because revision
restarts and decision resumes are bounded by time alone (§3), the accounting must be
**replay-stable** — every projection of the journal yields the same consumption — and crash-safe.
It is deliberately *not* claimed to be exact: an uncertain interval is bounded best-effort, per
the recovery rule below.

- Active time is delimited by paired, fsynced `execution.started` / `execution.stopped`
  events. Because v0.2 is sequential, **at most one interval is open at any time**.
- A **monotonic clock is used only within the living process** that opened the interval.
  Monotonic readings are not comparable across a restart — different boot epochs, different
  process lifetimes — so they are never persisted as an identity. What the journal stores is:
  `execution.started {wall_start, remaining_at_start}` and
  `execution.stopped {charged_duration}`, where `charged_duration` is computed by the
  opening process from its own monotonic clock.
- Consumption is the sum of `charged_duration` over closed intervals. An open interval is
  charged against the remainder for admission decisions using the current process's
  monotonic reading.
- Consumed history is never retroactively edited. A budget decrease (§9) yields
  `remaining_time = max(0, new_time_cap − consumed)` and
  `remaining_retries = max(0, new_retry_cap − retries_consumed)` — two independent caps, so a
  budget amendment that changes only one leaves the other untouched.
- On recovery, an `execution.started` with no matching `execution.stopped` is an **uncertain
  interval**: the process that held the monotonic reference is gone, and wall-clock cannot
  substitute for it because clocks jump — NTP steps, suspend/resume, and manual changes all
  make a wall gap an unreliable upper bound. It is closed deterministically with
  `execution.stopped {reason: recovered, charged: clamped}` where

  ```text
  charged_duration = min( max(0, observed_at − wall_start), remaining_at_start )
  ```

  `observed_at` is **sampled at recovery** — it is not journaled state — so recovery records
  both `observed_at` and the resulting `charged_duration` in the fsynced `execution.stopped`.
  Every later replay reads the recorded charge and never recomputes it, which is what makes
  the projection stable. Honestly labelled, this is **bounded best-effort accounting, not
  fail-closed**: a backward clock jump between `wall_start` and recovery can undercharge. The
  clamp guarantees only the upper bound — an uncertain interval never costs more than the
  budget that was actually available when it opened.
- Each attempt receives the remainder at its start (`request.budget`).
- **Exhaustion mid-flight has an explicit terminal path.** If the budget runs out while an
  adapter or an acceptance command is running, the attempt must be terminalized before the
  movement can fail — otherwise `movement.failed {budget_exhausted}` would leave a live
  `RUNNING`/`VERIFYING` attempt behind, and the derived `attempt.cancelled` cannot be used
  because it belongs solely to run cancellation:

  ```text
  stop/cancel the active process (§4 termination, §6 control channel)
  → execution.stopped {reason: budget_exhausted}
  → attempt.failed {kind: budget_exhausted}      # core-determined, not a vendor failure
  → movement.failed {budget_exhausted}
  → run.failed {budget_exhausted}
  ```

  This is distinct from a **criterion timeout**: a per-criterion `timeout_min` reached while run
  budget remains yields criterion `ERROR` on the ordinary quality path (§7). Only exhaustion of
  the *run's remaining budget* takes the budget path, which consumes no quality retry — there is
  nothing left to fund one.

  **Composition exhaustion needs no attempt event**, because fan-in and candidate composition
  run *between* attempts, not inside one (§5, §8) — there is no live attempt to terminalize:

  ```text
  movement fan-in:          stop composition → execution.stopped {budget_exhausted}
                            → movement.failed {budget_exhausted} → run.failed {budget_exhausted}
  candidate composition:    stop composition → execution.stopped {budget_exhausted}
                            → run.failed {budget_exhausted}
  ```

  Candidate composition is run-scoped by definition, so it fails the run directly with no
  movement in between.

**Active-run exclusivity and the two locks.** These are different concerns and must not
share a mechanism:

- A **repository state lock** (OS file lock) guards state mutations — journal appends, ref
  updates, snapshot writes, CAS operations. It is held only for the duration of a mutation,
  never across a human wait and never across an adapter execution.
- A per-run **execution-driver lease** (`runs/<run-id>/driver.lease`, §1) is held for the
  whole active execution episode by whichever process is driving attempts. A PID alone is
  insufficient — PIDs are reused and a stale file would masquerade as a live owner — so the lease
  records PID, **process-start identity**, a random **incarnation token**, and a monotonic
  **authority epoch**, all re-verified before it is honoured.

  Process-start identity, portable and without cgo:

  | Platform | Source |
  |---|---|
  | Linux | `/proc/sys/kernel/random/boot_id` **plus** `/proc/<pid>/stat` field 22 (start ticks). The boot id is required because a lease can outlive a reboot, where PID + start ticks alone can collide. Field 22 must be parsed after the final `)`, since the command name may contain spaces and parentheses. |
  | macOS | `proc_pidinfo(pid, PROC_PIDTBSDINFO)` → `pbi_start_tvsec` / `pbi_start_tvusec`. |

  Sandbox or cross-user policy can deny process inspection. **Failure to read or re-verify
  identity is "owner not safely verifiable" — never proof that the recorded owner is live.**

  **Fencing is a compare-and-swap on every *driver-authorized* mutation, not a one-time act.**
  The scope matters: `answer`, gate and amendment approvals, `amend`, `cancel`, `apply`, and
  `promote-score` legitimately mutate state **without** holding the lease (§7 command authority),
  so a universal lease check would forbid the commands the design depends on. What requires the
  CAS is exactly the set of mutations that constitute execution — attempt lifecycle, evidence,
  change sets, acceptance, candidate materialization, budget intervals.

  For those, while holding the repository state lock, the core checks: the run is nonterminal, the
  authority epoch and token match the durable authority record, and the PID and start identity
  still match. Non-driver commands take the same state lock but authorize against run lifecycle
  instead.

  **The epoch is journaled; the token never is.** `driver.lease` is removable — that is the point
  of reclamation — so an epoch held only there could be reused by a fenced incarnation after the
  lease vanished. The monotonic `authority_epoch` is therefore recorded in the journal by
  `authority.granted`, and every event that fences carries the epoch it moved to, so the current
  epoch is a **projection** like every other state (§1). `runs/<run-id>/authority.json` is a
  fsynced checkpoint of that projection, not its authority.

  The **token** is deliberately excluded from the journal: it exists to prove that a process is the
  same incarnation that acquired the lease, so journaling it would let any journal reader forge
  authority. It lives in `driver.lease` and in the owner's memory only.

  Consequently the CAS checks four things together, and **`driver.lease` must exist and match** —
  otherwise a driver whose lease was released but whose process is still live would remain
  authorized:

  ```text
  run is nonterminal
  AND authority_epoch == the current epoch projected from the journal
  AND driver.lease exists, and its epoch and token match
  AND the recorded PID and process-start identity still match
  ```

  **Fencing and terminalization are one transition, under the canceller's authority.** Revoking
  the epoch and then appending `run.cancelled` as a second step is impossible: the canceller does
  not hold the driver authority the CAS demands, and the fenced driver no longer does either, so
  the append could never pass. Instead, holding the state lock, the canceller performs a single
  transition — increment the epoch, revoke the token, append `run.cancelled` — authorized by run
  lifecycle rather than by the lease. There is never an intermediate state in which the run is
  fenced but not terminal.

A run is *active* while nonterminal (`RUNNING` or `WAITING_HUMAN`). v0.2 allows one active
run per repository: `partitur run` refuses to start while one exists (resume or cancel it
first). Commands accept an explicit run id or select the unique active run. The nonterminal
journal state is the logical guard; the lease guards concurrent *drivers* of the same run.

**Cancellation is run-scoped.** `partitur cancel` cancels the *run*, not a single attempt.
An attempt-scoped cancel would leave the movement `RUNNING` with no selection reason for what
comes next — not a retry, not a fallback, not a restart — so v0.2 does not offer one. The
protocol-level `cancel` (§4) still targets the current attempt, because that is the process
that must stop; the *authority* is the run-scoped request. `run.cancelled` atomically projects
every nonterminal movement and attempt to `CANCELLED` (Appendix B), and if no driver holds the
lease, `partitur cancel` completes `run.cancelled` itself rather than leaving a cancelled run
active until someone happens to `resume`. Recovery is therefore only needed for a crash
between the request and its terminalization.

**The control channel.** With no daemon, nothing waits in memory, so out-of-band
control — cancellation, and revision approval that supersedes a nonterminal attempt — needs a
durable path that reaches a driver mid-execution:

1. The requesting command appends the authoritative control event under the state lock
   (`cancel.requested`, or `amendment.approved` for a supersede).
2. It then wakes the verified current lease holder as a **best-effort latency
   optimization** — never as the mechanism of record.
3. The driver watches control state continuously while an adapter execution is pending —
   a separate goroutine tailing the authoritative journal while the stdout reader stays blocked.
   Polling only between criteria cannot interrupt a long `execute`, so this is a continuous
   watch, not a checkpoint. Measured append-to-detection latency at a 20 ms poll interval was
   22 ms on macOS and 24 ms on Linux, bounded by poll interval plus scheduling. A signal can
   prompt an immediate read; correctness never depends on it.
4. On observing the request the driver issues protocol `cancel`, awaits the `execute`
   response, then applies the outer termination grace and process-tree sweep (§4).
5. If no driver is alive, the cancelling command itself completes the terminalization; a
   later `resume` observes an already-terminal run and launches nothing.
6. **A live but wedged owner** — lease verified, yet the driver never acknowledges the journal —
   is the case a responsive/dead dichotomy misses. After a bounded acknowledgement deadline the
   canceller re-verifies the lease owner and terminates it and its process tree (§4). Then,
   **holding the repository state lock, it performs one transition**: increment the authority
   epoch, revoke the token, close any open budget interval, remove the lease, and append
   `run.cancelled` — all authorized by run lifecycle, not by the lease (§6 fencing).

   It must be one transition, not a sequence. Fencing first and appending afterwards cannot work:
   once the epoch is bumped, neither the canceller nor the fenced driver holds driver authority,
   so a subsequent append would fail its own check. The invariant this preserves: **the run is
   never declared cancelled while the old execution authority could still mutate it, and never
   left fenced without being terminal.**

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
- `causation_id` references another event's `event_id` and names the **source authority** for
  a derived or recovery-synthesized event. It is the idempotency key only where Appendix B
  says so — one source event can legitimately derive several distinct events, so it is not
  universally sufficient on its own.
- `event_id` and `seq` are allocated together by the single writer; `seq` is contiguous and
  strictly increasing within the run.
- Pending decisions are represented as an append-only pair — `decision.requested` /
  `decision.resolved` — never as a mutable record; readers project the pair into a
  "currently pending" list. Amendment decisions are the one exception: their terminal
  events resolve their own decision directly and no separate `decision.resolved` is ever
  appended on any amendment path (§9).

The complete registry of **event types and their state effects** — fsync requirement,
idempotency key, legal source state, and resulting projection — is **Appendix B**, which is
normative. An event type absent from Appendix B does not exist. Appendix B is *not* yet a
complete payload registry; per-event field schemas are completed in the follow-up PR.

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
as applicable. The movement's `acceptance_spec_hash` is the canonical hash of the **effective
compiled acceptance plan** — declared criteria with any replacements applied, plus
core-generated integrity checks, in the deterministic order below, plus `human_gate` — not of
the raw acceptance block as written. A mark must bind to what actually ran. Both hashes are
recorded with the evidence so a mark says exactly which specification it proved (Appendix A).

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
  If an acceptance command mutates the worktree — measured by the **full invariant**: tracked
  content, non-ignored untracked files, symlink targets, file modes, and protected-path
  integrity — the criterion fails with `acceptance_mutated_workspace` (ignored caches and
  outputs are permitted). The change set that was verified must be the change set that ships.
- `artifact` — the declared output was emitted, its `kind` matches, and its recorded hash
  matches the immutable instance (manifest integrity). An optional `expected_hash` is
  allowed for pre-known content. A declared output never emitted fails here.

  **Integrity checks are core-generated.** The compiler generates one artifact check for
  **every** ordinary declared output, so "declared but never emitted" is always caught without
  the user having to remember a criterion per output:

  - Reserved id `partitur.artifact.<logical_output_id>` — in the core namespace, hence outside
    the score's identifier grammar (§1), so it can never collide with a declared criterion.
  - `kind: change_set` outputs are excluded: they are core-synthesized (§5) and satisfied by
    `change_set.recorded`.
  - **Replacement is keyed by the referenced output, not by criterion id.** An explicitly
    declared `artifact` criterion referencing the same output suppresses the generated check
    for it — that is how an `expected_hash` is supplied.
  - **Deterministic order:** declared hard criteria in declaration order, then generated
    checks in output-declaration order. Generated checks participate in
    `acceptance_spec_hash`, so adding or removing an output correctly changes the effective
    acceptance specification and cannot silently alter what a mark proved.

  **Declared vs generated is the line that matters.** A generated check proves the performer
  *delivered*, never that the work is *correct*, so it can never on its own earn a mark:
  §2 rule 2, VERIFIED, and the `require ∋ verified` achievability rule all require the score
  to **declare** a hard criterion. An explicitly declared `artifact` criterion is a declared
  hard criterion and does count — the score chose it. Without this distinction, any write
  movement declaring an output would auto-satisfy its verification floor and could be marked
  VERIFIED because a file exists, which is the overstatement V-001 forbids.

**Acceptance runner authority — a deterministic invocation shape, not confinement.** A
criterion command is declared *in the score*, which the user authors and approves, so it runs
under the user's own authority. Confining it is therefore **not** the security boundary that
confining an agent is, and v0.2 does not pretend to sandbox it. What the core fixes is the
*shape of the invocation* — deliberately not "a reproducible environment", which a fixed argv
cannot deliver: toolchain versions, filesystem contents, and external services all remain
outside Partitur's control.

- **Fixed by the core:** working directory is the attempt worktree; the argv array is executed
  directly with **no shell interpretation**; the environment is a core-defined allowlist;
  temporary files default under the attempt's staging directory; output is bounded; the
  timeout is `min(criterion timeout, remaining active budget)`.
- **Advisory only:** filesystem and network restrictions. The core neither claims nor
  attempts physical confinement of an acceptance command.
- **Post-hoc detection is the control, and its reach is limited.** After the command runs, the
  core compares the worktree across tracked content, **non-ignored untracked files, symlink
  targets, and file modes**; divergence fails the criterion `acceptance_mutated_workspace`
  (ignored caches and outputs permitted), and protected paths (§2) are checked exactly as for
  a change set. This is what enforces "the change set that was verified is the change set that
  ships". It **cannot** detect network effects, writes outside the worktree, or mutation of
  shared Git refs and run state — those are guarded by isolation and integrity/CAS checks
  (§1), not by this comparison.

A criterion that cannot be spawned, times out, or whose runner crashes yields `ERROR`, not
`FAIL` — the criterion produced no verdict.

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
winning. It is fail-closed: before resuming any criterion the core re-verifies the worktree against the
**full invariant** — tracked content equal to the `subject_tree` recorded in
`acceptance.started`, plus non-ignored untracked files, symlink targets, modes, and
protected-path integrity — and on any mismatch records
`acceptance.failed {reason: recovery_subject_mismatch}`.

**Command authority.** Exactly one process drives attempts. The distinction is between
*recording a transition* and *launching execution* — several commands legitimately mutate
state, but only two ever start an adapter:

| Command | May mutate state | May launch an attempt |
|---|---|---|
| `validate`, `status`, `logs` | no | no |
| `init` | creates `.partitur/` and, when absent, a draft score — never overwrites either | no |
| `answer` | records `decision.resolved` only | no |
| `approve` (gate) | records the gate `decision.resolved`; the resulting completion or failure follows the Appendix C event sequence (`attempt.completed` then `movement.succeeded`, or `movement.failed`) — approval does not manufacture one atomic completion event | no |
| `approve` / `amend` (amendment) | may atomically write a snapshot and supersede attempts (§9) | no |
| `cancel` | appends the durable run-scoped cancel request, and — after verifying no valid lease owner remains — may append `run.cancelled` itself (§6) | no |
| `apply`, `promote-score` | own transactions (§8) | no |
| `run`, `resume` | full | **yes** — acquires the driver lease |

So §4's "the core starts a new attempt once decisions resolve" reads precisely as:
*resolution makes the run eligible to continue.* A live driver continues it; otherwise a
later `resume` does. `answer` never blocks waiting for work to finish.

**CLI v0.2.**

```text
partitur init            # create .partitur/, its .gitignore (runs/ and work/), and — when no
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
partitur cancel          # cancel the RUN (run-scoped; §6 control channel). There is no
                         #   attempt-scoped cancel — see §6
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

- **VERIFIED** — the attempt has `acceptance.evaluation_completed`, the movement
  **declares** ≥1 `hard` criterion, and every hard check — declared *and* core-generated —
  is `PASS`. Zero *declared* hard criteria can never yield VERIFIED: core-generated
  integrity checks close the "declared but never emitted" hole (§7) but can never by
  themselves earn a mark, or a movement would be VERIFIED because a file exists.
- **APPROVED** — a `decision.resolved` with `decision_type: human_gate` approves this
  movement, carrying `gate_id`, movement/attempt/`score_revision`, the exact `subject_tree`,
  the approval scope, and any overridden finding instance ids. Whenever
  `overridden_finding_instance_ids` is non-empty the event **must** carry a non-empty
  `override_reason`; an override with no recorded reason is invalid, because overriding a
  reviewer's blocking judgment is exactly the decision that must stay auditable. An approval
  never carries over to another attempt, revision, or subject tree.
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
# The candidate additionally records candidate_composition_dependency_hash, which binds the
# composition ENVIRONMENT (A.4) — the same trees composed under a different Git build or merge
# configuration are not the same composition (§5).
candidate_id = H("partitur/candidate",
                 { base_tree, result_tree, ordered_change_sets })      # Appendix A
```

It is a **content** identity, independent of score revision. A run with no write movements
records `result_tree == base_tree`. A composition conflict is handled **before** recording,
and terminally: it fails the run with `composition_unresolvable` per §5 — never discovered at
`apply` time.

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

At adapter exit the core applies the full read-only post-hoc invariant of §5 — tracked content,
non-ignored untracked files, symlink targets, modes, protected paths — and additionally verifies
that the worktree tree equals the recorded candidate `result_tree`. `subject_tree` equality
alone would miss an untracked file or a mode change that a later step could observe. Any
mismatch is `candidate_mismatch`, classified in the `grant_denied` class —
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
score.promotion_started { expected_root_file_hash, target_snapshot_file_hash, candidate_id }
→ temp write + fsync + atomic rename of the root score
→ score.promoted + fsync
```

`promote-score --recover`, under the lock: root **file** hash == target → complete
`score.promoted` idempotently under the original transaction id; root file hash == expected → resume the *same*
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
3. **Reserved fields** — the v0.2 reserved set is `{ /revision, /status }`. An operation
   *touches* a reserved pointer if its `path` (or `from` for `move`/`copy`) equals it,
   descends from it, or is an ancestor of it. `test` operations on reserved pointers are
   permitted. `/status` is reserved because the **core alone** may construct the
   `draft → finalized` transaction (§2); an ordinary adapter or CLI proposal touching
   `/status` rejects as `reserved_field`, which is what stops a performer from finalizing a
   score on its own.
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
The proposal's origin (adapter or CLI) never affects any rule — with the single, explicit
exception that only the core may construct the reserved `/status` finalization amendment, which
still always requires human approval.

**Typed comparison, never a JSON diff.** Neither classification nor impact computation
inspects RFC 6902 operations or a generic diff — array-index ambiguity would make the same
before/after pair yield different results per diff algorithm. The core compares the two
validated score **ASTs**:

- `movements` and `parts` are matched by immutable id; criteria by their `id`. **Content
  comparison is by id; sequence is compared separately.** A.1 hashes movement and criterion
  *declaration order* because that order is semantic (§5 tie-breaking, §7 short-circuiting), so
  a reorder is a real change and is recorded as an explicit collection-order change — never
  silently absorbed into a content diff, and never invisible. For envelope classification,
  movement count, ids, **and** order must all be identical, or the change is unclassified.
- `policy.allowed_paths`: strict **set** removal qualifies — the resulting set must be a
  proper subset. Order is not considered, because the field is declared an unordered union of
  positive patterns (§2) and A.1 hashes it sorted; requiring "order-preserving" removal here
  would contradict both.
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
becomes ambiguous the moment a collection is reordered. Items are matched **by id** for
deterministic content comparison, while semantic **sequence** is compared and hashed
independently in declaration order (A.1) — so a reorder is neither absorbed into a content diff
nor lost from the hash. An id-less array that differs in any way is recorded as a single
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
| `NARROW_PATHS` | Strict **set** removal of an `allowed_paths` entry | No attempt of **any** movement whose effective `paths_ro` or `paths_rw` would change has started — `allowed_paths` scopes reads as well as writes |
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
    { actual_adapter_id,                    # H() already injects the version tuple (A.2)
      canonical score-derived semantic execute-request projection,
        using extensions.<actual_adapter_id> })
```

The projection is defined in Appendix A.5, whose shape is **provisional until the follow-up PR**
— "includes at least" is not an identity definition, and A.5 is not yet one either. It is keyed by the adapter that actually served the attempt (primary or
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
             corresponding change_set_ids, composition_algorithm_version,
             composition_environment_hash })          # A.4 — Git build and merge config
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

- **Every nonterminal attempt** — `STARTING`, `RUNNING`, or `VERIFYING` — is superseded, not
  only running ones: a human gate can leave an old-revision attempt sitting in `VERIFYING`,
  and letting it complete would attach evidence to a revision that no longer exists. The core
  records the approval, cancels/terminates the adapter through the §6 control channel, accepts
  no further protocol or artifact output from the superseded attempt once the new head is
  recorded, and charges the time until actual termination against the active budget.
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

**Deterministic first failure.** "First failure wins" is only meaningful if ties are broken
deterministically, so that the same proposal always yields the same rejection reason and the
same audit delta:

1. The pipeline order of steps 1–9 above decides *which* check reports.
2. Within a check, typed `score_changes` are ordered by **stable selector**, then by
   `operation` (`add` < `remove` < `replace`).
3. `authority` entries are ordered by `movement_id`, then by pattern string.

No step depends on map iteration order, nor on the order in which a *generic diff* would emit
changes. Proposer operation order is **not** irrelevant, though: RFC 6902 application is
order-sensitive by definition, and the `partitur/patch-operations` identity (Appendix A) hashes
the operations as written. What is order-independent is the typed comparison and the impact
report derived from the before/after ASTs.

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
auto-approval envelope with glob-subset or criterion-strengthening proofs, an OS-sandbox
wrapper for enforcement (a separate security milestone — §4 fails closed without it), external
custom merge drivers (§5), and a **privileged containment backend for absolute descendant
ownership on macOS** — Linux cgroup v2 covers this where permission allows, but macOS has no
unprivileged equivalent, so §4's supervision is conformance cleanup rather than containment. See
CONCEPT's minimal-harness test; each of these fails it or belongs to score/extension space.

---

# Appendix A — Canonical encoding and identity domains

Normative. Every identity in Partitur is a hash of a canonical encoding of an exactly
specified projection. Two implementations of the encoding is a silent correctness bug, so
there is one encoder and every caller names a domain.

## A.1 Canonical encoding

Canonical JSON is **RFC 8785 (JCS)**: UTF-8, object keys sorted by UTF-16 code unit,
no insignificant whitespace, ES6 number serialization.

- **Numeric range.** Two different rules, deliberately, because the two sources have
  different authors:
  - **Score-schema numbers** must be integers in the I-JSON safe range
    `[-(2^53 - 1), 2^53 - 1]` (compiler rule 13). Partitur authors the schema, so it can
    simply forbid the ambiguous cases.
  - **`extensions.<adapter-id>` payloads** are authored in the score or cast — by the user,
    for a vendor to consume — are opaque to the core, participate in
    `execution_dependency_hash`, and may legitimately contain fractional numbers. They are
    encoded under the **full JCS/I-JSON number rule**, whose exact boundary behaviour the
    A.1 spike confirms, as do the raw RFC 6902 operations hashed pre-validation for
    `partitur/patch-operations`. Rejecting them outright would make legal payloads
    unhashable.
- **Unicode.** No normalization is applied; byte-identical input yields byte-identical
  output. Sorting is by UTF-16 code unit per JCS, not by code point.
- **YAML → JSON mapping.** `yamlsafe` parses one YAML 1.2 representation graph, then rejects —
  **at its own API boundary, before constructing the JSON AST** — duplicate keys, anchors,
  aliases, merge keys, custom tags, and every resolved scalar tag other than `!!str`, `!!bool`,
  `!!null`, `!!int`, or `!!float`. It validates numeric scalars as finite representable binary64
  values. These are `yamlsafe` decode errors: a general-purpose YAML parser builds its
  representation graph first and need not fail on them itself, so "rejected at parse time" would
  be unimplementable as literally stated.

  YAML 1.2 resolves a sexagesimal-looking plain scalar such as `12:34:56` as a **string**, not a
  numeric tag, so tag filtering alone does not catch it — `yamlsafe` rejects that lexical form
  explicitly.

  Block scalars keep their chomping semantics: clip (`|`, `>`) preserves the final newline, strip
  (`|-`) removes it, keep (`|+`) preserves additional trailing newlines.
- **Numeric ingress.** The split between schema-controlled and opaque values happens **before**
  encoding, so one encoder serves both:

  This applies to **every** JSON or YAML ingress path that feeds a hash — score and cast YAML,
  opaque `extensions` subtrees, and the raw RFC 6902 operations hashed pre-validation for
  `partitur/patch-operations`. A patch is untrusted input, so it gets the strictest treatment, not
  an exemption.

  1. Decode every number to a finite IEEE-754 binary64 value.
  2. At schema-controlled paths, validate integral and within `[-(2^53 - 1), 2^53 - 1]`
     (compiler rule 13).
  3. Leave opaque `extensions` values as finite binary64, fractions and out-of-safe-range
     magnitudes included.

  **Rejected at ingress, never encoded:** NaN, ±Infinity, overflow (`1e9999`), non-zero values
  that underflow to zero (`1e-9999`), lone UTF-16 surrogates, and **negative-zero spellings**.
  JCS maps negative zero to `0`, and RFC 8785 verified erratum 7920 advises rejecting `-0` at the
  parser; Partitur follows the stricter rule, because otherwise `-0` and `0` would silently share
  an identity. A programmatic negative zero that reaches the encoder still serializes as `0`.

  Decimal lexical precision is **not** preserved — JCS canonicalizes the parsed binary64 value.
  A value needing precision beyond binary64 must be carried as a string.
- **Omitted vs explicit defaults.** A projection is built from the **validated AST after
  defaults are applied**, so an omitted field and an explicitly written default produce the
  identical hash. Optional fields with no default are omitted from the projection entirely
  rather than encoded as `null`.
- **Ordered sequences vs unordered sets.** The rule is *semantic*, not structural: a
  collection is sorted only when its order genuinely carries no meaning. Sorting a
  collection whose order is meaningful would let two different scores share a `base_hash`.

  | Collection | Encoding | Why |
  |---|---|---|
  | `movements` | **declaration order preserved** | Order breaks topological ties and can change the fan-in result (§5); §9 treats a reorder as a real change |
  | `acceptance.hard`, `acceptance.review` | **declaration order preserved** | Criteria run in declaration order and short-circuit on the first failure (§7) |
  | `ordered_change_sets` | order preserved | It is the composition order (§5) |
  | `parts` | sorted by id | A mapping; order is not observable |
  | `allowed_paths` | sorted, duplicate-free | Declared an unordered union of positive patterns (§2), so reordering must **not** change the hash |
  | `needs`, `grants`, `capabilities`, rubric id sets | sorted, duplicate-free | Sets by definition |

  Correspondingly, typed comparison (§9) matches movement *content* by id but records a
  distinct collection-order change when the movement sequence differs — so a pure reorder is
  neither invisible to the hash nor misattributed to a content edit.

**Confirmed by spike.** A Go JCS encoder reproduced RFC 8785's Appendix B vectors, the published
canonical file vectors, and the published first-1,000-number checksum. The UTF-16 ordering rule is
observably load-bearing: for keys `U+E000` and `U+10000`, naive Go string comparison yields
`U+E000, U+10000` while JCS yields `U+10000, U+E000`, because `U+10000` begins with UTF-16 unit
`D800`. Sorting by Go string order would silently produce a different identity. Composed `Å` and
decomposed `A + U+030A` likewise produced different canonical bytes, confirming no normalization.

## A.2 Hash construction

```text
H(domain, value) = "sha256:" + hex(sha256(JCS({
  "domain":                     <domain>,
  "canonical_encoding_version": <A.3>,
  "projection_version":         <that domain's projection version, A.3>,
  "value":                      <the exact normalized projection>
})))
```

Every version that can change the bytes is **inside** the preimage, so a domain confusion, an
encoder change, or a projection change can never produce a colliding identity. Recording a
version alongside the result would not achieve this — the hash itself must depend on it.

**Recomputation across versions.** When the core recomputes a historical identity — the
executed-dependency check of §9 is the main case — it uses the **version tuple recorded with
that attempt**, not the current one. If the running binary no longer implements that tuple, the
run fails closed with `unsupported_run_format` rather than silently comparing hashes computed
under different rules.

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
| `partitur/criterion-spec` | ⚠ *provisional* — an exact tagged union over criterion kinds (`hard.run`, `hard.artifact`, `review`), carrying only that kind's semantic fields. The field names here are indicative, not final. |
| `partitur/acceptance-spec` | ⚠ *provisional* — the **effective compiled acceptance plan** (§7): declared criteria with replacements applied, plus core-generated integrity checks, in declaration order, plus `human_gate`. Exact projection deferred. |
| `partitur/change-set` | `{base_tree, result_tree}` |
| `partitur/candidate` | `{base_tree, result_tree, ordered_change_sets: [change_set_id]}` |
| `partitur/candidate-composition` | `{base_tree, ordered_contributing_movement_ids, ordered_change_set_ids, composition_algorithm_version, composition_environment_hash}` |
| `partitur/composition-environment` | `{git_build, object_format, merge_invocation, strategy_options, merge_renormalize, effective_merge_config}` — §5. Separate because both the candidate-level and the movement-level composition identity need it, and because the environment can change while every tree stays the same. |
| `partitur/execution-dependency` | ⚠ *provisional* — A.5 |
| `partitur/patch-operations` | The raw RFC 6902 operations array, for pre-validation rejection records only (§9). |

This registry is **incomplete** — `partitur/resolved-cast` and `partitur/score-subtree` are
missing and are added by the follow-up PR, along with object-format qualification for
Git-native ids.

Artifact and tree hashes are **not** canonical-AST identities: artifact instances are hashed as
raw file bytes (`sha256:<hex>`, §1) and tree/commit ids are Git-native and must carry their
object format (`git-sha1:<hex>`, `git-sha256:<hex>`) rather than a bare hex string. Mixing a canonical-AST hash
with a raw-byte hash is a category error; each identity states which kind it is.

## A.5 The execution-dependency projection

**Provisional shape — not implementation-ready.** This states the intended content well
enough to review the design; it is *not* yet the exhaustive projection an implementation can
serialize against, and the follow-up PR completes it (see the header note). What is already
binding: a field absent from the final projection is absent from the hash, and adding one is a
`projection_version` bump.

```text
{
  actual_adapter_id,                  # the adapter that SERVED the attempt (§1 per-attempt
                                      #   record), not the part's intended binding
  movement: {
    id, part,
    instruction,
    needs                             # sorted
    inputs,                           # sorted logical output ids
    outputs,                          # {artifact_id, kind} in DECLARATION order — output
                                      #   order controls generated-check order (§7)
    grants,                           # effective grants, as a sorted set
    acceptance                        # the EFFECTIVE compiled acceptance plan (§7), in
                                      #   declaration order — never id-sorted, since
                                      #   criteria short-circuit in order (A.1)
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
for: `run_id`, `attempt_number`, `attempt_id`, filesystem paths (`workdir`,
`output_dir`), `session_hint`, `request.feedback`, remaining budget, and wall-clock time.

---

# Appendix B — Journal event registry

Normative. **An event type absent from this table does not exist.** `sync` marks appends
that must be fsynced before the core proceeds. `idem key` is the value that makes a repeated
append a no-op — essential for the derived and recovery-synthesized events. Derived events
(marked *derived*) are never appended independently; they are projected idempotently from
their source event, including at recovery.

**Authority.** Every **non-derived** event in B.1–B.6 is **authoritative**: it is the record
that constitutes a transition, and projections read it. Rows marked *derived* — cancellation,
supersession, obsoletion — are projected idempotently from an authoritative source event and are
never appended on their own. The `log` and `progress` events of B.7 are
**observational**: sanitized, bounded, journaled for later display, and read by no projection
(§4 diagnostics privacy). Nothing observational can change state.

**Idempotency semantics.** Effective uniqueness is the pair `(event_type, idem_key)`:

- Same key, equivalent payload → **no-op**. This is what makes recovery replayable.
- Same key, *differing* payload → `journal_idempotency_conflict`, a recovery halt. Two
  different facts claiming one identity is corruption, not something to merge.
- `causation_id` names the *source authority* for a derived or recovery-synthesized event. It
  is the idempotency key only where this table says so — it is not universally sufficient,
  because one source event can legitimately derive several distinct events.

**Every event whose projection makes the run terminal obsoletes all remaining pending
decisions.** That is broader than the three `run.*` events: the final movement's
`movement.succeeded` carries the run's `SUCCEEDED` transition, and a final-movement
`movement.failed {human_gate_rejected}` carries its `FAILED` transition (§8). Keying the rule
to event *names* rather than to *projected run terminality* would leave those two paths
projecting `WAITING_HUMAN` forever (§6). Correspondingly, `decision.obsoleted` derives from any
such event, not from `amendment.approved` alone.

**Crash between cascading failures.** Attempt failure, movement failure, and run failure are
separate appends. Recovery **replays the disposition recorded on the failure event** and never
recomputes admissibility — recomputation could reach a different answer than the live process
did, which would make the projection depend on when it was replayed:

- Recorded disposition names a scheduled successor that is missing → synthesize it.
- Recorded disposition is *terminal* → synthesize `movement.failed`, and `run.failed` if that
  terminalizes the run. This holds even though a terminal failure is **uncharged** (§3) — being
  uncharged is precisely what marks it terminal.

Each synthesized event is keyed so a repeated recovery is a no-op.

> **Deferred to the follow-up PR:** exhaustive per-event payload schemas — required and
> optional fields, field types, closed enums, and subject/identity binding per event. The
> `Projection effect` column below states each event's *effect* and its binding obligations in
> prose, which is sufficient to review the state machine but **not** sufficient to implement
> serialization against. Appendix A's A.4/A.5 exhaustiveness is deferred with it.

## B.1 Run and movement lifecycle

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `run.started` | ✓ | run_id | — | Run → `RUNNING`; records base commit/tree, score snapshot hash, resolved-cast hash, root-score hash for CAS |
| `run.succeeded` | ✓ | run_id | Run `RUNNING` | Run → `SUCCEEDED`. On the **waived** path also carries the full candidate payload and binding (§8) |
| `run.failed` | ✓ | run_id | Run `RUNNING`/`WAITING_HUMAN` | Run → `FAILED`; carries reason (Appendix D) |
| `run.cancelled` | ✓ | run_id | Run nonterminal | Run → `CANCELLED`. When it terminalizes a fenced driver it also carries `fenced_epoch`, the epoch the authority moved to, so recovery projects the fence rather than inferring it (§6). Carries the affected movement and attempt ids and projects their cancellation **atomically**, so a crash mid-cancel cannot leave a cancelled run with a running attempt |
| `movement.ready` | | movement_id | Movement `PENDING`, deps succeeded | Movement → `READY` |
| `movement.started` | ✓ | movement_id | Movement `READY` | Movement → `RUNNING` |
| `movement.succeeded` | ✓ | movement_id + attempt_id | Attempt `COMPLETED` | Movement → `SUCCEEDED`; approves its artifacts and change set. Requires `attempt.completed` first — the completion order is always attempt, then movement. For the **final movement** this same event carries the run's `SUCCEEDED` transition (§8) |
| `movement.failed` | ✓ | movement_id | Movement `RUNNING`/`WAITING_HUMAN` | Movement → `FAILED`; reason ∈ Appendix D. For `human_gate_rejected` this **one** event atomically projects Attempt → `FAILED`, Movement → `FAILED`, and — for the final movement — Run → `FAILED`, charging no retry and no fallback. The idempotency key is always `movement_id`; the gate decision id is carried as causation and evidence, not as the key |
| `movement.cancelled` *derived* | — | source event_id + movement_id | — | Movement → `CANCELLED`; projected idempotently from `run.cancelled`. Not independently authoritative — an independent append would compete with `run.cancelled`'s atomic projection |

## B.2 Attempt lifecycle and performer selection

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `performer.selected` | ✓ | attempt_id | before `attempt.started` | Records the chosen performer, adapter id/version, model, enforcement posture and advisory dimensions for this attempt (§1), and **why** this attempt exists (`initial`, `quality_retry`, `fallback`, `revision_restart`, `decision_resume`). **Creates the attempt in `STARTING`**, which is what makes that state reachable and lets a spawn failure be attributed to a chosen performer. It records the *reason*; it never charges the budget — charging belongs to the failure event that caused it, so a retry cannot be double-counted |
| `attempt.started` | ✓ | attempt_id | Attempt `STARTING` | Attempt `STARTING → RUNNING`; records `attempt_number`, `execution_dependency_hash`, granted authority |
| `performer.completed` | ✓ | attempt_id | Attempt `RUNNING` | Attempt → `VERIFYING`. **Vendor execution ended; says nothing about success** (§6) |
| `attempt.completed` | ✓ | attempt_id | Attempt `VERIFYING`, acceptance and gate passed | Attempt → `COMPLETED` |
| `attempt.blocked` | ✓ | attempt_id | Attempt `RUNNING` | Attempt → `BLOCKED` (terminal); carries `pending_decision_ids` |
| `attempt.failed` | ✓ | attempt_id | Attempt `STARTING`/`RUNNING`/`VERIFYING` | Attempt → `FAILED`; carries the failure kind (Appendix D). Legal from `STARTING` so spawn and startup failures are representable. **Charges at most once, and only if it authorizes another attempt** (§3): a quality kind (`task_failed`) may consume one quality retry; an infrastructure kind may advance the fallback chain; `grant_denied` and `protocol_error` charge neither and terminalize the movement immediately |
| `attempt.cancelled` *derived* | — | source event_id + attempt_id | — | Attempt → `CANCELLED`; projected idempotently from `run.cancelled`, for the same reason |
| `attempt.superseded` *derived* | — | source event_id + attempt_id | — | Attempt → `SUPERSEDED`; projected from `amendment.approved` (§9) |
| `execution.started` | ✓ | `interval_id` | no interval open | Opens the (single) budget interval; carries `interval_id`, `phase`, `wall_start`, `remaining_at_start` (§6). Keyed on `interval_id` because one attempt legitimately opens several intervals |
| `execution.stopped` | ✓ | `interval_id` | that interval open | Closes it and charges `charged_duration`. `{reason: recovered, charged: clamped}` closes an uncertain crash interval by the deterministic clamp formula of §6 |

## B.3 Evidence

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `artifact.recorded` | ✓ | `(logical_output_id, attempt_id)` | Attempt `RUNNING`/`VERIFYING` | Registers the immutable instance and its byte hash (§1). A second append for the same key is `duplicate_artifact_instance` |
| `change_set.recorded` | ✓ | attempt_id | Attempt `VERIFYING` | Records `change_set_id`, `base_tree`, `result_tree`, and the pinning ref (§5). Only for `repo_write` movements |
| `composition.conflicted` | ✓ | `scope` + `target_id` + `composition_dependency_hash` | fan-in or candidate composition | **Evidence only — it projects no state.** Records `scope`, `target_id`, the ordered contributors and the conflicted paths. `scope: movement` ⇒ `target_id = movement_id`, and the terminal event is `movement.failed {composition_unresolvable}` — legal from `RUNNING`, since fan-in happens after `movement.started`. `scope: candidate` ⇒ `target_id = run_id` (**not** a `candidate_id`: composition failed, so no candidate exists), and the terminal event is `run.failed {composition_unresolvable}`. Recovery synthesizes the missing terminal event if a crash lands between the two appends. The key includes `target_id` because two movements can conflict over the same contributor list |
| `application_candidate.recorded` | ✓ | candidate_id | every `repo_write` movement succeeded | Records the candidate and **constitutes its initial binding** (§8) |
| `acceptance.started` | ✓ | attempt_id | Attempt `VERIFYING` | Binds `subject_tree` + `acceptance_spec_hash` before any criterion runs (§7) |
| `criterion.started` | ✓ | attempt_id + criterion_id | after `acceptance.started` | Carries `criterion_spec_hash` and the same subject binding |
| `criterion.completed` | ✓ | attempt_id + criterion_id | matching `criterion.started` | `outcome` ∈ `{PASS, FAIL, ERROR}` |
| `acceptance.failed` | ✓ | attempt_id | any `FAIL`/`ERROR`, or recovery | Terminal for this acceptance **and for the attempt**: projects Attempt `VERIFYING → FAILED` in the same transition, so no attempt is ever left stranded in `VERIFYING`. Reason ∈ Appendix D. Always terminalizes the attempt; charges a quality retry **only if another quality attempt is admissible** (§3), otherwise leads to terminal `movement.failed` without charging past the cap. The disposition is recorded in the event, and recovery replays it rather than recomputing. No separate `attempt.failed` is appended on this path |
| `acceptance.evaluation_completed` | ✓ | attempt_id | all criteria `PASS` | The **only** gateway to grade derivation (§8) |

## B.4 Decisions

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `decision.requested` | ✓ | decision_id | — | Adds to pending; `decision_type ∈ {question, human_gate, amendment, finalization}` |
| `decision.resolved` | ✓ | decision_id | pending | Removes from pending. Carries the answer, or for `human_gate` the `gate_id`, `subject_tree`, scope, and overridden finding instance ids (§8). **Never appended on any amendment path** (§9) |
| `decision.obsoleted` *derived* | — | source event_id + decision_id | — | Terminally closes a pending decision — either raised on a superseded revision, or outstanding when the run went terminal. Derives from `amendment.approved` **or** from any event whose projection makes the run terminal |

## B.5 Amendments

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `amendment.rejected` | ✓ | proposal_id | Run nonterminal | Terminal. Reason ∈ Appendix D. Records base hash and classifier version; records the typed delta only from pipeline step 6 on, else the patch-operations hash and error location |
| `amendment.routed_human` | ✓ | proposal_id | admissible | **Non-terminal** routing marker; appends `decision.requested` for the amendment |
| `amendment.approved` | ✓ | proposal_id | passed 1–9 | The **single authoritative transition**: new snapshot head, new revision, superseded attempt ids, obsoleted decision ids, re-bound `candidate_id`. Resolves its own decision directly. **Finalization special case** (`/status: draft → finalized`, §2): the same event additionally closes the draft phase and projects the interview movement to `SUCCEEDED`, manufacturing no `attempt.completed` and no VERIFIED/APPROVED evidence |
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
| `authority.granted` | ✓ | `authority_epoch` | Run nonterminal | Records that a driver acquired or reclaimed execution authority at a new monotonic epoch, with the owner's PID and process-start identity. Makes the current epoch a journal projection (§6) so it survives lease removal. The incarnation **token is never journaled** — journaling it would let any reader forge authority |
| `cancel.requested` | ✓ | run_id | Run nonterminal | The durable, **run-scoped** cancellation authority (§6) — never keyed by attempt, because there is no attempt-scoped cancel. Observed by a live driver mid-execution; otherwise the canceller itself terminalizes |
| `journal.tail_truncated` | ✓ | truncated seq | recovery | Records that an unparseable final line was discarded (§1) |
| `log` | | — | — | Mirrored adapter diagnostics; sanitized (§4) |
| `progress` | | — | — | Mirrored adapter progress |

---

# Appendix C — Acceptance recovery table

Normative (§7). Rows are evaluated **top-down; the first matching row wins**. Before
resuming any criterion the core re-verifies the worktree against the **full invariant** —
tracked content equal to the `subject_tree` recorded in `acceptance.started`, plus non-ignored
untracked files, symlink targets, modes, and protected-path integrity; on any mismatch it
records
`acceptance.failed {reason: recovery_subject_mismatch}`, which charges one quality retry only
if another quality attempt is admissible (§3). The event is keyed on `attempt_id` (Appendix B);
the causation id is evidence, not the key.

| Last durable state | Recovery action |
|---|---|
| `acceptance.failed` present | Terminal — synthesize no further criterion results. The attempt is already `FAILED` by that event's own projection (B.3); replay the **recorded** disposition (charged or not, §3) and its scheduling exactly once — never recompute admissibility at recovery |
| Any `criterion.completed` is `FAIL` or `ERROR`, no `acceptance.failed` | Append `acceptance.failed` idempotently — which terminalizes the attempt as `FAILED` — and start no further criterion |
| `criterion.started` without `criterion.completed` | Close it as `ERROR` — **including when the command in fact passed but crashed before the event was written** — then append `acceptance.failed` |
| All criteria completed, all `PASS`, no `acceptance.evaluation_completed` | Append `acceptance.evaluation_completed` idempotently |
| `acceptance.evaluation_completed`, required human gate not yet requested | Resume at the gate step; append one `decision.requested` idempotently |
| `decision.requested` (gate) unresolved | Append nothing. The unresolved decision **is** the `WAITING_HUMAN` projection (§6) — there is no state event to restore |
| Gate resolved approve, `attempt.completed` missing | Append `attempt.completed`, then `movement.succeeded` — in that order (B.1/B.2) — idempotently, including for the final movement the run's `SUCCEEDED` transition |
| Gate resolved reject, terminal failure event missing | Append `movement.failed {reason: human_gate_rejected, decision_id, subject_tree}` idempotently, keyed on `movement_id` (Appendix B) with the gate decision id carried as causation and evidence; that one event terminalizes attempt, movement, and — for the final movement — the run |
| `acceptance.evaluation_completed`, no gate required, `attempt.completed` missing | Append `attempt.completed`, then `movement.succeeded`, idempotently |
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
fallback chain **if a further fallback exists**, consuming no retry. Quality = `task_failed`
or any acceptance failure → consume one retry **if another quality attempt is admissible**,
same performer, clean base. When neither is available the failure terminalizes the movement
without charging past its cap. `protocol_error` triggers **neither** in
v0.2.

**Quality-failure reasons beyond acceptance:** `draft_no_blocking_output` (§2 draft
contract) joins `task_failed` as a quality failure the core itself determines.

**`acceptance.failed` reasons:** `criterion_failed`, `criterion_errored`,
`acceptance_mutated_workspace`, `artifact_missing`, `artifact_kind_mismatch`,
`artifact_hash_mismatch`, `findings_malformed`, `findings_subject_mismatch`,
`findings_rubric_incomplete`, `recovery_subject_mismatch`.

**`movement.failed` reasons:** `retries_exhausted`, `fallbacks_exhausted`,
`budget_exhausted`, `human_gate_rejected`, `grant_denied`, `protocol_error`,
`composition_unresolvable`. `protocol_error` and `grant_denied` need their own terminal path
because they trigger neither retry nor fallback (§3) — without it the movement would have no
way to end.

**`run.failed` reasons:** `movement_failed`, `budget_exhausted`, `composition_unresolvable`,
`recovery_halted`.

**Core-determined attempt failure kinds** — failures the core attributes to itself rather than to
a vendor, alongside the wire kinds above: `budget_exhausted` (§6 mid-flight exhaustion).

**`execution.stopped` reasons** (§6): `normal`, `cancelled`, `superseded`, `budget_exhausted`,
`recovered`.

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

**Output kinds** (§2) — **not a closed enum**, and the name is `output`, not `artifact`,
because one of the reserved kinds is deliberately never an artifact:

| Kind | Class |
|---|---|
| `findings` | reserved **artifact** output kind — the core validates its schema and subject binding (§7) |
| `change_set` | reserved **core-synthesized** output kind — never an artifact, never emitted by a performer (§5) |
| `document` | conventional; the core gives it no special handling |
| any other non-empty string | an ordinary artifact kind |

The protocol carries `kind` as a free string, so closing this set would break scores that name
domain-specific kinds for no benefit.

**Protocol error codes** (§4) — exactly the implemented set: `-32700` parse error, `-32600`
invalid request, `-32601` method not found, `-32602` invalid params, `-32603` internal error,
`-32000` execute already in progress, `-32001` frame too large (answer with `id: null`, skip
the frame, continue). There is **no** code for a duplicate artifact: a duplicate or undeclared
artifact notification is rejected before any append and fails the attempt as
`protocol_error` with sub-reason `duplicate_artifact_instance`. Duplicate ids *inside*
`partitur-result.json` are an invalid envelope, hence `task_failed` (§4).

**`protocol_error` sub-reasons** — *provisional until the follow-up PR completes structured
protocol-error handling:* `duplicate_artifact_instance`, `undeclared_artifact`,
`artifact_path_escape`, `change_set_emitted_as_artifact`, `proposal_without_authority`,
`partial_frame_eof`, `strict_decode_failed`, `frame_too_large`, `event_limit_exceeded`.

**Findings coverage conclusions** (§7): `examined_none_found`, `findings_raised`.

**Impact operations** (§9): `add`, `remove`, `replace`.

**Amendment approval modes** (§9): `auto`, `human`.

**Attempt selection reasons** (Appendix B, `performer.selected`): `initial`, `quality_retry`,
`fallback`, `revision_restart`, `decision_resume`. The last covers the case a purely
retry/fallback vocabulary cannot express: a `BLOCKED` attempt is terminal, and answering its
questions authorizes a **fresh** attempt without changing the score revision — so it is neither
a quality retry, nor a fallback, nor a revision restart.

**Execution phases** (`execution.started`): `adapter`, `acceptance`, `composition`.

**Charging modes** (`execution.stopped`): `measured`, `clamped`.

**Decision dispositions** (`decision.resolved`): `answered` (questions), `approved`,
`rejected` (gates). Amendment decisions never use `decision.resolved` at all (§9).

**Recovery outcomes** (`apply.recovery_resolved`): `rolled_back`.

**Composition scopes** (`composition.conflicted`): `movement`, `candidate`.

**Score status** (§2): `draft`, `finalized`.

**Amendment auto modes** (§2): `off`, `envelope`.

**Recovery halts** — conditions that stop a run rather than repairing it:
`journal_idempotency_conflict`, `unsupported_run_format`, `missing_artifact_file`,
`missing_snapshot_file`, `missing_changeset_ref`, `journal_corrupt`.
