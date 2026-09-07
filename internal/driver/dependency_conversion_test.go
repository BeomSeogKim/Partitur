package driver

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestExecutionDependencyConversionsCoverPublicBundle(t *testing.T) {
	forward := dependencyConversionFields(t, "dependenciesFromExecution")
	reverse := dependencyConversionFields(t, "executionDependenciesFrom")
	public := reflect.TypeOf(ExecutionDependencies{})
	if len(forward) != public.NumField() || len(reverse) != public.NumField() {
		t.Fatalf("dependency conversion sizes forward=%d reverse=%d public=%d", len(forward), len(reverse), public.NumField())
	}
	for index := 0; index < public.NumField(); index++ {
		field := public.Field(index)
		t.Run(field.Name, func(t *testing.T) {
			privateName := strings.ToLower(field.Name[:1]) + field.Name[1:]
			if forward[privateName] != "execution."+field.Name {
				t.Fatalf("public-to-private mapping %s=%q, want %q", privateName, forward[privateName], "execution."+field.Name)
			}
			if reverse[field.Name] != "dependencies."+privateName {
				t.Fatalf("private-to-public mapping %s=%q, want %q", field.Name, reverse[field.Name], "dependencies."+privateName)
			}
		})
	}
}

func TestExecuteAttemptUsesOneImmutableDependencyView(t *testing.T) {
	function := driverFunction(t, "ExecuteAttempt")
	t.Run("complete_conversion", func(t *testing.T) {
		assignments := 0
		ast.Inspect(function.Body, func(node ast.Node) bool {
			assignment, ok := node.(*ast.AssignStmt)
			if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
				return true
			}
			left, ok := assignment.Lhs[0].(*ast.Ident)
			if !ok || left.Name != "dependencies" {
				return true
			}
			assignments++
			call, ok := assignment.Rhs[0].(*ast.CallExpr)
			if !ok {
				t.Fatal("ExecuteAttempt dependencies assignment must call dependenciesFromExecution")
			}
			callee, calleeOK := call.Fun.(*ast.Ident)
			argument, argumentOK := singleIdentifierArgument(call)
			if !calleeOK || callee.Name != "dependenciesFromExecution" || !argumentOK || argument != "executionDependencies" {
				t.Fatal("ExecuteAttempt dependencies assignment must use dependenciesFromExecution(executionDependencies)")
			}
			return false
		})
		if assignments != 1 {
			t.Fatalf("ExecuteAttempt dependencies assignments=%d, want exactly one", assignments)
		}
	})
	t.Run("no_public_reads_after_conversion", func(t *testing.T) {
		ast.Inspect(function.Body, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			owner, ownerOK := selectorOwner(selector)
			if ok && ownerOK && owner == "executionDependencies" {
				t.Fatalf("ExecuteAttempt reads public dependency %s after constructing its private view", selector.Sel.Name)
			}
			return true
		})
	})
	t.Run("private_view_has_no_writes", func(t *testing.T) {
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.AssignStmt:
				for _, target := range node.Lhs {
					if selectorOwnedBy(target, "dependencies") {
						t.Fatal("ExecuteAttempt mutates its converted dependency view")
					}
				}
			case *ast.IncDecStmt:
				if selectorOwnedBy(node.X, "dependencies") {
					t.Fatal("ExecuteAttempt mutates its converted dependency view")
				}
			}
			return true
		})
	})
	t.Run("private_view_has_no_address_escape", func(t *testing.T) {
		ast.Inspect(function.Body, func(node ast.Node) bool {
			address, ok := node.(*ast.UnaryExpr)
			if ok && address.Op == token.AND && expressionRootedAt(address.X, "dependencies") {
				t.Fatal("ExecuteAttempt exposes its converted dependency view for mutation")
			}
			return true
		})
	})
}

func TestProductionDependencyLiteralsAreReviewed(t *testing.T) {
	want := []string{
		"acceptance_budget.go:TerminalizeAcceptanceBudget:probe=terminalization.Probe",
		"driver.go:dependenciesFromExecution:acquireDriver=execution.AcquireDriver,afterMovementFailed=execution.afterMovementFailed,afterPrepareAcknowledged=execution.AfterPrepareAcknowledged,client=execution.Client,newID=execution.NewID,now=execution.Now,probe=execution.Probe,proposalDisposition=execution.ProposalDisposition,receiptObserver=execution.ReceiptObserver,resolveTrampoline=execution.ResolveTrampoline,storeFactory=execution.StoreFactory",
	}
	got := productionDependencyLiterals(t)
	if !slices.Equal(got, want) {
		t.Fatalf("production dependency literals = %q, want reviewed sites %q", got, want)
	}
}

func dependencyConversionFields(t *testing.T, name string) map[string]string {
	t.Helper()
	function := driverFunction(t, name)
	fields := make(map[string]string)
	ast.Inspect(function.Body, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, element := range literal.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				t.Fatalf("%s contains an unkeyed dependency field", name)
			}
			key, keyOK := pair.Key.(*ast.Ident)
			if !keyOK {
				t.Fatalf("%s dependency field has no name", name)
			}
			value, valueOK := pair.Value.(*ast.SelectorExpr)
			if !valueOK {
				fields[key.Name] = ""
				continue
			}
			owner, ownerOK := value.X.(*ast.Ident)
			if !ownerOK {
				fields[key.Name] = ""
				continue
			}
			fields[key.Name] = owner.Name + "." + value.Sel.Name
		}
		return false
	})
	if len(fields) == 0 {
		t.Fatalf("%s dependency literal is absent", name)
	}
	return fields
}

func productionDependencyLiterals(t *testing.T) []string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate dependency conversion test")
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(current), "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	var sites []string
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				literal, ok := node.(*ast.CompositeLit)
				if !ok {
					return true
				}
				name, nameOK := literal.Type.(*ast.Ident)
				if !nameOK || name.Name != "dependencies" {
					return true
				}
				fields := make([]string, 0, len(literal.Elts))
				for _, element := range literal.Elts {
					pair, ok := element.(*ast.KeyValueExpr)
					if !ok {
						t.Fatalf("%s:%s contains an unkeyed dependency field", filepath.Base(path), function.Name.Name)
					}
					key, keyOK := pair.Key.(*ast.Ident)
					value, valueOK := pair.Value.(*ast.SelectorExpr)
					owner, ownerOK := selectorOwner(value)
					if !keyOK || !valueOK || !ownerOK {
						fields = append(fields, "unreviewed")
						continue
					}
					fields = append(fields, key.Name+"="+owner+"."+value.Sel.Name)
				}
				sort.Strings(fields)
				sites = append(sites, filepath.Base(path)+":"+function.Name.Name+":"+strings.Join(fields, ","))
				return false
			})
		}
	}
	sort.Strings(sites)
	return sites
}

func selectorOwner(selector *ast.SelectorExpr) (string, bool) {
	if selector == nil {
		return "", false
	}
	owner, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	return owner.Name, true
}

func selectorOwnedBy(expression ast.Expr, owner string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	got, ownerOK := selectorOwner(selector)
	return ok && ownerOK && got == owner
}

func expressionRootedAt(expression ast.Expr, owner string) bool {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name == owner
	case *ast.SelectorExpr:
		return expressionRootedAt(expression.X, owner)
	case *ast.ParenExpr:
		return expressionRootedAt(expression.X, owner)
	default:
		return false
	}
}

func driverFunction(t *testing.T, name string) *ast.FuncDecl {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate dependency conversion test")
	}
	path := filepath.Join(filepath.Dir(current), "driver.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == name {
			return function
		}
	}
	t.Fatalf("driver function %s is absent", name)
	return nil
}

func singleIdentifierArgument(call *ast.CallExpr) (string, bool) {
	if call == nil || len(call.Args) != 1 {
		return "", false
	}
	argument, ok := call.Args[0].(*ast.Ident)
	if !ok {
		return "", false
	}
	return argument.Name, ok
}
