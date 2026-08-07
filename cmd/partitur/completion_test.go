package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestCompletionCommandDispatchClaim(t *testing.T) {
	members := completionCommandMembers(t)
	dispatched := dispatchedCommands(t)
	for command := range dispatched {
		if !members[command] {
			t.Fatalf("dispatched command %q is absent from the COMPLETION command table", command)
		}
	}

	completion, err := os.ReadFile(filepath.Join("..", "..", "docs", "COMPLETION.md"))
	if err != nil {
		t.Fatal(err)
	}
	assertCountClaim(t, string(completion),
		regexp.MustCompile(`Currently red: ([a-z-]+) of the ([a-z-]+) are dispatched\.`),
		len(dispatched), len(members),
	)
}

func TestCommandStoreConstructionInstallsReceiptObserver(t *testing.T) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var stores int
	var unwired []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || !isRunstoreCall(call, "New") {
			return true
		}
		stores++
		for _, argument := range call.Args {
			if isRunstoreCall(argument, "ReceiptObserverFromEnvironment") {
				return true
			}
		}
		unwired = append(unwired, fileSet.Position(call.Pos()).String())
		return true
	})
	if stores == 0 {
		t.Fatal("main.go constructs no command stores")
	}
	if len(unwired) != 0 {
		t.Fatalf("command store construction lacks ReceiptObserverFromEnvironment at %s", strings.Join(unwired, ", "))
	}
}

func isRunstoreCall(expression ast.Expr, name string) bool {
	call, ok := expression.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	return ok && packageName.Name == "runstore"
}

func completionCommandMembers(t *testing.T) map[string]bool {
	t.Helper()
	completion, err := os.ReadFile(filepath.Join("..", "..", "docs", "COMPLETION.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(completion), "\n")
	start, end := tableBoundsCounted(t, lines, "## 1. Commands", "## 2. Events", 1, 1)
	commandPattern := regexp.MustCompile("^`([a-z][a-z-]*)`$")
	members := make(map[string]bool)
	for _, line := range lines[start:end] {
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "| Command |") || strings.HasPrefix(line, "|---") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) != 3 {
			t.Fatalf("unparseable COMPLETION command row %q", line)
		}
		match := commandPattern.FindStringSubmatch(strings.TrimSpace(cells[1]))
		if match == nil || members[match[1]] {
			t.Fatalf("unparseable or duplicate COMPLETION command row %q", line)
		}
		members[match[1]] = true
	}
	if len(members) == 0 {
		t.Fatal("COMPLETION command extraction produced no rows")
	}
	return members
}

func dispatchedCommands(t *testing.T) map[string]bool {
	t.Helper()
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	functions := make(map[string]*ast.FuncDecl)
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Recv == nil {
			functions[function.Name.Name] = function
		}
	}
	runWithReaders := functions["runWithReaders"]
	if runWithReaders == nil {
		t.Fatal("main.go does not declare runWithReaders")
	}

	dispatched := make(map[string]bool)
	for _, statement := range runWithReaders.Body.List {
		conditional, ok := statement.(*ast.IfStmt)
		if !ok {
			continue
		}
		command, found := commandSelectedBy(t, functions, conditional)
		if !found {
			continue
		}
		if dispatched[command] {
			t.Fatalf("main.go dispatches command %q more than once", command)
		}
		dispatched[command] = true
	}
	if len(dispatched) == 0 {
		t.Fatal("main.go dispatch extraction produced no commands")
	}
	return dispatched
}

func commandSelectedBy(t *testing.T, functions map[string]*ast.FuncDecl, conditional *ast.IfStmt) (string, bool) {
	t.Helper()
	if command, found := commandFromExpression(t, conditional.Cond); found {
		return command, true
	}
	parserName := parserCalledBy(t, conditional.Init)
	if parserName == "" {
		return "", false
	}
	parser := functions[parserName]
	if parser == nil {
		t.Fatalf("main.go dispatch calls missing parser %q", parserName)
	}
	command, found := commandFromFunction(t, parser)
	if !found {
		t.Fatalf("main.go dispatch parser %q has no command selector", parserName)
	}
	return command, true
}

func parserCalledBy(t *testing.T, statement ast.Stmt) string {
	t.Helper()
	assignment, ok := statement.(*ast.AssignStmt)
	if !ok {
		return ""
	}
	var names []string
	for _, expression := range assignment.Rhs {
		call, ok := expression.(*ast.CallExpr)
		if !ok {
			continue
		}
		function, ok := call.Fun.(*ast.Ident)
		if !ok || !strings.HasPrefix(function.Name, "parse") || !strings.HasSuffix(function.Name, "Args") {
			continue
		}
		names = append(names, function.Name)
	}
	if len(names) == 0 {
		return ""
	}
	if len(names) != 1 {
		t.Fatalf("main.go dispatch condition calls parsers %q", names)
	}
	return names[0]
}

func commandFromFunction(t *testing.T, function *ast.FuncDecl) (string, bool) {
	t.Helper()
	commands := make(map[string]bool)
	ast.Inspect(function.Body, func(node ast.Node) bool {
		expression, ok := node.(ast.Expr)
		if !ok {
			return true
		}
		if command, found := commandFromExpression(t, expression); found {
			commands[command] = true
		}
		return true
	})
	if len(commands) == 0 {
		return "", false
	}
	if len(commands) != 1 {
		t.Fatalf("parser %q selects commands %v", function.Name.Name, sortedCommands(commands))
	}
	for command := range commands {
		return command, true
	}
	return "", false
}

func commandFromExpression(t *testing.T, expression ast.Expr) (string, bool) {
	t.Helper()
	commands := make(map[string]bool)
	ast.Inspect(expression, func(node ast.Node) bool {
		comparison, ok := node.(*ast.BinaryExpr)
		if !ok || (comparison.Op != token.EQL && comparison.Op != token.NEQ) {
			return true
		}
		if command, ok := comparisonCommand(comparison.X, comparison.Y); ok {
			commands[command] = true
			return true
		}
		if command, ok := comparisonCommand(comparison.Y, comparison.X); ok {
			commands[command] = true
		}
		return true
	})
	if len(commands) == 0 {
		return "", false
	}
	if len(commands) != 1 {
		t.Fatalf("command selector names commands %v", sortedCommands(commands))
	}
	for command := range commands {
		return command, true
	}
	return "", false
}

func comparisonCommand(index, value ast.Expr) (string, bool) {
	indexed, ok := index.(*ast.IndexExpr)
	if !ok {
		return "", false
	}
	args, ok := indexed.X.(*ast.Ident)
	if !ok || args.Name != "args" {
		return "", false
	}
	position, ok := indexed.Index.(*ast.BasicLit)
	if !ok || position.Kind != token.INT || position.Value != "0" {
		return "", false
	}
	command, ok := value.(*ast.BasicLit)
	if !ok || command.Kind != token.STRING {
		return "", false
	}
	return strings.Trim(command.Value, "\""), true
}

func sortedCommands(commands map[string]bool) []string {
	result := make([]string, 0, len(commands))
	for command := range commands {
		result = append(result, command)
	}
	sort.Strings(result)
	return result
}
