package runstate

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestPackageDirectImportAllowlist(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var imports []string
	seen := map[string]bool{}
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
	want := []string{
		"bytes",
		"encoding/json",
		"errors",
		"fmt",
		"math",
		"slices",
		"strconv",
		"time",
	}
	if !slices.Equal(imports, want) {
		t.Fatalf("direct imports = %v, want exact allowlist %v", imports, want)
	}
	for _, denied := range []string{"os", "io", "syscall", "os/exec", "path/filepath", "net"} {
		if seen[denied] {
			t.Fatalf("write-capable import %q is forbidden", denied)
		}
	}
}

func TestExportedStructsHaveNoWriterPathOrProbeFields(t *testing.T) {
	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, ".", func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	files := make([]*ast.File, 0, len(packages["runstate"].Files))
	for _, file := range packages["runstate"].Files {
		files = append(files, file)
	}
	configuration := types.Config{Importer: importer.Default(), Error: func(error) {}}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}}
	_, _ = configuration.Check("runstate", set, files, info)

	for identifier, object := range info.Defs {
		if object == nil || !identifier.IsExported() {
			continue
		}
		typeName, ok := object.(*types.TypeName)
		if !ok {
			continue
		}
		named, ok := typeName.Type().(*types.Named)
		if !ok {
			continue
		}
		structure, ok := named.Underlying().(*types.Struct)
		if !ok {
			continue
		}
		for index := 0; index < structure.NumFields(); index++ {
			field := structure.Field(index)
			if !field.Exported() {
				continue
			}
			text := strings.ToLower(field.Name() + " " + field.Type().String())
			for _, forbidden := range []string{"writer", "path", "probe", "os.file", "filepath"} {
				if strings.Contains(text, forbidden) {
					t.Errorf("%s.%s exposes forbidden capability %q", identifier.Name, field.Name(), forbidden)
				}
			}
		}
	}
}
