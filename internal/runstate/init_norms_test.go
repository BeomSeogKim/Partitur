package runstate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type initPreconditionRow struct {
	id        string
	directory string
	ignore    string
	score     string
	result    string
}

func TestInitPreconditionMatrixIsSpecified(t *testing.T) {
	// Given
	designRows := initPreconditionDesignRows(t, recoveryDesignLines(t))
	harnessIDs := initPreconditionHarnessIDs(t)
	want := []initPreconditionRow{
		{"INIT-001", "absent", "absent", "absent", "Create `.partitur/`, write the required ignore bytes, create score; exit 0"},
		{"INIT-002", "absent", "absent", "exists", "Create `.partitur/`, write the required ignore bytes, preserve score bytes; exit 0"},
		{"INIT-003", "exists", "absent", "absent", "Write the required ignore bytes, create score; exit 0"},
		{"INIT-004", "exists", "absent", "exists", "Write the required ignore bytes, preserve score bytes; exit 0"},
		{"INIT-005", "exists", "correct bytes", "absent", "Preserve ignore bytes, create score; exit 0"},
		{"INIT-006", "exists", "correct bytes", "exists", "Preserve ignore bytes and score bytes; exit 0"},
		{"INIT-007", "exists", "different bytes", "absent", "Refuse the differing-ignore precondition: modify neither directory nor ignore file, create no score, and exit 2"},
		{"INIT-008", "exists", "different bytes", "exists", "Refuse the differing-ignore precondition: modify neither directory, ignore file, nor score, and exit 2"},
	}

	// When
	got := make(map[string]initPreconditionRow, len(designRows))
	for _, row := range designRows {
		got[row.id] = row
	}

	// Then
	if len(designRows) != len(want) {
		t.Fatalf("init precondition matrix rows = %d, want %d", len(designRows), len(want))
	}
	for _, wantRow := range want {
		if got[wantRow.id] != wantRow {
			t.Fatalf("init precondition row %s = %#v, want %#v", wantRow.id, got[wantRow.id], wantRow)
		}
	}
	requireSameUniqueStrings(t, "init DESIGN catalog IDs", initPreconditionIDs(designRows), "init HARNESS catalog IDs", harnessIDs)

	contents := strings.Join(recoveryDesignLines(t), "\n")
	const ignoreClause = "The required `.partitur/.gitignore` content is exactly the UTF-8 bytes\n`runs/\\nwork/\\n`: `runs/` is first, `work/` is second, the final newline is required, and no\nother bytes or lines are permitted. `init` compares that file byte-for-byte."
	if count := strings.Count(contents, ignoreClause); count != 1 {
		t.Fatalf("init exact-ignore clause count = %d, want 1", count)
	}
}

func initPreconditionDesignRows(t *testing.T, lines []string) []initPreconditionRow {
	t.Helper()

	start := uniqueLineIndex(t, lines, "### `partitur init` precondition matrix")
	end := uniqueLinePrefixIndex(t, lines, "**Command repository anchoring.**")
	if end <= start {
		t.Fatal("init precondition matrix must precede command repository anchoring")
	}
	section := lines[start+1 : end]
	return parseInitPreconditionRows(t, "init DESIGN matrix", section, 5)
}

func initPreconditionHarnessIDs(t *testing.T) []string {
	t.Helper()

	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "HARNESS.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(contents), "\n")
	start := uniqueLineIndex(t, lines, "### Init precondition specification lock")
	end := uniqueLineIndex(t, lines, "## Selection manifest")
	if end <= start {
		t.Fatal("init HARNESS specification lock must precede the selection manifest")
	}
	rows := parseInitPreconditionRows(t, "init HARNESS catalog", lines[start+1:end], 3)
	return initPreconditionIDs(rows)
}

func parseInitPreconditionRows(t *testing.T, table string, lines []string, cells int) []initPreconditionRow {
	t.Helper()

	header := "| Catalog ID | `.partitur/` | `.partitur/.gitignore` | `partitur.yaml` | Result |"
	if cells == 3 {
		header = "| Catalog ID | Selection | Driven by |"
	}
	index := uniqueLineIndex(t, lines, header)
	if index+1 >= len(lines) || lines[index+1] != strings.Repeat("|---", cells)+"|" {
		t.Fatalf("%s table has missing or malformed separator", table)
	}

	var rows []initPreconditionRow
	for index++; index+1 < len(lines) && strings.HasPrefix(lines[index+1], "|"); index++ {
		values := markdownTableCells(t, table, lines[index+1], cells)
		id := strings.Trim(values[0], "`")
		if id == values[0] || !strings.HasPrefix(id, "INIT-") {
			t.Fatalf("%s row has unparseable catalog ID %q", table, values[0])
		}
		row := initPreconditionRow{id: id}
		if cells == 5 {
			row.directory, row.ignore, row.score, row.result = values[1], values[2], values[3], values[4]
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		t.Fatalf("%s extracted no rows", table)
	}
	uniqueStrings(t, table+" IDs", initPreconditionIDs(rows))
	return rows
}

func initPreconditionIDs(rows []initPreconditionRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.id)
	}
	return ids
}
