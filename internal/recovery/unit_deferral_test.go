package recovery

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// unitOwnedDeferralDeclaration is the one file permitted to name the boundary
// type. Every other production file naming it is a second representation.
const unitOwnedDeferralDeclaration = "internal/recovery/unit_deferral.go"

// unitOwnedDeferralExcluded are the repository roots that are not product
// code. Everything else is walked, so a new top-level package is inside the
// denominator by default rather than outside it.
var unitOwnedDeferralExcluded = map[string]bool{
	".git":               true,
	"docs":               true,
	"spikes":             true,
	"reference-workflow": true,
	"vendor":             true,
}

func TestUnitOwnedDeferralBoundary(t *testing.T) {
	if population := UnitOwnedDeferrals(); len(population) != 0 {
		t.Fatalf("unit-owned deferral population = %d (%v), want 0", len(population), population)
	}

	declarations, references := unitOwnedDeferralBoundary(t)
	if len(declarations) != 1 {
		t.Fatalf("UnitOwnedDeferral declarations = %d at %s, want 1", len(declarations), strings.Join(declarations, ", "))
	}
	if declarations[0] != unitOwnedDeferralDeclaration {
		t.Fatalf("UnitOwnedDeferral declaration = %s, want %s", declarations[0], unitOwnedDeferralDeclaration)
	}
	if len(references) != 0 {
		t.Fatalf("UnitOwnedDeferral named by %d production file(s) outside its declaration: %s", len(references), strings.Join(references, ", "))
	}
}

// unitOwnedDeferralBoundary returns the production files declaring the
// boundary type and the production files outside the declaration that name it
// at all. Naming is the predicate rather than construction: a value of a named
// type cannot be produced without the name appearing in some production
// signature, so counting names catches the shapes a composite-literal scan
// misses -- `[]UnitOwnedDeferral{{}}`, a map or array element, a zero value, a
// constructor's return type.
func unitOwnedDeferralBoundary(t *testing.T) ([]string, []string) {
	t.Helper()

	repository := filepath.Clean(filepath.Join("..", ".."))
	var declarations, references []string
	err := filepath.WalkDir(repository, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repository, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if relative != "." && unitOwnedDeferralExcluded[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		for _, declaration := range file.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok || group.Tok != token.TYPE {
				continue
			}
			for _, specification := range group.Specs {
				typeSpec, ok := specification.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != "UnitOwnedDeferral" {
					continue
				}
				if !unitOwnedDeferralShape(typeSpec.Type) {
					t.Fatalf("UnitOwnedDeferral at %s must be struct { Kind ActionKind; Unit string }", relative)
				}
				declarations = append(declarations, relative)
			}
		}
		if relative == unitOwnedDeferralDeclaration {
			return nil
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if unitOwnedDeferralNamed(node) {
				references = append(references, relative)
				return false
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(declarations)
	sort.Strings(references)
	return declarations, unitOwnedDeferralUnique(references)
}

func unitOwnedDeferralNamed(node ast.Node) bool {
	switch typed := node.(type) {
	case *ast.Ident:
		return typed.Name == "UnitOwnedDeferral"
	case *ast.SelectorExpr:
		return typed.Sel != nil && typed.Sel.Name == "UnitOwnedDeferral"
	default:
		return false
	}
}

func unitOwnedDeferralUnique(values []string) []string {
	var unique []string
	for index, value := range values {
		if index == 0 || value != values[index-1] {
			unique = append(unique, value)
		}
	}
	return unique
}

func unitOwnedDeferralShape(expression ast.Expr) bool {
	structure, ok := expression.(*ast.StructType)
	if !ok || structure.Fields == nil || len(structure.Fields.List) != 2 {
		return false
	}
	return unitOwnedDeferralField(structure.Fields.List[0], "Kind", "ActionKind") &&
		unitOwnedDeferralField(structure.Fields.List[1], "Unit", "string")
}

func unitOwnedDeferralField(field *ast.Field, name, typeName string) bool {
	if len(field.Names) != 1 || field.Names[0].Name != name {
		return false
	}
	identifier, ok := field.Type.(*ast.Ident)
	return ok && identifier.Name == typeName
}
