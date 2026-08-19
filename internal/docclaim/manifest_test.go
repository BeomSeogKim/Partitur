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
	markerIDPattern = regexp.MustCompile(`^[A-Za-z0-9]+(?:[._/-][A-Za-z0-9]+)*$`)
	testNamePattern = regexp.MustCompile(`^Test[A-Za-z0-9_]+$`)
	packagePattern  = regexp.MustCompile(`^\./[A-Za-z0-9_./-]+$`)
)

type baseline struct {
	documentPath string
	gitBlob      string
}

type claim struct {
	documentPath    string
	markerID        string
	evidencePackage string
	evidenceTest    string
}

type manifest struct {
	baselines []baseline
	claims    []claim
}

func TestDocumentationClaimSchemaIsLocked(t *testing.T) {
	document := readClaimsDocument(t)
	requireOccurrence(t, document, "# Documentation claim manifest", 1)
	requireOccurrence(t, document, "## Claim definition", 1)
	requireOccurrence(t, document, "## Scope", 1)
	requireOccurrence(t, document, "## Go registry schema", 1)
	requireOccurrence(t, document, "## Discharge", 1)
	requireOccurrence(t, document, "## Activation boundary", 1)
	requireOccurrence(t, document, "membership in the\nmanifest confers its claim status", 1)
	requireOccurrence(t, document, "A package-level zero with no matching test is a failure.", 1)

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
		{"empty manifest", func(_ *testing.T, got *manifest, _ map[string]string) { *got = manifest{} }, "baseline population is empty"},
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
	passing := claim{evidencePackage: "./internal/docclaim/testdata/evidence", evidenceTest: "TestClaimEvidenceFixture"}
	if err := runEvidence(filepath.Join("..", ".."), passing); err != nil {
		t.Fatalf("passing evidence rejected: %v", err)
	}

	absent := passing
	absent.evidenceTest = "TestClaimEvidenceDoesNotExist"
	if err := runEvidence(filepath.Join("..", ".."), absent); err == nil || !strings.Contains(err.Error(), "did not run and pass") {
		t.Fatalf("absent evidence error = %v, want did-not-run failure", err)
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

func runEvidence(repoRoot string, row claim) error {
	goCache, err := os.MkdirTemp("", "partitur-docclaim-gocache-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(goCache)

	pattern := "^" + regexp.QuoteMeta(row.evidenceTest) + "$"
	command := exec.Command("go", "test", "-json", "-count=1", "-run", pattern, row.evidencePackage)
	command.Dir = repoRoot
	command.Env = append(os.Environ(), "GOCACHE="+goCache)
	output, commandErr := command.CombinedOutput()

	type event struct {
		Action string
		Test   string
	}
	run := false
	pass := false
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		var got event
		if json.Unmarshal(scanner.Bytes(), &got) != nil || got.Test != row.evidenceTest {
			continue
		}
		switch got.Action {
		case "run":
			run = true
		case "pass":
			pass = true
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if commandErr != nil {
		return fmt.Errorf("evidence %s %s failed: %w\n%s", row.evidencePackage, row.evidenceTest, commandErr, output)
	}
	if !run || !pass {
		return fmt.Errorf("evidence %s %s did not run and pass", row.evidencePackage, row.evidenceTest)
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

func requireOccurrence(t *testing.T, document, value string, want int) {
	t.Helper()
	if got := strings.Count(document, value); got != want {
		t.Fatalf("CLAIMS occurrence of %q = %d, want %d", value, got, want)
	}
}
