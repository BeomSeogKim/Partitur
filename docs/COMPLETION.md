# Completion contract

Non-normative about behaviour. [`DESIGN.md`](DESIGN.md) owns every definition this document counts;
[`HARNESS.md`](HARNESS.md) owns which fault-injection edges are selected. This document owns the
**denominator**: the set of rows that must be green, so that "is it done?" is a verification rather
than an investigation.

If this file and `DESIGN.md` disagree about what a row *is*, `DESIGN.md` is right and this file is a
defect.

## The fence

1. **Every row must be green.** A row cannot be waived, reclassified, or retired to declare
   completion.
2. **A decision not to implement something is never evidence.** No row is discharged by recording
   that its obligation was declined, deferred, or assigned elsewhere. The only way to remove an
   obligation is to remove it from the specification that states it — a visible edit to a normative
   document, reviewed as such.
3. **This document has no exception list, and no row admits an alternative to its stated evidence.**
   Both were tried in earlier drafts, and both became the waiver they were meant to constrain.
4. **Adding a row is an ordinary decision; it is never automatic.** Work discovered later that is
   not a row is classified before it is scheduled: **repair** — a predicate that cannot be
   evaluated, a hash that cannot be constructed, an obligation already implied by a row — is in
   scope. **Extension** — a new capability or a new guarantee — is out of scope until someone
   amends this document, which is a reviewable change to the finish line.

### Member sets that are read from a source

Some rows quantify over a set defined elsewhere — event types, action kinds, the fault-edge
catalogue. For those rows **the denominator is the normative enumeration in `DESIGN.md`**, never the
set of constants a package happens to declare.

Code is checked *against* that enumeration, not trusted as it. So each such row carries a
reconciliation: the declared constants are exactly the normative members. Deleting a constant then
**fails** the row instead of shrinking it, which is the property an earlier draft lacked — there,
removing a declaration would have quietly removed an obligation.

An obligation therefore disappears by exactly one route: editing the normative document that states
it. That is fence clause 2, applied to member sets rather than to rows.

Additions are safe by construction: a new normative member adds an obligation, and gaining
obligations cannot turn an incomplete system complete.

Where a member set would need an editorial judgement about what counts as a member, this document
does not carry the row at all. Those surfaces are recorded as outstanding prerequisites elsewhere,
not as rows that look checkable and are not.

Not every member set has a normative enumeration to be read from. Some are a **closure over code** —
"the action kinds the between-unit planner can return" is a property of the planner, and no document
owns it; writing one down would create a second, weaker source of truth rather than a stronger one.
A row over such a set says so explicitly and states how its derivation **fails closed**, so that a
construct the derivation cannot resolve stops it by name rather than shrinking it silently. That is
the same protection the reconciliation gives an enumerated set, obtained the only way available when
there is nothing to reconcile against.

### Kinds of check

Every row states one, and a row that states none is not a row.

- **mechanical** — can be executed as a lock; the row names what it checks.
- **manual** — a human confirms it; the row names what they confirm and why a machine cannot.
- **configuration** — the evidence is repository or platform settings, not source. The row names the
  exact setting and its expected value, and is verified by reading that setting.

---

## 1. Commands

**Green when** every command below is dispatched by `cmd/partitur`. **Mechanical.**

The table is the member set. It is written here rather than read from `DESIGN.md`, so that the
denominator cannot change without changing this document.

| Command |
|---|
| `init` |
| `validate` |
| `run` |
| `status` |
| `logs` |
| `answer` |
| `approve` |
| `amend` |
| `cancel` |
| `resume` |
| `apply` |
| `promote-score` |
| `version` |

Currently green: thirteen of the thirteen are dispatched.

> This row proves dispatch, not behaviour — a command could be dispatched and do nothing. Section 8
> holds the now-defined command-outcome scenario domain. Executed behavioural evidence for those
> scenarios remains an outstanding prerequisite.

## 2. Events

**Green when** both hold. **Mechanical.**

| Check |
|---|
| The event types declared in `internal/runstate` are exactly the events enumerated in `DESIGN.md` |
| Every event **not marked *derived*** in `DESIGN.md` Appendix B has at least one non-test append site |

The reconciliation is the first check, not an aside: without it, deleting a declaration would
discharge an obligation.

For this row, the derived set is read from Appendix B's normative `*derived*` markings, never from
`isDerivedEvent` or another Go classification. A Go classification that disagrees with those
markings cannot make a missing producer pass: a normative non-derived event remains in this row's
append-site denominator. The converse — a normative derived event that Go misclassifies, or whose
source projection is absent — is held by section 7, whose first check requires those markings and
the Go classification to be the same set.

This row has already earned its place. It is what showed that the entire human-decision handshake
was projected, planned for, and recoverable while nothing in production ever appended it.

## 3. Recovery

**Green when** all seven hold. **Mechanical**; the first three and the fifth are existing locks.

| Check |
|---|
| Every declared action kind has exactly one execution disposition |
| Every registered step has a planner witness |
| Every action kind the between-unit planner can return is dispatched by the live driver |
| **Replacing any of those dispatch bodies with a refusal fails a named behavioural test, and the unknown-kind path carries its own witness** |
| The planner is total over its declared axes |
| The unit-owned deferral boundary is **unpopulated** |
| **The boundary's type is named by no production file outside its declaration, and that declaration holds exactly the type, its registry, and its accessor** |

The first and the sixth are separate on purpose. The disposition lock treats a unit-owned refusal
as a permitted disposition, so it is satisfied today with several kinds unimplemented. Only the
unpopulated boundary distinguishes "every kind is accounted for" from "every kind is done".

The fourth check is what the third was missing. The third proves a switch case exists; a case that
dispatches to a refusal satisfies it. So the fourth runs the cases: for each kind the harness copies
the source, replaces only that kind's dispatch body with a refusal, and requires a named behavioural
test to fail. The witness map is compared for equality with the derived set, so a new kind without a
witness and a stale witness both fail rather than being skipped.

Building it found that three of the seven kinds had no behavioural test reaching them through the
switch, and that one of those was hidden behind a witness whose name matched but whose path did
not — refusing that dispatch left the named test passing. Refuting a plausible mapping is what a
per-kind *executed* harness is for, and what a per-kind claim could never do.

> This row's third and fourth checks read a denominator derived from the planner's own source, not
> from a normative enumeration: `DESIGN.md` does not name these Go action kinds, because the member
> set is "what the planner can return" rather than something a document owns. The derivation fails
> closed — an unresolvable return expression, an ambiguous construction, or any assignment replacing
> a decision's `Action` value stops it by name, file and line. Closing the last of those by
> recognising right-hand sides was attempted and abandoned: successive revisions were each evaded
> one level down, and one caused production code to rename its error paths to satisfy the checker.
> The guard now decides on the assignment target alone. Decision 0004 records the shape and the
> limit — the harness proves a branch changes its named behavioural outcome, not that the branch
> implements the semantically correct effect.

The sixth check reads its population from `recovery.UnitOwnedDeferrals`, the single typed
representation of that fact. It named a `map[ActionKind]string` until 2026-08-17; the map had no
production caller, so it recorded the fact without expressing it. Decision 0003 records the
replacement.

The sixth check reads the registry directly rather than through its accessor, so a change that
populates the registry and returns nil cannot pass it.

The seventh is why the sixth is worth more than a count. An empty registry is satisfiable beside a
second one, so emptiness alone says nothing about whether deferrals are recorded elsewhere. Naming
is the predicate rather than construction: a composite-literal scan misses `[]T{{}}`, an elided
element, a zero value, and a constructor's return type, while a value of a named type cannot be
produced without its name appearing in some production signature.

It carries an inside clause as well as an outside one because the declaration is necessarily exempt
from its own naming scan, and is therefore the only place a parallel registry can hide. The inside
clause compares that file's **entire declaration list** for equality rather than scanning it: a
second registry, an alias, or a helper whose derived registry never names the type all fail because
they are not on the list. Two review rounds each found a pattern the previous scan missed, which is
the trajectory recorded for the between-unit dispatch lock in section 3's note; equality against a
fixed list is what leaves that trajectory.

What the seventh check cannot reach is stated rather than implied. Code that never names the boundary
— a bare map, a comment, a magic string — is not recognisable as a deferral by any lock, and
rejecting such parallel representations stays with review. That residue is why this row pairs the
two checks instead of claiming either one alone settles the question.

## 4. Fault-injection edges

**Green when** all three hold. **Mechanical.**

| Check |
|---|
| The edge catalogue is identical across `DESIGN.md` Appendix E.2, the edge-id constants in `internal/faultpoint`, and the `HARNESS.md` selection manifest |
| **Every catalogue edge has exactly one disposition row** in the `HARNESS.md` table |
| **Every E.2 endpoint of every catalogue edge has at least one registry record, keyed by `(edge id, endpoint)`; the set of endpoint values on an edge's records is exactly the two endpoints E.2 names for that edge, and every record executed and passed** |

The first already runs in CI.

The second and third exist because the obvious predicate — "no row carries a not-reached
disposition" — is **vacuously satisfiable by deleting the not-reached rows**. Totality against the
catalogue closes that: an edge with no disposition row fails, so a row cannot be disposed of by
removing it. And since the catalogue itself is pinned to `DESIGN.md` E.2 by the first check, the
denominator cannot be shrunk from the harness side either.

The third is the substance, and it is stated as **registry evidence rather than as a claim about
fixtures** because a claim is not executable. Listing an edge against a `nil` fixture, or against a
fixture the harness skips, or against a test in another package that was deleted, would all satisfy
"has a two-sided fixture" while nothing ran.

So the registry holds **at least one record for each E.2 endpoint, keyed by `(edge id, endpoint)`**,
and the set of endpoint values on an edge's records must equal exactly the two endpoints Appendix
E.2 names for that edge. Every record must be executed and passing. An edge with no records, an edge
with only one endpoint represented, two or more records that name only the same endpoint, a record
for a different edge, or an endpoint value E.2 does not name for that edge fails the row.

The row no longer rejects a third or later executed, passing record for an endpoint already proved.
Record multiplicity can grow when another production path proves the same side; that does not change
the normative endpoint member set and does not require this document to enumerate producers.

The key is the pair, not the edge. Keying on the edge alone would let a harness record one endpoint
and satisfy a per-edge entry condition, which is the whole point of a **two-sided** cut: an edge is
covered when the state is proved on both sides of the durable boundary, not when something ran near
it.

**The registry is emitted by the harness run, not committed to the repository.** That is what makes
it evidence rather than an assertion: a hand-authored table could label both endpoints executed and
passed without anything having run, and no schema constrains a claim its author also writes.

| Registry field | Produced by |
|---|---|
| edge id | derived from Appendix E.2, not typed by hand |
| endpoint | which of that edge's two E.2 endpoints this record covers; the pair `(edge id, endpoint)` is the key |
| executed | the harness's own record that it reached and drove that endpoint in this run |
| outcome | the assertion result observed at that endpoint |

The checker consumes the artefact **produced by the same run that it checks**, and an entry with no
in-run producer is not admissible. An edge with no executed evidence fails the row; it cannot be
credited by assertion.

"Not reached by this gate's cuts" is narrower than "unreachable" — it records only that no fixture
drives the edge. Completion requires executed fixtures, not re-classification. A branch genuinely
unreachable by construction is removed from Appendix E.2 with a reason, which is a normative change.

## 5. Reference workflow

**Green when** the repository's reference score runs end to end on Linux and on macOS, exercising
every property below. **Manual** — this is the one row that asks whether the tool is usable rather
than whether it is correct, and no lock can answer that.

The properties are named here rather than left to the score, because a score that declares less
would otherwise pass by declaring less. Each is stated so that a degenerate run fails it — a
no-op write, a decision of the wrong type, or a `resume` with nothing to recover all satisfy a
loosely worded version of these and demonstrate nothing:

| The run must exercise | Why stated this tightly |
|---|---|
| a write movement whose captured change set is **not a no-op** — `result_tree != base_tree` | a successful write attempt is permitted to produce a no-op change set, which would demonstrate nothing |
| a hard criterion that **verifies that deliberate change**, evaluated to a verdict | a criterion that passes without observing the change proves only that it ran |
| an acceptance evaluation reaching a `VERIFIED` mark | — |
| a **`human_gate`** request resolved **`approved`** | a `question`, an amendment, or finalization would satisfy "a human decision" without exercising the gate |
| a kill at a durable point that **leaves a recovery obligation** — the cut between a durable `attempt.completed` and an absent `movement.succeeded` — so the first `resume` must append `movement.succeeded` under Appendix C's movement-succeeded row, and a second `resume` must append nothing | a kill at an already-fixed-point state makes "the required recovery" empty, so `resume` demonstrates nothing; naming a point with a pending obligation is what forces real recovery, and the second invocation is what proves the fixpoint |
| `apply` onto a checkout that **initially differs**, whose final tree equals the pinned candidate | applying onto a checkout that already matches proves nothing about the apply path |

Currently green, on evidence recorded 2026-08-12 at `main` = `b504f643`: one run per platform
carried all six properties, driving the real CLI against this repository with the real `codex`
performer.

| Platform | Run | Property 5's cut | Property 6 |
|---|---|---|---|
| macOS (darwin 25.5.0, arm64) | `019ff186-3528-7e99-81e0-171856267241` | `recovery.attempt.completed`; first `resume` appended `movement.succeeded`, second appended nothing | `apply` exit 0 onto a checkout at the candidate's `base_tree`, final tree equal to the candidate's `result_tree` |
| Linux (`linux/arm64`, kernel 6.12.76) | `019ff2fd-9a3c-765a-b158-ca042a79ad5c` | same | same |

Both runs produced the same `candidate_id` and the same trees. Property 6 was checked against the
tree, not against the command's exit status: the checkout stood at the candidate's `base_tree`
before `apply` and equalled its `result_tree` after.

Property 5 remains reachable deterministically only with a `-tags=faultprobe` build and the
receipt rendezvous, accepted as part of this row's procedure on 2026-08-08 on the condition that
the evidence keeps recording which binary each property needed. Five of the six were confirmed
with ordinary production-CLI commands; that condition still holds and this row does not erode it.

> This row is the reason a reference score cannot be trusted to declare its own difficulty. The
> gate above reads `require: [verified, approved]` because the movement carries a hard criterion
> and `human_gate: always` — a score declaring a waived gate, or no gate, would satisfy a loosely
> worded version of this row while demonstrating nothing about `apply`.

## 6. Verification matrices

**Green when** all four hold.

| Check | Kind |
|---|---|
| Full suite passes, including under the race detector | mechanical |
| Mutation suite passes | mechanical |
| Fault-probe suite passes | mechanical |
| The required status checks below are configured on the protected branch | configuration |

**Protected branch:** `main`. **Required status checks**, exactly:

| Context | Covers |
|---|---|
| `test (ubuntu-latest)` | build, vet, suite, race, and the fault-probe catalogue locks |
| `test (macos-latest)` | the same, on the second platform |
| `mutation` | the mutation proofs |
| `gitleaks` | the secret scan |

The fault-probe suite needs no separate context: it runs as a step inside the `test` job, so a
failure there fails a required check already.

This row is not provable from CI configuration — a workflow shows what runs, not what is required to
merge — which is why the expected values are written here and verified by reading the repository's
branch protection settings. A suite that runs but is not required can regress without blocking a
merge, and a lock behind a build tag is invisible to the untagged suite; that has already allowed a
missing catalogue constant to reach review.

## 7. Derived-event source projection

**Green when** all four hold. **Mechanical.**

| Check |
|---|
| The `*derived*` rows of `DESIGN.md` Appendix B are exactly the Go derived classification |
| The rows carrying a `**Run-terminal source:**` marking are exactly the event types whose projection assigns a terminal `RunLifecycle` |
| Every derived source transition named by those rows has a non-test append site for its source |
| **Every named transition is executed**: the source is applied, the derived state effect Appendix B names for that derived event is observed, and replay preserves it |

An empty or unparsable extraction fails all four. A reading that returns no derived rows, no marked
rows, a duplicated marking, or a condition literal outside the closed set is a failure, not a small
denominator.

The fourth is the substance, and it is stated as **executed** rather than as a claim that a fixture
exists. A registry of fixture names is satisfiable by a test that no longer asserts what it was
written to assert — which has already happened in this repository, where an extraction shrank a
mutation and the proof disappeared while the test function remained. So the lock runs the fixture
itself, and the set it runs is compared for equality with the set it parsed: a transition without a
fixture and a fixture without a transition both fail.

The second is what makes the first and third total rather than merely complete today. The
run-terminal set was once stated in prose, in three different phrasings, and a reading of it
returned five of six sources without failing. Deriving the Go side from the projection itself means
an event that terminalizes the run and omits its marking fails the row instead of shrinking it.

**Adding a new normative derived event is not a cheaper path to completion.** It adds a source
transition, and that transition needs an append site and an executed fixture before this row is
green again.

This row proves that each named derived effect is projected and survives replay. It does not prove
that the effect Appendix B names is the *right* effect — that is a normative question, and the
obligation for it is not here.

## 8. Command outcome specification

**Green when** all five hold. **Mechanical.**

| Check |
|---|
| Every command in section 1 has exactly one precondition matrix in `DESIGN.md`, and no matrix names a command outside that table |
| Every command matrix satisfies the precondition-matrix grammar |
| For every command, matrix results, grammar-owned usage, exhaustive absences, and the exit-7 applicability registry partition the global exit-code enumeration exactly once |
| The exit-7 applicability registry answers every command in section 1 |
| Currently red: executed behavioural witnesses completed for 40 of the 96 parsed command-matrix catalog IDs. This row is green only when the witnessed and denominator counts are equal. |

The section 1 command table is the member set. Matrix headings are reconciled for exact equality
with that table, so adding a command without a matrix and adding a matrix for an unlisted command
both fail by command name. `DESIGN.md`'s global exit-code table is the partition denominator; the
accounting does not infer a newly added code to be unused, so such an addition makes every command
incomplete until its owner is stated.

`status` and `logs` retain their command-specific prose mappings as the sole normative owners of
their exit classifications. The lock requires each observation matrix's result-code set to equal
the codes enumerated by its prose mapping, takes exhaustive absences from that mapping's explicit
negation, and leaves exit 7 to the applicability registry. They participate in this one partition
lock rather than being counted again by a separate observation partition.

The first four checks prove that the command-outcome specification artefact is complete and
mechanically reconciled. **They do not prove that production honours a matrix or that any catalog
scenario has executed.** The fifth row carries that execution obligation without an exception list:
its denominator is parsed from the matrices, and its witnessed count is checked against fixtures
that returned successfully. The full missing set remains derived by the reconciliation and is
printed only on explicit request.

## 9. P3 baseline classification

**Green when both rows below are green.** Neither row is evidence for the other.

| Check | Kind |
|---|---|
| The atomic P3 activation check accepts only when every region names the same immutable input blob, the ordered region universe covers every physical source line without a gap or overlap, classifications cover every non-whitespace payload byte exactly once, the classifications materialise as inline marker ranges, and the resulting marked blob and ordered classification match their pins; its fixed completion predicate is `unclassified == ∅`. The check validates structure and never proposes or infers a classification. | mechanical |
| A human reviews every region of the pinned input blob and confirms the complete ordered byte-range classification, including each independently violable normative statement and every explicitly non-normative range. This is manual because a detector would reproduce reviewer errors where judgement is hard and introduce errors elsewhere. | manual |

The staging ledger records one immutable region universe and review receipts. Reviewed and
unreviewed are derived states; it stores no editable status and the completion row stores no pending
region enumeration. Until the manual row is complete and the activation pins are present, the P3
baseline remains red and the document is not enrolled.

---

## Using this contract

**During development.** Each unit inherits this target. Work that is not a row is classified as
repair or extension before it is scheduled. An unclassified discovery is how a finish line moves
without anyone deciding to move it.

**At the audit.** Every row is green. There is no other way for a row to pass.

### What this document is not

**It is a baseline audit, not yet the completion audit.** It carries only rows whose green predicate
is decidable today over a member set it cannot silently lose: either pinned to a normative
enumeration, or — where no document owns the set — derived by a closure that fails closed, as the
member-set section describes. Surfaces that still need an evidence artefact before they can be
stated that way — an inventory mapping normative clauses to their evidence and a claim manifest for
documentation — are tracked as outstanding prerequisites, and each becomes a row here when its
artefact exists.

Five have since been promoted rather than dropped: the derived-event source-projection lock is
section 7, the single typed boundary for unit-owned deferrals is section 3's sixth and seventh
checks, the strengthened dispatch lock is section 3's fourth, and the command-outcome specification
artefact and executed command-witness progress are section 8. The executed-evidence promotion stays
red at its measured shortfall; it moved because the witness registry and its document reconciliation
now exist, not because the obligation was softened. Each moved because its stated artefact exists,
which is the only route out of this list.

So the rule is explicit rather than implied:

> **Completion cannot be declared while any of those prerequisites is outstanding.** A green audit
> against this document is necessary and not sufficient. Declaring the project complete requires
> every prerequisite to have become a row here, and every row to be green.

Stating this is the point of the reduction. A document that quietly counted six rows as the whole
finish line would be the failure it was written to prevent.
