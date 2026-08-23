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
			name:   "routed proposal dropped carrier",
			before: "- `origin` is one of `adapter`, `cli`, or `core_finalization`.\n",
			after:  "<!-- mutation: routed-proposal carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestSevenFenceDecomposition/routed-proposal",
		},
		{
			name:   "routed proposal reannotated specimen",
			before: "  origin,\n  attempt_id?,\n",
			after:  "  origin, # mutation\n  attempt_id?,\n",
			pkg:    "./internal/docmarker",
			target: "TestSevenFenceDecomposition/routed-proposal",
		},
		{
			name:   "amendment proposal relocated carrier dropped",
			before: "2. **Stale re-check** — `base_revision` / `base_hash` must match the snapshot head.\n",
			after:  "<!-- mutation: amendment-proposal relocated carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestSevenFenceDecomposition/amendment-proposal",
		},
		{
			name:   "amendment proposal reannotated specimen",
			before: "  base_revision, base_hash,\n  operations: [...],\n",
			after:  "  base_revision, base_hash, # mutation\n  operations: [...],\n",
			pkg:    "./internal/docmarker",
			target: "TestSevenFenceDecomposition/amendment-proposal",
		},
		{
			name:   "cast duplicate carrier dropped",
			before: "`allow_advisory_enforcement`\ndefaults to `false`;",
			after:  "`allow_advisory_enforcement`\ndefaults to `true`;",
			pkg:    "./internal/docmarker",
			target: "TestSevenFenceDecomposition/cast-schema",
		},
		{
			name:   "cast schema reannotated specimen",
			before: "    allow_advisory_enforcement: false\n    extensions:\n",
			after:  "    allow_advisory_enforcement: false # mutation\n    extensions:\n",
			pkg:    "./internal/docmarker",
			target: "TestSevenFenceDecomposition/cast-schema",
		},
		{
			name:   "application candidate dropped carrier",
			before: "- The candidate additionally records `candidate_composition_dependency_hash` (A.4).\n",
			after:  "<!-- mutation: application-candidate carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestSevenFenceDecomposition/application-candidate",
		},
		{
			name:   "application candidate reannotated specimen",
			before: "candidate_id = H(\"partitur/candidate\",\n",
			after:  "candidate_id = H(\"partitur/candidate\", # mutation\n",
			pkg:    "./internal/docmarker",
			target: "TestSevenFenceDecomposition/application-candidate",
		},
		{
			name:   "actual impact dropped carrier",
			before: "- `actual_impact.authority.side_effects.added` must be empty in v0.2.\n",
			after:  "<!-- mutation: actual-impact carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestSevenFenceDecomposition/actual-impact",
		},
		{
			name:   "actual impact reannotated specimen",
			before: "    side_effects: {added: [...], removed: [...]}\n",
			after:  "    side_effects: {added: [...], removed: [...]} # mutation\n",
			pkg:    "./internal/docmarker",
			target: "TestSevenFenceDecomposition/actual-impact",
		},
		{
			name:   "execution dependency dropped causal carrier",
			before: "- Binding only the logical artifact id would let those two attempts share an execution-dependency\n  hash.\n",
			after:  "<!-- mutation: execution-dependency causal carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestSevenFenceDecomposition/execution-dependency",
		},
		{
			name:   "execution dependency reannotated specimen",
			before: "  actual_adapter_id,\n  movement: {\n",
			after:  "  actual_adapter_id, # mutation\n  movement: {\n",
			pkg:    "./internal/docmarker",
			target: "TestSevenFenceDecomposition/execution-dependency",
		},
		{
			name:   "global invariants dropped causal carrier",
			before: "- Hashing raw `policy.allowed_paths` here would reintroduce the narrowing problem A.5 avoids.\n",
			after:  "<!-- mutation: global-invariants causal carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestSevenFenceDecomposition/global-invariants",
		},
		{
			name:   "global invariants reannotated specimen",
			before: "  resolved_questions: [\n",
			after:  "  resolved_questions: [ # mutation\n",
			pkg:    "./internal/docmarker",
			target: "TestSevenFenceDecomposition/global-invariants",
		},
		{
			name:   "owned Git refs dropped carrier",
			before: "- `refs/partitur/runs/<run-id>/candidate` points to the candidate result tree (§8).\n",
			after:  "<!-- mutation: owned-git-refs carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestSurveyFenceDecomposition/owned-git-refs",
		},
		{
			name:   "owned Git refs reannotated specimen",
			before: "refs/partitur/runs/<run-id>/base\nrefs/partitur/runs/<run-id>/attempts/<attempt-id>/changeset\n",
			after:  "refs/partitur/runs/<run-id>/base # mutation\nrefs/partitur/runs/<run-id>/attempts/<attempt-id>/changeset\n",
			pkg:    "./internal/docmarker",
			target: "TestSurveyFenceDecomposition/owned-git-refs",
		},
		{
			name:   "may propose relocated carrier dropped",
			before: "- `partitur.score-base` is the reserved input for proposal-capable movements.\n",
			after:  "<!-- mutation: may-propose relocated carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestSurveyFenceDecomposition/may-propose",
		},
		{
			name:   "may propose reannotated specimen",
			before: "    may_propose: true\n",
			after:  "    may_propose: true # mutation\n",
			pkg:    "./internal/docmarker",
			target: "TestSurveyFenceDecomposition/may-propose",
		},
		{
			name:   "inapplicable state dropped carrier",
			before: "- An `INAPPLICABLE` movement is never scheduled.\n",
			after:  "<!-- mutation: inapplicable-state carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestSurveyFenceDecomposition/inapplicable-state",
		},
		{
			name:   "inapplicable state reannotated specimen",
			before: "          | INAPPLICABLE\nAttempt:",
			after:  "          | INAPPLICABLE # mutation\nAttempt:",
			pkg:    "./internal/docmarker",
			target: "TestSurveyFenceDecomposition/inapplicable-state",
		},
		{
			name:   "CLI surface dropped carrier",
			before: "- `partitur amend --patch <path>` accepts RFC 6902 JSON, and `-` reads it from stdin.\n",
			after:  "<!-- mutation: CLI-surface carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestSurveyFenceDecomposition/cli-surface",
		},
		{
			name:   "CLI surface reannotated specimen",
			before: "partitur amend   [<run-id>] --patch <path>\n",
			after:  "partitur amend   [<run-id>] --patch <path> # mutation\n",
			pkg:    "./internal/docmarker",
			target: "TestSurveyFenceDecomposition/cli-surface",
		},
		{
			name:   "acceptance spec dropped carrier",
			before: "- `human_gate` is always explicit and is never omitted.\n",
			after:  "<!-- mutation: acceptance-spec carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestSurveyFenceDecomposition/acceptance-spec",
		},
		{
			name:   "acceptance spec reannotated specimen",
			before: "  human_gate: \"always\" | \"on_contested\" | \"never\"\n",
			after:  "  human_gate: \"always\" | \"on_contested\" | \"never\" # mutation\n",
			pkg:    "./internal/docmarker",
			target: "TestSurveyFenceDecomposition/acceptance-spec",
		},
		{
			name:   "disposition dropped carrier",
			before: "- `terminal_reason` is a `movement.failed` reason from Appendix D selected by §3.1's first arm.\n",
			after:  "<!-- mutation: disposition carrier dropped -->\n",
			pkg:    "./internal/docmarker",
			target: "TestSurveyFenceDecomposition/disposition",
		},
		{
			name:   "disposition reannotated specimen",
			before: "  terminal_reason?\n",
			after:  "  terminal_reason? # mutation\n",
			pkg:    "./internal/docmarker",
			target: "TestSurveyFenceDecomposition/disposition",
		},
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
			name:   "event notifications dropped relocated carrier",
			before: "**Amendment format v0.2.** A `proposal` carries:\n",
			after:  "<!-- mutation: event-notifications relocated carrier dropped -->\n",
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
