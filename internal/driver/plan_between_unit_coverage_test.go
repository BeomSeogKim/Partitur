package driver

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/recovery"
)

// This lock guarantees that every ActionKind PlanBetweenUnit can return is
// dispatched by the live driver, for the return shapes this derivation
// recognizes: a direct action(...) or Decision{...} construction, a resolvable
// same-package Decision helper, and a returned variable whose construction is
// unambiguous and whose Action.Kind is not written afterwards. Those four shapes
// fail closed rather than being guessed -- an unresolvable expression, a second
// possible construction, a post-construction Kind write, and an alias of the
// Decision or its Action all fail the test naming file, line and expression.
//
// It does NOT guarantee it against a selector written to defeat it. Replacing
// the whole field -- decision.Action = &Action{Kind: ...} -- is not recognized,
// and closing that led to a parser arms race: each of the six revisions of this
// lock was escaped one level down, and the revision that tried to forbid refusal
// bodies made production code rename its error paths to satisfy the parser,
// which is a worse failure than a written limit. Appendix E's own totality
// checker carries the same shape of caveat: it proves totality only relative to
// its declared axes.
//
// It also does not guarantee that a dispatched case does useful work: a case
// that dispatches to a refusal satisfies this lock.
// TestLiveLoopFailsRunDirectlyWhenBudgetExhaustsBetweenMovements and its
// charged-path sibling TestLiveChainTerminatesWhenBudgetExhaustsMidChain prove
// the behavioural effect. A source-copy mutation-kill harness -- replacing each
// dispatch body with a refusal and requiring those two to fail -- would close
// that gap without touching production code, and is recorded as follow-up work.
func TestPlanBetweenUnitActionKindsHaveLiveDriverDispatch(t *testing.T) {
	assertCoverageVacuityGuards(t)
	selected := planBetweenUnitActionKinds(t)
	if len(selected) == 0 {
		t.Fatal("derived PlanBetweenUnit action set is empty")
	}

	handled := liveSchedulerDispatchKinds(t, selected)
	if len(handled) == 0 {
		t.Fatal("found zero live driver dispatch handlers for derived PlanBetweenUnit action kinds")
	}

	missing := make([]string, 0)
	for kind := range selected {
		if !handled[kind] {
			missing = append(missing, string(kind))
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		t.Fatalf("PlanBetweenUnit action kinds missing live driver dispatch handlers: %v", missing)
	}
}

func assertCoverageVacuityGuards(t *testing.T) {
	t.Helper()
	_, path, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate live driver coverage test source")
	}
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		t.Fatalf("parse live driver coverage test source: %v", err)
	}
	found := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		statement, ok := node.(*ast.IfStmt)
		if !ok || !isEmptyGuard(statement.Cond) || !fatalGuardBody(statement.Body) {
			return true
		}
		found[guardedName(statement.Cond)] = true
		return true
	})
	for _, name := range []string{"selected", "handled"} {
		if !found[name] {
			t.Fatalf("coverage vacuity guard for %q is absent from %s", name, path)
		}
	}
}

func isEmptyGuard(expression ast.Expr) bool {
	comparison, ok := expression.(*ast.BinaryExpr)
	if !ok || comparison.Op != token.EQL {
		return false
	}
	call, ok := comparison.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	name, ok := call.Fun.(*ast.Ident)
	if !ok || name.Name != "len" {
		return false
	}
	identifier, ok := call.Args[0].(*ast.Ident)
	if !ok || (identifier.Name != "selected" && identifier.Name != "handled") {
		return false
	}
	literal, ok := comparison.Y.(*ast.BasicLit)
	return ok && literal.Kind == token.INT && literal.Value == "0"
}

func guardedName(expression ast.Expr) string {
	comparison := expression.(*ast.BinaryExpr)
	call := comparison.X.(*ast.CallExpr)
	return call.Args[0].(*ast.Ident).Name
}

func fatalGuardBody(body *ast.BlockStmt) bool {
	for _, statement := range body.List {
		expression, ok := statement.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := expression.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if ok && selector.Sel.Name == "Fatal" {
			return true
		}
	}
	return false
}

// planBetweenUnitActionKinds derives the action kinds returned by the
// PlanBetweenUnit selector, including any same-package Decision helper it
// calls. The AST is the selector's source authority; this intentionally has
// no hand-maintained ActionKind list.
func planBetweenUnitActionKinds(t *testing.T) map[recovery.ActionKind]bool {
	t.Helper()
	parsed := parseRecoveryPackage(t)

	selected := make(map[recovery.ActionKind]bool)
	visited := make(map[string]bool)
	var visit func(string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		function := parsed.functions[name]
		if function == nil {
			t.Fatalf("PlanBetweenUnit helper %q is absent", name)
		}
		for _, result := range decisionReturns(function) {
			resolveDecisionExpression(t, parsed, function, result, selected, visit)
		}
	}
	visit("PlanBetweenUnit")
	return selected
}

func liveSchedulerDispatchKinds(t *testing.T, selected map[recovery.ActionKind]bool) map[recovery.ActionKind]bool {
	t.Helper()
	_, testPath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate live driver dispatch coverage test source")
	}
	directory := filepath.Dir(testPath)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	var liveRunLoop *ast.FuncDecl
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, filepath.Join(directory, entry.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && function.Name.Name == "liveRunLoop" {
				liveRunLoop = function
			}
		}
	}
	if liveRunLoop == nil {
		t.Fatal("liveRunLoop is absent from internal/driver")
	}

	actionKinds := parsedActionKinds(t)
	handled := make(map[recovery.ActionKind]bool)
	foundDispatch := false
	ast.Inspect(liveRunLoop.Body, func(node ast.Node) bool {
		switchStatement, ok := node.(*ast.SwitchStmt)
		if !ok || !isDecisionActionKind(switchStatement.Tag) {
			return true
		}
		foundDispatch = true
		for _, statement := range switchStatement.Body.List {
			clause, ok := statement.(*ast.CaseClause)
			if !ok {
				t.Fatalf("live driver action dispatch clause is %T", statement)
			}
			for _, expression := range clause.List {
				selector, ok := expression.(*ast.SelectorExpr)
				if !ok {
					t.Fatalf("live driver action dispatch kind is %T, want recovery.ActionKind selector", expression)
				}
				packageName, ok := selector.X.(*ast.Ident)
				if !ok || packageName.Name != "recovery" {
					t.Fatalf("live driver action dispatch kind %q is not from recovery", selector.Sel.Name)
				}
				value, ok := actionKinds[selector.Sel.Name]
				if !ok {
					t.Fatalf("live driver action dispatch kind %q has no declared value", selector.Sel.Name)
				}
				kind := recovery.ActionKind(value)
				handled[kind] = true
			}
		}
		return false
	})
	if !foundDispatch {
		t.Fatal("liveRunLoop action-kind dispatch is absent")
	}
	return handled
}

func isDecisionActionKind(expression ast.Expr) bool {
	kind, ok := expression.(*ast.SelectorExpr)
	if !ok || kind.Sel.Name != "Kind" {
		return false
	}
	action, ok := kind.X.(*ast.SelectorExpr)
	if !ok || action.Sel.Name != "Action" {
		return false
	}
	decision, ok := action.X.(*ast.Ident)
	return ok && decision.Name == "decision"
}

type recoveryPackage struct {
	fileSet     *token.FileSet
	files       []*ast.File
	functions   map[string]*ast.FuncDecl
	actionKinds map[string]string
}

func parseRecoveryPackage(t *testing.T) recoveryPackage {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate PlanBetweenUnit coverage test source")
	}
	directory := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "recovery"))
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read internal/recovery: %v", err)
	}
	parsed := recoveryPackage{fileSet: token.NewFileSet(), functions: make(map[string]*ast.FuncDecl), actionKinds: make(map[string]string)}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		file, err := parser.ParseFile(parsed.fileSet, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		parsed.files = append(parsed.files, file)
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil || function.Name == nil {
				continue
			}
			if parsed.functions[function.Name.Name] != nil {
				t.Fatalf("duplicate internal/recovery function %q", function.Name.Name)
			}
			parsed.functions[function.Name.Name] = function
		}
	}
	parsed.actionKinds = declaredActionKinds(t, parsed.files)
	return parsed
}

func parsedActionKinds(t *testing.T) map[string]string {
	t.Helper()
	return parseRecoveryPackage(t).actionKinds
}

func declaredActionKinds(t *testing.T, files []*ast.File) map[string]string {
	t.Helper()
	values := make(map[string]string)
	for _, parsed := range files {
		for _, declaration := range parsed.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok || group.Tok != token.CONST {
				continue
			}
			for _, specification := range group.Specs {
				value, ok := specification.(*ast.ValueSpec)
				if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
					continue
				}
				declaredType, ok := value.Type.(*ast.Ident)
				if !ok || declaredType.Name != "ActionKind" {
					continue
				}
				literal, ok := value.Values[0].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("ActionKind %q has non-string declaration", value.Names[0].Name)
				}
				unquoted, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatalf("unquote ActionKind %q: %v", value.Names[0].Name, err)
				}
				values[value.Names[0].Name] = unquoted
			}
		}
	}
	return values
}

func decisionReturns(function *ast.FuncDecl) []ast.Expr {
	var results []ast.Expr
	ast.Inspect(function.Body, func(node ast.Node) bool {
		statement, ok := node.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		if len(statement.Results) != 1 {
			return true
		}
		results = append(results, statement.Results[0])
		return true
	})
	return results
}

func resolveDecisionExpression(t *testing.T, parsed recoveryPackage, function *ast.FuncDecl, expression ast.Expr, selected map[recovery.ActionKind]bool, visit func(string)) {
	t.Helper()
	switch value := expression.(type) {
	case *ast.CallExpr:
		callee, ok := value.Fun.(*ast.Ident)
		if !ok {
			unresolvableDecisionReturn(t, parsed, expression)
		}
		if callee.Name == "action" {
			if len(value.Args) < 2 {
				unresolvableDecisionReturn(t, parsed, expression)
			}
			kind, ok := value.Args[1].(*ast.Ident)
			if !ok {
				unresolvableDecisionReturn(t, parsed, expression)
			}
			kindValue, ok := parsed.actionKinds[kind.Name]
			if !ok {
				unresolvableDecisionReturn(t, parsed, expression)
			}
			selected[recovery.ActionKind(kindValue)] = true
			return
		}
		helper := parsed.functions[callee.Name]
		if helper == nil || !returnsDecision(helper) {
			unresolvableDecisionReturn(t, parsed, expression)
		}
		visit(callee.Name)
	case *ast.CompositeLit:
		resolveDecisionLiteral(t, parsed, value, selected)
	case *ast.Ident:
		construction, issue := decisionVariableConstruction(function, value.Name, expression.Pos())
		if issue != nil {
			rejectDecisionReturnShape(t, parsed, value.Name, issue)
		}
		if construction == nil {
			unresolvableDecisionReturn(t, parsed, expression)
		}
		resolveDecisionExpression(t, parsed, function, construction, selected, visit)
	default:
		unresolvableDecisionReturn(t, parsed, expression)
	}
}

func resolveDecisionLiteral(t *testing.T, parsed recoveryPackage, decision *ast.CompositeLit, selected map[recovery.ActionKind]bool) {
	t.Helper()
	decisionType, ok := decision.Type.(*ast.Ident)
	if !ok || decisionType.Name != "Decision" {
		unresolvableDecisionReturn(t, parsed, decision)
	}
	hasAction := false
	for _, element := range decision.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			unresolvableDecisionReturn(t, parsed, decision)
		}
		name, ok := field.Key.(*ast.Ident)
		if !ok || name.Name != "Action" {
			continue
		}
		hasAction = true
		action, ok := field.Value.(*ast.UnaryExpr)
		if !ok || action.Op != token.AND {
			unresolvableDecisionReturn(t, parsed, decision)
		}
		literal, ok := action.X.(*ast.CompositeLit)
		if !ok {
			unresolvableDecisionReturn(t, parsed, decision)
		}
		for _, actionElement := range literal.Elts {
			kindField, ok := actionElement.(*ast.KeyValueExpr)
			if !ok {
				unresolvableDecisionReturn(t, parsed, decision)
			}
			kindName, ok := kindField.Key.(*ast.Ident)
			if !ok || kindName.Name != "Kind" {
				continue
			}
			kind, ok := kindField.Value.(*ast.Ident)
			if !ok {
				unresolvableDecisionReturn(t, parsed, decision)
			}
			kindValue, ok := parsed.actionKinds[kind.Name]
			if !ok {
				unresolvableDecisionReturn(t, parsed, decision)
			}
			selected[recovery.ActionKind(kindValue)] = true
			return
		}
		unresolvableDecisionReturn(t, parsed, decision)
	}
	if !hasAction {
		return
	}
	unresolvableDecisionReturn(t, parsed, decision)
}

type decisionReturnShapeIssue struct {
	kind         string
	node         ast.Node
	construction ast.Expr
}

func decisionVariableConstruction(function *ast.FuncDecl, name string, returnedAt token.Pos) (ast.Expr, *decisionReturnShapeIssue) {
	scope := decisionReturnScope(function, returnedAt)
	if scope == nil {
		return nil, &decisionReturnShapeIssue{kind: "unresolvable", node: function}
	}
	var construction ast.Expr
	var issue *decisionReturnShapeIssue
	var constructionAssignment *ast.AssignStmt
	for _, statement := range decisionScopeStatements(scope) {
		assignment, ok := statement.(*ast.AssignStmt)
		if !ok || assignment.Pos() >= returnedAt {
			continue
		}
		for index, left := range assignment.Lhs {
			if identifier, ok := left.(*ast.Ident); ok && identifier.Name == name {
				if index >= len(assignment.Rhs) {
					issue = &decisionReturnShapeIssue{kind: "unresolvable", node: assignment}
					break
				}
				if construction != nil {
					issue = &decisionReturnShapeIssue{kind: "rebinding", node: assignment, construction: construction}
					break
				}
				construction = assignment.Rhs[index]
				constructionAssignment = assignment
				break
			}
		}
		if issue != nil {
			break
		}
	}
	if issue != nil || construction == nil {
		return construction, issue
	}
	ast.Inspect(scope, func(node ast.Node) bool {
		if issue != nil {
			return false
		}
		if _, ok := node.(*ast.FuncLit); ok {
			return false
		}
		assignment, ok := node.(*ast.AssignStmt)
		if ok && assignment != constructionAssignment && assignment.Pos() < returnedAt && assignment.Pos() > construction.Pos() {
			for index, left := range assignment.Lhs {
				if identifier, ok := left.(*ast.Ident); ok && identifier.Name == name {
					issue = &decisionReturnShapeIssue{kind: "rebinding", node: assignment, construction: construction}
					return false
				}
				if isActionKindWrite(left, name) {
					issue = &decisionReturnShapeIssue{kind: "write", node: assignment}
					return false
				}
				if index < len(assignment.Rhs) && isDecisionOrActionAlias(assignment.Rhs[index], name) {
					issue = &decisionReturnShapeIssue{kind: "alias", node: assignment}
					return false
				}
			}
		}
		value, ok := node.(*ast.ValueSpec)
		if !ok || value.Pos() >= returnedAt || value.Pos() <= construction.Pos() {
			return true
		}
		for _, expression := range value.Values {
			if isDecisionOrActionAlias(expression, name) {
				issue = &decisionReturnShapeIssue{kind: "alias", node: value}
				return false
			}
		}
		return true
	})
	return construction, issue
}

func decisionReturnScope(function *ast.FuncDecl, returnedAt token.Pos) ast.Node {
	var scope ast.Node
	ast.Inspect(function.Body, func(node ast.Node) bool {
		switch node.(type) {
		case *ast.BlockStmt, *ast.CaseClause, *ast.CommClause:
		default:
			return true
		}
		if node.Pos() > returnedAt || node.End() < returnedAt {
			return true
		}
		if scope == nil || node.Pos() >= scope.Pos() && node.End() <= scope.End() {
			scope = node
		}
		return true
	})
	return scope
}

func decisionScopeStatements(scope ast.Node) []ast.Stmt {
	switch value := scope.(type) {
	case *ast.BlockStmt:
		return value.List
	case *ast.CaseClause:
		return value.Body
	case *ast.CommClause:
		return value.Body
	default:
		return nil
	}
}

func isActionKindWrite(expression ast.Expr, name string) bool {
	kind, ok := expression.(*ast.SelectorExpr)
	if !ok || kind.Sel.Name != "Kind" {
		return false
	}
	action, ok := kind.X.(*ast.SelectorExpr)
	if !ok || action.Sel.Name != "Action" {
		return false
	}
	decision, ok := action.X.(*ast.Ident)
	return ok && decision.Name == name
}

func isDecisionOrActionAlias(expression ast.Expr, name string) bool {
	if decision, ok := expression.(*ast.Ident); ok {
		return decision.Name == name
	}
	action, ok := expression.(*ast.SelectorExpr)
	if !ok || action.Sel.Name != "Action" {
		return false
	}
	decision, ok := action.X.(*ast.Ident)
	return ok && decision.Name == name
}

func rejectDecisionReturnShape(t *testing.T, parsed recoveryPackage, name string, issue *decisionReturnShapeIssue) {
	t.Helper()
	switch issue.kind {
	case "write":
		postConstructionDecisionWriteReturn(t, parsed, name, issue.node)
	case "rebinding":
		ambiguousDecisionReturn(t, parsed, name, issue.construction, issue.node)
	case "alias":
		decisionReturnAlias(t, parsed, name, issue.node)
	default:
		unresolvableDecisionReturn(t, parsed, issue.node)
	}
}

func postConstructionDecisionWriteReturn(t *testing.T, parsed recoveryPackage, name string, node ast.Node) {
	t.Helper()
	var rendered bytes.Buffer
	if err := format.Node(&rendered, parsed.fileSet, node); err != nil {
		rendered.WriteString("<unprintable>")
	}
	position := parsed.fileSet.Position(node.Pos())
	t.Fatalf("Decision return variable %q has a post-construction Action.Kind write at %s:%d: %s", name, position.Filename, position.Line, rendered.String())
}

func ambiguousDecisionReturn(t *testing.T, parsed recoveryPackage, name string, construction ast.Expr, rebinding ast.Node) {
	t.Helper()
	var first, second bytes.Buffer
	if err := format.Node(&first, parsed.fileSet, construction); err != nil {
		first.WriteString("<unprintable>")
	}
	if err := format.Node(&second, parsed.fileSet, rebinding); err != nil {
		second.WriteString("<unprintable>")
	}
	firstPosition := parsed.fileSet.Position(construction.Pos())
	secondPosition := parsed.fileSet.Position(rebinding.Pos())
	t.Fatalf("Decision return variable %q has ambiguous constructions: first at %s:%d: %s; conflicting line at %s:%d: %s", name, firstPosition.Filename, firstPosition.Line, first.String(), secondPosition.Filename, secondPosition.Line, second.String())
}

func decisionReturnAlias(t *testing.T, parsed recoveryPackage, name string, node ast.Node) {
	t.Helper()
	var rendered bytes.Buffer
	if err := format.Node(&rendered, parsed.fileSet, node); err != nil {
		rendered.WriteString("<unprintable>")
	}
	position := parsed.fileSet.Position(node.Pos())
	t.Fatalf("Decision return variable %q takes a Decision or Action alias at %s:%d: %s", name, position.Filename, position.Line, rendered.String())
}

func unresolvableDecisionReturn(t *testing.T, parsed recoveryPackage, node ast.Node) {
	t.Helper()
	var rendered bytes.Buffer
	if err := format.Node(&rendered, parsed.fileSet, node); err != nil {
		rendered.WriteString("<unprintable>")
	}
	position := parsed.fileSet.Position(node.Pos())
	t.Fatalf("unresolvable Decision return path at %s:%d: %s", position.Filename, position.Line, rendered.String())
}

func returnsDecision(function *ast.FuncDecl) bool {
	if function.Type.Results == nil || len(function.Type.Results.List) != 1 {
		return false
	}
	result, ok := function.Type.Results.List[0].Type.(*ast.Ident)
	return ok && result.Name == "Decision"
}
