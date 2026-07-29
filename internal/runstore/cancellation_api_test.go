package runstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCancellationSweepHasNoExportedEntryPoint(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate cancellation API test")
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(filepath.Dir(testFile), "cancellation.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range parsed.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			if ast.IsExported(declaration.Name.Name) && strings.Contains(declaration.Name.Name, "Cancellation") && declaration.Name.Name != "RequestCancellation" && declaration.Name.Name != "ExecuteCancellation" {
				t.Fatalf("cancellation step %s is exported outside the oracle", declaration.Name.Name)
			}
		case *ast.GenDecl:
			for _, specification := range declaration.Specs {
				if typeSpec, ok := specification.(*ast.TypeSpec); ok && ast.IsExported(typeSpec.Name.Name) && strings.Contains(typeSpec.Name.Name, "Cancellation") {
					t.Fatalf("cancellation step proof %s is exported outside the oracle", typeSpec.Name.Name)
				}
			}
		}
	}
}
