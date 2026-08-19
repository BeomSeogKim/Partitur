# Document marker grammar

This document defines the one syntax used to mark both normative clauses and documentation claims.
It defines no clause inventory and no claim manifest. Those are separate registries built in later
units, and neither registry may define another marker form.

## Marker token

A marker is an exact HTML comment matched by this Go/RE2 regular expression:

```regex
<!-- partitur:mark (begin|end) (anchor=([A-Za-z0-9]+(?:[._/-][A-Za-z0-9]+)*)|non-normative) -->
```

An `anchor=<marker-id>` begin token and the identical end token delimit one marked byte range. A
`non-normative` begin token and its identical end token delimit one explicitly non-normative byte
range. Tokens may stand on their own lines or occur within a Markdown line, which lets the same
syntax delimit paragraphs, fenced blocks, list items, and individual table rows without interpreting
Markdown or the text's meaning.

The rules are closed:

- A range is non-empty after ASCII whitespace is removed.
- Ranges do not overlap or nest.
- An end token matches the open token exactly, including the marker ID.
- A marker ID is case-sensitive and occurs in at most one range in a document.
- Text beginning with `<!-- partitur:mark` that does not match the production is an error, not text
  that the parser ignores.

Coverage uses these terms:

| Term | Definition |
|---|---|
| `payload-byte` | Any document byte outside a recognized marker token. ASCII-whitespace payload bytes need no classification. |
| `byte-granularity` | Coverage assigns each non-whitespace payload byte independently; one physical line may be partitioned across multiple ranges. |

### Marker placement in fenced blocks

The parser applies the grammar to raw document bytes, so a fenced block does not exclude normative
text from classification. Placement must nevertheless preserve the host specimen's validity and
meaning.

| Placement | Rule |
|---|---|
| `whole-block` | If a whole fenced block is one clause, its markers may surround the block. |
| `internal` | Markers may occur inside a block only when the host syntax remains valid and literal marker visibility is accepted. |
| `decomposition` | A fence containing several independently normative payloads may be split into one fence per payload, with each resulting specimen kept coherent or explicitly non-normative. |
| `incompatible-host` | Otherwise the clause must be lifted into adjacent anchored prose or the marker representation must be revised; it must never be merged with another clause or classified as non-normative merely to avoid the placement problem. |

These placement choices are authoring guidance: a bad choice can corrupt the host specimen, but it
does not change the meaning conferred by an otherwise well-formed marker range. Decomposition also
has the preservation precondition below. That precondition is an invariant because violating it can
silently delete or merge normative content while leaving every resulting marker range well formed.

The marker-ID production admits the existing catalog-ID families (`RC-RESUME-001`, `RA-001`,
`RS-001`, `INIT-001`, and command IDs such as `ANSWER-001`) as well as qualified event, enum, and
domain identifiers such as `run.started`, `git_exit`, and `partitur/criterion-spec`. The
`unwrapped-names` invariant below separates lexical acceptance from conferred marking.

## Normative invariants

These six rows are the canonical semantic rules of the grammar. They are conferred as table syntax
for the same reason document marking is conferred as token syntax: deciding whether reworded prose
means the same thing would recreate the inference problem this grammar removes.

| Invariant | Rule |
|---|---|
| `unwrapped-names` | Their existing unwrapped appearances are names, not markers. |
| `baseline-activation` | A document enters this regime only when a reviewed baseline registry names the document and its exact blob. |
| `unmarked-requirement` | An unmarked normative requirement is void. |
| `forward-range-coverage` | Every non-whitespace payload byte on an added source line, including the current side of a modified line, must be inside exactly one well-formed current range. |
| `no-normativity-inference` | The fence checks syntax, range coverage, and registry-key equality. It does not infer normativity or re-run the baseline judgement. |
| `decomposition-preservation` | Before a fenced block is decomposed, every independently normative statement removed from a resulting specimen must have an adjacent anchored prose carrier; each retained payload must remain byte-identical, independently copyable, and either a coherent whole-block clause or explicitly non-normative. |

## Conferred meaning and activation

The grammar is syntactic. A parser recognizes only the token production and paired ranges; it does
not decide whether prose is normative, whether a range is one coherent clause, or whether cited
evidence proves it.

The `baseline-activation` invariant supplies the transition into this regime. Before enrollment,
this grammar does not change the authority of existing text. Enrollment is the one-time human pass:
classification is byte-range based, and every non-whitespace payload byte in the enrolled blob is
classified exactly once as anchored text or explicitly non-normative text. A normative registry row
is anchored text, not a third inferred form. The baseline records the review; no parser may claim to
reconstruct it from modal verbs, prose, inline-code density, or identifier-shaped names.

After enrollment, normative force is conferred only by an `anchor=<marker-id>` range. The
`unmarked-requirement` invariant supplies the consequence of omitting one. A `non-normative` range
is explicitly outside the normative and claim denominators. This rule is mechanical for text added
after the baseline and is trusted to the one-time human classification for text already in the
baseline blob.

## Two registries, one foreign key

Both registries use the same foreign key: `(document path, marker ID)`. The marker carries no
registry kind and its syntax does not vary by registry.

| Registry kind | What its row maps |
|---|---|
| `clause-evidence` | one anchored normative clause to the evidence that discharges it |
| `documentation-claim` | one anchored documentation claim to what makes the claim true |

A registry row whose key has no unique anchor range in its named document is invalid. Conversely,
an enrolled anchor required by a registry's declared population but absent from that registry is
invalid. The later P3 baseline and P5 manifest define their row fields and populations; they reuse
this key and may not weaken its exact-match rule.

## Forward diff fence

The baseline lock fixes the reviewed blob and its complete ordered classification. For every later
edit it compares that blob with the current document and treats a modified line as one deletion plus
one addition. "Added" therefore covers every payload byte on the current side of an added or modified
line, not only the byte subsequence newly inserted within a line. The `forward-range-coverage`
invariant owns the requirement for each non-whitespace payload byte; the line may span multiple
ranges as long as each such byte is in exactly one. A changed marker ID or boundary must still
reconcile exactly with the applicable registry; deleting text cannot create an unmarked obligation.

The `no-normativity-inference` invariant bounds what the fence claims. Text added inside a
`non-normative` range is mechanically classified as non-normative; if it is written as a requirement
anyway, the grammar makes that requirement void rather than asking a detector to recover the
author's intent.
