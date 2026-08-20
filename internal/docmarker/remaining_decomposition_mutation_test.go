//go:build mutation

package docmarker

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

func TestMutationRemainingFenceDecomposition(t *testing.T) {
	mutations := []struct {
		name   string
		before string
		after  string
		pkg    string
		target string
	}{
		{
			name:   "repository layout dropped carrier",
			before: "- Only the core writes `<repo>/.partitur/runs/<run-id>/`.\n",
			after:  "<!-- mutation: repository-layout carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestRemainingFenceDecomposition/repository-layout",
		},
		{
			name:   "repository layout reannotated specimen",
			before: "<repo>/\n  partitur.yaml\n",
			after:  "<repo>/\n  partitur.yaml # mutation\n",
			pkg:    "./internal/docmarker",
			target: "TestRemainingFenceDecomposition/repository-layout",
		},
		{
			name:   "score example dropped carrier",
			before: "- `revision` is bumped only by amendments.\n",
			after:  "<!-- mutation: score-example carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestRemainingFenceDecomposition/score-example",
		},
		{
			name:   "score example reannotated specimen",
			before: "score: \"0.2\"\nname: rsvp-deadline-reminders\n",
			after:  "score: \"0.2\" # mutation\nname: rsvp-deadline-reminders\n",
			pkg:    "./internal/docmarker",
			target: "TestRemainingFenceDecomposition/score-example",
		},
		{
			name:   "adapter methods dropped carrier",
			before: "- The adapter never judges success.\n",
			after:  "<!-- mutation: adapter-methods carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestRemainingFenceDecomposition/adapter-methods",
		},
		{
			name:   "adapter methods reannotated specimen",
			before: "probe() -> {\n  protocol: 2,\n",
			after:  "probe() -> {\n  protocol: 2, # mutation\n",
			pkg:    "./internal/docmarker",
			target: "TestRemainingFenceDecomposition/adapter-methods",
		},
		{
			name:   "event notifications dropped carrier",
			before: "- Only the immutable artifact copy is treated as recorded.\n",
			after:  "<!-- mutation: event-notifications carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestRemainingFenceDecomposition/event-notifications",
		},
		{
			name:   "event notifications reannotated specimen",
			before: "artifact { artifact_id, path }\n",
			after:  "artifact { artifact_id, path } # mutation\n",
			pkg:    "./internal/docmarker",
			target: "TestRemainingFenceDecomposition/event-notifications",
		},
		{
			name:   "reserved inputs dropped carrier",
			before: "- `partitur.score-base.base_hash` must equal a proposal's `base_hash`.\n",
			after:  "<!-- mutation: reserved-inputs carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestRemainingFenceDecomposition/reserved-inputs",
		},
		{
			name:   "reserved inputs reannotated specimen",
			before: "artifact_id: partitur.score-base\nkind:        partitur/score-base+json;v=1\n",
			after:  "artifact_id: partitur.score-base # mutation\nkind:        partitur/score-base+json;v=1\n",
			pkg:    "./internal/docmarker",
			target: "TestRemainingFenceDecomposition/reserved-inputs",
		},
		{
			name:   "CLI prose command corrupted",
			before: "- `partitur version` prints the core version and reads or writes no run state.\n",
			after:  "- `partitur versions` prints the core version and reads or writes no run state.\n",
			pkg:    "./internal/runstate",
			target: "TestShippingCommandSurfaceIsSpecified",
		},
	}

	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			runRemainingDecompositionMutation(t, mutation.before, mutation.after, mutation.pkg, mutation.target)
		})
	}
}

func runRemainingDecompositionMutation(t *testing.T, before, after, pkg, target string) {
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
	if err := copyRemainingDecompositionMutationRepository(copyRoot, repository); err != nil {
		t.Fatal(err)
	}

	designPath := filepath.Join(copyRoot, "docs", "DESIGN.md")
	contents, err := os.ReadFile(designPath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(contents), before); count != 1 {
		t.Fatalf("mutation anchor count = %d, want 1", count)
	}
	mutated := strings.Replace(string(contents), before, after, 1)
	if err := os.WriteFile(designPath, []byte(mutated), 0o600); err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(mutated, after); count != 1 {
		t.Fatalf("mutation injection count = %d, want 1", count)
	}
	t.Logf("confirmed injected mutation; target=%s", target)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	result := mutationtest.Run(ctx, mutationtest.Child{
		Dir:         copyRoot,
		Package:     pkg,
		TestPattern: target,
		TestNames:   []string{target},
		Environment: environment.ChildEnvironment(os.Environ()),
	})
	if result.Outcome != mutationtest.Killed {
		t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
	}
	t.Logf("target ran and mutation was killed: %s", target)
}

func copyRemainingDecompositionMutationRepository(destination, source string) error {
	for _, directory := range []string{"docs", "internal"} {
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
