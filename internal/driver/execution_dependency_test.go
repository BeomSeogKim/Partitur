package driver

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/validate"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

// TestExecutionDependencyProjectionCompleteness locks A.5's declared field
// names to the projection. It checks names only; conditional presence and
// values remain the responsibility of their behavioural contract tests.
func TestExecutionDependencyProjectionCompleteness(t *testing.T) {
	declared := executionDependencyFieldsFromDesign(t)
	prepared := fanInProjectionFixture(t)
	movement, part, performer, plan, err := selectAttempt(
		prepared.Score, prepared.Cast, "inspect", "worker",
	)
	if err != nil {
		t.Fatal(err)
	}
	compositionHash, err := movementCompositionDependencyHash(movement.ID, "sha256:tree")
	if err != nil {
		t.Fatal(err)
	}
	projection := executionDependencyProjection(
		prepared.Score,
		movement,
		part,
		performer,
		effectiveGrants(movement, prepared.Score.EffectivePolicy()),
		map[string]any{},
		plan.Hash(),
		compositionHash,
	)
	assertDeclaredProjectionFields(t, "A.5", projection, declared.top)
	movementValue, ok := projection["movement"].(map[string]any)
	if !ok {
		t.Fatalf("A.5 movement = %#v, want object", projection["movement"])
	}
	assertDeclaredProjectionFields(t, "A.5 movement", movementValue, declared.movement)
	if got := movementValue["needs"]; !slices.Equal(got.([]any), []any{"prepare"}) {
		t.Fatalf("A.5 movement needs = %#v, want [prepare]", got)
	}
	if got := movementValue["base_composition_hash"]; got != compositionHash {
		t.Fatalf("A.5 movement composition hash = %#v, want %q", got, compositionHash)
	}
}

func TestMovementCompositionContributorsUsePinnedTopologicalDeclarationOrder(t *testing.T) {
	document := writerSliceScore(false)
	document["verification"].(map[string]any)["final_movement"] = "target"
	document["movements"] = []any{
		map[string]any{"id": "z", "part": "reader", "needs": []any{"b"}, "grants": []any{"repo_read", "repo_write"}, "instruction": "z", "outputs": []any{map[string]any{"id": "z-change", "kind": "change_set"}}, "acceptance": map[string]any{"hard": []any{map[string]any{"id": "z-test", "run": []any{"true"}}}}},
		map[string]any{"id": "b", "part": "reader", "grants": []any{"repo_read", "repo_write"}, "instruction": "b", "outputs": []any{map[string]any{"id": "b-change", "kind": "change_set"}}, "acceptance": map[string]any{"hard": []any{map[string]any{"id": "b-test", "run": []any{"true"}}}}},
		map[string]any{"id": "a", "part": "reader", "grants": []any{"repo_read", "repo_write"}, "instruction": "a", "outputs": []any{map[string]any{"id": "a-change", "kind": "change_set"}}, "acceptance": map[string]any{"hard": []any{map[string]any{"id": "a-test", "run": []any{"true"}}}}},
		map[string]any{"id": "target", "part": "reader", "needs": []any{"z", "a"}, "grants": []any{"repo_read"}, "instruction": "target", "outputs": []any{}, "acceptance": map[string]any{"hard": []any{map[string]any{"id": "target-test", "run": []any{"true"}}}}},
	}
	prepared := prepareFixture(t, document)
	state := runstate.State{MovementResults: map[runstate.MovementID]runstate.MovementResult{}, ChangeSets: map[runstate.AttemptID]runstate.ChangeSetRecord{}}
	for _, id := range []string{"a", "b", "z"} {
		attempt, changeSet := runstate.AttemptID("attempt-"+id), "sha256:"+id
		state.MovementResults[runstate.MovementID(id)] = runstate.MovementResult{AttemptID: attempt, ApprovedChangeSetID: changeSet}
		state.ChangeSets[attempt] = runstate.ChangeSetRecord{AttemptID: attempt, ChangeSetID: changeSet, BaseTree: "git-sha1:base", ResultTree: "git-sha1:" + id}
	}
	contributors, err := movementCompositionContributors(prepared.Score, state, "target")
	if err != nil {
		t.Fatal(err)
	}
	got := []runstate.MovementID{contributors[0].MovementID, contributors[1].MovementID, contributors[2].MovementID}
	if want := []runstate.MovementID{"b", "z", "a"}; !slices.Equal(got, want) {
		t.Fatalf("contributor order = %v, want topological declaration order %v", got, want)
	}
	if contributors[0].ChangeSetID != "sha256:b" || contributors[1].BaseTree != "git-sha1:base" {
		t.Fatalf("contributors did not bridge movement result attempt ids to change sets: %+v", contributors)
	}
}

func TestExecutionDependencyHashBindsFanInIdentity(t *testing.T) {
	prepared := fanInProjectionFixture(t)
	movement, part, performer, plan, err := selectAttempt(
		prepared.Score, prepared.Cast, "inspect", "worker",
	)
	if err != nil {
		t.Fatal(err)
	}
	baseCompositionHash, err := movementCompositionDependencyHash(movement.ID, "sha256:tree")
	if err != nil {
		t.Fatal(err)
	}
	hash := func(value []string, composition string) string {
		t.Helper()
		candidate := movement
		candidate.Needs = value
		got, err := executionDependencyHash(
			prepared.Score,
			candidate,
			part,
			performer,
			effectiveGrants(candidate, prepared.Score.EffectivePolicy()),
			map[string]any{},
			plan.Hash(),
			composition,
		)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if hash([]string{"prepare"}, baseCompositionHash) == hash([]string{"other"}, baseCompositionHash) {
		t.Fatal("execution dependency hash does not bind movement needs")
	}
	if hash([]string{"prepare"}, baseCompositionHash) == hash([]string{"prepare"}, "sha256:other-composition") {
		t.Fatal("execution dependency hash does not bind base composition hash")
	}
}

func TestMovementCompositionIdentityForbidsEnvironmentHash(t *testing.T) {
	identity, err := movementCompositionDependencyHash("inspect", "sha256:tree")
	if err != nil {
		t.Fatal(err)
	}
	// This independently constructed map is A.4's specified identity preimage,
	// rather than a mirror of the helper, so protocol-field drift stays visible.
	expected, err := canonical.Hash(canonical.DomainMovementComposition, map[string]any{
		"composition_mode":              "identity",
		"movement_id":                   "inspect",
		"base_tree":                     "sha256:tree",
		"contributors":                  []any{},
		"composition_algorithm_version": float64(canonical.CompositionAlgorithmVersion),
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity != expected {
		t.Fatalf("identity composition hash = %q, want A.4 identity preimage hash %q", identity, expected)
	}
	fabricated, err := canonical.Hash(canonical.DomainMovementComposition, map[string]any{
		"composition_mode":              "identity",
		"movement_id":                   "inspect",
		"base_tree":                     "sha256:tree",
		"contributors":                  []any{},
		"composition_algorithm_version": float64(canonical.CompositionAlgorithmVersion),
		"composition_environment_hash":  "sha256:fabricated",
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity == fabricated {
		t.Fatal("identity composition hash accepted a fabricated environment hash")
	}
	if got, err := movementCompositionDependencyHash("inspect", "sha256:tree"); err != nil || got != identity {
		t.Fatalf("identity composition helper = %q, %v; want %q", got, err, identity)
	}
}

func TestMovementCompositionMergeUsesFullPreDedupContributorPreimage(t *testing.T) {
	contributors := []workspace.CompositionContributor{
		{MovementID: "prepare", ChangeSetID: "sha256:first"},
		{MovementID: "prepare-again", ChangeSetID: "sha256:first"},
	}
	const environment = "sha256:environment"
	got, err := movementCompositionMergeDependencyHash(
		"inspect", "sha256:tree", contributors, environment,
	)
	if err != nil {
		t.Fatal(err)
	}
	// This is independently constructed from A.4's merge preimage. In
	// particular, the duplicate stays in the identity even though the engine
	// applies that change set only once.
	want, err := canonical.Hash(canonical.DomainMovementComposition, map[string]any{
		"composition_mode": "merge",
		"movement_id":      "inspect",
		"base_tree":        "sha256:tree",
		"contributors": []any{
			map[string]any{"movement_id": "prepare", "change_set_id": "sha256:first"},
			map[string]any{"movement_id": "prepare-again", "change_set_id": "sha256:first"},
		},
		"composition_algorithm_version": float64(canonical.CompositionAlgorithmVersion),
		"composition_environment_hash":  environment,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("merge composition hash = %q, want A.4 merge preimage hash %q", got, want)
	}
	withoutDuplicate, err := movementCompositionMergeDependencyHash(
		"inspect", "sha256:tree", contributors[:1], environment,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got == withoutDuplicate {
		t.Fatal("merge composition hash deduplicated its contributor identity")
	}
}

func fanInProjectionFixture(t *testing.T) *validate.Preparation {
	t.Helper()
	document := sliceScore()
	movements := document["movements"].([]any)
	inspect := movements[0].(map[string]any)
	inspect["needs"] = []any{"prepare"}
	prepare := map[string]any{
		"id": "prepare", "part": "reader", "grants": []any{"repo_read"},
		"instruction": "Prepare the report.",
		"outputs":     []any{map[string]any{"id": "prepared-report", "kind": "artifact"}},
		"acceptance": map[string]any{"hard": []any{map[string]any{
			"id": "prepared-report-present", "artifact": "prepared-report",
		}}},
	}
	document["movements"] = []any{prepare, inspect}
	return prepareFixture(t, document)
}

type declaredA5Fields struct {
	top      map[string]bool
	movement map[string]bool
}

var a5FieldLine = regexp.MustCompile(`^\s*([a-z_]+)(\?)?(?::|,|\s|$)`)

func executionDependencyFieldsFromDesign(t *testing.T) declaredA5Fields {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "DESIGN.md")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const heading = "## A.5 The execution-dependency projection"
	if count := strings.Count(string(contents), heading); count != 1 {
		t.Fatalf("A.5 heading count = %d, want 1", count)
	}
	section := string(contents)[strings.Index(string(contents), heading):]
	if next := strings.Index(section[len(heading):], "\n## "); next >= 0 {
		section = section[:len(heading)+next]
	}
	lines := strings.Split(section, "\n")
	fence := -1
	for index, line := range lines {
		if line == "```text" {
			fence = index
			break
		}
	}
	if fence < 0 {
		t.Fatal("A.5 projection text fence is absent")
	}
	end := -1
	for index := fence + 1; index < len(lines); index++ {
		if lines[index] == "```" {
			end = index
			break
		}
	}
	if end < 0 {
		t.Fatal("A.5 projection text fence is unterminated")
	}
	fields := declaredA5Fields{top: map[string]bool{}, movement: map[string]bool{}}
	inMovement := false
	for _, line := range lines[fence+1 : end] {
		if line == "  movement: {" {
			fields.top["movement"] = false
			inMovement = true
			continue
		}
		if inMovement && line == "  }," {
			inMovement = false
			continue
		}
		indent := "  "
		target := fields.top
		if inMovement {
			indent = "    "
			target = fields.movement
		}
		if !strings.HasPrefix(line, indent) || strings.HasPrefix(line, indent+"  ") {
			continue
		}
		match := a5FieldLine.FindStringSubmatch(strings.TrimPrefix(line, indent))
		if match == nil {
			continue
		}
		optional := match[2] == "?" || strings.Contains(line, "omitted")
		target[match[1]] = optional
	}
	if len(fields.top) == 0 || len(fields.movement) == 0 {
		t.Fatalf("A.5 field extraction produced top=%d movement=%d fields", len(fields.top), len(fields.movement))
	}
	return fields
}

func assertDeclaredProjectionFields(t *testing.T, scope string, value map[string]any, declared map[string]bool) {
	t.Helper()
	for name := range value {
		if _, known := declared[name]; !known {
			t.Fatalf("%s projection has undeclared field %q", scope, name)
		}
	}
	for name, optional := range declared {
		if _, present := value[name]; !optional && !present {
			t.Fatalf("%s projection omits declared required field %q", scope, name)
		}
	}
}
