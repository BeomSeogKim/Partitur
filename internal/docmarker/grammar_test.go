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
	requireOccurrence(t, document, "These six rows are the canonical semantic rules of the grammar.", 1)
	coverageRows := []string{
		"| `payload-byte` | Any document byte outside a recognized marker token. ASCII-whitespace payload bytes need no classification. |",
		"| `byte-granularity` | Coverage assigns each non-whitespace payload byte independently; one physical line may be partitioned across multiple ranges. |",
	}
	coverageTable := "| Term | Definition |\n|---|---|\n" + strings.Join(coverageRows, "\n")
	requireOccurrence(t, document, coverageTable+"\n\n### Marker placement in fenced blocks", 1)
	placementRows := []string{
		"| `whole-block` | If a whole fenced block is one clause, its markers may surround the block. |",
		"| `internal` | Markers may occur inside a block only when the host syntax remains valid and literal marker visibility is accepted. |",
		"| `decomposition` | A fence containing several independently normative payloads may be split into one fence per payload, with each resulting specimen kept coherent or explicitly non-normative. |",
		"| `incompatible-host` | Otherwise the clause must be lifted into adjacent anchored prose or the marker representation must be revised; it must never be merged with another clause or classified as non-normative merely to avoid the placement problem. |",
	}
	placementTable := "| Placement | Rule |\n|---|---|\n" + strings.Join(placementRows, "\n")
	requireOccurrence(t, document, placementTable, 1)
	invariantRows := []string{
		"| `unwrapped-names` | Their existing unwrapped appearances are names, not markers. |",
		"| `baseline-activation` | A document enters this regime only when a reviewed baseline registry names the document and its exact blob. |",
		"| `unmarked-requirement` | An unmarked normative requirement is void. |",
		"| `forward-range-coverage` | Every non-whitespace payload byte on an added source line, including the current side of a modified line, must be inside exactly one well-formed current range. |",
		"| `no-normativity-inference` | The fence checks syntax, range coverage, and registry-key equality. It does not infer normativity or re-run the baseline judgement. |",
		"| `decomposition-preservation` | Before a fenced block is decomposed, every independently normative statement removed from a resulting specimen must have an adjacent anchored prose carrier; each retained payload must remain byte-identical, independently copyable, and either a coherent whole-block clause or explicitly non-normative. |",
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
