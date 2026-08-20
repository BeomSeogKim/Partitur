package docmarker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentMarkerGrammarIsLocked(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "MARKERS.md"))
	if err != nil {
		t.Fatal(err)
	}
	document := string(contents)

	requireOccurrence(t, document, "# Document marker grammar", 1)
	requireOccurrence(t, document, Production, 1)
	requireOccurrence(t, document, "| `clause-evidence` |", 1)
	requireOccurrence(t, document, "| `documentation-claim` |", 1)
	requireOccurrence(t, document, "## Normative invariants", 1)
	requireOccurrence(t, document, "These seven rows are the canonical semantic rules of the grammar.", 1)
	coverageRows := []string{
		"| `payload-byte` | Any document byte outside a recognized marker token. ASCII-whitespace payload bytes need no classification. |",
		"| `byte-granularity` | Coverage assigns each non-whitespace payload byte independently; one physical line may be partitioned across multiple ranges. |",
	}
	coverageTable := "| Term | Definition |\n|---|---|\n" + strings.Join(coverageRows, "\n")
	requireOccurrence(t, document, coverageTable+"\n\n### Marker placement in fenced blocks", 1)
	placementRows := []string{
		"| `whole-block` | If a whole fenced block is one clause, its markers may surround the block. |",
		"| `internal` | Markers may occur inside a block only when the host syntax remains valid and literal marker visibility is accepted. |",
		"| `decomposition` | A fence containing several independently violable normative statements, whether carried by separate payloads or by annotations within one payload, is not one whole-block clause. It may be rewritten as one independently copyable fence per payload; field-level statements are lifted into adjacent prose clauses that cite the resulting coherent specimen. |",
		"| `incompatible-host` | Otherwise the clause must be lifted into adjacent anchored prose or the marker representation must be revised; it must never be merged with another clause or classified as non-normative merely to avoid the placement problem. |",
	}
	placementTable := "| Placement | Rule |\n|---|---|\n" + strings.Join(placementRows, "\n")
	requireOccurrence(t, document, placementTable, 1)
	requireOccurrence(t, document, "Changing specimen bytes is not a marker-level exception.", 1)
	requireOccurrence(t, document, "`baseline-activation` pins the exact blob that results from that review.", 1)
	requireOccurrence(t, document, "Decomposition defines no canonicalization function or alternate byte equality.", 1)
	requireOccurrence(t, document, "An inline\nannotation is not a detached field anchor:", 1)
	criterion := "That statement-level boundary is the P3 preparation gate. P3 classifies every normative statement\n" +
		"and maps each resulting clause to evidence, so a reviewer must be able to discharge one independently\n" +
		"violable field rule without accepting unrelated field rules in the same specimen. Top-level payload\n" +
		"count cannot supply that boundary: before decomposition, A.5's one execution-dependency payload\n" +
		"carried twenty-one annotation runs of independently violable field rules, and treating its 78 lines\n" +
		"as one clause would have made it indistinguishable from the score YAML, whose many independent field\n" +
		"rules already required decomposition."
	requireOccurrence(t, document, criterion, 1)
	requireOccurrence(t, document, "The `decomposition-preservation` invariant below, not a historical blob-and-line inventory, governs\n"+
		"later fenced-block edits.", 1)
	invariantRows := []string{
		"| `unwrapped-names` | Their existing unwrapped appearances are names, not markers. |",
		"| `baseline-activation` | A document enters this regime only when a reviewed baseline registry names the document and its exact blob. |",
		"| `unmarked-requirement` | An unmarked normative requirement is void. |",
		"| `forward-range-coverage` | Every non-whitespace payload byte on an added source line, including the current side of a modified line, must be inside exactly one well-formed current range. |",
		"| `no-normativity-inference` | The fence checks syntax, range coverage, and registry-key equality. It does not infer normativity or re-run the baseline judgement. |",
		"| `baseline-complete-classification` | The enrolled blob has a complete ordered classification with `unclassified == ∅`; every non-whitespace payload byte is classified exactly once as anchored or explicitly non-normative. |",
		"| `decomposition-preservation` | Before a fenced block is decomposed, every normative statement in the original must be inventoried and assigned exactly one resulting carrier before its source annotation is removed. Each resulting specimen must be independently copyable as one coherent whole-block clause or explicitly non-normative. A specimen whose bytes change is a replacement normative clause subject to ordinary review, never a formatting exemption; no clause may be merged or classified non-normative to avoid placement. |",
	}
	for _, row := range invariantRows {
		requireOccurrence(t, document, row, 1)
	}
	invariantTable := "| Invariant | Rule |\n|---|---|\n" + strings.Join(invariantRows, "\n")
	requireOccurrence(t, document, invariantTable+"\n\n## Conferred meaning and activation", 1)
}

func TestDocumentMarkerGrammarAcceptsMultipleRangesWithinOneLine(t *testing.T) {
	document := "<!-- partitur:mark begin anchor=protocol.version -->version 2; <!-- partitur:mark end anchor=protocol.version -->" +
		"<!-- partitur:mark begin anchor=protocol.tokens -->no tokens<!-- partitur:mark end anchor=protocol.tokens -->"

	ranges, err := Parse(document)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 2 {
		t.Fatalf("marker ranges = %d, want 2", len(ranges))
	}
	if ranges[0].Contents != "version 2; " || ranges[1].Contents != "no tokens" {
		t.Fatalf("marker range contents = %q, %q", ranges[0].Contents, ranges[1].Contents)
	}
}

func TestDocumentMarkerGrammarAcceptsBothRegistriesAndExistingIDs(t *testing.T) {
	ids := []string{
		"RC-RESUME-001",
		"RC-DISPOSITION-001",
		"RC-APPLY-001",
		"RC-PROMOTE-001",
		"RA-001",
		"RS-001",
		"INIT-001",
		"ANSWER-001",
		"run.started",
		"git_exit",
		"partitur/criterion-spec",
	}
	var document strings.Builder
	for _, id := range ids {
		document.WriteString("<!-- partitur:mark begin anchor=" + id + " -->clause")
		document.WriteString("<!-- partitur:mark end anchor=" + id + " -->\n")
	}
	document.WriteString("<!-- partitur:mark begin non-normative -->context")
	document.WriteString("<!-- partitur:mark end non-normative -->")

	spans, err := Parse(document.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != len(ids)+1 {
		t.Fatalf("marker spans = %d, want %d", len(spans), len(ids)+1)
	}
}

func TestDocumentMarkerGrammarRejectsMalformedOrAmbiguousRanges(t *testing.T) {
	tests := []struct {
		name     string
		document string
	}{
		{
			name:     "malformed token",
			document: "<!-- partitur:mark begin anchor=RC RESUME 001 -->clause",
		},
		{
			name:     "empty range",
			document: "<!-- partitur:mark begin anchor=RC-RESUME-001 --> \n <!-- partitur:mark end anchor=RC-RESUME-001 -->",
		},
		{
			name:     "mismatched end",
			document: "<!-- partitur:mark begin anchor=RC-RESUME-001 -->clause<!-- partitur:mark end anchor=RC-RESUME-002 -->",
		},
		{
			name:     "nested range",
			document: "<!-- partitur:mark begin anchor=RC-RESUME-001 -->a<!-- partitur:mark begin anchor=RA-001 -->b<!-- partitur:mark end anchor=RA-001 --><!-- partitur:mark end anchor=RC-RESUME-001 -->",
		},
		{
			name:     "duplicate anchor",
			document: "<!-- partitur:mark begin anchor=RA-001 -->a<!-- partitur:mark end anchor=RA-001 --><!-- partitur:mark begin anchor=RA-001 -->b<!-- partitur:mark end anchor=RA-001 -->",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Parse(test.document); err == nil {
				t.Fatal("invalid marker document was accepted")
			}
		})
	}
}

func requireOccurrence(t *testing.T, document, value string, want int) {
	t.Helper()
	if got := strings.Count(document, value); got != want {
		t.Fatalf("MARKERS occurrence of %q = %d, want %d", value, got, want)
	}
}
