package recoveryexec

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/driver"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
)

func defaultKinds() map[recovery.ActionKind]StepHandler {
	return defaultKindsWithExecutionDependencies(testAttemptDependencies())
}

func selectInitialPerformer(ctx context.Context, execution HandlerContext, action recovery.Action) error {
	return selectInitialPerformerWithExecutionDependencies(ctx, execution, action, testAttemptDependencies())
}

func materializeSuccessor(ctx context.Context, execution HandlerContext, action recovery.Action) error {
	return materializeSuccessorWithExecutionDependencies(ctx, execution, action, testAttemptDependencies())
}

func testAttemptDependencies() driver.ExecutionDependencies {
	return driver.DefaultExecutionDependencies(faultpoint.Nop{})
}

func TestRecoveryProductionDoesNotConstructAttemptDependencies(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(".", entry.Name())
		file, err := parser.ParseFile(files, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		driverNames := map[string]bool{}
		for _, imported := range file.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if importPath != "github.com/BeomSeogKim/Partitur/internal/driver" {
				continue
			}
			name := "driver"
			if imported.Name != nil {
				name = imported.Name.Name
			}
			driverNames[name] = true
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch expression := node.(type) {
			case *ast.CallExpr:
				if isDriverSelector(expression.Fun, driverNames, "DefaultExecutionDependencies") {
					t.Errorf("production recovery constructs default attempt dependencies at %s", files.Position(expression.Pos()))
				}
			case *ast.CompositeLit:
				if isDriverSelector(expression.Type, driverNames, "ExecutionDependencies") {
					t.Errorf("production recovery constructs an attempt dependency literal at %s", files.Position(expression.Pos()))
				}
			}
			return true
		})
	}
}

func isDriverSelector(expression ast.Expr, driverNames map[string]bool, selected string) bool {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != selected {
		return false
	}
	identifier, ok := selector.X.(*ast.Ident)
	return ok && driverNames[identifier.Name]
}
