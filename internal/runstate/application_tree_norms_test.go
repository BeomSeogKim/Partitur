package runstate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type applicationTreeRule struct {
	id    string
	value string
}

type applicationTreeSite struct {
	id          string
	timing      string
	requirement string
}

type applicationTreeObservation struct {
	file     string
	function string
	variable string
}

func TestApplicationTreeProjectionIsSpecified(t *testing.T) {
	// Given
	lines := recoveryDesignLines(t)
	section := applicationTreeProjectionSection(t, lines)
	rules := applicationTreeRules(t, section)
	sites := applicationTreeComparisonSites(t, section)
	global := globalExitCodeDescriptions(t, lines)
	shipping := recoveryDocumentSection(t, lines,
		"## 8. Verification and shipping",
		"## 9. Amendments")
	apply := shippingExitCodeTable(t, shipping,
		"**`partitur apply` exit mapping.**",
		"| Code | `partitur apply` outcome |")

	// When
	wantRules := []applicationTreeRule{
		{id: "state-directory-paths", value: "excluded"},
		{id: "candidate-identity", value: "raw"},
		{id: "candidate-comparisons", value: "projected"},
		{id: "cleanliness", value: "state-directory paths allowed"},
		{id: "root-score", value: "included"},
		{id: "candidate-diff", value: "protected worktree paths forbidden"},
	}
	wantSites := []applicationTreeSite{
		{id: "precondition", timing: "before `apply.started`", requirement: "compare checkout with candidate `base_tree`"},
		{id: "post-apply", timing: "after applying the candidate patch", requirement: "compare checkout with candidate `result_tree`"},
		{id: "rollback-before-restore", timing: "after a patch failure and before restore", requirement: "compare checkout with candidate `base_tree`"},
		{id: "rollback-after-restore", timing: "after restoring touched paths", requirement: "compare checkout with candidate `base_tree`"},
		{id: "recovery-cause-observation", timing: "before recording `apply.recovery_required`", requirement: "observe checkout"},
		{id: "recovery-resolution", timing: "after recording the recovery cause", requirement: "compare checkout with candidate `base_tree` and `result_tree`"},
	}

	// Then
	assertApplicationTreeRules(t, rules, wantRules)
	assertApplicationTreeSites(t, sites, wantSites)
	assertApplicationTreeProtectedDiffExit(t, global, apply)
	assertApplicationTreeRecoveryObservationPayload(t, lines)
}

func TestApplicationTreeSitesReconcileApplicationObservations(t *testing.T) {
	// Among non-test Go files in `internal/runstore`, the only direct assignments from applicationWorktreeTree are the six documented `(file, function, variable)` observation sites.
	// This does not prove that every possible checkout-tree observation is covered: one built through a different helper or a direct temporary-index Git sequence is invisible to it.
	// It also proves nothing about whether comparisons use projected operands.
	// Given
	files := applicationSourceFiles(t)
	got := applicationTreeObservations(t, files)
	want := []applicationTreeObservation{
		{file: "application.go", function: "applyCandidate", variable: "beforeTree"},
		{file: "application.go", function: "applyCandidate", variable: "afterTree"},
		{file: "application.go", function: "applyCandidate", variable: "restored"},
		{file: "application.go", function: "applyCandidate", variable: "restored"},
		{file: "application.go", function: "recoverApplication", variable: "tree"},
		{file: "application.go", function: "recoverApplication", variable: "tree"},
	}

	// When
	assertApplicationTreeObservations(t, got, want)
}

func applicationTreeProjectionSection(t *testing.T, lines []string) []string {
	t.Helper()

	start := uniqueLinePrefixIndex(t, lines, "**Application-tree projection.**")
	end := uniqueLinePrefixIndex(t, lines, "**`apply` preconditions** (under the state lock):")
	if end <= start {
		t.Fatal("application-tree projection must precede apply preconditions")
	}
	return lines[start+1 : end]
}

func applicationTreeRules(t *testing.T, lines []string) []applicationTreeRule {
	t.Helper()

	rows := applicationTreeTable(t, "application-tree rules", lines, "| Rule | Value |", 2)
	rules := make([]applicationTreeRule, 0, len(rows))
	for _, row := range rows {
		rules = append(rules, applicationTreeRule{id: row[0], value: row[1]})
	}
	if len(rules) == 0 {
		t.Fatal("application-tree rules extracted no rows")
	}
	return rules
}

func applicationTreeComparisonSites(t *testing.T, lines []string) []applicationTreeSite {
	t.Helper()

	rows := applicationTreeTable(t, "application-tree comparison sites", lines,
		"| Site | Timing | Required application-tree use |", 3)
	sites := make([]applicationTreeSite, 0, len(rows))
	for _, row := range rows {
		sites = append(sites, applicationTreeSite{id: row[0], timing: row[1], requirement: row[2]})
	}
	if len(sites) == 0 {
		t.Fatal("application-tree comparison sites extracted no rows")
	}
	return sites
}

func applicationTreeTable(t *testing.T, name string, lines []string, header string, cells int) [][]string {
	t.Helper()

	index := uniqueLineIndex(t, lines, header)
	if index+1 >= len(lines) || lines[index+1] != strings.Repeat("|---", cells)+"|" {
		t.Fatalf("%s has missing or malformed table separator", name)
	}

	var rows [][]string
	for index++; index+1 < len(lines) && strings.HasPrefix(lines[index+1], "|"); index++ {
		rows = append(rows, markdownTableCells(t, name, lines[index+1], cells))
	}
	if len(rows) == 0 {
		t.Fatalf("%s extracted no rows", name)
	}
	return rows
}

func assertApplicationTreeRules(t *testing.T, got, want []applicationTreeRule) {
	t.Helper()

	gotByID := make(map[string]applicationTreeRule, len(got))
	for _, rule := range got {
		if rule.id == "" || rule.value == "" {
			t.Fatalf("application-tree rule is incomplete: %#v", rule)
		}
		if _, duplicate := gotByID[rule.id]; duplicate {
			t.Fatalf("application-tree rules duplicate %q", rule.id)
		}
		gotByID[rule.id] = rule
	}
	if len(gotByID) != len(want) {
		t.Fatalf("application-tree rule count = %d, want %d", len(gotByID), len(want))
	}
	for _, rule := range want {
		if gotByID[rule.id] != rule {
			t.Fatalf("application-tree rule %q = %#v, want %#v", rule.id, gotByID[rule.id], rule)
		}
	}
}

func assertApplicationTreeSites(t *testing.T, got, want []applicationTreeSite) {
	t.Helper()

	gotByID := make(map[string]applicationTreeSite, len(got))
	for _, site := range got {
		if site.id == "" || site.timing == "" || site.requirement == "" {
			t.Fatalf("application-tree comparison site is incomplete: %#v", site)
		}
		if _, duplicate := gotByID[site.id]; duplicate {
			t.Fatalf("application-tree comparison sites duplicate %q", site.id)
		}
		gotByID[site.id] = site
	}
	if len(gotByID) != len(want) {
		t.Fatalf("application-tree comparison site count = %d, want %d", len(gotByID), len(want))
	}
	for _, site := range want {
		if gotByID[site.id] != site {
			t.Fatalf("application-tree comparison site %q = %#v, want %#v", site.id, gotByID[site.id], site)
		}
	}
}

func assertApplicationTreeProtectedDiffExit(t *testing.T, global, apply map[int]string) {
	t.Helper()

	const cause = "a candidate raw diff naming a protected worktree path"
	if !strings.Contains(global[2], cause) {
		t.Fatalf("global exit-code 2 excludes application-tree cause %q", cause)
	}
	if !strings.Contains(apply[2], cause) {
		t.Fatalf("apply exit-code 2 excludes application-tree cause %q", cause)
	}
}

func assertApplicationTreeRecoveryObservationPayload(t *testing.T, lines []string) {
	t.Helper()

	start := uniqueLineIndex(t, lines, "apply.recovery_required {")
	end := -1
	for index := start + 1; index < len(lines); index++ {
		if lines[index] == "}" {
			end = index
			break
		}
	}
	if end == -1 {
		t.Fatal("apply.recovery_required payload has no closing delimiter")
	}
	observedTreeComment := ""
	for index := start + 1; index < end; index++ {
		line := strings.TrimSpace(lines[index])
		if !strings.HasPrefix(line, "observed_tree?") {
			continue
		}
		_, comment, found := strings.Cut(line, "#")
		if !found {
			t.Fatal("apply.recovery_required observed_tree has no comment")
		}
		observedTreeComment = strings.TrimSpace(comment)
		for index++; index < end && strings.HasPrefix(strings.TrimSpace(lines[index]), "#"); index++ {
			observedTreeComment += " " + strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[index]), "#"))
		}
		break
	}
	const observedTree = "observed_tree?, # application tree observed under the lock, when it could be computed"
	if "observed_tree?, # "+observedTreeComment != observedTree {
		t.Fatalf("apply.recovery_required observed_tree lacks application-tree meaning %q", observedTree)
	}
}

type applicationSourceFile struct {
	name string
	file *ast.File
}

func applicationSourceFiles(t *testing.T) []applicationSourceFile {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join("..", "runstore", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fileSet := token.NewFileSet()
	files := make([]applicationSourceFile, 0, len(paths))
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fileSet, path, contents, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, applicationSourceFile{name: filepath.Base(path), file: file})
	}
	if len(files) == 0 {
		t.Fatal("internal/runstore has no non-test Go files")
	}
	return files
}

func applicationTreeObservations(t *testing.T, files []applicationSourceFile) []applicationTreeObservation {
	t.Helper()

	type positionedObservation struct {
		applicationTreeObservation
		file     string
		position token.Pos
	}
	var positioned []positionedObservation
	for _, source := range files {
		for _, declaration := range source.file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				assignment, ok := node.(*ast.AssignStmt)
				if !ok || len(assignment.Lhs) == 0 || len(assignment.Rhs) != 1 {
					return true
				}
				call, ok := assignment.Rhs[0].(*ast.CallExpr)
				if !ok || calledIdentifier(call) != "applicationWorktreeTree" {
					return true
				}
				variable, ok := assignment.Lhs[0].(*ast.Ident)
				if !ok {
					t.Fatalf("applicationWorktreeTree result is not assigned to an identifier in %s", source.name)
				}
				positioned = append(positioned, positionedObservation{
					applicationTreeObservation: applicationTreeObservation{file: source.name, function: function.Name.Name, variable: variable.Name},
					file:                       source.name,
					position:                   call.Pos(),
				})
				return true
			})
		}
	}
	sort.Slice(positioned, func(left, right int) bool {
		if positioned[left].file != positioned[right].file {
			return positioned[left].file < positioned[right].file
		}
		return positioned[left].position < positioned[right].position
	})
	observations := make([]applicationTreeObservation, 0, len(positioned))
	for _, observation := range positioned {
		observations = append(observations, observation.applicationTreeObservation)
	}
	return observations
}

func calledIdentifier(call *ast.CallExpr) string {
	identifier, _ := call.Fun.(*ast.Ident)
	if identifier == nil {
		return ""
	}
	return identifier.Name
}

func assertApplicationTreeObservations(t *testing.T, got, want []applicationTreeObservation) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("applicationWorktreeTree observations = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("applicationWorktreeTree observation %d = %#v, want %#v", index, got[index], want[index])
		}
	}
}
