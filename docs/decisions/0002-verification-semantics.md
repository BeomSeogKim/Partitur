# Decision 0002 — Verification semantics

- Status: accepted (dual-model consensus, 2026-07-25 — 9 review rounds)
- Refines: DESIGN §1–§2, §5, §7, consensus V-001
- Where this decision adds fields, events, or identities beyond DESIGN v0.1, the delta
  is listed in §10 and this document governs.

## Context

V-001 fixed three verification grades — VERIFIED (machine), APPROVED (human),
REVIEWED (LLM) — and forbade collapsing them into one scalar. This document defines how
grades are derived from journal evidence, what they are bound to, how the run's results
compose into one application candidate, and how the ship gate (`apply`) and
`promote-score` behave.

## Decision

### 1. Expectation = intent + apply gate (two concepts, not one)

DESIGN §2's `expectation: write-basic-tests | pass-existing-tests | none` expresses the
user's *work intent* — what the performers should do about verification. The ship gate
expresses *which evidence `apply` demands*. They are declared separately:

```yaml
verification:
  expectation:
    intent: write-basic-tests    # write-basic-tests | pass-existing-tests | none
                                 #   (existing enum, unchanged; forwarded to briefs as
                                 #    brief.verification_expectation)
    apply_gate:
      require: [verified, reviewed]   # non-empty, duplicate-free subset of
                                      # {verified, approved, reviewed}
      predicates: [no_unresolved_blocking_findings]   # optional; closed enum, §6
  final_movement: check          # required iff apply_gate is not waived — §6
  # — or —
  expectation:
    intent: none
    apply_gate:
      waived: true
      reason: "spike; will be rewritten"   # non-empty reason mandatory
```

- `require` and `waived` are mutually exclusive (XOR); `require: []` is invalid.
- `intent: none` is an explicit user choice and does not by itself require a waiver;
  `waived` concerns only the apply gate.
- `intent` is **not** ship policy: because it is forwarded into performer briefs, it is
  part of every movement's execution dependencies (Decision 0001 §6) and cannot be
  changed in-place once movements have succeeded.
- Waiver flow follows DESIGN §2's draft contract: the interview *proposes* the waiver
  as an amendment, the human approves that amendment into a snapshot, and the
  `draft → finalized` transition is a **separate** human decision. After finalization,
  any change to the `verification` block is a score amendment that is never
  envelope-eligible (Decision 0001), i.e. always human, and is further constrained by
  §7 below.

### 2. Evidence: positive events with explicit identity and binding

Marks are derived from **positive journal evidence**, never from the absence of failure
events, and every piece of evidence is bound to what it actually proved.

**Criterion identity.** Every acceptance criterion (hard / review / artifact) carries
an explicit `id`, unique within its movement (schema delta, §10). Its
`criterion_spec_hash` is the canonical hash of the criterion's semantic fields (id,
kind, argv, timeout, artifact reference/expected hash, rubric — as applicable). The
movement's `acceptance_spec_hash` is the canonical hash of the entire acceptance block,
ordered criteria plus `human_gate`.

**Event protocol** (per attempt):

- `acceptance.started` carries `attempt_id`, `score_revision`, `subject_tree` (the
  tree hash the acceptance runs against), and `acceptance_spec_hash` — so the subject
  binding is durable before any criterion runs. Then per criterion
  `criterion.started` (criterion id, `criterion_spec_hash`, and the same subject
  binding) and `criterion.completed` with `outcome: PASS | FAIL | ERROR`,
  `attempt_id`, `score_revision`, `subject_tree`, and `criterion_spec_hash`.
- If the runner short-circuits on the first FAIL/ERROR, it records the terminal
  `acceptance.failed`; when every criterion ran, it records
  `acceptance.evaluation_completed` ("evaluation", not a Git commit). Grades are
  derived **only** from attempts with `acceptance.evaluation_completed`.
- **Recovery table** (fail-closed; rows are evaluated top-down — the first matching
  row wins; before resuming any criterion, the core verifies the worktree's current
  tree still equals the `subject_tree` recorded in `acceptance.started`; on mismatch
  it records `acceptance.failed {reason: recovery_subject_mismatch}`, consuming
  exactly one quality retry keyed on that event's causation id):

  | Last durable state | Recovery action |
  |---|---|
  | `acceptance.failed` present | Terminal — synthesize no further criterion results; project retry consumption/scheduling exactly once, keyed on that event's causation id |
  | Any `criterion.completed` is `FAIL` or `ERROR`, no `acceptance.failed` | Append `acceptance.failed` idempotently; start no further criterion |
  | `criterion.started` without `completed` | Close it as `ERROR` — including when the command in fact passed but crashed before the event was written — then append `acceptance.failed` |
  | All criteria completed, all `PASS`, no `evaluation_completed` | Append `acceptance.evaluation_completed` idempotently |
  | `acceptance.evaluation_completed`, required human gate not yet requested | Resume at the gate step; append one `decision.requested` idempotently |
  | `decision.requested` (gate) unresolved | Restore the `WAITING_HUMAN` projection; append nothing |
  | Gate resolved approve, movement success event missing | Complete the movement success idempotently — including, for the final movement, the run's `SUCCEEDED` transition |
  | Gate resolved reject, terminal failure event missing | Record `movement.failed {reason: human_gate_rejected, decision_id, subject_tree}` idempotently, keyed on the gate decision id |
  | `acceptance.evaluation_completed`, no gate required, movement success event missing | Complete the movement idempotently |
  | `acceptance.started`, no criterion events | Resume with the first criterion |
  | Some `criterion.completed` (all `PASS`), none in flight, criteria remaining | Resume with the next unstarted criterion |

- Every mark is bound to at least
  `(run, movement, attempt, score_revision, subject_tree, acceptance_spec_hash)`.
- Evidence from `FAILED`, `CANCELLED`, or `SUPERSEDED` attempts contributes nothing to
  current marks (it remains in the journal as history).

**Artifact instances.** Score-declared output ids are *logical* ids. Each emission is a
distinct immutable instance identified by `(logical_output_id, attempt_id)` — the
`artifact_instance_id`. A logical output may be emitted **at most once per attempt**; a
second emission is the deterministic protocol error `duplicate_artifact_instance`. A
declared output that was never emitted is caught by the existing artifact-integrity
criterion (DESIGN §7). Journal entries, hashes, marks, and finding overrides reference
instances; a downstream movement's logical input resolves to the instance from the
completing successful, non-superseded attempt. Retries and revision re-runs therefore
never collide with earlier immutable artifacts.

### 3. Grade derivation

Marks are a **projection** computed on demand (`status`, `apply`); nothing mutable is
stored, so marks can never drift from the evidence.

- **VERIFIED** — the attempt's `acceptance.evaluation_completed` exists, the movement
  declares ≥ 1 `hard` criterion, and every declared hard criterion has
  `outcome: PASS`. (Zero hard criteria ⇒ never VERIFIED.)
- **APPROVED** — a `decision.resolved` with `decision_type: human_gate` approves this
  movement. The event carries `gate_id`, movement/attempt/`score_revision`, the exact
  `subject_tree`, the approval scope, and related finding instance ids. An approval
  does not carry over to a new attempt, revision, or subject tree. Overriding a
  blocking finding requires a recorded reason.
- **REVIEWED** — the movement declares ≥ 1 `review` criterion (compiler-enforced — zero
  review criteria can never yield REVIEWED by vacuous truth), and every declared review
  criterion is satisfied by a well-formed, subject-bound findings artifact (§5).
  REVIEWED means "typed model review completed" — a *kind* marker, deliberately silent
  about the outcome. The outcome lives in a separate projection, `review_outcome`:
  - `CLEAN` — zero blocking findings were raised;
  - `CONTESTED` — at least one blocking finding is unresolved (including partial
    overrides);
  - `OVERRIDDEN` — blocking findings were raised and **all** were overridden by
    exact-subject human decisions.
  Findings are immutable; an override links to the finding instance via events and
  yields `REVIEWED + APPROVED` with `review_outcome: OVERRIDDEN` — it never makes the
  blocker look like it was not raised.

Marks form a **set**, never a ladder: no ordering, no substitution. REVIEWED never
counts toward anything that asks for VERIFIED or APPROVED.

### 4. Criterion outcomes and the no-override rule

`ERROR` = spawn failure, timeout, runner crash — the criterion produced no verdict.

- Both `FAIL` and `ERROR` block movement completion and VERIFIED, and **neither is
  human-overridable**. A human cannot complete a movement over a red or errored hard
  criterion. The paths out are: retry (both consume the movement's quality-retry
  budget per DESIGN §3 — no new attempt once budget is exhausted), or an audited score
  amendment changing the criterion — never envelope-eligible, applying only to a new
  revision and new attempt, and subject to Decision 0001 §6 (a succeeded movement's
  acceptance cannot be rewritten in place).
- A later clean-base attempt whose criteria pass may become VERIFIED; earlier failed
  attempts stay visible in `status` (§9) — flakiness is surfaced, not laundered.
- Blocking findings are different in kind: they are reviewer *judgment*, so
  `CONTESTED → WAITING_HUMAN → approve` is a legitimate human override (recorded as
  such, §3).
- A human gate resolved as **reject** terminates the movement:
  `movement.failed {reason: human_gate_rejected, decision_id, subject_tree}` — it
  consumes no quality retry and no fallback, the movement is terminal `FAILED`, and
  for the final movement the same authoritative transition takes the run to `FAILED`.
  Recovery records it exactly once, keyed on the gate decision id (§2 table).

### 5. Findings artifacts: structurally accountable

The core cannot verify model judgment, but it verifies what the reviewer *claims to
have examined*:

- The findings artifact **must** carry the exact `subject_tree` it reviewed; input
  change-set hashes are auxiliary provenance only.
- The core compares the declared `subject_tree` against the tree it observed in the
  reviewing movement's worktree; a mark's subject binding is generated from the
  **core-observed** tree, never trusted from the artifact.
- It must contain a coverage entry for **every** declared rubric; zero findings still
  requires a typed per-rubric conclusion ("examined, none found").
- The core validates subject binding, rubric completeness, and schema — never truth.
- A findings artifact missing any of these is malformed → acceptance execution failure
  (DESIGN §7).

### 6. One application candidate per run; the gate binds to it

Two write movements, each VERIFIED on its own tree, prove nothing about their combined
result. The subject of shipping is the **application candidate**, and every run that
reaches `SUCCEEDED` has exactly one — waived or not.

**Materialization and identity.** With a final verification movement, the core
composes the candidate after every `repo_write` movement has succeeded and before the
final movement becomes READY. For **waived** scores (no final movement), the
materialization is deferred until every non-draft movement has succeeded and is folded
into run completion. "Atomic" always means **one authoritative journal event**, never
a multi-event batch that could tear on crash:

- Non-waived initial materialization: `application_candidate.recorded` itself
  constitutes the initial binding to the revision in its payload — no separate
  `application_candidate.bound` event is appended for it.
- Subsequent bindings: projected from `amendment.approved` (§7), as before.
- Waived completion: a single `run.succeeded` event carries the full candidate
  payload and binding; the candidate's recorded/bound facts are projections of it.
  An active waived run therefore never holds a recorded candidate, so the
  candidate-compatibility judgment (§7) is never applicable to it; ordinary
  amendment rules govern until the run is terminal.
- Final-movement completion: one terminal event projects both the final movement's
  success and the run's `SUCCEEDED` transition.

In both paths the composition takes all contributing change sets — the approved
deltas of every successful, non-superseded `repo_write` movement, deduplicated, in
the deterministic fan-in order (DESIGN §5) — into `(base_tree, result_tree)`:

- `candidate_id = sha256(canonical_json({format_version, base_tree, result_tree,
  ordered_change_sets}))` — a domain-versioned canonical encoding, so the identity is
  unambiguous; it is a content identity, independent of score revision.
- `application_candidate.recorded` is written once per candidate content, carrying the
  candidate id, both trees, the ordered contributing change sets, the
  `candidate_composition_dependency_hash` with its hash-format version (§7), and the
  score revision at materialization.
- The **binding fact** `{candidate_id, score_revision}` is always a projection, never
  its own authoritative event: the initial binding projects from
  `application_candidate.recorded`'s payload, and each later binding projects from
  the candidate-compatible `amendment.approved` event, which carries the
  `candidate_id` (Decision 0001 §1) — a crash can never leave a new revision
  permanently unbound. Where an `application_candidate.bound` bookkeeping event
  exists it is derived and idempotent, and it is never appended at materialization.
- A run with no write movements records `result_tree == base_tree`.
- A composition conflict is handled **before** recording — `WAITING_HUMAN` or a
  structured failure per DESIGN §5 — never discovered at `apply` time.

**The final verification movement** (`verification.final_movement`, required unless
the gate is waived):

- Its effective grants must not include `repo_write`; read-only enforcement applies to
  its attempts regardless of the part's capabilities (a write-capable part may play
  it). At adapter exit the core verifies the movement's worktree tree equals the
  recorded candidate `result_tree`; a mismatch is `candidate_mismatch`, classified in
  the `grant_denied` failure class — an unauthorized write to the tree under
  verification; the attempt fails with **no quality retry and no fallback**. (If the
  core's own composition disagrees with what it recorded, that is a distinct internal
  corruption error handled by recovery, never attributed to the performer.)
- It is the run's **terminal sink**: it transitively depends (via `needs`) on **every**
  non-draft movement, it has no downstream movements, and no non-draft movement may
  sit outside its dependency closure. Its worktree *is* the candidate `result_tree`,
  so its marks bind to it naturally — and its successful completion *is* the run's
  transition to `SUCCEEDED` (one atomic journal transition). There is consequently no
  window in which the final movement has succeeded while the run is still amendable:
  after it succeeds, the run is terminal and admissibility rejects with
  `run_terminal`. Re-running a succeeded final verifier would require the
  successful-attempt invalidation/replay contract that Decision 0001 §6 defers —
  v0.1 does not allow it.
- Compiler achievability rules (a finalized score whose gate can never be satisfied is
  rejected):
  - `require` ∋ `verified` ⇒ the final movement declares ≥ 1 hard criterion;
  - `require` ∋ `reviewed`, or any predicate present ⇒ it declares ≥ 1 review
    criterion with a typed findings output;
  - `require` ∋ `approved` ⇒ it declares `human_gate: always`;
  - `apply_gate.waived` ⇒ `final_movement` **must** be omitted (keeping the apply
    branching of this section exhaustive); a non-waived score must declare it.

**Predicates** (closed enum, optional, only meaningful with review evidence bound to
the candidate):

- `no_unresolved_blocking_findings` — passes when `review_outcome` ∈
  `{CLEAN, OVERRIDDEN}`.
- `no_blocking_findings` — passes only when `review_outcome = CLEAN` (no blocker was
  ever raised).

The `apply` judgment branches explicitly on the gate:

- **`require` path**: `apply` succeeds only when, for every grade in `require` and
  every predicate, there is evidence whose `subject_tree` equals the candidate's
  `result_tree`, **and** the candidate binding, the expectation, and the final
  movement's marks all belong to the current head revision.
- **`waived` path**: grade, predicate, and final-mark checks are skipped entirely;
  `apply` requires only a current-head candidate binding and the validly recorded
  waiver. (v0.1 schema makes this exhaustive: a waived score has no final movement,
  and a non-waived score's final movement is mandatory — there is no third case.)

Per-movement floors are unchanged: DESIGN §2 rule 2 still guarantees that every
**succeeded** `repo_write` movement carries VERIFIED or APPROVED evidence under its
completing, non-superseded attempt. The apply judgment, however, is made on the
candidate — never summed over movements.

### 7. Candidate compatibility — the shared judgment

Decision 0001 §1 already excludes terminal runs, and 0001 §6 excludes any amendment
that changes a succeeded movement's execution dependencies. Once a candidate has been
recorded, every approval — auto or human — must additionally be
**candidate-compatible**. This judgment is normative for both documents; there is no
separate field whitelist:

```text
candidate-compatible iff
  1. no movement with a completed successful (non-superseded) attempt has a
     different execution_dependency_hash under the patched score, and none is
     removed  (identical to Decision 0001 §6);
  2. the candidate composition identity is unchanged: the core recomputes, from the
     patched score and the recorded successful attempt/change-set mapping,

       candidate_composition_dependency_hash =
         hash(base_tree,
              deterministic ordered contributing movement ids,
              corresponding change-set instance ids/hashes)

     and it must equal the hash recorded with the candidate. Changes that alter the
     composition — movement order, `needs`, contributor membership — are
     incompatible even if the resulting tree would coincidentally be identical;
  3. the patched score remains non-waived, and its designated final movement (or
     redesignated replacement) has no completed successful attempt. (Because the
     final movement is the terminal sink — §6 — its success makes the run terminal,
     so this condition is "the verification episode has not finished, and the gate
     mode is unchanged".)

failure reasons (closed enum, recorded by amendment.rejected(candidate_incompatible)):
  succeeded_dependency_changed | composition_changed |
  verification_episode_finished | verification_mode_changed
```

`require ↔ waived` transitions are permitted only **before** a candidate is recorded
(they are then ordinary verification-block amendments); after the candidate exists,
changing the gate mode requires a new run — a mid-flight switch to `waived` would
leave an active run holding a recorded candidate with no final verifier and no
defined `SUCCEEDED` transition, violating §6's invariants.

This deliberately covers, among others: `apply_gate.require` / `apply_gate.predicates`
changes; redesignating `verification.final_movement` to a movement that has **not
started**; the final movement's own `acceptance` block while it has no completed
successful attempt; `BUDGET_DECREASE`; `policy.amendment` changes; and changes to
not-yet-started read-only movements. `verification.expectation.intent` is never
candidate-compatible once movements have succeeded (§1).

Effects of a candidate-compatible approval: the candidate is re-bound to the new
revision (`application_candidate.bound`, derived from `amendment.approved` — §6), the
current verification episode is superseded per Decision 0001 §2 (RUNNING attempt
cancelled, old-revision gates `decision.obsoleted`), and the final verification
movement re-runs on the new revision against the unchanged candidate `result_tree`
before `apply` can pass. If the remaining budget cannot fund that re-run, the
candidate stays bound but unmarked and the run fails through the normal exhaustion
rules — a bound candidate is never an apply permission by itself.

Everything that is not candidate-compatible and touches executed work requires
cancelling the run and starting a new one.

### 8. `apply`, `promote-score`, and the checkout CAS

"The change set that was verified is the change set that ships" requires a
compare-and-swap against the working checkout and a real transaction, not just mark
equality.

**Preconditions** (checked under the repository state lock):

- No other active (non-terminal) run exists in the repository.
- The selected run is terminal `SUCCEEDED`; `apply` targets its recorded candidate.
- The checkout is clean and its **computed working tree** equals the candidate
  `base_tree`. The working-tree hash is computed with a temporary Git index over the
  current tracked contents (modes, additions, deletions included); the user's index
  and `HEAD` are never modified.

**The application axis.** Applying is not a run state: the run stays terminal
`SUCCEEDED` forever, and a separate per-run **application projection** tracks
shipping:

```text
NOT_APPLIED → APPLYING → APPLIED
                      ↘ FAILED_CLEAN        (rollback verified; retry allowed)
                      ↘ RECOVERY_REQUIRED   (rollback not verified)
```

State preconditions: a normal `apply` starts a transaction only from `NOT_APPLIED` or
`FAILED_CLEAN`. In `APPLIED` it returns an idempotent "already applied" result and
records nothing — including for a no-write candidate (`base_tree == result_tree`). In
`APPLYING` or `RECOVERY_REQUIRED` a normal `apply` is refused; only
`apply --recover` is accepted, and `--recover` is itself refused outside those two
states.

**Transaction:**

1. Record and fsync `apply.started` with the candidate id, the before-tree, the
   touched paths, and recovery information (→ `APPLYING`).
2. Dry-run the candidate patch; then apply it to the working checkout.
3. Recompute the working tree; iff it equals `result_tree`, record
   `apply.completed` (→ `APPLIED`). On any mismatch or failure, restore exactly the
   touched paths to the base tree, re-verify the base hash, and record `apply.failed`
   (→ `FAILED_CLEAN`; retry is allowed).
4. If a crash interrupts and recovery cannot verify the base state was restored, the
   application enters `RECOVERY_REQUIRED`: the CLI refuses apply/promote until
   resolved. `partitur apply --recover` re-examines the checkout under the lock:
   if its tree equals `base_tree`, record `apply.recovery_resolved
   {outcome: rolled_back}` (→ `FAILED_CLEAN`); if it equals `result_tree`, record
   `apply.completed` (→ `APPLIED`); otherwise it stays `RECOVERY_REQUIRED` — the core
   never claims "nothing applied" unless it verified it.

**`promote-score`:**

- Only the **latest revision of a `SUCCEEDED` run** may be promoted, with at most one
  *successful* promotion event, and only after `apply.completed` for the same
  candidate. Promotion is outside the verification gates — it creates no marks and
  grants no apply permission — but has this lifecycle prerequisite, because promotion
  edits a tracked file (`partitur.yaml`) and would otherwise move the checkout away
  from the candidate `base_tree`, permanently blocking `apply`. The order **`apply`,
  then `promote-score`** is an enforced command precondition, not a convention.
  (Promoting from failed/cancelled runs is a possible future option, deliberately
  excluded from v0.1.)
- Promotion is itself a journaled transaction:

  ```text
  score.promotion_started { expected_root_hash, target_snapshot_hash, candidate_id }
  → temp write + fsync + atomic rename of the root score
  → score.promoted + fsync
  ```

  Promotion has its own projection, symmetric to application:

  ```text
  NOT_PROMOTED → PROMOTING → PROMOTED
                          ↘ RECOVERY_REQUIRED
  ```

  State preconditions mirror the application axis: a normal `promote-score` starts
  only from `NOT_PROMOTED`; in `PROMOTED` it returns an idempotent "already
  promoted" result and records nothing; in `PROMOTING | RECOVERY_REQUIRED` the
  normal command is refused and only `promote-score --recover` is accepted, which is
  itself refused outside those two states.

  `partitur promote-score --recover`, under the lock: root hash == target →
  complete `score.promoted` idempotently under the original transaction id; root
  hash == expected → resume the *same* transaction (a repeated
  `score.promotion_started` is an idempotent resume, never a second promotion);
  anything else → stays `RECOVERY_REQUIRED` (the root was changed by something
  else; the CAS can no longer decide). "At most once" means at most one
  *successful* `score.promoted` event.
- `apply` evaluates the selected run's candidate against the expectation of the same
  run revision — never against the root score.

### 9. Display: marks always carry their provenance

A naked `VERIFIED` overstates trivial criteria (`["true"]` passes machinery, not
goals). `status` and `apply` output always attach:

- criterion count and ids/spec hashes, the `subject_tree`, and the `score_revision` —
  e.g. `VERIFIED (2 criteria, tree abc123, rev 4)`;
- attempt history — e.g. `after 1 failed attempt`;
- for REVIEWED: the findings artifact instance id and `review_outcome`; for APPROVED:
  the human gate decision id.

Reviewer identity/vendor is recorded in the findings artifact's provenance as
experiment metadata only; no rule may condition on it (consensus: error-decorrelation
across vendors is unproven).

### 10. Deltas to DESIGN v0.1 introduced by this decision

- `verification.expectation` restructured into `intent` + `apply_gate`
  (require/predicates XOR waived); new `verification.final_movement`.
- Explicit `id` on every acceptance criterion; `criterion_spec_hash` /
  `acceptance_spec_hash` definitions.
- Journal events: `acceptance.started`, `criterion.started`, `criterion.completed`,
  `acceptance.failed`, `acceptance.evaluation_completed`,
  `application_candidate.recorded` (constitutes the initial binding;
  `application_candidate.bound` is derived bookkeeping only), `apply.started`,
  `apply.completed`, `apply.failed`, `apply.recovery_resolved`,
  `score.promotion_started`, `score.promoted`; acceptance recovery table (§2).
- The shared candidate-compatibility judgment (§7) with
  `candidate_composition_dependency_hash`, referenced by Decision 0001 §4/§6.
- Final movement as terminal sink: transitive closure over all non-draft movements,
  no downstream, none outside the closure; its success is the run's `SUCCEEDED`
  transition (compiler-enforced).
- Per-run application projection `NOT_APPLIED | APPLYING | APPLIED | FAILED_CLEAN |
  RECOVERY_REQUIRED` with state preconditions, and promotion projection
  `NOT_PROMOTED | PROMOTING | PROMOTED | RECOVERY_REQUIRED` (run lifecycle
  unchanged); `partitur apply --recover` and `partitur promote-score --recover`.
- `acceptance.failed {reason: recovery_subject_mismatch}`; subject binding carried by
  `acceptance.started` / `criterion.started`.
- Artifact instance identity `(logical_output_id, attempt_id)`; at-most-once emission
  per attempt (`duplicate_artifact_instance` protocol error).
- `candidate_mismatch` structured failure in the `grant_denied` class (no retry, no
  fallback).
- Compiler rules: gate achievability (§6), REVIEWED non-vacuity, final-movement
  dependency closure.
- CLI contract: apply/promote preconditions (no other active run, run `SUCCEEDED`,
  checkout CAS via temporary-index tree hash), the apply transaction with the
  application projection's `RECOVERY_REQUIRED` state, apply-before-promote as an
  enforced precondition, single promotion of a `SUCCEEDED` run's latest revision.
- Journal events `run.succeeded` (waived path: carries candidate payload + binding)
  and `movement.failed {reason: human_gate_rejected}`.
- Gate-mode changes (`require ↔ waived`) barred after candidate recording
  (`verification_mode_changed` in the incompatibility enum).

## Consequences

- `status` can honestly display e.g. `implement: VERIFIED,REVIEWED · plan: APPROVED`,
  and each mark says what tree and revision it proved.
- Every run that reaches `SUCCEEDED` — even a gate-waived spike — carries a recorded
  application candidate, so conflicts surface during the run, not at ship time.
- The cost is one explicit final movement in every score whose gate is not waived, and
  an occasional full-run restart instead of in-place patching of executed work —
  accepted as the honest price of composed, auditable verification in v0.1.
