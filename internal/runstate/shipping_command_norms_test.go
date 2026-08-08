package runstate

import (
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
	shipping := recoveryDocumentSection(t, lines,
		"## 8. Verification and shipping",
		"## 9. Amendments")
	applyCodes := shippingExitCodeTable(t, shipping,
		"**`partitur apply` exit mapping.**",
		"| Code | `partitur apply` outcome |")
	promotionCodes := shippingExitCodeTable(t, shipping,
		"**`partitur promote-score` exit mapping.**",
		"| Code | `partitur promote-score` outcome |")

	// When
	assertSameExitCodeSet(t, "apply", applyCodes, globalCodes)
	assertSameExitCodeSet(t, "promote-score", promotionCodes, globalCodes)

	// Then
	assertShippingOutcomeCode(t, applyCodes, "FAILED_CLEAN", 4)
	assertShippingOutcomeCode(t, applyCodes, "matching neither `base_tree` nor `result_tree`", 5)
	assertShippingOutcomeCode(t, promotionCodes, "CAS conflict", 2)
	assertShippingOutcomeCode(t, promotionCodes, "matching neither the expected nor target hash", 5)
}

func TestShippingExitMappingsStayInsideGlobalCategories(t *testing.T) {
	// Given
	lines := recoveryDesignLines(t)
	global := globalExitCodeDescriptions(t, lines)
	shipping := recoveryDocumentSection(t, lines,
		"## 8. Verification and shipping",
		"## 9. Amendments")
	apply := shippingExitCodeTable(t, shipping,
		"**`partitur apply` exit mapping.**",
		"| Code | `partitur apply` outcome |")
	promotion := shippingExitCodeTable(t, shipping,
		"**`partitur promote-score` exit mapping.**",
		"| Code | `partitur promote-score` outcome |")

	// When
	assertShippingOutcomeAdmittedByGlobal(t, global, apply, 4, "FAILED_CLEAN", "FAILED_CLEAN")
	assertShippingOutcomeAdmittedByGlobal(t, global, apply, 5,
		"Application `RECOVERY_REQUIRED`", "Application or Promotion remains `RECOVERY_REQUIRED`")
	assertShippingOutcomeAdmittedByGlobal(t, global, promotion, 2, "CAS conflict", "CAS conflict")
	assertShippingOutcomeAdmittedByGlobal(t, global, promotion, 5,
		"Promotion `RECOVERY_REQUIRED`", "Application or Promotion remains `RECOVERY_REQUIRED`")
	assertShippingOutcomeAdmittedByGlobal(t, global, apply, 6,
		"Application remains `APPLYING`", "Application `APPLYING`")
	assertShippingOutcomeAdmittedByGlobal(t, global, promotion, 6,
		"Promotion remains `PROMOTING`", "Promotion `PROMOTING`")

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
		header          string
		failure         string
		recovery        string
		interruption    string
		globalFailure   string
		globalRecovery  string
		globalInterrupt string
	}{
		{"run", "| Code | `partitur run` outcome |", "terminal `FAILED` or `CANCELLED`", "Appendix D", "run remains nonterminal", "a run reached terminal `FAILED` or `CANCELLED`", "a run halt names an Appendix D reason", "a run by `resume`"},
		{"resume", "| Code | `partitur resume` outcome |", "terminal `FAILED` or `CANCELLED`", "Appendix D", "operational-interruption outcome", "a run reached terminal `FAILED` or `CANCELLED`", "a run halt names an Appendix D reason", "a run by `resume`"},
		{"cancel", "| Code | `partitur cancel` outcome |", "terminal `FAILED` or `CANCELLED`", "Appendix D", "run remains nonterminal", "a run reached terminal `FAILED` or `CANCELLED`", "a run halt names an Appendix D reason", "a run by `resume`"},
	} {
		mapping := documentExitCodeTable(t, lines, command.name, command.header)
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

func documentExitCodeTable(t *testing.T, lines []string, name, header string) map[int]string {
	t.Helper()

	table := uniqueLineIndex(t, lines, header)
	return exitCodeTable(t, name+" exit mapping", lines[table:], header)
}

func shippingExitCodeTable(t *testing.T, section []string, title, header string) map[int]string {
	t.Helper()

	start := uniqueLinePrefixIndex(t, section, title)
	table := uniqueLineIndex(t, section, header)
	if table <= start {
		t.Fatalf("%s exit-code table must follow its mapping", title)
	}
	return exitCodeTable(t, title, section[table:], header)
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

func assertSameExitCodeSet(t *testing.T, command string, got map[int]string, want map[int]struct{}) {
	t.Helper()

	for code := range got {
		if _, declared := want[code]; !declared {
			t.Fatalf("%s exit mapping uses undeclared global code %d", command, code)
		}
	}
	for code := range want {
		if _, found := got[code]; !found {
			t.Fatalf("%s exit mapping omits declared global code %d", command, code)
		}
	}
}

func assertShippingOutcomeCode(t *testing.T, codes map[int]string, outcome string, want int) {
	t.Helper()

	found := -1
	count := 0
	for code, description := range codes {
		if strings.Contains(description, outcome) {
			found = code
			count++
		}
	}
	if count != 1 {
		t.Fatalf("shipping outcome %q appears in %d exit-code rows, want 1", outcome, count)
	}
	if found != want {
		t.Fatalf("shipping outcome %q maps to %d, want %d", outcome, found, want)
	}
}

func shippingCLIForms(t *testing.T, lines []string) map[string]map[string]bool {
	t.Helper()

	start := uniqueLineIndex(t, lines, "**CLI v0.2.**")
	end := uniqueLineIndex(t, lines, "### `partitur init` precondition matrix")
	if end <= start {
		t.Fatal("CLI command list must precede init precondition matrix")
	}

	forms := make(map[string]map[string]bool)
	for _, line := range commandCodeBlock(t, "CLI command list", lines[start+1:end]) {
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
