package recovery

import (
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

const appendixDHaltReasonCount = 13

func TestAppendixDHaltReasonsMatchConstants(t *testing.T) {
	specReasons := appendixDHaltReasons(t)
	if len(specReasons) != appendixDHaltReasonCount {
		t.Fatalf("Appendix D halt reasons = %d, want declared closed set %d", len(specReasons), appendixDHaltReasonCount)
	}

	constantReasons := haltReasonConstants(t)
	requireSameHaltReasons(t, "Appendix D halt reasons", specReasons, "HaltReason constants", constantReasons)
	actualReasons := make([]string, 0, len(AppendixDHaltReasons()))
	for _, reason := range AppendixDHaltReasons() {
		actualReasons = append(actualReasons, string(reason))
	}
	requireSameHaltReasons(t, "HaltReason constants", constantReasons, "command halt-reason set", actualReasons)
}

func appendixDHaltReasons(t *testing.T) []string {
	t.Helper()

	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(contents), "\n")
	start := uniqueAppendixDLine(t, lines, "# Appendix D — Closed enums")
	end := uniqueAppendixDLine(t, lines, "# Appendix E — Ordered-step boundaries")
	if end <= start {
		t.Fatal("Appendix D ends before it begins")
	}

	const prefix = "**Recovery halts** — conditions that stop a run rather than repairing it:"
	paragraph := ""
	for _, line := range lines[start+1 : end] {
		if line == prefix || strings.HasPrefix(line, prefix+" ") {
			if paragraph != "" {
				t.Fatal("duplicate Appendix D recovery-halts paragraph")
			}
			paragraph = line
			continue
		}
		if paragraph != "" {
			if line == "" {
				break
			}
			paragraph += " " + line
		}
	}
	if paragraph == "" {
		t.Fatal("Appendix D recovery-halts paragraph is missing")
	}
	const suffix = ". Each `missing_*` reason covers **both** absence and hash mismatch:"
	index := strings.Index(paragraph, suffix)
	if index == -1 {
		t.Fatalf("Appendix D recovery-halts paragraph is malformed: %q", paragraph)
	}
	entries := strings.TrimSpace(strings.TrimPrefix(paragraph[:index], prefix))
	if entries == "" {
		t.Fatal("Appendix D recovery-halts extraction produced no entries")
	}

	entryPattern := regexp.MustCompile("^`([a-z][a-z0-9_]*)`$")
	var reasons []string
	for _, entry := range strings.Split(entries, ",") {
		entry = strings.TrimSpace(entry)
		match := entryPattern.FindStringSubmatch(entry)
		if match == nil {
			t.Fatalf("unparseable Appendix D recovery-halt entry %q", entry)
		}
		reasons = append(reasons, match[1])
	}
	if len(reasons) == 0 {
		t.Fatal("Appendix D recovery-halts extraction produced no reasons")
	}
	requireUniqueHaltReasons(t, "Appendix D recovery-halt entries", reasons)
	return reasons
}

func uniqueAppendixDLine(t *testing.T, lines []string, want string) int {
	t.Helper()
	index := -1
	for line, value := range lines {
		if value != want {
			continue
		}
		if index != -1 {
			t.Fatalf("duplicate Appendix D boundary %q", want)
		}
		index = line
	}
	if index == -1 {
		t.Fatalf("missing Appendix D boundary %q", want)
	}
	return index
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
	leftSet := requireUniqueHaltReasons(t, leftName, left)
	rightSet := requireUniqueHaltReasons(t, rightName, right)
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
		t.Fatalf("%s and %s differ: only %s=%v; only %s=%v", leftName, rightName, leftName, onlyLeft, rightName, onlyRight)
	}
}

func requireUniqueHaltReasons(t *testing.T, source string, reasons []string) map[string]bool {
	t.Helper()
	seen := make(map[string]bool, len(reasons))
	for _, reason := range reasons {
		if seen[reason] {
			t.Fatalf("%s contains duplicate reason %q", source, reason)
		}
		seen[reason] = true
	}
	return seen
}
