# Documentation claim manifest

This document defines the P5 manifest schema. It does not populate the manifest. Population is a
separate reviewed pass, and P5 remains an outstanding prerequisite in `COMPLETION.md` until that
pass exists and its executed gate is promoted there.

## Claim definition

A documentation claim is a statement about the code whose `(document path, marker ID)` key is a
row in the documentation-claim manifest. The marker range locates the statement; membership in the
manifest confers its claim status. Words such as "must", "never", function names, and descriptions
of behaviour do not. No detector may infer an omitted claim from prose.

The same anchored range may independently be a normative clause in P3 and a documentation claim in
P5. In particular, `docs/DESIGN.md` is in both scopes. This overlap is intentional: the two
registries assign different evidence roles to the same foreign key, and neither changes the
marker grammar in `MARKERS.md`.

P5 adds no normative grammar invariant. Conferred claim membership, exact scope reconciliation, and
executed discharge are rules of this registry; they do not change marker syntax or the meaning of a
marker across both registries.

## Scope

The population pass covers every Markdown document in this closed scope:

- `README.md`
- `docs/CONCEPT.md`
- `docs/DESIGN.md`
- `docs/HARNESS.md`
- `docs/COMPLETION.md`
- `docs/CLAIMS.md`
- `docs/MARKERS.md`
- every `*.md` directly under `docs/ko/`
- every `*.md` directly under `docs/decisions/`

The Go lock derives the two directory members and reconciles the result with the manifest. Adding
or removing an in-scope document therefore cannot silently leave the population unchanged.

## Go registry schema

The manifest is a Go registry consumed only by its test gate, not a Markdown table. This keeps the
reviewed keys and blob pins static while making discharge executable. Its logical records are:

| Record | Required fields | Rule |
|---|---|---|
| `baseline` | `document_path`, `git_blob` | Exactly one row for every in-scope document. `git_blob` is the lowercase 40-hex Git blob ID of the reviewed document. |
| `claim` | `document_path`, `marker_id`, `evidence_package`, `evidence_test` | The claim key is unique, names one unique anchor in its baseline document, and names one top-level Go test. |

The validator fails closed on an empty manifest, duplicate or out-of-scope baseline paths, missing
scope members, malformed or stale blob IDs, duplicate claim keys, missing or duplicate anchors,
malformed evidence coordinates, and a manifest with no claim rows. These are schema and population
checks; they do not decide whether the human classification was correct or whether an evidence test
is semantically strong enough for its claim.

## Discharge

A test name alone does not discharge a claim. After schema validation, the manifest gate invokes
each row's exact `evidence_package` and `evidence_test` with Go test JSON output and caching disabled.
The row is green only when the output positively records that exact top-level test running and
passing and the command exits zero. A package-level zero with no matching test is a failure.

An evidence test may be behavioural or may mechanically inspect source or another durable
artefact, but it must assert the claim's observable predicate. Review owns the semantic fit between
the prose and that predicate; execution owns whether the cited evidence actually ran.

## Activation boundary

The schema lock uses synthetic documents and evidence to prove these rules without pretending the
real population exists. The later population change must add the reviewed baselines and claim rows,
then run the same validator and executed-evidence gate. Until then, no in-scope prose acquires claim
status from this document alone.
