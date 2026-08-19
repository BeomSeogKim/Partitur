package docmarker

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const markerProduction = `<!-- partitur:mark (begin|end) (anchor=([A-Za-z0-9]+(?:[._/-][A-Za-z0-9]+)*)|non-normative) -->`

var markerPattern = regexp.MustCompile(markerProduction)

func TestDocumentMarkerGrammarIsLocked(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "MARKERS.md"))
	if err != nil {
		t.Fatal(err)
	}
	document := string(contents)

	requireOccurrence(t, document, "# Document marker grammar", 1)
	requireOccurrence(t, document, markerProduction, 1)
	requireOccurrence(t, document, "| `clause-evidence` |", 1)
	requireOccurrence(t, document, "| `documentation-claim` |", 1)
	requireProseOccurrence(t, document, "Their existing unwrapped appearances are names, not markers.", 1)
	requireProseOccurrence(t, document, "A document enters this regime only when a reviewed baseline registry names the document and its exact blob.", 1)
	requireProseOccurrence(t, document, "An unmarked normative requirement is void.", 1)
	requireProseOccurrence(t, document, "Every added non-whitespace source line must be inside exactly one well-formed current range.", 1)
	requireProseOccurrence(t, document, "The fence checks syntax, range coverage, and registry-key equality. It does not infer normativity or re-run the baseline judgement.", 1)
}

func TestProseOccurrenceNormalizesWhitespaceOnly(t *testing.T) {
	canonical := "The fence checks syntax, range coverage, and registry-key equality. It does not infer normativity or re-run the baseline judgement."
	rewrapped := "The fence checks syntax, range coverage,\nand registry-key equality. It does not infer\tnormativity or re-run the baseline judgement."
	if got := proseOccurrenceCount(rewrapped, canonical); got != 1 {
		t.Fatalf("rewrapped prose occurrences = %d, want 1", got)
	}
	paragraphBreak := strings.Replace(rewrapped, "coverage,\nand", "coverage,\n\nand", 1)
	if !strings.Contains(paragraphBreak, "coverage,\n\nand") {
		t.Fatal("paragraph-break fixture does not contain the injected blank line")
	}
	if restored := strings.Replace(paragraphBreak, "coverage,\n\nand", "coverage,\nand", 1); restored != rewrapped {
		t.Fatal("paragraph-break fixture differs from the passing rewrap by more than one newline")
	}

	mutations := []struct {
		name  string
		value string
	}{
		{name: "changed word", value: "The fence checks syntax, range coverage, and registry-key equality. It does not detect normativity or re-run the baseline judgement."},
		{name: "dropped clause", value: "The fence checks syntax, range coverage, and registry-key equality."},
		{name: "reordered sentences", value: "It does not infer normativity or re-run the baseline judgement. The fence checks syntax, range coverage, and registry-key equality."},
		{name: "paragraph break", value: paragraphBreak},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			if got := proseOccurrenceCount(mutation.value, canonical); got != 0 {
				t.Fatalf("mutated prose occurrences = %d, want 0", got)
			}
		})
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

	spans, err := parseMarkerRanges(document.String())
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
			if _, err := parseMarkerRanges(test.document); err == nil {
				t.Fatal("invalid marker document was accepted")
			}
		})
	}
}

type markerRange struct {
	classification string
	contents       string
}

func parseMarkerRanges(document string) ([]markerRange, error) {
	matches := markerPattern.FindAllStringSubmatchIndex(document, -1)
	remainder := markerPattern.ReplaceAllString(document, "")
	if strings.Contains(remainder, "<!-- partitur:mark") {
		return nil, markerError("malformed marker token")
	}

	seenAnchors := make(map[string]bool)
	var ranges []markerRange
	var openClassification string
	contentStart := 0
	for _, match := range matches {
		boundary := document[match[2]:match[3]]
		classification := document[match[4]:match[5]]
		switch boundary {
		case "begin":
			if openClassification != "" {
				return nil, markerError("nested marker range")
			}
			if strings.HasPrefix(classification, "anchor=") {
				id := strings.TrimPrefix(classification, "anchor=")
				if seenAnchors[id] {
					return nil, markerError("duplicate marker ID")
				}
				seenAnchors[id] = true
			}
			openClassification = classification
			contentStart = match[1]
		case "end":
			if openClassification == "" || classification != openClassification {
				return nil, markerError("marker end does not match open range")
			}
			contents := document[contentStart:match[0]]
			if strings.Trim(contents, " \t\r\n") == "" {
				return nil, markerError("empty marker range")
			}
			ranges = append(ranges, markerRange{classification: classification, contents: contents})
			openClassification = ""
		}
	}
	if openClassification != "" {
		return nil, markerError("unclosed marker range")
	}
	return ranges, nil
}

type markerError string

func (err markerError) Error() string { return string(err) }

func requireOccurrence(t *testing.T, document, value string, want int) {
	t.Helper()
	if got := strings.Count(document, value); got != want {
		t.Fatalf("MARKERS occurrence of %q = %d, want %d", value, got, want)
	}
}

func requireProseOccurrence(t *testing.T, document, value string, want int) {
	t.Helper()
	if got := proseOccurrenceCount(document, value); got != want {
		t.Fatalf("MARKERS prose occurrence of %q = %d, want %d", value, got, want)
	}
}

func proseOccurrenceCount(document, value string) int {
	// Each single ASCII space in the expected prose admits exactly one of two
	// source boundaries: one or more ASCII spaces/tabs, or one LF/CRLF with
	// optional ASCII spaces/tabs on either side. Blank lines, bare CR, other
	// Unicode whitespace, and every non-boundary byte remain significant.
	parts := strings.Split(value, " ")
	if value == "" {
		return 0
	}
	quoted := make([]string, len(parts))
	for index, part := range parts {
		if part == "" || strings.ContainsAny(part, "\t\r\n") {
			return 0
		}
		quoted[index] = regexp.QuoteMeta(part)
	}
	pattern := regexp.MustCompile(strings.Join(quoted, `(?:[ \t]+|[ \t]*\r?\n[ \t]*)`))
	return len(pattern.FindAllStringIndex(document, -1))
}
