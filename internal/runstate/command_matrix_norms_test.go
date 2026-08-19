package runstate

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestExit7ApplicabilityRegistryCoversShippingCommands(t *testing.T) {
	// Given
	lines := recoveryDesignLines(t)
	wantCommands := completionCommandIDs(t)

	// When
	rows := exit7ApplicabilityRows(t, lines)
	gotCommands := make([]string, 0, len(rows))
	for _, row := range rows {
		gotCommands = append(gotCommands, row.command)
	}

	// Then
	requireSameUniqueStrings(t,
		"exit-7 applicability registry commands", gotCommands,
		"COMPLETION section 1 commands", wantCommands,
	)
}

func TestPresentCommandMatricesFollowGrammar(t *testing.T) {
	// Given
	lines := recoveryDesignLines(t)
	globalCodes := globalExitCodeSet(t, lines)
	grammar := uniqueLineIndex(t, lines, "### Command precondition-matrix grammar")
	headingPattern := regexp.MustCompile("^### `partitur ([a-z]+(?:-[a-z]+)*)` precondition matrix$")
	exitPattern := regexp.MustCompile(`\bexit ([0-9]+)\b`)

	// When
	seenCommands := make(map[string]bool)
	seenCatalogIDs := make(map[string]string)
	matrixCount := 0
	for index, line := range lines {
		candidate := strings.HasPrefix(line, "#") && strings.Contains(line, "precondition matrix")
		match := headingPattern.FindStringSubmatch(line)
		if !candidate {
			continue
		}
		if match == nil {
			t.Fatalf("malformed command precondition-matrix heading %q", line)
		}
		if index <= grammar {
			t.Fatalf("command matrix %q precedes its grammar", match[1])
		}
		command := match[1]
		if seenCommands[command] {
			t.Fatalf("command %q has more than one precondition matrix", command)
		}
		seenCommands[command] = true
		matrixCount++

		end := len(lines)
		for candidate := index + 1; candidate < len(lines); candidate++ {
			if strings.HasPrefix(lines[candidate], "### ") || strings.HasPrefix(lines[candidate], "## ") {
				end = candidate
				break
			}
		}
		checkPresentCommandMatrix(t, command, lines[index+1:end], globalCodes, exitPattern, seenCatalogIDs)
	}

	// Then
	if matrixCount == 0 {
		t.Fatal("DESIGN contains no command precondition matrices")
	}
}

type exit7ApplicabilityRow struct {
	command string
}

func exit7ApplicabilityRows(t *testing.T, lines []string) []exit7ApplicabilityRow {
	t.Helper()

	section := recoveryDocumentSection(t, lines,
		"### Exit-7 command applicability registry",
		"**Exit codes** — stable categories, so a script can branch without parsing prose:",
	)
	header := "| Command | Reaches a required `Txn.Append`? | Exit 7 disposition | Owner |"
	table := uniqueLineIndex(t, section, header)
	if table+1 >= len(section) || section[table+1] != "|---|---|---|---|" {
		t.Fatal("exit-7 applicability registry has missing or malformed separator")
	}

	var rows []exit7ApplicabilityRow
	for index := table + 2; index < len(section) && strings.HasPrefix(section[index], "|"); index++ {
		cells := markdownTableCells(t, "exit-7 applicability registry", section[index], 4)
		command := strings.Trim(cells[0], "`")
		if cells[0] != "`"+command+"`" || !regexp.MustCompile(`^[a-z]+(?:-[a-z]+)*$`).MatchString(command) {
			t.Fatalf("exit-7 applicability registry has invalid command %q", cells[0])
		}
		wantDisposition := ""
		switch cells[1] {
		case "yes":
			wantDisposition = "Applicable"
		case "no":
			wantDisposition = "Not applicable"
		default:
			t.Fatalf("exit-7 applicability for %s has reachability %q, want yes or no", command, cells[1])
		}
		if cells[2] != wantDisposition {
			t.Fatalf("exit-7 applicability for %s has disposition %q, want %q", command, cells[2], wantDisposition)
		}
		if cells[3] == "" {
			t.Fatalf("exit-7 applicability for %s has no owner", command)
		}
		rows = append(rows, exit7ApplicabilityRow{command: command})
	}
	if len(rows) == 0 {
		t.Fatal("exit-7 applicability registry extracted no rows")
	}
	return rows
}

func completionCommandIDs(t *testing.T) []string {
	t.Helper()

	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "COMPLETION.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(contents), "\n")
	section := recoveryDocumentSection(t, lines, "## 1. Commands", "## 2. Events")
	table := uniqueLineIndex(t, section, "| Command |")
	if table+1 >= len(section) || section[table+1] != "|---|" {
		t.Fatal("COMPLETION section 1 command table has missing or malformed separator")
	}
	pattern := regexp.MustCompile(`^[a-z]+(?:-[a-z]+)*$`)
	var commands []string
	for index := table + 2; index < len(section) && strings.HasPrefix(section[index], "|"); index++ {
		cell := markdownTableCells(t, "COMPLETION section 1 command table", section[index], 1)[0]
		command := strings.Trim(cell, "`")
		if cell != "`"+command+"`" || !pattern.MatchString(command) {
			t.Fatalf("COMPLETION section 1 has invalid command %q", cell)
		}
		commands = append(commands, command)
	}
	if len(commands) == 0 {
		t.Fatal("COMPLETION section 1 extracted no commands")
	}
	uniqueStrings(t, "COMPLETION section 1 commands", commands)
	return commands
}

func checkPresentCommandMatrix(
	t *testing.T,
	command string,
	section []string,
	globalCodes map[int]struct{},
	exitPattern *regexp.Regexp,
	seenCatalogIDs map[string]string,
) {
	t.Helper()

	headerIndex := -1
	var header []string
	for index, line := range section {
		cells, ok := closedMarkdownCells(line)
		if !ok {
			continue
		}
		catalogCount := countString(cells, "Catalog ID")
		resultCount := countString(cells, "Result")
		if catalogCount == 0 && resultCount == 0 {
			continue
		}
		if catalogCount != 1 || resultCount != 1 {
			t.Fatalf("%s precondition matrix header has %d Catalog ID columns and %d Result columns, want one each", command, catalogCount, resultCount)
		}
		if headerIndex != -1 {
			t.Fatalf("%s precondition matrix has more than one catalog table", command)
		}
		headerIndex, header = index, cells
	}
	if headerIndex == -1 {
		t.Fatalf("%s precondition matrix has no Catalog ID and Result table", command)
	}
	if len(header) < 3 {
		t.Fatalf("%s precondition matrix has no precondition column", command)
	}
	if headerIndex+1 >= len(section) || section[headerIndex+1] != strings.Repeat("|---", len(header))+"|" {
		t.Fatalf("%s precondition matrix has missing or malformed separator", command)
	}

	catalogIndex := stringIndex(header, "Catalog ID")
	resultIndex := stringIndex(header, "Result")
	idPattern := regexp.MustCompile("^" + regexp.QuoteMeta(strings.ToUpper(command)) + `-[0-9]{3}$`)
	rowCount := 0
	for index := headerIndex + 2; index < len(section) && strings.HasPrefix(section[index], "|"); index++ {
		cells := markdownTableCells(t, command+" precondition matrix", section[index], len(header))
		for cellIndex, cell := range cells {
			if cell == "" {
				t.Fatalf("%s precondition matrix row has empty %q cell", command, header[cellIndex])
			}
		}
		catalogID := strings.Trim(cells[catalogIndex], "`")
		if cells[catalogIndex] != "`"+catalogID+"`" || !idPattern.MatchString(catalogID) {
			t.Fatalf("%s precondition matrix has invalid catalog ID %q", command, cells[catalogIndex])
		}
		if owner, duplicate := seenCatalogIDs[catalogID]; duplicate {
			t.Fatalf("catalog ID %s appears in both %s and %s matrices", catalogID, owner, command)
		}
		seenCatalogIDs[catalogID] = command

		exits := exitPattern.FindAllStringSubmatch(cells[resultIndex], -1)
		if len(exits) != 1 {
			t.Fatalf("%s row %s Result has %d literal exits, want exactly one", command, catalogID, len(exits))
		}
		code, err := strconv.Atoi(exits[0][1])
		if err != nil {
			t.Fatalf("%s row %s has invalid exit %q: %v", command, catalogID, exits[0][1], err)
		}
		if _, declared := globalCodes[code]; !declared {
			t.Fatalf("%s row %s uses undeclared global exit %d", command, catalogID, code)
		}
		if code == 7 {
			t.Fatalf("%s row %s places exit 7 in a precondition matrix", command, catalogID)
		}
		rowCount++
	}
	if rowCount == 0 {
		t.Fatalf("%s precondition matrix extracted no rows", command)
	}
}

func closedMarkdownCells(line string) ([]string, bool) {
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return nil, false
	}
	raw := strings.Split(line, "|")
	cells := make([]string, len(raw)-2)
	for index := range cells {
		cells[index] = strings.TrimSpace(raw[index+1])
	}
	return cells, true
}

func countString(values []string, target string) int {
	count := 0
	for _, value := range values {
		if value == target {
			count++
		}
	}
	return count
}

func stringIndex(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}
