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
went from eleven edges to twenty-two under review, every addition a category rather than an oversight.

**The precondition, which held.** Fault injection cannot adjudicate contradictory normative text; it
tests whichever reading the implementation chose. The oracle had to become singular first, and it is:
one `(a)`–`(f)` list, referenced from three places and restated nowhere.

## Scope

Exactly Appendix E's: the **control channel** (prepare, quiesce, cancellation, supersession fencing),
**authority acquisition**, and **launch identity handoff**, together with the C.1 rows those depend
on.

The evidence and lifecycle chains are **out of scope** — `attempt.completed` → `movement.succeeded`,
`movement.failed` → `run.failed`, a criterion's error completion → `acceptance.failed`,
`acceptance.evaluation_completed` → `decision.requested`. E defers them because C.2 and C.3 still
express steps as `schedule per §3` and `proceed`, which branch expansion cannot terminate on. This
harness does not cover them and must not be described as covering §7 acceptance or Appendix C
generally.

## Selection manifest

All twenty-two E.2 edges are in scope for v0.2; none is deferred. Each needs a crash injected at a
cut **on either side of each endpoint**, and a resume that evaluates the assertion E.2 states for
that edge.

**Blocks on** is how the harness reaches those cuts, and it differs by endpoint kind. An `R`
endpoint is controlled through its `DurabilityReceipt`: suspend before the operation, and again once
the receipt has been returned. A `B` endpoint has **no durable operation to bracket** — it is
controlled through the neutral probe, which blocks the actor at the named point. Every edge carrying
a `B` is therefore one the harness cannot hang off an fsync at all.

### Prepare and quiesce

| Edge | Blocks on | Driven by | Precondition to reach |
|---|---|---|---|
| `prepare.snapshot_to_plan` | R → R | approver | approval intent established (§9 policy, not merely admissibility) |
| `prepare.plan_to_prepared` | R → R | approver | as above |
| `prepare.prepared_to_observed` | R → **B** | approver, then driver | a driver holding the lease that has not yet polled |
| `quiesce.swept_to_lease_moved` | **B** → R | driver | a pending prepare the driver has observed |
| `quiesce.lease_moved_to_commit_lock` | R → **B** | driver, then approver | sidecar written, approver not yet re-entered |
| `prepare.quarantined_to_abandoned` | R → R | approver or canceller | an abandonment on each reason: `cancelled`, `base_head_changed`, `plan_invalidated` |

### Cancellation

`(a)`–`(f)` are §6's labels. E.4 requires all eight `(b, c, d)` combinations, and its step 3 permits
several assertions to hold at one cut — so an edge whose obligation is unconditional is exercised
across every combination, not only the one where the other predicates are false.

| Edge | Blocks on | Driven by | Combinations required |
|---|---|---|---|
| `cancel.swept_to_terminal` | **B** → R | canceller | **all eight.** Terminalization follows a verified sweep whatever `(b)`, `(c)` and `(d)` do, so restricting this to the all-false case would test the weakest instance of an unconditional obligation |
| `cancel.swept_to_quarantined` | **B** → R | canceller | all four with `(b)` true |
| `cancel.interval_stopped_to_terminal` | R → R | canceller | all four with `(c)` true — varying `(b)` as well as `(d)`, since `(c)` and `(d)` have independent predicates |
| `cancel.fence_decided_to_terminal` | **B** → R | canceller | all four with `(d)` true. **Historical: the mirrored lost-fence bug lived here** |
| `cancel.terminal_to_lease_removed` | R → R | canceller | all four with `(d)` true. **Historical: the forward lost-fence bug lived here** |

### Supersession fencing

Reached through the commit table's deadline and dead-owner branches — a driver that does not
acknowledge, or one that dies mid-drain.

| Edge | Blocks on | Driven by | Cases required |
|---|---|---|---|
| `supersede.swept_to_approved` | **B** → R | approver | both branches — deadline expired with a wedged owner, and an owner verifiably gone. The sweep is attested by no other edge in this group, so it is exercised on each |
| `supersede.interval_stopped_to_approved` | R → R | approver | an interval still open, on **both** branches. A dead owner can leave one open just as a wedged one can, so this is not a deadline-only case |
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
| `launch.adapter.marker_held_to_identity_published` | **B** → R | driver | a trampoline holding its marker, before it publishes |
| `launch.adapter.identity_published_to_recorded` | R → R | driver | identity published, journal append not yet durable |
| `launch.adapter.recorded_to_gate` | R → **B** | driver | recorded identity, gate not yet released |
| `launch.criterion.marker_held_to_identity_published` | **B** → R | driver | as the adapter row, per criterion launch |
| `launch.criterion.identity_published_to_recorded` | R → R | driver | as above |
| `launch.criterion.recorded_to_gate` | R → **B** | driver | as above |

## Execution model — deterministic interleaving, not a self-racing process

Killing a single sequential process would look like coverage while never exercising a concurrent
canceller against a committing approver. The harness **schedules** the interleavings rather than
hoping for them.

Four actors, each advanced one step at a time:

- **driver** — holds the lease, observes prepares, sweeps, renames
- **approver** — prepares, waits, commits
- **canceller** — appends `cancel.requested`, runs the oracle
- **reclaimer** — a second driver attempting to acquire authority

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
2. **Cancellation outranks approval.** No `amendment.approved` after a durable `cancel.requested`.
3. **Recovery is idempotent.** Running it twice from the same durable state produces no additional
   events.
4. **Recovery is deterministic.** Two runs from identical durable state produce identical journals.

Checks 3 and 4 compare **semantic** recovery state and action. Timestamps, generated identifiers, and
diagnostics are excluded — comparing them would fail on differences that carry no meaning, and a
check that cries wolf is a check that gets disabled.

### Post-recovery convergence

*"No terminal run with a live attempt, an open interval, or a surviving lease"* was previously listed
as an invariant. **It is false as an immediate crash invariant**: between `(e)` and `(f)` the run is
terminal while the stale lease deliberately survives, and C.1 exists precisely to clean that state.

It splits in two. The pre-terminal obligations are **edge-owned and cited** — sweep before terminal,
interval close before terminal or approval, terminal or approval before lease removal. What remains
is a check on the fixed point:

> After a non-halted recovery reaches its fixed point, no in-scope terminal control state retains an
> open interval, a released session or process, or residual lease, sidecar, or staging state.

Attempt and movement lifecycle completion stays **out** of this check. Appendix E defers those chains
and this harness does not cover them.

## What this harness is not

It does not validate the identity contracts — RFC 8785's published vectors and the ES6 first-1,000
checksum do that, and `internal/canonical` passes both. It does not settle safety-policy choices,
which are value judgements only review can weigh.

And it does not license changes to Appendix E: what happens when an edge proves impossible to inject
at is E.4's to say, and this file does not restate it.
