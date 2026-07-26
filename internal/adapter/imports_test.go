package adapter

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
		"bytes":         true,
		"encoding/json": true,
		"errors":        true,
		"fmt":           true,
		"io":            true,
		"io/fs":         true,
		"os":            true,
		"os/exec":       true,
		"path/filepath": true,
		"sort":          true,
		"strconv":       true,
		"strings":       true,
		"sync":          true,
		"syscall":       true,
		"time":          true,
		"unicode/utf8":  true,
		"unsafe":        true,
		"github.com/BeomSeogKim/Partitur/internal/adapterkit": true,
		"github.com/BeomSeogKim/Partitur/internal/protocol":   true,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
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
