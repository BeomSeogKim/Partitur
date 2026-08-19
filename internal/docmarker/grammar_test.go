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
	invariantRows := []string{
		"| `unwrapped-names` | Their existing unwrapped appearances are names, not markers. |",
		"| `baseline-activation` | A document enters this regime only when a reviewed baseline registry names the document and its exact blob. |",
		"| `unmarked-requirement` | An unmarked normative requirement is void. |",
		"| `forward-range-coverage` | Every added non-whitespace source line must be inside exactly one well-formed current range. |",
		"| `no-normativity-inference` | The fence checks syntax, range coverage, and registry-key equality. It does not infer normativity or re-run the baseline judgement. |",
	}
	for _, row := range invariantRows {
		requireOccurrence(t, document, row, 1)
	}
	invariantTable := "| Invariant | Rule |\n|---|---|\n" + strings.Join(invariantRows, "\n")
	requireOccurrence(t, document, invariantTable+"\n\n## Conferred meaning and activation", 1)
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
