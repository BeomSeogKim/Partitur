package faultpoint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestAppendixEEdgeIDsAreCompleteAndUnique(t *testing.T) {
	designEdges := edgeIDsFromDesign(t)
	sourceEdges := edgeIDsFromPackageSource(t)

	designSet := uniqueEdgeIDs(t, "DESIGN E.2", designEdges)
	sourceSet := uniqueEdgeIDs(t, "Go EdgeID constants", sourceEdges)

	var onlyInDesign []string
	for edge := range designSet {
		if !sourceSet[edge] {
			onlyInDesign = append(onlyInDesign, edge)
		}
	}
	var onlyInSource []string
	for edge := range sourceSet {
		if !designSet[edge] {
			onlyInSource = append(onlyInSource, edge)
		}
	}
	sort.Strings(onlyInDesign)
	sort.Strings(onlyInSource)
	if len(onlyInDesign) != 0 || len(onlyInSource) != 0 {
		t.Fatalf("edge ID catalog mismatch: only in DESIGN E.2: %q; only in Go EdgeID constants: %q", onlyInDesign, onlyInSource)
	}
}

func edgeIDsFromDesign(t *testing.T) []string {
	t.Helper()

	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(contents), "\n")

	const catalogHeading = "## E.2 The catalog"
	const nextHeadingPrefix = "## E.3 "
	catalogStart := -1
	catalogHeadingCount := 0
	nextHeading := -1
	nextHeadingCount := 0
	for index, line := range lines {
		if line == catalogHeading {
			catalogStart = index + 1
			catalogHeadingCount++
		}
		if strings.HasPrefix(line, nextHeadingPrefix) {
			nextHeading = index
			nextHeadingCount++
		}
	}
	if catalogHeadingCount != 1 {
		t.Fatalf("%s heading count = %d, want 1", catalogHeading, catalogHeadingCount)
	}
	if nextHeadingCount != 1 {
		t.Fatalf("%s heading count = %d, want 1", strings.TrimSpace(nextHeadingPrefix), nextHeadingCount)
	}
	if nextHeading <= catalogStart {
		t.Fatalf("%s must follow %s", strings.TrimSpace(nextHeadingPrefix), catalogHeading)
	}

	edgeRow := regexp.MustCompile("^\\| `([a-z][a-z0-9_.]*)` \\|")
	var edges []string
	for _, line := range lines[catalogStart:nextHeading] {
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "| Edge |") || strings.HasPrefix(line, "|---") {
			continue
		}
		match := edgeRow.FindStringSubmatch(line)
		if match == nil {
			t.Fatalf("unparseable %s table row %q", catalogHeading, line)
		}
		edges = append(edges, match[1])
	}
	if len(edges) == 0 {
		t.Fatalf("%s extraction produced no edge IDs", catalogHeading)
	}
	return edges
}

func edgeIDsFromPackageSource(t *testing.T) []string {
	t.Helper()

	pkg := parsePackageSource(t)
	var edges []string
	for _, file := range pkg.Files {
		for _, declaration := range file.Decls {
			constants, ok := declaration.(*ast.GenDecl)
			if !ok || constants.Tok != token.CONST {
				continue
			}
			for _, spec := range constants.Specs {
				values := spec.(*ast.ValueSpec)
				hasEdgeName := false
				for _, name := range values.Names {
					if strings.HasPrefix(name.Name, "Edge") {
						hasEdgeName = true
					}
				}
				edgeType, ok := values.Type.(*ast.Ident)
				if !ok || edgeType.Name != "EdgeID" {
					if hasEdgeName {
						t.Fatalf("Edge-prefixed const declaration must have explicit type EdgeID")
					}
					continue
				}
				if len(values.Names) != len(values.Values) {
					t.Fatalf("EdgeID const declaration has %d names and %d values", len(values.Names), len(values.Values))
				}
				for index, expression := range values.Values {
					if !strings.HasPrefix(values.Names[index].Name, "Edge") {
						t.Fatalf("EdgeID const name %q must start with Edge", values.Names[index].Name)
					}
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						t.Fatalf("EdgeID const value must be a string literal, got %T", expression)
					}
					edge, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatalf("parse EdgeID const value %q: %v", literal.Value, err)
					}
					edges = append(edges, edge)
				}
			}
		}
	}
	if len(edges) == 0 {
		t.Fatal("package source contains no typed EdgeID constants")
	}
	return edges
}

func uniqueEdgeIDs(t *testing.T, source string, edges []string) map[string]bool {
	t.Helper()

	seen := make(map[string]bool, len(edges))
	for _, edge := range edges {
		if edge == "" {
			t.Fatalf("%s contains an empty edge ID", source)
		}
		if seen[edge] {
			t.Fatalf("%s contains duplicate edge ID %q", source, edge)
		}
		seen[edge] = true
	}
	return seen
}

func TestBoundaryPointIDsAreSemanticAndUnique(t *testing.T) {
	points := []PointID{
		PointPrepareObserved,
		PointQuiesceSessionsSwept,
		PointQuiesceCommitLockHeld,
		PointCancelSessionsSwept,
		PointCancelFenceDecided,
		PointSupersedeSessionsSwept,
		PointSupersedeFenceDecided,
		PointLaunchAdapterMarkerHeld,
		PointLaunchAdapterGateReleased,
		PointLaunchCriterionMarkerHeld,
		PointLaunchCriterionGateReleased,
	}
	seen := map[PointID]bool{}
	for _, point := range points {
		if point == "" || seen[point] {
			t.Fatalf("empty or duplicate point id %q", point)
		}
		seen[point] = true
	}
}

func TestPackageHasNoGlobalProbeSetter(t *testing.T) {
	pkg := parsePackageSource(t)
	for _, file := range pkg.Files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && function.Recv == nil && function.Name.Name == "SetProbe" {
				t.Fatal("package-level SetProbe is forbidden")
			}
		}
	}
}

func parsePackageSource(t *testing.T) *ast.Package {
	t.Helper()

	set := token.NewFileSet()
	packages, err := parser.ParseDir(set, ".", func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ok := packages["faultpoint"]
	if !ok {
		t.Fatal("faultpoint package not found in package source")
	}
	return pkg
}
