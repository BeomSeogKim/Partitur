package validate

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestProductionImportsAreExplicitlyAllowed(t *testing.T) {
	t.Parallel()
	allowed := map[string]bool{
		"errors":        true,
		"os":            true,
		"path/filepath": true,
		"slices":        true,
		"sort":          true,
		"github.com/BeomSeogKim/Partitur/internal/adapter": true,
		"github.com/BeomSeogKim/Partitur/internal/cast":    true,
		"github.com/BeomSeogKim/Partitur/internal/score":   true,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(
			token.NewFileSet(),
			name,
			nil,
			parser.ImportsOnly,
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if !allowed[path] {
				t.Errorf("%s imports forbidden package %q", name, path)
			}
		}
	}
}
