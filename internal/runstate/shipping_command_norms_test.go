package runstate

import (
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
