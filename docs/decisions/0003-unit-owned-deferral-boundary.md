# Decision 0003 — Unit-owned deferral boundary

- Status: accepted (2026-08-17)
- Scope: repository completion evidence; not system behaviour

## Context

Recovery recorded unimplemented actions in an empty `map[ActionKind]string`.
Nothing in production read that map. A missing handler therefore reached the
ordinary `ErrUnreachableAction` path, which describes a planner/driver
inconsistency rather than a unit-owned deferral.

The completion contract requires an artefact that makes the repository's
incompleteness explicit and checkable. It does not define an execution outcome
for deferred actions.

## Decision

`recovery.UnitOwnedDeferral` is the one sanctioned production representation of
a recovery action deferred to a named later unit. Its current population is
empty. `UnitOwnedDeferrals` exposes a copy only to the existing recovery
completeness check.

The boundary lock checks three things. The **population** is empty, read from
the accessor rather than from source. Exactly one `UnitOwnedDeferral`
declaration exists, with the `{ Kind ActionKind; Unit string }` shape, at the
recovery boundary. And **no production file outside that declaration names the
type at all**.

Naming is the predicate rather than construction. A composite-literal scan
misses `[]UnitOwnedDeferral{{}}`, an elided element, a map or array element, a
zero value, and a constructor's return type — the first of those was caught by
this decision's own mutation proof before it landed. A value of a named type
cannot be produced without the name appearing in some production signature, so
counting names is total against syntax where counting literals is not.

The walk covers the repository minus `docs`, `spikes`, `reference-workflow`,
and vendored trees. It is stated as an exclusion rather than as a list of
included roots so that a new top-level package is inside the denominator by
default; an inclusion list would have quietly placed it outside.

The declaration file is necessarily exempt from its own naming scan, which
makes it the one place a parallel registry could hide: a second
`[]UnitOwnedDeferral` beside the first would leave the accessor returning the
empty slice while the populated one sat next to it. So the declaration is
bounded rather than skipped — exactly one package-level var may name the
boundary, and it must be the one the accessor reads.

Three mutation proofs pin the three halves: one populates the boundary, one
introduces a second production file naming the type, one adds a second registry
inside the declaration. Each requires the lock
test to fail.

The lock deliberately does not add a dispatch branch. With no deferral to
dispatch, a new typed runtime refusal would be dead production behaviour and a
new error/outcome guarantee. No normative system document defines that
guarantee. Under COMPLETION fence clause 4, this is an **extension**, not a
repair, and needs its own normative decision if an actual deferral requires
one.

The empty typed boundary and its population lock are the **repair**: they make
the already-required completion evidence decidable without inventing runtime
semantics. Consequently, the old conflation remains observable only if a
future change populates the boundary without also making and locking the
necessary runtime decision; that change is deliberately visible in review.

## Mechanical limit

The lock can prove the declared boundary is unique, unpopulated, and unnamed
outside its declaration. It cannot infer that code which never names the type
is semantically a unit-owned deferral — a bare `map[ActionKind]string`, a
comment, a magic string. Review remains responsible for rejecting such parallel
representations and for requiring any future population to carry its runtime
semantics and tests.

That residue is irreducible and is stated rather than represented as a
false-complete mechanical claim. What the naming predicate buys over a
construction scan is that the residue is now only *not using the boundary at
all*, rather than also including every syntactic way of using it that a
literal scan happens not to recognise.
