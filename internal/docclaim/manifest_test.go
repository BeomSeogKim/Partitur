package docclaim

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	markerIDPattern        = regexp.MustCompile(`^[A-Za-z0-9]+(?:[._/-][A-Za-z0-9]+)*$`)
	testNamePattern        = regexp.MustCompile(`^Test[A-Za-z0-9_]+$`)
	packagePattern         = regexp.MustCompile(`^\./[A-Za-z0-9_./-]+$`)
	errManifestUnpopulated = errors.New("documentation claim manifest is unpopulated")
)

const p5OutstandingText = "The canonical P5 registry and its batched executed-evidence gate are bound, but the registry intentionally contains zero baselines and zero claim rows because the reviewed marking pass has not happened. P5 therefore remains an outstanding prerequisite; its remaining work is the human review of every in-scope document, the complete baseline and claim population, and promotion to a completion row only after that review is complete."

func TestDocumentationClaimSchemaIsLocked(t *testing.T) {
	document := readClaimsDocument(t)
	requireOccurrence(t, document, "# Documentation claim manifest", 1)
	requireOccurrence(t, document, "## Claim definition", 1)
	requireOccurrence(t, document, "## Scope", 1)
	requireOccurrence(t, document, "## Go registry schema", 1)
	requireOccurrence(t, document, "## Discharge", 1)
	requireOccurrence(t, document, "## Activation boundary", 1)
	requireOccurrence(t, document, "membership in the\nmanifest confers its claim status", 1)
	requireOccurrence(t, strings.Join(strings.Fields(document), " "), "A package-level zero with no matching test is a failure.", 1)
	requireOccurrence(t, document, "`internal/docclaim/manifest.go`", 1)

	rowBaseline := "| `baseline` | `document_path`, `git_blob` | Exactly one row for every in-scope document. `git_blob` is the lowercase 40-hex Git blob ID of the reviewed document. |"
	rowClaim := "| `claim` | `document_path`, `marker_id`, `evidence_package`, `evidence_test` | The claim key is unique, names one unique anchor in its baseline document, and names one top-level Go test. |"
	requireOccurrence(t, document, rowBaseline, 1)
	requireOccurrence(t, document, rowClaim, 1)
	requireOccurrence(t, document, "| Record | Required fields | Rule |\n|---|---|---|\n"+rowBaseline+"\n"+rowClaim, 1)

	wantScope := []string{
		"README.md",
		"docs/CLAIMS.md",
		"docs/COMPLETION.md",
		"docs/CONCEPT.md",
		"docs/DESIGN.md",
		"docs/HARNESS.md",
		"docs/MARKERS.md",
		"docs/decisions/0001-amendment-envelope.md",
		"docs/decisions/0002-verification-semantics.md",
		"docs/decisions/0003-unit-owned-deferral-boundary.md",
		"docs/decisions/0004-between-unit-dispatch-liveness.md",
		"docs/ko/CONCEPT.md",
	}
	gotScope, err := documentationScope(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(gotScope, "\n") != strings.Join(wantScope, "\n") {
		t.Fatalf("documentation scope =\n%s\nwant:\n%s", strings.Join(gotScope, "\n"), strings.Join(wantScope, "\n"))
	}
}

func TestDocumentationClaimManifest(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	scope, err := documentationScope(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := readDocuments(repoRoot, scope)
	if err != nil {
		t.Fatal(err)
	}

	err = validateManifest(documentationClaimManifest, scope, documents)
	if errors.Is(err, errManifestUnpopulated) {
		if len(documentationClaimManifest.baselines) != 0 || len(documentationClaimManifest.claims) != 0 {
			t.Fatal("unpopulated canonical manifest must have zero baselines and zero claims")
		}
		completion := strings.Join(strings.Fields(readCompletionDocument(t)), " ")
		if got := strings.Count(completion, p5OutstandingText); got != 1 {
			t.Fatalf("COMPLETION P5 outstanding statement occurrences = %d, want 1", got)
		}
	} else if err != nil {
		t.Fatalf("canonical manifest rejected: %v", err)
	}

	if err := runEvidence(repoRoot, documentationClaimManifest.claims); err != nil {
		t.Fatalf("canonical manifest evidence rejected: %v", err)
	}
}

func TestDocumentationClaimSchemaValidatesPopulation(t *testing.T) {
	document := "<!-- partitur:mark begin anchor=run.started -->The run records its start.<!-- partitur:mark end anchor=run.started -->"
	otherDocument := "<!-- partitur:mark begin non-normative -->Context.<!-- partitur:mark end non-normative -->"
	documents := map[string]string{"docs/example.md": document, "docs/other.md": otherDocument}
	valid := manifest{
		baselines: []baseline{
			{documentPath: "docs/example.md", gitBlob: mustGitBlobID(t, document)},
			{documentPath: "docs/other.md", gitBlob: mustGitBlobID(t, otherDocument)},
		},
		claims: []claim{{
			documentPath: "docs/example.md", markerID: "run.started",
			evidencePackage: "./internal/example", evidenceTest: "TestRunStarted",
		}},
	}
	scope := []string{"docs/example.md", "docs/other.md"}
	if err := validateManifest(valid, scope, documents); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*testing.T, *manifest, map[string]string)
		want   string
	}{
		{"empty manifest", func(_ *testing.T, got *manifest, _ map[string]string) { *got = manifest{} }, "manifest is unpopulated"},
		{"missing scope member", func(_ *testing.T, got *manifest, _ map[string]string) { got.baselines = got.baselines[:1] }, "has no baseline"},
		{"duplicate baseline", func(_ *testing.T, got *manifest, _ map[string]string) {
			got.baselines = append(got.baselines, got.baselines[0])
		}, "duplicate baseline path"},
		{"out-of-scope baseline", func(t *testing.T, got *manifest, docs map[string]string) {
			docs["docs/outside.md"] = "outside"
			got.baselines = append(got.baselines, baseline{documentPath: "docs/outside.md", gitBlob: mustGitBlobID(t, "outside")})
		}, "is out of scope"},
		{"malformed blob", func(_ *testing.T, got *manifest, _ map[string]string) { got.baselines[0].gitBlob = "abc" }, "malformed Git blob"},
		{"stale blob", func(_ *testing.T, got *manifest, _ map[string]string) {
			got.baselines[0].gitBlob = strings.Repeat("0", 40)
		}, "does not match reviewed document"},
		{"duplicate claim key", func(_ *testing.T, got *manifest, _ map[string]string) { got.claims = append(got.claims, got.claims[0]) }, "duplicate claim key"},
		{"missing anchor", func(_ *testing.T, got *manifest, _ map[string]string) { got.claims[0].markerID = "run.finished" }, "anchor occurrences"},
		{"duplicate anchor", func(t *testing.T, got *manifest, docs map[string]string) {
			docs["docs/example.md"] += document
			got.baselines[0].gitBlob = mustGitBlobID(t, docs["docs/example.md"])
		}, "begin anchor occurrences"},
		{"malformed package", func(_ *testing.T, got *manifest, _ map[string]string) {
			got.claims[0].evidencePackage = "internal/example"
		}, "invalid evidence package"},
		{"malformed test", func(_ *testing.T, got *manifest, _ map[string]string) { got.claims[0].evidenceTest = "RunStarted" }, "invalid evidence test"},
		{"no claims", func(_ *testing.T, got *manifest, _ map[string]string) { got.claims = nil }, "claim population is empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := cloneManifest(valid)
			gotDocuments := map[string]string{"docs/example.md": document, "docs/other.md": otherDocument}
			test.mutate(t, &got, gotDocuments)
			err := validateManifest(got, scope, gotDocuments)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestDocumentationClaimEvidenceRequiresObservedPass(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	passing := claim{evidencePackage: "./internal/docclaim/testdata/evidence", evidenceTest: "TestClaimEvidenceFixture"}
	other := claim{evidencePackage: passing.evidencePackage, evidenceTest: "TestOtherClaimEvidenceFixture"}
	if err := runEvidence(repoRoot, []claim{passing, passing, other}); err != nil {
		t.Fatalf("passing evidence rejected: %v", err)
	}

	absent := passing
	absent.evidenceTest = "TestClaimEvidenceDoesNotExist"
	if err := runEvidence(repoRoot, []claim{absent}); err == nil || !strings.Contains(err.Error(), "did not run and pass") {
		t.Fatalf("absent evidence error = %v, want did-not-run failure", err)
	}

	goCache := t.TempDir()
	err := runEvidencePackage(repoRoot, goCache, passing.evidencePackage,
		[]string{other.evidenceTest}, []string{passing.evidenceTest})
	if err == nil || !strings.Contains(err.Error(), other.evidenceTest+" did not run and pass") {
		t.Fatalf("unselected existing evidence error = %v, want exact did-not-run failure", err)
	}

	t.Run("pass without run", func(t *testing.T) {
		output := []byte(`{"Action":"pass","Test":"TestClaimEvidenceFixture"}` + "\n")
		err := requireObservedEvidence(passing.evidencePackage, []string{passing.evidenceTest}, output, nil)
		if err == nil || !strings.Contains(err.Error(), "did not run and pass") {
			t.Fatalf("pass-only evidence error = %v, want did-not-run failure", err)
		}
	})
	t.Run("run without pass", func(t *testing.T) {
		output := []byte(`{"Action":"run","Test":"TestClaimEvidenceFixture"}` + "\n")
		err := requireObservedEvidence(passing.evidencePackage, []string{passing.evidenceTest}, output, nil)
		if err == nil || !strings.Contains(err.Error(), "did not run and pass") {
			t.Fatalf("run-only evidence error = %v, want did-not-pass failure", err)
		}
	})
}

func TestDocumentationClaimEvidenceBatchesByPackageAndCoordinate(t *testing.T) {
	rows := []claim{
		{evidencePackage: "./internal/z", evidenceTest: "TestZ"},
		{evidencePackage: "./internal/a", evidenceTest: "TestB"},
		{documentPath: "docs/one.md", evidencePackage: "./internal/a", evidenceTest: "TestA"},
		{documentPath: "docs/two.md", evidencePackage: "./internal/a", evidenceTest: "TestA"},
	}
	got := evidenceBatches(rows)
	want := []evidenceBatch{
		{packagePath: "./internal/a", testNames: []string{"TestA", "TestB"}},
		{packagePath: "./internal/z", testNames: []string{"TestZ"}},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("evidence batches = %v, want %v", got, want)
	}

	var caches []string
	err := runEvidenceWithRunner("unused", rows, func(_ string, goCache string, _ evidenceBatch) error {
		if info, err := os.Stat(goCache); err != nil || !info.IsDir() {
			t.Fatalf("shared Go cache = %q, stat error = %v", goCache, err)
		}
		caches = append(caches, goCache)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(caches) != 2 || caches[0] != caches[1] {
		t.Fatalf("package command caches = %v, want two commands sharing one cache", caches)
	}
}

func TestDocumentationClaimEvidenceRequiresPackageExitZero(t *testing.T) {
	failing := claim{
		evidencePackage: "./internal/docclaim/testdata/failure",
		evidenceTest:    "TestClaimEvidenceFailureFixture",
	}
	err := runEvidence(filepath.Join("..", ".."), []claim{failing})
	if err == nil || !strings.Contains(err.Error(), "evidence package "+failing.evidencePackage+" failed") {
		t.Fatalf("failing evidence error = %v, want package failure", err)
	}
}

func documentationScope(repoRoot string) ([]string, error) {
	paths := []string{
		"README.md",
		"docs/CLAIMS.md",
		"docs/COMPLETION.md",
		"docs/CONCEPT.md",
		"docs/DESIGN.md",
		"docs/HARNESS.md",
		"docs/MARKERS.md",
	}
	for _, pattern := range []string{"docs/decisions/*.md", "docs/ko/*.md"} {
		matches, err := filepath.Glob(filepath.Join(repoRoot, pattern))
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			relative, err := filepath.Rel(repoRoot, match)
			if err != nil {
				return nil, err
			}
			paths = append(paths, filepath.ToSlash(relative))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func validateManifest(got manifest, scope []string, documents map[string]string) error {
	if len(got.baselines) == 0 && len(got.claims) == 0 {
		return errManifestUnpopulated
	}
	if len(got.baselines) == 0 {
		return errors.New("baseline population is empty")
	}
	if len(got.claims) == 0 {
		return errors.New("claim population is empty")
	}

	wantScope := make(map[string]bool, len(scope))
	for _, path := range scope {
		if wantScope[path] {
			return fmt.Errorf("duplicate scope path %q", path)
		}
		wantScope[path] = true
	}
	baselines := make(map[string]baseline, len(got.baselines))
	for _, row := range got.baselines {
		if baselines[row.documentPath].documentPath != "" {
			return fmt.Errorf("duplicate baseline path %q", row.documentPath)
		}
		if !wantScope[row.documentPath] {
			return fmt.Errorf("baseline path %q is out of scope", row.documentPath)
		}
		if len(row.gitBlob) != 40 {
			return fmt.Errorf("baseline %q has malformed Git blob", row.documentPath)
		}
		decoded, err := hex.DecodeString(row.gitBlob)
		if err != nil || hex.EncodeToString(decoded) != row.gitBlob {
			return fmt.Errorf("baseline %q has malformed Git blob", row.documentPath)
		}
		document, ok := documents[row.documentPath]
		if !ok {
			return fmt.Errorf("baseline %q has no document contents", row.documentPath)
		}
		blob, err := gitBlobID(document)
		if err != nil {
			return fmt.Errorf("calculate baseline %q Git blob: %w", row.documentPath, err)
		}
		if blob != row.gitBlob {
			return fmt.Errorf("baseline %q does not match reviewed document", row.documentPath)
		}
		baselines[row.documentPath] = row
	}
	for path := range wantScope {
		if baselines[path].documentPath == "" {
			return fmt.Errorf("scope path %q has no baseline", path)
		}
	}

	keys := make(map[string]bool, len(got.claims))
	for _, row := range got.claims {
		if baselines[row.documentPath].documentPath == "" {
			return fmt.Errorf("claim document %q has no baseline", row.documentPath)
		}
		if !markerIDPattern.MatchString(row.markerID) {
			return fmt.Errorf("claim marker ID %q is invalid", row.markerID)
		}
		key := row.documentPath + "\x00" + row.markerID
		if keys[key] {
			return fmt.Errorf("duplicate claim key (%q, %q)", row.documentPath, row.markerID)
		}
		keys[key] = true
		anchor := "<!-- partitur:mark begin anchor=" + row.markerID + " -->"
		if count := strings.Count(documents[row.documentPath], anchor); count != 1 {
			return fmt.Errorf("claim (%q, %q) begin anchor occurrences = %d, want 1", row.documentPath, row.markerID, count)
		}
		end := "<!-- partitur:mark end anchor=" + row.markerID + " -->"
		if count := strings.Count(documents[row.documentPath], end); count != 1 {
			return fmt.Errorf("claim (%q, %q) end anchor occurrences = %d, want 1", row.documentPath, row.markerID, count)
		}
		beginIndex := strings.Index(documents[row.documentPath], anchor) + len(anchor)
		endIndex := strings.Index(documents[row.documentPath], end)
		if endIndex < beginIndex || strings.Trim(documents[row.documentPath][beginIndex:endIndex], " \t\r\n") == "" {
			return fmt.Errorf("claim (%q, %q) does not name one non-empty anchor range", row.documentPath, row.markerID)
		}
		if !packagePattern.MatchString(row.evidencePackage) || strings.Contains(row.evidencePackage, "..") {
			return fmt.Errorf("claim (%q, %q) has invalid evidence package %q", row.documentPath, row.markerID, row.evidencePackage)
		}
		if !testNamePattern.MatchString(row.evidenceTest) {
			return fmt.Errorf("claim (%q, %q) has invalid evidence test %q", row.documentPath, row.markerID, row.evidenceTest)
		}
	}
	return nil
}

type evidenceBatch struct {
	packagePath string
	testNames   []string
}

func evidenceBatches(rows []claim) []evidenceBatch {
	byPackage := make(map[string]map[string]bool)
	for _, row := range rows {
		if byPackage[row.evidencePackage] == nil {
			byPackage[row.evidencePackage] = make(map[string]bool)
		}
		byPackage[row.evidencePackage][row.evidenceTest] = true
	}
	packages := make([]string, 0, len(byPackage))
	for packagePath := range byPackage {
		packages = append(packages, packagePath)
	}
	sort.Strings(packages)

	batches := make([]evidenceBatch, 0, len(packages))
	for _, packagePath := range packages {
		testNames := make([]string, 0, len(byPackage[packagePath]))
		for testName := range byPackage[packagePath] {
			testNames = append(testNames, testName)
		}
		sort.Strings(testNames)
		batches = append(batches, evidenceBatch{packagePath: packagePath, testNames: testNames})
	}
	return batches
}

func runEvidence(repoRoot string, rows []claim) error {
	return runEvidenceWithRunner(repoRoot, rows, func(repoRoot, goCache string, batch evidenceBatch) error {
		return runEvidencePackage(repoRoot, goCache, batch.packagePath, batch.testNames, batch.testNames)
	})
}

func runEvidenceWithRunner(repoRoot string, rows []claim, runner func(string, string, evidenceBatch) error) error {
	goCache, err := os.MkdirTemp("", "partitur-docclaim-gocache-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(goCache)

	for _, batch := range evidenceBatches(rows) {
		if err := runner(repoRoot, goCache, batch); err != nil {
			return err
		}
	}
	return nil
}

func runEvidencePackage(repoRoot, goCache, packagePath string, expectedTests, selectedTests []string) error {
	quoted := make([]string, len(selectedTests))
	for i, testName := range selectedTests {
		quoted[i] = regexp.QuoteMeta(testName)
	}
	pattern := "^(" + strings.Join(quoted, "|") + ")$"
	command := exec.Command("go", "test", "-json", "-count=1", "-run", pattern, packagePath)
	command.Dir = repoRoot
	command.Env = append(os.Environ(), "GOCACHE="+goCache)
	output, commandErr := command.CombinedOutput()
	return requireObservedEvidence(packagePath, expectedTests, output, commandErr)
}

func requireObservedEvidence(packagePath string, expectedTests []string, output []byte, commandErr error) error {
	type event struct {
		Action string
		Test   string
	}
	run := make(map[string]bool, len(expectedTests))
	pass := make(map[string]bool, len(expectedTests))
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		var got event
		if json.Unmarshal(scanner.Bytes(), &got) != nil {
			continue
		}
		switch got.Action {
		case "run":
			run[got.Test] = true
		case "pass":
			pass[got.Test] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if commandErr != nil {
		return fmt.Errorf("evidence package %s failed: %w\n%s", packagePath, commandErr, output)
	}
	for _, testName := range expectedTests {
		if !run[testName] || !pass[testName] {
			return fmt.Errorf("evidence %s %s did not run and pass", packagePath, testName)
		}
	}
	return nil
}

func gitBlobID(document string) (string, error) {
	command := exec.Command("git", "hash-object", "--stdin")
	command.Stdin = strings.NewReader(document)
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func mustGitBlobID(t *testing.T, document string) string {
	t.Helper()
	blob, err := gitBlobID(document)
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

func cloneManifest(source manifest) manifest {
	return manifest{
		baselines: append([]baseline(nil), source.baselines...),
		claims:    append([]claim(nil), source.claims...),
	}
}

func readClaimsDocument(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "CLAIMS.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func readCompletionDocument(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "COMPLETION.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func readDocuments(repoRoot string, paths []string) (map[string]string, error) {
	documents := make(map[string]string, len(paths))
	for _, path := range paths {
		contents, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(path)))
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", path, err)
		}
		documents[path] = string(contents)
	}
	return documents, nil
}

func requireOccurrence(t *testing.T, document, value string, want int) {
	t.Helper()
	if got := strings.Count(document, value); got != want {
		t.Fatalf("CLAIMS occurrence of %q = %d, want %d", value, got, want)
	}
}
