package recoveryexec

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/recoveryconsequence"
)

type plannerStepRoute struct {
	caseID recovery.CaseID
	kind   recovery.ActionKind
	step   recovery.ActionStep
}

func (route plannerStepRoute) String() string {
	return fmt.Sprintf("%s %s %s", route.caseID, route.kind, route.step)
}

// TestPlannerStepRoutesAreExecutable derives step-dispatched routes from the
// planner's action constructors and then exercises the executor's real route.
// The derivation recognizes direct action construction, helpers whose CaseID
// and ActionKind parameters flow into action, and subsequent Steps assignment.
// It fails closed on unresolved arguments and on every Steps assignment or
// keyed Action composite-literal Steps field that was not resolved into at
// least one route. Positional Action literals and steps introduced only by
// copying or returning a prebuilt whole Action remain outside this syntax lock.
func TestPlannerStepRoutesAreExecutable(t *testing.T) {
	routes := plannerStepRoutes(t)
	if len(routes) == 0 {
		t.Fatal("derived planner step route set is empty")
	}
	t.Logf("derived %d planner step routes", len(routes))
	executor := &Executor{}
	missing := make([]string, 0)
	for _, route := range routes {
		handler, ok := executor.stepHandler(route.caseID, route.kind, route.step)
		if !ok || handler == nil {
			missing = append(missing, route.String()+" (no handler)")
			continue
		}
		err := handler(context.Background(), HandlerContext{}, recovery.Action{Kind: route.kind})
		if errors.Is(err, recoveryconsequence.ErrUnrecognizedCase) || errors.Is(err, recoveryconsequence.ErrInvalidAction) {
			missing = append(missing, route.String()+" ("+err.Error()+")")
		}
	}
	if len(missing) != 0 {
		t.Fatalf("planner step routes are not executable:\n  %s", joinLines(missing))
	}
}

func joinLines(lines []string) string {
	result := ""
	for index, line := range lines {
		if index != 0 {
			result += "\n  "
		}
		result += line
	}
	return result
}

type routeTemplate struct {
	caseParam  int
	kindParam  int
	stepsParam int
	kind       recovery.ActionKind
	steps      []recovery.ActionStep
	stepSites  []token.Pos
}

func plannerStepRoutes(t *testing.T) []plannerStepRoute {
	t.Helper()
	path := filepath.Join("..", "recovery", "planner.go")
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	caseValues := typedStringConstants(t, file, "CaseID")
	kindValues := typedStringConstants(t, file, "ActionKind")
	stepValues := typedStringConstants(t, file, "ActionStep")
	functions := map[string]*ast.FuncDecl{}
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			functions[function.Name.Name] = function
		}
	}
	templates := map[string]routeTemplate{}
	for name, function := range functions {
		if template, ok := plannerRouteTemplate(t, function, kindValues, stepValues); ok {
			templates[name] = template
		}
	}

	set := map[plannerStepRoute]bool{}
	resolvedStepSites := map[token.Pos]int{}
	for _, function := range functions {
		locals := localCaseValues(function, caseValues)
		directRoutes, directSites := directPlannerStepRoutes(t, function, caseValues, kindValues, stepValues, locals)
		for _, route := range directRoutes {
			set[route] = true
		}
		for site, count := range directSites {
			resolvedStepSites[site] += count
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			callee, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			template, ok := templates[callee.Name]
			if !ok {
				return true
			}
			cases := resolveCases(t, call.Args[template.caseParam], caseValues, locals)
			kind := template.kind
			if template.kindParam >= 0 {
				kind = resolveKind(t, call.Args[template.kindParam], kindValues)
			}
			resolvedSteps := template.steps
			if template.stepsParam >= 0 {
				resolvedSteps = resolveSteps(t, call.Args[template.stepsParam], stepValues)
			}
			for _, caseID := range cases {
				for _, step := range resolvedSteps {
					set[plannerStepRoute{caseID: caseID, kind: kind, step: step}] = true
				}
			}
			for _, site := range template.stepSites {
				resolvedStepSites[site] += len(cases) * len(resolvedSteps)
			}
			return true
		})
	}
	for _, function := range functions {
		ast.Inspect(function.Body, func(node ast.Node) bool {
			literal, ok := node.(*ast.CompositeLit)
			if ok && isActionCompositeLiteral(literal) {
				for _, element := range literal.Elts {
					field, keyed := element.(*ast.KeyValueExpr)
					if !keyed {
						continue
					}
					name, named := field.Key.(*ast.Ident)
					if named && name.Name == "Steps" && resolvedStepSites[field.Pos()] == 0 {
						position := fileSet.Position(field.Pos())
						t.Fatalf("planner Action composite literal Steps field at %s was not resolved into a route", position)
					}
				}
			}
			assignment, ok := node.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, left := range assignment.Lhs {
				if _, isSteps := stepsAssignmentRoot(left); isSteps && resolvedStepSites[left.Pos()] == 0 {
					position := fileSet.Position(left.Pos())
					t.Fatalf("planner Steps assignment at %s was not resolved into a route", position)
				}
			}
			return true
		})
	}
	routes := make([]plannerStepRoute, 0, len(set))
	for route := range set {
		routes = append(routes, route)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].String() < routes[j].String() })
	return routes
}

func isActionCompositeLiteral(literal *ast.CompositeLit) bool {
	switch actionType := literal.Type.(type) {
	case *ast.Ident:
		return actionType.Name == "Action"
	case *ast.SelectorExpr:
		return actionType.Sel.Name == "Action"
	default:
		return false
	}
}

func plannerRouteTemplate(t *testing.T, function *ast.FuncDecl, kinds, steps map[string]string) (routeTemplate, bool) {
	t.Helper()
	params := map[string]int{}
	index := 0
	if function.Type.Params != nil {
		for _, field := range function.Type.Params.List {
			for _, name := range field.Names {
				params[name.Name] = index
				index++
			}
		}
	}
	template := routeTemplate{caseParam: -1, kindParam: -1, stepsParam: -1}
	foundAction := false
	actionRoots := map[string]bool{}
	ast.Inspect(function.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok {
			callee, direct := call.Fun.(*ast.Ident)
			if direct && callee.Name == "action" {
				if len(call.Args) < 2 {
					t.Fatalf("%s action call has %d arguments", function.Name.Name, len(call.Args))
				}
				caseName, ok := call.Args[0].(*ast.Ident)
				if !ok {
					t.Fatalf("%s action case is %T, want identifier", function.Name.Name, call.Args[0])
				}
				caseParam, caseIsParam := params[caseName.Name]
				if !caseIsParam {
					return true
				}
				template.caseParam = caseParam
				kindName, ok := call.Args[1].(*ast.Ident)
				if !ok {
					t.Fatalf("%s action kind is %T, want identifier", function.Name.Name, call.Args[1])
				}
				if kindParam, ok := params[kindName.Name]; ok {
					template.kindParam = kindParam
				} else if value, ok := kinds[kindName.Name]; ok {
					template.kind = recovery.ActionKind(value)
				} else {
					t.Fatalf("%s action kind %s is unresolved", function.Name.Name, kindName.Name)
				}
				foundAction = true
			}
		}
		assignment, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for index, right := range assignment.Rhs {
			call, ok := right.(*ast.CallExpr)
			if !ok || index >= len(assignment.Lhs) {
				continue
			}
			callee, ok := call.Fun.(*ast.Ident)
			root, rootOK := assignment.Lhs[index].(*ast.Ident)
			if ok && callee.Name == "action" && rootOK {
				actionRoots[root.Name] = true
			}
		}
		for rightIndex, left := range assignment.Lhs {
			root, isSteps := stepsAssignmentRoot(left)
			if !isSteps || !actionRoots[root] || rightIndex >= len(assignment.Rhs) {
				continue
			}
			template.stepSites = append(template.stepSites, left.Pos())
			if name, ok := assignment.Rhs[rightIndex].(*ast.Ident); ok {
				if parameter, ok := params[name.Name]; ok {
					template.stepsParam = parameter
					continue
				}
			}
			template.steps = append(template.steps, resolveSteps(t, assignment.Rhs[rightIndex], steps)...)
		}
		return true
	})
	return template, foundAction && template.caseParam >= 0 && (len(template.steps) != 0 || template.stepsParam >= 0)
}

func directPlannerStepRoutes(t *testing.T, function *ast.FuncDecl, cases, kinds, steps map[string]string, locals map[string][]recovery.CaseID) ([]plannerStepRoute, map[token.Pos]int) {
	t.Helper()
	routes := []plannerStepRoute{}
	resolvedSites := map[token.Pos]int{}
	var scanBlock func(*ast.BlockStmt, map[string]struct {
		cases []recovery.CaseID
		kind  recovery.ActionKind
	})
	clone := func(input map[string]struct {
		cases []recovery.CaseID
		kind  recovery.ActionKind
	}) map[string]struct {
		cases []recovery.CaseID
		kind  recovery.ActionKind
	} {
		output := make(map[string]struct {
			cases []recovery.CaseID
			kind  recovery.ActionKind
		}, len(input))
		for name, value := range input {
			output[name] = value
		}
		return output
	}
	scanBlock = func(block *ast.BlockStmt, actions map[string]struct {
		cases []recovery.CaseID
		kind  recovery.ActionKind
	}) {
		if block == nil {
			return
		}
		for _, statement := range block.List {
			switch statement := statement.(type) {
			case *ast.AssignStmt:
				for index, right := range statement.Rhs {
					call, ok := right.(*ast.CallExpr)
					callee, direct := callFunction(call)
					if ok && direct == "action" && len(call.Args) >= 2 && index < len(statement.Lhs) {
						caseName, identifier := call.Args[0].(*ast.Ident)
						if identifier {
							_, constant := cases[caseName.Name]
							_, local := locals[caseName.Name]
							if !constant && !local {
								continue
							}
						}
						name, ok := statement.Lhs[index].(*ast.Ident)
						if !ok {
							t.Fatalf("%s direct action target is %T, want identifier", function.Name.Name, statement.Lhs[index])
						}
						actions[name.Name] = struct {
							cases []recovery.CaseID
							kind  recovery.ActionKind
						}{resolveCases(t, call.Args[0], cases, locals), resolveKind(t, call.Args[1], kinds)}
						_ = callee
					}
				}
				for index, left := range statement.Lhs {
					root, isSteps := stepsAssignmentRoot(left)
					if !isSteps || index >= len(statement.Rhs) {
						continue
					}
					action, ok := actions[root]
					if !ok {
						// Parameterized helpers are derived as templates at their call sites.
						continue
					}
					for _, caseID := range action.cases {
						for _, step := range resolveSteps(t, statement.Rhs[index], steps) {
							routes = append(routes, plannerStepRoute{caseID: caseID, kind: action.kind, step: step})
							resolvedSites[left.Pos()]++
						}
					}
				}
			case *ast.IfStmt:
				scanBlock(statement.Body, clone(actions))
				if other, ok := statement.Else.(*ast.BlockStmt); ok {
					scanBlock(other, clone(actions))
				}
			case *ast.SwitchStmt:
				for _, item := range statement.Body.List {
					clause := item.(*ast.CaseClause)
					scanBlock(&ast.BlockStmt{List: clause.Body}, clone(actions))
				}
			case *ast.ForStmt:
				scanBlock(statement.Body, clone(actions))
			case *ast.RangeStmt:
				scanBlock(statement.Body, clone(actions))
			}
		}
	}
	scanBlock(function.Body, map[string]struct {
		cases []recovery.CaseID
		kind  recovery.ActionKind
	}{})
	return routes, resolvedSites
}

func callFunction(call *ast.CallExpr) (*ast.CallExpr, string) {
	if call == nil {
		return nil, ""
	}
	name, ok := call.Fun.(*ast.Ident)
	if !ok {
		return call, ""
	}
	return call, name.Name
}

func stepsAssignmentRoot(expression ast.Expr) (string, bool) {
	steps, ok := expression.(*ast.SelectorExpr)
	if !ok || steps.Sel.Name != "Steps" {
		return "", false
	}
	action, ok := steps.X.(*ast.SelectorExpr)
	if !ok || action.Sel.Name != "Action" {
		return "", false
	}
	root, ok := action.X.(*ast.Ident)
	return root.Name, ok
}

func localCaseValues(function *ast.FuncDecl, constants map[string]string) map[string][]recovery.CaseID {
	locals := map[string][]recovery.CaseID{}
	ast.Inspect(function.Body, func(node ast.Node) bool {
		assignment, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for index, left := range assignment.Lhs {
			name, ok := left.(*ast.Ident)
			if !ok || index >= len(assignment.Rhs) {
				continue
			}
			value, ok := assignment.Rhs[index].(*ast.Ident)
			if !ok {
				continue
			}
			if caseID, ok := constants[value.Name]; ok {
				locals[name.Name] = append(locals[name.Name], recovery.CaseID(caseID))
			}
		}
		return true
	})
	return locals
}

func resolveCases(t *testing.T, expression ast.Expr, constants map[string]string, locals map[string][]recovery.CaseID) []recovery.CaseID {
	t.Helper()
	name, ok := expression.(*ast.Ident)
	if !ok {
		t.Fatalf("planner route case is %T, want identifier", expression)
	}
	if value, ok := constants[name.Name]; ok {
		return []recovery.CaseID{recovery.CaseID(value)}
	}
	if values := locals[name.Name]; len(values) != 0 {
		return values
	}
	t.Fatalf("planner route case %s is unresolved", name.Name)
	return nil
}

func resolveKind(t *testing.T, expression ast.Expr, constants map[string]string) recovery.ActionKind {
	t.Helper()
	name, ok := expression.(*ast.Ident)
	if !ok {
		t.Fatalf("planner route kind is %T, want identifier", expression)
	}
	value, ok := constants[name.Name]
	if !ok {
		t.Fatalf("planner route kind %s is unresolved", name.Name)
	}
	return recovery.ActionKind(value)
}

func resolveSteps(t *testing.T, expression ast.Expr, constants map[string]string) []recovery.ActionStep {
	t.Helper()
	if call, ok := expression.(*ast.CallExpr); ok {
		callee, ok := call.Fun.(*ast.Ident)
		if !ok || callee.Name != "append" || len(call.Args) < 2 {
			t.Fatalf("planner steps call is not a recognized append")
		}
		result := make([]recovery.ActionStep, 0, len(call.Args)-1)
		for _, argument := range call.Args[1:] {
			result = append(result, resolveSteps(t, argument, constants)...)
		}
		return result
	}
	literal, ok := expression.(*ast.CompositeLit)
	if ok {
		result := make([]recovery.ActionStep, 0, len(literal.Elts))
		for _, element := range literal.Elts {
			result = append(result, resolveSteps(t, element, constants)...)
		}
		return result
	}
	name, ok := expression.(*ast.Ident)
	if !ok {
		t.Fatalf("planner step is %T, want identifier", expression)
	}
	if name.Name == "nil" {
		return nil
	}
	value, ok := constants[name.Name]
	if !ok {
		t.Fatalf("planner step %s is unresolved", name.Name)
	}
	return []recovery.ActionStep{recovery.ActionStep(value)}
}

func typedStringConstants(t *testing.T, file *ast.File, typeName string) map[string]string {
	t.Helper()
	values := map[string]string{}
	for _, declaration := range file.Decls {
		constants, ok := declaration.(*ast.GenDecl)
		if !ok || constants.Tok != token.CONST {
			continue
		}
		for _, item := range constants.Specs {
			spec, ok := item.(*ast.ValueSpec)
			if !ok {
				continue
			}
			typeIdent, ok := spec.Type.(*ast.Ident)
			if !ok || typeIdent.Name != typeName {
				continue
			}
			for index, name := range spec.Names {
				literal, ok := spec.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("%s %s is not a string literal", typeName, name.Name)
				}
				values[name.Name] = literal.Value[1 : len(literal.Value)-1]
			}
		}
	}
	return values
}
