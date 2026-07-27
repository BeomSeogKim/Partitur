package runstate

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type recoveryCoverageEntry struct {
	kind           string
	name           string
	classification string
	cases          []string
	coverage       string
}

func TestAppendixCRecoveryCoverageRegistryIsComplete(t *testing.T) {
	lines := recoveryDesignLines(t)
	events := appendixBEventTypes(t, lines)
	dispositions := successorDispositionArms(t, lines)
	entries := recoveryCoverageRegistry(t, lines)
	declaredCases := declaredRecoveryCases(t, lines)
	openCases := openRecoveryCases(t, lines)

	registryEvents := make([]string, 0, len(events))
	registryDispositions := make([]string, 0, len(dispositions))
	for _, entry := range entries {
		switch entry.kind {
		case "event":
			registryEvents = append(registryEvents, entry.name)
		case "disposition":
			registryDispositions = append(registryDispositions, entry.name)
		default:
			t.Fatalf("recovery coverage entry %q has unknown kind %q", entry.name, entry.kind)
		}
		validateRecoveryCoverageEntry(t, entry, declaredCases, openCases)
	}

	requireSameUniqueStrings(t, "Appendix B events", events, "recovery registry events", registryEvents)
	requireSameUniqueStrings(t, "§3.1 dispositions", dispositions, "recovery registry dispositions", registryDispositions)
	t.Logf("recovery coverage registry: %d Appendix B events, %d §3.1 dispositions, %d declared cases, %d open cases",
		len(events), len(dispositions), len(declaredCases), len(openCases))
}

func recoveryDesignLines(t *testing.T) []string {
	t.Helper()

	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(string(contents), "\n")
}

func appendixBEventTypes(t *testing.T, lines []string) []string {
	t.Helper()

	section := recoveryDocumentSection(t, lines,
		"# Appendix B — Journal event registry",
		"# Appendix C — Recovery")
	const header = "| Type | sync | idem key | Legal from | Projection effect |"
	const separator = "|---|---|---|---|---|"
	typeCell := regexp.MustCompile("^`([a-z][a-z0-9_.]*)`(?: \\*derived\\*)?$")

	var events []string
	tableCount := 0
	for index := 0; index < len(section); index++ {
		if section[index] != header {
			continue
		}
		tableCount++
		if index+1 >= len(section) || section[index+1] != separator {
			t.Fatalf("Appendix B event table has missing or malformed separator after %q", header)
		}
		rowCount := 0
		for index += 2; index < len(section) && strings.HasPrefix(section[index], "|"); index++ {
			cells := markdownTableCells(t, "Appendix B event table", section[index], 5)
			match := typeCell.FindStringSubmatch(cells[0])
			if match == nil {
				t.Fatalf("unparseable Appendix B event row %q", section[index])
			}
			events = append(events, match[1])
			rowCount++
		}
		if rowCount == 0 {
			t.Fatal("Appendix B event table contains no event rows")
		}
		index--
	}
	if tableCount == 0 || len(events) == 0 {
		t.Fatalf("Appendix B extraction produced %d tables and %d events", tableCount, len(events))
	}
	return events
}

func successorDispositionArms(t *testing.T, lines []string) []string {
	t.Helper()

	section := recoveryDocumentSection(t, lines,
		"### 3.1 The successor oracle",
		"## 4. Adapter protocol v2")
	const header = "| Recovery case | `disposition.charged` | Action |"
	const separator = "|---|---|---|"
	dispositionCell := regexp.MustCompile("^`([a-z][a-z0-9_]*)`$")

	headerIndex := uniqueLineIndex(t, section, header)
	if headerIndex+1 >= len(section) || section[headerIndex+1] != separator {
		t.Fatalf("§3.1 disposition table has missing or malformed separator after %q", header)
	}
	var dispositions []string
	for index := headerIndex + 2; index < len(section) && strings.HasPrefix(section[index], "|"); index++ {
		cells := markdownTableCells(t, "§3.1 disposition table", section[index], 3)
		match := dispositionCell.FindStringSubmatch(cells[1])
		if match == nil {
			t.Fatalf("unparseable §3.1 disposition row %q", section[index])
		}
		dispositions = append(dispositions, match[1])
	}
	if len(dispositions) == 0 {
		t.Fatal("§3.1 disposition extraction produced no arms")
	}
	return dispositions
}

func recoveryCoverageRegistry(t *testing.T, lines []string) []recoveryCoverageEntry {
	t.Helper()

	section := recoveryDocumentSection(t, lines,
		"## C.0 Recovery surfaces and coverage registry",
		"## C.1 Run-level precedence")
	const header = "| Kind | Entry | Classification | Cases | Coverage |"
	const separator = "|---|---|---|---|---|"
	entryCell := regexp.MustCompile("^`([a-z][a-z0-9_.]*)`$")
	caseReference := regexp.MustCompile("`(RC-[A-Z]+-[0-9]{3})`")

	headerIndex := uniqueLineIndex(t, section, header)
	if headerIndex+1 >= len(section) || section[headerIndex+1] != separator {
		t.Fatalf("recovery coverage registry has missing or malformed separator after %q", header)
	}
	var entries []recoveryCoverageEntry
	for index := headerIndex + 2; index < len(section) && strings.HasPrefix(section[index], "|"); index++ {
		cells := markdownTableCells(t, "recovery coverage registry", section[index], 5)
		match := entryCell.FindStringSubmatch(cells[1])
		if match == nil {
			t.Fatalf("unparseable recovery coverage registry entry %q", section[index])
		}
		var cases []string
		for _, caseMatch := range caseReference.FindAllStringSubmatch(cells[3], -1) {
			cases = append(cases, caseMatch[1])
		}
		if cells[3] != "—" && len(cases) == 0 {
			t.Fatalf("recovery coverage entry %q has malformed cases cell %q", match[1], cells[3])
		}
		entries = append(entries, recoveryCoverageEntry{
			kind:           cells[0],
			name:           match[1],
			classification: cells[2],
			cases:          cases,
			coverage:       cells[4],
		})
	}
	if len(entries) == 0 {
		t.Fatal("recovery coverage registry extraction produced no entries")
	}
	return entries
}

func declaredRecoveryCases(t *testing.T, lines []string) map[string]bool {
	t.Helper()

	caseRow := regexp.MustCompile("^\\| `(RC-[A-Z]+-[0-9]{3})` \\|")
	casePrefix := regexp.MustCompile("^\\| `RC-")
	caseLabel := regexp.MustCompile("^\\*\\*Recovery case:\\*\\* `(RC-[A-Z]+-[0-9]{3})`\\.$")
	seen := map[string]bool{}
	for _, line := range lines {
		var match []string
		switch {
		case casePrefix.MatchString(line):
			match = caseRow.FindStringSubmatch(line)
		case strings.HasPrefix(line, "**Recovery case:**"):
			match = caseLabel.FindStringSubmatch(line)
		default:
			continue
		}
		if match == nil {
			t.Fatalf("malformed recovery case declaration %q", line)
		}
		if seen[match[1]] {
			t.Fatalf("duplicate recovery case declaration %q", match[1])
		}
		seen[match[1]] = true
	}
	if len(seen) == 0 {
		t.Fatal("DESIGN contains no recovery case declarations")
	}
	return seen
}

func openRecoveryCases(t *testing.T, lines []string) map[string]bool {
	t.Helper()

	section := recoveryDocumentSection(t, lines,
		"## C.0 Recovery surfaces and coverage registry",
		"## C.1 Run-level precedence")
	const header = "| Recovery case | Planned closure | Open gap |"
	const separator = "|---|---|---|"
	caseCell := regexp.MustCompile("^`(RC-[A-Z]+-[0-9]{3})`$")

	headerIndex := uniqueLineIndex(t, section, header)
	if headerIndex+1 >= len(section) || section[headerIndex+1] != separator {
		t.Fatalf("open recovery case table has missing or malformed separator after %q", header)
	}
	open := map[string]bool{}
	for index := headerIndex + 2; index < len(section) && strings.HasPrefix(section[index], "|"); index++ {
		cells := markdownTableCells(t, "open recovery case table", section[index], 3)
		match := caseCell.FindStringSubmatch(cells[0])
		if match == nil {
			t.Fatalf("unparseable open recovery case row %q", section[index])
		}
		if open[match[1]] {
			t.Fatalf("duplicate open recovery case %q", match[1])
		}
		if cells[1] == "" || cells[2] == "" {
			t.Fatalf("open recovery case %q must name its planned closure and gap", match[1])
		}
		open[match[1]] = true
	}
	if len(open) == 0 {
		t.Fatal("open recovery case extraction produced no cases")
	}
	return open
}

func validateRecoveryCoverageEntry(t *testing.T, entry recoveryCoverageEntry, declaredCases, openCases map[string]bool) {
	t.Helper()

	switch entry.classification {
	case "direct", "structural", "neutral", "separate-surface":
	default:
		t.Fatalf("recovery coverage entry %q has unknown classification %q", entry.name, entry.classification)
	}
	switch entry.coverage {
	case "covered", "open":
	default:
		t.Fatalf("recovery coverage entry %q has unknown coverage %q", entry.name, entry.coverage)
	}

	if entry.classification == "neutral" {
		if len(entry.cases) != 0 || entry.coverage != "covered" {
			t.Fatalf("neutral recovery coverage entry %q must be covered with no cases", entry.name)
		}
		return
	}
	if len(entry.cases) == 0 {
		t.Fatalf("non-neutral recovery coverage entry %q has no recovery cases", entry.name)
	}

	hasOpenCase := false
	seenCases := map[string]bool{}
	for _, caseID := range entry.cases {
		if seenCases[caseID] {
			t.Fatalf("recovery coverage entry %q repeats case %q", entry.name, caseID)
		}
		seenCases[caseID] = true
		if !declaredCases[caseID] {
			t.Fatalf("recovery coverage entry %q references undeclared case %q", entry.name, caseID)
		}
		hasOpenCase = hasOpenCase || openCases[caseID]
		if entry.classification == "separate-surface" &&
			!strings.HasPrefix(caseID, "RC-APPLY-") &&
			!strings.HasPrefix(caseID, "RC-PROMOTE-") {
			t.Fatalf("separate-surface entry %q references resume case %q", entry.name, caseID)
		}
	}
	if entry.coverage == "open" && !hasOpenCase {
		t.Fatalf("open recovery coverage entry %q names no open recovery case", entry.name)
	}
	if entry.coverage == "covered" && hasOpenCase {
		t.Fatalf("covered recovery coverage entry %q references an open recovery case", entry.name)
	}
}

func recoveryDocumentSection(t *testing.T, lines []string, heading, nextHeading string) []string {
	t.Helper()

	start := uniqueLineIndex(t, lines, heading)
	end := uniqueLineIndex(t, lines, nextHeading)
	if end <= start {
		t.Fatalf("heading %q must follow %q", nextHeading, heading)
	}
	return lines[start+1 : end]
}

func uniqueLineIndex(t *testing.T, lines []string, target string) int {
	t.Helper()

	index := -1
	count := 0
	for lineIndex, line := range lines {
		if line == target {
			index = lineIndex
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%q line count = %d, want 1", target, count)
	}
	return index
}

func markdownTableCells(t *testing.T, table, line string, want int) []string {
	t.Helper()

	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		t.Fatalf("%s row is not a closed Markdown table row: %q", table, line)
	}
	raw := strings.Split(line, "|")
	if len(raw) != want+2 {
		t.Fatalf("%s row has %d cells, want %d: %q", table, len(raw)-2, want, line)
	}
	cells := make([]string, want)
	for index := range cells {
		cells[index] = strings.TrimSpace(raw[index+1])
	}
	return cells
}

func requireSameUniqueStrings(t *testing.T, leftName string, left []string, rightName string, right []string) {
	t.Helper()

	leftSet := uniqueStrings(t, leftName, left)
	rightSet := uniqueStrings(t, rightName, right)
	var onlyLeft []string
	for value := range leftSet {
		if !rightSet[value] {
			onlyLeft = append(onlyLeft, value)
		}
	}
	var onlyRight []string
	for value := range rightSet {
		if !leftSet[value] {
			onlyRight = append(onlyRight, value)
		}
	}
	sort.Strings(onlyLeft)
	sort.Strings(onlyRight)
	if len(onlyLeft) != 0 || len(onlyRight) != 0 {
		t.Fatalf("%s and %s differ: only in %s: %q; only in %s: %q",
			leftName, rightName, leftName, onlyLeft, rightName, onlyRight)
	}
}

func uniqueStrings(t *testing.T, source string, values []string) map[string]bool {
	t.Helper()

	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" {
			t.Fatalf("%s contains an empty value", source)
		}
		if seen[value] {
			t.Fatalf("%s contains duplicate value %q", source, value)
		}
		seen[value] = true
	}
	return seen
}
