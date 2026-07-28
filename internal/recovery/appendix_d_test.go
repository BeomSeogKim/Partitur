package recovery

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestAppendixDHaltReasonsMatchConstants(t *testing.T) {
	specReasons := appendixDHaltReasons(t)
	closedCardinality := len(specReasons)

	constantReasons := haltReasonConstants(t)
	requireSameHaltReasons(t, fmt.Sprintf("Appendix D's %d halt reasons", closedCardinality), specReasons, "HaltReason constants", constantReasons)
	actualReasons := make([]string, 0, len(AppendixDHaltReasons()))
	for _, reason := range AppendixDHaltReasons() {
		actualReasons = append(actualReasons, string(reason))
	}
	requireSameHaltReasons(t, "HaltReason constants", constantReasons, "command halt-reason set", actualReasons)
}

func appendixDHaltReasons(t *testing.T) []string {
	t.Helper()

	return appendixDHaltReasonsAt(t, filepath.Join("..", "..", "docs", "DESIGN.md"))
}

func appendixDHaltReasonsAt(t *testing.T, path string) []string {
	t.Helper()

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reasons, err := parseAppendixDHaltReasons(string(contents))
	if err != nil {
		t.Fatal(err)
	}
	return reasons
}

func parseAppendixDHaltReasons(contents string) ([]string, error) {
	lines := strings.Split(string(contents), "\n")
	start, err := uniqueAppendixDLine(lines, "# Appendix D — Closed enums")
	if err != nil {
		return nil, err
	}
	end, err := uniqueAppendixDLine(lines, "# Appendix E — Ordered-step boundaries")
	if err != nil {
		return nil, err
	}
	if end <= start {
		return nil, fmt.Errorf("Appendix D ends before it begins")
	}

	const prefix = "**Recovery halts** — conditions that stop a run rather than repairing it:"
	paragraphs := appendixDParagraphs(lines[start+1 : end])
	var contributors []string
	for _, paragraph := range paragraphs {
		if paragraph == prefix || strings.HasPrefix(paragraph, prefix+" ") {
			contributors = append(contributors, paragraph)
		}
	}
	if len(contributors) == 0 {
		return nil, fmt.Errorf("Appendix D recovery-halts paragraph is missing")
	}
	if len(contributors) != 1 {
		return nil, fmt.Errorf("duplicate Appendix D recovery-halts paragraph")
	}
	paragraph := contributors[0]
	const suffix = ". Each `missing_*` reason covers **both** absence and hash mismatch:"
	index := strings.Index(paragraph, suffix)
	if index == -1 {
		return nil, fmt.Errorf("Appendix D recovery-halts paragraph is malformed: %q", paragraph)
	}
	entries := strings.TrimSpace(strings.TrimPrefix(paragraph[:index], prefix))
	if entries == "" {
		return nil, fmt.Errorf("Appendix D recovery-halts extraction produced no entries")
	}

	entryPattern := regexp.MustCompile("^`([a-z][a-z0-9_]*)`$")
	var reasons []string
	for _, entry := range strings.Split(entries, ",") {
		entry = strings.TrimSpace(entry)
		match := entryPattern.FindStringSubmatch(entry)
		if match == nil {
			return nil, fmt.Errorf("unparseable Appendix D recovery-halt entry %q", entry)
		}
		reasons = append(reasons, match[1])
	}
	if len(reasons) == 0 {
		return nil, fmt.Errorf("Appendix D recovery-halts extraction produced no reasons")
	}
	if _, err := uniqueHaltReasons("Appendix D recovery-halt entries", reasons); err != nil {
		return nil, err
	}
	return reasons, nil
}

func appendixDParagraphs(lines []string) []string {
	var paragraphs []string
	paragraph := ""
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if paragraph != "" {
				paragraphs = append(paragraphs, paragraph)
				paragraph = ""
			}
			continue
		}
		if paragraph != "" {
			paragraph += " "
		}
		paragraph += line
	}
	if paragraph != "" {
		paragraphs = append(paragraphs, paragraph)
	}
	return paragraphs
}

func uniqueAppendixDLine(lines []string, want string) (int, error) {
	index := -1
	for line, value := range lines {
		if value != want {
			continue
		}
		if index != -1 {
			return 0, fmt.Errorf("duplicate Appendix D boundary %q", want)
		}
		index = line
	}
	if index == -1 {
		return 0, fmt.Errorf("missing Appendix D boundary %q", want)
	}
	return index, nil
}

func TestAppendixDHaltReasonGuardsRejectCorruptedScratchDocuments(t *testing.T) {
	design, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "**Recovery halts** — conditions that stop a run rather than repairing it:"
	const suffix = ". Each `missing_*` reason covers **both** absence and hash mismatch:"
	originalReasons, err := parseAppendixDHaltReasons(string(design))
	if err != nil {
		t.Fatal(err)
	}
	markedReasons := make([]string, 0, len(originalReasons))
	for _, reason := range originalReasons {
		markedReasons = append(markedReasons, "`"+reason+"`")
	}
	fullParagraph := prefix + " " + strings.Join(markedReasons, ", ") + suffix
	for _, test := range []struct {
		name   string
		mutate func(string) string
		check  func([]string, error) error
	}{
		{
			name: "malformed paragraph",
			mutate: func(contents string) string {
				return strings.Replace(contents, suffix, ". Missing the required explanatory suffix:", 1)
			},
			check: func(_ []string, err error) error {
				if err == nil || !strings.Contains(err.Error(), "malformed") {
					return fmt.Errorf("error = %v, want malformed paragraph guard", err)
				}
				return nil
			},
		},
		{
			name: "unparseable entry",
			mutate: func(contents string) string {
				return strings.Replace(contents, "`journal_corrupt`", "journal corrupt", 1)
			},
			check: func(_ []string, err error) error {
				if err == nil || !strings.Contains(err.Error(), "unparseable") {
					return fmt.Errorf("error = %v, want unparseable-entry guard", err)
				}
				return nil
			},
		},
		{
			name: "duplicate paragraph after a blank line",
			mutate: func(contents string) string {
				return strings.Replace(contents, "\n# Appendix E — Ordered-step boundaries", "\n\n"+fullParagraph+"\n\n# Appendix E — Ordered-step boundaries", 1)
			},
			check: func(_ []string, err error) error {
				if err == nil || !strings.Contains(err.Error(), "duplicate") {
					return fmt.Errorf("error = %v, want duplicate guard", err)
				}
				return nil
			},
		},
		{
			name: "empty extraction",
			mutate: func(contents string) string {
				return replaceRecoveryHaltParagraph(contents, prefix+" "+suffix)
			},
			check: func(_ []string, err error) error {
				if err == nil || !strings.Contains(err.Error(), "no entries") {
					return fmt.Errorf("error = %v, want empty-extraction guard", err)
				}
				return nil
			},
		},
		{
			name: "short extraction",
			mutate: func(contents string) string {
				return replaceRecoveryHaltParagraph(contents, prefix+" `"+originalReasons[0]+"`"+suffix)
			},
			check: func(reasons []string, err error) error {
				if err != nil {
					return err
				}
				if err := sameHaltReasons("short Appendix D extraction", reasons, "HaltReason constants", haltReasonConstants(t)); err == nil {
					return fmt.Errorf("short Appendix D extraction unexpectedly matched HaltReason constants")
				}
				return nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			scratch := filepath.Join(t.TempDir(), "DESIGN.md")
			if err := os.WriteFile(scratch, []byte(test.mutate(string(design))), 0o600); err != nil {
				t.Fatal(err)
			}
			reasons, parseErr := parseAppendixDHaltReasons(string(mustReadFile(t, scratch)))
			if err := test.check(reasons, parseErr); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func replaceRecoveryHaltParagraph(contents string, replacement string) string {
	const prefix = "**Recovery halts** — conditions that stop a run rather than repairing it:"
	const suffix = ". Each `missing_*` reason covers **both** absence and hash mismatch:"
	start := strings.Index(contents, prefix)
	if start == -1 {
		return contents
	}
	end := strings.Index(contents[start:], suffix)
	if end == -1 {
		return contents
	}
	end += start + len(suffix)
	return contents[:start] + replacement + contents[end:]
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func haltReasonConstants(t *testing.T) []string {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), "planner.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var reasons []string
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok.String() != "const" {
			continue
		}
		for _, specification := range general.Specs {
			value, ok := specification.(*ast.ValueSpec)
			if !ok || value.Type == nil || len(value.Names) != 1 || len(value.Values) != 1 {
				continue
			}
			identifier, ok := value.Type.(*ast.Ident)
			if !ok || identifier.Name != "HaltReason" {
				continue
			}
			literal, ok := value.Values[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				t.Fatalf("unparseable HaltReason constant %s", value.Names[0].Name)
			}
			reason := strings.Trim(literal.Value, "\"")
			if reason == "" {
				t.Fatalf("HaltReason constant %s has an empty value", value.Names[0].Name)
			}
			reasons = append(reasons, reason)
		}
	}
	if len(reasons) == 0 {
		t.Fatal("HaltReason constant extraction produced no reasons")
	}
	requireUniqueHaltReasons(t, "HaltReason constants", reasons)
	return reasons
}

func requireSameHaltReasons(t *testing.T, leftName string, left []string, rightName string, right []string) {
	t.Helper()
	if err := sameHaltReasons(leftName, left, rightName, right); err != nil {
		t.Fatal(err)
	}
}

func sameHaltReasons(leftName string, left []string, rightName string, right []string) error {
	leftSet, err := uniqueHaltReasons(leftName, left)
	if err != nil {
		return err
	}
	rightSet, err := uniqueHaltReasons(rightName, right)
	if err != nil {
		return err
	}
	var onlyLeft, onlyRight []string
	for reason := range leftSet {
		if !rightSet[reason] {
			onlyLeft = append(onlyLeft, reason)
		}
	}
	for reason := range rightSet {
		if !leftSet[reason] {
			onlyRight = append(onlyRight, reason)
		}
	}
	slices.Sort(onlyLeft)
	slices.Sort(onlyRight)
	if len(onlyLeft) != 0 || len(onlyRight) != 0 {
		return fmt.Errorf("%s and %s differ: only %s=%v; only %s=%v", leftName, rightName, leftName, onlyLeft, rightName, onlyRight)
	}
	return nil
}

func requireUniqueHaltReasons(t *testing.T, source string, reasons []string) map[string]bool {
	t.Helper()
	seen, err := uniqueHaltReasons(source, reasons)
	if err != nil {
		t.Fatal(err)
	}
	return seen
}

func uniqueHaltReasons(source string, reasons []string) (map[string]bool, error) {
	seen := make(map[string]bool, len(reasons))
	for _, reason := range reasons {
		if seen[reason] {
			return nil, fmt.Errorf("%s contains duplicate reason %q", source, reason)
		}
		seen[reason] = true
	}
	return seen, nil
}
