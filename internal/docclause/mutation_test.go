//go:build mutation

package docclause

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationRegionStructuralChecks(t *testing.T) {
	mutations := []struct {
		name, source, before, after, target string
	}{
		{
			name: "document bytes match input blob", source: "internal/docclause/registry.go",
			before: "\tif actual := GitBlobID(document); actual != inputBlob {\n",
			after:  "\tif actual := GitBlobID(document); false && actual != inputBlob { // mutation\n",
			target: "TestRegistryStructuralChecksAndReceiptInvalidation",
		},
		{
			name: "same blob", source: "internal/docclause/regions.go",
			before: "\t\tif region.Key.InputBlob != inputBlob {\n",
			after:  "\t\tif false && region.Key.InputBlob != inputBlob { // mutation\n",
			target: "TestRegionUniverseCompleteness/different_blob",
		},
		{
			name: "gap and overlap", source: "internal/docclause/regions.go",
			before: "\t\tif region.Key.StartLine != expectedStart {\n",
			after:  "\t\tif false && region.Key.StartLine != expectedStart { // mutation\n",
			target: "TestRegionUniverseCompleteness/gap",
		},
		{
			name: "source bytes", source: "internal/docclause/regions.go",
			before: "\t\tif strings.Join(region.Lines, \"\\n\") != strings.Join(wantLines, \"\\n\") {\n",
			after:  "\t\tif false && strings.Join(region.Lines, \"\\n\") != strings.Join(wantLines, \"\\n\") { // mutation\n",
			target: "TestRegionUniverseCompleteness/changed_source_bytes",
		},
		{
			name: "receipt universe key", source: "internal/docclause/registry.go",
			before: "\t\tif !ok {\n\t\t\treturn fmt.Errorf(\"region receipt %s does not match immutable universe\", key)\n\t\t}\n",
			after:  "\t\tif false && !ok { // mutation\n\t\t\treturn fmt.Errorf(\"region receipt %s does not match immutable universe\", key)\n\t\t}\n",
			target: "TestRegistryStructuralChecksAndReceiptInvalidation/registry_mismatch",
		},
		{
			name: "registry input blob", source: "internal/docclause/registry.go",
			before: "\tif registry.InputBlob != inputBlob {\n",
			after:  "\tif false && registry.InputBlob != inputBlob { // mutation\n",
			target: "TestRegistryStructuralChecksAndReceiptInvalidation/input_blob",
		},
		{
			name: "pinned region universe", source: "internal/docclause/registry.go",
			before: "\t\tif registry.RegionUniverse[index] != region.Key {\n",
			after:  "\t\tif false && registry.RegionUniverse[index] != region.Key { // mutation\n",
			target: "TestRegistryStructuralChecksAndReceiptInvalidation/region_universe_key",
		},
		{
			name: "uncovered payload byte", source: "internal/docclause/registry.go",
			before: "\t\tif !asciiWhitespace(value) && coverage[offset] == 0 {\n",
			after:  "\t\tif false && !asciiWhitespace(value) && coverage[offset] == 0 { // mutation\n",
			target: "TestRegistryStructuralChecksAndReceiptInvalidation/uncovered_payload_byte_after_mid_line_end",
		},
		{
			name: "within-line overlap", source: "internal/docclause/registry.go",
			before: "\t\t\tif coverage[offset] != 0 {\n",
			after:  "\t\t\tif false && coverage[offset] != 0 { // mutation\n",
			target: "TestRegistryStructuralChecksAndReceiptInvalidation/within_line_overlap",
		},
		{
			name: "line-boundary-only coverage", source: "internal/docclause/registry.go",
			before: "\t\tif !asciiWhitespace(value) && coverage[offset] == 0 {\n",
			after:  "\t\tif !asciiWhitespace(value) && coverage[offset] == 0 && (offset == 0 || contents[offset-1] == '\\n') { // mutation\n",
			target: "TestRegistryStructuralChecksAndReceiptInvalidation/uncovered_payload_byte_after_mid_line_end",
		},
		{
			name: "materialized classification equality", source: "internal/docclause/activation.go",
			before: "\tif !bytes.Equal(marked, materialized) {\n",
			after:  "\tif false && !bytes.Equal(marked, materialized) { // mutation\n",
			target: "TestActivationRequiresCompleteMaterializedAndPinnedClassification/materialized_bytes",
		},
		{
			name: "marked blob pin", source: "internal/docclause/activation.go",
			before: "\tif registry.Activation.MarkedBlob != markedBlob {\n",
			after:  "\tif false && registry.Activation.MarkedBlob != markedBlob { // mutation\n",
			target: "TestActivationRequiresCompleteMaterializedAndPinnedClassification/marked_blob_pin",
		},
		{
			name: "ordered classification pin", source: "internal/docclause/activation.go",
			before: "\tif registry.Activation.OrderedClassificationSHA256 != digest {\n",
			after:  "\tif false && registry.Activation.OrderedClassificationSHA256 != digest { // mutation\n",
			target: "TestActivationRequiresCompleteMaterializedAndPinnedClassification/classification_pin",
		},
		{
			name: "baseline complete classification byte lock", source: "docs/MARKERS.md",
			before: "| `baseline-complete-classification` | The enrolled blob has a complete ordered classification with `unclassified == ∅`;",
			after:  "| `baseline-complete-classification` | The enrolled blob has an ordered classification with `unclassified == ∅`;",
			target: "TestP3MarkerInvariantIsByteLocked",
		},
		{
			name: "mechanical completion row byte lock", source: "docs/COMPLETION.md",
			before: "its fixed completion predicate is `unclassified == ∅`. The check validates structure",
			after:  "its completion predicate is `unclassified == ∅`. The check validates structure",
			target: "TestP3CompletionRowsAreByteLockedAndCarryNoPendingEnumeration",
		},
		{
			name: "manual completion row byte lock", source: "docs/COMPLETION.md",
			before: "This is manual because a detector would reproduce reviewer errors",
			after:  "This is manual because automation would reproduce reviewer errors",
			target: "TestP3CompletionRowsAreByteLockedAndCarryNoPendingEnumeration",
		},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			runStructuralMutation(t, mutation.source, mutation.before, mutation.after, mutation.target)
		})
	}
}

func runStructuralMutation(t *testing.T, source, before, after, target string) {
	t.Helper()
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve mutation source directory")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	copyRoot := filepath.Join(t.TempDir(), "partitur")
	if err := copyMutationInputs(copyRoot, repository); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(copyRoot, source)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count = %d in %s, want 1", count, source)
	}
	mutated := strings.Replace(string(contents), before, after, 1)
	if err := os.WriteFile(path, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(mutated, after); count != 1 {
		t.Fatalf("mutation injection count = %d in %s, want 1", count, source)
	}
	t.Logf("confirmed injected mutation in %s; target=%s", source, target)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         copyRoot,
		Package:     "./internal/docclause",
		TestPattern: target,
		TestNames:   []string{target},
		Environment: environment.ChildEnvironment(os.Environ()),
	})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
	t.Logf("target ran and mutation was killed: %s", target)
}

func copyMutationInputs(destination, source string) error {
	for _, directory := range []string{"docs", "internal/docclause", "internal/docmarker"} {
		if err := os.MkdirAll(filepath.Join(destination, directory), 0o700); err != nil {
			return err
		}
		if err := os.CopyFS(filepath.Join(destination, directory), os.DirFS(filepath.Join(source, directory))); err != nil {
			return err
		}
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		contents, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(destination, name), contents, 0o600); err != nil {
			return err
		}
	}
	return nil
}
