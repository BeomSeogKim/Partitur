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
| Adapter protocol (`protocol:`) | `2` | the sole accepted version. Tagged `resolved_decisions` is the baseline form. `probe.features` remains an open extension point because feature negotiation cannot later be introduced by a field that itself needs negotiating; no tokens are defined in v0.2, and absent or empty means none. Enforcement booleans remain fail-closed because absence decodes `false` |
| Adapter result envelope | `1` | unchanged (adapter-internal) |

**A fourth spike has run: process-identity handoff.** Three questions resisted prose because the
answer is whatever the OS actually permits, and all three **changed the document** rather than
confirming it:

- **Quiesce handshake** (§6) — an acknowledgement cannot be an append, because the approver holds the
  state lock. It is a **lease move** bound to a new durable `amendment.approval_prepared`. No second
  fencing event is needed, so §9's single-transition rule survives intact.
- **Spawn window** (§4) — neither spawn-first nor append-first is recoverable. A **gated
  session-leader trampoline** records identity before any adapter code executes, so an unrecorded
  *trampoline* may survive a crash but an unrecorded **mutator** cannot.
- **Acceptance subprocess** (§7) — `criterion.started` needed the same identity, and recovery must
  sweep before synthesizing `ERROR`, or it would verify a worktree an orphan is still mutating. A
  synthesized completion omits `duration_ms` rather than fabricating one.

One measurement is worth keeping in view while implementing: on Linux, start ticks are coarse enough
that 500 rapidly-spawned children yielded only **10 distinct start identities** — 490 of the 500
observations repeated an identity already seen. Start identity alone does not distinguish processes;
PID must remain in the tuple, exactly as §6 has it.

**Three earlier rules were gated on a bounded implementation spike.** All three have run against real
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

**Implementation readiness.** All four spikes have run and their findings are folded in, so no surface
of this document is marked non-normative. A.4/A.5 give the canonical-AST identity domains and the
execution-dependency projection; B.0–B.7 give each authoritative event's payload; Appendix C defines
surface-indexed recovery coverage for `resume` and indexes the two §8 shipping-recovery surfaces;
Appendix E freezes the ordered-step boundaries that fault injection is performed against.

**Normative does not mean proved.** Each residual risk now has a forcing function other than another
prose pass, and they are at different stages:

| Risk | Forcing function | Status |
|---|---|---|
| JSON canonicalization (A.1) | RFC 8785 vectors and the published ES6 first-1,000 checksum, both carried by CI | **proved against those published expectations; the full 100-million-record corpus stays open (above).** `internal/canonical` checks Appendix B's numbers against their published strings, and regenerates the upstream first 1,000 ES6 records by upstream's own networkless method — its published static prefix, then the published serial continuation from `0x0010000000000000` — serializes them with the production encoder, and matches upstream's published SHA-256. The expectation is external in both cases, so a systematically wrong encoder fails rather than pinning its own output |
| Domain-separated `H()` construction (A.2) | its own separation tests | proved for the substrate; **each A.4 projector is proved only when it exists** |
| Process primitives — trampoline gate, session sweep, start identity | SPIKE-4 measurement | measured |
| The prepare / ACK / quiesce protocol built on them (§6) | replay and fault-injection tests (Appendix E) | **not proved** — designed against the measurements, not measured itself |
| Recovery — pending-prepare, cancellation precedence, live criterion sweeping | replay and fault-injection tests (Appendix E) | **not proved** |
| Forced PID reuse, Intel macOS, power-loss durability | targeted testing | open |
| Safety-policy choices, including the factory cast's enforcement posture (§3) | specification review | reviewed, and review is the only instrument they admit |

Two things the table deliberately does not say. It does not claim every residual risk has a
*non-review* forcing function — safety-policy choices are value judgements and review is the
appropriate instrument, not a fallback. And it does not claim the contracts are proved sufficient to
implement against; it claims they are stated exactly enough to *attempt* an implementation, which is
what will produce the next round of corrections.

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
                               #   (run state and staging; neither is ever committed)
    cast.yaml                  # project cast override (committed or ignored, user's choice)

    runs/.state.lock           # persistent repository-scoped state-mutation lock (§6)
    runs/<run-id>/             # AUTHORITATIVE — core-written only, never agent-writable
      journal.jsonl            # append-only event log (single writer: core)
      manifest.yaml            # rebuildable projection: score revision + hash, resolved
                               # cast pins, per-attempt enforcement record, artifact index
      scores/revision-<n>.yaml # immutable score snapshots (see below)
      prepares/<prepare-id>.json
                               # the complete planned amendment.approved payload, written and
                               #   fsynced BEFORE the prepare event (§6). Recovery replays this
                               #   plan rather than recomputing one — an auto approval has no
                               #   proposal record to rebuild from
      proposals/<proposal-id>.json
                               # immutable routed-proposal record, schema
                               #   partitur/proposal-record+json;v=1 (§9). Decision-time
                               #   re-validation replays the pipeline from the ORIGINAL
                               #   operations, which a typed delta cannot reconstruct — the
                               #   delta is lossy by design
      quarantine/<kind>/<content-sha256>/<source-basename>
                               # durable destination for quarantined run files
      resolved-cast.yaml       # the fully resolved cast used by this run
      artifacts/<logical-output-id>/<attempt-id>
                               # immutable artifact instances (identity and atomicity
                               # below); a retry never overwrites earlier evidence
      session/                 # session hints, mode 0600 (see §4 privacy)
      driver.lease             # execution-driver lease (§6); absent when no driver runs
      authority.json           # execution-authority checkpoint: current **epoch only** (§6).
                               #   A PROJECTION of authority.granted / run.* events, like the
                               #   manifest — rebuildable, never the authority itself. It does
                               #   NOT hold the token: the token lives solely in driver.lease
                               #   and its owner's memory, because a value that proves
                               #   incarnation identity must not sit in a rebuildable file
      attempts/<attempt-id>/
        stderr                 # sanitized vendor/adapter diagnostics (§4 privacy)
        trace.jsonl            # protocol trace

    work/<run-id>/<attempt-id>/   # NON-AUTHORITATIVE staging — agent-writable
      output/                  # the attempt's output_dir (§5); artifacts are copied out
                               # of here into runs/.../artifacts/ and only the copy counts

~/.config/partitur/cast.yaml   # user-global cast override
```

Git refs the core owns — never user-visible branches, and never garbage-collected while
the run exists:

```text
refs/partitur/runs/<run-id>/base                              # the run's base commit (§5)
refs/partitur/runs/<run-id>/attempts/<attempt-id>/changeset   # storage handle (§5)
refs/partitur/runs/<run-id>/movements/<movement-id>/base      # a fan-in composed base (§5)
refs/partitur/runs/<run-id>/candidate                         # the candidate result tree (§8)
```

**Everything the run will need later is pinned, not just change sets.** A tree or commit reachable
from nothing is Git-GC-eligible, and the run needs its base to resume, its composed movement bases
to re-run a movement, and its candidate result tree for final verification and `apply` — potentially
long after the run ended. Pinning only per-attempt change sets would let a `git gc` between a run
and its `apply` make the candidate unrecoverable. Composed bases and the candidate tree are pinned
as commits wrapping the tree, since a ref must point at a commit or tag to survive ordinary
reachability rules.

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
  of them. Collision freedom is load-bearing namespace separation: a `run_id` names the
  authoritative run directory, writable staging root, owned Git refs, and every journal envelope,
  so a collision would alias two runs' state rather than merely label them alike. UUIDv7's time
  ordering is an allocation property only — no projection, command selection, or recovery rule
  sorts or chooses a run by decoding it. Those decisions use journal sequence and lifecycle state.
  The id is allocated before any per-run paths are populated, but `run.started`'s fsynced envelope
  is the first authoritative record that the run exists; a pre-event directory is orphan state,
  not a run.
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

**Rename durability.** Every temp → rename transaction in this document — artifacts, score
snapshots, proposal records, `resolved-cast.yaml`, the promoted root score — **fsyncs the containing
directory** after the rename and before the authorizing journal append. POSIX does not make a rename
durable when only the file was fsynced, so without this step a crash can leave the event referring to
a name that does not exist, which the recovery rules would report as corruption.

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
- **The truncation record itself can be lost**, and v0.2 accepts that: a crash after truncating but
  before appending `journal.tail_truncated` leaves no evidence the truncation happened. This is a
  narrow *audit* loss with no correctness consequence — the discarded event was never observed, so
  nothing downstream depends on knowing it existed. Closing it would need a durable intent file
  written before the truncation, which is more machinery than a lost audit line justifies. Stated
  rather than left as an assumed guarantee.

**Artifact instances.** Score-declared output ids are *logical* ids. Each emission is a
distinct immutable instance identified by `(logical_output_id, attempt_id)`. Because that pair is
referenced from payload arrays, proposal evidence, and finding overrides, it needs a single scalar
form, so the `artifact_instance_id` is the **reversible string** `<logical_output_id>@<attempt_id>`.
Both components are constrained by the identifier grammar above — the slug excludes `@`, and a
UUIDv7 contains none — so the split point is unambiguous and no escaping is required. Stored at
`artifacts/<logical-output-id>/<attempt-id>`, both safe path segments by that same grammar. A logical output may be
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

**Resolved-cast persistence.** `resolved-cast.yaml` is authoritative *input*: `run.started` records
only its hash, and the layering that produced it (project → user-global → factory) can change
underneath, so the hash alone cannot reconstruct the bindings and fallbacks a not-yet-attempted
movement will need. It is written before `run.started` under the same discipline as snapshots —
temp → fsync → atomic rename → parent-directory fsync — and a `run.started` whose
`resolved-cast.yaml` is missing or hash-mismatched is a recovery halt (`missing_resolved_cast`). An
orphan file with no `run.started` is quarantined.

**Routed-proposal records.** A proposal routed to a human must survive a crash, because
decision-time re-validation replays the pipeline from the original operations (§9). It follows the
same discipline as artifacts and snapshots, for the same reason — the file must exist before the
event that makes it authoritative:

- Write to a temporary file → compute `sha256` and durably flush → atomic rename into
  `proposals/<proposal-id>.json` → append `amendment.routed_human` (fsynced) carrying
  `proposal_record_hash`.
- On recovery, a proposal file with **no** `amendment.routed_human` is quarantined: nothing ever
  referred to it. An `amendment.routed_human` whose file is **missing**, or whose bytes do not hash
  to the recorded value, is a recovery halt (`missing_proposal_record`) — the run cannot honour a
  pending human decision it can no longer re-validate.
- `amendment.routed_human` and its `decision.requested` are separate appends. On recovery, a
  `routed_human` with no matching `decision.requested` appends it idempotently, keyed on
  `decision_id`; `routed_human` is the source authority, so the decision is fully determined by it.
- Records are immutable and retained with the run, like snapshots. A decided proposal's record stays
  — the audit trail is the point.

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
  snapshot, the core **refuses to resume and halts** with `root_snapshot_divergence` (Appendix D) —
  it does not prompt, because §7 makes every command non-interactive. The operator's paths out are
  explicit: restore the root score to match, or cancel the run and start a new one. "Asks the user"
  was not a contract; a halt with a named reason is.
- The manifest records the source revision and both hashes.

**Cast layering.** Precedence is **project → user-global → factory default**. In v0.2 the core
discovers only `.partitur/cast.yaml` and `~/.config/partitur/cast.yaml`; either file may be absent.
The factory-default slot remains the lowest-precedence layer, but v0.2 ships and discovers no
factory file. Shipping one requires a separate specification change that defines both its location
and its content under §3's strict/advisory posture. Present layers use deliberately simple merge
rules (no deep merge):

- `performers` entries replace **whole objects** per performer id.
- `bindings` entries replace whole objects per part id; `fallbacks` lists are replaced
  wholesale, never concatenated.
- Removal/tombstoning is not supported in v0.2.
- Adapters are resolved on demand for the adapter ids the resolved cast references —
  the core never scans `PATH` for everything that looks like an adapter.

The run manifest pins the resolved performer, adapter id, and model for every part — the
*intended* binding. Adapter version is an observed fact, not cast input, so it is recorded only
after the gated adapter for an attempt answers `probe`. Because an infrastructure fallback changes
who actually served an attempt, the manifest additionally records, **per attempt**, that observed
adapter version, the model used, and the capability and enforcement posture in effect, including
which constraints were advisory (§3, §4). Marks, execution-dependency hashes, and audit output
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
  hoc even when an `allowed_paths` glob would admit them. A violation fails the attempt with
  `kind: grant_denied` (`protected_path_violation`), which §3.1 classes immediately terminal.
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

  §3.1's first arm classifies it as any other quality failure, and its second arm realizes that
  classification.
- **Ordinary acceptance can never make the interview movement succeed.** Only the finalization
  amendment projects it to `SUCCEEDED`.

A patch touching anything besides `/status` is not a finalization amendment and is rejected as
an ordinary proposal would be.

**Who constructs it, and when.** The core constructs and routes the finalization amendment when the
draft movement's latest attempt is terminal **and** every open question is resolved or waived and
`verification.expectation` is complete — checked by `run`/`resume` under the state lock, so it has an
owner and a recovery point rather than depending on whoever happened to notice. If a crash lands
between constructing the record and appending `amendment.routed_human`, the orphan record is
quarantined (§1) and the next `resume` constructs it again.

**A finalized score still contains its draft movement**, since finalization changes only `/status`.
A *new* run started from that score therefore has an interview movement and no finalization event.
**It is not instantiated at all: the movement is projected `INAPPLICABLE`** — a projection value with
no attempt, no evidence, and no participation in scheduling.

Two rationales I considered and rejected. Projecting `SUCCEEDED` would manufacture a success nothing
proved, and a directly hand-authored `finalized` score is not evidence that any interview ever ran.
And the deadlock I first offered as justification does not exist: §2 rule 12 excludes draft movements
from the final movement's dependency closure, so leaving one uninstantiated blocks nothing. The
correct reason is simpler — a draft movement is meaningful only while `status: draft`, and outside
that phase there is nothing for it to do.

**Routed proposal record.** When a proposal is routed to a human its exact submission is persisted,
because re-validation replays the pipeline from the original operations (§1):

```text
{
  schema: "partitur/proposal-record+json;v=1",
  proposal_id,
  origin,                          # adapter | cli | core_finalization
  attempt_id?,                      # present iff origin = adapter
  emitted_id?,                      # present iff origin = adapter
  base_revision, base_hash,
  operations,                       # the RFC 6902 array VERBATIM, as submitted
  reason,
  evidence?: [artifact_instance_id],
  claimed_impact,
  requires_decision
}
```

Strictly decoded like every other core file: unknown fields, duplicate keys, and invalid UTF-8 are
rejected, and the bytes are UTF-8 JSON — not canonical JSON, since `operations` must survive
verbatim rather than being re-encoded.

**A rejection before step 4 records no operations hash.** §9 says an early rejection records the
`partitur/patch-operations` hash, but that hash is only constructible once the operations are known
to be a JCS-encodable RFC 6902 array. A `patch_error` rejection caused by operations that are *not*
that — a malformed array, an unencodable value — therefore records the raw-byte `sha256` of the
submitted operations instead, and says which form it used. Promising a canonical hash of something
that cannot be canonically encoded would be unimplementable.

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

**Rules enforced by `partitur validate` (the score compiler).** All active rules are checked and
reported together; validation is not short-circuited at the first error. Rule numbers are stable
contract identifiers: a retired number remains as a tombstone and is never reused or silently
assigned to a later rule.

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
5. **Retired — artifact-path containment is a runtime ingest invariant, not a score rule.**
   The score declares logical output ids and kinds, but no artifact path, so the compiler has no
   source pointer on which to enforce this check. The adapter rejects an absolute path, a `..`
   segment, a post-symlink escape from `output_dir`, or a non-regular file when it validates the
   result envelope (§4). The core independently repeats the same post-symlink containment and
   regular-file validation while recording the artifact (§1); the adapter-side check cannot
   discharge the core-side one because the adapter is across the trust boundary. Rule number 5 is
   not reused.
6. Unknown fields in the core namespace are an error; adapter-specific data lives only
   under `extensions.<adapter-id>`.
7. An `acceptance.review` entry must reference a `findings`-kind output of the same
   movement.
8. **At most one** movement carries `phase: draft`, and if one exists it is the one
   `draft.interview_movement` names — and conversely. A `status: draft` score must have one; a
   finalized score retains whichever it had, since finalization patches only `/status`. A draft movement may not hold `repo_write`.
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
13. Every numeric value at a **schema-controlled path** is an integer in the canonical safe range
    (Appendix A). Numeric values below `extensions.<adapter-id>` are opaque and instead follow
    A.1's full finite-binary64 ingress rule; applying the schema range to them would collapse the
    deliberate split between core-authored schema and user-authored adapter-namespaced payloads.
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
17. **At most one declared `artifact` criterion per ordinary output** within a movement. Two
    would make "which criterion replaces the generated check" ambiguous (§7), and the effective
    acceptance plan must be a function of the score, not of resolution order.
18. An `artifact` criterion may not reference a `kind: change_set` output. Change sets are
    core-synthesized and satisfied by `change_set.recorded` (§5), so such a criterion could
    never pass.
19. `may_propose` (below) defaults to `false` and is permitted on **any** movement. The
    draft interview movement has it implicitly. The effective value — including that
    implicit default — is materialized in the canonical AST, so it is visible to hashing.
    An explicit `may_propose: false` on the draft interview movement is a compiler error:
    the field cannot withdraw the authority that movement's purpose requires, and silently
    ignoring it would make the projected effective value disagree with the source.

**Defaults, optionality, and ranges.** The canonical AST is built *after defaults are applied*
(A.1), so two implementations cannot agree on an identity unless they agree on the defaults. The
example above is illustrative; this table is normative:

| Field | Required? | Default when omitted | Range / constraint |
|---|---|---|---|
| `score`, `name`, `goal` | required | — | `score` is exactly `"0.2"` |
| `revision` | required | — | integer ≥ 1 |
| `status` | required | — | either `draft` or `finalized` |
| `context` | optional | **absent** — omitted from the canonical projection, from `brief`, and from A.5 alike, never sent as `""` | — |
| `draft.interview_movement` | required iff a `phase: draft` movement exists | — | must name it. Finalization changes only `/status`, so a finalized score **retains** both the field and the movement (rule 8 applies in either status) |
| `open_questions` | optional | `[]` | ids unique |
| `verification.expectation.intent` | required iff `status: finalized` | — | closed enum. A draft may omit the whole `verification` block: discovering it is the interview's job (rule 1) |
| `apply_gate.require` / `.waived` | exactly one, iff `status: finalized` | — | `require` non-empty, duplicate-free |
| `apply_gate.predicates` | optional | `[]` | closed enum |
| `verification.final_movement` | when finalized: required iff not waived, forbidden if waived | — | §2 rule 12 |
| `parts.<id>.capabilities` | required | — | non-empty, duplicate-free |
| `parts.<id>.read_only` | optional | `false` | — |
| `movements[].needs` | optional | `[]` | DAG; ids must exist |
| `movements[].grants` | optional | `[]` | ⊆ part capabilities |
| `movements[].inputs` / `.outputs` | optional | `[]` | §2 rules 4, 15 |
| `movements[].phase` | optional | **absent** (non-draft) | only `draft` |
| `movements[].may_propose` | optional | `false`; `true` for the draft interview movement | — |
| `movements[].acceptance` | optional | `{hard: [], review: [], human_gate: "never"}` | `human_gate` defaults to **`never`** — the permissive-looking default is safe because §2 rule 2 independently forces a write movement to declare a hard criterion or `always` |
| `acceptance.hard[].timeout_min` | optional | **absent** — the effective timeout is then the remaining budget alone (§7) | integer ≥ 1 |
| `acceptance.hard[].expected_hash` | optional | absent | `sha256:<hex>` |
| `policy.allowed_paths` | optional | `[]` | no duplicates; empty means no path authority (A.5) |
| `policy.side_effects` | optional | `[]` | must be `[]` in v0.2 |
| `policy.budget.active_wall_clock_min` | required | — | integer ≥ 1 |
| `policy.budget.retries_per_movement` | optional | `0` | integer ≥ 0 |
| `policy.amendment.auto` | optional | `"off"` | either `off` or `envelope` |

**No field is nullable.** An absent optional field and an explicitly-`null` one are not the same:
`null` is a type error, and an omitted field takes the default above. Where the default is "absent",
the field does not appear in the projection at all rather than appearing as `null` (A.1).

For the cast: `performers.<id>.adapter` and `.model` are required; `allow_advisory_enforcement`
defaults to `false`; `extensions` defaults to absent; `bindings.<part>.performer` is required and
`.fallbacks` defaults to `[]`. Every bound performer must exist, every part must have a binding, and
a fallback chain must be duplicate-free and must not contain its own primary.

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

**A non-blocking proposal is advisory and expires.** Because a `may_propose` attempt's dependency
includes the whole score (A.5), the moment that movement succeeds its own proposal becomes
unapprovable — approving it would change a succeeded movement's dependency hash (§9). That is safe,
but it is worth stating rather than leaving as a surprise: a non-blocking proposal is useful only
while its movement is still running, and beyond that window it is a suggestion for the *next* run. A
performer that needs its proposal to land must mark it `requires_decision: true` and block on it.

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

> **This example is illustrative, not runnable.** Under the fail-closed predicate of §4 it does not
> pass `partitur validate`, for the reasons the paragraphs below record. A runnable cast must either set
> `allow_advisory_enforcement: true` on the affected performers — accepting that the
> manifest records which constraints were advisory per attempt — or bind write movements to
> a performer whose enforcement covers the movement's grants.

**Requirements for a future factory cast.** Enforcement is reported by the adapter's `probe` and
recorded per attempt (§1, §4); a factory cast must neither supply nor override it. Within enforcement
posture, any factory cast that ships decides only where it opts into advisory execution:

> A factory cast MUST NOT duplicate or override adapter enforcement reports. It may opt into advisory
> execution only through dedicated performer entries, **never globally**. Parts intended to remain
> strict MUST use distinct strict entries for both their primary performer and **every fallback**,
> even where the adapter and model are identical.

Opting in globally would make advisory execution habitual — the failure that would leave all five
dimensions implemented and meaningless.

The binding granularity is what forces the last clause: `bindings.<part>` selects a primary performer
and fallback chain for a **part**, while `grants` are declared per **movement**, and one part may play
several movements with different grants. An advisory-enabled entry can therefore allow an unmet
enforcement dimension to proceed in any movement in which that entry is selected, including a
movement holding no `repo_write`; the exact advisory dimensions remain movement- and attempt-specific
under §4. Distinct entries are necessary but not sufficient: a part remains strict only when its
primary and every fallback are strict entries.

Whether a started adapter session is denied before `execute` or proceeds with recorded advisory
dimensions is governed solely by §4's per-movement predicate. Both outcomes are legitimate; a
future factory cast must not authorize the second through a global opt-in.

*Non-normative observation, 2026-07-26.* Of the first-party adapters, `codex` reports only
`read_only` and `network_grants` as `true`, and `claude` reports none of the five. That is why the
rule above constrains any factory cast that later ships rather than describing one that exists; the
observation is not a claim this document freezes, and the probe governs.

- `partitur validate` checks every bound performer's probed capabilities against the
  part's `capabilities`, and the adapter's enforcement against the movement's grants
  (see §4, trust boundary).
- `allow_advisory_enforcement` defaults to `false`, applies per performer (including
  independently to each fallback performer), and when `true` the manifest records
  individually which constraints were advisory for which attempt.

**Retry and fallback semantics.** `retries_per_movement` is the movement's **quality-retry budget**,
a per-movement total shared across the fallback chain — a fallback performer does not receive a fresh
one. Infrastructure failures advance the fallback chain instead of consuming it.

### 3.1 The successor oracle

Normative, and the **single** statement of how a failure selects what happens next. Appendix C
references this rule and restates no part of it; a second copy would be a second chance to disagree.

It has **two arms, and merging them is the defect it exists to prevent**: one runs before the failure
event is recorded and may read the budget, the other runs after it is durable and may not.

**Arm 1 — classify, before the failure event is appended.**

The classified input is the **failure case**: an `attempt.failed`'s `kind`, or an
`acceptance.failed` — which carries a `reason` and no kind, and is a failure case in its own right.
The remaining inputs are all projected from the journal: whether an unvisited fallback remains,
`retries_consumed` against `retries_per_movement`, and `remaining_time`.

Appendix D partitions those failure cases into three classes and owns that membership; a case
belonging to none is a defect there, not one for this rule to guess at. What each class selects:

| Class (Appendix D) | `charged` | `terminal_reason` when `none` |
|---|---|---|
| Infrastructure | `fallback` if an unvisited fallback remains **and** `remaining_time > 0`; otherwise `none` | `budget_exhausted` if `remaining_time == 0`, else `fallbacks_exhausted` |
| Quality | `quality_retry` if `retries_consumed < retries_per_movement` **and** `remaining_time > 0`; otherwise `none` | `budget_exhausted` if `remaining_time == 0`, else `retries_exhausted` |
| Immediately terminal | always `none` | that kind's own `movement.failed` reason |

**The immediately-terminal class never consults the budget**, so a zero budget cannot overwrite its
reason. A `grant_denied` that coincides with an exhausted budget stays `grant_denied`: it is the more
specific cause, it is true independently of the budget, and reporting `budget_exhausted` there would
hide a policy violation behind an accounting one.

`remaining_time == 0` therefore starts no new attempt of any kind, while `remaining_retries == 0`
forbids only new *quality-retry* attempts. `protocol_error` triggers neither
retry nor fallback in v0.2 (uniform rule; a cast-level opt-in may come later), and quality failures
never trigger fallback: a different model is not the fix for a failed test; amendments and humans are.

The result is written into the failure event's `disposition` (B.0) **atomically with the failure**. A
failure charges only when it authorizes another attempt — otherwise `retries_consumed` could exceed
`retries_per_movement` and the projection would contradict its own bound.

**Arm 2 — realize, after the failure event is durable.**

Input: the recorded failure and its recorded `disposition`. This arm reads **no budget and no
admissibility state** — Arm 1 already consulted both, and consulting them here is exactly the
recomputation Appendix C forbids. Resolving *which* performer a recorded decision names is not that:
reading the durable cast projection to find the same performer or the next unvisited fallback is
deterministic target resolution, not a fresh judgement.

| Recovery case | `disposition.charged` | Action |
|---|---|---|
| `RC-DISPOSITION-001` | `quality_retry` | one new attempt with the **same** performer |
| `RC-DISPOSITION-002` | `fallback` | one new attempt with the immediate next unvisited fallback; the chain never revisits an earlier performer |
| `RC-DISPOSITION-003` | `none` | `movement.failed` carrying the recorded `terminal_reason` verbatim |

Every retry and fallback starts from the same clean base and the same input artifact instances.
For `quality_retry` and `fallback`, “one new attempt” first establishes a pending successor whose
performer and reason are fixed by this arm; the between-unit scheduler makes that choice durable as
`performer.selected` and only then may launch it. Recovery selecting the pending successor does not
itself append `performer.selected` or launch an adapter. The recorded failure remains the authority
until that durable selection exists, so a repeated recovery selects the same successor rather than
charging or deciding again. For `none`, the durable realization is the `movement.failed` named in
the table.

**Outside the oracle.** These paths never reach it, because none of them is an attempt failure:
blocking review findings set `review_outcome` and open the human gate (§7, §8); a rejected human gate
terminalizes through `movement.failed {human_gate_rejected}` directly (B.1); user cancellation
terminalizes the run (§6); **composition conflicts and composition execution failures run between
attempts and terminalize their own scope (§5)**; and revision-triggered restarts (§9) and decision
resumes (§4) are not failures at all.

Restarts and resumes are limited **only by the remaining active wall-clock budget**, and a decision
resume additionally preserves the blocked attempt's performer and its position in the fallback
chain — answering a question is not evidence that the performer was wrong. So attempts per movement
have no closed-form bound:

```text
total attempts = initial
               + quality retries consumed          (≤ retries_per_movement)
               + infrastructure fallbacks consumed (≤ fallback_count)
               + revision-triggered superseded restarts   (unbounded by retry policy)
               + decision resumes                        (unbounded by retry policy)
```

## 4. Adapter protocol v2

**Packaging.** An adapter is an executable found on `PATH` under the exact name
`partitur-adapter-<id>`. v0.2 has no core-side adapter-path override. An adapter is explicitly
enabled only when the resolved cast references its id; the core resolves those exact names on
demand and never scans `PATH` for candidate adapters. v0.2 supports macOS and Linux; Windows is
out of scope.

**Adapter process environment.** For validation and execution, the core takes one snapshot of the
environment inherited by the Partitur process, resolves the exact adapter executable using `PATH`
from that snapshot, and passes the same snapshot unchanged to the adapter. The core does not add,
remove, or overwrite variables for adapter launch — including `PATH`, home or configuration
variables, credential variables, or values derived from the score, cast, run, movement, or attempt —
and it neither records nor renders the inherited environment. The composition-subprocess allowlist
(§5) and criterion-runner allowlist (§7) do not apply to adapters.

For an execution launch, the trusted trampoline receives all Partitur launch-control values —
including the already-resolved adapter path, handoff location, `launch_id`, and nonce — through its
argv, and receives the gate as an inherited file descriptor, never through the environment. It
consumes and closes the gate before `exec`, replaces its argv with the adapter's protocol argv, and
`exec`s the already-resolved adapter in place with the unchanged environment. The marker descriptor
survives that `exec` only to hold the lifetime lock required below. Thus the adapter does not receive
launch-control parameters through either its environment or its argv; its request data travels only
through the protocol.

This inheritance is deliberate: an adapter is an operator-enabled trusted executable running with
the user's privileges and must retain vendor-specific authentication and configuration that a
vendor-neutral core cannot enumerate. Filtering the environment would break legitimate vendor
control-plane access without creating containment, because malicious adapter behaviour is already
outside the core's security boundary.

**Process model and framing.** Execution uses one adapter process **per attempt**; the core calls
`probe` and then `execute` on that same process. Validation uses one standalone process per distinct
adapter as specified below. Transport is JSON-RPC 2.0 messages
as **UTF-8 JSON Lines**, one per line, directional: **requests travel on the adapter's stdin;
responses and event notifications travel on its stdout.** Newlines inside values are escaped;
control frames have a fixed size cap — large content travels as artifact files, never inline.
`stderr` is diagnostics only: execution captures it to the attempt directory, while validation
keeps only the bounded sanitized diagnostic defined below. The adapter must keep reading stdin
during `execute` so a `cancel` request can be received; after a grace timeout the core terminates
the process, and force-kills after a further timeout. No daemon in v0.2.

**Frozen wire rules.** These are normative for both sides, and "frozen" means "the contract
will not change" — **not** "every line already exists". Implementation status:

- Every adapter-side rule below is implemented and conformance-tested through the adapter kit and
  first-party adapters, including duplicate-key and invalid-UTF-8 rejection, `shell_grants` /
  `read_grants` reporting, and pre-persistence stderr sanitization.
- The core-side client implements discovery, `probe`, and `execute` through `Client.Resolve`,
  `Client.ProbeAll`, and `Client.Execute`, with `HaltError` for fail-closed conditions. Its process
  supervision covers session leadership, process-group sweeps, bounded sanitized stderr, and one
  deadline spanning response receipt through clean process exit.

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
- **Duplicate JSON keys are rejected** at every nesting depth. Go's `encoding/json` silently keeps
  the last value for a duplicate key, so this needs an explicit walk; the result-envelope parser
  shares it, so the two paths cannot disagree. Duplicate-key tolerance is a parser-differential
  hazard.
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
  protocol: 2,
  adapter: { id, version },
  capabilities: {
    repo_read, repo_write, shell, network: bool,
    resumable_sessions: bool,
    models: [ { id, aliases? } ]
  },
  features: [string],           # protocol 2. An OPEN list of feature tokens, not a closed enum:
                                #   a closed enum would need a bump for every future token, which
                                #   is the problem negotiation exists to solve. A core ignores
                                #   tokens it does not know; an adapter advertises only what it
                                #   implements. No tokens are defined in v0.2; absent or empty
                                #   means none.
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
      context?,                 # omitted entirely when the score omits it (§2 defaults) —
                                #   never sent as ""

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
    inputs: [ { artifact_id, kind, instance_id, path, hash } ],
                                #   `instance_id` is delivered, not just hashed: A.5 binds it, so a
                                #   performer and the core must agree on which instance this is
    feedback: [                 # diagnostics from prior failed attempts (see §7). Delivered like
      { previous_attempt_id,    #   any other input — a readable path and a hash — because a bare
        kind,                   #   artifact id is not something a performer can open
        artifact_instance_id,
        path,                   # read-only, inside the attempt's readable area
        hash }                  # sha256: raw bytes, so tampering is detectable
    ],                          # read-only; never applied to the base
    resolved_decisions: [       # tagged union; every entry carries `kind`
      { decision_id, kind, ... } # see the blocking handshake below for the closed variants
    ],
    workdir,                    # the attempt worktree (see §5)
    output_dir,                 # always-writable artifact area (see §5)
    grants: { paths_rw, paths_ro, shell, network },
    budget: { remaining_ms },    # remaining at attempt start, INTEGER MILLISECONDS. Not
                                #   `active_wall_clock_min`: the score declares the cap in
                                #   minutes but the remainder is tracked and compared in ms
                                #   (§6), and a minutes-only field could not carry it losslessly
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

**Validation probing.** `partitur validate` probes each distinct adapter id referenced by a
primary or fallback performer of a bound part exactly once, regardless of how many performers use
that adapter. Every referenced adapter is attempted even after another adapter fails, and probe
diagnostics are aggregated rather than short-circuited.

The probe-completion deadline is **15000 integer milliseconds per distinct adapter**, covering
process start, request write, response read, stdin close, and the adapter's clean exit. It is a
policy ceiling derived from the first-party clean-probe path: 10000 ms for the vendor-CLI probe
plus 5000 ms reserved for adapter startup, JSON-RPC framing, clean shutdown, and scheduling.
Termination costs are not part of this deadline; they begin only after it expires. Durations are
compared as integer milliseconds, consistent with §6.

A successful probe is not complete at the response frame. The core closes the adapter's stdin,
which is the clean-EOF signal above, and requires the adapter to exit zero within the remaining
probe-completion deadline. A nonzero exit before or after a response is a probe failure. After the
leader exits, the core verifies the recorded session empty; a successful response and zero leader
status do not excuse a surviving descendant. Thus the success and timeout paths converge on the
same verified-empty boundary rather than making process exit a proxy for session cleanup.

Each of the following is an adapter-environment validation diagnostic: the exact executable is
absent from `PATH`; spawn or request/response I/O fails; EOF arrives before a complete response;
the response is malformed, oversized, duplicate-keyed, invalid UTF-8, an error response, names the
wrong adapter id, or reports a protocol version other than 2; the adapter exits nonzero; the
probe-completion deadline expires; or cleanup cannot verify that the adapter session is empty.
When an adapter has no valid probe result, `validate` reports that adapter-level failure and
suppresses derivative capability and enforcement diagnostics for every performer using it:
those predicates have no observed input and manufacturing their outcomes would not be fail-closed.
Other adapters are still probed and their independent diagnostics are still reported.

Model availability is **not** a `validate` predicate in v0.2: the command does not compare a cast's
model with `probe.capabilities.models`, and `model_unavailable` remains an attempt failure. Adding
that check requires a separate rule to settle id-versus-alias matching and whether a mismatch is
fail-closed or advisory; the probe-failure suppression rule above does not create it implicitly.

On timeout the core closes stdin, sends `SIGTERM` to the adapter session, waits the core's outer
termination grace, then repeatedly sends `SIGKILL` and re-enumerates until the session is verified
empty. Probe stderr is diagnostics only: it is buffered to at most 65536 bytes, sanitized before
rendering, and never written to run storage.

The completion deadline and outer grace run serially on the timeout path: the bounded waits before
the first `SIGKILL` total **45000 ms per distinct adapter** (15000 ms completion plus 30000 ms
grace). A serial implementation probing two wedged adapters may spend 90000 ms before both reach
that point; parallel probing may reduce wall-clock time but does not change either per-adapter
bound. The verified-empty sweep after the first `SIGKILL` is additional and has no fixed deadline,
because returning while a session remains observable would falsely claim cleanup.

The core starts a validation probe as a new POSIX session leader and keeps its identity in memory
for the controlled-exit sweep above. It does **not** use the durable execution-launch gate: a probe
has no attempt, receives no `execute` request, worktree, output directory, grants, or run state, and
cannot be recovered from an attempt journal. If the core itself dies, closing its stdin pipe invokes
the frozen EOF contract above: a conforming adapter cancels in-flight work, drains, and exits zero
after clean EOF, or exits nonzero after a partial frame. A non-conforming adapter or a descendant
that deliberately escapes its session remains outside the containment boundary below.

The core creates no run, attempt, journal, manifest, or resolved-cast file during validation and
mutates no repository state. Any score, cast, adapter-environment, capability, or enforcement
diagnostic makes `partitur validate` exit 3; usage errors remain exit 1.

Validation output is deterministic and grouped in dependency order: **score → cast →
adapter-environment → capability → enforcement**. Score and cast diagnostics retain the ordering
their compilers define; adapter-environment diagnostics are ordered by adapter id; capability
results are ordered by part id, then primary followed by fallback order, then missing capability;
and enforcement results are ordered by movement declaration order, then primary followed by
fallback order, then unmet dimension. Diagnostics are rendered to stderr. A validation with no
diagnostic or advisory report writes nothing to either stream; stdout is always empty for
`validate`.

The ordering reflects these suppression boundaries rather than manufacturing derivative facts.
Score diagnostics do not prevent independent cast-layer parsing, but they suppress score-relative
cast checks, adapter selection and probing, capability checks, and enforcement checks. A
cast-resolution failure suppresses all derivative probing, capability, and enforcement output.
A missing binding suppresses only the affected part: adapters reachable through other valid
binding chains are still probed and evaluated. An unprobeable adapter suppresses capability and
enforcement output only for performers using that adapter, as specified above; other adapters and
performers continue. A capability diagnostic does not suppress an independently evaluable
enforcement result.

An unmet enforcement dimension for a performer whose resolved cast entry has
`allow_advisory_enforcement: false` is an enforcement diagnostic and therefore exits 3. For a
performer whose entry has `allow_advisory_enforcement: true`, the same unmet dimension is instead a
non-fatal **advisory report**: it appears on stderr in the enforcement block with the exact unmet
dimension set, and does not change exit 0 when no diagnostic exists. This is the one non-fatal
reporting class in v0.2, required by the fail-closed predicate below; it is not a general severity
axis, and no other finding may use it without a specification change.

**Run-attempt probing.** `partitur run` does not perform or reuse a run-level validation preflight.
Validation is a separate, optional command surface; its observation cannot truthfully describe an
adapter process launched later, and its failure has a CLI contract of its own (§7). Instead, every
attempt observes the peer that will execute it: after `performer.selected` creates the attempt, the
core opens the §6 `adapter` execution interval, launches the attempt's one adapter process through
the durable gate, appends and fsyncs `attempt.started`, releases the gate, and calls `probe` on that
process **before** `execute`.

The run probe uses the validation probe's **15000 ms probe-completion deadline**, response
validation rules, bounded and sanitized stderr handling, probe-failure suppression rule, and
**30000 ms outer termination grace plus verified-empty sweep by reference**; those rules are not
redeclared for execution. For a run probe the completion deadline covers trampoline start, durable
handoff, gate release, request write, and the complete response; for a denied admission it also
covers the clean exit below. The standalone-probe successful shutdown step does not apply to an
`execute` admission: the core keeps this same process open for `execute`. A denied admission has no
next RPC, so it uses that referenced clean-EOF/zero-exit completion rule; on any failure that
requires forced shutdown, the referenced close-stdin, `SIGTERM`, `SIGKILL`, and verified-empty
ladder applies unchanged. The probe deadline is a ceiling, not extra budget: §6's active-wall-clock
exhaustion can terminate the adapter interval sooner.

The suppression rule is the same fact boundary in a different surface: without a valid probe
result the core manufactures no capability, enforcement, feature, or adapter-version observation.
It records an `attempt.failed` instead, with the failure classified under Appendix D, and sends no
`execute`. A valid result must name the selected adapter id and advertise every capability required
by the selected part. The core evaluates capability coverage and the fail-closed/advisory predicate.
Missing required capability is
`attempt.failed {kind: adapter_unavailable, reason: capability_unavailable}` and therefore may
select an infrastructure fallback; strict unmet enforcement follows the existing
`grant_denied {enforcement_unavailable}` path. Only after every pre-execute rule permits execution,
the advisory dimensions and bounded `resolved_decisions` delivery are fixed, and the exact execution
request's A.5 hash is computed, may the core append and fsync `adapter.probed`.

Only a durable `adapter.probed` authorizes the core to send `execute` on that same process. A denied
probe closes and verifies the adapter session under the referenced probe rules, then follows its
separately specified failure path; no execute request is sent. A higher-priority shutdown failure
supersedes that intended failure under Appendix D's classification priority. Recovery therefore
distinguishes a missing durable observation from an observed peer that had already been authorized
for execution, without manufacturing probe facts.

There is therefore no new pre-run diagnostic or exit category: a run probe failure is durable
attempt history, §3.1 decides whether a fallback follows, and an exhausted path reaches the existing
movement/run failure sequence and exit 4 (§7).

**Run execute completion.** A complete `execute` response is a protocol completeness marker, not
yet a durable attempt transition. The core reuses the validation probe's clean-EOF, zero-exit, and
verified-empty completion boundary above **by reference**: after receiving and validating the
response, it closes adapter stdin, requires a zero adapter exit, and verifies the recorded gated
session empty. If forced termination is required, the same §4 outer grace and session sweep apply.
The validation probe's **15000 ms completion deadline does not carry over**: a run already has an
open §6 `adapter` interval, and the remaining run budget plus cancellation or supersession bounds
that interval.

The run-only durable order is fixed:

```text
complete execute response (all event notifications already precede it)
→ validation probe's clean-EOF / zero-exit / verified-empty boundary (above)
→ execution.stopped {reason: normal, charging: measured}
→ the response-derived B.2 transition
```

For `outcome: completed`, that final transition is `performer.completed`; the other outcomes use
the existing one-to-one mapping in B.2. `normal` describes the living opener's ordinary interval
close, not success: an otherwise valid response followed by a nonzero or unobtainable adapter exit
is discarded, the session is swept, the interval is closed `normal`/`measured`, and the attempt
fails as `attempt.failed {kind: adapter_unavailable, disposition}` with no sub-reason, through
§3.1. If the adapter hangs after its response, the interval stays open until the existing
budget-exhaustion, cancellation, or supersession path terminates and sweeps it; none of those paths
appends the provisional response's transition.

The interval remains open through session verification deliberately. Closing it at leader exit
would stop charging a surviving descendant while it could still mutate the worktree; closing it
again after the sweep would double-charge the same interval. Only verified emptiness permits the
single ordinary close. If the sweep itself is unverifiable, the core halts
`sweep_unverifiable` **before** closing the interval or appending any response-derived transition.
It cannot safely convert that condition into `attempt.failed`: §3.1 could authorize a retry or
fallback while the unverified session still holds the old attempt's authority.

These are the run-specific additions to the shared process lifecycle: the adapter was launched
through the durable gate, its session identity is journaled on the attempt, its active time is
charged, and its response produces an authoritative event only after cleanup. The standalone
validation probe has none of those four properties; it aggregates diagnostics and returns only
after its in-memory session identity reaches the same empty boundary.

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
  `pending_decision_ids`, and exits. The core **validates that set exactly**: it must equal every
  emitted question plus precisely those proposals with `requires_decision: true`. A missing id, an
  unknown id, or `outcome: completed` after emitting a blocking event is a `protocol_error`
  (`blocking_set_mismatch`) — otherwise a peer could strand a question the core would never surface,
  or block on an id nothing raised. A blocking proposal's `id` is a valid member of
  `pending_decision_ids`, alongside question ids. The attempt ends `BLOCKED` (a terminal
  attempt state — no process stays alive).
- **The blocked result is recorded by one fsynced event, and every request has exactly one durable
  source.** `attempt.blocked` carries the complete set of raised decisions *with their content* —
  question text, proposal ids, types, and blocking flags. A question's `decision.requested` derives
  from that recorded `raised` entry. A proposal's request derives only from
  `amendment.routed_human`, which owns the routing facts §1 requires; `attempt.blocked` records only
  that the attempt raised and, where applicable, blocked on the proposal. Recording question
  requests as independent appends first would mean a crash after the first question lost the
  second one's text, while deriving a proposal request from both events would give it competing
  authorities. Appendix C closes either source-to-request cut idempotently before treating the run
  as intentionally waiting.
- An unresolved blocking decision projects the movement and the run to `WAITING_HUMAN` (§6) — a
  projection of the outstanding decision, not a separately appended state event.
- **Emitted ids are attempt-scoped, so neither `decision_id` nor `proposal_id` is the emitted
  id.** The adapter kit guarantees question and proposal ids are unique only *within* an attempt,
  so two attempts of the same movement may legitimately both emit `q-1` — or both emit `p-1`. The
  core therefore derives **both** core ids collision-safely from `(attempt_id, emitted_id)` and
  carries the emitted id as provenance:

  The derivation and the allocation rules for both are in **A.4.3**, which covers every origin.

  **Ids with no emitted id are core-allocated**, since there is nothing to derive from. A.4.3 owns
  the complete rule for every origin — derived and allocated alike — so that two implementations
  cannot produce different identifiers for the same event.

  Nothing — no journal key, no CLI argument, no projection — may key on a raw emitted id.
  Amendment events are keyed by `proposal_id` (Appendix B), so without this two attempts each
  emitting `p-1` would collide on one amendment identity, and the second proposal would appear to
  be a repeat of the first.
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
  | **Amendment rejected** — at admissibility, by the human, or at decision-time re-validation | `decision_resume` | No revision change, so this behaves exactly like an answered question: the blocked attempt stays history and a new attempt continues on the **same** revision |

  **A rejected blocking proposal must still close its pending decision**, or the attempt would wait
  on an id nothing can ever resolve. All three amendment terminals therefore carry the
  `decision_id` and terminally resolve it in projection (§9) — including
  `amendment.rejected`, which may fire *before* any `decision.requested` exists. In that case the
  `decision_id` is the one derived from `(attempt_id, emitted_id)` (A.4.3), so it matches the id the
  attempt is blocked on whether or not a decision was ever opened.

  The next attempt learns the outcome through `resolved_decisions`, which carries a typed entry for
  a rejection rather than only for an answer:

  ```text
  resolved_decisions: [
      {decision_id, kind: "answer",             answer}
    | {decision_id, kind: "amendment_rejected", reason}   # closed enum: the amendment.rejected
                                                          #   reason, or `human_rejected`
  ]
  ```

  A performer that proposed an amendment and had it refused therefore knows that it was refused and
  why, instead of re-proposing it blindly.

  Protocol 2 delivers each retained resolution in this tagged form unconditionally.

  Whenever resolution returns the movement to `RUNNING`, a live driver continues into a new
  attempt — `performer.selected`, `attempt.started`, then `adapter.probed`, as for every executing
  attempt — and only then passes the resolutions in `resolved_decisions` (plus a compatible
  `session_hint`); if no driver holds the lease, a later `resume` does it (§7 command authority).

  **Which resolutions, and how many.** A new attempt receives every decision resolved **for its own
  movement, in this run, on the current revision**, ordered by resolving `seq`. Not just the last
  blocked attempt's — a movement can block more than once and later attempts need the earlier answers
  — and not the whole run's, since another movement's answers are not this movement's context.
  Revision-scoping falls out of §9: an approved amendment obsoletes old-revision decisions, so they
  are not resolved on the current revision and never appear.

  The list is bounded, because it competes with the 1 MiB frame cap (§4). One ordering and one
  measurement, so the delivered bytes are a function of recorded state:

  1. Order by resolving `seq`, ascending. This is the **delivered** order and the order A.5 hashes —
     A.5 does not re-sort by `decision_id`, because the hash must cover what was actually sent.
  2. Drop from the **front** (oldest first) while the **fully serialized request** exceeds 512 KiB —
     the whole frame, not the resolution subsection, since the cap applies to the frame.

  Truncation therefore removes the oldest answers, which a performer is least likely to still need,
  and the retained suffix stays in `seq` order. The omission is recorded
  (`adapter.probed.truncated_resolutions`) and reported. Truncation drops the *oldest* answers, which are the ones a performer is least likely
  to still need, and the journal keeps all of them regardless. A session hint must never be relied on
  to carry what was dropped — hints are an optimization and never required state (§4).

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
it. The gated adapter session must start before its enforcement can be observed, but the core may
append `adapter.probed` and send `execute` only if every row that applies to the movement's effective
grants is satisfied:

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
surfaced by `validate` and `status`. `validate` evaluates this predicate against its standalone
probes; `run` evaluates it against the selected attempt's gated peer. That is why either surface
requires cast resolution and probing (§7).

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
  decoding: unknown fields, duplicate keys, and invalid UTF-8 are all rejected, through the same
  parser as the wire. Maximum 1 MiB.
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

**Execution spawn is gated, so identity is recorded before any execution adapter code runs.** This
contract applies to run-scoped adapter and criterion launches, not to the standalone validation
probe defined above. The core cannot record an execution adapter's session id before it exists, and
cannot record it after without leaving a window in which a crash strands an unrecorded process. The
spike settled the ordering: the core starts a **trusted launch trampoline** as a new POSIX session
leader; while the trampoline blocks on an **inherited gate**, the core records and fsyncs its PID,
session id, and process-start identity in `attempt.started`; only then does it release the gate, and
the trampoline `exec`s the adapter **in place**. EOF on the gate before release makes the trampoline
exit **without executing adapter code at all**.

That is what closes the dangerous ambiguity rather than merely narrowing it, stated as narrowly as it
was measured: an unrecorded *trampoline* may briefly survive a crash, but it **contains no adapter code
and has no worktree mutation path before the gate opens**, and it never opens unless its identity is
already durable. The claim is deliberately not "it holds no authority" — the process still has the
ambient OS permissions of whoever launched it. What it lacks is any code that would use them.

`pid == session_id` is validated at handoff, since the trampoline is the session leader. PGID is not
persisted: §4 enumerates the recorded session and discovers its current process groups at sweep time.

The handoff contract, since recovery depends on reading it (Appendix C.2):

- Both files live under the attempt's **staging** root (`.partitur/work/…`, §1), never under
  authoritative run state.
- **Keyed per launch, not per attempt** — `<attempt_id>/<launch_id>`. The reason is *successive*
  launches, not concurrent ones: criteria run sequentially and short-circuit (§7), so nothing races,
  but one attempt performs many launches — the adapter, then one trampoline per external criterion,
  plus retries — and a per-attempt key would let a **stale handoff from an earlier launch** be
  mistaken for the current one.
- The trampoline **publishes its identity, fsyncs it, then blocks** on the gate. The core reads the
  published identity, records it in the journal, fsyncs that, and only then releases the gate. So a
  published identity always precedes a released gate.
- Both carry a **nonce created with the launch**, and a file whose nonce does not match is stale and is
  ignored rather than trusted.
- Exact shapes, since recovery parses them: `<launch_id>/identity.json` carries
  `{nonce, pid, session_id, start_identity}` and is written-then-renamed with a directory fsync;
  `<launch_id>/marker` is an advisory-locked file containing **only the nonce** — not empty, since an
  empty file could not carry one — that the trampoline holds for its lifetime. Both files carry the same
  nonce and recovery compares the copies: a mismatch means one of the pair is from an earlier launch, and
  both are ignored.
- Recovery discovers **unjournaled** `launch_id` directories by listing the attempt's staging root:
  a directory with no corresponding journal event is precisely the crash-inside-the-window case C.2's
  first row handles.
- **The marker is acquired before the identity is published**, and released only at trampoline exit.
  The other order would leave a window where an identity exists but nothing holds the marker. With this
  order the residual window is *before* the marker is taken, when the trampoline has published nothing
  and still contains no adapter code.

  So the property recovery may rely on is precisely **"marker free ⇒ no *released mutator* survives"** —
  not "no launch process survives", which the pre-marker window makes false. The weaker statement is the
  one that matters: an unreleased trampoline holds no adapter or criterion code and will exit on gate
  EOF without executing any.
- A held marker with no matching readable identity is **stabilized before it is classified**. A
  multithreaded process can have a leader already observable as gone while another thread still
  retains the shared file table and therefore the inherited marker lock. The first held sample is
  not proof that an unattributable process remains.

  The marker-stabilization deadline is **30000 integer milliseconds**, measured monotonically from
  the first complete observation of “matching marker held, no matching readable identity.” It is
  derived from the core's outer termination grace below, not from the spike harness: both bounds
  allow local process teardown to settle before the core may safely advance, and a trampoline that
  cannot yet be named and signalled receives the same pre-force allowance as a named session. This
  is a conservative policy ceiling, not a kernel timing guarantee. The 15000 ms probe deadline and
  first-party kit's 10000 ms grace do not apply: no probe RPC or adapter body exists in this window.
  The spike's 3000 ms timeout and 5 ms polling cadence remain test parameters and define no
  production behaviour.

  Until that deadline, recovery re-observes in this priority order: a matching, valid
  `identity.json` is verified and its session swept; otherwise a successful nonblocking marker-lock
  acquisition proves the marker free, is released immediately, and selects the consuming recovery
  row's no-released-mutator action; otherwise an expected held-lock result with no identity
  continues stabilization. At the deadline recovery takes one final observation in the same order.
  Still held with no identity halts `spawn_handoff_unverifiable`. Any malformed matching identity or
  unexpected identity-read, marker-open, or lock error halts that reason immediately rather than
  consuming the deadline. This one procedure applies to adapter and external-criterion trampolines:
  both inherit the same marker and publish identity in the same order.
- These are coordination files, not journal identity: the journal's copy is authoritative and the files
  are removed with the run's staging root.

**Process supervision.** The adapter and the vendor process it spawns are separate process
groups, so hard-killing a wedged adapter can orphan the vendor group. Termination is layered:
the adapter MUST handle `SIGTERM` by terminating its vendor process group before exiting, and the
core's outer termination grace is **30000 integer milliseconds**. A conforming adapter MUST keep
its own `SIGTERM`→`SIGKILL` grace strictly below 30000 ms. The direction is intentional: `probe()`
does not report an adapter's internal grace, so the core cannot discover and compare it; publishing
the core constant makes the same safety relation checkable where both values are known — in adapter
conformance. The first-party adapter kit's 10000 ms internal grace satisfies that bound.

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
  core reports the sweep as *incomplete* and follows the consuming boundary's prescribed failure
  or halt rather than claiming zero survivors. On the run execute-completion path defined above,
  that is the `sweep_unverifiable` halt: the §6 interval stays open and no response-derived attempt
  transition is appended; Appendix C retries the sweep before it closes that interval or
  synthesizes a failure. Conformance cleanup being the design's ceiling is one thing; asserting a
  clean result from state the core could not read would be another, and it is the assertion that
  would be dangerous.

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

  A successful fan-in records its own `movement_composition_dependency_hash`
  (`partitur/movement-composition`, A.4) alongside the composed base. The candidate-level hash
  covers only the final composition, so without this a movement's clean base would have no
  identity at all, and a change to *how it was assembled* — contributor membership, order, or the
  composition environment — would be invisible to the executed-dependency check (§9).
  A movement may have `needs` while receiving zero contributing change sets — for example, when
  every dependency is read-only. In that case `T` remains the run base tree and **no merge
  executes**. Its required `movement_composition_dependency_hash` uses A.4's
  `composition_mode: identity` projection: the empty contributor list is recorded as a fact, and
  no `composition_environment_hash` is fabricated for a subprocess that never ran.

  A merge conflict yields exactly one outcome, and it is **terminal, not a wait**: the core
  records `composition.conflicted` with the conflicting contributors and the conflicted paths
  as **evidence only**, then fails the affected movement — or, for candidate composition, the
  run — through the ordinary `movement.failed` / `run.failed` path with reason
  `composition_unresolvable`, so the conflict never bypasses the normal lifecycle and recovery
  cascade. It never merges silently and never picks a side.
  Interactive resolution of tree conflicts is future scope — a `WAITING_HUMAN` here would
  be a wait with no decision to answer and no resolution event, so v0.2 fails deterministically
  instead. Changes from failed, cancelled, or superseded attempts are never candidates.

  **A composition execution failure takes the same shape and a different reason.** The term is
  deliberately not "infrastructure failure": §3.1's Infrastructure class partitions **attempt**-failure
  kinds, and composition runs between attempts with no performer and no fallback chain, so that class
  cannot apply to it.

  It is defined by its **outcome, not by a list of ways to reach it**: the core ran the composition
  and **obtained no verdict** — neither a result tree nor a conflict. The core records
  `composition.failed` as **evidence only**, then fails the affected movement — or, for candidate
  composition, the run — with reason `composition_failed`.

  **The outcome alone does not select this reason, because other rules reach it too.** The live
  selection is an ordered test, and its order is **C.1's, not a second one** — a live core and a
  recovering one must not classify the same durable state differently. **C.1 governs**: if this test
  and C.1 ever disagree, C.1 is right and this is the defect:

  1. **A core-controlled interruption inherits its own authority.** Cancellation and supersession
     terminating a driver owner under §6's owner-termination rule, and an exhausted budget stopping
     the composition, are owned by §6's oracle, §6's commit table and §6's budget path. Where a
     **durable** control request already governs, its precedence decides even if the core had not yet
     signalled: a spontaneous Git failure under a durable `cancel.requested` does not outrank the
     cancellation. C.1 places control above the integrity halts below it, so this test does too.
  2. **An integrity condition halts and classifies nothing.** A change-set ref named by an event but
     missing or hash-mismatched is `missing_changeset_ref` (§1), and a halt never appends to a journal
     whose integrity is in question (B.7). That the same condition also prevents populating the
     temporary repository does not make it a composition failure.
  3. **Otherwise**, an observed no-verdict outcome is `composition_failed`, and Appendix D closes its
     causes over exactly that.

  A crash *during* composition is not on this list because it is not something the live core
  observes — it is Appendix C's, under its own precedence.

  **It is terminal and v0.2 does not retry it.** Composition has no performer, so there is no
  fallback chain to advance, and §3.1's classes are defined over attempt failures. A composition
  retry budget would be a second retry policy answering to nothing in §3, and inventing one to cover
  a transient Git fault is more machinery than the fault justifies. Stated rather than left as an
  assumed guarantee: a transient failure ends the run, and the operator re-runs.

  The separation from `composition_unresolvable` is the point, and the claim is narrower than
  "environment": an execution failure proves **no verdict was obtained**, never that the trees are
  unmergeable. A `driver_rejected` can originate from an in-tree `.gitattributes`, which is content
  rather than environment, and it still says nothing about whether the trees compose. Collapsing the
  two reasons would let a machine with a broken Git report a clean repository as unmergeable.

  **The exact invocation is normative**, because the composed tree depends on it. Each step runs,
  in a **non-bare** repository with system and global Git config isolated:

  The Git subprocess environment is an **allowlist, not the inherited environment**: only the
  variables below are passed, so `GIT_CONFIG_COUNT` / `GIT_CONFIG_KEY_*` / `GIT_CONFIG_VALUE_*` —
  which can inject arbitrary config, custom merge drivers included — cannot reach it from the
  operator's shell. The allowlisted set is part of the composition environment identity, so a change
  to it is detectable.

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
  - Any other exit is a **composition execution failure**, defined below, and must never be
    reported as a conflict.

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
  `repo_write`, the core verifies after §4's execute-completion boundary that the worktree is
  unchanged. The adapter session is already verified empty, so the check cannot race a surviving
  descendant. A Git tree comparison alone is insufficient: the check covers tracked content,
  **non-ignored untracked files, symlink targets, and file modes**, plus the protected paths of §2.
  A violation is `kind: grant_denied`, which §3.1 classes immediately terminal. For the
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
          | INAPPLICABLE     # a draft movement in a finalized run (§2) — never scheduled
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
  success (§4). After the same section's session cleanup and adapter-interval close, the core
  records `performer.completed` and the attempt enters `VERIFYING` while the change set is
  captured, acceptance runs, and any required human gate is decided. `attempt.completed` — and
  thus `COMPLETED` — is recorded **only** after all of that succeeds. Conflating the two would let a
  movement look successful on the strength of an adapter's self-report.
- `BLOCKED` is a **terminal** attempt state: the attempt exited while waiting on human
  decisions; the follow-up work happens in a **new** attempt. Only movements and runs
  use `WAITING_HUMAN`.
- A movement fails when **no further execution path is authorized** — retries and fallbacks
  exhausted, budget exhausted, or an immediate terminal cause such as `grant_denied`,
  `protocol_error`, human-gate rejection, `composition_unresolvable`, or `composition_failed`;
  `SUPERSEDED` exists only at the attempt level (steer, instruction revision, or an
  approved amendment → new attempt).
- `CONTESTED` is **not** a state at any level. It is a value of the `review_outcome`
  projection (§8); a contested movement reaches `WAITING_HUMAN` through its human gate
  like any other.

**`WAITING_HUMAN` is a projection, and it has a defined exit.** There is no
`movement.waiting_human` event: a separate state event would be a second authority
competing with the decisions themselves, and a crash between the two would leave the run
stuck in a state no decision explains. Instead:

- **Entry** — a movement or run is `WAITING_HUMAN` exactly while it has ≥1 **unresolved blocking
  decision**: a `decision.requested` with no terminal counterpart *and* `blocking: true`, together
  with `attempt.blocked` where an attempt raised it. Questions, human gates, and finalization are
  always blocking; an amendment decision is blocking iff the proposal set `requires_decision`
  (§4). A routed non-blocking proposal therefore waits for a human **without** stopping the run —
  which is the whole point of a non-blocking proposal, and is why the flag has to be journaled
  rather than inferred.
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
restarts and decision resumes are bounded by time alone (§3.1), the accounting must be
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
- **Durations are integer milliseconds everywhere they are persisted or compared**, even though the
  score declares `active_wall_clock_min` in minutes (`min × 60000`). A floating or
  loosely-rounded "minutes" value would let replay and admission disagree on whether the budget is
  exhausted, which is exactly the divergence the projection must not have.
- Consumption is the sum of `charged_duration` over closed intervals. An open interval is
  charged against the remainder for admission decisions using the current process's
  monotonic reading.
- Consumed history is never retroactively edited. A budget decrease (§9) yields
  `remaining_time = max(0, new_time_cap − consumed)` and
  `remaining_retries = max(0, new_retry_cap − retries_consumed)` — two independent caps, so a
  budget amendment that changes only one leaves the other untouched.
- **Whenever an interval is closed by a process other than the one that opened it**, the monotonic
  reference is unavailable — that process's clock is meaningless here, or it is gone entirely — and
  wall-clock cannot substitute for it, because clocks jump: NTP steps, suspend/resume, and manual
  changes all make a wall gap an unreliable upper bound. The situations below qualify, and all use
  the same rule rather than inventing one each:

  | Situation | `reason` | Closed by |
  |---|---|---|
  | Recovery finds an `execution.started` with no matching stop | `recovered` | the recovering process |
  | A wedged driver is fenced and cancelled (control channel, below) | `cancelled` | the canceller |
  | A wedged driver is fenced and **superseded** by an approved revision (§9) | `superseded` | the approving command |
  | A **responsive driver cancels itself** through the oracle's `(c)` (control channel, below) | `cancelled` | the driver, in the canceller role |

  The last row is the one case where the closer *is* the opener and the charge is still clamped, so
  it is listed here rather than left to the ordinary rule below. It is not the missing-reference
  situation the paragraph above describes — the driver has its monotonic reading — but the oracle's
  `(c)` must produce one event shape whichever canceller runs it. Otherwise a single E.2 edge
  acquires two payloads and `execution.stopped {reason: cancelled}` cannot be read without first
  establishing who wrote it. Uniformity of the oracle's output is worth more here than the accuracy
  of one close, and the clamp below bounds what is given up.

  In each case `charging: clamped` and

  ```text
  charged_duration = min( max(0, observed_at − wall_start), remaining_at_start )
  ```

  `observed_at` is **sampled by the closing process** — it is not journaled state — so that
  process records both `observed_at` and the resulting `charged_duration` in the fsynced
  `execution.stopped`. Every later replay reads the recorded charge and never recomputes it, which
  is what makes the projection stable. Honestly labelled, this is **bounded best-effort accounting,
  not fail-closed**: a backward clock jump between `wall_start` and the close can undercharge. The
  clamp guarantees only the upper bound — an uncertain interval never costs more than the budget
  that was actually available when it opened.

  A `charging: measured` close is the ordinary case and requires the closer to *be* the opener; any
  other closer uses `clamped`, as does the one opener the table above names. That is what makes the
  charge a deterministic function of recorded state in every case.
- An `adapter` interval that reaches `execute` closes ordinarily at §4's run execute-completion
  boundary, not when the response or adapter leader first exits. The opening driver keeps it open
  through the verified-empty session sweep, then appends one measured
  `execution.stopped {reason: normal}` before the response-derived attempt event. Appendix C owns
  the corresponding crash close and performs the sweep first. This ordering charges a same-session
  survivor until it is gone and leaves no second close for recovery to charge again.
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
  budget remains yields criterion `ERROR` on the ordinary quality path (§7). When the two deadlines
  fall on the **same instant**, the **criterion timeout wins** — `ERROR`, on the quality path — because it
  is the more specific of the two and because a tie resolved the other way would end a run on an
  ambiguity. What the rule fixes is the *classification*, so two implementations cannot disagree
  about which evidence the failure records. It does **not** make the attempt retryable at the tie:
  §3.1's first arm reads `remaining_time == 0` and selects `none` with
  `terminal_reason: budget_exhausted`. Both paths end the movement; they differ in what they record,
  and an `acceptance.failed {criterion_errored}` is the truer account of what happened. Only exhaustion of
  the *run's remaining budget* takes the budget path, which §3.1 classes immediately terminal —
  there is nothing left to fund a retry.

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

  **The adapter's session identity is persisted too, not just the driver's.** A driver can die while
  the adapter session it spawned survives, and §4's survivor sweep needs a target that is safe to
  inspect and kill. `attempt.started` therefore records the adapter's session id together with its
  process-start identity, so recovery has a verifiable sweep target rather than a bare SID that may
  have been recycled. Without it, a recovering process would face a live foreign session and no way
  to tell whether it was the run's.

  **Lease and authority are ordered.** `authority.granted` is appended and fsynced **before**
  `driver.lease` is written. On recovery:

  | Observed | Action |
  |---|---|
  | `authority.granted` present, no lease | The driver died between the two writes; the epoch stands and the lease is reclaimable at the next epoch |
  | Lease present, no `authority.granted` at its epoch | The lease is an orphan from a crashed acquisition: quarantine it, then re-evaluate — reclaiming immediately would bypass a pending cancellation or prepare (C.1) |
  | Both present and consistent | Verify the owner per above |

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
  AND driver.lease exists, and its epoch and token match the caller's
  AND the recorded PID and process-start identity still match
  ```

  Note where each conjunct is read from, since this is the one place the two files could be confused:
  the **epoch** comes from the journal projection (`authority.json` is only its checkpoint), and the
  **token** comes from `driver.lease` and nowhere else. `authority.json` is never consulted for the
  token, and never authoritative for the epoch.

  **Fencing and terminalization are one transition, under the canceller's authority.** A *canceller*
  is a role, not a fixed process: it is whichever actor completes the already-durable
  `cancel.requested` — the responsive driver, the cancelling command when no driver remains or after
  it terminates a wedged owner, or recovery. Fencing and then appending `run.cancelled` as a
  *separately authorized* step is impossible: the canceller does not hold the driver authority the
  CAS demands, and the fenced driver no longer does either, so the append could never pass. Instead
  `(a)` runs before the lock-held transition, and the canceller holds the state lock across `(b)`–`(f)`,
  authorized by **run lifecycle** rather than by the lease. There is never an intermediate state in
  which the run is fenced but not terminal.

  The steps inside that transition are the cancellation oracle in the control channel — this paragraph
  establishes
  *whose authority* they run under and deliberately does not restate their order, since a fourth copy
  of the sequence would be a fourth chance to disagree with the other three.

A run is *active* while nonterminal (`RUNNING` or `WAITING_HUMAN`). v0.2 allows one active
run per repository: `partitur run` refuses to start while one exists (resume or cancel it
first). Commands that address an existing run accept its explicit id or select the unique active
run. The nonterminal journal state is the logical guard; the lease guards concurrent *drivers* of
the same run.

**Cancellation is run-scoped.** `partitur cancel` cancels the *run*, not a single attempt.
An attempt-scoped cancel would leave the movement `RUNNING` with no selection reason for what
comes next — not a retry, not a fallback, not a restart — so v0.2 does not offer one. The
protocol-level `cancel` (§4) still targets the current attempt, because that is the process
that must stop; the *authority* is the run-scoped request. `run.cancelled` atomically projects
every nonterminal movement and attempt to `CANCELLED` (Appendix B), and it is appended by whichever
canceller the control channel below selects — the live driver that observed the request, or
`partitur cancel` itself where no valid lease owner remains. No branch leaves a cancelled run
active until someone happens to `resume`. Recovery is therefore only needed for a crash
between the request and its terminalization.

**The control channel.** With no daemon, nothing waits in memory, so out-of-band
control — cancellation, and revision approval that supersedes a nonterminal attempt — needs a
durable path that reaches a live driver at any point in its work:

1. The requesting command appends the authoritative control event under the state lock —
   `cancel.requested` for a cancellation, or **`amendment.approval_prepared`** for a supersede.
   Cancellation's remaining steps happen in the order below, and the order matters because
   `approval_abandoned` is what **lifts the barrier**: everything the barrier was protecting must be
   done before it is appended, or a crash immediately after its fsync leaves the barrier lifted with
   the immutable revision path still occupied.

   ```text
   (a) sweep every recorded adapter and criterion session to verified empty
       └ sweep_unverifiable → halt
   (b) if a prepare is pending:
         quarantine the prewritten snapshot, remove the plan and sidecar,
         then append amendment.approval_abandoned {reason: cancelled}   # barrier lifts here
   (c) if ANY execution interval is open:
         append execution.stopped {reason: cancelled, charging: clamped}
   (d) if a lease matching observed_authority_epoch still exists:
         advance the authority epoch to observed + 1 and revoke the token
         — but do NOT remove the lease yet
   (e) append run.cancelled, carrying fenced_epoch iff (d) advanced it
   (f) only now remove the stale lease
   ```

   Two of these separations are load-bearing rather than stylistic.

   **(c) and (d) have independent predicates.** An interval can be open whether or not the owner needed
   fencing — a responsive driver that died after closing writers, or a verifiably dead owner, both leave
   one — so gating the close on fencing would let `run.cancelled` be appended with an interval still
   open, and the budget projection would then read a run that never stopped consuming.

   **(d)'s predicate is the surviving lease, not the owner's liveness.** "The owner was live and must be
   fenced" is not replayable: after `(d)` advances the epoch and crashes, recovery observes a *verifiably
   dead* owner, so a liveness-based predicate is false, `(d)` is skipped, and `(e)` appends without the
   `fenced_epoch` that was already advanced — the fence is lost exactly as it would have been the other
   way. Keying it on **a lease still matching `observed_authority_epoch`** makes it durable and
   idempotent: advancing to `observed + 1` twice yields the same epoch, and `(f)` removing that lease is
   what finally makes the predicate false.

   **(f) comes after (e), not inside (d).** The advanced epoch is authoritative only once journaled, and
   removing the lease is what makes the old incarnation unable to act. Removing it first would destroy
   the very state `(d)`'s predicate reads. Journaling first and cleaning after makes the stale lease a
   consequence of a durable fact rather than its precondition.

   **This list is the single oracle for cancellation.** Appendix C.1 and step 6 both *reference*
   it rather than restating it, and the live path executes these steps in this order. A paraphrase that
   dropped the conditional (c), or inverted the order inside it — earlier drafts did both — leaves two
   normative sequences,
   and no amount of testing can adjudicate between contradictory normative text: a harness only tests
   whichever reading the implementation happened to choose.
   Supersession's durable request is the *prepare*, not the approval: the approval is what the
   handshake ends with, so it cannot also be what starts it (§6 quiesce).
2. It then wakes the verified current lease holder as a **best-effort latency
   optimization** — never as the mechanism of record.
3. The driver watches control state continuously **for as long as it holds the lease** —
   a separate goroutine tailing the authoritative journal while the stdout reader stays blocked.
   Polling only between criteria cannot interrupt a long `execute`, so this is a continuous
   watch, not a checkpoint.

   The watch spans the driver's whole tenure rather than only a pending adapter `execute`, and the
   reason is step 6 rather than the adapter. A driver blocked on a long external criterion or on
   composition is as unable to poll as one blocked on `execute`, so a watch scoped to adapter
   execution would leave those phases with no acknowledgement path — and step 6 would then read a
   perfectly healthy driver as a wedged owner and terminate it once the deadline passed. Appendix
   E.4 records that hazard as one created by implementation *order*; scoping the watch more narrowly
   than the canceller role is assigned would make it a defect in this section instead.

   Measured append-to-detection latency at a 20 ms poll interval was
   22 ms on macOS and 24 ms on Linux, bounded by poll interval plus scheduling. A signal can
   prompt an immediate read; correctness never depends on it.
4. On observing the request the driver stops what it launched. Where an adapter `execute` is in
   flight it issues protocol `cancel` and awaits that call's response, since §4 makes the response
   the completeness marker; where none is — a pending criterion, a composition, or an idle
   moment between them — there is no protocol `cancel` to issue and nothing to await. Either way it
   then applies the outer termination grace and process-tree sweep (§4) to every session it
   recorded. It then acts as
   the canceller and executes the cancellation oracle itself, authorized by run lifecycle rather
   than by its lease.

   **The response is awaited, not obeyed.** From the moment the driver observes the durable
   `cancel.requested` — not from the moment it writes the protocol frame — it
   records **no response-derived attempt outcome** — whatever the `outcome` field says, including
   `completed`. The boundary is the observation rather than the signal because this document
   already fixes it there: a durable control request governs "even if the core had not yet
   signalled". A driver that observed the request and had not yet written the frame would otherwise
   have a window in which a response it is about to discard could still be journaled.
   §4 makes the response a completeness marker, which is why step 4 waits for it; it
   does not make its verdict authoritative here. `run.cancelled` is the single producer of that
   attempt's terminal projection, so journaling the response as well would be a second producer of
   one projection, which is the defect this section spends its length avoiding. §4's blessed race —
   the core "may legitimately race a cancel against completion" — grants the *adapter* the freedom
   to finish; it does not oblige the core to record that as the attempt's outcome. And the rule has
   to cover every outcome rather than only `cancelled`: `waiting_human` would otherwise open a
   `decision.requested` on a run that is terminating. Evidence is unaffected, because artifacts and
   the other observations are appended as they arrive rather than derived from the response.

   This **extends** a principle §4 states for one case; it does not merely restate it, and calling
   it a restatement would be the more comfortable claim rather than the true one. §4's run execute
   completion says that where an adapter *hangs after responding*, the interval stays open until
   "the existing budget-exhaustion, cancellation, or supersession path terminates and sweeps it;
   none of those paths appends the provisional response's transition" — expressly scoped to the
   hung adapter. The extension is nonetheless required rather than optional: the reason §4 gives
   there is that another authority has taken the outcome over, and that reason does not depend on
   whether the adapter went on to hang. Leaving the responsive case out would make the same
   response authoritative or not according to what the adapter did *after* sending it.

   The drain makes no oracle step unnecessary and skips none: it does not close
   the execution interval, so `(c)` is evaluated on its own predicate exactly as anywhere else, and
   it leaves `(a)`'s verified-empty sweep an idempotent no-op. That is the oracle's conditionals
   being evaluated, not a second cancellation sequence. The driver's matching lease makes `(d)`
   true, and because `(f)` follows `(e)`, the resulting `run.cancelled` carries `fenced_epoch`.
   Thus the live journal is the same one produced when recovery re-enters the oracle after the
   driver dies immediately before `(e)`. No new recovery case or artifact follows: in that crash
   `RC-RESUME-006` re-enters from `(a)` and reproduces the same fenced epoch; after `(e)` and before
   `(f)`, `RC-RESUME-002` wins and retries the residual cleanup.
5. If no driver is alive, the cancelling command itself completes the terminalization; a
   later `resume` observes an already-terminal run and launches nothing.
6. **A live but wedged owner** — lease verified, yet the driver never acknowledges the journal —
   is the case a responsive/dead dichotomy misses. After a bounded acknowledgement deadline the
   canceller re-verifies the lease owner. Its termination target is **only the recorded lease-owner
   PID with its recorded process-start identity**: §4 contributes its discipline here, not its
   adapter-session enumeration. The canceller re-checks that identity immediately before each
   signal, sends `SIGTERM`, waits §4's outer termination grace, then repeatedly sends `SIGKILL` and
   re-observes until that owner is verifiably gone — **halting `owner_unverifiable` if a required
   re-verification fails**, since a process that cannot be named cannot be terminated.

   The driver's own session is never a sweep target: the core did not create it, so it is not an
   ownership set the core established, and it routinely contains processes the core has no claim
   over. Its supervisable descendants are the recorded adapter and criterion sessions. Step 6 does
   not sweep those sessions itself; the cancellation oracle's `(a)`, which follows the owner
   termination immediately, applies §4's session mechanism to them. Thus a successful session
   sweep still says nothing about the owner's identity, and is not a substitute for the owner
   re-verification. A launch whose identity is not yet recorded is outside this target set. That is
   the residual §4 already admits: an unrecorded trampoline may briefly survive, but it has no
   adapter or criterion code and no worktree-mutation path before its gate is released.

   The canceller then **executes the cancellation oracle** with its conditional phase (d) taken,
   authorized by run lifecycle rather than by the lease.

   The acknowledgement deadline is a latency and ownership parameter, not a correctness one: a
   legitimate `(a)` sweep has no fixed deadline. Its expiry merely moves responsibility to the
   same oracle, so terminating a slow driver yields the same journal rather than a competing
   terminalization. C.1 selects its terminal row before this escalation when the driver completed
   during the wait, so a successfully cancelled run cannot acquire a spurious `owner_unverifiable`
   halt from the escalation. The state lock is `flock`, which the kernel releases when a terminated
   driver exits, so the escalation strands no lock. The acknowledgement deadline **SHOULD exceed**
   §4's 30000 ms outer termination grace, allowing a conforming drain normally to win that race;
   §6 fixes no more specific duration.

   This step deliberately does not restate the order. An earlier draft did, and inverted it — advancing
   the epoch before closing the interval — which is precisely the kind of second normative sequence that
   leaves an implementation free to pick either.

   The interval closure is its own append rather than a field of `run.cancelled` because budget
   consumption is projected from paired `execution.started` / `execution.stopped` events and
   nothing else; a charge hidden inside a cancellation payload would be invisible to that
   projection. Two appends inside one lock-held transition is still one transition: recovery
   completes the pair idempotently if a crash lands between them.

   It must be one transition, not a sequence. Fencing first and appending afterwards cannot work:
   once the epoch is bumped, neither the canceller nor the fenced driver holds driver authority,
   so a subsequent append would fail its own check. The invariant this preserves: **the run is
   never declared cancelled while the old execution authority could still mutate it, and never
   left fenced without being terminal.**

**Supersession uses the same branches, including the wedged one.** An approved revision must
supersede every nonterminal attempt (§9), and the driver holding that attempt can be wedged exactly
as it can be during cancellation — so the two are not different mechanisms, only different terminal
intents:

**Driver disposition is settled before the approval is appended, never after**, and the handshake that
makes it possible is now measured rather than assumed. The difficulty was real: holding the state lock
prevents the driver from appending an acknowledgement, releasing it makes the lease observation stale,
and a driver can wedge *after* acknowledging. The resolution is a durable prepare plus an
acknowledgement that is a **lease move rather than an append**, so the driver never needs the lock *for
the duration of the drain* — it takes it only briefly to make the move a compare-and-swap (step 2):

> Before an approval may change the score head, the core appends and fsyncs
> `amendment.approval_prepared`, bound to the proposal, base head, target attempts, and observed
> authority epoch. A quiesce ACK is accepted only when the driver has verified the adapter session
> empty, closed its interval, stopped every driver-authorized writer, and atomically moved its exact
> matching `driver.lease` to the prepare-bound quiesced path. The approving command then revalidates
> the prepare and base head under the state lock and appends `amendment.approved` once; on timeout it
> fences the still-matching incarnation and carries that epoch in the same approval event.

**No second fencing event is needed**, which is why §9's single-transition rule survives: the prepare
is a control request that changes nothing, and a wedge discovered at timeout is carried by the one
approval event that was going to be appended anyway.

Two design choices make the interval between prepare and commit safe, and they matter more than the
step list:

- **The snapshot and the complete approval payload are written *before* the prepare.** Recording only
  a semantic hash would be unrecoverable: `amendment.approved` also needs `new_snapshot_file_hash`, the
  typed delta, `actual_impact`, obsoleted decision ids, candidate binding, and finalization state — and
  an **auto** proposal has no durable proposal record to rebuild them from. So the snapshot file is
  written and fsynced first, the full planned approval payload is persisted beside it as
  `prepares/<prepare-id>.json`, and the prepare event references both by raw hash. Recovery then
  *replays* a plan rather than recomputing one.
- **Durable consequences close before a prepare can exist.** Under the state lock, a command that
  has reached approval intent first closes every already-determined, non-supersedable consequence
  selected by `RC-RESUME-011`, `019`, `020`, `021`–`023`, `025`, `026`, `028`–`030`, `037`,
  `039`, `040`, and `047`. These are recorded failures or completed verdicts awaiting their §3.1 or lifecycle
  consequence, composition evidence awaiting its terminal, and durable sources awaiting mandatory
  `decision.requested` authority. The command applies those Appendix C cases idempotently until none
  matches, then re-establishes §9 steps 1–9 and approval intent against the resulting projection
  before writing a snapshot or plan. A consequence that selects a retry or fallback remains pending
  until the between-unit scheduler records its `performer.selected`; prepare cannot step around it.

  This is narrower than quiescing all unfinished work. A `performer.selected`, `attempt.started`,
  `adapter.probed`, `performer.completed`, `acceptance.started`, or `criterion.started` without a
  verified outcome still belongs to a nonterminal attempt and may be superseded by the prepare.
  What is newly forbidden is establishing `amendment.approval_prepared` after an authoritative
  event has already fixed a consequence but before that consequence is durable. Without this
  barrier, an old-revision disposition, terminal cascade, or decision authority could race the new
  head and either disappear or be realized beside its revision replacement.
- **A pending prepare is a mutation barrier.** Between prepare and commit the only mutations permitted
  are the drain itself: closing an execution interval, sweeping sessions, the lease rename, and
  cancellation — and because cancellation is admitted through the barrier, **every step that follows
  must re-check `cancel.requested` and yield to it.** Otherwise the live pipeline and recovery would
  implement opposite orderings: recovery gives cancellation precedence and abandons the prepare, while
  a live approver could observe a matching sidecar and approve a run that was already cancelled. Everything else — `attempt.started`, acceptance events, candidate materialization,
  opening or resolving decisions — is refused with `prepare_pending`. Without the barrier, commit
  would have to re-run every runtime guard, because a `NARROW_GRANTS` guard that passed at prepare
  time can be falsified by the driver starting that very movement before it notices the prepare.
  Freezing the run is simpler than re-deriving what changed, and it is what makes "revalidate the
  prepare" sufficient.

The procedure:

1. **Plan and prepare.** Under the state lock, having closed the durable-consequence barrier above
   and re-established approval intent (§9 policy, not merely admissibility): write and fsync the new
   snapshot; write and fsync `prepares/<prepare-id>.json` carrying the complete approval payload;
   then append and fsync
   `amendment.approval_prepared` referencing both. At most one prepare may be pending per run. Release
   the lock — the driver cannot acknowledge while it is held.
2. **Quiesce or expire.** The driver, on observing the prepare, sweeps its adapter session **and every
   recorded criterion launch** to verified empty (§4), closes its execution interval, and stops every
   driver-authorized writer. It then **briefly takes the state lock**, revalidates that this exact
   prepare is still pending, that **no `cancel.requested` is present**, and that its lease still
   matches, renames `driver.lease` to
   `driver.quiesced.<prepare_id>`, fsyncs the directory, and releases.

   The lock is required and the rename alone is not sufficient: POSIX rename is atomic but it is not a
   compare-and-swap — it cannot mean "rename only if the contents still hold this epoch and token".
   Without the brief lock, cancellation or reclamation races the check-to-rename interval. The
   approver still does not hold the lock while *waiting*, which is what the deadlock required.
3. **Commit.** Re-acquire the lock and revalidate — in this order, so cancellation outranks approval
   exactly as it does in recovery (Appendix C.1): **`cancel.requested` absent**; the prepare still
   pending; the prewritten snapshot present with its recorded raw *and* semantic hashes and bound to the
   plan; the base head unchanged; and then the sidecar or deadline.

   | Observed at commit | Action |
   |---|---|
   | `cancel.requested` present | **Do not approve.** Hand off to the cancellation oracle **from step (a)** with (b) taken — not from (c). Restating (b) here and skipping (a) would let a cancellation arriving after the quiesce deadline terminalize the run without ever sweeping its sessions, which is the one thing (a) exists to prevent |
   | Snapshot missing, or either hash mismatched, or not bound to the plan | Halt `missing_snapshot_file` — the approval names bytes that no longer exist |
   | Quiesced sidecar present and matching | append `amendment.approved` **once** from the persisted plan, `fenced_epoch` omitted |
   | Deadline passed, lease still present and matching, owner **unverifiable** | Halt `owner_unverifiable`. The row below applies step 6's owner-termination rule before fencing, and that cannot be done to a process recovery cannot name. A successful **session** sweep does not make the **owner** verifiable — the adapter and criterion sessions and the driver's own identity are separate things, and sweeping the first says nothing about the second. C.1's halt does not cover this case, because a live approver never passes through C.1 |
   | Deadline passed, lease still present and matching | Sweep every recorded adapter and criterion session to verified empty. Then **re-verify the owner immediately before terminating it** — the row above was evaluated before the sweep, and the sweep takes time, so the verification that selected this row may be stale by now (§4 requires the same recheck before each signal). Halt `owner_unverifiable` if that inspection fails; if the owner has become **verifiably gone**, skip termination and take the next row's action instead, the sweep already being done. Only an owner still verified as the matching live one is terminated under step 6's owner-termination rule. Then close its open interval with `execution.stopped {reason: superseded, charging: clamped}`, advance the epoch and revoke the token, append `amendment.approved` with that `fenced_epoch` — **and only then** remove the now-stale lease, since the journaled epoch advance is what makes it stale |
   | No sidecar, lease present, owner **verifiably gone** | the driver died mid-drain. Sweep as above, then treat exactly as the fenced case: advance the epoch, append with `fenced_epoch`, clean the lease. No deadline wait is needed — a dead owner will not acknowledge |
   | No sidecar, lease present and matching, owner **not verifiably gone**, deadline not yet passed | **Wait.** This is the table's only non-terminal row: release the state lock and observe until a sidecar appears, `cancel.requested` lands, the owner becomes verifiably gone, or the deadline expires — then re-enter this table from the top. The driver is mid-drain and entitled to finish, and holding the lock while waiting is the deadlock step 2 exists to avoid. The predicate is *not verifiably gone* rather than *verified live* so an **unverifiable** owner also waits: waiting mutates nothing. If it is still unverifiable when the deadline expires, the halt row above catches it — not the fence row, which would terminate a process it cannot name. Recovery reaching this state takes the same posture instead of inventing a verdict, though C.1's scoped `owner_unverifiable` halt usually resolves it first. Without this row the state selects no action at all, which E.4 forbids |
   | No lease and no sidecar at all | nothing to quiesce: append `amendment.approved`, `fenced_epoch` omitted, no deadline wait |
   | Base head changed, or the plan fails validation below | quarantine the prewritten snapshot and remove the plan and sidecar, **then** append `amendment.approval_abandoned {reason: base_head_changed \| plan_invalidated}` — that order, since the event lifts the barrier. §9 may then be re-run from step 1 |

   **A lease present but *not* matching `observed_authority_epoch` is unreachable here, and that is a
   claim rather than an omission.** Only this table advances the epoch under a pending prepare: the
   barrier admits nothing but the drain and cancellation, the drain does not advance it, and
   cancellation leaves through the first row. So every reachable state with a lease has a matching
   one. If an implementation ever observes otherwise, the barrier has been violated somewhere else and
   the right response is to halt, not to guess which incarnation is authoritative.

**Plan validation is a closed predicate**, not a judgement. Commit verifies the plan bytes against
`plan_record_hash` — a mismatch or missing file halts `missing_prepare_plan`, exactly as recovery does —
and then requires equality on every field the prepare and the plan both carry:

```text
plan.proposal_id            == prepare.proposal_id
plan.base_revision          == prepare.base_revision
plan.base_hash              == prepare.base_hash
plan.new_revision           == prepare.new_revision
plan.new_snapshot_hash      == prepare.new_snapshot_hash
plan.new_snapshot_file_hash == prepare.new_snapshot_file_hash
plan.superseded_attempt_ids == prepare.target_attempt_ids
plan.mode                   == prepare.mode
  and, per mode: decision_id present iff human; envelope_class present iff auto
```

Any inequality is `plan_invalidated`. Duplicating these fields is deliberate: the equality check is what
makes a plan and a prepare provably the same operation rather than two that merely look alike.

4. **Clean up idempotently.** Remove the quiesced sidecar and the plan file. A leftover sidecar with no
   pending prepare is inert and removed on sight; it authorizes nothing.

The run stays nonterminal throughout: supersession fences an *incarnation*, not the run, so a new
driver acquires authority at the incremented epoch via `authority.granted` and continues on the new
revision. Cancellation differs only in that its fence and its terminalization coincide.

The journal is the authority; the signal only reduces latency. Because supersession and
cancellation share every branch, this is one general control channel rather than a cancel-only
feature.

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

The complete registry of authoritative events — fsync requirement, idempotency key, legal source
state, resulting projection, **and exact payload** — is **Appendix B**, which is normative. An
event type absent from Appendix B does not exist, and a payload field absent from its schema
there is malformed rather than ignored.

## 7. Acceptance runner and CLI v0.2

When an attempt's `completed` response has crossed §4's execute-completion boundary, the core runs
acceptance **in a fixed order**, before the worktree is removed. The attempt is in `VERIFYING`
throughout (§6):

```text
performer.completed                       (session verified empty and adapter interval closed;
                                            vendor execution ended — not success)
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

**Every external criterion command is launched through the gated session-leader trampoline** (§4).
`criterion.started` records the trampoline's PID, session id, and process-start identity before the
gate is released; the trampoline then `exec`s the criterion in place. Criterion commands and their
descendants MUST NOT create another POSIX session — the same conformance requirement §4 places on
adapters, and for the same reason: a session escape leaves a process recovery cannot name.

**The session is swept to verified empty on every external-command outcome**, not only during
recovery. A command's leader can exit while a same-session descendant survives, so recording
`PASS`/`FAIL`/timeout and then running the post-hoc worktree check would verify a tree that descendant
can still mutate. The order is therefore fixed: command exits → **sweep the recorded session to
verified empty** → post-hoc verification → `criterion.completed`. `sweep_unverifiable` fails the
criterion as `ERROR` rather than reporting a verdict the core could not isolate.

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
complete a movement over a red or errored hard criterion. The paths out are a retry (§3.1 classifies both as quality failures) or an
audited score amendment changing the criterion — never envelope-eligible, applying only to a new revision and a new attempt, and
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
    ],                               # `id` MUST be non-empty and unique within this artifact:
                                     #   overrides and gate decisions address individual
                                     #   findings as (artifact_instance_id, finding_id) (§8),
                                     #   which is not addressable if ids repeat or are blank.
                                     #   A duplicate or empty id makes the artifact malformed
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
**Appendix C.3**, under the run- and attempt-level precedence of C.1 and C.2, evaluated top-down with the first matching row
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
| `cancel` | appends the durable run-scoped cancel request, and acts as canceller to complete terminalization once no valid lease owner remains — because none did, or because it terminated a wedged one (§6) | no |
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
partitur resume          # resume after interruption; recovers by Appendix C, then takes the
                         #   lease only when §6 authorizes it
partitur apply           # apply the candidate to the checkout (§8)
partitur apply --recover           # only from APPLYING | RECOVERY_REQUIRED
partitur promote-score             # copy a run revision to partitur.yaml (CAS, §1, §8)
partitur promote-score --recover   # only from PROMOTING | RECOVERY_REQUIRED
partitur version         # prints the core version; no run state is read or written
```

**Command repository anchoring.** For every command, the invocation working directory is `<repo>` —
exactly the root whose project inputs, authoritative `.partitur/runs/` state, writable
`.partitur/work/` staging, and owned Git refs §1 lays out. Partitur never searches parent
directories for `partitur.yaml`, `.partitur/`, or a Git worktree root, and never retargets a command
to a parent that contains one. Upward discovery could silently operate on a parent's run state,
after which state mutation, tree composition, checkout application, score promotion, and
protected-path enforcement would all use an inferred root. The one explicit anchor avoids a second
notion of “the repository.”

Every project-relative fixed path is resolved from that `<repo>`. Operator-supplied relative path
operands such as `--answer-file` and `--patch` are resolved from the same invocation directory;
absolute operands remain absolute. Inputs whose locations are explicitly outside the project keep
their own stated discovery rules — the user-global cast under `~/.config/partitur/` and adapter
executables on `PATH` — and neither can redefine `<repo>`.

Anchoring defines **where** a command looks, not **what must exist there**. `init` creates its files
at the anchor; `validate` reads there without making Git a precondition; `version` reads no project
state, so the rule has no filesystem effect. Commands that require existing run state, Git state, a
clean checkout, or a particular projection still use their own §5–§9 preconditions. When such a
precondition requires a Git worktree, the anchored `<repo>` itself is the worktree root the command
uses; finding a parent worktree never changes the anchor or satisfies the command by retargeting it.

`--recover` is refused outside the two states that admit it, and the normal form of each
command is refused inside them. `apply` before `promote-score` is an enforced precondition,
not a convention (§8).

**Operands and options.** Enough to be a contract rather than a sketch; anything not listed is not
part of v0.2's surface:

```text
partitur validate
partitur answer  <decision-id> --answer <text> | --answer-file <path>
partitur approve <decision-id> --approve | --reject [--reason <text>]
                 [--override <artifact-instance-id>:<finding-id>]...   # requires --reason
partitur amend   --patch <path>            # RFC 6902 JSON; - reads stdin
                 --reason <text> [--claimed-impact <path>]
partitur cancel  [<run-id>]
partitur run
partitur resume  [<run-id>]
partitur status  [<run-id>] [--json]
partitur logs    [<run-id>] [--jsonl] [--follow]
partitur apply   [<run-id>] [--recover]
partitur promote-score [<run-id>] [--recover]
```

Every command is **non-interactive**: a missing operand is an error, never a prompt, so the CLI is
scriptable and a GUI can use the same commands (§0). `partitur run` accepts no run-id operand or
option: §1's core allocation is the only creation path. For commands whose syntax includes
`[<run-id>]`, omitting it selects the unique active run and errors if there is not exactly one.
`--approve`/`--reject` is mandatory rather than defaulted, because defaulting either direction on a
human gate would be indefensible.

**`partitur status` observable surface.** `status` is an observation of the selected run's
authoritative journal and its immutable pinned score snapshots. It takes neither a driver lease nor
the repository state lock; it never creates a directory, repairs a tail, rebuilds a checkpoint, or
writes a journal event. In particular, it does not turn the B.7 torn-tail repair into a read
operation. Its projection uses the same event application and score-derived movement seed as
`resume` and `apply`; it is not a second state model. A missing selected run, missing or unreadable
pinned snapshot, malformed journal prefix, or an event the core cannot project is a refused
precondition or recovery halt as applicable — `status` never guesses a state from the root score,
current cast, worktree, or a manifest.

`--json` writes exactly one UTF-8 JSON document followed by `\n`, and writes no prose to stdout.
The top-level `schema` is the required literal `partitur/status+json;v=1`. It is the stable
interface surface shared with a later GUI: a compatible producer may add fields but must not change
the meaning or type of a v=1 field; a consumer must ignore unknown fields. Every array is present
even when empty. Movement order is the pinned score's declaration order; attempts, advisories, and
marks for the same movement are stable by their durable identifiers. `candidate` is `null` until a
candidate is recorded. The complete v=1 shape is:

```text
{
  schema: "partitur/status+json;v=1",
  run: {
    id, lifecycle: RUNNING | SUCCEEDED | FAILED | CANCELLED,
    score: { revision, semantic_hash, file_hash },
    pending_decisions: [{ id, type, movement_id?, attempt_id?, score_revision }],
    movements: [{
      id, state,
      attempts: [{ id, state, failure: null | { kind, reason? } }],
      marks: [{
        grade: VERIFIED | APPROVED | REVIEWED,
        attempt_id,
        criteria: [{ id, spec_hash }],
        subject_tree, score_revision, failed_attempts,
        findings_instance_id?, review_outcome?, gate_decision_id?
      }]
    }]
  },
  application: {
    state: NOT_APPLIED | APPLYING | APPLIED | FAILED_CLEAN | RECOVERY_REQUIRED,
    candidate: null | {
      id, score_revision, base_tree, result_tree, ordered_change_sets,
      contributors: [{ movement_id, change_set_id }], composition_dependency_hash
    }
  },
  promotion: { state: NOT_PROMOTED | PROMOTING | PROMOTED | RECOVERY_REQUIRED },
  enforcement_advisories: [{ attempt_id, dimensions }],
  journal: { integrity: INTACT | TAIL_UNPARSEABLE, truncated_seq?, discarded_bytes? },
  recovery: { state: NOT_REQUIRED | RECOVERY_REQUIRED, reason? }
}
```

`criteria` is the full completed acceptance set, including core-generated checks, in acceptance
plan order; it is never replaced by a count alone. A VERIFIED mark exists only with the complete
criterion list, exact subject tree, revision, and failed-attempt count. A REVIEWED mark additionally
carries its findings artifact instance and one of `CLEAN`, `CONTESTED`, or `OVERRIDDEN` as
`review_outcome`; an APPROVED mark carries its exact gate decision id. Fields inapplicable to a
grade are omitted, not filled with a misleading empty identifier. `enforcement_advisories` is the
per-attempt record of the §4 predicate's explicitly allowed advisory exceptions; a fail-closed
rejection has no successful attempt observation to disguise as an advisory.

Without `--json`, `status` renders the same projection as deterministic lines: the run id and
lifecycle, pinned score head, journal integrity and recovery state, application and promotion
states, then its pending decisions and each movement with its attempts, marks, and enforcement
advisories. A mark line is never bare: it renders, for example, `VERIFIED (2 criteria: lint
[sha256:...], tests [sha256:...]; tree abc123; rev 4; after 1 failed attempt)`, and appends
`findings <instance>; review outcome <outcome>` for REVIEWED and `gate decision <id>` for APPROVED.
Thus the human surface retains the same evidence binding as the JSON surface rather than collapsing
a grade into a reassuring label.

**`partitur logs` observable surface.** `logs` is an observation of the selected run's
authoritative journal. It takes neither a driver lease nor the repository state lock; it never
creates a directory, repairs a tail, rebuilds a checkpoint, or writes a journal event. It uses
`status`'s read-only selection and replay path, including the pinned-score seed used to identify
the unique active run, rather than constructing a second lifecycle model. Its stream is the
complete durable sequence of the B.7 `log` and `progress` observations in journal order; it does
not render state-transition, recovery, or other journal rows as if they were adapter output.

`--jsonl` writes one UTF-8 JSON object followed by `\n` for each such observation, and writes no
prose to stdout. Each object has the required literal `schema` value
`partitur/logs+jsonl;v=1`; it is a normalized event envelope, not the verbatim on-disk journal
line. A compatible producer may add fields but must not change the meaning or type of a v=1 field;
a consumer must ignore unknown fields. Every v=1 object has `schema`, `run_id`, `seq`, `ts`,
`type`, and `message`; `type` is `log` or `progress`, and `level` is present only when `type` is
`log`. `run_id`, `seq`, and `ts` identify the durable source observation, while `message` and
`level` are its already-sanitized B.7 payload. Thus the journal's storage-only fields and layout
remain private while clients can order, de-duplicate, and display the observation stream.

Without `--jsonl`, `logs` renders one deterministic line per observation: a `LOG <level>:` line or
a `PROGRESS:` line, each prefixed by its sequence and timestamp. Those labels describe the
observation kind only; they do not imply a movement, attempt, or run state transition. v0.2 has no
logs filtering flag: every durable `log` and `progress` row is included, and the separate `status`
surface remains the way to inspect authoritative state.

With `--follow`, `logs` first writes the complete current observation history, then waits for
newly durable observations. It stops successfully as soon as a read-only replay observes the
selected run in terminal `SUCCEEDED`, `FAILED`, or `CANCELLED`; therefore a run already terminal
at startup streams its history and exits rather than blocking. Before that point it continues until
the terminal lifecycle is observed or the caller sends SIGINT, which stops following successfully
after any observations already written. A torn final journal line is not an observation: `logs`
emits only the valid prefix, neither repairs nor synthesizes a row for the tail, and returns to
waiting when following. If that valid prefix is terminal, the terminal rule still ends the command;
otherwise a later driver or `resume` repair may make a new durable observation visible. An
unparseable line before the final line remains journal corruption and no current stream can be
produced.

**Observation outcome.** `status` exits on the success of the observation, never on the health of
what it observed. It returns 0 whenever it produced and reported a projection, including RUNNING,
SUCCEEDED, FAILED, CANCELLED, Application or Promotion `RECOVERY_REQUIRED`, and a journal whose
only defect is an unparseable final line. Those facts are data: callers read `run.lifecycle`,
`application.state`, `promotion.state`, and `journal.integrity`, so `status --json | jq ...` remains
usable under `set -e`. A genuine Application or Promotion `RECOVERY_REQUIRED` therefore retains its
authoritative `recovery.state: RECOVERY_REQUIRED` and cause while `status` remains observational.

An unparseable final line is reported as `journal.integrity: TAIL_UNPARSEABLE`, with its would-be
sequence and discarded byte count, but does **not** manufacture `recovery.state: RECOVERY_REQUIRED`.
Appendix C classifies that tail as safe automatic repair: a later `resume` truncates it, appends
`journal.tail_truncated`, and re-evaluates without operator intervention. A subsequent status then
reports the normal post-repair projection. An unparseable line anywhere before the final line is
different corruption: no projection can be built.

The status-specific exit mapping is exhaustive: 0 for a reported projection; 1 for usage; 2 for a
refused selection or required readable input, including no active run, a non-unique active-run set,
or an unreadable pinned snapshot; and 5 only when the projection cannot be built, including a
corrupt journal prefix or an event the core cannot project. `status` never returns 3, 4, or 6: it
neither validates nor drives a run.

The logs-specific exit mapping is likewise exhaustive: 0 for a produced observation stream,
including an empty history, a RUNNING or terminal run, a SIGINT-ended follow, and a journal whose
only defect is an unparseable final line; 1 for usage; 2 for a refused selection, required
readable input, or unwritable output stream; and 5 when the stream cannot be produced because of a
corrupt journal prefix or an event the core cannot project. `logs` never returns 3, 4, or 6: it
neither validates nor drives a run.

For both `status` and `logs`, a closed or broken consumer pipe ends output successfully and emits
no stderr diagnostic. Any other stdout write failure is an unwritable output stream and is a
precondition refusal, not a recovery halt.

**`partitur run` observable surface.** Before a run exists, `run` uses §2's score-input and
score-compilation rules, §3's cast resolution, and the score/cast diagnostic ordering and
suppression of `validate`'s contract above — all by reference. It does **not** perform validation's
adapter preflight: §4's run-attempt probe is the observation made by the gated peer and owns every
adapter failure after run creation. The additional Git repository and clean-source preconditions
are §5's. A refusal before `run.started` exits 2; a score, cast, or §5 run-start validation
diagnostic exits 3. Neither case creates a run or writes stdout.

Once `run.started` is fsynced, `run` attempts to write the allocated UUIDv7 as exactly one UTF-8
line `<run-id>\n` to stdout, with no label or surrounding whitespace. It never attempts that write
before the durability receipt: §1 makes a pre-event directory orphan state, not a run. A successful
write occurs exactly once even when the run later fails, recovery halts, or this invocation is
operationally interrupted, so a caller then has the handle for `status` and `logs` once durable run
state exists.

The id write is also the admission boundary for execution: `run` acquires no driver lease and
performs no driver-authorized mutation or launch until the write succeeds. If the write fails, the
run exists but this invocation stops before acquiring authority, attempts one core-generated stderr
diagnostic that names the allocated `run_id`, and exits with the operational-interruption code
below. The failed stdout write is not retried or redirected to stderr as a second machine-readable
stream. The explicit id in the diagnostic preserves a recovery handle when stderr remains writable;
when it does not, `status` or `resume` without an id can still select the run if it is the unique
active one under the command-selection rule above.

After that line, stdout is silent. Core-generated usage, refusal, validation, terminal-outcome, and
recovery-halt diagnostics go to stderr; raw adapter/vendor stderr remains only in the sanitized
attempt file (§4) and is never mirrored by `run`. A terminal `FAILED` or `CANCELLED` run writes one
stderr diagnostic naming the projected terminal state and, when the authoritative terminal event
has one, its reason. A recovery halt writes one stderr diagnostic naming its Appendix D halt reason.
An operational interruption after a successful id write writes one stderr diagnostic stating that
the run remains nonterminal and is continued with `partitur resume <run-id>`; it does not manufacture
a journal reason or recovery-halt reason for an event that never occurred.
Successful `SUCCEEDED` and quiescent `WAITING_HUMAN` returns add no stderr summary: their structured
state and pending decisions are read with `partitur status <run-id> --json`.

An **operational interruption** is an invocation outcome, not a run lifecycle state: after
`run.started`, this invocation cannot safely continue, no terminal event has been appended, and the
condition is not an Appendix D recovery halt. The command leaves the run at its last durable
projection and starts no further driver-authorized work. It must still perform every cleanup whose
ordering is already required by §4 and §6; in particular, this outcome cannot replace a
verified-empty sweep, and an unverifiable sweep remains the `sweep_unverifiable` halt. A matching
lease that cannot be removed before the process exits is handled as a dead-owner state by C.1; its
presence does not authorize the failing invocation to recover in place.

`partitur resume` is the sole continuation from this outcome. It applies Appendix C from the last
durable state and acquires a driver lease only when C.1 and §6 authorize a new owner and authority
epoch. `run` does not implement a parallel in-process recovery sequence, reuse an unleased
`authority.granted` epoch, or turn an interruption into a false terminal or halt claim. If the
`resume` invocation is itself operationally interrupted while the run remains nonterminal, it
returns the same exit 6 and may be invoked again; it does not allocate or print another run id,
because its explicit or uniquely selected id already names the existing run.

The command's exit codes are exhaustive over its specified outcomes:

| Code | `partitur run` outcome |
|---|---|
| 0 | The run reached `SUCCEEDED`, or quiesced in `WAITING_HUMAN` with no adapter or criterion process left running |
| 1 | Usage error; no run exists and stdout is empty |
| 2 | Precondition refused before `run.started`; no run exists and stdout is empty |
| 3 | Pre-run validation failed before `run.started`; no run exists and stdout is empty |
| 4 | The durable run reached terminal `FAILED` or `CANCELLED`; the id was already written |
| 5 | Recovery halted for an Appendix D reason; the id was already written and the run remains at its last durable state |
| 6 | This invocation was operationally interrupted after `run.started`; the run remains nonterminal and resumable. The id was already written except when that single stdout write was itself the interruption |

v0.2 defines no `run --json` or `run --jsonl`, live progress rendering, TTY-specific mode, spinner,
colour, or human-oriented status stream. Adapter `log` and `progress` notifications remain the
journal observations §4/B.7 define; `partitur logs <run-id> --jsonl [--follow]` is their stream, and
`partitur status <run-id> --json` is the structured state surface. The bare stdout id is a
correlation handle, not a second state or event stream.

**`partitur resume` observable surface.** After run selection under the generic `[<run-id>]` rule
above, `resume` takes its historical authority from the selected run's journal and the run-owned
durable inputs and records that §1, §4, §6, and Appendix C require for its projected state. It does
not recompile the current root `partitur.yaml` as recovery input, repeat cast layering from current
project, user-global, or factory inputs, or infer missing historical decisions from current
configuration. §1's `root_snapshot_divergence` observation is the sole exception: `resume` reads and
hashes the current root only to establish that halt condition, and discards the result rather than
admitting it as recovery input.
If Appendix C authorizes new execution, the existing attempt-time adapter resolution and probe rules
still apply; they do not replace the persisted score revision or resolved cast.

`resume` writes no bytes to stdout on any outcome. The selected run id remains the correlation
handle; `status --json` and `logs --jsonl` remain the structured state and event surfaces. For
usage, refusal, projected success, quiescence, and terminal failure, `resume` uses the `run` stderr
policy above by reference. On a recovery halt it writes exactly one core-generated stderr diagnostic
naming the selected run id and the exact Appendix D halt reason. Because B.7 makes a recovery halt a
condition, not a journal event, `status --json` does not gain a persistent halt result from the
invocation; where the run remains readable, it continues to describe the last durable projection
rather than reconstructing that stderr diagnostic.

The following table is exhaustive for `resume`; the `partitur run` table above remains scoped only
to the creating command:

| Code | `partitur resume` outcome |
|---|---|
| 0 | The selected run reached or was already at `SUCCEEDED`, or reached or was already quiescent in `WAITING_HUMAN` with no adapter or criterion process left running |
| 1 | Usage error |
| 2 | Run selection or another command precondition was refused under the global exit-code table below |
| 3 | Not used: `resume` performs neither pre-run validation nor amendment rejection |
| 4 | The selected run reached or was already at terminal `FAILED` or `CANCELLED` |
| 5 | Recovery halted for the Appendix D reason reported on stderr; the run remains at its last durable projection |
| 6 | The operational-interruption outcome already defined for `resume` above |

An already-terminal run is therefore not a wrong-projection-state refusal. Treating its durable
outcome idempotently closes the normal race in which a caller repeats `resume` after exit 6 but
another invocation completed the run first; `SUCCEEDED` returns 0, while `FAILED` or `CANCELLED`
returns 4.

Where C.1 yields to a verified live owner (`RC-RESUME-046`), `resume` has nothing left to do: the
owner already holds continuation authority, and this invocation may neither reclaim it nor run
beside it. That is a refused command precondition and returns 2. The row states the yield and this
paragraph states the mapping, because the same yield leads `cancel` somewhere else entirely.

**`partitur cancel` observable surface.** `cancel` first records its durable run-scoped request as
§6 requires, then waits, bounded by §6's acknowledgement deadline, for the terminal event that is
also its acknowledgement. A recovery row selects the action from the durable state; §7 maps the
outcome of the invoking command after that action. In particular, a verified live owner is yielded
to rather than refused: the request is already durable, and the responsive driver is the canceller
that will terminalize it. Only an operational interruption of this `cancel` invocation while the
run remains nonterminal returns 6.

An already-terminal run is not a wrong-projection-state refusal for `cancel` either, and for the
same reason it is not one for `resume`: the caller who repeats `cancel` after an interrupted
invocation is racing whichever canceller finished first, and reporting the run's durable outcome
closes that race idempotently. 2.1b makes the race ordinary rather than rare, because `cancel` now
waits on a live driver and can be interrupted while waiting. The mapping is therefore `resume`'s:
`SUCCEEDED` returns 0, `FAILED` or `CANCELLED` returns 4. A `SUCCEEDED` run reports 0 even though
nothing was cancelled — the code reports the run's durable outcome, not whether this invocation
caused it, and `status --json` remains the surface that says which outcome it was.

The following table is exhaustive for `cancel`:

| Code | `partitur cancel` outcome |
|---|---|
| 0 | The selected run was already at `SUCCEEDED` |
| 1 | Usage error |
| 2 | Run selection or another command precondition was refused under the global exit-code table below |
| 3 | Not used: `cancel` performs neither validation nor amendment rejection |
| 4 | The run reached, or was already at, terminal `FAILED` or `CANCELLED`; where this invocation made the request, the `run.cancelled` it waited for is also its acknowledgement |
| 5 | Recovery halted for the Appendix D reason reported on stderr; the run remains at its last durable projection |
| 6 | This `cancel` invocation was operationally interrupted while the run remains nonterminal |

`validate` acquires inputs before interpreting their contents. A missing or unreadable required
`partitur.yaml`, or a discovered cast file that cannot be read, is a refused precondition and exits
2. A missing optional project or user-global cast layer is simply absent and produces no
diagnostic. Once an input has been read successfully, malformed or defective score or cast content
is a validation diagnostic and exits 3. The asymmetry is deliberate: cast layers are optional and
no layers resolve to a valid empty cast; only when an existing score declares parts does that empty
cast produce `binding_missing`, which is a validation result about content that exists.

**Exit codes** — stable categories, so a script can branch without parsing prose:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | usage error: unknown command, missing or malformed operand |
| 2 | precondition refused: missing or unreadable required input, unreadable discovered input, no active run, wrong projection state, dirty checkout, lock held, unwritable output stream |
| 3 | validation failed: `partitur validate`, pre-run validation for `partitur run`, or a rejected amendment |
| 4 | a run-driving command reached terminal `FAILED` or `CANCELLED` |
| 5 | recovery halt — the run cannot proceed and needs an operator (Appendix D) |
| 6 | post-creation operational interruption — the run remains nonterminal and resumable |

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
  `overridden_findings` is non-empty the event **must** carry a non-empty
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
# The candidate additionally records candidate_composition_dependency_hash (A.4). With one or
# more contributors it binds the composition ENVIRONMENT — the same trees composed under a
# different Git build or merge configuration are not the same composition (§5). With zero
# contributors its tagged identity projection records that no merge ran and binds no fictitious
# environment.
candidate_id = H("partitur/candidate",
                 { base_tree, result_tree, ordered_change_sets })      # Appendix A
```

It is a **content** identity, independent of score revision. A run with no write movements
records `result_tree == base_tree`, `contributors == []`, and
`candidate_composition_dependency_hash` over A.4's `composition_mode: identity` projection.
No merge executes and no `composition_environment_hash` exists on that branch. A composition
that conflicts or cannot be executed is handled **before** recording, and terminally: it fails
the run per §5 — never discovered at `apply` time.

Materialization is always **one authoritative journal event**, never a batch that could
tear on crash:

- **Non-waived:** the core composes the candidate after every `repo_write` movement has
  succeeded and before the final movement becomes READY.
  `application_candidate.recorded` itself constitutes the initial binding — no separate binding
  event is appended for it. It carries the candidate id, both trees, the ordered contributing
  change sets, and the `candidate_composition_dependency_hash`. The **revision it binds to is the
  event's envelope `score_revision`**, not a payload field: duplicating an envelope field is a
  defect (B.0), and only one revision is in play at materialization.
- **Waived:** materialization is deferred until every non-draft movement has succeeded and
  is folded into a single `run.succeeded` event carrying the full candidate payload and
  binding. An active waived run therefore never holds a recorded candidate.
- The binding fact `{candidate_id, score_revision}` — the revision taken from the recording
  event's envelope — is **always a projection**, never its own authoritative event — initially from `application_candidate.recorded`, and thereafter
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

After §4's execute-completion boundary the core applies the full read-only post-hoc invariant of
§5 — tracked content, non-ignored untracked files, symlink targets, modes, protected paths — and
additionally verifies that the worktree tree equals the recorded candidate `result_tree`.
`subject_tree` equality alone would miss an untracked file or a mode change that a later step could
observe. Any mismatch is `candidate_mismatch`, a `grant_denied` sub-reason (Appendix D) — an
unauthorized write to the tree under verification — failing the attempt with a kind §3.1 classes
immediately terminal. If the core's own composition disagrees with what it recorded, that is
internal corruption handled by recovery, never attributed to the performer.

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

1. **Run lifecycle** — the run must be `RUNNING` or `WAITING_HUMAN` **and have no pending
   `cancel.requested`**; a terminal run rejects with `run_terminal`, and a cancelling one with
   `run_cancelling`. Nonterminality alone is insufficient: a cancellation is durable but not yet
   terminal, so without this an amendment could be admitted after the barrier lifted and before
   `run.cancelled` landed. Score changes after a run ends happen by editing the root score or
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

Passing this pipeline and the approval policy establishes intent; it does not by itself authorize
`amendment.approval_prepared`. §6's durable-consequence closure barrier runs before preparation and
then requires this pipeline and the policy decision to be re-established against the projection
that closure produced. The barrier adds no amendment class or rejection reason, but closure may
change the state read by an existing guard — for example, materializing a recorded retry can make
`runtime_scope_started` true. That is the intended precedence: an approval reservation cannot
overtake a consequence the journal has already fixed merely because the earlier observation would
have passed.

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

The projection is defined **exhaustively** in Appendix A.5 — "includes at least" is not an
identity definition. Two of its exclusions carry real consequences. `policy.budget` and
`policy.amendment` are excluded as **scheduling policy** for an ordinary attempt, so a permitted
`BUDGET_DECREASE` cannot invalidate a succeeded one — but they *do* reach a `may_propose`
performer inside the score base, where `score_base_hash` covers them transitively (A.5).
Effective authority is hashed rather than raw `allowed_paths`, so a `NARROW_PATHS` removal
irrelevant to a movement does not block its own approval.

**A `may_propose` attempt is therefore invalidated by every amendment**, since its dependency
includes the whole score. That is intended, not an accident of the projection: a performer that was
handed the complete score to patch reasoned about all of it. It is keyed by the adapter that actually served the attempt (primary or
fallback), so changing `extensions.<fallback>` after a fallback attempt succeeded is detected.
It excludes per-attempt values: runtime identity, filesystem paths, `session_hint`, and remaining
budget. **`request.feedback` is included**, not excluded: delivered feedback changes the rendered
brief (§7), so it changes what the attempt was asked to do. A.5's exhaustive projection governs.

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
     execution_dependency_hash under the patched score, and none is removed. Because that
     projection carries base_composition_hash (A.5), a change to how a succeeded movement's
     clean base was assembled is caught by this same condition — including an
     `identity` → `merge` transition when the first contributing writer enters a previously
     zero-contributor fan-in;
  2. the candidate composition identity is unchanged —
       candidate_composition_dependency_hash =
         H("partitur/candidate-composition", candidate_composition_projection)
                                                       # A.4 tagged identity | merge union
     must equal the hash recorded with the candidate. Changes altering the composition —
     movement order, `needs`, contributor membership — are incompatible even if the
     resulting tree would coincidentally be identical. In particular, adding the first writer
     to a zero-writer candidate changes `composition_mode: identity` to
     `composition_mode: merge` and is incompatible even when that writer's change set is a no-op
     and `result_tree` remains equal to `base_tree`; removing the last writer is the inverse
     incompatible transition;
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
  and letting it complete would attach evidence to a revision that no longer exists.
- **Termination precedes the approval, not the reverse.** The quiesce handshake of §6 drains or
  fences the driver *before* `amendment.approved` is appended, because that event is the single
  transition and cannot be appended twice. So the ordering is: prepare → the driver quiesces or the
  deadline passes and it is fenced → approve. Time until actual termination is charged against the
  active budget by the interval close that quiescing performs.
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
- Revision-triggered restarts consume no quality retry — §3.1 places them outside the oracle.

**Journal taxonomy.** Routing is not an outcome, and neither is preparation.
`amendment.approval_prepared` is the durable control request of §6 and changes no score or lifecycle
projection whatsoever. So the single-transition rule is unchanged by the quiesce handshake —
`amendment.approved {mode: auto | human}` is still the **only** authoritative transition, carrying
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
is recorded only once a validated patched AST exists — that is, once step 6 has **passed**, so it
is available for failures at step 7 and later. Steps 1–6, `invalid_score` included, record the
patch-operations hash and the error location instead.

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

**Snapshot bytes are canonical, not pretty-printed.** An amendment operates on canonical JSON but
persists a YAML snapshot whose exact bytes later become the `promote-score` target (§8), so a
non-deterministic emitter would make the promotion CAS depend on formatting luck. Amended snapshots
are therefore emitted by a **fixed deterministic serializer**: keys in canonical order (A.1), two-space
indentation, block scalars for any string containing a newline, double-quoted scalars only where YAML
requires quoting, no flow collections, no anchors or aliases, LF endings, one trailing newline. The
*root* score is never reformatted by the core — only `promote-score` writes it, and it writes the
snapshot's exact bytes.

**The plan is the invariant part plus an enumerated overlay.** `prepares/<prepare-id>.json` cannot
literally contain the whole approval, because `fenced_epoch` is decided at commit — so it carries
`{schema: "partitur/approval-plan+json;v=1", …}` with every field fixed at prepare time, and commit
adds exactly one enumerated overlay from its outcome table — and **both its presence and its value are
bound**, so the overlay carries no freedom at all:

```text
present   iff this transition advances the authority epoch
value     fenced_epoch = observed_authority_epoch + 1
          (precondition: the current epoch still equals observed_authority_epoch, which
           commit revalidates; otherwise the prepare is invalid and is abandoned)
```

"Advance the epoch" alone would have left the number unbound, which is a second degree of freedom
masquerading as one. With both fixed, "recovery replays the plan" is literally true: recovery
reconstructs the same bytes the original approver would have written. The record is
written temp → fsync → rename → directory-fsync, like every other authoritative sidecar (§1).

**Snapshot lifecycle.** Because a snapshot is now written *before* its approval (§6), "a snapshot with
no `amendment.approved`" is no longer sufficient to condemn it. A prewritten snapshot occupies the
immutable path `scores/revision-<base+1>.yaml`, so leaving an abandoned one there would collide with the
next attempt at the same revision, which may legitimately need different bytes. Four states, and
recovery decides by which events reference it:

| State | Recognised by | Action |
|---|---|---|
| Unreferenced pre-prepare | no `approval_prepared` names it | quarantine — a crash before the prepare |
| *(plan records follow the same states)* | an orphan `prepares/<prepare-id>.json` with no `approval_prepared`, or one whose prepare is already terminal | **removed on sight.** A plan authorizes nothing by itself — only a pending prepare gives it force — so an orphan is inert and needs no quarantine, unlike a snapshot that occupies an immutable path |
| Pending prepare | a pending `approval_prepared` names it | **retain**, and verify both hashes before commit |
| Approved | `amendment.approved` names it | authoritative snapshot |
| Abandoned | `approval_abandoned` closed the prepare that named it | quarantine **before appending that event**, since appending it is what lifts the barrier. A crash between the two would leave the barrier lifted with the immutable revision path still occupied, and a retry needing different bytes at that same path would have nowhere to write |

**Persistence.** The approved snapshot is written temp → fsync → atomic rename; then the
single `amendment.approved` event is appended and fsynced — head change and logical supersede
become visible together as one replayable transition — and only then is the manifest
projection updated. An **amendment** snapshot with no corresponding `amendment.approved` event is quarantined at recovery
and never becomes head — unless a pending `approval_prepared` names it, which is the one legitimate case
for such a snapshot to exist (snapshot lifecycle above). The rule is scoped to amendment snapshots
deliberately: the run's **initial** snapshot is authorized by `run.started`, not by any approval, and
read unscoped this rule would condemn it. An `amendment.approved` event with no
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
  aliases, merge-tagged keys, custom tags, and every resolved scalar tag other than `!!str`,
  `!!bool`, `!!null`, `!!int`, or `!!float`. A quoted or explicitly `!!str`-tagged `<<` is an
  ordinary string key and remains legal. It validates numeric scalars as finite representable
  binary64 values. These are `yamlsafe` decode errors: a general-purpose YAML parser builds its
  representation graph first and need not fail on them itself, so "rejected at parse time" would
  be unimplementable as literally stated.

  YAML 1.2 resolves a sexagesimal-looking plain scalar such as `12:34:56` as a **string**, not a
  numeric tag, so tag filtering alone does not catch it. The rejected form is specifically **YAML
  1.1's base-60 integer shape** — `[-+]?[1-9][0-9_]*(:[0-5]?[0-9])+` — and not any string containing
  a colon: `12:99` and `0:00` are ordinary strings and stay legal. Naming the shape matters, because
  "sexagesimal-looking" read literally would reject ordinary data. Underscores are legal only in
  the first component, so `1_2:34` is rejected while `12:3_4` remains an ordinary string.

  **Plain timestamps are an additional Partitur lexical restriction, not an inherited resolver
  choice.** An unquoted scalar is rejected when it is a calendar-valid `YYYY-M-D` date, or that
  date followed by either `T`/`t` plus `HH:M:S[.fraction]` and `Z` or a `±HH:MM` offset, or a
  space plus `HH:M:S[.fraction]` without an offset. This rule is applied independently of the tag
  assigned by the YAML library. Timestamp-shaped string data must therefore be quoted or explicitly
  tagged `!!str`.

  **Tag filtering alone is insufficient for range errors, too.** A YAML implementation may resolve an
  overflowing plain scalar such as `1e9999` to `!!str` *after* its own binary64 conversion fails, so
  it arrives as a legal string rather than a rejected number. `yamlsafe` therefore additionally range-
  checks plain scalars that *look* numeric, rather than trusting the resolved tag.

  **Collection tags** are filtered on the same principle as scalars: explicit `!!seq` and `!!map` are
  permitted because they denote exactly what the JSON AST already has, and every other collection tag —
  `!!set`, `!!omap`, `!!pairs` — is rejected, since none has a JSON counterpart.

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
  | `outputs` | **declaration order preserved** | It controls generated-check order (§7) |
  | `inputs` | sorted by `artifact_id` | A set; the brief renders them but order carries no meaning |
  | `open_questions` | sorted by id | A set keyed by id |
  | `bindings[].fallbacks` (cast) | **order preserved** | It *is* the fallback chain (§3) |
  | `apply_gate.require`, `apply_gate.predicates` | sorted, duplicate-free | Declared duplicate-free subsets (§2) |
  | `side_effects` | sorted, duplicate-free | A set; empty in v0.2 |
  | `contributors`, `ordered_change_sets` | order preserved | Composition order (§5, §8) |
  | `coverage`, `findings` (findings artifact) | sorted by rubric / by finding id | Sets keyed by id |

  The rule for anything not listed: **if two orderings would mean the same thing to the core, sort;
  otherwise preserve.** A collection whose order is meaningful and is nonetheless sorted is a silent
  identity collision, which is the failure this table exists to prevent.

  Correspondingly, typed comparison (§9) matches movement *content* by id but records a
  distinct collection-order change when the movement sequence differs — so a pure reorder is
  neither invisible to the hash nor misattributed to a content edit.

**The spike code under `spikes/` is evidence, not a skeleton.** It was written to answer the
questions below and predates several rules this document later settled — most sharply, the lease
spike persists the incarnation token in `authority.json` and treats that file as authority, which is
the model §6 explicitly rejects. Its `yamlsafe` also rejects tags this document allows, its fan-in
uses bare Git OIDs rather than object-format-qualified ids and versioned domain hashes, and its
Darwin enumerator uses a fixed unchecked buffer where production must resize, retry, or fail closed.
Reading it as a reference implementation would reintroduce every one of those.

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
that attempt**, not the current one. It dispatches the projector identified by
`(domain, projection_version)` and hashes that projector's result; no generic API may pair a
caller-supplied projection AST with a historical version label. If the running binary cannot
dispatch either the recorded projector or canonical encoder, the run fails closed with
`unsupported_run_format` rather than silently comparing hashes computed under different rules.

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

Complete. Every canonical-AST identity in Partitur is in this table; a hash of anything else is
a defect.

| Domain | Projection |
|---|---|
| `partitur/score` | The whole validated score AST after defaults. Used for `score_hash`, `base_hash`, and snapshot identity. |
| `partitur/score-subtree` | `{selector, value}` — the stable selector of §9 **is** part of the preimage, alongside the subtree value. Two different pointers holding equal values are different facts, so hashing the value alone would let an impact entry claim the wrong location. Used for `before_hash` / `after_hash` in `actual_impact`. |
| `partitur/resolved-cast` | The fully resolved cast AST after layering (§1), with `performers` and `bindings` sorted by id and every effective default materialized. Used for `resolved_cast_hash` in `run.started`. |
| `partitur/criterion-spec` | A.4.1 — a tagged union over criterion kinds. |
| `partitur/acceptance-spec` | A.4.2 — the effective compiled acceptance plan. |
| `partitur/change-set` | `{base_tree, result_tree}` |
| `partitur/candidate` | `{base_tree, result_tree, ordered_change_sets: [change_set_id]}` — the **content-deduplicated applied** sequence (§8). |
| `partitur/candidate-composition` | Tagged union: zero contributors ⇒ `{composition_mode: "identity", base_tree, contributors: [], composition_algorithm_version}`; one or more contributors ⇒ `{composition_mode: "merge", base_tree, contributors: [{movement_id, change_set_id}], composition_algorithm_version, composition_environment_hash}` with `contributors` non-empty. The tag and cardinality must agree. `identity` states that no merge ran, so an environment hash is forbidden rather than fabricated; `merge` records the **full pre-dedup ordered** sequence, so adding, removing, or reordering an identical or no-op writer stays detectable. |
| `partitur/movement-composition` | The same tagged union as `partitur/candidate-composition`, with `movement_id` added to both variants: zero contributors ⇒ `{composition_mode: "identity", movement_id, base_tree, contributors: [], composition_algorithm_version}`; one or more ⇒ `{composition_mode: "merge", movement_id, base_tree, contributors: [{movement_id, change_set_id}], composition_algorithm_version, composition_environment_hash}` with `contributors` non-empty. This is a movement's own fan-in (§5), including the reachable case where it has `needs` but every dependency is read-only. The candidate-level hash covers only the final composition, so without this a movement's clean base has no identity and a change to how it was assembled would be invisible. |
| `partitur/composition-environment` | §5, exactly: `{git_version_string, object_format, argv, env, merge_renormalize, merge_config}` where `git_version_string` is `git --version` verbatim, `argv` is the full merge argv as executed, `env` is the **allowlisted** subset the core passes (sorted key/value pairs), and `merge_config` is every `merge.*` key effective in the composition repository, sorted. Separate because both `composition_mode: merge` identities need it, and because the environment can change while every tree stays the same. It is absent from `composition_mode: identity`, where no merge invocation exists to describe. |
| `partitur/composition-subject` | `{scope, target_id, contributors: [change_set_id], composition_algorithm_version, composition_environment_hash}` — identifies *which* composition failed to yield a usable result, and is the third component of both non-success evidence keys — `composition.conflicted` and `composition.failed` (B.3). It must include the algorithm and environment because the payloads do: the same contributors failing under a different Git configuration is a different fact, and omitting them would let one idempotency key acquire two differing payloads. Distinct from the two dependency hashes: those identify a **successful** composition's inputs, this identifies a composition that produced none. |
| `partitur/execution-dependency` | A.5 — exhaustive. |
| `partitur/patch-operations` | The raw RFC 6902 operations array **as the proposer wrote it**, order preserved, for pre-validation rejection records (§9). |
| `partitur/resolution-body` | `{kind, answer}` for an answered question, or `{kind, reason}` for an amendment rejection — the resolution's semantic content and nothing else. Used for `resolved_decisions[].digest` in A.5, so an answer's length cannot bloat the projection while its *content* still binds. |

`composition_algorithm_version` remains in the `identity` variant because the algorithm owns
contributor selection, ordering, and content deduplication — including the conclusion that the
contributor list is empty. It binds that rule without claiming that a merge subprocess ran.

The two modes are disjoint facts, not sentinel values: `identity` requires exactly zero
contributors and forbids `composition_environment_hash`; `merge` requires at least one contributor
and requires it. `partitur/composition-subject` has no `identity` variant because a zero-contributor
composition completes as the identity result without attempting a merge and therefore cannot yield
`composition.conflicted` or `composition.failed`. Every composition subject consequently describes
an attempted `merge` branch and retains its non-empty contributor list and environment hash.

### A.4.3 Scoped identifiers — derived, but not content identities

`decision_id`, `proposal_id`, and `gate_id` are run-unique names. They are **derived where
something exists to derive from, and allocated otherwise** — never `H()`, and never version-bound:

```text
decision_id = "dec-" ‖ hex(sha256("partitur/decision-id" ‖ 0x00 ‖ JCS({attempt_id, emitted_id})))
proposal_id = "prp-" ‖ hex(sha256("partitur/proposal-id" ‖ 0x00 ‖ JCS({attempt_id, emitted_id})))
gate_id     = "gat-" ‖ hex(sha256("partitur/gate-id"     ‖ 0x00 ‖ JCS({attempt_id})))
```

`hex` is **lowercase, unpadded, full 64 characters**. The short prefix makes the kind legible in a
journal or a CLI argument and keeps the value JSON- and path-safe; raw digest bytes would be
neither. These are *not* written `sha256:…`, because that form is reserved for content hashes
(A.4) and these are names, not content identities.

| Identity | Origin | Rule |
|---|---|---|
| `decision_id`, `proposal_id` | adapter `question` / `proposal` | derived above from `(attempt_id, emitted_id)` |
| `gate_id` | a human gate | derived above from `{attempt_id}` — a movement declares at most one `human_gate` |
| `decision_id` | a human gate, routing a CLI amendment, or the reserved finalization amendment (§2) | **allocated** UUIDv7 |
| `proposal_id` | `partitur amend`, or the reserved finalization amendment (§2) | **allocated** UUIDv7 |
| `txn_id`, `interval_id`, `run_id`, `attempt_id` | core | **allocated** UUIDv7 |

Note these use **raw SHA-256 over a domain-prefixed JCS encoding**, deliberately *not* the
version-bound `H()` of A.2. They are also **not** in the registry above and **not** subject to
`identity_versions`, because they are not content identities. Nothing ever recomputes one in order to *compare* it
against a stored value — the way `execution_dependency_hash` is recomputed to detect a changed
dependency. They are opaque run-unique names, looked up rather than verified.

Derivation is chosen over allocation for one reason: **idempotency**. If the core processes the
same emitted `question` twice — a retry, a replayed frame — derivation collapses both to one
`decision_id`, whereas an allocated id would create a second decision for the same question.

An identity that is never recomputed for comparison needs no version binding; requiring it would
put `identity_versions` on nearly every event in B.4, B.1, and B.2 to protect a comparison that
never happens.

**Not canonical-AST identities.** These are different *kinds* of identity and are never mixed
with the above — a category error here silently aliases unrelated objects:

| Identity | Form | Note |
|---|---|---|
| Artifact instance content | `sha256:<hex>` over raw file bytes (§1) | Not an AST; the bytes are the artifact. |
| Score **file** content | `sha256:<hex>` over raw file bytes (§1) | `score_file_hash`, for the promotion CAS. Deliberately distinct from `partitur/score`. |
| Git trees and commits | `git-sha1:<hex>` / `git-sha256:<hex>` | **Must** carry the object format. A bare hex string would alias two different objects across a repository-format migration, and the prefix makes the mistake impossible to make silently. |

### A.4.1 `partitur/criterion-spec`

A tagged union — a flat bag of optional fields would let two different criterion kinds collide on
the same hash:

```text
{kind: "hard.run",      id, run: [argv...], timeout_min?}
{kind: "hard.artifact", id, artifact: <logical_output_id>, expected_hash?}
{kind: "review",        id, findings: <logical_output_id>, rubric: [rubric_id]}   # rubric sorted
```

`kind` values are closed (Appendix D). No positional or presentational field participates: not the
criterion's index, not its source line. A core-generated integrity check (§7) projects as
`hard.artifact` with its reserved id.

### A.4.2 `partitur/acceptance-spec`

The **effective compiled plan**, not the acceptance block as written — a mark must bind to what
actually ran (§7):

```text
{
  hard:   [criterion_spec_hash],   # declared hard criteria in DECLARATION order, replacements
                                   #   applied, then core-generated integrity checks in output
                                   #   declaration order
  review: [criterion_spec_hash],   # declared review criteria in declaration order
  human_gate: "always" | "on_contested" | "never"    # always explicit, never omitted
}
```

Hashing criterion *hashes* rather than inlining their bodies keeps a criterion's identity stable
when a sibling changes, which is what lets `criterion_spec_hash` stand alone as evidence.

## A.5 The execution-dependency projection

**Exhaustive.** A field absent from this projection is absent from the hash, and adding one is a
`projection_version` bump. The governing question for every field is: *would changing it change
what this attempt was asked to do?* If yes it is in; if it merely varies between attempts of the
same request, it is out.

Note the projection is **score-and-performer derived**, not purely score-derived: `extensions`
comes from the resolved cast, and the adapter id is the one that actually served the attempt.
The core computes it after the gated peer answers `probe`, when it fixes the exact request including
bounded `resolved_decisions` delivery, and records it in `adapter.probed` before sending `execute`.

```text
{
  actual_adapter_id,                  # the adapter that SERVED this attempt (§1 per-attempt
                                      #   record) — never the part's intended binding, so
                                      #   changing extensions.<fallback> after a fallback
                                      #   attempt succeeded is detected
  movement: {
    id,
    part,                             # the part id
    instruction,
    needs:      [movement_id],        # sorted — a set
    inputs: [{artifact_id, kind, instance_id, content_hash}],
                                      # sorted by artifact_id. `kind` is included because
                                      #   RenderPrompt shows it; `instance_id` and `content_hash`
                                      #   because two attempts can legitimately receive DIFFERENT
                                      #   bytes for the same logical input (an upstream retry
                                      #   produced a new instance). Binding only the logical id
                                      #   would let those two attempts share a dependency hash
    outputs:    [{artifact_id, kind}],# DECLARATION order — it controls generated-check
                                      #   order (§7), so it is semantic
    grants:     [grant],              # sorted — the movement's declared grants
    may_propose: bool,                # effective value including the implicit default
    score_base_hash?,                 # = H("partitur/score", complete validated score AST) —
                                      #   the score-base input's SEMANTIC base_hash, never the
                                      #   raw file hash of the delivered artifact.
                                      #   REQUIRED iff may_propose is true, omitted otherwise.
                                      #   A may_propose attempt receives the COMPLETE score
                                      #   base (§4), so the whole score is one of its
                                      #   execution dependencies and any later amendment
                                      #   invalidates it (§2). The boolean alone would not
                                      #   capture that — two attempts with may_propose true
                                      #   under different scores must not share a hash
    phase:      "draft" | omitted,
    acceptance: acceptance_spec_hash,  # the effective compiled plan (A.4.2)
    base_composition_hash?             # movement_composition_dependency_hash (A.4), REQUIRED
                                       #   iff the movement has ≥1 `needs` — how its clean base
                                       #   was assembled. `needs` with zero contributing change
                                       #   sets uses the tagged `identity` projection. Without
                                       #   the hash, changing contributor
                                       #   membership, order, or the composition environment
                                       #   would leave a succeeded attempt looking valid (§5)
  },
  part: {
    capabilities: [capability],       # sorted
    read_only:    bool
  },
  model,                              # the model requested of the performer — part of what
                                      #   the attempt was asked to do, and this projection is
                                      #   performer-derived, not purely score-derived
  authority: {                        # the movement's EFFECTIVE authority, not raw policy —
    paths_rw: [pattern],              #   see below
    paths_ro: [pattern],              #   all sorted, duplicate-free
    shell:    bool,
    network:  bool,
    side_effects: [side_effect]       # sorted; empty in v0.2
  },
  score: {
    goal,
    context?,                           # omitted when absent, never encoded as ""

    global_invariants,                # A.5.1 — enumerated
    verification_expectation_intent   # forwarded into briefs, hence a dependency
  },
  resolved_decisions: [{decision_id, kind, digest}],
                                      # digest = H("partitur/resolution-body", …) — A.4
                                      # in DELIVERED order (§4: resolving seq, after truncation) —
                                      #   not re-sorted, because the hash must bind what was
                                      #   actually sent. Opposite human answers must not
                                      #   yield the same hash — the answers are part of what the
                                      #   attempt was asked to do. `digest` is a canonical hash of
                                      #   the resolution body rather than the body itself, so an
                                      #   answer's length cannot bloat the projection
  feedback: [{previous_attempt_id, kind, artifact_instance_id, content_hash}],
                                      # sorted by (previous_attempt_id, artifact_instance_id) —
                                      #   an exact key, since one prior attempt can contribute
                                      #   several diagnostics. Empty for a first attempt. Feedback changes the
                                      #   rendered brief (§7), so it changes what was asked
  extensions: <canonical extensions.<actual_adapter_id> subtree, or omitted>
}
```

**Effective authority, not raw `policy`** — and "intersected with its grants" is not an algorithm,
so here it is:

```text
P := policy.allowed_paths, as a sorted duplicate-free set
paths_rw := P  if grants ∋ repo_write  else []
paths_ro := P  if grants ∋ repo_read   else []
```

- **A write grant does not imply a read grant.** They are declared separately in §2 and a
  `read_only` part exists precisely to hold one without the other, so nothing is inferred.
- A pattern therefore **does appear in both** lists when a movement holds both grants. That is not
  redundancy: the two lists answer different questions, and the wire request carries both (§4).
- **Empty `policy.allowed_paths` yields empty lists**, which means a movement holding `repo_write`
  can write nothing. That is a coherent, if useless, score; the compiler **preserves that empty
  authority** rather than silently widening it to `**`. It is not reported: §2's rules are all
  rejections and every validation diagnostic is fatal (§4), so there is no general non-fatal
  **score** reporting channel in v0.2 through which a score this appendix calls coherent could be
  surfaced without also refusing it. §4's explicit enforcement advisory report is not such a
  channel: it carries per-performer probe outcomes only, and no score fact may use it without a
  specification change.
- `["**"]` is **not** path-scoped: it is the whole repository, so the fail-closed predicate (§4) asks
  only for `read_only` where writes are withheld, and does not additionally demand `path_grants`.
  Any narrower pattern set *is* path-scoped and does demand it. Getting this backwards would either
  block every ordinary run or silently accept unenforced path scoping.

The projection hashes this result rather than the whole `policy.allowed_paths` list. Hashing the raw list would mean a permitted `NARROW_PATHS`
amendment that removes a pattern irrelevant to a succeeded movement still changes that
movement's dependency hash and blocks its own approval (§9). The envelope would then be
unusable for exactly the narrowing it exists to allow.

**Excluded**, because they vary between attempts of the same request without changing it:
`run_id`, `attempt_id`, `attempt_number`, filesystem paths (`workdir`, `output_dir`),
`session_hint`, remaining budget, wall-clock time, and the score's
`revision` (a revision bump with an identical projection is not a dependency change).

The `revision` exclusion has **one exception**: for a `may_propose` movement it is included
transitively, because `score_base_hash` is over the whole score AST and `revision` is part of
that AST. A revision bump therefore does change a `may_propose` attempt's hash — which is the
same intended consequence as above, not a separate rule.

**`policy.budget` and `policy.amendment` are excluded as *scheduling policy* — not because they
never reach a performer.** That looser claim would be false: a `may_propose` attempt receives the
complete score base, budget and amendment policy included, and the remaining budget additionally
reaches every attempt in the wire request. The precise position is:

- For an **ordinary** attempt they are excluded, because they govern *how much* and *how* work is
  scheduled rather than *what* was asked. Including them would make `BUDGET_DECREASE` — the one
  envelope class with no state guard — invalidate every succeeded attempt in the run.
- For a **`may_propose`** attempt they are already covered, transitively and exactly, by
  `score_base_hash` above. Nothing is lost; it is the whole score that is the dependency.
- The remaining budget in the wire request is excluded as a per-attempt runtime value, like the
  filesystem paths beside it.

### A.5.1 `global_invariants`

§4 calls this "a deterministic projection computed by the core from goal, finalized resolutions,
and policy — not a separate score field". Enumerated:

```text
{
  resolved_questions: [                               # sorted by id; a tagged union, because
    {id, question, disposition: "resolved", resolution}   # a shape that requires `resolution`
    | {id, question, disposition: "waived"}               # cannot also express a waiver
  ],
  effective_paths: {                                  # the MOVEMENT's effective authority,
    rw: [pattern], ro: [pattern]                      #   sorted — never raw
  },                                                  #   policy.allowed_paths, which would
                                                      #   reintroduce the narrowing problem
                                                      #   A.5 exists to avoid
  side_effects_permitted: [side_effect],              # sorted; empty in v0.2
  protected_paths: [pattern]                          # sorted — §2, stated so a performer can
                                                      #   avoid an unrecoverable violation
}
```

Excluded from `global_invariants` for the same reason as above: budget and amendment policy.
`verification_expectation_intent` is a sibling field rather than a member here, because §9 names
it individually as an execution dependency.

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

- Recorded disposition names a successor that is missing → realize it per §3.1's second arm.
- Recorded disposition is *terminal* → synthesize `movement.failed`, and `run.failed` if that
  terminalizes the run. This holds even though a terminal failure is **uncharged** (§3.1) — being
  uncharged is precisely what marks it terminal.

Each synthesized event is keyed so a repeated recovery is a no-op.

## B.0 Payload conventions

The tables in B.1–B.7 give each event's **state effect**; the `payload` blocks that follow each
table give its **exact fields**. Both are normative. Conventions:

- **Envelope fields are never repeated in a payload.** `event_id`, `seq`, `ts`, `run_id`,
  `score_revision`, `movement_id`, `part_id`, `attempt_id`, `type`, and `causation_id` live in
  the envelope (§6). A payload field that duplicates one of them is a defect, not redundancy.
- `?` marks an optional field. Everything else is **required** — absent is malformed, and
  malformed is a recovery halt, not a value to guess at.
- An **empty payload** is written `{}` and is a deliberate statement: the event's entire meaning
  is its type plus its envelope. Four *authoritative* events are like this —
  `movement.ready`, `movement.started`, `verification.passed`, `attempt.completed` — and it is not
  an oversight, because their target and prerequisites are determined by upstream authority.
  **Derived** events carry only what the envelope cannot supply:
  usually nothing, since their type, envelope, and `causation_id` already say which target and
  which source. The exception is `decision.obsoleted`, which must name its `decision_id` because
  the envelope has no such field.
- Hash-valued fields carry their prefix (`sha256:`, `git-sha1:`) per A.4. Tree-valued fields are
  always Git-native.
- **Version binding.** Any payload carrying a canonical-AST hash **must** carry
  `identity_versions`, so a later recomputation uses the same rules or fails closed as
  `unsupported_run_format` (A.2). A single `projection` value cannot serve: one payload routinely
  carries hashes from several independently versioned domains, so the projection versions are a
  **map keyed by domain**:

  ```text
  identity_versions: {
    canonical_encoding: int,
    projections: { "<domain>": int, ... },   # every domain whose hash appears in this payload
    classifier?:  int,                        # amendment events (§9)
    composition?: int                         # composition and candidate events (§5)
  }
  ```

  Raw-byte and Git-native hashes need no entry — they are not canonical-AST identities (A.4).

  The map is the **transitive closure**, not just the domains named in the payload. An
  `execution_dependency_hash` is computed over an acceptance-spec hash, which is computed over
  criterion-spec hashes, and may carry a movement-composition and a score hash — so replaying it
  needs every one of those projection versions. Recording only the outermost domain would let a
  recomputation silently use a different inner rule.

**Subject binding.** Where a payload includes `subject_tree`, it is the **core-observed** tree,
never a value taken from an artifact (§7). Events repeat it rather than referring to an earlier
event because a mark must be readable from the single event that constitutes it — a projection
that had to join across events to learn what a criterion proved would be one join away from
proving the wrong thing.

**Disposition.** Failure events that may or may not authorize another attempt carry the decision
explicitly, because recovery replays it and must never recompute admissibility (§3.1, Appendix C):

```text
disposition: {
  charged: "quality_retry" | "fallback" | "none",
  movement_terminal: bool,         # true ⇔ charged: "none" and no further path exists
  terminal_reason?                 # REQUIRED iff movement_terminal; a movement.failed reason
                                   #   (Appendix D), selected by §3.1's first arm
}
```

`charged: "none"` with `movement_terminal: true` is what marks a *terminal* failure. Being
uncharged is the marker — a terminal failure must not consume past its cap (§3.1).

**`terminal_reason` exists because `charged: "none"` does not record *why* the classification
stopped.** Quality with retries remaining but no time left, and quality with time remaining but no
retries left, are both `none` — and `retries_exhausted` is false for the first. Recovery cannot
recover the distinction later without re-reading the budget, which §3.1's second arm forbids, so the
reason is chosen once and carried.

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

**Payloads.**

```text
run.started {
  base_commit,                    # git-native, object-format qualified (A.4)
  base_tree,                      # git-native; the candidate's base_tree (§8)
  score_hash,                     # partitur/score — semantic snapshot identity
  score_file_hash,                # sha256: raw bytes of the root score AT RUN START, for the
                                  #   promotion CAS (§1, §8)
  resolved_cast_hash,             # partitur/resolved-cast
  identity_versions
}

run.succeeded {                   # WAIVED PATH ONLY. On the non-waived path the final
                                  #   movement's movement.succeeded carries the run's
                                  #   SUCCEEDED transition and no run.succeeded is appended
                                  #   (§8) — the two paths are mutually exclusive because a
                                  #   waived score has no final movement (§2 rule 12)
  candidate: {                    # the full candidate payload, folded in atomically (§8)
    candidate_id, base_tree, result_tree,
    ordered_change_sets: [change_set_id],
    contributors: [{movement_id, change_set_id}],
    candidate_composition_dependency_hash
                                  # partitur/candidate-composition; A.4's tagged identity | merge
                                  # projection, including the zero-writer candidate
  },
  waiver: {reason},               # the recorded apply_gate waiver (§2). The candidate's binding
                                  #   revision is the envelope's score_revision — not repeated
                                  #   here, since B.0 forbids duplicating envelope fields
  identity_versions
}

run.failed    { reason }          # closed enum, Appendix D
run.cancelled {
  cancelled_movement_ids: [movement_id],   # sorted; every nonterminal movement
  cancelled_attempt_ids:  [attempt_id],    # sorted; every nonterminal attempt
  obsoleted_decision_ids: [decision_id],   # sorted; source for derived decision.obsoleted
  fenced_epoch?                            # present when this transition also fenced a wedged
                                           #   driver (§6): the epoch authority moved to, so
                                           #   recovery projects the fence instead of inferring
                                           #   it. Fencing and terminalization are one
                                           #   transition, so they are one event
}

movement.ready   {}               # meaning is entirely type + envelope
movement.started {}               # likewise

movement.succeeded {
  approved_artifact_instance_ids: [artifact_instance_id],   # sorted
  approved_change_set_id?,        # present iff the movement holds repo_write (§5)
  identity_versions,              # `change_set_id` is a canonical-AST identity (A.4)
  run_succeeded: bool             # true only for the final movement, where this event IS the
                                  #   run's SUCCEEDED transition (§8)
}

movement.failed {
  reason,                         # closed enum, Appendix D
  decision_id?,                   # required iff reason = human_gate_rejected
  subject_tree?,                  # required iff reason = human_gate_rejected
  run_failed: bool                # true iff THIS event carries the run's FAILED transition, which
                                  #   happens in exactly one case: a final-movement
                                  #   `human_gate_rejected`, where §8 makes it one atomic
                                  #   transition. In every other case the run failure is a
                                  #   separate `run.failed` append (Appendix C.2), so there is
                                  #   never a choice between two plausible sequences
}

movement.cancelled {}             # derived from run.cancelled
```

## B.2 Attempt lifecycle and performer selection

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `performer.selected` | ✓ | attempt_id | before `attempt.started` | Records the chosen performer, adapter id, model, and **why** this attempt exists (`initial`, `quality_retry`, `fallback`, `revision_restart`, `decision_resume`). **Creates the attempt in `STARTING`**, which is what makes that state reachable and lets a spawn failure be attributed to a chosen performer. Probe-derived facts cannot appear here: the selected adapter has not passed the durable gate yet. It records the *reason*; it never charges the budget — charging belongs to the failure event that caused it, so a retry cannot be double-counted |
| `attempt.started` | ✓ | attempt_id | Attempt `STARTING` | Attempt `STARTING → RUNNING`; records `attempt_number`, the gated adapter process identity, and granted authority |
| `adapter.probed` | ✓ | attempt_id | Attempt `RUNNING`, no prior `adapter.probed` | Records the valid observation from this attempt's own gated adapter peer, the resulting advisory decisions and recognized feature set, and the exact request's `execution_dependency_hash`. It is appended and fsynced before `execute`; no `execute` is legal without it |
| `performer.completed` | ✓ | attempt_id | Attempt `RUNNING`, `adapter.probed` present, recorded adapter session verified empty, and its `adapter` interval closed | Attempt → `VERIFYING`. **Vendor execution ended and its session is harmless; says nothing about success** (§4, §6) |
| `attempt.completed` | ✓ | attempt_id | Attempt `VERIFYING`, acceptance and gate passed | Attempt → `COMPLETED` |
| `attempt.blocked` | ✓ | attempt_id | Attempt `RUNNING`, `adapter.probed` present | Attempt → `BLOCKED` (terminal); carries `pending_decision_ids` |
| `attempt.failed` | ✓ | attempt_id | Attempt `STARTING`/`RUNNING`/`VERIFYING` | Attempt → `FAILED`; carries the failure kind (Appendix D). Legal from `STARTING` so spawn and startup failures are representable. **Charges at most once, and only if it authorizes another attempt**; what it charges and what follows are §3.1's, selected by its first arm and recorded in `disposition` |
| `attempt.cancelled` *derived* | — | source event_id + attempt_id | — | Attempt → `CANCELLED`; projected idempotently from `run.cancelled`, for the same reason |
| `attempt.superseded` *derived* | — | source event_id + attempt_id | — | Attempt → `SUPERSEDED`; projected from `amendment.approved` (§9) |
| `execution.started` | ✓ | `interval_id` | no interval open | Opens the (single) budget interval; carries `interval_id`, `phase`, `wall_start`, `remaining_at_start` (§6). Keyed on `interval_id` because one attempt legitimately opens several intervals |
| `execution.stopped` | ✓ | `interval_id` | that interval open | Closes it and charges `charged_duration`. `charging: measured` requires the closer to **be** the process that opened it; every other closer uses `charging: clamped` and the deterministic formula of §6 — which covers recovery (`reason: recovered`) and a fenced supersession (`reason: superseded`) alike. **`reason: cancelled` is always `clamped`**, whether or not `(d)` fenced and whether or not the canceller running the oracle's `(c)` is the opener: §6 fixes one shape for that step across every canceller, so this is a property of the reason rather than of the closer. For an ordinary `adapter` close, §4 additionally requires the recorded session to be verified empty first |

**Payloads.**

```text
performer.selected {
  reason,                         # closed enum: initial | quality_retry | fallback |
                                  #   revision_restart | decision_resume
  performer_id, adapter_id, model
}
                                  # NB: no budget field. Charging belongs to the failure event
                                  #   that authorized this attempt, never to selection

adapter.probed {
  adapter_version,                # reported by this attempt's gated peer, never copied from
                                  #   validate or an earlier attempt
  capabilities: {
    repo_read, repo_write, shell, network, resumable_sessions       # all bool; models are
                                  #   intentionally absent because §4 does not evaluate them
  },
  enforcement: {                  # reported by this same peer (§4)
    path_grants, read_only, network_grants, shell_grants, read_grants   # all bool
  },
  negotiated_features: [string],  # sorted; empty in v0.2 because no tokens are defined.
                                  #   Retained so future negotiated sets are authoritative and
                                  #   historical requests remain reconstructible without a
                                  #   payload change.
  truncated_resolutions: [decision_id],
                                  # sorted; resolutions dropped for the frame budget (§4)
  advisory_dimensions: [dimension],
                                  # sorted; exact constraints proceeding without enforcement
  execution_dependency_hash,      # A.5; the exact request shape is now fixed
  identity_versions
}

attempt.started {
  attempt_number,                 # per-movement display ordinal — never the identifier (§6)
  adapter_process: {              # recorded BEFORE the trampoline's gate is released, so an
    pid,                          #   unrecorded process can never have executed adapter code (§4)
    session_id,                   #   == pid; the trampoline is the session leader
    start_identity: (
        {platform: "linux",  boot_id, start_ticks}
      | {platform: "darwin", start_tvsec, start_tvusec}
    )
  },                              # PGID is deliberately absent: §4 enumerates the recorded session
                                  #   and discovers its process groups at sweep time
  base_composition_hash?,         # movement_composition_dependency_hash (A.4); present iff the
                                  #   movement has dependencies. Journaled here because §5
                                  #   records it and §9 checks it — a declared identity that is
                                  #   never persisted protects nothing. A dependency set with
                                  #   zero contributing change sets uses `composition_mode:
                                  #   identity`
  granted_authority: {            # exactly the wire `grants` object of §4
    paths_rw: [pattern], paths_ro: [pattern], shell: bool, network: bool
  },
  identity_versions
}

performer.completed {             # appended ONLY for outcome `completed` — see the mapping below
  session_hint_stored: bool       # whether a hint was retained (§4). The hint itself is
                                  #   NEVER journaled
}

  # Adapter outcome → journal transition. This mapping is applied only after §4's
  # execute-completion cleanup and interval close. Each reported outcome has exactly one
  # authoritative event, so no outcome is left without a terminal transition:
  #
  #   completed      → performer.completed        (attempt RUNNING → VERIFYING)
  #   waiting_human  → attempt.blocked            (attempt RUNNING → BLOCKED, terminal)
  #   failed         → attempt.failed {kind}      (from the reported failure kind)
  #   cancelled      → attempt.cancelled   if a RUN cancellation authorizes it (derives from
  #                                          run.cancelled), or
  #                    attempt.superseded    if an approved revision authorizes it (derives from
  #                                          amendment.approved) — the two have different sources
  #                                          and must not be conflated, or
  #                    attempt.failed {kind: task_failed, reason: unsolicited_cancel} when
  #                                          nothing asked. An adapter cancelling itself
  #                                          unprompted is a task failure, and must not escape
  #                                          charging by reporting the wrong outcome

attempt.completed {}              # acceptance and any gate already recorded the evidence

attempt.blocked {                 # the SOURCE event for the whole handshake (§4). Every
                                  #   decision.requested below is DERIVED from it, so a crash
                                  #   between appends can never lose a question's text
  raised: [                       # sorted by decision_id; the complete set, content included
      {decision_id, emitted_id, kind: "question",  question, blocking: true}
    | {decision_id, emitted_id, kind: "proposal",  proposal_id, blocking}
                                  #   A proposal entry records that the attempt BLOCKED on it —
                                  #   nothing more. Its decision.requested is derived from
                                  #   `amendment.routed_human` (§1), which alone carries
                                  #   `routed_reason`, `decision_type`, and the record hash, and
                                  #   which only exists once the proposal passes admissibility.
                                  #   Two source events would otherwise compete for one decision
  ],
  pending_decision_ids: [decision_id]    # sorted; MUST equal exactly the ids in `raised` whose
                                  #   `blocking` is true — the adapter's reported set is validated
                                  #   against the events it actually emitted (§4), never trusted
}

attempt.failed {
  kind,                           # adapter failure kind, or a core-determined kind
                                  #   (budget_exhausted) — Appendix D
  reason?,                        # sub-reason within the kind, e.g. draft_no_blocking_output
                                  #   for task_failed, or a grant_denied/protocol_error
                                  #   sub-reason
  detail?,                        # sanitized free text
  disposition                     # B.0 — replayed verbatim by recovery
}

attempt.cancelled  {}             # derived from run.cancelled
attempt.superseded {}             # derived from amendment.approved

execution.started {
  interval_id,                    # unique; one attempt legitimately opens several intervals
  phase,                          # adapter | acceptance | composition
  wall_start,                     # RFC 3339 with millisecond precision, UTC
  remaining_at_start              # integer MILLISECONDS, the clamp's second operand (§6)
}

execution.stopped {
  interval_id,
  reason,                         # normal | cancelled | superseded | budget_exhausted |
                                  #   recovered
  charging,                       # measured | clamped — orthogonal to reason
  charged_duration,               # integer MILLISECONDS, computed by the OPENING process from its
                                  #   own monotonic clock, or by the clamp when recovering
  observed_at?                    # required iff charging = clamped; the recovery-time wall
                                  #   sample, journaled so replay never re-samples (§6)
}
```

## B.3 Evidence

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `artifact.recorded` | ✓ | `(logical_output_id, attempt_id)` | Attempt `RUNNING` with `adapter.probed` present, or `VERIFYING` | Registers the immutable instance and its byte hash (§1). Two distinct rules apply and must not be conflated: a **second adapter notification** for the same logical id is rejected *before* any append (`duplicate_artifact_instance`, §1), while a **replayed append** with the same key and equivalent payload is an ordinary idempotent no-op (B.0) — replay must be safe, and a protocol violation must not be |
| `change_set.recorded` | ✓ | attempt_id | Attempt `VERIFYING` | Records `change_set_id`, `base_tree`, `result_tree`, and the pinning ref (§5). Only for `repo_write` movements |
| `verification.passed` | ✓ | attempt_id | Attempt `VERIFYING` | The post-hoc verification boundary (§5): protected paths, and the read-only invariant where applicable, both held. Whether the read-only invariant applies is derived from this movement's grants in the pinned score revision identified by the envelope, never restated in the payload. Exists so recovery can tell "checked" from "not yet checked" — inferring it from `change_set.recorded` would silently skip the check |
| `composition.conflicted` | ✓ | `scope` + `target_id` + `composition_subject_hash` | fan-in or candidate composition | **Evidence only — it projects no state.** Records `scope`, `target_id`, the ordered contributors and the conflicted paths. `scope: movement` ⇒ `target_id = movement_id`, and the terminal event is `movement.failed {composition_unresolvable}` — legal from `RUNNING`, since fan-in happens after `movement.started`. `scope: candidate` ⇒ `target_id = run_id` (**not** a `candidate_id`: composition failed, so no candidate exists), and the terminal event is `run.failed {composition_unresolvable}`. Recovery synthesizes the missing terminal event if a crash lands between the two appends, **subject to C.1's precedence** — a `cancel.requested` landing in that window outranks it. The key includes `target_id` because two movements can conflict over the same contributor list |
| `composition.failed` | ✓ | `scope` + `target_id` + `composition_subject_hash` | fan-in or candidate composition | **Evidence only — it projects no state**, exactly as the conflict event. Records the `cause` (Appendix D), the merge's exit status where the merge exited at all, and a sanitized diagnostic. The terminal event is `movement.failed {composition_failed}` or `run.failed {composition_failed}` by the same scope rule, and recovery synthesizes it if a crash lands between the two appends — **subject to C.1's precedence**, since a `cancel.requested` landing in that window outranks it. Separate from `composition.conflicted` because a conflict is a verdict and this is the absence of one — reporting the second as the first would let a broken Git call a clean repository unmergeable (§5) |
| `application_candidate.recorded` | ✓ | candidate_id | every `repo_write` movement succeeded | Records the candidate and **constitutes its initial binding** (§8) |
| `acceptance.started` | ✓ | attempt_id | Attempt `VERIFYING` | Binds `subject_tree` + `acceptance_spec_hash` before any criterion runs (§7) |
| `criterion.started` | ✓ | attempt_id + criterion_id | after `acceptance.started` | Carries `criterion_spec_hash` and the same subject binding |
| `criterion.completed` | ✓ | attempt_id + criterion_id | matching `criterion.started` | `outcome` ∈ `{PASS, FAIL, ERROR}` |
| `acceptance.failed` | ✓ | attempt_id | any `FAIL`/`ERROR`, or recovery | Terminal for this acceptance **and for the attempt**: projects Attempt `VERIFYING → FAILED` in the same transition, so no attempt is ever left stranded in `VERIFYING`. Reason ∈ Appendix D. Always terminalizes the attempt; it is a quality failure case, so §3.1's first arm decides what it charges and what follows. The disposition is recorded in the event, and recovery replays it rather than recomputing. No separate `attempt.failed` is appended on this path |
| `acceptance.evaluation_completed` | ✓ | attempt_id | all criteria `PASS` | The **only** gateway to grade derivation (§8) |

**Payloads.**

```text
artifact.recorded {
  logical_output_id, kind,
  content_hash,                   # sha256: raw bytes of the immutable instance (§1)
  size_bytes,
  source_path                     # output_dir-relative, as announced — provenance only; the
                                  #   immutable copy is what counts (§1)
}

change_set.recorded {
  change_set_id,                  # partitur/change-set
  base_tree, result_tree,         # git-native; equal for a no-op write (§5)
  commit,                         # git-native; the checkpoint commit — a STORAGE HANDLE, not
                                  #   the identity (§5)
  ref,                            # refs/partitur/runs/<run-id>/attempts/<attempt-id>/changeset
  identity_versions
}

verification.passed {}            # attests the full §5 invariant. The expected worktree is
                                  #   determined by the run/composition/change-set/candidate facts
                                  #   for this attempt, and a tree hash could not restate
                                  #   non-ignored untracked files or the other non-tree checks

composition.conflicted {          # EVIDENCE ONLY — projects no state (§5)
  scope,                          # movement | candidate
  target_id,                      # movement_id when scope=movement; run_id when
                                  #   scope=candidate — no candidate_id exists, composition
                                  #   is what failed (§5)
  composition_subject_hash,       # partitur/composition-subject — the idempotency key's
                                  #   third component. Deliberately NOT the movement-level or
                                  #   candidate-level composition dependency hash: those
                                  #   identify a successful composition's inputs, whereas this
                                  #   identifies WHICH composition conflicted
  contributors: [{movement_id, change_set_id}],   # composition order
  conflicted_paths: [path],       # sorted
  composition_algorithm_version,
  identity_versions
}

composition.failed {              # EVIDENCE ONLY — projects no state (§5)
  scope,                          # movement | candidate — same values as composition.conflicted
  target_id,                      # as composition.conflicted
  composition_subject_hash,       # partitur/composition-subject — identifies WHICH composition
                                  #   could not be determined. Same third key component as the
                                  #   conflict event, for the same reason
  cause,                          # closed enum, Appendix D
  git_exit_code?,                 # required iff cause is `git_exit` or `output_unusable` — the two
                                  #   in which the merge ran AND exited. Absent for `git_signalled`,
                                  #   which has no exit status, and for the phase 1 and phase 2
                                  #   causes, which never reached the merge
  diagnostic,                     # sanitized and truncated to 4 KiB on a valid UTF-8 boundary,
                                  #   like log (B.7). Composition runs a subprocess against a
                                  #   core-created temporary repository, so its stderr can name
                                  #   paths; it is evidence, not a channel
  contributors: [{movement_id, change_set_id}],   # composition order, as the conflict event
  composition_algorithm_version,
  identity_versions
}

application_candidate.recorded {
  candidate_id,                   # partitur/candidate
  base_tree, result_tree,
  ordered_change_sets: [change_set_id],           # content-deduplicated applied sequence
  contributors: [{movement_id, change_set_id}],   # full pre-dedup ordered sequence
  candidate_composition_dependency_hash,
                                  # partitur/candidate-composition; A.4's tagged identity | merge
                                  # projection, including the zero-writer candidate
                                  # The initial binding is {candidate_id, score_revision} where
                                  #   score_revision comes from the ENVELOPE; recording this
                                  #   event IS the binding, and no separate binding event is ever
                                  #   appended (§8)
  identity_versions
}

acceptance.started {
  subject_tree,                   # core-observed; bound before any criterion runs (§7)
  acceptance_spec_hash,           # A.4.2 — the effective compiled plan
  planned_criterion_ids: [criterion_id],   # execution order, declared then generated (§7).
                                  #   Recovery reads this to know what remains
  identity_versions
}

criterion.started {
  criterion_id, criterion_spec_hash,
  subject_tree,                   # repeated: every criterion event repeats the binding (§7)
  criterion_process?: {           # present IFF this criterion is an external command that spawned
    pid, session_id,              #   successfully. `artifact` and `review` criteria evaluate
    start_identity: (             #   in-process and have no PID at all, and a command that failed
        {platform: "linux",  boot_id, start_ticks}      # to spawn never had one — so requiring it
      | {platform: "darwin", start_tvsec, start_tvusec} # unconditionally would make those cases
    )                             #   unrepresentable. Same trampoline handoff as an attempt (§4),
  },                              #   recorded before the gate is released: without it recovery
                                  #   could synthesize ERROR while an orphan still mutates the
                                  #   worktree, then verify that worktree as if it were quiet
  spawn_failed?: bool,            # present and true iff an external command could not be spawned.
                                  #   Its `criterion.completed` is ERROR with no process identity,
                                  #   and recovery has nothing to sweep
  identity_versions
}

criterion.completed {
  criterion_id, criterion_spec_hash,
  subject_tree,                   # repeated for the same reason — a mark must be readable
                                  #   from the event that constitutes it, without joining
  outcome,                        # PASS | FAIL | ERROR
  exit_code?,                     # present for a spawned command that ran to completion
  duration_ms?,                   # present for an OBSERVED completion; absent only when recovery
                                  #   synthesized the completion without observing process exit
                                  #   (Appendix C.3) — a fabricated duration would be evidence
  output_ref?,                    # attempt-directory path to the bounded captured output;
                                  #   the output itself is never inlined (§7)
  error_detail?,                  # required iff outcome = ERROR — what produced no verdict
  identity_versions
}

acceptance.failed {
  reason,                         # closed enum, Appendix D
  failed_criterion_id?,           # absent for reasons that are not criterion-scoped, e.g.
                                  #   recovery_subject_mismatch
  subject_tree,
  disposition                     # B.0 — this event terminalizes the attempt itself, so no
                                  #   separate attempt.failed is appended (B.2)
}

acceptance.evaluation_completed {
  subject_tree, acceptance_spec_hash,
  criterion_outcomes: [{criterion_id, criterion_spec_hash, outcome}],
                                  # planned order; every outcome PASS by definition. Present
                                  #   so grade derivation reads ONE event (§8)
  identity_versions
}
```

## B.4 Decisions

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `decision.requested` | ✓ | decision_id | — | Adds to pending. **Always physically appended**, never only projected — the `sync: ✓` is real. What varies is the *source* it is derived from, and each decision has exactly one: a **question** from `attempt.blocked`'s `raised` entry; an **amendment or finalization** from `amendment.routed_human`, which alone carries `routed_reason` and `decision_type`; a **human gate** appended directly with no derivation. `decision_type ∈ {question, human_gate, amendment, finalization}` |
| `decision.resolved` | ✓ | decision_id | pending | Removes from pending. Carries the answer, or for `human_gate` the `gate_id`, `subject_tree`, scope, and overridden finding instance ids (§8). **Never appended on any amendment path** (§9) |
| `decision.obsoleted` *derived* | — | source event_id + decision_id | — | Terminally closes a pending decision — either raised on a superseded revision, or outstanding when the run went terminal. Derives from `amendment.approved` **or** from any event whose projection makes the run terminal |

**Payloads.**

```text
decision.requested {              # a TAGGED UNION on decision_type — a flat bag of optional
                                  #   fields would let a human-gate request omit its gate id
  decision_id,                    # derived from (attempt_id, emitted_id) for adapter-raised
                                  #   decisions (§4) — never the raw emitted id
  emitted_id?,                    # the adapter's own id, retained as provenance. Present iff
                                  #   the decision came from an adapter; only attempt-unique,
                                  #   so nothing may key on it
  decision_type,
  ... per-type fields below
}

  decision_type: "question"    ⇒ { question }              # sanitized text (§4)
  decision_type: "human_gate"  ⇒ { gate_id, gate_mode, subject_tree,
                                   review_outcome?,        # present iff review evidence exists
                                   blocking_findings: [{artifact_instance_id, finding_id}] }
                                                           # sorted; empty when the gate is
                                                           #   `always` with no blockers raised
  decision_type: "amendment"   ⇒ { proposal_id, routed_reason, blocking }
                                                           # `blocking` preserves the proposal's
                                                           #   requires_decision (§4): a blocking
                                                           #   proposal stops its attempt, a
                                                           #   non-blocking one does not, and
                                                           #   WAITING_HUMAN projects only from
                                                           #   unresolved BLOCKING decisions (§6)
  decision_type: "finalization"⇒ { proposal_id, routed_reason }   # always draft_phase (§2) and
                                                           #   always blocking

decision.resolved {               # also a TAGGED UNION. NEVER appended on an amendment or
                                  #   finalization path — those resolve through their own
                                  #   terminal event (§9)
  decision_id,
  decision_type,                  # question | human_gate. Repeated because §8 derives APPROVED
                                  #   from `decision.resolved {decision_type: human_gate}` and
                                  #   must not have to join back to the request to know
  disposition,
  ... per-type fields below
}

  decision_type: "question", disposition: "answered"
    ⇒ { answer }                  # the wire resolved_decisions entry is
                                  #   {decision_id, kind: "answer", answer} (§4)

  decision_type: "human_gate", disposition: "approved" | "rejected"
    ⇒ { gate_id,
        scope: {subject_tree},    # the EXACT tree approved; movement and attempt are NOT repeated
                                  #   here — they are envelope fields and B.0 forbids duplicating
                                  #   them. An approval
                                  #   never carries over to another attempt, revision, or tree
                                  #   (§8). `scope.subject_tree` is the single source — there is
                                  #   no sibling subject_tree to fall out of agreement with
        overridden_findings: [{artifact_instance_id, finding_id}],   # sorted; empty when none
        override_reason? }        # MUST be present and non-empty iff overridden_findings is
                                  #   non-empty (§8) — overriding a reviewer's blocking judgment
                                  #   is precisely the decision that must stay auditable

decision.obsoleted {
  decision_id                     # the envelope has no decision_id (§6), so the target must be
                                  #   named here. `causation_id` says what obsoleted it
}
```

## B.5 Amendments

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `amendment.rejected` | ✓ | proposal_id | **Any** run state — `reason: run_terminal` exists precisely for a terminal run (§9 step 1), so restricting this event to nonterminal runs would make that reason unreachable | Terminal. Reason ∈ Appendix D. Records base hash and classifier version; records the typed delta only for failures at step 7 or later, since a validated AST exists only once step 6 has passed and `invalid_score` is step 6 failing; otherwise the patch-operations hash and error location |
| `amendment.approval_prepared` | ✓ | `prepare_id`; **at most one prepare may be pending per run**, since two concurrent quiesces would race for one lease | **approval intent established** — for `auto`, after envelope classification and state guards pass; for `human`, after the decision is approve and steps 1–9 have been re-run. Not merely "passed 1–9", which precedes approval policy and would let a merely-routable proposal reserve a prepare | **Changes nothing**, and **raises the mutation barrier** (§6). The durable quiesce request: binds the proposal, the base head, the already-written snapshot, the persisted approval plan, the target attempts, and the observed authority epoch, so a driver's lease-move ACK matches a specific prepare rather than "some approval". A repeat with the same `prepare_id` is an idempotent re-request |
| `amendment.approval_abandoned` | ✓ | `prepare_id` | a pending prepare | Terminally closes the prepare and **lifts the barrier**. Required because the journal is append-only: removing the sidecar cannot clear a pending `approval_prepared`, so without this event the prepare stays pending on every replay, "at most one pending prepare" blocks every retry, and the run wedges permanently |
| `amendment.routed_human` | ✓ | proposal_id | admissible | **Non-terminal** routing marker; appends `decision.requested` for the amendment |
| `amendment.approved` | ✓ | proposal_id | **a matching commit-ready prepare** (§6 step 3) — not merely "passed 1–9", which would let an implementation bypass preparation and quiescence entirely | The **single authoritative transition**: new snapshot head, new revision, superseded attempt ids, obsoleted decision ids, re-bound `candidate_id`. Resolves its own decision directly. **Finalization special case** (`/status: draft → finalized`, §2): the same event additionally closes the draft phase and projects the interview movement to `SUCCEEDED`, manufacturing no `attempt.completed` and no VERIFIED/APPROVED evidence |
| `amendment.human_rejected` | ✓ | proposal_id | routed | Terminal; carries proposal id, decision id, human reason; resolves its own decision |

**Payloads.** Every amendment event carries `base_hash` and `classifier_version` (§9).

```text
amendment.rejected {
  proposal_id,
  emitted_id?,                    # provenance when the proposal came from an adapter. Present
                                  #   here too, because a rejected or auto-approved proposal
                                  #   never opens a decision.requested, so this is the only
                                  #   place its origin would be recorded
  reason,                         # closed enum, Appendix D
  condition?,                     # required iff reason = candidate_incompatible: one of
                                  #   succeeded_dependency_changed | composition_changed |
                                  #   verification_episode_finished | verification_mode_changed
  base_revision, base_hash, classifier_version,
  # A validated patched AST exists only once step 6 has PASSED, so these are present for
  # failures at step 7 and later — not "from step 6 on", since `invalid_score` IS step 6 failing:
  typed_delta?: [{selector, operation, before_hash?, after_hash?}],
  actual_impact?,                 # §9 shape
  # for failures at steps 1-6 there is no validated AST, so the record is coarser:
  patch_operations_hash?,         # partitur/patch-operations
  error_location?,                # where in the patch or the score the failure was found
  decision_id?,                   # present iff a decision exists or could exist for this
                                  #   proposal — that is, iff it was blocking (§4) OR it had been
                                  #   routed. Routing always opens a decision regardless of
                                  #   `blocking`, so a routed NON-blocking proposal rejected at
                                  #   decision-time revalidation must be able to close its pending
                                  #   decision too; omitting the id there would leave it pending
                                  #   forever. Terminally resolves the decision in projection (§9).
                                  #   Derived per A.4.3, so it matches the id the attempt or the
                                  #   routing opened
  identity_versions
}

amendment.routed_human {          # NON-TERMINAL routing marker
  proposal_id, reason,            # routing reason, closed enum
  emitted_id?,                    # provenance when the proposal came from an adapter
  decision_type,                  # amendment | finalization. REQUIRED because recovery recreates
                                  #   decision.requested from this event alone and cannot otherwise
                                  #   tell them apart: a CLI draft amendment and the reserved
                                  #   finalization amendment can both be `draft_phase`, blocking,
                                  #   and carry no emitted id
  blocking,                       # the proposal's requires_decision (§4). REQUIRED here because
                                  #   recovery recreates decision.requested from this event alone,
                                  #   and the amendment variant needs `blocking` to know whether
                                  #   the run enters WAITING_HUMAN — recovery must never guess that
  proposal_record_hash,           # sha256: raw bytes of proposals/<proposal-id>.json (§1).
                                  #   Decision-time re-validation needs the ORIGINAL operations
                                  #   (§9 re-runs steps 1–9), and neither the typed delta nor
                                  #   actual_impact can reconstruct them — both are lossy
                                  #   projections of the patched AST, not the patch
  base_revision, base_hash, classifier_version,
  decision_id,                    # the decision.requested this opens
  typed_delta: [{selector, operation, before_hash?, after_hash?}],
  actual_impact,
  envelope_evaluation?: {          # recomputed for the AUDIT RECORD ONLY — it never re-routes
    class?,                        #   or blocks a human approval, which would loop (§9)
    guard_passed: bool,
    guard_failure_reason?
  },
  identity_versions
}

amendment.approved {              # the SINGLE authoritative transition (§9)
  proposal_id,
  emitted_id?,                    # provenance when the proposal came from an adapter
  mode,                           # auto | human
  decision_id?,                   # required iff mode = human; this event resolves it directly
  envelope_class?,                # required iff mode = auto: NARROW_PATHS | NARROW_GRANTS |
                                  #   BUDGET_DECREASE — WHICH class justified the approval.
                                  #   Without it an auto-approval is unauditable
  base_revision, base_hash, classifier_version,
  new_revision,                   # = base_revision + 1. The ENVELOPE's score_revision on this
                                  #   event is `new_revision`, not `base_revision`: the event
                                  #   constitutes the new head, and candidate re-binding takes
                                  #   the revision from the envelope (§8), so it must be the
                                  #   revision being bound to
  new_snapshot_hash,
  new_snapshot_file_hash,         # sha256: raw bytes written, for byte-exact promotion (§8)
  typed_delta: [{selector, operation, before_hash?, after_hash?}],
  actual_impact,
  superseded_attempt_ids: [attempt_id],    # sorted; EVERY nonterminal attempt — STARTING,
                                  #   RUNNING, or VERIFYING (§9). Source for the derived
                                  #   attempt.superseded rows
  obsoleted_decision_ids: [decision_id],   # sorted; excludes this amendment's own decision,
                                  #   which this event resolves directly
  candidate_id?,                  # present iff a candidate exists: the re-bound candidate.
                                  #   The binding fact projects from here (§8)
  envelope_evaluation?: {         # present iff mode = human: the guards recomputed AT DECISION
    class?,                       #   TIME (§9), for the audit record. The routed event's own
    guard_passed: bool,           #   evaluation is already fsynced and cannot be mutated or
    guard_failure_reason?         #   re-appended, so the later evaluation needs its own carrier
  },                              #   here or it is simply lost
  fenced_epoch?,                  # present **iff this transition advances the authority epoch**
                                  #   (§6) — which covers a wedged driver AND a driver that died
                                  #   mid-drain, both of which need the advance. Keying it on
                                  #   "wedged" alone contradicted the commit table, which gives the
                                  #   fenced branch to a verifiably dead owner too. Value is always
                                  #   `observed_authority_epoch + 1`
  finalization: bool,             # true iff this is the reserved /status draft→finalized
                                  #   amendment (§2). When true this event ALSO closes the
                                  #   draft phase and projects the interview movement to
                                  #   SUCCEEDED — manufacturing no attempt.completed and no
                                  #   VERIFIED/APPROVED evidence
  identity_versions
}

amendment.approval_abandoned {    # lifts the barrier; changes no score or lifecycle projection
  prepare_id, proposal_id,
  reason,                         # base_head_changed | plan_invalidated | cancelled
  base_revision, base_hash,       # every amendment event carries these (§9)
  classifier_version
}

amendment.approval_prepared {     # changes nothing; it RESERVES and RECORDS an approval operation
  prepare_id,                     # names the quiesced sidecar path `driver.quiesced.<prepare_id>`,
                                  #   so a driver's lease move is bound to THIS prepare
  proposal_id,
  mode,                           # auto | human — the approval intent, so recovery knows which
                                  #   policy path produced it
  decision_id?,                   # required iff mode = human: the decision already approved
  envelope_class?,                # required iff mode = auto: the class that justified it
  base_revision, base_hash,       # the head this prepare is bound to; a head change invalidates it
  new_revision,
  new_snapshot_hash,              # semantic (partitur/score)
  new_snapshot_file_hash,         # sha256: raw bytes of the snapshot ALREADY WRITTEN (§6 step 1)
  plan_record_hash,               # sha256: raw bytes of prepares/<prepare-id>.json (§9), which
                                  #   carries every amendment.approved field that is INVARIANT at
                                  #   prepare time — typed delta, actual_impact, obsoleted decision
                                  #   ids, candidate binding, finalization state. A semantic hash
                                  #   alone would be unrecoverable, since an AUTO proposal has no
                                  #   durable proposal record to rebuild those from
  target_attempt_ids: [attempt_id],   # sorted; the nonterminal attempts to be quiesced
  observed_authority_epoch,       # the epoch seen when preparing — the ACK must still match it
  quiesce_deadline,               # RFC 3339 ms; after it, the approver fences instead of waiting
  classifier_version,             # every amendment event carries it (§9)
  identity_versions               # it carries canonical hashes (B.0)
}

amendment.human_rejected {
  proposal_id, decision_id,
  human_reason,                   # non-empty
  base_revision, base_hash, classifier_version,
  identity_versions
}
```

## B.6 Shipping

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `apply.started` | ✓ | candidate_id + txn id | `NOT_APPLIED`/`FAILED_CLEAN` | Application → `APPLYING`; records before-tree, touched paths, recovery info |
| `apply.completed` | ✓ | txn id | `APPLYING`/`RECOVERY_REQUIRED` | Application → `APPLIED` |
| `apply.failed` | ✓ | txn id | `APPLYING` | Application → `FAILED_CLEAN` — rollback **verified** |
| `apply.recovery_required` | ✓ | txn id | `APPLYING` | Application → `RECOVERY_REQUIRED`. Appended explicitly after inspection under the lock; the state is never inferred silently (§8) |
| `apply.recovery_resolved` | ✓ | txn id | `RECOVERY_REQUIRED` | `{outcome: rolled_back}` → `FAILED_CLEAN` |
| `score.promotion_started` | ✓ | txn id | `NOT_PROMOTED`/`PROMOTING` | Promotion → `PROMOTING`. From `PROMOTING` it is legal **only with the transaction id already recorded** — a different id is refused, since otherwise "at most one successful promotion" could be circumvented by resuming under a fresh transaction. A repeat with the same id is an idempotent resume |
| `score.promoted` | ✓ | txn id | `PROMOTING`/`RECOVERY_REQUIRED` | Promotion → `PROMOTED`. At most one *successful* promotion per run |
| `score.promotion_recovery_required` | ✓ | txn id | `PROMOTING` | Promotion → `RECOVERY_REQUIRED` |

**Payloads.** `txn_id` is the transaction identity a `--recover` invocation resumes under; a
repeated `*_started` with the same `txn_id` is an idempotent resume, never a second transaction.

```text
apply.started {
  txn_id, candidate_id,
  before_tree,                    # the computed working tree, which must equal the
                                  #   candidate's base_tree (§8 preconditions)
  result_tree,                    # what a successful apply must produce
  touched_paths: [path],          # sorted — exactly what a rollback restores
  recovery: {                     # what --recover needs to decide, recorded BEFORE the write
    base_tree, result_tree
  },
  identity_versions               # `candidate_id` is a canonical-AST identity (A.4), and every
                                  #   B.6 event below carries it for the same reason
}

apply.completed { txn_id, candidate_id, result_tree, identity_versions }
apply.failed    {
  txn_id, candidate_id, identity_versions,
  failure_detail,
  rollback_verified: true         # always true on this path: FAILED_CLEAN means the base was
                                  #   re-verified after restoring. Unverified rollback is
                                  #   apply.recovery_required instead (§8)
}
apply.recovery_required {
  txn_id, candidate_id, identity_versions,
  observed_tree?,                 # what inspection under the lock actually found, when it
                                  #   could be computed
  failure_detail
}
apply.recovery_resolved {
  txn_id, candidate_id, identity_versions,
  outcome                         # rolled_back → FAILED_CLEAN. The APPLIED resolution appends
                                  #   apply.completed instead, not this event (§8)
}

score.promotion_started {
  txn_id, candidate_id, identity_versions,
  expected_root_file_hash,        # sha256: raw bytes — the CAS operand
  target_snapshot_file_hash,      # sha256: raw bytes to be written
  target_revision
}
score.promoted { txn_id, candidate_id, identity_versions, target_revision,
                 target_snapshot_file_hash }
score.promotion_recovery_required {
  txn_id, candidate_id, identity_versions,
  observed_root_file_hash?,       # present when the root was readable: it matched neither the
                                  #   expected nor the target hash, so the CAS can no longer
                                  #   decide and something else changed the root (§8)
  failure_detail
}
```

## B.7 Control and diagnostics

| Type | sync | idem key | Legal from | Projection effect |
|---|---|---|---|---|
| `authority.granted` | ✓ | `authority_epoch` | Run nonterminal | Records that a driver acquired or reclaimed execution authority at a new monotonic epoch, with the owner's PID and process-start identity. Makes the current epoch a journal projection (§6) so it survives lease removal. The incarnation **token is never journaled** — journaling it would let any reader forge authority |
| `cancel.requested` | ✓ | run_id | Run nonterminal | The durable, **run-scoped** cancellation authority (§6) — never keyed by attempt, because there is no attempt-scoped cancel. Observed by a live driver at any point while it holds the lease, which then terminalizes in the canceller role; where no valid lease owner remains, the cancelling command does |
| `journal.tail_truncated` | ✓ | truncated seq | recovery | Records that an unparseable final line was discarded (§1) |
| `log` | | — | — | Mirrored adapter diagnostics; sanitized (§4) |
| `progress` | | — | — | Mirrored adapter progress |

**Payloads.**

```text
authority.granted {
  authority_epoch,                # the new monotonic epoch — makes the current epoch a journal
                                  #   projection that survives lease removal (§6)
  owner_pid,
  owner_start_identity: (         # a TAGGED UNION, not an opaque string: another implementation
                                  #   must be able to replay the comparison
      {platform: "linux",  boot_id, start_ticks}       # /proc/sys/kernel/random/boot_id and
                                                       #   /proc/<pid>/stat field 22 (§6)
    | {platform: "darwin", start_tvsec, start_tvusec}  # PROC_PIDTBSDINFO (§6)
  ),
  reclaimed_from_epoch?           # present when this reclaimed a stale lease
}
                                  # The incarnation TOKEN is deliberately absent: journaling it
                                  #   would let any journal reader forge authority (§6)

cancel.requested {
  requested_by                    # cli — recorded for provenance; no other origin exists in
                                  #   v0.2, and the field exists so a future one is additive
}

journal.tail_truncated {
  truncated_seq,                  # the seq the discarded line would have occupied
  discarded_bytes                 # length only. The bytes themselves are NOT retained: they
                                  #   are unparseable and may be a torn fragment of anything,
                                  #   including a session hint (§4 privacy)
}

log      { level, message }       # mirrors the wire notification (§4), sanitized and truncated
                                  #   to 4 KiB on a valid UTF-8 boundary
progress { message }              # likewise
```

`log` and `progress` are the two observational rows: journaled for a later client, read by no
projection, and incapable of changing state (B.0 authority).

**Recovery-halt conditions are not events.** Every condition in Appendix D's closed
**Recovery halts** set stays outside the journal. They are conditions under which the core refuses
to proceed and reports to the operator — appending while journal integrity, referenced state, or
cleanup safety is in question is exactly the wrong response. They surface through command output
and exit 5, never through the log they distrust.

---

# Appendix C — Recovery

Normative. Recovery replays the journal, rebuilds every projection, and then resumes from the
**last durable state**. It is organised top-down over the whole run, and within each table rows are
evaluated **top-down; the first matching row wins**.

## C.0 Recovery surfaces and coverage registry

Recovery totality is indexed by the command surface, not by durable state alone:

```text
(recovery surface, durable projection, required external observations)
    → exactly one recovery action or one named Appendix D halt
```

The surface is part of the input because the run lifecycle, application, and promotion projections
are separate axes (§6). A terminal `SUCCEEDED` run may simultaneously require application recovery;
`resume` and `apply --recover` then correctly select different actions for the same journal.

| Recovery surface | Projection owned | Normative selection owner |
|---|---|---|
| `resume` | Run, movement, attempt, acceptance, decision, amendment, authority, and execution-interval recovery | This appendix, with the existing referenced preprocessing rules below |
| `apply --recover` | Application projection | §8's apply transaction and recovery rule |
| `promote-score --recover` | Promotion projection | §8's promotion transaction and recovery rule |

**Stable recovery case identifiers.** Every selectable case has a stable `RC-*` identifier.
Identifiers survive row moves and rewrites. If a case is retired, its identifier remains as a
tombstone and is never reused or silently assigned to a later case, under the same stability rule as
the compiler rule numbers (§2). An `open` case identifies a known hole; it is not an action and
cannot satisfy totality until a later revision supplies the action or halt while retaining the
identifier.

The shipping cases below only index rules already normative in §7 and §8; this appendix does not
restate their transaction branches.

| Recovery case | Surface | Existing owning rule |
|---|---|---|
| `RC-APPLY-001` | `apply --recover` | §8 recovery from `APPLYING` or `RECOVERY_REQUIRED` |
| `RC-APPLY-002` | `apply --recover` | §7 refusal of `--recover` outside those states |
| `RC-PROMOTE-001` | `promote-score --recover` | §8 recovery from `PROMOTING` or `RECOVERY_REQUIRED` |
| `RC-PROMOTE-002` | `promote-score --recover` | §7 refusal of `--recover` outside those states |

The following `resume` cases likewise index existing rules outside Appendix C rather than copying
them into a second normative sequence. Journal replay applies them before or while evaluating the
tables below as their owning clauses require:

| Recovery case | Existing owning rule |
|---|---|
| `RC-RESUME-034` | §1 torn-tail repair and manifest rebuild during journal replay |
| `RC-RESUME-035` | §1 orphan artifact, routed-proposal record, and change-set-ref cleanup; §9 unreferenced pre-prepare snapshot cleanup |
| `RC-RESUME-036` | §6/§9 inert orphan plan and quiesced-sidecar cleanup |
| `RC-RESUME-038` | §2 reconstruction of an orphaned core-finalization proposal on the next `resume` |

The known-hole table is deliberately retained even when empty: a later gap must be named here rather
than hidden in a coverage row, and the mechanical guard checks the table's shape. No known holes
remain after the branch expansion in C.4.

| Recovery case | Planned closure | Open gap |
|---|---|---|

The registry is a coverage index, not another recovery procedure. `direct` means a case predicate
names the event or disposition; `structural` means a broader projected-state or phase predicate owns
it; `neutral` means it changes no projection used for action selection; `separate-surface` delegates
it to one of the two §8 surfaces above. `open` entries point to the stable gap identifiers above.

| Kind | Entry | Classification | Cases | Coverage |
|---|---|---|---|---|
| event | `run.started` | structural | `RC-RESUME-010`, `RC-RESUME-043` | covered |
| event | `run.succeeded` | direct | `RC-RESUME-002` | covered |
| event | `run.failed` | direct | `RC-RESUME-002` | covered |
| event | `run.cancelled` | direct | `RC-RESUME-002` | covered |
| event | `movement.ready` | direct | `RC-RESUME-043` | covered |
| event | `movement.started` | direct | `RC-RESUME-043`, `RC-RESUME-044` | covered |
| event | `movement.succeeded` | structural | `RC-RESUME-002`, `RC-RESUME-043` | covered |
| event | `movement.failed` | direct | `RC-RESUME-020`, `RC-RESUME-047` | covered |
| event | `movement.cancelled` | structural | `RC-RESUME-002` | covered |
| event | `performer.selected` | direct | `RC-RESUME-013` | covered |
| event | `attempt.started` | direct | `RC-RESUME-014` | covered |
| event | `adapter.probed` | direct | `RC-RESUME-015` | covered |
| event | `performer.completed` | direct | `RC-RESUME-016`, `RC-RESUME-017` | covered |
| event | `attempt.completed` | direct | `RC-RESUME-019` | covered |
| event | `attempt.blocked` | direct | `RC-RESUME-040`, `RC-RESUME-048` | covered |
| event | `attempt.failed` | direct | `RC-RESUME-039` | covered |
| event | `attempt.cancelled` | structural | `RC-RESUME-002` | covered |
| event | `attempt.superseded` | structural | `RC-RESUME-042` | covered |
| event | `execution.started` | structural | `RC-RESUME-001`, `RC-RESUME-044` | covered |
| event | `execution.stopped` | structural | `RC-RESUME-015`, `RC-RESUME-044` | covered |
| event | `artifact.recorded` | structural | `RC-RESUME-010`, `RC-RESUME-015`, `RC-RESUME-016`, `RC-RESUME-017` | covered |
| event | `change_set.recorded` | direct | `RC-RESUME-018` | covered |
| event | `verification.passed` | direct | `RC-RESUME-018` | covered |
| event | `composition.conflicted` | direct | `RC-RESUME-011` | covered |
| event | `composition.failed` | direct | `RC-RESUME-011` | covered |
| event | `application_candidate.recorded` | direct | `RC-RESUME-043` | covered |
| event | `acceptance.started` | direct | `RC-RESUME-031`, `RC-RESUME-032`, `RC-RESUME-033` | covered |
| event | `criterion.started` | direct | `RC-RESUME-024` | covered |
| event | `criterion.completed` | direct | `RC-RESUME-023`, `RC-RESUME-025`, `RC-RESUME-033` | covered |
| event | `acceptance.failed` | direct | `RC-RESUME-022` | covered |
| event | `acceptance.evaluation_completed` | direct | `RC-RESUME-026`, `RC-RESUME-030` | covered |
| event | `decision.requested` | direct | `RC-RESUME-027`, `RC-RESUME-040`, `RC-RESUME-048` | covered |
| event | `decision.resolved` | direct | `RC-RESUME-028`, `RC-RESUME-029`, `RC-RESUME-041` | covered |
| event | `decision.obsoleted` | structural | `RC-RESUME-002`, `RC-RESUME-041`, `RC-RESUME-042` | covered |
| event | `amendment.rejected` | structural | `RC-RESUME-041` | covered |
| event | `amendment.approval_prepared` | direct | `RC-RESUME-007` | covered |
| event | `amendment.approval_abandoned` | structural | `RC-RESUME-036`, `RC-RESUME-043` | covered |
| event | `amendment.routed_human` | direct | `RC-RESUME-037` | covered |
| event | `amendment.approved` | structural | `RC-RESUME-003`, `RC-RESUME-042`, `RC-RESUME-043` | covered |
| event | `amendment.human_rejected` | structural | `RC-RESUME-041` | covered |
| event | `apply.started` | separate-surface | `RC-APPLY-001` | covered |
| event | `apply.completed` | separate-surface | `RC-APPLY-002` | covered |
| event | `apply.failed` | separate-surface | `RC-APPLY-002` | covered |
| event | `apply.recovery_required` | separate-surface | `RC-APPLY-001` | covered |
| event | `apply.recovery_resolved` | separate-surface | `RC-APPLY-002` | covered |
| event | `score.promotion_started` | separate-surface | `RC-PROMOTE-001` | covered |
| event | `score.promoted` | separate-surface | `RC-PROMOTE-002` | covered |
| event | `score.promotion_recovery_required` | separate-surface | `RC-PROMOTE-001` | covered |
| event | `authority.granted` | direct | `RC-RESUME-003`, `RC-RESUME-008`, `RC-RESUME-046` | covered |
| event | `cancel.requested` | direct | `RC-RESUME-006` | covered |
| event | `journal.tail_truncated` | neutral | — | covered |
| event | `log` | neutral | — | covered |
| event | `progress` | neutral | — | covered |
| disposition | `quality_retry` | direct | `RC-DISPOSITION-001` | covered |
| disposition | `fallback` | direct | `RC-DISPOSITION-002` | covered |
| disposition | `none` | direct | `RC-DISPOSITION-003` | covered |

**Before any table below runs, recovery closes any open execution interval — unless a C.1 control
row will close it with a more specific reason, or a C.2 unfinished-adapter row must first verify its
recorded session empty.** §6 requires the close, and every other row would otherwise have to remember
it: an `execution.started` with no matching stop is closed with
`execution.stopped {reason: recovered, charging: clamped}` first, so budget consumption is correct
before any admission decision reads it. A row that computes a disposition against a stale remainder
would charge the wrong thing.

**Recovery case:** `RC-RESUME-001`.

The exception matters, because closing it here would record the wrong reason and the wrong order.
**Cancellation** closes its interval in step (c) of the §6 oracle with `reason: cancelled`, whenever one
is open and independently of whether (d) fences anything; **supersession** closes it with
`reason: superseded`. So recovery checks C.1's
control rows *first* and performs the generic close only for an interval no row claims. The reason is
not cosmetic: it is how a later reader distinguishes a run that was cancelled from one that merely
crashed.

The C.2 exception is about containment rather than the reason label. For an open `adapter` interval
before `performer.completed`, the matching C.2 row sweeps the recorded launch first and then closes
the interval `recovered`/`clamped`. A generic pre-table close would stop charging before the survivor
check that determines whether the old attempt is harmless. If that sweep is unverifiable, recovery
halts with the interval still open; it neither charges twice nor starts a successor beside a
possibly-live predecessor.

Two further rules govern everything below, and every row obeys them rather than restating them:

1. **Recovery never recomputes a decision the live process already made.** Where a disposition,
   charge, or admissibility outcome was recorded, recovery realizes it per §3.1's second arm. Where
   recovery must *originate* a decision, the table gives a deterministic rule — never "recompute".
2. **Recovery never claims an outcome it did not verify.** Anything unverifiable is a halt
   (Appendix D), not a guess.

## C.1 Run-level precedence

Evaluated before any attempt- or acceptance-level table, because a pending control request outranks
resuming work.

**A row selects a recovery action, never a command-visible outcome.** Several commands drive the
same table, so a row that fixed an exit code would fix it for all of them: `cancel` reaches C.1
having *already* durably appended `cancel.requested`, and reporting a precondition refusal for an
invocation that has mutated authoritative state is not a refusal at all. §7 maps each command's
outcome from what that command did after the selected action. This is why no row below names a
command, and why the same row can leave `resume` with nothing to do and send `cancel` on to §6.

The rows:

| Recovery case | Last durable state | Recovery action |
|---|---|---|
| `RC-RESUME-002` | Run is terminal | Complete any derived projections idempotently (`movement.cancelled`, `attempt.cancelled`, `decision.obsoleted`), **and finish any residual non-journal cleanup**: remove a stale `driver.lease` or quiesced sidecar, an orphan plan record, and the run's staging root. This is what makes `(f)` crash-closed — a crash between `(e)` and `(f)` makes this row win, and the cancellation row can no longer match because the run is now terminal, so without cleanup here `(f)` would never be retried. Terminality protects the driver CAS; it must not also strand the filesystem. Launch nothing |
| `RC-RESUME-003` | A readable `driver.lease` is at an epoch **older** than the journal-projected one | It is stale, not dangerous: the mutation CAS requires a lease at the current epoch (§6), so its owner cannot mutate whatever its liveness. Remove it and re-evaluate this table from the top. This is what clears a lease stranded by a crash between a fencing terminal event and its cleanup (Appendix E) |
| `RC-RESUME-004` | A `driver.lease` exists with no `authority.granted` at its epoch | An orphan from a crashed acquisition (§6): quarantine it and **re-evaluate this table from the top**. Reclamation is deliberately not performed here — this row sits above the cancellation and pending-prepare rows, and reclaiming authority while a prepare is pending is exactly what that row forbids. Once the orphan is gone, whichever row genuinely applies wins, including the no-live-owner reclaim below |
| `RC-RESUME-005` | A `driver.lease` exists **at the current journal-projected epoch** and its owner is **unverifiable** | Halt `owner_unverifiable`. This outranks even a pending cancellation: cancellation outranks *resumption*, never the safety check that terminalizing requires. Declaring a run cancelled while a possibly-live owner could still mutate it is the one thing §6 forbids outright. The check is scoped to a **current** lease deliberately — the CAS needs one, so an owner without one is already unable to act, and an unscoped check halts on states that are provably safe: after `authority.granted` but before the lease exists, after the lease has moved to a quiesced sidecar, and after a fence has advanced the epoch past it |
| `RC-RESUME-046` | A `driver.lease` exists at the current journal-projected epoch and its matching owner is verifiably live | Yield to the live owner. The current owner already holds continuation authority; recovery neither reclaims its lease nor enters C.2 beside it, and appends no event. §7 maps the outcome of the invoking command after this action |
| `RC-RESUME-006` | `cancel.requested` present, run nonterminal | **Cancellation takes precedence over resumption and over a pending prepare.** Execute **steps (a)–(f) of the §6 cancellation oracle exactly** — the whole list, including `(e)`'s `run.cancelled` and `(f)`'s lease cleanup; an earlier draft stopped at `(d)`, which left recovery unable to terminalize a cancelling run at all — including the conditional `(c)` and `(d)` — this row deliberately does not restate them, because two copies of a sequence are two chances to disagree. Three notes specific to recovery: `sweep_unverifiable` in (a) halts, since C.1 runs before C.2/C.3 and would otherwise terminalize without consulting the process identities those tables rely on; (c)'s interval close — which has its own predicate, independent of whether (d) fences — is why the generic pre-table close skips a cancelled run; and **no replacement driver is launched** |
| `RC-RESUME-007` | `amendment.approval_prepared` pending, no matching `amendment.approved` or `amendment.approval_abandoned`, no cancellation | **Complete or abandon the prepare — never step past it**, and the **mutation barrier stays in force** while doing so. Verify **both** referenced files, because first-match ordering means the generic missing-file checks below are never reached: `prepares/<prepare-id>.json` against its recorded hash (`missing_prepare_plan`), and the prewritten snapshot against its recorded raw *and* semantic hashes and its binding to that plan (`missing_snapshot_file`). Sweep every recorded adapter and criterion launch to verified empty (`sweep_unverifiable` halts). Then run §6's commit table exactly as the original approver would have, appending `amendment.approved` **from the persisted plan** — or `amendment.approval_abandoned` if the head changed or the plan no longer validates. Reclaiming authority or entering C.2 while a prepare is pending would let a new driver run against a revision that was about to change |
| `RC-RESUME-008` | An `authority.granted` epoch exists with no live owner | Reclaim per §6 |
| `RC-RESUME-009` | `root_snapshot_divergence` — the root score claims the snapshot's revision with a different semantic hash | Halt (§1). Resume is impossible until an operator resolves it |
| `RC-RESUME-010` | A run-level snapshot, artifact, proposal record, ref, or `resolved-cast.yaml` named by an event is missing or hash-mismatched | Halt with the matching reason (Appendix D). The journal is the authority and this is corruption |
| `RC-RESUME-037` | `amendment.routed_human` durable, its matching `decision.requested` absent | Apply §1's source-to-request rule idempotently, then re-evaluate C.1. The routed event fixes the request payload; recovery does not re-run routing or infer it from `attempt.blocked` |
| `RC-RESUME-042` | `amendment.approved` established the current head, the run remains nonterminal, and an affected movement has no attempt on that head | Replay the approval's derived supersession and decision-obsoletion projections, select the `revision_restart` continuation already fixed by §4 and §9, and hand its materialization to the between-unit scheduler (`RC-RESUME-043`). Do not enter C.2 or C.3 for an attempt from the superseded revision |
| `RC-RESUME-011` | `composition.conflicted` or `composition.failed` durable, its terminal event missing | Append idempotently **the terminal event B.3 gives for that evidence type and scope** — the mapping is B.3's and is not restated here. This row sits below the control rows deliberately: a `cancel.requested` landing between the evidence and its terminal outranks it, which is the qualification B.3 already carries rather than a second precedence. Composition runs between attempts, so nothing on this row enters C.2 |
| `RC-RESUME-012` | Otherwise | Proceed to C.2 for the movement's current-head, non-superseded in-flight attempt, if any; when there is none, proceed to the between-unit scheduler in C.4 |

## C.2 Attempt lifecycle recovery

The window between `performer.selected` and `acceptance.started` was previously undefined, which
left those states permanently stranded. `attempt_terminated_incomplete` is a quality failure whose
disposition is classified by §3.1's first arm and recorded on the synthesized event, so C.3's
replay rule holds.

**C.2 and C.3 inspect only an attempt on the current score head that is not projected
`SUPERSEDED`.** An event-tail predicate from an older revision cannot make its attempt current:
recovery never verifies, fails, completes, or resumes that historical attempt. After
`amendment.approved`, `RC-RESUME-042` owns revision-continuation selection, and a state with no
current-head attempt belongs to the between-unit scheduler rather than falling through these
tables. A terminal `BLOCKED` attempt whose last blocking decision has become terminal likewise
leaves the attempt lifecycle; `RC-RESUME-041` selects its continuation and C.4 materializes it.
Its source/request and intentional-wait cuts are closed below.

| Recovery case | Last durable state | Recovery action |
|---|---|---|
| `RC-RESUME-039` | Current-head `attempt.failed` durable, its recorded §3.1 Arm 2 consequence absent | Realize that recorded disposition exactly once per §3.1's second arm, reading no current budget or admissibility state. A `none` consequence appends the prescribed `movement.failed` and re-evaluates from C.1. A `quality_retry` or `fallback` consequence selects the recorded pending successor and exits C.2 to the between-unit scheduler; this row does not append `performer.selected` or launch it |
| `RC-RESUME-040` | Current-head `attempt.blocked` has a `question` in `raised` whose matching `decision.requested` is absent | Append the first missing request idempotently from the recorded source, in `raised` order, then re-evaluate this row. Proposal requests never match this row; their sole source is `amendment.routed_human` and `RC-RESUME-037` owns that cut |
| `RC-RESUME-041` | Current-head `attempt.blocked`, every request required by a durable source is durable, no blocking decision remains unresolved, the movement and run remain nonterminal, and the score head is the attempt's revision | Select the `decision_resume` continuation §4 already fixes, preserving the blocked attempt's performer and fallback position as §3.1 requires, then hand its materialization to C.4. A terminal run is owned by `RC-RESUME-002`; an approved revision is owned by `RC-RESUME-042`; finalization that completed the draft movement returns to C.1/C.4. Those are the decision-obsoletion branches, so this row never turns obsoletion into an invented same-revision retry |
| `RC-RESUME-048` | Current-head `attempt.blocked`, every request required by a durable source is durable, and at least one blocking decision unresolved | Append nothing and return the quiescent `WAITING_HUMAN` result §6 and §7 already define. No adapter or criterion process remains: `attempt.blocked` is terminal and §4's execute-completion boundary precedes it |
| `RC-RESUME-013` | `performer.selected`, no `attempt.started` | **No adapter body was ever released** (§4 gate). Run §4's shared bounded handoff stabilization rather than classifying its first sample. If it yields a matching identity, verify and sweep that session. If it yields marker-free, rely only on the stated **no released mutator survives** property — an unreleased trampoline may still be in its pre-marker window but contains no adapter code. Either halt outcome stops this row. Only after a published session is verified empty or the marker is observed free, close any open `adapter` interval `recovered`/`clamped`; then append `attempt.failed {kind: task_failed, reason: attempt_never_started, disposition}` and realize it per §3.1's second arm |
| `RC-RESUME-014` | `attempt.started`, no `adapter.probed` | Sweep the **recorded** adapter session to verified empty **before** any failure or fallback — whether its leader is live, dead, or already a zombie, since a survivor holds repository authority either way. Inspection failure is `sweep_unverifiable`. Then close any open `adapter` interval `recovered`/`clamped`. No durable valid probe observation exists, so append `attempt.failed {kind: adapter_unavailable, reason: probe_terminated_incomplete, disposition}`, then realize it per §3.1. Recovery does not manufacture the missing observation |
| `RC-RESUME-015` | `adapter.probed`, **no** `performer.completed` | Sweep the recorded adapter session to verified empty before any failure or retry. Inspection failure is `sweep_unverifiable`. Then close any open `adapter` interval `recovered`/`clamped` and append `attempt.failed {kind: task_failed, reason: attempt_terminated_incomplete, disposition}`. The `performer.completed` exclusion matters: that event attests both verified-empty cleanup and the prior interval close, so recovery may enter verification without repeating either; without the exclusion this row would turn every normal completion into an incomplete attempt |
| `RC-RESUME-016` | `performer.completed`, movement holds `repo_write`, no `change_set.recorded` | The worktree still exists and its tree is authoritative: capture the change set idempotently, then continue at the next row. If the worktree is gone, the candidate cannot be reconstructed — append `attempt.failed {kind: task_failed, reason: worktree_lost, disposition}` |
| `RC-RESUME-017` | `performer.completed`, no `verification.passed` | Re-run the **full** §5 post-hoc verification — protected paths for every movement, plus the read-only invariant where the movement holds no `repo_write` — against the surviving worktree; if it is gone, `attempt.failed {kind: task_failed, reason: worktree_lost, disposition}`. A durable `verification.passed` event marks the boundary, because without one a crash after `change_set.recorded` but before the check would let recovery start acceptance having verified nothing |
| `RC-RESUME-018` | `change_set.recorded` or `verification.passed`, no `acceptance.started` | Begin acceptance: append `acceptance.started` and proceed to C.3 |
| `RC-RESUME-019` | `attempt.completed`, no `movement.succeeded` | Append `movement.succeeded` idempotently — including, for the final movement, the run's `SUCCEEDED` transition (§8). Without this row a crash between the two appends left the movement, and possibly the run, nonterminal forever |
| `RC-RESUME-020` | `movement.failed`, run not terminal | Append `run.failed {reason: movement_failed}` idempotently. The movement's own reason is **not** propagated: most `movement.failed` reasons are not valid `run.failed` values (Appendix D), and `movement_failed` is the run-level reason that exists for exactly this. The movement's reason stays readable on its own event |
| `RC-RESUME-021` | Gate resolved reject on the **final** movement, no terminal event | Append `movement.failed {reason: human_gate_rejected, run_failed: true, decision_id, subject_tree}` — one atomic transition (§8), so no separate `run.failed` follows |

**Recovery case:** `RC-RESUME-047`.

There is no contrary branch to `RC-RESUME-020`. B.1 makes `movement.failed` the movement's terminal
transition, and §6 defines movement failure as occurring only when no execution path remains.
Its producers — §3.1 selecting `none`, composition terminalizing its scope, a rejected gate, and
budget exhaustion — all satisfy that definition. v0.2 has no optional failed movement, and every successful run
requires every applicable movement to succeed, directly on the waived path or through the final
sink (§8). The old phrase “and no further execution path authorized” therefore tried to decide
again what the durable event already proves. Removing it makes the predicate closed instead of
inventing an unreachable alternative.

## C.3 Acceptance recovery

Before resuming any criterion **or synthesizing any event that claims acceptance succeeded** —
`acceptance.evaluation_completed`, `attempt.completed`, or a post-gate `movement.succeeded` — the core
re-verifies the worktree against the **full invariant** —
tracked content equal to the `subject_tree` recorded in `acceptance.started`, plus non-ignored
untracked files, symlink targets, modes, and protected-path integrity; on any mismatch it records
`acceptance.failed {reason: recovery_subject_mismatch, disposition}`. The event is keyed on
`attempt_id` (Appendix B); the causation id is evidence, not the key.

**Every `acceptance.failed` recovery synthesizes carries a disposition**, classified by §3.1's first
arm: an acceptance failure is that rule's quality class, and every input it reads is a fact already
projected from the journal rather than a judgement recovery originates.

| Recovery case | Last durable state | Recovery action |
|---|---|---|
| `RC-RESUME-022` | `acceptance.failed` present | Terminal — synthesize no further criterion results. The attempt is already `FAILED` by that event's own projection (B.3); realize the **recorded** disposition exactly once per §3.1's second arm — never recompute admissibility at recovery |
| `RC-RESUME-023` | Any `criterion.completed` is `FAIL` or `ERROR`, no `acceptance.failed` | Append `acceptance.failed` idempotently — which terminalizes the attempt as `FAILED` — and start no further criterion |
| `RC-RESUME-024` | `criterion.started` without `criterion.completed` | **First sweep the recorded criterion session to verified empty** (nothing to sweep when `spawn_failed`) — otherwise an orphan could still be mutating the worktree about to be verified; unverifiable process state halts recovery. Then re-verify the worktree, and branch on the result: a **mismatch** is `acceptance.failed {reason: recovery_subject_mismatch}`, because the tree no longer matches what acceptance bound and no verdict about it is meaningful; only a **clean** re-verification synthesizes `criterion.completed {outcome: ERROR, error_detail: "recovered_without_observed_completion"}` with `exit_code`, `duration_ms`, and `output_ref` **absent**, followed by `acceptance.failed`. Either way it closes as a failure even when the command in fact passed but crashed before its event was written: recovery reports what it observed, and it observed no completion |
| `RC-RESUME-025` | All criteria completed, all `PASS`, no `acceptance.evaluation_completed` | Append `acceptance.evaluation_completed` idempotently |
| `RC-RESUME-026` | `acceptance.evaluation_completed`, required human gate not yet requested | Resume at the gate step; append one `decision.requested` idempotently |
| `RC-RESUME-027` | `decision.requested` (gate) unresolved | Append nothing. The unresolved decision **is** the `WAITING_HUMAN` projection (§6) — there is no state event to restore |
| `RC-RESUME-028` | Gate resolved approve, `attempt.completed` missing | Append `attempt.completed`, then `movement.succeeded` — in that order (B.1/B.2) — idempotently, including for the final movement the run's `SUCCEEDED` transition |
| `RC-RESUME-029` | Gate resolved reject, terminal failure event missing | Append `movement.failed {reason: human_gate_rejected, decision_id, subject_tree}` idempotently, keyed on `movement_id` (Appendix B) with the gate decision id carried as causation and evidence; that one event terminalizes attempt, movement, and — for the final movement — the run |
| `RC-RESUME-030` | `acceptance.evaluation_completed`, no gate required, `attempt.completed` missing | Append `attempt.completed`, then `movement.succeeded`, idempotently |
| `RC-RESUME-031` | `acceptance.started`, an **unjournaled** `launch_id` directory present | A criterion launch crashed before its `criterion.started` append. Run §4's same bounded handoff stabilization. If it yields a matching identity, verify and sweep that session (`sweep_unverifiable` halts); if it yields marker-free, no released criterion mutator survives; either halt outcome stops this row. Only after the identity's session is verified empty or the marker is observed free, remove the directory and continue with the rows below — the criterion never started as far as the journal is concerned, but an unreleased pre-marker trampoline may still be exiting on gate EOF |
| `RC-RESUME-032` | `acceptance.started`, no criterion events | Resume with the first criterion |
| `RC-RESUME-033` | Some `criterion.completed` (all `PASS`), none in flight, criteria remaining | Resume with the next unstarted criterion |

## C.4 Between-unit continuation and branch expansion

C.4 runs only after C.1 has selected no higher-precedence run action and C.2/C.3 have selected no
current-head, non-superseded in-flight attempt action. It is the owner of a nonterminal run with no
such attempt. It does not select a successor that §3.1, §4, or §9 already selected; it makes that
selection durable or advances the score's already-compiled lifecycle.

| Recovery case | Last durable state | Recovery action |
|---|---|---|
| `RC-RESUME-045` | No current-head, non-superseded attempt is in flight and `remaining_time == 0` | Take §6's existing budget-exhaustion path for the unit that would otherwise run. A `RUNNING` movement awaiting an attempt or movement fan-in receives `movement.failed {budget_exhausted}` and then `RC-RESUME-020`; candidate composition, or exhaustion between movements before another movement is `RUNNING`, receives `run.failed {budget_exhausted}` directly. Append no `performer.selected`, `movement.ready`, or `movement.started` after exhaustion |
| `RC-RESUME-044` | The generic pre-table close recorded `execution.stopped {phase: composition, reason: recovered}`, no `composition.conflicted` or `composition.failed` evidence exists for that interval's subject, and budget remains | Re-run the same §5 deterministic composition from its durable inputs. A movement fan-in resumes the current `RUNNING` movement; candidate composition resumes the run-scoped materialization. Record a conflict or execution failure only if this invocation observes that verdict; on success continue through `RC-RESUME-043`. The recovered close alone is not failure evidence and never synthesizes `composition.failed` |
| `RC-RESUME-043` | No row above applies and the run is nonterminal with no current-head, non-superseded attempt in flight | Advance exactly one scheduler step, then re-evaluate from C.1. First materialize any pending successor already selected by §3.1, `RC-RESUME-041`, or `RC-RESUME-042` as its one `performer.selected`. Otherwise apply the compiled score in declaration order: make the first dependency-satisfied `PENDING` movement `READY`; start the first `READY` movement; for a `RUNNING` movement with no attempt, perform its §5 fan-in and durably select the `initial` attempt; after movement success, repeat readiness; when §8's candidate precondition holds, compose and record the candidate; after that event the final movement enters the same readiness path. On the waived path, when §8's completion predicate holds, append its single candidate-carrying `run.succeeded`. The scheduler never bypasses §2's final-movement or `apply_gate` rules |

`RC-RESUME-048` is the intentional fixed point before this scheduler: an unresolved blocking
decision selects “return quiescent in `WAITING_HUMAN`”. It is an action with no append, not a state
that falls through to C.4. Conversely, a non-blocking decision does not stop scheduling, and a
terminal blocking decision reaches `RC-RESUME-041`, `RC-RESUME-042`, terminal cleanup, or the
finalization branch of `RC-RESUME-043`; decision obsoletion never creates an extra retry category.

### C.4.1 Finite selection model

The mechanical expansion operates on **selection cuts**: journal replay and the required external
observations have been projected, but the selected recovery action has not yet run. An action that
appends or cleans up one fact produces another cut and selection starts again at C.0. This keeps the
model finite without pretending that a multi-step recovery is atomic.

The axes are normative. `irrelevant` means an earlier branch has made that axis unreachable at this
cut; it does not mean the implementation may ignore a value that could change the selected branch.

| Axis | Values | Why it discriminates |
|---|---|---|
| `surface` | `resume`, `apply`, `promote` | The same journal may select different actions on the three recovery surfaces |
| `run` | `active`, `terminal`, `irrelevant` | Terminal cleanup outranks continuation, while shipping recovery is on a separate projection |
| `integrity` | `valid`, `repair`, `halt`, `irrelevant` | Replay repair may proceed, but an untrusted authoritative input must halt before mutation |
| `owner` | `clear`, `stale`, `orphan`, `unverifiable`, `live`, `unowned`, `irrelevant` | Lease cleanup, refusal, halt, and authority reclamation are distinct results |
| `control` | `none`, `cancel`, `prepare`, `irrelevant` | Cancellation and a pending prepare outrank ordinary lifecycle continuation |
| `consequence` | `none`, `interval`, `request`, `revision`, `disposition`, `composition_terminal`, `lifecycle_terminal`, `irrelevant` | Already-determined durable consequences must close before new work |
| `unit` | `none`, `attempt`, `acceptance`, `movement_composition`, `candidate_composition`, `application`, `promotion` | It separates attempt/criterion recovery, between-unit composition, and the two shipping projections |
| `phase` | `idle`, `tail_repair`, `orphan_cleanup`, `control_cleanup`, `finalization_rebuild`, `root_divergence`, `missing_reference`, `interval_open`, `revision_changed`, `selected`, `started`, `probed`, `performed`, `verified`, `acceptance_ready`, `completed`, `failed`, `blocked`, `acceptance_empty`, `criterion_pending`, `criterion_running`, `criterion_failed`, `criterion_next`, `criteria_passed`, `evaluated`, `gate_free`, `gate_open`, `gate_approved`, `gate_rejected`, `ready`, `running`, `interrupted`, `recoverable`, `refused` | The last durable phase selects the idempotent continuation within a unit |
| `decision` | `none`, `unresolved`, `released`, `irrelevant` | An unresolved blocking decision selects quiescence; a released one selects a new attempt |
| `budget` | `available`, `exhausted`, `irrelevant` | New active work is forbidden at zero, including work between attempts |
| `observation` | `safe`, `owner_unverifiable`, `handoff_unverifiable`, `sweep_unverifiable`, `irrelevant` | A required process or filesystem observation may turn a continuation into a specific named halt |

The **reachable recovery selection cuts** table is the finite state expansion. Each row denotes one
reachable combination; comma-separated cells denote the Cartesian alternatives in that cell.
No wildcard is allowed here. The alternatives expose precedence interleavings rather than hiding
them in prose. A combination not generated by this table is outside the declared reachable model.

| Cut | surface | run | integrity | owner | control | consequence | unit | phase | decision | budget | observation |
|---|---|---|---|---|---|---|---|---|---|---|---|
| `RS-001` | apply | irrelevant | irrelevant | irrelevant | irrelevant | irrelevant | application | recoverable | irrelevant | irrelevant | irrelevant |
| `RS-002` | apply | irrelevant | irrelevant | irrelevant | irrelevant | irrelevant | application | refused | irrelevant | irrelevant | irrelevant |
| `RS-003` | promote | irrelevant | irrelevant | irrelevant | irrelevant | irrelevant | promotion | recoverable | irrelevant | irrelevant | irrelevant |
| `RS-004` | promote | irrelevant | irrelevant | irrelevant | irrelevant | irrelevant | promotion | refused | irrelevant | irrelevant | irrelevant |
| `RS-005` | resume | irrelevant | repair | clear | none | none | none | tail_repair,orphan_cleanup,control_cleanup,finalization_rebuild | none | available | safe |
| `RS-006` | resume | active | halt | clear | none,cancel,prepare | none | none | root_divergence,missing_reference | none | available | safe |
| `RS-007` | resume | terminal | valid | clear,stale,orphan | none,cancel,prepare | none,lifecycle_terminal | none | idle | none,unresolved | available,exhausted | safe |
| `RS-008` | resume | active | valid | stale | none,cancel,prepare | none | none | idle | none | available | safe |
| `RS-009` | resume | active | valid | orphan | none,cancel,prepare | none | none | idle | none | available | safe |
| `RS-010` | resume | active | valid | unverifiable | none,cancel,prepare | none | none | idle | none | available | owner_unverifiable |
| `RS-011` | resume | active | valid | live | none,cancel,prepare | none | none | idle | none | available | safe |
| `RS-012` | resume | active | valid | clear,unowned | cancel | none,request,disposition,composition_terminal | none,attempt,acceptance,movement_composition,candidate_composition | idle,failed,interrupted | none,unresolved,released | available,exhausted | safe |
| `RS-013` | resume | active | valid | clear,unowned | prepare | none | none | idle | none | available | safe |
| `RS-014` | resume | active | valid | unowned | none | none | none | idle | none | available | safe |
| `RS-015` | resume | active | valid | clear | none | request | none | blocked | none | available | safe |
| `RS-016` | resume | active | valid | clear | none | disposition | attempt,acceptance | failed | none | available | safe |
| `RS-017` | resume | active | valid | clear | none | composition_terminal | movement_composition,candidate_composition | completed | none | available | safe |
| `RS-018` | resume | active | valid | clear | none | lifecycle_terminal | attempt | completed,failed,gate_rejected | none | available | safe |
| `RS-019` | resume | active | valid | clear | none | none | attempt | selected | none | available | safe,handoff_unverifiable,sweep_unverifiable |
| `RS-020` | resume | active | valid | clear | none | none | attempt | performed,verified,acceptance_ready | none | available | safe |
| `RS-021` | resume | active | valid | clear | none | none | acceptance | gate_open | unresolved | available | safe |
| `RS-022` | resume | active | valid | clear | none | none | attempt | blocked | unresolved | available | safe |
| `RS-023` | resume | active | valid | clear | none | none | attempt | blocked | released | available | safe |
| `RS-025` | resume | active | valid | clear | none | none | none,attempt | idle,ready,running | none | available | safe |
| `RS-026` | resume | active | valid | clear | none | none | movement_composition,candidate_composition | interrupted | none | available | safe |
| `RS-027` | resume | active | valid | clear | none | none | none,attempt,movement_composition,candidate_composition | idle,failed,interrupted | none,released | exhausted | safe |
| `RS-029` | resume | active | valid | clear | none | none | acceptance | criterion_pending | none | available | safe,handoff_unverifiable,sweep_unverifiable |
| `RS-030` | resume | active | valid | clear | none | none | acceptance | criterion_failed,criterion_next,criteria_passed,evaluated,gate_free | none | available | safe |
| `RS-031` | resume | active | valid | clear | none | none | acceptance | gate_approved,gate_rejected | released | available | safe |
| `RS-032` | resume | active | valid | clear | none | request | attempt | blocked | none,unresolved | available | safe |
| `RS-033` | resume | active | valid | clear | none | interval | none | interval_open | none | available,exhausted | safe |
| `RS-034` | resume | active | valid | clear | none | revision | none | revision_changed | released | available | safe |
| `RS-035` | resume | active | valid | clear | none | none | acceptance | acceptance_empty | none | available | safe |
| `RS-036` | resume | active | valid | clear | none | none | attempt | started,probed | none | available | safe,sweep_unverifiable |
| `RS-037` | resume | active | valid | clear | none | none | acceptance | criterion_running | none | available | safe,sweep_unverifiable |

The **recovery action selection expansion** is independent of that reachability declaration. A
cell is either one axis value, a comma-separated set, or `*`. Lower numeric precedence wins.
Every generated cut must have exactly one winning row after precedence, and every action row must
win at least one cut.

| Action row | Precedence | Recovery case | surface | run | integrity | owner | control | consequence | unit | phase | decision | budget | observation | Selected result |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| `RA-001` | 10 | `RC-APPLY-001` | apply | * | * | * | * | * | application | recoverable | * | * | irrelevant | Execute §8 application recovery |
| `RA-002` | 10 | `RC-APPLY-002` | apply | * | * | * | * | * | application | refused | * | * | irrelevant | Refuse under §7 |
| `RA-003` | 10 | `RC-PROMOTE-001` | promote | * | * | * | * | * | promotion | recoverable | * | * | irrelevant | Execute §8 promotion recovery |
| `RA-004` | 10 | `RC-PROMOTE-002` | promote | * | * | * | * | * | promotion | refused | * | * | irrelevant | Refuse under §7 |
| `RA-005` | 20 | `RC-RESUME-034` | resume | irrelevant | repair | * | * | * | * | tail_repair | * | * | safe | Repair the torn tail, then re-evaluate |
| `RA-006` | 70 | `RC-RESUME-009` | resume | active | halt | * | none | * | * | root_divergence | * | * | * | Halt `root_snapshot_divergence` |
| `RA-007` | 30 | `RC-RESUME-002` | resume | terminal | * | * | * | * | * | * | * | * | safe | Complete terminal projections and cleanup |
| `RA-008` | 40 | `RC-RESUME-003` | resume | active | valid | stale | * | * | * | * | * | * | safe | Remove stale lease, then re-evaluate |
| `RA-009` | 40 | `RC-RESUME-004` | resume | active | valid | orphan | * | * | * | * | * | * | safe | Quarantine orphan lease, then re-evaluate |
| `RA-010` | 40 | `RC-RESUME-005` | resume | active | valid | unverifiable | * | * | * | * | * | * | owner_unverifiable | Halt `owner_unverifiable` |
| `RA-011` | 40 | `RC-RESUME-046` | resume | active | valid | live | * | * | * | * | * | * | safe | Yield to the live owner |
| `RA-012` | 50 | `RC-RESUME-006` | resume | active | valid,halt | clear,unowned | cancel | * | * | * | * | * | safe | Execute the §6 cancellation oracle |
| `RA-013` | 50 | `RC-RESUME-007` | resume | active | valid,halt | clear,unowned | prepare | none | none | idle,root_divergence,missing_reference | none | available | safe | Complete or abandon the prepare |
| `RA-014` | 60 | `RC-RESUME-008` | resume | active | valid | unowned | none | none | none | idle | none | available | safe | Reclaim authority |
| `RA-015` | 70 | `RC-RESUME-037` | resume | active | valid | clear | none | request | none | blocked | none | available | safe | Append the derived request |
| `RA-016` | 70 | `RC-RESUME-040` | resume | active | valid | clear | none | request | attempt | blocked | none,unresolved | available | safe | Append the missing question request |
| `RA-017` | 70 | `RC-RESUME-039` | resume | active | valid | clear | none | disposition | attempt | failed | none | available | safe | Realize the recorded attempt disposition |
| `RA-018` | 70 | `RC-RESUME-022` | resume | active | valid | clear | none | disposition | acceptance | failed | none | available | safe | Realize the recorded acceptance disposition |
| `RA-019` | 70 | `RC-RESUME-011` | resume | active | valid | clear | none | composition_terminal | movement_composition,candidate_composition | completed | none | available | safe | Append the evidence-selected terminal |
| `RA-020` | 70 | `RC-RESUME-019` | resume | active | valid | clear | none | lifecycle_terminal | attempt | completed | none | available | safe | Append `movement.succeeded` |
| `RA-021` | 70 | `RC-RESUME-020` | resume | active | valid | clear | none | lifecycle_terminal | attempt | failed | none | available | safe | Append `run.failed` |
| `RA-022` | 70 | `RC-RESUME-021` | resume | active | valid | clear | none | lifecycle_terminal | attempt | gate_rejected | none | available | safe | Append the atomic final-movement failure |
| `RA-023` | 80 | `RC-RESUME-013` | resume | active | valid | clear | none | none | attempt | selected | none | available | safe | Stabilize the handoff, then fail the unstarted attempt |
| `RA-024` | 80 | `RC-RESUME-014` | resume | active | valid | clear | none | none | attempt | started | none | available | safe | Sweep, then record the incomplete probe failure |
| `RA-025` | 80 | `RC-RESUME-015` | resume | active | valid | clear | none | none | attempt | probed | none | available | safe | Sweep, then record incomplete execution |
| `RA-026` | 80 | `RC-RESUME-016` | resume | active | valid | clear | none | none | attempt | performed | none | available | safe | Capture the change set or record worktree loss |
| `RA-027` | 80 | `RC-RESUME-017` | resume | active | valid | clear | none | none | attempt | verified | none | available | safe | Re-run post-hoc verification |
| `RA-028` | 80 | `RC-RESUME-018` | resume | active | valid | clear | none | none | attempt | acceptance_ready | none | available | safe | Append `acceptance.started` |
| `RA-029` | 80 | `RC-RESUME-031` | resume | active | valid | clear | none | none | acceptance | criterion_pending | none | available | safe | Stabilize and remove the unjournaled launch |
| `RA-030` | 80 | `RC-RESUME-024` | resume | active | valid | clear | none | none | acceptance | criterion_running | none | available | safe | Sweep and record only the observed recovery result |
| `RA-031` | 80 | `RC-RESUME-023` | resume | active | valid | clear | none | none | acceptance | criterion_failed | none | available | safe | Append `acceptance.failed` |
| `RA-032` | 80 | `RC-RESUME-033` | resume | active | valid | clear | none | none | acceptance | criterion_next | none | available | safe | Resume the next unstarted criterion |
| `RA-033` | 80 | `RC-RESUME-025` | resume | active | valid | clear | none | none | acceptance | criteria_passed | none | available | safe | Append evaluation completion |
| `RA-034` | 80 | `RC-RESUME-026` | resume | active | valid | clear | none | none | acceptance | evaluated | none | available | safe | Append the required gate request |
| `RA-035` | 80 | `RC-RESUME-030` | resume | active | valid | clear | none | none | acceptance | gate_free | none | available | safe | Complete the attempt and movement |
| `RA-036` | 80 | `RC-RESUME-028` | resume | active | valid | clear | none | none | acceptance | gate_approved | released | available | safe | Complete the attempt and movement |
| `RA-037` | 80 | `RC-RESUME-029` | resume | active | valid | clear | none | none | acceptance | gate_rejected | released | available | safe | Append the gate-selected failure |
| `RA-038` | 90 | `RC-RESUME-027` | resume | active | valid | clear | none | none | acceptance | gate_open | unresolved | available | safe | Return quiescent in `WAITING_HUMAN` |
| `RA-039` | 90 | `RC-RESUME-048` | resume | active | valid | clear | none | none | attempt | blocked | unresolved | available | safe | Return quiescent in `WAITING_HUMAN` |
| `RA-040` | 90 | `RC-RESUME-041` | resume | active | valid | clear | none | none | attempt | blocked | released | available | safe | Select `decision_resume` |
| `RA-041` | 90 | `RC-RESUME-045` | resume | active | valid | clear | none | * | none,attempt,movement_composition,candidate_composition | idle,failed,interrupted | none,released | exhausted | safe | Take the §6 budget-exhaustion path |
| `RA-042` | 100 | `RC-RESUME-044` | resume | active | valid | clear | none | none | movement_composition,candidate_composition | interrupted | none | available | safe | Re-run deterministic composition |
| `RA-043` | 100 | `RC-RESUME-043` | resume | active | valid | clear | none | none | none,attempt | idle,ready,running | none | available | safe | Advance one between-unit scheduler step |
| `RA-046` | 20 | `RC-RESUME-035` | resume | irrelevant | repair | * | * | * | * | orphan_cleanup | * | * | safe | Remove orphan artifacts, then re-evaluate |
| `RA-047` | 20 | `RC-RESUME-036` | resume | irrelevant | repair | * | * | * | * | control_cleanup | * | * | safe | Remove inert control artifacts, then re-evaluate |
| `RA-048` | 20 | `RC-RESUME-038` | resume | irrelevant | repair | * | * | * | * | finalization_rebuild | * | * | safe | Reconstruct the finalization proposal, then re-evaluate |
| `RA-049` | 70 | `RC-RESUME-010` | resume | active | halt | * | none | * | * | missing_reference | * | * | * | Halt with the matching Appendix D missing-reference reason |
| `RA-050` | 65 | `RC-RESUME-001` | resume | active | valid | clear | none | interval | none | interval_open | none | * | safe | Close the open interval, then re-evaluate |
| `RA-051` | 70 | `RC-RESUME-042` | resume | active | valid | clear | none | revision | none | revision_changed | released | available | safe | Select `revision_restart` |
| `RA-052` | 80 | `RC-RESUME-032` | resume | active | valid | clear | none | none | acceptance | acceptance_empty | none | available | safe | Resume the first criterion |
| `RA-053` | 75 | `RC-RESUME-013` | resume | active | valid | clear | none | none | attempt | selected | none | available | handoff_unverifiable | Halt `spawn_handoff_unverifiable` |
| `RA-054` | 75 | `RC-RESUME-031` | resume | active | valid | clear | none | none | acceptance | criterion_pending | none | available | handoff_unverifiable | Halt `spawn_handoff_unverifiable` |
| `RA-055` | 75 | `RC-RESUME-013` | resume | active | valid | clear | none | none | attempt | selected | none | available | sweep_unverifiable | Halt `sweep_unverifiable` |
| `RA-056` | 75 | `RC-RESUME-014` | resume | active | valid | clear | none | none | attempt | started | none | available | sweep_unverifiable | Halt `sweep_unverifiable` |
| `RA-057` | 75 | `RC-RESUME-015` | resume | active | valid | clear | none | none | attempt | probed | none | available | sweep_unverifiable | Halt `sweep_unverifiable` |
| `RA-058` | 75 | `RC-RESUME-031` | resume | active | valid | clear | none | none | acceptance | criterion_pending | none | available | sweep_unverifiable | Halt `sweep_unverifiable` |
| `RA-059` | 75 | `RC-RESUME-024` | resume | active | valid | clear | none | none | acceptance | criterion_running | none | available | sweep_unverifiable | Halt `sweep_unverifiable` |

Two Appendix C identifiers deliberately are not action rows:

| Non-action case | Reason |
|---|---|
| RC-RESUME-012 | C.1 dispatcher into the attempt, acceptance, or between-unit tables; it cannot be the relation's final selection |
| RC-RESUME-047 | proof that `movement.failed` has no contrary branch; the selectable action is `RC-RESUME-020` |

The checker expands the reachability rows, validates every value against the axis table, applies all
matching action rows, and requires one winner at the best precedence. It also rejects duplicate
reachable combinations, an axis value used by no reachable combination, an undeclared recovery
case, an Appendix C case missing from both the action and non-action tables, and an action row that
never wins. Thus the checked claim is: **for every combination
generated by the declared axes and reachable-cut table, selection yields exactly one action,
refusal, quiescent return, or named halt.**

That claim is deliberately relative to the model. The checker cannot prove that these axes and
reachable cuts capture every state the implementation can produce, nor that the prose action has
been implemented correctly. Adding an event, projection value, external observation, or precedence
condition therefore requires reviewing the axes and expanding the reachable cuts before claiming
coverage. That branch-expansion review obligation is the boundary between a mechanical totality
proof and “the table looked complete”.

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

**Core-determined `adapter_unavailable` sub-reasons:** `capability_unavailable` (§4 run probe)
and recovery-originated `probe_terminated_incomplete` (Appendix C.2). The kind remains
infrastructure-classified so a performer fallback may satisfy a capability the selected adapter
does not, and so recovery does not turn an unobserved probe into a quality judgment.

**`grant_denied` sub-reasons** (core-determined; §3.1 classes the kind immediately terminal):
`candidate_mismatch`, `protected_path_violation`, `read_only_violation`,
`path_grant_violation`, `enforcement_unavailable`.

**Failure classes** — the partition §3.1's first arm classifies over. It covers the **failure
cases**, not only the attempt-failure kinds: `acceptance.failed` carries a reason rather than a kind
and is a case in its own right. Membership is fixed here; what each class *does* is §3.1's and is not
restated:

| Class | Failure cases |
|---|---|
| Infrastructure | `adapter_unavailable`, `model_unavailable`, `provider_timeout`, `rate_limited`, `authentication` |
| Quality | `task_failed`, and every `acceptance.failed` |
| Immediately terminal | `grant_denied`, `protocol_error`, `budget_exhausted` |

The three classes are exhaustive over **every** attempt-failure kind this appendix declares — the
adapter kinds above and the core-determined kinds below — plus `acceptance.failed`. A failure case in
none of them is a defect in this appendix.

**Quality-failure reasons beyond acceptance** — `task_failed` reasons the core itself determines:
`draft_no_blocking_output` (§2 draft contract), and the recovery-originated
`attempt_never_started`, `attempt_terminated_incomplete`, `worktree_lost` (Appendix C.2), and
`unsolicited_cancel` — an adapter reporting `cancelled` with nothing having asked for it (B.2).

**`acceptance.failed` reasons:** `criterion_failed`, `criterion_errored`,
`acceptance_mutated_workspace`, `artifact_missing`, `artifact_kind_mismatch`,
`artifact_hash_mismatch`, `findings_malformed`, `findings_subject_mismatch`,
`findings_rubric_incomplete`, `recovery_subject_mismatch`.

**`movement.failed` reasons:** `retries_exhausted`, `fallbacks_exhausted`,
`budget_exhausted`, `human_gate_rejected`, `grant_denied`, `protocol_error`,
`composition_unresolvable`, `composition_failed`. `protocol_error` and `grant_denied` need their own terminal path
because §3.1 classes them immediately terminal — without it the movement would have no
way to end.

**`run.failed` reasons:** `movement_failed`, `budget_exhausted`, `composition_unresolvable`,
`composition_failed`.
A recovery halt is deliberately **not** among them: halts are never journal events (B.7), so a run
whose recovery halted stays in whatever state its last durable event left it, and the halt is
reported to the operator instead.

**Core-determined attempt failure kinds** — failures the core attributes to itself rather than to
a vendor, alongside the wire kinds above: `budget_exhausted` (§6 mid-flight exhaustion).

**`execution.stopped` reasons** (§6): `normal` (an ordinary close by the living opener, not a
success verdict), `cancelled`, `superseded`, `budget_exhausted`, `recovered`.

**`amendment.rejected` reasons:** `run_terminal`, `run_cancelling`, `stale`, `patch_error`,
`invalid_score`,
`reserved_field`, `no_op`, `claim_narrower`, `executed_dependency_changed`,
`candidate_incompatible`.

**`amendment.approval_abandoned` reasons** (§6): `base_head_changed`, `plan_invalidated`, `cancelled`.

**`amendment.routed_human` reasons:** `draft_phase`, `auto_disabled`,
`unclassified_change`, `recognized_non_monotone`, `runtime_scope_started`.

**`candidate_incompatible` conditions** (§9): `succeeded_dependency_changed`,
`composition_changed`, `verification_episode_finished`, `verification_mode_changed`.

**Envelope classes** (§9): `NARROW_PATHS`, `NARROW_GRANTS`, `BUDGET_DECREASE`.

**Criterion outcomes** (§7): `PASS`, `FAIL`, `ERROR`.

**Synthesized-completion detail** (Appendix C.3): `recovered_without_observed_completion` — the only
`error_detail` recovery originates, so a synthesized `ERROR` is always distinguishable from an observed
runner failure.

**Review outcomes** (§8): `CLEAN`, `CONTESTED`, `OVERRIDDEN`.

**Apply-gate grades** (§8): `verified`, `approved`, `reviewed`.

**Apply-gate predicates** (§8): `no_unresolved_blocking_findings`, `no_blocking_findings`.

**Verification intents** (§2): `write-basic-tests`, `pass-existing-tests`, `none`.

**Human gate modes** (§2): `always`, `on_contested`, `never`.

**Decision types** (Appendix B): `question`, `human_gate`, `amendment`, `finalization`.

**Adapter features** (§4 `probe`) — no tokens are defined in v0.2. The list remains an open
extension point; absent or empty means none, and the core ignores unknown tokens.

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

**`protocol_error` sub-reasons** — carried as `attempt.failed.reason` when
`kind: protocol_error` (B.2): `duplicate_artifact_instance`, `undeclared_artifact`,
`artifact_path_escape`, `change_set_emitted_as_artifact`, `proposal_without_authority`,
`partial_frame_eof`, `strict_decode_failed`, `frame_too_large`, `event_limit_exceeded`,
`blocking_set_mismatch`, `duplicate_emitted_id`, `draft_non_blocking_proposal` (§2 draft
contract).

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

**Composition scopes** (`composition.conflicted`, `composition.failed`): `movement`, `candidate`.

**`composition.failed` causes** (§5), closed and **partitioned by phase**. Composition runs three
ordered phases, and **a phase completes only when its subprocess status *and* its fully decoded,
validated output have both been accepted**. Attribution follows the **first phase operation that
fails to produce its required accepted output** — never the latent origin of the defect. So an
incomplete phase-1 population that only surfaces as a merge exit `128` is `git_exit`, because phase 1
had produced its accepted output and phase 3 is where an operation first failed; a malformed
inspection result caught before the merge stays `inspection_failed`. Causal attribution would make
the same fault classifiable two ways depending on when it was noticed:

| Phase | Cause | Condition |
|---|---|---|
| 1 — prepare | `repository_unusable` | the core-created temporary repository could not be created or populated |
| 2 — inspect | `inspection_failed` | the pre-merge inspection could not be completed, including a failure to start it |
| 2 — inspect | `driver_rejected` | the inspection completed and found an external custom merge driver |
| 3 — merge | `spawn_failed` | the merge subprocess could not be started |
| 3 — merge | `git_exit` | the merge exited outside `0` and `1` |
| 3 — merge | `git_signalled` | the merge was terminated by a signal |
| 3 — merge | `output_unusable` | the merge exited `0` or `1` but its output could not be read as §5 specifies |
| 3 — merge | `status_unobtainable` | the merge started but the core could not obtain or interpret its termination status at all |

Within a phase the causes are disjoint by construction: phase 2 either completes or does not, and
phase 3's five are exhaustive over the **no-verdict** outcomes of a child — not over all of them, since
an exit `0` or `1` whose output decodes is a verdict and leaves this table entirely. Inside that
boundary the child either fails to start, or starts without a usable terminal status, or is signalled,
or exits outside `0`/`1`, or exits inside it with undecodable output. `status_unobtainable` covers
every case with no usable status — a failed wait, or a status interpretable as neither exited nor
signalled — and is what keeps the other four from silently assuming a status was obtained. The set is closed over the outcome
§5 defines — **an observed no-verdict outcome that no control request and no recovery rule owns** —
and a condition inside that boundary fitting no row is a defect in this appendix, not a case for an
implementation to classify.

**Score status** (§2): `draft`, `finalized`.

**Amendment auto modes** (§2): `off`, `envelope`.

**Recovery halts** — conditions that stop a run rather than repairing it:
`journal_idempotency_conflict`, `unsupported_run_format`, `missing_artifact_file`,
`missing_snapshot_file`, `missing_changeset_ref`, `missing_proposal_record`,
`missing_resolved_cast`, `missing_prepare_plan`, `owner_unverifiable`, `sweep_unverifiable`,
`spawn_handoff_unverifiable`, `root_snapshot_divergence`, `journal_corrupt`. Each `missing_*` reason covers **both** absence and hash mismatch: a file whose
bytes do not match the recorded hash is no more usable than one that is gone, and splitting them
would double the enum without changing any action.

# Appendix E — Ordered-step boundaries

Normative. §6's quiesce and cancellation protocols are **ordered steps**: each depends on a preceding
write having become durable. The readiness table marks them **not proved** — designed against
SPIKE-4's measurements, not measured themselves — and names fault injection as the forcing function.
This appendix freezes what injection is performed against, so the implementation exposes those points
by construction rather than being retrofitted with them.

It defines *where* a crash may be injected and *what must hold across it*. It does not restate any
protocol. §6's cancellation oracle is referenced by its own `(a)`–`(f)` labels, which §6 establishes
as the single normative sequence.

**Scope, stated as a selection rule rather than a list.** This appendix covers the `resume` families
whose ordered crash cuts have a selected recovery action in Appendix C: the **control channel**
(prepare, quiesce, cancellation, supersession fencing), **authority acquisition**, **launch
identity handoff**, **adapter execute completion**, and the **evidence and lifecycle consequences**
that terminalize an attempt, movement, run, or acceptance or open a required gate. An ordered pair
belongs here when a crash between its steps can strand durable or process state such that recovery
must reason about it from the last durable endpoint rather than infer that the right endpoint ran.

## E.1 The two signals

An edge has two endpoints, and they do not attest the same kind of thing.

**`DurabilityReceipt`** — proof that a named persistent mutation has crossed its required fsync or
directory-fsync boundary (§1). It is a typed return value of the durable operation, produced **after**
that boundary and **before** the next protocol action. Five operation kinds produce one: a journal
append whose class declares `sync` (Appendix B); a file publication (temp → fsync → rename →
directory-fsync); a durable quarantine or removal, which also directory-fsyncs before reporting; a
Git ref creation, which §1 requires to be durable before the journal event naming it; and a lease
create, compare-move, or compare-remove.

**One receipt attests one mutation.** Where a protocol step performs several — `(b)` quarantines the
snapshot and removes both the plan and the sidecar — each is its own receipt and its own injectable
point. Bundling them would hide the orderings inside a step from the harness, which is where the
window this appendix exists for actually lives.

**Every receipt is addressable; not every receipt owns an E.2 edge.** E.2 lists the windows across
which something must hold, and a receipt owning no edge is still emitted and still injectable. But
"no edge" means two different things, and conflating them would read as a safety claim this appendix
has not made:

- **In-scope and recovery-neutral.** The mutation's loss is genuinely harmless, so no assertion is
  owed. `(b)`'s plan and sidecar removals are the worked example: an orphan plan is removed on sight
  and a leftover sidecar is inert.
- **Outside this appendix's selection rule.** The receipt is real and its loss may be non-neutral,
  but the ordering belongs to a recovery surface or protocol family E.2 does not select. Silence is
  scope, not an assertion that the mutation is harmless.

E.1 and E.2 therefore describe one mechanism at two granularities, not two catalogs.

Unowned receipts need addresses too, and enumerating them here would be a second catalog to keep in
step with the first. They are named by **derivation** instead: `<owning edge id>/<operation>`, where
the operation is the mutation the receipt attests — `prepare.quarantined_to_abandoned/plan_removed`.
A receipt inside no edge at all takes the protocol step as its prefix. Only E.2's edge IDs are
frozen; derived names follow whatever the step is called.

**`BoundaryReached`** — proof only that an actor arrived at a named execution point. It is
**ephemeral**: it may block under the harness, and **no recovery rule and no correctness argument may
depend on it**. A `BoundaryReached` that a crash erases must leave the system in a state the durable
record already accounts for; if it does not, the protocol is wrong and no probe placement can repair
it.

Endpoints are classified by **what they attest, not by which component owns them**. Acquiring the
state lock is a `runstore` operation and still emits `BoundaryReached`, because holding a lock is not
a durable fact.

**One probe mechanism, four surfaces.** `runstore`, the driver and cancellation executor, process
supervision, and the launch path all emit their point IDs through the same neutral probe. Production
installs a no-op; the harness installs a blocking one. Per-surface probes would make cross-surface
interleaving harder to schedule and would let the same word mean different things in two places.
Receipts are **not** routed through that probe — they are return values, and collapsing them into a
notification would erase the distinction E.1 exists to draw.

**The edges below are v0.2's required fault-injection catalog, not a claim that they are all the
crash windows Partitur has.** Point IDs are semantic so the catalog can grow: a newly discovered
window is added without renumbering, and no count is part of any interface. Changing an existing
point's meaning is a design change, not an implementation detail. The catalog grew from eleven to
twenty-two across three review rounds, and to twenty-four when freezing the execute-completion
order exposed two more — supersession fencing, the interval-close ordering, authority
acquisition, two thirds of the launch sequence, and both unconditional sweep edges were all missing
from the first draft. It grew to twenty-eight when Appendix C's mechanically total recovery
expansion removed the deferral of four evidence and lifecycle consequence chains. A numbered scheme
would have made each of those admissions expensive, which is the argument for semantic IDs stated as
something that already happened rather than something that might. **This is history, not evidence
of convergence.** What bounds the catalog is E's scope rule above and the branch expansion E.4
requires, not the number of review rounds that have run.

## E.2 The catalog

`R` marks a `DurabilityReceipt` endpoint, `B` a `BoundaryReached` one. **Thirteen of the twenty-eight
have a `B` endpoint** — the harness cannot hang those on an fsync and must block on the probe.

**Prepare and quiesce**

| Edge | Left | Right | Owning clause | Assertion across a crash |
|---|---|---|---|---|
| `prepare.snapshot_to_plan` | snapshot published `R` | plan record published `R` | §6 step 1; §9 snapshot lifecycle | A snapshot no `approval_prepared` names is quarantined and never becomes head |
| `prepare.plan_to_prepared` | plan record published `R` | `amendment.approval_prepared` appended `R` | §6 step 1; B.5 | An orphan plan with no `approval_prepared` is removed on sight — it authorizes nothing, so it needs no quarantine |
| `prepare.prepared_to_observed` | `approval_prepared` durable `R` | driver observes the prepare `B` | §6 mutation barrier | The barrier is in force from the moment the event is durable, **not** from when the driver notices, so everything but the drain is refused `prepare_pending` regardless of driver progress. **This edge adds no invariant the left endpoint does not already carry**; it is in the catalog as a deterministic delayed-observation point, so the harness can schedule a driver that has not yet noticed against one that has |
| `quiesce.swept_to_lease_moved` | adapter and criterion sessions verified empty `B` | `driver.lease` compare-moved to the prepare-bound path `R` | §6 step 2 | A crash here leaves **no** sidecar, and the sidecar is the only durable evidence that the whole ACK sequence ran — sweep, interval close, writer stop, revalidation, compare-move. Absence forces another sweep in the reachable *matching lease, no sidecar* state; the *no lease, no sidecar* branch legitimately commits without one |
| `quiesce.lease_moved_to_commit_lock` | lease move durable `R` | approver holds the state lock `B` | §6 step 3 | A matching sidecar does **not** mean the approval must commit — it means commit must be **re-entered**, approving only if every guard still passes. Cancellation may win, a hash mismatch may halt, and a changed base head or invalidated plan abandons. What is forbidden is stepping past a pending prepare |
| `prepare.quarantined_to_abandoned` | snapshot quarantine durable `R` | `amendment.approval_abandoned` appended `R` | §6 `(b)` and step 3's abandon row; §9 snapshot lifecycle | The barrier must not lift with the immutable revision path still occupied, so the quarantine precedes the append that lifts it. This holds for **every** abandonment reason — `cancelled`, `base_head_changed`, `plan_invalidated` — not only the cancellation one. The accompanying plan and sidecar removals carry no separate assertion: an orphan plan is removed on sight and a leftover sidecar is inert, so losing either is recoverable |

**Cancellation** — `(a)`–`(f)` are §6's labels and are not restated here.

| Edge | Left | Right | Owning clause | Assertion across a crash |
|---|---|---|---|---|
| `cancel.swept_to_terminal` | `(a)` sessions verified empty `B` | `(e)` `run.cancelled` appended `R` | §6 `(a)`, `(e)` | **Terminalization always follows a verified sweep**, whatever `(b)`, `(c)` and `(d)` do. The edges below cover those conditionals; this one covers the case where all three skip, which is the ordinary shape when there is no pending prepare, no open interval, and no matching lease — and which would otherwise be the one cancellation path with no edge asserting the invariant the whole oracle exists for. `sweep_unverifiable` halts, and C.1's cancellation row re-enters at `(a)` rather than resuming mid-sequence |
| `cancel.swept_to_quarantined` | `(a)` sessions verified empty `B` | `(b)` snapshot quarantined `R` | §6 `(a)`–`(b)` | The `(b)`-true refinement of the row above: a pending prepare is quarantined before anything else proceeds |
| `cancel.interval_stopped_to_terminal` | `(c)` `execution.stopped {reason: cancelled, charging: clamped}` appended `R` | `(e)` `run.cancelled` appended `R` | §6 `(c)`–`(e)` | `run.cancelled` never lands with an execution interval still open, **independently of whether `(d)` fences** — the two predicates are separate, and gating the close on fencing would let the budget projection read a run that never stopped consuming. The close carries `charging: clamped` **whichever canceller appended it**, including a responsive driver closing the interval it opened itself; one oracle step has one event shape, so the assertion does not branch on the actor. This is the only edge covering the `(d)`-false path, which E.3 excludes |
| `cancel.fence_decided_to_terminal` | `(d)` taken with its lease predicate **true** — no durable output `B` | `(e)` `run.cancelled` appended `R` | §6 `(d)`–`(e)` | See E.3 |
| `cancel.terminal_to_lease_removed` | `(e)` durable `R` | `(f)` lease removed `R` | §6 `(f)`; C.1 terminal row | `(f)` must still run. The cancellation row can no longer match once the run is terminal, so C.1's terminal row is what retries the cleanup |

**Supersession fencing** — the commit table's deadline and dead-owner branches.

| Edge | Left | Right | Owning clause | Assertion across a crash |
|---|---|---|---|---|
| `supersede.swept_to_approved` | survivor sweep verified empty `B` | `amendment.approved` appended `R` | §6 step 3 commit table | The deadline and dead-owner branches sweep before fencing and approving, and **nothing else in this group attests that**: the edge below attests an interval close and the one after it an epoch advance, neither of which is the sweep. Without this edge an approval could follow an unswept survivor on either branch. Note it is the sweep that is unattested elsewhere, not the branch that is rare — both lower edges are absent only when the driver quiesced normally, which leaves the matching-sidecar branch and not this group at all |
| `supersede.interval_stopped_to_approved` | `execution.stopped {reason: superseded}` appended `R` | `amendment.approved` appended `R` | §6 step 3 commit table | Same obligation as `cancel.interval_stopped_to_terminal`. It arises only on the branches where the approver closes the interval; a driver that quiesced normally closed its own in step 2 |
| `supersede.fence_decided_to_approved` | fence branch taken with the lease still matching — no durable output `B` | `amendment.approved` carrying `fenced_epoch` appended `R` | §6 step 3 commit table | E.3's shape on the supersession path. Nothing durable records the advance until the approval carries it, so recovery must re-derive the same branch from the retained lease. The commit table gives this branch to a verifiably dead owner as well as a wedged one, which is why the field is keyed on *advancing the epoch* rather than on wedging |
| `supersede.approved_to_lease_removed` | `amendment.approved` durable `R` | stale lease removed `R` | §6 step 3 commit table; C.1 stale-lease row | The journaled advance is what makes the lease stale, so removal follows the append and never precedes it. A lease stranded here is at a superseded epoch, so C.1's stale-lease row removes it and re-evaluates. Freezing this edge is what showed that row had to exist: the unscoped `owner_unverifiable` check halted on this state, which is provably safe |

**Authority acquisition**

| Edge | Left | Right | Owning clause | Assertion across a crash |
|---|---|---|---|---|
| `authority.granted_to_lease_created` | `authority.granted` appended `R` | `driver.lease` created `R` | §6 lease/authority ordering; C.1 | The epoch stands with no authorized driver, and the next acquisition must use a **newer** epoch rather than reusing the granted one. The inverse state — a lease with no `authority.granted` at its epoch — is an orphan from a crashed acquisition, quarantined and then re-evaluated rather than reclaimed on the spot (C.1) |

**Launch** — each row applies per launch, to the adapter and to every external criterion.

| Edge | Left | Right | Owning clause | Assertion across a crash |
|---|---|---|---|---|
| `launch.adapter.marker_held_to_identity_published` | trampoline holds the marker `B` | `identity.json` published `R` | §4; C.2 first row | A first held/no-identity sample starts §4's bounded stabilization; it does not halt. §4 owns the four outcomes and their priority. The harness MUST be able to block at this existing left endpoint, terminate the trampoline before the right receipt, and start recovery immediately, so the exact pre-publication window is injectable rather than inferred from the spike's published-handoff test |
| `launch.adapter.identity_published_to_recorded` | `identity.json` published `R` | `attempt.started` appended `R` | §4; C.2 | An unjournaled `launch_id` directory is found by listing the staging root and its session swept before recovery proceeds. Both files carry the launch nonce and a mismatch means one is from an earlier launch, so both are ignored |
| `launch.adapter.recorded_to_gate` | `attempt.started` appended `R` | trampoline gate released `B` | §4 | No released mutator exists without a journaled identity. Marker free ⇒ no *released mutator* survives — not "no launch process survives", which the pre-marker window makes false |
| `launch.criterion.marker_held_to_identity_published` | trampoline holds the marker `B` | `identity.json` published `R` | §7; §4; C.3 | The same §4 stabilization and exact pre-publication injection obligation as the adapter row. Criterion and adapter trampolines share the inherited marker and publication order, so the thread-group file-table window cannot be scoped to one launch type |
| `launch.criterion.identity_published_to_recorded` | `identity.json` published `R` | `criterion.started` appended `R` | §7; C.3 | As the adapter row. C.3 handles the unjournaled directory explicitly, ahead of the rows that synthesize a completion |
| `launch.criterion.recorded_to_gate` | `criterion.started` appended `R` | criterion gate released `B` | §7; §4 | As the adapter row |

**Adapter execute completion** — the response-derived event names vary, but the cleanup boundary is
one sequence (§4).

| Edge | Left | Right | Owning clause | Assertion across a crash |
|---|---|---|---|---|
| `execute.adapter_swept_to_interval_stopped` | complete response validated, adapter exited zero, and recorded adapter session verified empty `B` | `execution.stopped {reason: normal, charging: measured}` appended `R` | §4; §6; C.2 | A crash leaves the interval open. Recovery does not trust the volatile response: it sweeps again, closes the interval `recovered`/`clamped`, and records an incomplete-attempt failure. Closing before the left endpoint would stop charging a survivor |
| `execute.interval_stopped_to_outcome` | ordinary adapter `execution.stopped` durable `R` | the response-derived B.2 event appended `R` | §4; B.2; C.2 | A crash records no outcome from the lost response and never enters acceptance. C.2 re-sweeps, observes the interval already closed, and records the incomplete-attempt failure without a second charge. In particular, `performer.completed` can never coexist with an open adapter interval or an unswept session |

**Evidence and lifecycle consequences**

| Edge | Left | Right | Owning clause | Assertion across a crash |
|---|---|---|---|---|
| `lifecycle.attempt_completed_to_movement_succeeded` | `attempt.completed` appended `R` | `movement.succeeded` appended `R` | B.1–B.2; C.2 `RC-RESUME-019` | The completed attempt is the movement's durable success authority. Recovery appends the missing movement success exactly once and never starts another attempt beside it |
| `lifecycle.movement_failed_to_run_failed` | `movement.failed` appended `R` | `run.failed` appended `R` | B.1; C.2 `RC-RESUME-020` | A failed movement cannot leave the run schedulable. Recovery terminalizes the run with the run-level `movement_failed` reason and neither propagates an invalid movement reason nor schedules more work |
| `acceptance.criterion_error_to_failed` | `criterion.completed {outcome: ERROR}` appended `R` | `acceptance.failed` appended `R` | B.3; C.3 `RC-RESUME-023` | The durable criterion error forbids another criterion or a success claim. Recovery appends the acceptance failure that terminalizes the attempt and leaves successor selection to its recorded disposition |
| `acceptance.evaluation_completed_to_decision_requested` | `acceptance.evaluation_completed` appended for an attempt requiring a human gate `R` | required human-gate `decision.requested` appended `R` | B.3–B.4; C.3 `RC-RESUME-026` | Evaluation completion does not imply gate approval. Recovery appends the same required request idempotently and cannot complete the attempt or bypass the unresolved gate |

## E.3 `cancel.fence_decided_to_terminal` is not a lost-write boundary

It is the window the mirrored fence bug lived in, and its left endpoint writes nothing. `(d)` produces
**no new durable state**: the authority epoch is a journal projection, and the advance becomes
authoritative only when `(e)` journals it. What `(d)` does is *retain* a durable input — the
still-matching old lease — which is what makes its predicate replayable, and which is why `(f)` and
not `(d)` removes it: removing the retained input first would destroy the state the predicate reads.

So the requirement is not "do not lose the fence". It is **an implementation must not make `(d)`
independently durable.**

**Scope.** The assertions below govern the branch where `(d)`'s predicate is *true* — a lease still
matching `observed_authority_epoch`. `(d)` evaluating false is the ordinary path in which no epoch
advances and `(e)` carries no `fenced_epoch`; nothing here applies to it.

Within that branch, four assertions evaluable from durable state alone, and one obligation on
recovery:

1. Between `(d)` and `(e)`, the journaled epoch is unchanged.
2. The old matching lease is still present.
3. `authority.json` never publishes an epoch ahead of the journal.
4. **No persistent state claims the advanced epoch unless the fencing terminal event carries it.**
5. Recovery re-evaluates the retained lease predicate and appends `run.cancelled {fenced_epoch:
   observed + 1}` — **subject to C.1's precedence, which this list does not restate**. Rows above the
   cancellation one can match first and halt or clean up instead. The append is therefore not
   unconditional and must not be asserted as such; what governs is C.1, read top-down.

Re-evaluating that predicate is not the recomputation Appendix C forbids. C forbids recomputing a
*recorded decision*; `(d)` records none, and recovery evaluates the same closed predicate from
retained durable inputs. The distinction is the whole reason `(d)` is keyed on the surviving lease
rather than on owner liveness.

`supersede.fence_decided_to_approved` is the same shape with a different terminal: the advance is
carried by `amendment.approved` rather than `run.cancelled`, and the run stays nonterminal. Every
assertion above applies with that substitution.

The lease spike in `spikes/` persists the incarnation token in `authority.json` and treats that file
as authority. Appendix A already flags it as a model this document discarded; against this appendix
it is **forbidden evidence, not a skeleton to copy**.

## E.4 Obligations on what implements this

**Implementation status.** Appendix E is partially implemented. `DurabilityReceipt` is threaded
through production journal appends and carries a `ReceiptAddress`. In production, `PointID` boundary
points are emitted by the launch trampoline, at the marker-held and gate-released seams for adapter
and criterion launches, and by the cancellation oracle at the sessions-swept and fence-decided seams;
no prepare, quiesce, or supersession boundary point is emitted. Production installs only the no-op probe. The E.2 `EdgeID` values are declared in Go and
mechanically cross-checked against the catalog, but no production path carries one yet.

The paragraph above records current implementation status. The obligations below state what must be
true of the implementation — the whole point of freezing the contract first is that `runstore` is
written against it rather than retrofitted. [`HARNESS.md`](HARNESS.md) selects from E.2 rather than
describing boundaries of its own.

**Unit 2.1 allocation.** The first cancellation slice implements the shared §6 cancellation
oracle, `RC-RESUME-006`, and §7's `cancel` command where no valid lease owner remains. Unit 2.1b
adds steps 3–4's watcher and responsive-driver terminalization: the driver observes the durable
request, becomes the canceller role, and completes the same oracle. It also adds step 6's bounded
acknowledgement and wedged-owner escalation. Without that acknowledgement path, a healthy driver
is indistinguishable from a wedged owner and the deadline would terminate it; that hazard is created
by implementation order, not by §6.

The five `cancel.*` E.2 edges remain outside 2.1a; 2.1b owns their subprocess fixture and the
required `(b, c, d)` matrix.

- The Go types implement E.1's semantics and carry E.2's edge IDs verbatim.
- They **do not restate the assertions.** An invariant in a doc comment is a second normative text,
  and this document has paid for that mistake more than once. A comment may name the edge and point
  here; it may not paraphrase what must hold.
- The edge IDs are not a numbered enum. Adding a newly discovered window must not renumber anything.
- [`HARNESS.md`](HARNESS.md) selects edges by ID and cites the owning clause instead of paraphrasing
  it. It gives every edge an explicit disposition, so a new one is a visible omission there rather
  than a silent gap. If it and this appendix ever disagree, this appendix governs and that file is
  the defect.
- **An edge that proves impossible to inject at is a defect in this appendix**, to be revised here
  explicitly. It is not licence for `HARNESS.md` to reword the seam or drop it.

**Completeness is checked by branch expansion, not by inspection.** Reading for missing pairs is what
produced three rounds of additions. The check that terminates is:

1. Expand every in-scope branch — prepare and quiesce, all eight `(b, c, d)` combinations of the
   cancellation oracle, every supersession commit-table branch, authority acquisition, and both
   launch types, adapter execute completion, and each evidence and lifecycle consequence chain.
2. Treat every receipt and every correctness-critical ephemeral point as an `R` or `B` node.
3. At each reachable crash cut, require **at least one** classification: an E.2 assertion with a named
   recovery owner, an explicit recovery-neutral exemption, a recorded deferral, or a named halt. More
   than one *invariant* may apply — `cancel.swept_to_terminal` and its `(b)`-true refinement both
   cover the same cut by design, and a cut may owe several assertions at once.
4. Require **exactly one** *recovery action* to be selected after precedence is applied. C.1's rows
   overlap deliberately and first-match resolves them; what is forbidden is a reachable state that
   selects none, or one where two rows would each be correct to run.
5. Reject mechanically — an uncovered cut, or a recovery state selecting zero actions, is a failure
   of this contract and not of the harness.

The distinction between 3 and 4 is the load-bearing part, and collapsing it was a real error in an
earlier draft: overlapping *assertions* are how refinement works, while overlapping *actions* are how
recovery becomes ambiguous. Step 4 is what catches the class of defect this freeze found in C.1,
where a provably safe state selected a halt it should never have reached.
