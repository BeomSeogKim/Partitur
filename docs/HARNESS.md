# Fault-injection harness

Non-normative. [`DESIGN.md`](DESIGN.md) Appendix E owns the boundaries, their assertions, and which
recovery rule owns each one. This document owns the **selection**: which of E.2's edges this harness
injects at, how it drives each, and the checks that span more than one.

Nothing here restates an assertion. Where an obligation is cited by edge id, E.2 is what governs; if
this file and Appendix E disagree, Appendix E is right and this file is a defect.

> The original investigation used pre-final step labels; this specification uses the final §6
> `(a)`–`(f)` labels throughout.

## Why this exists

DESIGN's readiness table marks the prepare/quiesce protocol and its recovery **not proved** —
designed against SPIKE-4's measurements, not measured themselves — and names fault injection as the
forcing function.

Prose review found two genuine durability bugs in that protocol, both only after the duplicated
copies of the cancellation sequence had been normalized into one oracle:

- **The lost fence, forward.** Lease removal — `(f)` in the final labelling — ran before the terminal
  event recorded `fenced_epoch`. A crash between them left recovery seeing no lease, skipping the
  fence branch, and appending `run.cancelled` without the epoch that had already been advanced.
- **The lost fence, mirrored.** After splitting that, `(d)`'s predicate was still "the owner was live
  and must be fenced" — not replayable, because after `(d)` and a crash, recovery observes a
  *verifiably dead* owner, the predicate is false, and the fence is lost from the other side.

Both are crash windows between ordered steps. That is the class a harness enumerates mechanically and
prose enumeration keeps missing by one — which the appendix's own history bears out: E.2's catalog
went from eleven edges to twenty-four, every addition a category rather than an oversight, and then
to twenty-eight when Appendix C made the four previously deferred evidence and lifecycle
consequences selectable.

**The precondition, which held.** Fault injection cannot adjudicate contradictory normative text; it
tests whichever reading the implementation chose. The oracle had to become singular first, and it is:
one `(a)`–`(f)` list, referenced from three places and restated nowhere.

## Scope

Exactly Appendix E's: the **control channel** (prepare, quiesce, cancellation, supersession fencing),
**authority acquisition**, **launch identity handoff**, **adapter execute completion**, and the four
**evidence and lifecycle consequence** chains selected by Appendix C, plus **change-set capture and
composition**.

### Init precondition specification lock

This is a specification-only catalog, not an Appendix E crash edge. It names the complete
`partitur init` precondition matrix that implementation item 6 must exercise at the filesystem and
exit surface; until then, it has no harness fixture.

| Catalog ID | Selection | Driven by |
|---|---|---|
| `INIT-001` | `.partitur/` absent; ignore absent; score absent | specification lock |
| `INIT-002` | `.partitur/` absent; ignore absent; score exists | specification lock |
| `INIT-003` | `.partitur/` exists; ignore absent; score absent | specification lock |
| `INIT-004` | `.partitur/` exists; ignore absent; score exists | specification lock |
| `INIT-005` | `.partitur/` exists; ignore has correct bytes; score absent | specification lock |
| `INIT-006` | `.partitur/` exists; ignore has correct bytes; score exists | specification lock |
| `INIT-007` | `.partitur/` exists; ignore has different bytes; score absent | specification lock |
| `INIT-008` | `.partitur/` exists; ignore has different bytes; score exists | specification lock |

## Selection manifest

This gate reaches thirty-nine E.2 edges. Each receives a crash injected **on either side of each
endpoint**, followed by the fixed-point recovery check below. The other E.2 edges are **not reached
by this gate's cuts**; their recorded, clause-cited dispositions appear in the gate-cut table below.
They are not claims that those edges are unreachable.

**Blocks on** is how the harness reaches those cuts, and it differs by endpoint kind. An `R`
endpoint is controlled through its `DurabilityReceipt`: suspend before the operation, and again once
the receipt has been returned. A `B` endpoint has **no durable operation to bracket** — it is
controlled through the neutral probe, which blocks the actor at the named point. Every edge carrying
a `B` is therefore one the harness cannot hang off an fsync at all.

A `B` point is a crash rendezvous, not a general parent-mutation handoff. If the emitter has not
returned from its `Store.Mutate` or `Driver.Mutate`, that actor still owns the state lock and a
fixture must terminate or release it before another actor mutates. The prepare fixtures rely on the
specific stronger guarantee of `authority.lease_created`: both acquisition and dead-owner reclaim
emit it only after their mutation returns, so the parent may append the prepare while the live driver
is paused there.

### Prepare and quiesce

| Edge | Blocks on | Driven by | Precondition to reach |
|---|---|---|---|
| `prepare.snapshot_to_plan` | R → R | approver | approval intent established (§9 policy, not merely admissibility) |
| `prepare.plan_to_prepared` | R → R | approver | as above |
| `prepare.prepared_to_observed` | R → R | approver, then driver | a driver holding the lease that has not yet written its first receipt |
| `quiesce.observed_to_swept` | R → **B** | driver | a durable quiesce receipt before that receipt's sweep completes |
| `quiesce.swept_to_lease_moved` | **B** → R | driver | a pending prepare the driver has observed |
| `quiesce.lease_moved_to_commit_lock` | R → **B** | driver, then approver | sidecar written, approver not yet re-entered |
| `prepare.quarantined_to_abandoned` | R → R | canceller | a cancellation with a pending prepare, so the cancellation oracle takes `(b)` |

### Routed proposals

| Edge | Selection | Driven by | Reason |
|---|---|---|---|
| `proposal.published_to_blocked_route` | reachable | `TestProposalPublicationKillCuts` | A real blocking-proposal `run` is killed at its immutable record and matching `attempt.blocked` descriptor; recovery quarantines the unreferenced record with its original bytes at the first cut and retains the descriptor-bound record at the second |
| `proposal.blocked_route_to_routed` | reachable | `TestBlockedProposalRouteKillCuts` | A real `run` is killed at the durable `attempt.blocked` descriptor and recovery's durable routed receipt; the recovery route is compared field-for-field with the frozen descriptor while the current project score is invalid |
| `proposal.published_to_routed` | reachable | `TestCLIProposalPublishedToRoutedKillCuts` | A real CLI `amend` is killed at its published record and routed receipt; recovery quarantines the route-absent record with its original bytes at the first cut and retains the route-bound record at the second |
| `proposal.core_finalization_published_to_routed` | reachable | `TestCoreFinalizationPublishedToRoutedKillCuts` | A real draft `run`, question resolution, and `resume` are killed at the core publisher's record and route receipts; the near cut quarantines the original bytes before a fresh record routes, while the far cut retains its raw-hash-bound record and a second `resume` appends no duplicate route. |
| `proposal.routed_to_decision_requested` | reachable | `TestCLIRoutedProposalToDecisionRequestedKillCuts` | A real CLI `amend` is killed at its routed and decision-request receipts; the route cut recovers the matching request while no `attempt.blocked` exists in the journal, and the request cut proves that append is durable and idempotent |

### Cancellation

`(a)`–`(f)` are §6's labels. E.4 requires all eight `(b, c, d)` combinations, and its step 3 permits
several assertions to hold at one cut — so an edge whose obligation is unconditional is exercised
across every combination, not only the one where the other predicates are false.

The following table is the cancellation-combination selection contract for this gate. Its five edges
are reachable through the real `cancel` subprocess matrix below; shared cuts discharge overlapping
endpoint obligations without reducing the required combination set.

For `(d)` false, the fixture supplies a stale-epoch lease, so `RC-RESUME-003` removes it and
re-evaluates before the cancellation row. The no-lease `(d)`-false shape is **not reached by these
tests**; these edges do not require it as a separate predicate branch.

| Edge | Blocks on | Driven by | Combinations required |
|---|---|---|---|
| `cancel.swept_to_terminal` | **B** → R | canceller | **all eight.** Terminalization follows a verified sweep whatever `(b)`, `(c)` and `(d)` do, so restricting this to the all-false case would test the weakest instance of an unconditional obligation |
| `cancel.swept_to_quarantined` | **B** → R | canceller | all four with `(b)` true |
| `cancel.interval_stopped_to_terminal` | R → R | canceller | all four with `(c)` true — varying `(b)` as well as `(d)`, since `(c)` and `(d)` have independent predicates |
| `cancel.fence_decided_to_terminal` | **B** → R | canceller | all four with `(d)` true. **Historical: the mirrored lost-fence bug lived here** |
| `cancel.terminal_to_lease_removed` | R → R | canceller | all four with `(d)` true. **Historical: the forward lost-fence bug lived here** |

### Supersession fencing

Reached through the commit table's silence-expiry and dead-owner branches — a driver that does not
acknowledge, or one that dies mid-drain.

| Edge | Blocks on | Driven by | Cases required |
|---|---|---|---|
| `supersede.swept_to_approved` | **B** → R | approver | both branches — silence expired with a wedged owner, and an owner verifiably gone. The sweep is attested by no other edge in this group, so it is exercised on each |
| `supersede.interval_stopped_to_approved` | R → R | approver | an interval still open, on **both** branches. A dead owner can leave one open just as a wedged one can, so this is not a silence-expiry-only case |
| `supersede.fence_decided_to_approved` | **B** → R | approver | lease still matching, on both branches. The commit table gives the fencing path to a verifiably dead owner as well as a wedged one |
| `supersede.approved_to_lease_removed` | R → R | approver | as above |

### Authority acquisition

| Edge | Blocks on | Driven by | Cases required |
|---|---|---|---|
| `authority.granted_to_lease_created` | R → R | driver or reclaimer | Two fixtures, because only one of the states is reachable by crashing this edge. **Crash fixture:** grant durable, lease absent — a conforming acquisition produces this by crashing after the left receipt, and recovery reclaims at a newer epoch. **Seeded fixture:** a lease with no matching grant. A conforming acquisition can never produce it, since the contract orders the grant first, so the orphan state must be seeded rather than crashed into |

### Launch

Every row applies twice — once for the adapter, once for an external criterion. Both sets are
required; the criterion set is not implied by the adapter set.

| Edge | Blocks on | Driven by | Precondition to reach |
|---|---|---|---|
| `launch.adapter.marker_held_to_identity_published` | **B** → R | driver | block the trampoline after marker acquisition, terminate it before identity publication, and start recovery immediately |
| `launch.adapter.identity_published_to_recorded` | R → R | driver | identity published, journal append not yet durable |
| `launch.adapter.recorded_to_gate` | R → **B** | driver | recorded identity, gate not yet released |
| `launch.criterion.marker_held_to_identity_published` | **B** → R | driver | as the adapter row, per criterion launch |
| `launch.criterion.identity_published_to_recorded` | R → R | driver | as above |
| `launch.criterion.recorded_to_gate` | R → **B** | driver | as above |

### Adapter execute completion

| Edge | Blocks on | Driven by | Precondition to reach |
|---|---|---|---|
| `execute.adapter_swept_to_interval_stopped` | **B** → R | driver | a validated execute response and a zero adapter exit, with the recorded session verified empty and the `adapter` interval not yet closed |
| `execute.interval_stopped_to_outcome` | R → R | driver | the adapter `execution.stopped` durable, the response-derived event not yet appended |

### Change-set capture and composition

| Edge | Blocks on | Driven by | Precondition to reach |
|---|---|---|---|
| `change_set.captured_to_recorded` | R → R | core | a `repo_write` attempt with its checkpoint ref durable and `change_set.recorded` absent |
| `composition.movement_evidence_to_terminal` | R → R | core | a movement-scoped `composition.conflicted` and a movement-scoped `composition.failed` fixture, each with its matching `movement.failed` absent |
| `composition.candidate_evidence_to_terminal` | R → R | core | a candidate-scoped `composition.conflicted` and a candidate-scoped `composition.failed` fixture, each with its matching `run.failed` absent |

### Acceptance subject pinning

| Edge | Selection | Driven by | Reason |
|---|---|---|---|
| `acceptance.subject_pinned_to_started` | B → R | core recovery | A real writer is killed at the static post-pin probe and at the `acceptance.acceptance.started` receipt. The probe is required because production returns but does not publish the attempt-scoped subject-ref receipt to the harness observer |

### Evidence and lifecycle consequences

| Edge | Blocks on | Driven by | Precondition to reach |
|---|---|---|---|
| `lifecycle.attempt_completed_to_movement_succeeded` | R → R | driver | `attempt.completed` durable, `movement.succeeded` absent |
| `lifecycle.movement_failed_to_run_failed` | R → R | driver | `movement.failed` durable, run still nonterminal |
| `lifecycle.draft_performer_completed_to_no_blocking_failure` | R → R | driver / recovery | `TestDraftResultBoundaryKillCuts` kills the real draft interview at `attempt.performer_completed` and `attempt.failed`; recovery appends the classified failure at the first cut and the second resume is byte-identical at both, with `acceptance.started` absent |
| `acceptance.criterion_error_to_failed` | R → R | driver | `criterion.completed {outcome: ERROR}` durable, `acceptance.failed` absent |
| `acceptance.evaluation_completed_to_decision_requested` | R → R | driver | evaluation complete, a required human-gate request absent |

## Gate-cut dispositions

The table is the executable selection contract for this gate. `reachable` means its registry has
emitted passing records for both endpoints: probe-addressed cuts come from the subprocess matrix,
and receipt-addressed cuts come from their receipt fixture. `not reached by this gate's cuts` is
deliberately narrower than unreachable: it records only that this gate has no fixture for the
stated Appendix E branch.

| Edge | Disposition | Owning clause | Reason |
|---|---|---|---|
| `prepare.snapshot_to_plan` | reachable | §6 step 1; §9; E.2 | `TestPreparePublicationKillCuts` emits passing records for snapshot and plan receipts; production `resume` quarantines the unreferenced snapshot, leaves the original head, and reaches a journal fixed point |
| `prepare.plan_to_prepared` | reachable | §6 step 1; B.5; E.2 | `TestPreparePublicationKillCuts` emits passing records for plan and approval-prepared receipts; production `resume` removes the orphan plan rather than quarantining it and reaches a journal fixed point |
| `prepare.prepared_to_observed` | reachable | §6 mutation barrier; E.2 | `TestPrepareQuiesceDriverKillCuts` retains the durable prepare while the production driver is still paused before observation, then at its first quiesce receipt. An ordinary durable mutation is refused `prepare_pending` in both states before recovery |
| `quiesce.observed_to_swept` | reachable | §6 step 2; B.5; E.2 | `TestPrepareQuiesceDriverKillCuts` kills the production driver after its first durable quiesce receipt and at the completed-sweep probe. Both states retain no sidecar and re-enter recovery without resume minting a new quiesce receipt |
| `quiesce.swept_to_lease_moved` | reachable | §6 step 2; E.2 | `TestPrepareQuiesceDriverKillCuts` kills the production driver after the completed-sweep probe and at its durable lease-move receipt. With the matching lease and no sidecar, recovery reaches its own post-sweep probe before fencing; the no-lease control does not reach that probe and commits unfenced |
| `quiesce.lease_moved_to_commit_lock` | reachable | §6 step 3; E.2 | `TestPrepareQuiesceDriverKillCuts` kills the production driver after `prepare.ack.lease`, then cancellation wins without an approval. It separately kills production `resume` at the commit-lock boundary, corrupts the bound plan, and requires repeated `missing_prepare_plan` halts with the pending prepare and sidecar unchanged |
| `prepare.quarantined_to_abandoned` | reachable | §6 `(b)`; §9 snapshot lifecycle; E.2 | `TestPrepareQuarantinedToAbandonedKillCuts` kills the production `cancel` after the snapshot quarantine and after `amendment.approval_abandoned`. The first cut preserves the quarantined snapshot bytes and a `prepare_pending` mutation refusal; the second carries `reason: cancelled` only after that quarantine. Both recover to the cancellation fixed point |
| `proposal.published_to_blocked_route` | reachable | §1 routed-proposal records; §4 blocking handshake; C.1 `RC-RESUME-035`; E.2 | `TestProposalPublicationKillCuts` kills the production run at the published record and its matching blocked descriptor. Recovery quarantines the unreferenced record with its original bytes at the first cut and retains the descriptor raw-hash-bound record at the second |
| `proposal.blocked_route_to_routed` | reachable | §4 blocking handshake; C.1 `RC-RESUME-049`; E.2 | `TestBlockedProposalRouteKillCuts` kills the production run at `attempt.blocked` and the production resume at its routed receipt. The route must match the frozen descriptor while invalid current score input proves recovery did not re-run §9 |
| `proposal.published_to_routed` | reachable | §1 routed-proposal records; C.1 `RC-RESUME-035`; E.2 | `TestCLIProposalPublishedToRoutedKillCuts` kills the production CLI `amend` at `proposal.record.published` and `amendment.routed_human`. Recovery quarantines the route-absent record with its original bytes at the first cut and retains the route-bound record at the second |
| `proposal.core_finalization_published_to_routed` | reachable | §2 finalization; C.1 `RC-RESUME-038`; E.2 | `TestCoreFinalizationPublishedToRoutedKillCuts` kills the production draft lifecycle at `proposal.record.published` and `amendment.routed_human`. The first cut quarantines the original bytes and routes one fresh record on the next `resume`; the second retains the route-bound record and the following `resume` appends neither another record nor another route. |
| `proposal.routed_to_decision_requested` | reachable | §1 routed-proposal records; C.1 `RC-RESUME-037`; E.2 | `TestCLIRoutedProposalToDecisionRequestedKillCuts` kills a real CLI `amend` at `amendment.routed_human` and `amendment.decision.requested`. At the route cut, recovery appends the sole matching request from the routed fields while the journal has no `attempt.blocked`; at the request cut, the same request is already durable and repeated recovery is a fixed point |
| `cancel.swept_to_terminal` | reachable | §6 (a), (e); E.2 | Real `cancel` subprocess matrix covers all eight `(b, c, d)` combinations at both endpoints |
| `cancel.swept_to_quarantined` | reachable | §6 (a)-(b); E.2 | Real `cancel` subprocess matrix covers the four `(b)`-true combinations at both endpoints |
| `cancel.interval_stopped_to_terminal` | reachable | §6 (c)-(e); E.2 | Real `cancel` subprocess matrix covers the four `(c)`-true combinations at both endpoints |
| `cancel.fence_decided_to_terminal` | reachable | §6 (d)-(e); E.2; E.3 | Real `cancel` subprocess matrix covers the four `(d)`-true combinations at both endpoints and checks E.3 at `(d)` |
| `cancel.terminal_to_lease_removed` | reachable | §6 (e)-(f); C.1 terminal row; E.2 | Real `cancel` subprocess matrix covers the four `(d)`-true combinations at both endpoints |
| `supersede.swept_to_approved` | reachable | §6 commit table; E.2 | `TestSupersessionKillMatrix` cuts both endpoints on each silence-expiry and dead-owner branch; every survivor sweep is verified empty before the fenced approval |
| `supersede.interval_stopped_to_approved` | reachable | §6 commit table; E.2 | `TestSupersessionKillMatrix` cuts the durable interval close and approval on both branches, requiring `execution.stopped {reason: superseded, charging: clamped}` before approval |
| `supersede.fence_decided_to_approved` | reachable | §6 commit table; E.2 | `TestSupersessionKillMatrix` cuts the fence decision and durable fenced approval on both branches, retaining the old epoch before the approval and requiring its increment afterward |
| `supersede.approved_to_lease_removed` | reachable | §6 commit table; C.1 stale-lease row; E.2 | `TestSupersessionKillMatrix` cuts the durable approval and stale-lease removal on both branches, then recovers to the lease-free fixed point |
| `authority.granted_to_lease_created` | reachable | §6 authority acquisition; E.2 | Driver/reclaimer crash fixture |
| `launch.adapter.marker_held_to_identity_published` | reachable | §4 launch handoff; E.2 | Adapter trampoline crash fixture |
| `launch.adapter.identity_published_to_recorded` | reachable | §4 launch handoff; E.2 | Adapter trampoline crash fixture |
| `launch.adapter.recorded_to_gate` | reachable | §4 launch handoff; E.2 | Adapter trampoline crash fixture |
| `launch.criterion.marker_held_to_identity_published` | reachable | §7 criterion launch; E.2 | `integration/criterionexec` criterion trampoline crash fixture |
| `launch.criterion.identity_published_to_recorded` | reachable | §7 criterion launch; E.2 | `integration/criterionexec` criterion trampoline crash fixture |
| `launch.criterion.recorded_to_gate` | reachable | §7 criterion launch; E.2 | `integration/criterionexec` criterion trampoline crash fixture |
| `execute.adapter_swept_to_interval_stopped` | reachable | §4 execute completion; E.2 | Adapter execute crash fixture |
| `execute.interval_stopped_to_outcome` | reachable | §4 execute completion; E.2 | Adapter execute crash fixture |
| `change_set.captured_to_recorded` | reachable | §5; B.3; C.2 `RC-RESUME-016`; E.2 | `integration/composition` writer-change-set fixture kills the real writer capture at both endpoints |
| `acceptance.subject_pinned_to_started` | reachable | §7; C.2 `RC-RESUME-018`; E.2 | `TestAcceptanceSubjectPinnedToStartedKillCuts` kills a real writer after its subject ref is durable and after `acceptance.started`; recovery dispatches `RC-RESUME-018`, binds the event to the ref tree, and fails with `worktree_lost` when the pre-event worktree is removed. The left cut uses a probe because the returned attempt-scoped ref receipt is not published to the production receipt observer |
| `composition.movement_evidence_to_terminal` | reachable | B.3; C.1 `RC-RESUME-011`; E.2 | `integration/composition` has conflict-verdict and no-verdict Git-failure fan-in fixtures; each kills both sides of the durable movement composition evidence/terminal cut |
| `composition.candidate_evidence_to_terminal` | reachable | B.3; C.1 `RC-RESUME-011`; E.2 | `integration/composition` has conflict-verdict and no-verdict Git-failure candidate fixtures; each kills both sides of the durable candidate composition evidence/terminal cut |
| `lifecycle.attempt_completed_to_movement_succeeded` | reachable | §7 lifecycle; E.2 | One-movement lifecycle crash fixture |
| `lifecycle.movement_failed_to_run_failed` | reachable | §7 lifecycle; E.2 | Real `run` subprocess matrix uses a terminal adapter-failure fixture at both endpoints; before `resume`, the crashed journal and projection prove the durable movement-failed/run-failed cut window |
| `lifecycle.draft_performer_completed_to_no_blocking_failure` | reachable | §2 draft result boundary; C.2 `RC-RESUME-050`, `RC-RESUME-039`; E.2 | `TestDraftResultBoundaryKillCuts` kills a real draft interview after `attempt.performer_completed` and `attempt.failed`. The first cut appends `draft_no_blocking_output`, realizes it through `RC-RESUME-039`, and leaves `acceptance.started` absent; the second resume is a fixed point at both cuts |
| `acceptance.criterion_error_to_failed` | reachable | §7 acceptance; E.2 | On non-root runners, a real run criterion makes subject verification error after starting by removing worktree permissions. The harness restores those permissions before checking the crashed endpoint and fixed-point recovery; an explicit UID-0 guard rejects runners on which the permission failure is ineffective. The subprocess matrix kills after the durable ERROR completion and after the resulting acceptance failure |
| `acceptance.evaluation_completed_to_decision_requested` | reachable | §7 acceptance; E.2 | Real `run` subprocess fixture cuts both the completed evaluation with the gate request absent and the durable gate request |

## Execution model — deterministic interleaving, not a self-racing process

Killing a single sequential process would look like coverage while never exercising a concurrent
canceller against a committing approver. The harness **schedules** the interleavings rather than
hoping for them.

Four actors, each advanced one step at a time:

- **driver** — holds the lease, observes prepares and cancel requests, sweeps, renames, and
  terminalizes a cancellation in the canceller role (§6 step 4)
- **approver** — prepares, waits, commits
- **canceller** — appends `cancel.requested`; runs the oracle where no live driver does, and after
  terminating a wedged owner (§6 step 6)
- **reclaimer** — a second driver attempting to acquire authority

`canceller` in the edge tables above is §6's **role**, not this actor: the obligations there hold
whichever actor completes the durable request, so a `Driven by: canceller` cell is satisfied by a
terminalizing driver as much as by the cancelling command. The cancellation matrix discharges them
through the cancelling command, against a fixture whose lease owner is verifiably gone. **The
responsive-driver cut is not reached by these tests** — that actor is exercised end to end without a
cut elsewhere. Because one oracle step has one event shape, the assertions do not branch on the
actor (E.2 `cancel.interval_stopped_to_terminal`), so this leaves no endpoint obligation
undischarged; what it leaves unreached is the second live state, not a second assertion.

E.2 coverage is therefore keyed per endpoint pair, as required by
[`COMPLETION.md`](COMPLETION.md) §4, not per producer. Where two producers begin with the same
durable and external semantic preconditions and the recovery assertion does not branch on the
actor, one executed `(edge id, endpoint)` record covers that endpoint for both: a different call
stack alone creates no additional edge. If a producer creates a materially different recovery
state or consequence, this equivalence does not apply; Appendix E.4's branch expansion must
classify that crash cut, possibly by refining an existing edge or adding a new one. Appendix E.4
also distinguishes that per-cut classification obligation from the executed records required per
catalogue edge; the harness must not substitute either obligation for the other.

Enumerate the interleavings of their step sequences to a bounded depth and evaluate the oracles below
at each. This is a model-checking shape, not a stress test: the value is exhaustiveness over a small
state space. A stress test passing ten thousand random schedules proves less than one covering all
schedules of depth six.

**Actors run as subprocesses and are killed, not returned an error.** Injecting a Go error runs
ordinary error handling and deferred cleanup, which erases the state recovery is meant to observe.

**Real, never faked:** the journal and every filesystem operation. **Injectable:** the clock, owner
identity inspection, and wake-up delivery. E.1's probe is a no-op in production and blocking here;
the harness must not require production code to know it exists.

## Oracles

### Cited from Appendix E — not restated here

These were once written as harness invariants. They are per-edge assertions and E.2 owns them; the
harness evaluates them by resuming at the edge and checking what E.2 states.

| Was | Now |
|---|---|
| "No lost fence" | `cancel.fence_decided_to_terminal`, and **E.3 in full**. Its old phrasing — *if any epoch advance became durable* — encodes the model E.3 corrected: nothing at `(d)` is durable, and `(d)` retains a durable *input* rather than producing an output |
| "No lifted barrier with an occupied revision path" | `prepare.quarantined_to_abandoned` |
| "No unrecorded released mutator" | `launch.adapter.recorded_to_gate` and `launch.criterion.recorded_to_gate` |

### Cross-edge checks

These span more than one edge and attach to no single crash window, so they are the harness's own.

1. **At most one pending prepare**, and never two `amendment.approved` for one proposal.
   `TestCrossEdgePrepareCheckProductionSubprocess` observes the ordered decoded journal and exported
   projection while production `approve` is paused after `amendment.approval_prepared`, after the
   approver and driver are killed, and after production `resume` converges. The prepare is observed
   positively at both pre-recovery points; recovery then observes its terminal
   `amendment.approved` exactly once, after the prepare, and no pending prepare.

   The pending-prepare conjunct is reachable from the live driver proposal path, CLI `amend` auto
   approval, and CLI `approve --approve`; its observation points span
   `prepare.prepared_to_observed`, the three quiesce edges, and the supersession commit edges.
   Cancellation can abandon that prepare but cannot create one, while `resume` can only complete or
   abandon the prepare it observed. No reachable interleaving can create two today: every producer
   takes the state lock, and both the disposition guard and ordered projector reject a second
   `amendment.approval_prepared` before the first is terminal.

   The per-proposal approval conjunct is reachable only at the shared commit table entered by the
   live approver or by `resume`, including a crash after the durable `prepare.commit.approved`
   receipt and before command exit. No reachable retry can append a second approval today: the
   first ordered approval clears its matching pending prepare in the same projection transition,
   so re-entry has no matching commit-ready prepare. A decoded synthetic table supplies the
   otherwise unreachable negative controls: two simultaneous prepares fail, two approvals for one
   proposal fail, and two ordered approvals for different proposals pass.
2. **Cancellation outranks approval.** No `amendment.approved` after a durable `cancel.requested`.
   `TestCrossEdgeCancellationCheckProductionSubprocess` reads the ordered decoded journal after a
   production `cancel` reaches its terminal disposition with a pending prepare. It observes exactly
   one `cancel.requested`, exactly one `run.cancelled`, the request before that disposition, and
   either no approval or every approval strictly before the request. This is a positive ordering
   oracle, not merely an absence check. The retained approval position is the last one, so a journal
   that approves both before and after the request is rejected.

   The approval-after-cancellation production violation is **not reached** by this harness's
   interleavings. Every production `amendment.approved` append goes through
   `Txn.approvePrepare` (`internal/runstore/prepare.go:412`), called only at :265, :272, :336, and
   :357. `classifyPrepareCommit` returns the cancellation path when its locked projection has
   `CancelRequested` (`internal/runstore/prepare.go:235-242`), and the fenced re-check refuses the
   append when that same projection has it (`internal/runstore/prepare.go:307-315`).
   `RequestCancellation` projects and appends `cancel.requested` inside its own `Store.Mutate`
   transaction (`internal/runstore/cancellation.go:23-54`), so the fenced re-check closes the window
   between classification and commit. This is not an impossibility claim. The watcher is not the
   deciding point: it stops at the first ordered `cancel.requested`
   (`internal/cancellation/watcher.go:170-176`); :185 merely clears its in-memory pending marker.

   The projector does not duplicate this enforcement. `EventAmendmentApproved` never reads
   `CancelRequested` (`internal/runstate/apply.go:769-866`), while `EventCancelRequested` sets that
   flag without clearing the pending prepare (:884-888). The synthetic negative control therefore
   appends a real prepared plan's matching approval after a durable cancellation, then a matching
   terminal event; the real decoder and projector replay it cleanly, and this check rejects its
   ordered journal. It is the only journal-level enforcement of this invariant. It does not claim to
   catch a reachable production violation; the red control is synthetic.
3. **Recovery is idempotent in both forms below.** Both are required; the first does not imply the
   second.
   - **Repeated resume after convergence:** after one recovery has reached its fixed point, another
     `resume` appends no event and leaves the semantic recovery state unchanged.
   - **First recovery from one pre-recovery durable state:** clone the complete durable and external
     preconditions before recovery. Recover one clone once and the other clone twice; after the
     second clone converges, their semantic recovery states and recovery action traces are equal.
4. **Recovery is deterministic.** Clone the complete durable and external preconditions before
   recovery; independent recoveries produce equal semantic journals, projections, and action traces.

Checks 3 and 4 compare the following **semantic recovery state and action**. The state comparison
retains the ordered event sequence and, for every non-diagnostic event, its type, sequence position,
run and score revision, movement, part, and attempt identity, causation relationship, and all
payload fields; it also retains every exported durable projection field produced from that sequence.
The action comparison retains each decision's recovery `CaseID`, either its named halt or its
action, and every field of that action, including its `ActionKind`, ordered steps, continuation, and
replan outcome. The command result comparison retains exit class and named halt even when stderr
wording is not retained.

Before comparison, generated identifiers are alpha-renamed independently by identifier class in
first-occurrence order: for example the first generated decision and interval become `decision#1`
and `interval#1`. Every later occurrence or reference to the same original identifier maps to the
same canonical name, including references across envelopes, payloads, projection fields, and
actions. Stable input identities and content hashes are retained verbatim. Every timestamp value,
whether in an envelope, payload, projection, or action, is replaced with `<timestamp>`, but each
timestamp field's required presence is retained and event order remains the journal's sequence
order. Before normalization, every clamped `execution.stopped` independently proves its
`charged_duration` equals `min(max(0, observed_at - wall_start), remaining_at_start)`. The
timestamp-derived `charged_duration` and its accumulated projected budget values are then replaced
with declared sentinels, retaining their keys and the `charging` and `reason` classification. This
means checks 3 and 4 do not prove independent recoveries charged the same amount; they prove each
charge obeys the formula while all non-derived semantic consequences remain equal. Diagnostics are
excluded entirely: `log` and `progress` events and command stdout/stderr are not inputs to the
comparison. No other retained value may be discarded merely because it differs between runs;
in particular, `criterion.completed.duration_ms` remains literal because this fixture does not
exercise it and it is not derived from a normalized persisted timestamp pair.

The completeness lock uses the normalizer's timestamp value classifier, rather than a second
timestamp-key spelling list, and a closed vocabulary for budget magnitudes. It observes retained
event payloads and the durable projection; envelope fields and the recovery action trace are
normalized but outside its domain. Each observed retained timestamp or budget magnitude must have a
path-specific normalization or a stated literal reason; therefore a valid timestamp such as
`wall_start` is locked even though its key has no budget-shaped spelling. A budget magnitude is
still recognized by name, because a bare integer cannot be identified by value.

These comparisons must be non-vacuous: their extractor must reject an empty event, projection, or
action domain where the fixture promises one, and the comparison suite must demonstrate that it
distinguishes two deliberately different seeds whose retained semantic consequences differ. A
normalizer or comparison that cannot make that distinction does not satisfy checks 3 or 4.

The clone check declares every durable-file glob as required or deliberately absent. The
criterion-launch prefix requires its initial score and `driver.lease` but records why it has no
prepared plan, quiesce sidecar, or quarantine entry. A separate pending-human-prepare prefix
requires and byte-compares `prepares/*.json`, the score snapshots, and the lease; the plan bytes
are load-bearing because `plan_record_hash` digests the exact record. That fixture deliberately
holds the driver before its quiesce acknowledgement and has discarded no artifact, so the recorded
absence of sidecars and quarantine entries is part of its precondition rather than a silent pass.

### Post-recovery convergence

*"No terminal run with a live attempt, an open interval, or a surviving lease"* was previously listed
as an invariant. **It is false as an immediate crash invariant**: between `(e)` and `(f)` the run is
terminal while the stale lease deliberately survives, and C.1 exists precisely to clean that state.

It splits in two. The pre-terminal obligations are **edge-owned and cited** — sweep before terminal,
interval close before terminal or approval, terminal or approval before lease removal. What remains
is a check on the fixed point:

> After a non-halted recovery reaches its fixed point, no in-scope terminal control state retains an
> open interval, a released session or process, or residual `resume`-owned state in
> `.partitur/runs/<run-id>/driver.lease`, `.partitur/runs/<run-id>/driver.quiesced.<prepare-id>`,
> `.partitur/runs/<run-id>/prepares/<prepare-id>.json`, `.partitur/work/<run-id>/`, a
> `.partitur/runs/<run-id>/scores/revision-*.yaml` not referenced by the journal, an
> `.partitur/runs/<run-id>/inputs/*/revision-*/subject-tree.json` not bound by `attempt.started`, or
> an original `.partitur/runs/<run-id>/proposals/*.json` not referenced by a durable route on the
> active path; quarantined copies are durable forensic results, not residue. No
> in-scope durable consequence remains unrealized — a completed attempt has its movement success, a
> failed movement has its run failure, a criterion error has its acceptance failure, and completed
> evaluation has its required gate request.

The oracle must inspect those concrete families; a glob that matches nothing without proving the
family's expected root and injected positive case is not evidence of absence. Application and
promotion temporaries are excluded because their command-specific recovery owns them.

The `resume` residue enumerator is mechanical, following `terminalCleanup` rather than a prose
guess. Its lease family is the exact `driver.lease` pathname; its sidecar family is regular
`driver.quiesced.<prepare-id>` entries other than the malformed bare prefix; and its staging
families are regular `prepares/*.json` plan records plus the complete per-run
`.partitur/work/<run-id>/` tree. The latter includes adapter and criterion launch handoff children,
their temporary directories, and the attempt worktree without having to predict child names. That
work tree is permitted only for a `WAITING_HUMAN` fixed point, where the human-gate fixture retains
the accepted subject. Criterion captures under `runs/<run-id>/attempts/` are durable evidence, not
resume staging; apply checkout state and promotion's root-level temporary files are owned by their
respective `--recover` commands, so none belongs to this enumeration.

The four effect implications are checked from the durable journal into its projection, never from
the selected recovery case: **completed attempt → movement success** (`attempt.completed` then the
matching `movement.succeeded`, projected `SUCCEEDED`); **failed movement → run failure**
(`movement.failed` then `run.failed`, or its terminal `run_failed: true` effect, projected
`FAILED`); **criterion error → acceptance failure**
(`criterion.completed {outcome: ERROR}` then matching `acceptance.failed`, projected failed
attempt); and **completed evaluation → required gate request** (a gate-required
`acceptance.evaluation_completed` then its matching `decision.requested {decision_type:
human_gate}`, projected pending or resolved human gate). These assert consequences recovery made
durable, not merely the class that chose a continuation.

Every recovery result belongs to exactly one partition: **named halt**, **`WAITING_HUMAN`**,
**ordinary `resume` fixed point**, or **command-specific recovery**. A fixture declares its
command-specific branch as exactly `none`, `application`, or `promotion`; the oracle must require
the declaration and durable projection to agree, never infer the declaration from the projection.
`application` requires an unsettled application projection owned by `apply --recover`, and
`promotion` requires an unsettled promotion projection owned by `promote-score --recover`. A
command-specific declaration with a settled projection fails, as does an unsettled projection under
`none` or the other command. In particular, `APPLYING`, application `RECOVERY_REQUIRED`, `PROMOTING`,
and promotion `RECOVERY_REQUIRED` remain failures unless their matching declared branch or another
norm explicitly permits them. The default declaration is `none`, preserving the existing assertion
that both projections are settled.

This partition is not a general liveness claim. An unresolved blocking decision legitimately lands
in `WAITING_HUMAN`, while a named recovery halt stops before any fixed point is claimed.

## What this harness is not

It does not validate the identity contracts — RFC 8785's published vectors and the ES6 first-1,000
checksum do that, and `internal/canonical` passes both. It does not settle safety-policy choices,
which are value judgements only review can weigh.

And it does not license changes to Appendix E: what happens when an edge proves impossible to inject
at is E.4's to say, and this file does not restate it.
