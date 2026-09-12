package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/docclause"
)

func TestRegenerateLedgerIsDeterministicAndIdempotent(t *testing.T) {
	repository := repositoryRoot(t)
	documentPath := filepath.Join("docs", "DESIGN.md")
	registryPath := filepath.Join("docs", "DESIGN.clause-staging.json")

	first, _, err := regenerateLedger(repository, documentPath, registryPath, "")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := regenerateLedger(repository, documentPath, registryPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("same regeneration inputs produced different ledger bytes")
	}
	want, err := os.ReadFile(filepath.Join(repository, registryPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, want) {
		t.Fatal("no-change regeneration did not reproduce the committed ledger byte-for-byte")
	}
}

func TestRegenerateLedgerRecutsShiftedRegionBoundary(t *testing.T) {
	fixture := newRegenerationFixture(t)
	insertion := []byte("inserted-boundary-line\n")
	newDocument := append(append([]byte(nil), insertion...), fixture.oldDocument...)
	if err := os.WriteFile(fixture.documentPath, newDocument, 0o600); err != nil {
		t.Fatal(err)
	}
	deltaPath := fixture.writeDelta(t, []docclause.Classification{{
		StartByte: 0,
		EndByte:   len(insertion),
		Kind:      docclause.ClassificationNonNormative,
	}})

	contents, _, err := regenerateLedger(fixture.repository, fixture.documentPath, fixture.registryPath, deltaPath)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := decodeRegistry(contents)
	if err != nil {
		t.Fatal(err)
	}
	regions, err := docclause.GenerateRegions(newDocument, docclause.GitBlobID(newDocument))
	if err != nil {
		t.Fatal(err)
	}
	if len(regions) != 2 || regions[0].Key.EndLine != 200 || regions[1].Key.StartLine != 201 {
		t.Fatalf("regenerated universe = %+v, want boundary between new lines 200 and 201", registry.RegionUniverse)
	}
	if got := regions[0].Lines[0]; got != strings.TrimSuffix(string(insertion), "\n") {
		t.Fatalf("first regenerated region begins with %q, want inserted line", got)
	}
	if got := regions[1].Lines[0]; got != "line-200" {
		t.Fatalf("second regenerated region begins with %q, want shifted old boundary line", got)
	}
	for _, receipt := range registry.Receipts {
		if len(receipt.Review.Decisions) != 1 {
			t.Fatalf("region %d has %d decisions, want one maximal range after global merge and boundary split", receipt.Key.Ordinal, len(receipt.Review.Decisions))
		}
	}
	firstRegionBytes := len([]byte(strings.Join(regions[0].Lines, "\n") + "\n"))
	if got := registry.Receipts[1].Review.Decisions[0].StartByte; got != firstRegionBytes {
		t.Fatalf("second region decision starts at byte %d, want shifted boundary %d", got, firstRegionBytes)
	}
}

func TestRegenerateLedgerFailsClosed(t *testing.T) {
	t.Run("dirty marker without replacement delta", func(t *testing.T) {
		fixture := newRegenerationFixture(t)
		fixture.reclassifyAsAnchor(t, "fixture.anchor")
		changed := bytes.Replace(fixture.oldDocument, []byte("line-100"), []byte("line-changed"), 1)
		if err := os.WriteFile(fixture.documentPath, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := regenerateLedger(fixture.repository, fixture.documentPath, fixture.registryPath, "")
		assertErrorContains(t, err, `dirty marker "fixture.anchor" must be re-supplied`)
	})

	t.Run("uncovered new payload", func(t *testing.T) {
		fixture := newRegenerationFixture(t)
		fixture.writeNewDocument(t, "new-payload\n")
		_, _, err := regenerateLedger(fixture.repository, fixture.documentPath, fixture.registryPath, "")
		assertErrorContains(t, err, "uncovered non-whitespace byte ranges")
	})

	t.Run("overlapping delta ranges", func(t *testing.T) {
		fixture := newRegenerationFixture(t)
		fixture.writeNewDocument(t, "new-payload\n")
		deltaPath := fixture.writeDelta(t, []docclause.Classification{
			{StartByte: 0, EndByte: 4, Kind: docclause.ClassificationNonNormative},
			{StartByte: 2, EndByte: 11, Kind: docclause.ClassificationNonNormative},
		})
		_, _, err := regenerateLedger(fixture.repository, fixture.documentPath, fixture.registryPath, deltaPath)
		assertErrorContains(t, err, "delta ranges overlap")
	})

	t.Run("duplicate marker id", func(t *testing.T) {
		fixture := newRegenerationFixture(t)
		fixture.writeNewDocument(t, "ab\n")
		deltaPath := fixture.writeDelta(t, []docclause.Classification{
			{StartByte: 0, EndByte: 1, Kind: docclause.ClassificationAnchor, MarkerID: "duplicate.marker"},
			{StartByte: 1, EndByte: 2, Kind: docclause.ClassificationAnchor, MarkerID: "duplicate.marker"},
		})
		_, _, err := regenerateLedger(fixture.repository, fixture.documentPath, fixture.registryPath, deltaPath)
		assertErrorContains(t, err, `duplicate marker_id "duplicate.marker"`)
	})

	t.Run("anchor without marker id", func(t *testing.T) {
		fixture := newRegenerationFixture(t)
		fixture.writeNewDocument(t, "a\n")
		deltaPath := fixture.writeDelta(t, []docclause.Classification{{
			StartByte: 0,
			EndByte:   1,
			Kind:      docclause.ClassificationAnchor,
		}})
		_, _, err := regenerateLedger(fixture.repository, fixture.documentPath, fixture.registryPath, deltaPath)
		assertErrorContains(t, err, "requires marker_id")
	})

	t.Run("unresolvable registry input blob", func(t *testing.T) {
		fixture := newRegenerationFixture(t)
		contents, err := os.ReadFile(fixture.registryPath)
		if err != nil {
			t.Fatal(err)
		}
		registry, err := decodeRegistry(contents)
		if err != nil {
			t.Fatal(err)
		}
		registry.InputBlob = strings.Repeat("0", 40)
		writeJSON(t, fixture.registryPath, registry)
		_, _, err = regenerateLedger(fixture.repository, fixture.documentPath, fixture.registryPath, "")
		assertErrorContains(t, err, "resolve registry input_blob")
	})
}

type regenerationFixture struct {
	repository   string
	documentPath string
	registryPath string
	oldDocument  []byte
}

func newRegenerationFixture(t *testing.T) regenerationFixture {
	t.Helper()
	repository := t.TempDir()
	command := exec.Command("git", "init", "--quiet")
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	lines := make([]string, 201)
	for index := range lines {
		lines[index] = fmt.Sprintf("line-%03d", index+1)
	}
	oldDocument := []byte(strings.Join(lines, "\n") + "\n")
	command = exec.Command("git", "hash-object", "-w", "--stdin")
	command.Dir = repository
	command.Stdin = bytes.NewReader(oldDocument)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	blob := strings.TrimSpace(string(output))
	documentPath := filepath.Join(repository, "spec.md")
	registryPath := filepath.Join(repository, "ledger.json")
	if err := os.WriteFile(documentPath, oldDocument, 0o600); err != nil {
		t.Fatal(err)
	}
	regions, err := docclause.GenerateRegions(oldDocument, blob)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := buildRegistry(documentPath, blob, regions, []docclause.Classification{{
		StartByte: 0,
		EndByte:   len(oldDocument),
		Kind:      docclause.ClassificationNonNormative,
	}})
	if err != nil {
		t.Fatal(err)
	}
	marked, err := docclause.Materialize(documentPath, oldDocument, blob, regions, registry)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := docclause.ClassificationDigest(documentPath, oldDocument, blob, regions, registry)
	if err != nil {
		t.Fatal(err)
	}
	registry.Activation = &docclause.ActivationPins{
		MarkedBlob:                  docclause.GitBlobID(marked),
		OrderedClassificationSHA256: digest,
	}
	writeJSON(t, registryPath, registry)
	return regenerationFixture{
		repository:   repository,
		documentPath: documentPath,
		registryPath: registryPath,
		oldDocument:  oldDocument,
	}
}

func (fixture regenerationFixture) writeNewDocument(t *testing.T, prefix string) {
	t.Helper()
	contents := append([]byte(prefix), fixture.oldDocument...)
	if err := os.WriteFile(fixture.documentPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (fixture regenerationFixture) writeDelta(t *testing.T, delta []docclause.Classification) string {
	t.Helper()
	path := filepath.Join(fixture.repository, "delta.json")
	writeJSON(t, path, delta)
	return path
}

func (fixture regenerationFixture) reclassifyAsAnchor(t *testing.T, markerID string) {
	t.Helper()
	contents, err := os.ReadFile(fixture.registryPath)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := decodeRegistry(contents)
	if err != nil {
		t.Fatal(err)
	}
	for receiptIndex := range registry.Receipts {
		for decisionIndex := range registry.Receipts[receiptIndex].Review.Decisions {
			decision := &registry.Receipts[receiptIndex].Review.Decisions[decisionIndex]
			decision.Kind = docclause.ClassificationAnchor
			decision.MarkerID = markerID
		}
	}
	regions, err := docclause.GenerateRegions(fixture.oldDocument, registry.InputBlob)
	if err != nil {
		t.Fatal(err)
	}
	registry.Activation = nil
	marked, err := docclause.Materialize(fixture.documentPath, fixture.oldDocument, registry.InputBlob, regions, registry)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := docclause.ClassificationDigest(fixture.documentPath, fixture.oldDocument, registry.InputBlob, regions, registry)
	if err != nil {
		t.Fatal(err)
	}
	registry.Activation = &docclause.ActivationPins{
		MarkedBlob:                  docclause.GitBlobID(marked),
		OrderedClassificationSHA256: digest,
	}
	writeJSON(t, fixture.registryPath, registry)
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
}
