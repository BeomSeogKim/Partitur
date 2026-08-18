# Decision 0001 — Amendment admissibility and auto-approval envelope

- Status: accepted (dual-model consensus, 2026-07-25 — 9 review rounds)
- Refines: DESIGN §1–§4, consensus A-001, I-001
- Scope: historical decision record; not governing system behaviour. [`DESIGN.md`](../DESIGN.md)
  is the sole normative specification.

## Context

`policy.amendment.auto: envelope` promises that "only provably-monotone changes inside
the declared bounds are auto-approved; everything else waits for a human." This document
defines (a) validity rules every proposal must pass regardless of approval mode, (b)
*provably monotone* precisely enough to implement, and (c) what an approval — automatic
or human — does to a run that is already executing. A-001's open risk is the accuracy
of impact-scope computation; the resolution is to make it typed, state-aware, and
fail-closed.

## Decision

### 1. Universal admissibility, then approval policy

Validity is not an envelope concern: a malformed proposal is rejected no matter who
would approve it. Every proposal passes the **admissibility pipeline** under the
repository state lock:

1. **Run lifecycle**: the run must be in `RUNNING` or `WAITING_HUMAN`. Amendments
   against a terminal run (`SUCCEEDED | FAILED | CANCELLED`) → reject
   (`run_terminal`); score changes after a run ends happen by editing the root score
   or starting a new run. Reopening a terminal run would need its own lifecycle
   decision and is out of scope for v0.1.
2. **Stale re-check**: `base_revision` / `base_hash` must match the snapshot head.
3. **Reserved fields** (§7): the patch must not modify core-reserved pointers.
4. **Patch application** to the canonical JSON; any RFC 6902 error → reject.
5. **No-op check**: canonical equality of before/after → reject.
6. **Compiler validation** of the patched score (`partitur validate`, DESIGN §2);
   invalid → reject.
7. **Impact computation and claim containment** (§5); claim narrower than actual →
   reject.

Two **feasibility checks** follow admissibility, in this order, each terminating
immediately on failure — they apply to *every* approval path, auto and human alike:

8. **Executed-dependency feasibility** (§6) — failure terminates as
   `amendment.rejected(executed_dependency_changed)`.
9. **Candidate compatibility** (Decision 0002 §7), when a recorded candidate exists —
   failure terminates as `amendment.rejected(candidate_incompatible)`.

Only a proposal that passes admissibility and both feasibility checks reaches the
**approval policy**:

| Condition (on the base snapshot) | Outcome |
|---|---|
| `base.status == draft` | route to human (`draft_phase`) |
| `base.policy.amendment.auto == off` | route to human (`auto_disabled`) |
| Patch changes `policy.amendment` semantics | route to human (`recognized_non_monotone`) |
| Otherwise (`auto == envelope`) | typed classification (§3) + state guards (§4) |

When a human later decides a routed proposal, the core re-runs, under the lock at
decision time, the same ordered sequence — admissibility 1–7, then feasibility 8–9 —
because the head, the compiler result, and the runtime state (a movement may have
succeeded meanwhile) can all have changed while the proposal waited. The §4
auto-envelope guards are *recomputed for the audit record only*: they never re-route
or block a human approval — routing a guard failure back to the human who is deciding
would loop. A proposal that fails §4 but passes the feasibility checks can be
human-approved; the current execution episode then restarts on the new revision (§2).

Rejection is permanent (a corrected proposal is a new proposal). The proposal's origin
(adapter vs CLI) never affects admissibility or approval rules.

**Journal taxonomy.** Routing is not an outcome; approval is a single authoritative
event:

- `amendment.rejected` — terminal; reason ∈ `run_terminal | stale | patch_error |
  invalid_score | reserved_field | no_op | claim_narrower |
  executed_dependency_changed | candidate_incompatible`. A `candidate_incompatible`
  rejection records the candidate id and which compatibility condition failed
  (closed enum, Decision 0002 §7).
- `amendment.routed_human` — **non-terminal** routing marker; reason ∈ `draft_phase |
  auto_disabled | unclassified_change | recognized_non_monotone |
  runtime_scope_started`
- `amendment.approved { mode: auto | human }` — terminal; the **single authoritative
  transition**. It carries the proposal id (and the human decision id when
  `mode: human`), base and new snapshot hashes, the new revision, the ids of the
  attempts it supersedes, the ids of the pending decisions it obsoletes, and — when an
  application candidate exists — the `candidate_id` it re-binds (Decision 0002 §7).
  `attempt.superseded`, `decision.obsoleted`, and `application_candidate.bound`
  bookkeeping events are all derived idempotently from this one event, including at
  recovery — a crash after its fsync can never leave the new head with stale pending
  decisions or an unbound candidate. The amendment's own human decision is resolved
  *by this event*; no separate `decision.resolved` append is involved.
- `amendment.human_rejected` — terminal; carries the proposal and decision ids and the
  human's reason.

Pending-decision closure is symmetric across all three terminals: `amendment.approved`,
`amendment.human_rejected`, and a decision-time `amendment.rejected(...)` (a human
chose approve but re-validation failed) each carry the `decision_id` and terminally
resolve the amendment's own pending decision in projection — no separate
`decision.resolved` append exists on any path.

Every event records the base hash and the classifier version. The canonical typed
delta is recorded only when a validated patched AST exists (pipeline step 6 on);
earlier rejections record the patch operations hash and the error location instead.
"First failure wins" is deterministic: the pipeline order above, then delta entries
sorted by movement id, then canonical JSON Pointer.

### 2. Approval effects: revision, episode supersede

- The core alone assigns the new snapshot's revision as exactly `base_revision + 1`
  upon approval (auto or human).
- An approved revision change invalidates the **current execution episode**, not just
  RUNNING attempts, regardless of the proposal's origin:
  - RUNNING attempts are superseded: the core records the approval, cancels/terminates
    the adapter, accepts no further protocol or artifact output from the superseded
    attempt after the new head is recorded, and counts the time until actual
    termination against the active budget.
  - In `WAITING_HUMAN`, terminal attempts (`BLOCKED`, completed) keep their history,
    but every pending decision or gate raised on the old revision is closed with
    `decision.obsoleted`, derived idempotently from the `amendment.approved` event's
    `obsoleted_decision_ids` (§1) — except the amendment's own decision, which that
    event resolves directly. Gate approvals never carry across revisions
    (Decision 0002 §3).
  - Execution resumes only in a new attempt on the new revision — nothing keeps
    running, and no old-revision decision stays answerable, under the old authority
    or budget.
- Revision-triggered superseded restarts do **not** consume quality retries. The
  per-movement attempt bound becomes:

  ```text
  total attempts = initial
                 + quality retries consumed
                 + infrastructure fallbacks consumed
                 + revision-triggered superseded restarts
  ```

  The last term lies outside DESIGN §3's `1 + retries + fallbacks` bound and is limited
  only by the active wall-clock budget; this is an explicit amendment to that formula
  (§9).

### 3. Typed comparison, not JSON diff

Neither classification nor impact computation ever inspects RFC 6902 operations or a
generic JSON diff — array-index ambiguity would make the same before/after pair produce
different results depending on the diff algorithm. The core compares the two
**validated score ASTs**:

- `movements` are matched by immutable `movement.id`; for envelope classification,
  count, ids, and order must be identical, otherwise the change is unclassified.
- `policy.allowed_paths`: only order-preserving element removal qualifies.
- `movements[].grants`: treated as a duplicate-free set; only a strict subset
  qualifies.
- `budget.active_wall_clock_min`, `budget.retries_per_movement`: only a strict decrease
  between valid integers qualifies.
- Every other field must be canonically equal, otherwise the change is unclassified.

The audit delta is generated from this comparison as `(movement_id, canonical JSON
Pointer, kind)` triples.

### 4. Whitelist classes with runtime state guards

Structural narrowing is **not** monotone with respect to results that already exist: a
grant removed after a movement produced its change set does not un-produce that change
set. Each class carries a state guard evaluated against the run's execution state
(under the same lock):

| Class | Structural rule | State guard |
|---|---|---|
| `NARROW_PATHS` | Order-preserving removal of an `allowed_paths` entry | No attempt of **any** movement whose effective `paths_ro` or `paths_rw` would change has started — `allowed_paths` scopes reads as well as writes, and a read-only movement's artifacts produced under the wider paths must not survive into a revision that forbids them |
| `NARROW_GRANTS` | Removal of a grant from one movement | That movement **and its transitive downstream** have not started |
| `BUDGET_DECREASE` | Strict decrease of a budget cap | Always allowed; semantics below |

- Guard failure → `routed_human(runtime_scope_started)`.
- Budget caps are always totals for the run/movement. Consumed history is never
  retroactively edited: `remaining_time = max(0, new_cap − consumed)` and
  `remaining_retries = max(0, new_cap − retries_consumed)`. The two remainders block
  different things: `remaining_time == 0` starts **no** new attempt of any kind;
  `remaining_retries == 0` forbids only new *quality-retry* attempts — initial
  attempts, infrastructure fallbacks, and revision-triggered superseded restarts are
  unaffected by the retry remainder (they remain subject to the time remainder).
  Exhaustion transitions through the existing rules.
- Once an application candidate has been recorded (Decision 0002 §6), every approval —
  auto or human — additionally passes the shared **candidate-compatibility** judgment
  of Decision 0002 §7 (§1 step 9) and re-binds the candidate to the new revision.
  Under their state guards the whitelist classes change no *succeeded* movement's
  execution-dependency hash and no candidate composition, and the final movement is
  the run's terminal sink (Decision 0002 §6) — once it succeeds the run is
  `SUCCEEDED` and step 1 of admissibility already rejects (`run_terminal`). So for
  auto-approved amendments this adds a re-binding obligation, not a new refusal path.

Evaluation is a deterministic function of *(base snapshot, patch, run execution state)*
observed under the state lock. No model is ever involved.

### 5. `actual_impact`: expresses every change, deterministically

Claim containment applies to **all** admissible proposals — including those that will
route to a human — so the impact type must express widenings, additions, and generic
edits:

```text
actual_impact = {
  score_changes: [{
    pointer,                     # canonical JSON Pointer into the score AST
    operation: add | remove | replace,
    before_hash?, after_hash?    # canonical-JSON hashes of the subtree
  }],
  authority: {
    allowed_paths: {added: [...], removed: [...]},        # exact pattern strings
    grants: [{movement_id, added: [...], removed: [...]}],
    side_effects: {added: [...], removed: [...]}          # added must be [] in v0.1
  },
  budget: {
    active_wall_clock_min?: {from, to},
    retries_per_movement?:  {from, to}
  }
}
```

**Generation rule.** `score_changes` is produced by the same schema-aware typed
comparison as §3, never from the RFC 6902 operations: collections whose elements carry
ids (`movements`, `parts`, criteria) are matched by id; an id-less array that differs
in any way is recorded as a single `replace` of the whole field (one coarse pointer —
e.g. a movement reorder is one `replace` of `/movements`). The same before/after AST
therefore always yields the same `actual_impact`, whatever operations the proposer
wrote.

`claimed_impact` (DESIGN §2's `{paths, grants, side_effects, budget_delta}`) is
replaced by this same shape — the normative schema is the one above. Containment
(`actual ⊆ claimed`) is component-wise:

- `score_changes`: every actual `(pointer, operation)` must appear in the claim
  (hashes are informative, not compared for containment).
- `authority`: patterns compare as exact strings (no glob-subset reasoning); grant
  changes compare per movement id as set inclusion, separately for `added` and
  `removed`.
- `budget`: a claim contains the actual change iff it declares the same direction and
  a magnitude ≥ the actual magnitude.

A claim narrower than the actual impact on any component → `claim_narrower`.

### 6. Executed-dependency feasibility: no in-place rewrite of executed work

This check runs in the pipeline for **every** approval path (§1 step 8) — it is not a
human-only limit. An approval, whoever grants it, does not create evidence that
already-produced results satisfy the new score.

- The **`execution_dependency_hash`** is bound to the *attempt*, not abstractly to the
  movement, and is defined structurally rather than by field enumeration:

  ```text
  attempt.execution_dependency_hash =
    hash(format_version,
         actual_adapter_id,
         canonical score-derived semantic execute-request projection,
           using extensions.<actual_adapter_id>)
  ```

  i.e. the canonical projection of everything the score contributes to that attempt's
  execute request, keyed by the adapter that actually served it (primary or fallback —
  a fallback attempt binds to the fallback adapter's extension payload, so changing
  `extensions.<fallback>` after a fallback attempt succeeded is detected). The
  projection includes at least: the brief as composed for the performer (instruction,
  goal/context, resolved-question invariants, forwarded
  `verification.expectation.intent`), the movement's `part` and the part's
  capabilities/read-only flag, effective grants and the policy fields that scope them
  (`allowed_paths`, `side_effects`), `inputs`, `needs`, `outputs`, the movement's
  `acceptance` block, and the canonical `extensions.<actual_adapter_id>` payload
  (DESIGN §2/§4 — extension data directly shapes attempt behavior). It explicitly
  excludes non-score, per-attempt values: runtime identity, filesystem paths,
  `session_hint`, `request.feedback`, and remaining budget. The feasibility check
  (§1 step 8) recomputes, for every completed successful non-superseded attempt, the
  patched score's projection **using the adapter id stored with that attempt**, and
  compares it to the attempt's recorded hash.
- **v0.1 rule**: an amendment after which any movement with a completed successful
  (non-superseded) attempt has a different `execution_dependency_hash` — or which
  removes such a movement — cannot be approved in-place. The CLI refuses the approval
  with the reason; the paths out are cancelling the run and starting a new one, or
  amending something else. This check runs at evaluation *and again at human decision
  time* (§1).
- What a human *can* approve mid-run is exactly what passes this rule; once an
  application candidate exists, the approval must additionally be
  **candidate-compatible** per the shared judgment defined normatively in Decision
  0002 §7 (no succeeded movement's dependency hash changed, candidate composition
  identity unchanged, and the final movement has no completed successful attempt).
  There is no separate field whitelist here — the two documents share one judgment.
  Changing `verification.expectation.intent` is an execution-dependency change (it is
  forwarded into briefs), never candidate-compatible once movements have succeeded.
- An atomic invalidation-and-replay contract (superseding the affected movements'
  attempts, artifacts, change sets, and grades plus their transitive downstream, then
  re-running on the new revision) is future work requiring its own decision record.

### 7. Reserved fields and path-language premise

- The v0.1 reserved set is exactly `{ /revision }`. An operation *touches* a reserved
  pointer if its `path` — or its `from` for `move`/`copy` — equals the reserved
  pointer, is a descendant of it, or is an ancestor of it (e.g. a root `replace`).
  `test` operations on reserved pointers are permitted (read-only).
- Element removal from `allowed_paths` is monotone only because the list is an
  **unordered union of positive glob patterns**: negation/exclusion syntax is not part
  of the language (`!` is a literal character), duplicate patterns are a compiler
  error, and order carries no semantics. The envelope still accepts only
  order-preserving removal to keep the audit delta trivial.

### 8. Atomic persistence and recovery

- The approved snapshot is written temp → fsync → atomic rename; then the single
  `amendment.approved` event is appended and fsynced — head change and logical
  supersede become visible together, as one replayable transition — and only then is
  the manifest projection updated (same discipline as artifacts, DESIGN §1).
- A snapshot file with no corresponding `amendment.approved` event (crash window) is
  quarantined at recovery and never becomes head.
- An `amendment.approved` event with no snapshot file is a recovery halt — the journal
  is the authority and this state is corruption, not something to silently repair.

### 9. Deltas to DESIGN v0.1 introduced by this decision

These deltas were absorbed into DESIGN v0.2 and are retained here for provenance:

- `claimed_impact` schema replaced by §5's typed shape.
- Journal events `amendment.rejected` / `amendment.routed_human` (non-terminal) /
  `amendment.approved {mode}` (carrying superseded attempt ids, obsoleted decision
  ids, and the re-bound `candidate_id` where one exists; derived events are
  idempotent) / `amendment.human_rejected` / `decision.obsoleted`, with the reason
  enums of §1.
- Per-movement attempt bound extended by revision-triggered superseded restarts (§2),
  bounded by the active budget instead of the `1 + retries + fallbacks` formula.
- `execution_dependency_hash` and the in-place approval prohibition (§6).
- Amendments admissible only while the run is `RUNNING | WAITING_HUMAN` (§1).

## Consequences

- The v0.1 envelope is deliberately small: it can only *shrink* authority and budget,
  and only where execution has not already consumed the wider authority. Most
  amendments (instruction revisions, plan changes) go to the human — the intended
  A-001 posture, not a limitation to fix.
- The strictest rule is §6: once a movement has succeeded, anything its execution
  depended on cannot be rewritten under it. Runs stay auditable at the price of
  occasionally restarting a run — accepted for v0.1.
- Widening the envelope (glob-subset proofs, criterion-strengthening proofs,
  invalidation-and-replay, terminal-run reopening) is future work requiring its own
  decision record with empirical justification (C-001: evidence before automation).
