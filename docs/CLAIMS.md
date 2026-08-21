# Documentation claim manifest

This document defines the P5 manifest schema. The canonical registry exists at
`internal/docclaim/manifest.go`, but it intentionally has zero rows. Population is a separate
reviewed pass, and P5 remains an outstanding prerequisite in `COMPLETION.md` until that pass exists
and its executed gate is promoted there.

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

The population validator reports the exact zero-baseline, zero-claim registry as unpopulated and
fails closed on every structurally partial or invalid population: claims without baselines,
baselines without claims, duplicate or out-of-scope baseline paths, missing scope members,
malformed or stale blob IDs, duplicate claim keys, missing or duplicate anchors, and malformed
evidence coordinates. The canonical test accepts the exact unpopulated state only while
`COMPLETION.md` says P5 is outstanding; any non-empty registry must pass the population validator.
These checks do not decide whether the human population is complete, whether its classification was
correct, or whether an evidence test is semantically strong enough for its claim.

## Discharge

A test name alone does not discharge a claim. After schema validation, the manifest gate
deduplicates evidence coordinates, groups them by package, and invokes one regex-selected
`go test -json -count=1` command per package against one writable cache. Every selected top-level
test must have both `run` and `pass` events, and every package command must exit zero. A package-level
zero with no matching test is a failure.

An evidence test may be behavioural or may mechanically inspect source or another durable
artefact, but it must assert the claim's observable predicate. Review owns the semantic fit between
the prose and that predicate; execution owns whether the cited evidence actually ran.

## Activation boundary

The canonical manifest test binds the real registry path to the validator and executed-evidence
gate. Its exact empty state means only that the reviewed marking pass has not happened; it is not a
completed population and cannot promote P5 into `COMPLETION.md`. The later population change must
add the reviewed baselines and claim rows, then pass the same validator and executed-evidence gate.
Until then, no in-scope prose acquires claim status from this document alone.
