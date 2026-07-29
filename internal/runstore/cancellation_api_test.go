package runstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// cancellationOracleStepFloor is a floor because a new boundary-reaching or
// receipt-appending step must be discovered without changing this test.
const cancellationOracleStepFloor = 5

func TestCancellationSweepHasNoExportedEntryPoint(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate cancellation API test")
	}

	scan, err := scanCancellationAPI(filepath.Dir(testFile))
	if err != nil {
		t.Fatal(err)
	}
	if scan.parsedFiles == 0 {
		t.Fatal("cancellation API guard parsed no production Go files")
	}
	if len(scan.oracleSteps) == 0 {
		t.Fatal("cancellation API guard discovered no oracle-step functions")
	}
	if len(scan.oracleSteps) < cancellationOracleStepFloor {
		t.Fatalf("cancellation API guard discovered %d oracle-step functions; want at least %d", len(scan.oracleSteps), cancellationOracleStepFloor)
	}
	if len(scan.exportedNames) != 0 {
		t.Fatalf("cancellation step identifiers are exported outside the oracle: %s", strings.Join(scan.exportedNames, ", "))
	}
	if len(scan.exportedSteps) != 0 {
		t.Fatalf("cancellation oracle steps are exported outside the oracle: %s", strings.Join(scan.exportedSteps, ", "))
	}
}

type cancellationAPIScan struct {
	parsedFiles   int
	oracleSteps   []string
	exportedNames []string
	exportedSteps []string
}

func scanCancellationAPI(directory string) (cancellationAPIScan, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return cancellationAPIScan{}, err
	}

	var scan cancellationAPIScan
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		path := filepath.Join(directory, name)
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return cancellationAPIScan{}, err
		}
		scan.parsedFiles++
		for _, declaration := range parsed.Decls {
			scan.inspectDeclaration(name, declaration)
		}
	}
	return scan, nil
}

func (scan *cancellationAPIScan) inspectDeclaration(file string, declaration ast.Decl) {
	switch declaration := declaration.(type) {
	case *ast.FuncDecl:
		name := declaration.Name.Name
		if ast.IsExported(name) && cancellationNamed(name) && !cancellationEntryPoint(name) {
			scan.exportedNames = append(scan.exportedNames, file+":"+name)
		}
		if cancellationOracleStep(declaration.Body) {
			scan.oracleSteps = append(scan.oracleSteps, file+":"+name)
			if ast.IsExported(name) && !cancellationEntryPoint(name) {
				scan.exportedSteps = append(scan.exportedSteps, file+":"+name)
			}
		}
	case *ast.GenDecl:
		for _, specification := range declaration.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if ok && ast.IsExported(typeSpec.Name.Name) && cancellationNamed(typeSpec.Name.Name) {
				scan.exportedNames = append(scan.exportedNames, file+":"+typeSpec.Name.Name)
			}
		}
	}
}

func cancellationNamed(name string) bool {
	return strings.Contains(name, "Cancellation") || strings.Contains(name, "Cancel")
}

func cancellationEntryPoint(name string) bool {
	return name == "RequestCancellation" || name == "ExecuteCancellation"
}

func cancellationOracleStep(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if cancellationBoundaryCall(call) || cancellationReceiptCall(call) {
			found = true
			return false
		}
		return true
	})
	return found
}

func cancellationBoundaryCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Reached" {
		return false
	}
	for _, argument := range call.Args {
		point, ok := argument.(*ast.SelectorExpr)
		if !ok || !strings.HasPrefix(point.Sel.Name, "PointCancel") {
			continue
		}
		packageName, ok := point.X.(*ast.Ident)
		if ok && packageName.Name == "faultpoint" {
			return true
		}
	}
	return false
}

func cancellationReceiptCall(call *ast.CallExpr) bool {
	for _, argument := range call.Args {
		literal, ok := argument.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			continue
		}
		value, err := strconv.Unquote(literal.Value)
		if err == nil && strings.HasPrefix(value, "cancellation.") {
			return true
		}
	}
	return false
}
