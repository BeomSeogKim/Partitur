package runstate

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestShippingCommandSurfaceIsSpecified(t *testing.T) {
	// Given
	lines := recoveryDesignLines(t)
	cliForms := shippingCLIForms(t, lines)
	operandForms := shippingOperandForms(t, lines)

	// When
	assertSameCommandIDs(t, cliForms, operandForms)

	// Then
	for _, command := range []string{"apply", "promote-score"} {
		forms := cliForms[command]
		if len(forms) != 2 || !forms["normal"] || !forms["recover"] {
			t.Fatalf("CLI forms for %s = %#v, want normal and --recover", command, forms)
		}
		if operandForms[command] != "partitur "+command+" <run-id> [--recover]" {
			t.Fatalf("operand form for %s = %q, want mandatory <run-id>", command, operandForms[command])
		}
	}

	for command, forms := range cliForms {
		if command == "apply" || command == "promote-score" {
			continue
		}
		if len(forms) != 1 || !forms["normal"] {
			t.Fatalf("CLI forms for %s = %#v, want one normal form", command, forms)
		}
	}
}

func TestShippingExitMappingsAreSpecified(t *testing.T) {
	// Given
	lines := recoveryDesignLines(t)
	globalCodes := globalExitCodeSet(t, lines)
	applicability := exit7ApplicabilityByCommand(t, lines)
	commands := completionCommandIDs(t)
	accounting := commandExitAccounting()
	accountedCommands := make([]string, 0, len(accounting))
	for command := range accounting {
		accountedCommands = append(accountedCommands, command)
	}
	requireSameUniqueStrings(t,
		"command exit-code accounting", accountedCommands,
		"COMPLETION section 1 commands", commands,
	)

	// When
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			codes := commandMatrixExitCodeDescriptions(t, lines, command, globalCodes)
			grammarOwned := []int(nil)
			if codes[1] == "" {
				grammarOwned = []int{1}
			}
			entry := accounting[command]
			if entry.observationProse {
				assertObservationAccounting(t, lines, command, codes, entry.unused, applicability[command])
			}
			assertShippingMatrixExitCodePartition(t, command, codes, grammarOwned, entry.unused, globalCodes)
		})
	}
}

type commandExitAccountingEntry struct {
	unused           []int
	observationProse bool
}

func commandExitAccounting() map[string]commandExitAccountingEntry {
	return map[string]commandExitAccountingEntry{
		"init":          {unused: []int{3, 4, 5, 6}},
		"validate":      {unused: []int{4, 5, 6}},
		"run":           {},
		"status":        {unused: []int{3, 4, 6}, observationProse: true},
		"logs":          {unused: []int{3, 4, 6}, observationProse: true},
		"answer":        {unused: []int{3, 4}},
		"approve":       {unused: []int{4}},
		"amend":         {unused: []int{4}},
		"cancel":        {unused: []int{3}},
		"resume":        {unused: []int{3}},
		"apply":         {unused: []int{3}},
		"promote-score": {unused: []int{3, 4}},
		"version":       {unused: []int{2, 3, 4, 5, 6}},
	}
}

func exit7ApplicabilityByCommand(t *testing.T, lines []string) map[string]bool {
	t.Helper()

	result := make(map[string]bool)
	for _, row := range exit7ApplicabilityRows(t, lines) {
		if _, duplicate := result[row.command]; duplicate {
			t.Fatalf("exit-7 applicability registry has duplicate command %s", row.command)
		}
		result[row.command] = row.applicable
	}
	return result
}

func commandMatrixExitCodeDescriptions(t *testing.T, lines []string, command string, globalCodes map[int]struct{}) map[int]string {
	t.Helper()

	descriptions := make(map[int]string)
	for _, row := range commandMatrixRows(t, lines, command, globalCodes) {
		if descriptions[row.exit] != "" {
			descriptions[row.exit] += "\n"
		}
		descriptions[row.exit] += row.catalogID + ": " + row.description
	}
	return descriptions
}

func assertShippingMatrixExitCodePartition(t *testing.T, command string, matrix map[int]string, grammarOwned, unused []int, global map[int]struct{}) {
	t.Helper()

	accounted := make(map[int]string)
	add := func(code int, owner string) {
		if existing := accounted[code]; existing != "" {
			t.Fatalf("%s exit code %d is owned by both %s and %s", command, code, existing, owner)
		}
		accounted[code] = owner
	}
	for code := range matrix {
		add(code, "precondition matrix")
	}
	for _, code := range grammarOwned {
		add(code, "command-matrix grammar")
	}
	for _, code := range unused {
		add(code, "exhaustive matrix absence")
	}
	add(7, "exit-7 applicability registry")

	got := make(map[int]struct{}, len(accounted))
	for code := range accounted {
		got[code] = struct{}{}
	}
	if !sameIntSet(got, global) {
		t.Fatalf("%s matrix/grammar/absence/registry partition = %v, want global exit-code set %v", command, sortedIntSet(got), sortedIntSet(global))
	}
}

func assertObservationAccounting(t *testing.T, lines []string, command string, matrix map[int]string, unused []int, exit7Applicable bool) {
	t.Helper()

	enumerated, negated := observationExitCodeSets(t, lines, command)
	matrixCodes := make(map[int]struct{}, len(matrix))
	for code := range matrix {
		matrixCodes[code] = struct{}{}
	}
	if !sameIntSet(enumerated, matrixCodes) {
		t.Fatalf("%s prose-enumerated exit codes = %v, want matrix result codes %v", command, sortedIntSet(enumerated), sortedIntSet(matrixCodes))
	}
	wantNegated := make(map[int]struct{}, len(unused)+1)
	for _, code := range unused {
		wantNegated[code] = struct{}{}
	}
	wantNegated[7] = struct{}{}
	if !sameIntSet(negated, wantNegated) {
		t.Fatalf("%s prose-negated exit codes = %v, want exhaustive absences plus exit 7 %v", command, sortedIntSet(negated), sortedIntSet(wantNegated))
	}
	if exit7Applicable {
		t.Fatalf("%s prose says exit 7 is unused but the applicability registry says Applicable", command)
	}
}

func observationExitCodeSets(t *testing.T, lines []string, command string) (map[int]struct{}, map[int]struct{}) {
	t.Helper()

	prefix := "The " + command + "-specific exit mapping is "
	start := uniqueLinePrefixIndex(t, lines, prefix)
	end := start
	for end < len(lines) && lines[end] != "" {
		end++
	}
	paragraph := strings.Join(lines[start:end], " ")

	integerLiteral := regexp.MustCompile(`[0-9]+`)
	enumeratedClause := regexp.MustCompile(`(?:^|[:;])\s*(?:and )?([0-9]+)\s+(?:for|only when|when)\b`)
	negatedClause, err := regexp.Compile("`" + regexp.QuoteMeta(command) + "` never returns ((?:[0-9]+, )*[0-9]+(?:,? or [0-9]+)?):")
	if err != nil {
		t.Fatal(err)
	}

	attributions := make(map[int]string)
	for _, match := range enumeratedClause.FindAllStringSubmatchIndex(paragraph, -1) {
		attributions[match[2]] = "enumerated"
	}
	if match := negatedClause.FindStringSubmatchIndex(paragraph); match != nil {
		listStart := match[2]
		for _, literal := range integerLiteral.FindAllStringIndex(paragraph[match[2]:match[3]], -1) {
			attributions[listStart+literal[0]] = "negated"
		}
	}

	enumerated := make(map[int]struct{})
	negated := make(map[int]struct{})
	for _, literal := range integerLiteral.FindAllStringIndex(paragraph, -1) {
		category, attributed := attributions[literal[0]]
		if !attributed {
			t.Fatalf("%s exit mapping has unattributed integer literal %s in %q", command, paragraph[literal[0]:literal[1]], paragraph)
		}
		code, err := strconv.Atoi(paragraph[literal[0]:literal[1]])
		if err != nil {
			t.Fatalf("%s exit mapping has unparseable %s code %q: %v", command, category, paragraph[literal[0]:literal[1]], err)
		}
		if category == "enumerated" {
			enumerated[code] = struct{}{}
		} else {
			negated[code] = struct{}{}
		}
	}
	return enumerated, negated
}

func sameIntSet(left, right map[int]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if _, found := right[value]; !found {
			return false
		}
	}
	return true
}

func sortedIntSet(set map[int]struct{}) []int {
	values := make([]int, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Ints(values)
	return values
}

func TestShippingMatrixOutcomesStayInsideGlobalCategories(t *testing.T) {
	// Given
	lines := recoveryDesignLines(t)
	global := globalExitCodeDescriptions(t, lines)
	globalCodes := globalExitCodeSet(t, lines)
	apply := commandMatrixExitCodeDescriptions(t, lines, "apply", globalCodes)
	promotion := commandMatrixExitCodeDescriptions(t, lines, "promote-score", globalCodes)

	// When
	assertShippingOutcomeAdmittedByGlobal(t, global, apply, 4, "verified clean rollback", "FAILED_CLEAN")
	assertShippingOutcomeAdmittedByGlobal(t, global, apply, 5,
		"retain Application `RECOVERY_REQUIRED`", "Application or Promotion remains `RECOVERY_REQUIRED`")
	assertShippingOutcomeAdmittedByGlobal(t, global, promotion, 2,
		"pre-start root-hash conflict", "promotion pre-start root-hash conflict")
	assertShippingOutcomeAdmittedByGlobal(t, global, promotion, 5,
		"retain Promotion `RECOVERY_REQUIRED`", "Application or Promotion remains `RECOVERY_REQUIRED`")
	assertShippingOutcomeAdmittedByGlobal(t, global, apply, 6,
		"`partitur apply <run-id> --recover`", "Application `APPLYING`")
	assertShippingOutcomeAdmittedByGlobal(t, global, promotion, 6,
		"`partitur promote-score <run-id> --recover`", "Promotion `PROMOTING`")

	// Then
	assertExistingRunCommandNarrowings(t, lines, global)
}

func assertShippingOutcomeAdmittedByGlobal(t *testing.T, global, command map[int]string, code int, commandOutcome, globalAdmission string) {
	t.Helper()

	if !strings.Contains(command[code], commandOutcome) {
		t.Fatalf("shipping exit-code %d does not name outcome %q", code, commandOutcome)
	}
	if !strings.Contains(global[code], globalAdmission) {
		t.Fatalf("global exit-code %d excludes shipping outcome %q", code, commandOutcome)
	}
}

func assertExistingRunCommandNarrowings(t *testing.T, lines []string, global map[int]string) {
	t.Helper()

	for _, command := range []struct {
		name            string
		failure         string
		recovery        string
		interruption    string
		globalFailure   string
		globalRecovery  string
		globalInterrupt string
	}{
		{"run", "authoritative terminal event", "selected halt reason", "`partitur resume <run-id>`", "a run reached terminal `FAILED` or `CANCELLED`", "a run halt names an Appendix D reason", "a run by `resume`"},
		{"resume", "required residual cleanup", "selected halt reason", "later `resume`", "a run reached terminal `FAILED` or `CANCELLED`", "a run halt names an Appendix D reason", "a run by `resume`"},
		{"cancel", "authoritative terminal event", "selected halt reason", "`partitur resume <run-id>`", "a run reached terminal `FAILED` or `CANCELLED`", "a run halt names an Appendix D reason", "a run by `resume`"},
	} {
		mapping := commandMatrixExitCodeDescriptions(t, lines, command.name, globalExitCodeSet(t, lines))
		for code, outcomes := range map[int]struct{ command, global string }{
			4: {command.failure, command.globalFailure},
			5: {command.recovery, command.globalRecovery},
			6: {command.interruption, command.globalInterrupt},
		} {
			if !strings.Contains(mapping[code], outcomes.command) {
				t.Fatalf("%s exit-code %d no longer narrows the run outcome %q", command.name, code, outcomes.command)
			}
			if !strings.Contains(global[code], outcomes.global) {
				t.Fatalf("global exit-code %d excludes the run narrowing %q", code, outcomes.global)
			}
		}
	}
}

func globalExitCodeSet(t *testing.T, lines []string) map[int]struct{} {
	t.Helper()

	descriptions := globalExitCodeDescriptions(t, lines)
	codes := make(map[int]struct{}, len(descriptions))
	for code := range descriptions {
		codes[code] = struct{}{}
	}
	return codes
}

func globalExitCodeDescriptions(t *testing.T, lines []string) map[int]string {
	t.Helper()

	start := uniqueLineIndex(t, lines, "**Exit codes** — stable categories, so a script can branch without parsing prose:")
	end := uniqueLineIndex(t, lines, "## 8. Verification and shipping")
	if end <= start {
		t.Fatal("global exit-code table must precede §8")
	}
	section := lines[start+1 : end]
	table := uniqueLineIndex(t, section, "| Code | Meaning |")
	return exitCodeTable(t, "global exit-code table", section[table:], "| Code | Meaning |")
}

func exitCodeTable(t *testing.T, name string, lines []string, header string) map[int]string {
	t.Helper()

	if len(lines) < 3 || lines[0] != header || lines[1] != "|---|---|" {
		t.Fatalf("%s has missing or malformed exit-code table header", name)
	}

	codes := make(map[int]string)
	for _, line := range lines[2:] {
		if !strings.HasPrefix(line, "|") {
			break
		}
		cells := markdownTableCells(t, name, line, 2)
		code, err := strconv.Atoi(cells[0])
		if err != nil {
			t.Fatalf("%s has unparseable exit code %q: %v", name, cells[0], err)
		}
		if _, duplicate := codes[code]; duplicate {
			t.Fatalf("%s has duplicate exit code %d", name, code)
		}
		codes[code] = cells[1]
	}
	if len(codes) == 0 {
		t.Fatalf("%s extracted no exit-code rows", name)
	}
	return codes
}

func shippingCLIForms(t *testing.T, lines []string) map[string]map[string]bool {
	t.Helper()

	start := uniqueLineIndex(t, lines, "**CLI v0.2.**")
	end := uniqueLineIndex(t, lines, "### `partitur init` precondition matrix")
	if end <= start {
		t.Fatal("CLI command list must precede init precondition matrix")
	}

	forms := make(map[string]map[string]bool)
	for _, line := range commandProseForms(t, "CLI command list", lines[start+1:end]) {
		fields := commandFields(t, "CLI command list", line)
		command := fields[1]
		variant := "normal"
		if len(fields) > 2 && fields[2] == "--recover" {
			variant = "recover"
		}
		if forms[command] == nil {
			forms[command] = make(map[string]bool)
		}
		if forms[command][variant] {
			t.Fatalf("CLI command list has duplicate %s form for %s", variant, command)
		}
		forms[command][variant] = true
	}
	if len(forms) == 0 {
		t.Fatal("CLI command list extracted no command IDs")
	}
	return forms
}

func commandProseForms(t *testing.T, name string, lines []string) []string {
	t.Helper()

	var commands []string
	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			t.Fatalf("%s still uses a fenced representation", name)
		}
		if !strings.HasPrefix(line, "- `partitur ") {
			continue
		}
		form := strings.TrimPrefix(line, "- `")
		end := strings.Index(form, "`")
		if end < 0 {
			t.Fatalf("%s has unterminated command form %q", name, line)
		}
		if strings.TrimSpace(form[end+1:]) == "" {
			t.Fatalf("%s command form has no prose carrier %q", name, line)
		}
		commands = append(commands, form[:end])
	}
	if len(commands) == 0 {
		t.Fatalf("%s has no prose command forms", name)
	}
	return commands
}

func shippingOperandForms(t *testing.T, lines []string) map[string]string {
	t.Helper()

	section := approvalSyntaxSection(t, lines)
	forms := make(map[string]string)
	for _, line := range commandCodeBlock(t, "operands and options", section) {
		fields := commandFields(t, "operands and options", line)
		command := fields[1]
		if _, duplicate := forms[command]; duplicate {
			t.Fatalf("operands and options has duplicate command ID %s", command)
		}
		forms[command] = strings.Join(fields, " ")
	}
	if len(forms) == 0 {
		t.Fatal("operands and options extracted no command IDs")
	}
	return forms
}

func commandCodeBlock(t *testing.T, name string, lines []string) []string {
	t.Helper()

	start := -1
	for index, line := range lines {
		if line != "```text" {
			continue
		}
		if start >= 0 {
			t.Fatalf("%s has multiple command code blocks", name)
		}
		start = index
	}
	if start < 0 {
		t.Fatalf("%s has no command code block", name)
	}
	for end := start + 1; end < len(lines); end++ {
		if lines[end] == "```" {
			var commands []string
			for _, line := range lines[start+1 : end] {
				if strings.TrimSpace(line) == "" || line != strings.TrimLeft(line, " \t") {
					continue
				}
				if !strings.HasPrefix(line, "partitur") {
					t.Fatalf("%s has unparseable command line %q", name, line)
				}
				commands = append(commands, line)
			}
			return commands
		}
	}
	t.Fatalf("%s command code block is unterminated", name)
	return nil
}

func commandFields(t *testing.T, name, line string) []string {
	t.Helper()

	if !strings.HasPrefix(line, "partitur") {
		t.Fatalf("%s has unparseable command line %q", name, line)
	}
	syntax, _, _ := strings.Cut(line, "#")
	fields := strings.Fields(syntax)
	if len(fields) < 2 || fields[0] != "partitur" || !validCommandID(fields[1]) {
		t.Fatalf("%s has unparseable command line %q", name, line)
	}
	return fields
}

func validCommandID(command string) bool {
	for _, character := range command {
		if character != '-' && (character < 'a' || character > 'z') {
			return false
		}
	}
	return command != ""
}

func assertSameCommandIDs(t *testing.T, cliForms map[string]map[string]bool, operandForms map[string]string) {
	t.Helper()

	for command := range cliForms {
		if _, found := operandForms[command]; !found {
			t.Fatalf("operands and options is missing CLI command ID %s", command)
		}
	}
	for command := range operandForms {
		if _, found := cliForms[command]; !found {
			t.Fatalf("CLI command list is missing operands command ID %s", command)
		}
	}
}
