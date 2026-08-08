package runstate

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type recoveryAxis struct {
	name   string
	values []string
}

type recoveryActionRow struct {
	id         string
	precedence int
	caseID     string
	guards     map[string][]string
	result     string
}

func TestAppendixCRecoverySelectionIsTotal(t *testing.T) {
	lines := recoveryDesignLines(t)
	axes := recoverySelectionAxes(t, lines)
	states := reachableRecoverySelectionCuts(t, lines, axes)
	actions := recoveryActionRows(t, lines, axes, declaredRecoveryCases(t, lines))
	validateRecoveryCasePartition(t, lines, actions)

	wins := make(map[string]int, len(actions))
	for stateKey, state := range states {
		var matching []recoveryActionRow
		for _, action := range actions {
			if recoveryActionMatches(action, state) {
				matching = append(matching, action)
			}
		}
		if len(matching) == 0 {
			t.Fatalf("reachable recovery cut %s is uncovered: %s", stateKey, formatRecoveryState(axes, state))
		}
		best := matching[0].precedence
		for _, action := range matching[1:] {
			if action.precedence < best {
				best = action.precedence
			}
		}
		var winners []recoveryActionRow
		for _, action := range matching {
			if action.precedence == best {
				winners = append(winners, action)
			}
		}
		if len(winners) != 1 {
			var ids []string
			for _, winner := range winners {
				ids = append(ids, fmt.Sprintf("%s/%s", winner.id, winner.caseID))
			}
			sort.Strings(ids)
			t.Fatalf("reachable recovery cut %s is ambiguous at precedence %d between %s: %s",
				stateKey, best, strings.Join(ids, ", "), formatRecoveryState(axes, state))
		}
		wins[winners[0].id]++
	}

	for _, action := range actions {
		if wins[action.id] == 0 {
			t.Fatalf("recovery action row %s (%s) is dead: it wins no reachable cut", action.id, action.caseID)
		}
	}
	t.Logf("recovery selection expansion: %d axes, %d reachable cuts, %d live action rows",
		len(axes), len(states), len(actions))
}

func TestDraftNoBlockingOutputRecoveryPrecedesOrdinaryVerification(t *testing.T) {
	lines := recoveryDesignLines(t)
	c2 := recoveryDocumentSection(t, lines,
		"## C.2 Attempt lifecycle recovery",
		"## C.3 Acceptance recovery")
	contents := strings.Join(c2, "\n")

	const caseID = "RC-RESUME-050"
	const row = "| `RC-RESUME-050` | Current-head `performer.completed` on the draft interview movement while the score remains `status: draft` | Append `attempt.failed {kind: task_failed, reason: draft_no_blocking_output, disposition}` after classifying the quality failure under §3.1's first arm, then re-evaluate C.1 and hand its recorded second-arm consequence to `RC-RESUME-039`. **Acceptance never begins.** A genuinely blocking draft result instead makes `attempt.blocked` durable, not `performer.completed`, so this row is selected from the journal alone and does not reconstruct a lost response |"
	if count := strings.Count(contents, row); count != 1 {
		t.Fatalf("C.4.1 action row RA-061 is orphaned: %s must appear exactly once in C.2, got %d", caseID, count)
	}
	for _, after := range []string{"| `RC-RESUME-016` |", "| `RC-RESUME-017` |"} {
		if index := strings.Index(contents, after); index == -1 || strings.Index(contents, row) >= index {
			t.Fatalf("%s must precede %s in C.2", caseID, after)
		}
	}

	axes := recoverySelectionAxes(t, lines)
	states := reachableRecoverySelectionCuts(t, lines, axes)
	actions := recoveryActionRows(t, lines, axes, declaredRecoveryCases(t, lines))
	var special map[string]string
	for _, state := range states {
		if state["cut"] == "RS-039" {
			special = state
			break
		}
	}
	if special == nil {
		t.Fatal("C.4.1 is missing the draft no-blocking-output selection cut RS-039")
	}
	var winners []recoveryActionRow
	for _, action := range actions {
		if recoveryActionMatches(action, special) {
			winners = append(winners, action)
		}
	}
	if len(winners) != 1 || winners[0].id != "RA-061" || winners[0].caseID != caseID || winners[0].precedence != 70 {
		t.Fatalf("RS-039 winner = %+v, want RA-061/%s at precedence 70", winners, caseID)
	}
}

func recoverySelectionSection(t *testing.T, lines []string) []string {
	t.Helper()
	return recoveryDocumentSection(t, lines,
		"### C.4.1 Finite selection model",
		"# Appendix D — Closed enums")
}

func recoverySelectionAxes(t *testing.T, lines []string) []recoveryAxis {
	t.Helper()
	section := recoverySelectionSection(t, lines)
	const header = "| Axis | Values | Why it discriminates |"
	const separator = "|---|---|---|"
	codeValue := regexp.MustCompile("`([a-z][a-z0-9_-]*)`")

	headerIndex := uniqueLineIndex(t, section, header)
	if headerIndex+1 >= len(section) || section[headerIndex+1] != separator {
		t.Fatalf("recovery axis table has missing or malformed separator after %q", header)
	}
	var axes []recoveryAxis
	seen := map[string]bool{}
	for index := headerIndex + 2; index < len(section) && strings.HasPrefix(section[index], "|"); index++ {
		cells := markdownTableCells(t, "recovery axis table", section[index], 3)
		nameMatch := codeValue.FindStringSubmatch(cells[0])
		if nameMatch == nil || nameMatch[0] != cells[0] {
			t.Fatalf("unparseable recovery axis name %q", cells[0])
		}
		name := nameMatch[1]
		if seen[name] {
			t.Fatalf("duplicate recovery axis %q", name)
		}
		seen[name] = true
		var values []string
		for _, match := range codeValue.FindAllStringSubmatch(cells[1], -1) {
			values = append(values, match[1])
		}
		if len(values) == 0 {
			t.Fatalf("recovery axis %q declares no values", name)
		}
		uniqueStrings(t, "recovery axis "+name, values)
		if cells[2] == "" {
			t.Fatalf("recovery axis %q has no discrimination rationale", name)
		}
		axes = append(axes, recoveryAxis{name: name, values: values})
	}
	if len(axes) == 0 {
		t.Fatal("recovery axis extraction produced no axes")
	}
	return axes
}

func reachableRecoverySelectionCuts(t *testing.T, lines []string, axes []recoveryAxis) map[string]map[string]string {
	t.Helper()
	section := recoverySelectionSection(t, lines)
	header := recoveryStateTableHeader("Cut", axes)
	separator := recoveryTableSeparator(len(axes) + 1)
	cutCell := regexp.MustCompile("^`(RS-[0-9]{3})`$")

	headerIndex := uniqueLineIndex(t, section, header)
	if headerIndex+1 >= len(section) || section[headerIndex+1] != separator {
		t.Fatalf("reachable recovery selection cuts table has missing or malformed separator after %q", header)
	}
	axisValues := recoveryAxisValueSets(axes)
	states := map[string]map[string]string{}
	usedValues := map[string]map[string]bool{}
	seenCuts := map[string]bool{}
	for _, axis := range axes {
		usedValues[axis.name] = map[string]bool{}
	}
	for index := headerIndex + 2; index < len(section) && strings.HasPrefix(section[index], "|"); index++ {
		cells := markdownTableCells(t, "reachable recovery selection cuts", section[index], len(axes)+1)
		match := cutCell.FindStringSubmatch(cells[0])
		if match == nil {
			t.Fatalf("unparseable reachable recovery cut id %q", cells[0])
		}
		if seenCuts[match[1]] {
			t.Fatalf("duplicate reachable recovery cut id %q", match[1])
		}
		seenCuts[match[1]] = true
		alternatives := make([][]string, len(axes))
		for axisIndex, axis := range axes {
			alternatives[axisIndex] = recoveryCellValues(t, "reachable cut "+match[1], axis, cells[axisIndex+1], false, axisValues)
		}
		expandRecoveryState(t, axes, alternatives, 0, map[string]string{}, func(state map[string]string) {
			key := recoveryStateKey(axes, state)
			if prior, exists := states[key]; exists {
				t.Fatalf("duplicate reachable recovery combination %s from %s and %s",
					formatRecoveryState(axes, state), prior["cut"], match[1])
			}
			copyState := make(map[string]string, len(state)+1)
			for name, value := range state {
				copyState[name] = value
				usedValues[name][value] = true
			}
			copyState["cut"] = match[1]
			states[key] = copyState
		})
	}
	if len(states) == 0 {
		t.Fatal("reachable recovery selection cuts extraction produced no combinations")
	}
	for _, axis := range axes {
		for _, value := range axis.values {
			if !usedValues[axis.name][value] {
				t.Fatalf("recovery axis %q value %q has no reachable combination", axis.name, value)
			}
		}
	}
	return states
}

func recoveryActionRows(t *testing.T, lines []string, axes []recoveryAxis, declaredCases map[string]bool) []recoveryActionRow {
	t.Helper()
	section := recoverySelectionSection(t, lines)
	header := recoveryActionTableHeader(axes)
	separator := recoveryTableSeparator(len(axes) + 4)
	idCell := regexp.MustCompile("^`(RA-[0-9]{3})`$")
	caseCell := regexp.MustCompile("^`(RC-[A-Z]+-[0-9]{3})`$")

	headerIndex := uniqueLineIndex(t, section, header)
	if headerIndex+1 >= len(section) || section[headerIndex+1] != separator {
		t.Fatalf("recovery action selection expansion has missing or malformed separator after %q", header)
	}
	axisValues := recoveryAxisValueSets(axes)
	seen := map[string]bool{}
	var rows []recoveryActionRow
	for index := headerIndex + 2; index < len(section) && strings.HasPrefix(section[index], "|"); index++ {
		cells := markdownTableCells(t, "recovery action selection expansion", section[index], len(axes)+4)
		idMatch := idCell.FindStringSubmatch(cells[0])
		if idMatch == nil {
			t.Fatalf("unparseable recovery action row id %q", cells[0])
		}
		if seen[idMatch[1]] {
			t.Fatalf("duplicate recovery action row id %q", idMatch[1])
		}
		seen[idMatch[1]] = true
		precedence, err := strconv.Atoi(cells[1])
		if err != nil || precedence <= 0 {
			t.Fatalf("recovery action row %s has invalid precedence %q", idMatch[1], cells[1])
		}
		caseMatch := caseCell.FindStringSubmatch(cells[2])
		if caseMatch == nil || !declaredCases[caseMatch[1]] {
			t.Fatalf("recovery action row %s references undeclared case %q", idMatch[1], cells[2])
		}
		guards := map[string][]string{}
		for axisIndex, axis := range axes {
			guards[axis.name] = recoveryCellValues(t, "action row "+idMatch[1], axis, cells[axisIndex+3], true, axisValues)
		}
		result := cells[len(cells)-1]
		if result == "" {
			t.Fatalf("recovery action row %s has an empty selected result", idMatch[1])
		}
		rows = append(rows, recoveryActionRow{
			id: idMatch[1], precedence: precedence, caseID: caseMatch[1], guards: guards, result: result,
		})
	}
	if len(rows) == 0 {
		t.Fatal("recovery action selection expansion produced no action rows")
	}
	return rows
}

func validateRecoveryCasePartition(t *testing.T, lines []string, actions []recoveryActionRow) {
	t.Helper()
	appendix := recoveryDocumentSection(t, lines,
		"# Appendix C — Recovery",
		"# Appendix D — Closed enums")
	caseRow := regexp.MustCompile("^\\| `(RC-[A-Z]+-[0-9]{3})` \\|")
	caseLabel := regexp.MustCompile("^\\*\\*Recovery case:\\*\\* `(RC-[A-Z]+-[0-9]{3})`\\.$")
	declared := map[string]bool{}
	for _, line := range appendix {
		var match []string
		if strings.HasPrefix(line, "| `RC-") {
			match = caseRow.FindStringSubmatch(line)
		} else if strings.HasPrefix(line, "**Recovery case:**") {
			match = caseLabel.FindStringSubmatch(line)
		}
		if match != nil {
			declared[match[1]] = true
		}
	}

	section := recoverySelectionSection(t, lines)
	const header = "| Non-action case | Reason |"
	const separator = "|---|---|"
	caseCell := regexp.MustCompile("^(RC-[A-Z]+-[0-9]{3})$")
	headerIndex := uniqueLineIndex(t, section, header)
	if headerIndex+1 >= len(section) || section[headerIndex+1] != separator {
		t.Fatalf("recovery non-action case table has missing or malformed separator after %q", header)
	}
	nonActions := map[string]bool{}
	for index := headerIndex + 2; index < len(section) && strings.HasPrefix(section[index], "|"); index++ {
		cells := markdownTableCells(t, "recovery non-action case table", section[index], 2)
		match := caseCell.FindStringSubmatch(cells[0])
		if match == nil || !declared[match[1]] {
			t.Fatalf("recovery non-action table references undeclared case %q", cells[0])
		}
		if nonActions[match[1]] || cells[1] == "" {
			t.Fatalf("duplicate or unexplained recovery non-action case %q", match[1])
		}
		nonActions[match[1]] = true
	}
	if len(nonActions) == 0 {
		t.Fatal("recovery non-action case extraction produced no cases")
	}

	actionCases := map[string]bool{}
	for _, action := range actions {
		actionCases[action.caseID] = true
	}
	for caseID := range declared {
		if actionCases[caseID] == nonActions[caseID] {
			t.Fatalf("Appendix C case %s must appear in exactly one of the action or non-action sets", caseID)
		}
	}
	for caseID := range actionCases {
		if !declared[caseID] {
			t.Fatalf("recovery action case %s is not declared in Appendix C", caseID)
		}
	}
}

func recoveryCellValues(t *testing.T, source string, axis recoveryAxis, cell string, allowWildcard bool, axisValues map[string]map[string]bool) []string {
	t.Helper()
	if allowWildcard && cell == "*" {
		return append([]string(nil), axis.values...)
	}
	if cell == "" || strings.Contains(cell, "*") {
		t.Fatalf("%s has malformed %s guard %q", source, axis.name, cell)
	}
	values := strings.Split(cell, ",")
	seen := map[string]bool{}
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
		if !axisValues[axis.name][values[index]] {
			t.Fatalf("%s uses unknown %s value %q", source, axis.name, values[index])
		}
		if seen[values[index]] {
			t.Fatalf("%s repeats %s value %q", source, axis.name, values[index])
		}
		seen[values[index]] = true
	}
	return values
}

func recoveryAxisValueSets(axes []recoveryAxis) map[string]map[string]bool {
	sets := make(map[string]map[string]bool, len(axes))
	for _, axis := range axes {
		sets[axis.name] = make(map[string]bool, len(axis.values))
		for _, value := range axis.values {
			sets[axis.name][value] = true
		}
	}
	return sets
}

func expandRecoveryState(t *testing.T, axes []recoveryAxis, alternatives [][]string, index int, state map[string]string, visit func(map[string]string)) {
	t.Helper()
	if index == len(axes) {
		visit(state)
		return
	}
	for _, value := range alternatives[index] {
		state[axes[index].name] = value
		expandRecoveryState(t, axes, alternatives, index+1, state, visit)
	}
	delete(state, axes[index].name)
}

func recoveryActionMatches(action recoveryActionRow, state map[string]string) bool {
	for axis, allowed := range action.guards {
		match := false
		for _, value := range allowed {
			match = match || state[axis] == value
		}
		if !match {
			return false
		}
	}
	return true
}

func recoveryStateTableHeader(first string, axes []recoveryAxis) string {
	cells := []string{first}
	for _, axis := range axes {
		cells = append(cells, axis.name)
	}
	return "| " + strings.Join(cells, " | ") + " |"
}

func recoveryActionTableHeader(axes []recoveryAxis) string {
	cells := []string{"Action row", "Precedence", "Recovery case"}
	for _, axis := range axes {
		cells = append(cells, axis.name)
	}
	cells = append(cells, "Selected result")
	return "| " + strings.Join(cells, " | ") + " |"
}

func recoveryTableSeparator(cells int) string {
	return "|" + strings.Repeat("---|", cells)
}

func recoveryStateKey(axes []recoveryAxis, state map[string]string) string {
	values := make([]string, len(axes))
	for index, axis := range axes {
		values[index] = state[axis.name]
	}
	return strings.Join(values, "\x00")
}

func formatRecoveryState(axes []recoveryAxis, state map[string]string) string {
	values := make([]string, len(axes))
	for index, axis := range axes {
		values[index] = axis.name + "=" + state[axis.name]
	}
	return strings.Join(values, ", ")
}
