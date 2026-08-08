package workspace

import (
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestProductionImportsAreExplicitlyAllowed(t *testing.T) {
	t.Parallel()
	allowed := []string{
		"bytes",
		"context",
		"crypto/rand",
		"crypto/sha256",
		"encoding/json",
		"errors",
		"fmt",
		"io",
		"io/fs",
		"os",
		"os/exec",
		"path/filepath",
		"slices",
		"strconv",
		"strings",
		"time",
		"github.com/BeomSeogKim/Partitur/internal/canonical",
		"github.com/BeomSeogKim/Partitur/internal/faultpoint",
		"github.com/BeomSeogKim/Partitur/internal/protectedpath",
		"github.com/BeomSeogKim/Partitur/internal/runstate",
		"github.com/BeomSeogKim/Partitur/internal/runstore",
		"github.com/BeomSeogKim/Partitur/internal/score",
		"github.com/BeomSeogKim/Partitur/internal/validate",
	}
	slices.Sort(allowed)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var imports []string
	seen := make(map[string]bool)
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
			if !seen[path] {
				seen[path] = true
				imports = append(imports, path)
			}
		}
	}
	slices.Sort(imports)
	if !slices.Equal(imports, allowed) {
		t.Fatalf("production imports = %#v, want exact allowlist %#v", imports, allowed)
	}
}
