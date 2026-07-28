package recovery

import (
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestPlannerProductionImportsRemainPure(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var imports []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range file.Imports {
			path, err := strconv.Unquote(declaration.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if !seen[path] {
				seen[path] = true
				imports = append(imports, path)
			}
		}
	}
	slices.Sort(imports)
	want := []string{"github.com/BeomSeogKim/Partitur/internal/runstate"}
	if !slices.Equal(imports, want) {
		t.Fatalf("production imports = %v, want pure allowlist %v", imports, want)
	}
	for _, denied := range []string{"os", "io", "syscall", "os/exec", "path/filepath", "net", "time"} {
		if seen[denied] {
			t.Errorf("planner imports forbidden effect-capable package %q", denied)
		}
	}
}
