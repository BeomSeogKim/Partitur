package score

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAmendmentImpactSeparatesContentAndOrder(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	design := string(contents)
	const start = "## 9. Amendments"
	const end = "## 10. Out of scope for v0.2"
	if count := strings.Count(design, start); count != 1 {
		t.Fatalf("%q heading count=%d, want 1", start, count)
	}
	if count := strings.Count(design, end); count != 1 {
		t.Fatalf("%q heading count=%d, want 1", end, count)
	}
	startIndex := strings.Index(design, start)
	endIndex := strings.Index(design, end)
	if endIndex <= startIndex {
		t.Fatalf("%q must follow %q", end, start)
	}
	section := strings.Join(strings.Fields(design[startIndex:endIndex]), " ")

	for _, clause := range []string{
		"**Every score-AST collection whose A.1 projection preserves declaration order is compared in two dimensions:** item content and sequence.",
		"For an id-keyed collection, item content changes are matched by id and recorded as `add`, `remove`, or `replace` at that item's semantic selector.",
		"A sequence difference records one coarse `replace` of the collection selector; when item content and sequence both differ, the impact records both.",
		"Thus a pure reorder records only the collection `replace` — a movement reorder at `/movements`, an output reorder at `/movements[id=build]/outputs` — and never a numeric position.",
		"An id-less array that differs in any way is likewise one coarse `replace` of its whole-field selector.",
	} {
		if !strings.Contains(section, clause) {
			t.Fatalf("§9 typed-impact ordering is missing clause %q", clause)
		}
	}
	if strings.Contains(section, "Where raw order is semantically meaningful, the schema carries an explicit order field rather than relying on position.") {
		t.Fatal("§9 typed-impact ordering still claims semantic order requires an explicit order field")
	}
}
