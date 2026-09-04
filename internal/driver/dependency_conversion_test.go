package driver

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"runtime"
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

func TestExecuteAttemptUsesCompleteDependencyConversion(t *testing.T) {
	function := driverFunction(t, "ExecuteAttempt")
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
			t.Fatalf("ExecuteAttempt dependencies assignment must use dependenciesFromExecution(executionDependencies)")
		}
		return false
	})
	if assignments != 1 {
		t.Fatalf("ExecuteAttempt dependencies assignments=%d, want exactly one", assignments)
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
