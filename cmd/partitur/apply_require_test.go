package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/acceptance"
	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

// applyGate is the part of a non-waived score the apply judgment reads, plus the
// review evidence the run records under it.
type applyGate struct {
	require       []string
	predicates    []string
	reviewOutcome string
	// humanGate is the movement's declared gate mode; approved evidence needs
	// "always", and rule 11 refuses `require: [approved]` without it.
	humanGate string
	// resolveGate journals an approved human-gate resolution. With a CONTESTED
	// review it overrides the raised blocker, which is the only way review_outcome
	// reaches OVERRIDDEN.
	resolveGate bool
	// gateSubjectIsBase binds that resolution to the base tree instead of the
	// candidate's result tree, so its scope no longer covers what is applied.
	gateSubjectIsBase bool
	// evidenceSubjectIsBase binds the whole acceptance to the base tree, which
	// detaches every mark from the candidate at once.
	evidenceSubjectIsBase bool
	// plannedCriterionIDs overrides the journaled plan. It is intentionally
	// available only to the malformed-evidence apply oracles below.
	plannedCriterionIDs []string
	// declaredArtifactHard adds a second declared hard check, so the oracle can
	// prove coverage for artifact criteria independently of hard.run.
	declaredArtifactHard bool
	// acceptanceSpecHash overrides the journaled hash for the malformed-evidence
	// oracle that proves the score-plan correspondence is checked.
	acceptanceSpecHash string
}

// applyRequireFixture builds a SUCCEEDED run whose candidate materializes one
// file, under a non-waived apply gate. Every test below drives `apply` through
// the CLI so the judgment is observed by its effect on the checkout, never by
// calling the judgment directly.
func applyRequireFixture(t *testing.T, gate applyGate) (string, *runstore.Store, string) {
	t.Helper()
	return applyRequireFixtureWithFiles(t, gate, applyFixtureCandidateFiles)
}

// applyRequireFixtureWithNestedPath puts one touched path inside a directory, so
// a rollback has a parent it could be tricked into following out of the checkout.
func applyRequireFixtureWithNestedPath(t *testing.T) (string, *runstore.Store, string) {
	t.Helper()
	files := append(slices.Clone(applyFixtureCandidateFiles),
		applyFixtureFile{name: "nested/file.txt", contents: "nested result\n"})
	return applyRequireFixtureWithFiles(t, applyGate{require: []string{"verified"}}, files)
}

func applyRequireFixtureWithFiles(t *testing.T, gate applyGate, files []applyFixtureFile) (string, *runstore.Store, string) {
	t.Helper()
	root, store := resumeFixtureWithInputs(
		t,
		"",
		applyRequireScore(gate),
		[]byte("cast: \"0.1\"\nperformers:\n  reviewer:\n    adapter: adapter\n    model: model\nbindings:\n  reviewer:\n    performer: reviewer\n"),
	)
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	baseTree, baseCommit := input.BaseTree, input.BaseCommit
	resultTree := applyFixtureResultTree(t, root, baseCommit, files)
	candidateID := appendApplyRequireRun(t, store, baseTree, resultTree, gate)
	return root, store, candidateID
}

// applyFixtureFile is one path the candidate would write into the checkout.
type applyFixtureFile struct {
	name     string
	contents string
	inBase   bool
}

// applyFixtureCandidateFiles is the default candidate: two additions and one
// modification of a path the base already carries, so a rollback has both kinds
// of touched path to restore.
var applyFixtureCandidateFiles = []applyFixtureFile{
	{name: "applied.txt", contents: "candidate result\n"},
	{name: "second.txt", contents: "second result\n"},
	{name: resumeFixtureBaseFile, contents: "modified by the candidate\n", inBase: true},
}

// applyFixtureResultTree commits the candidate's files to learn their tree, then
// restores the checkout to the base so `apply` has real work to do.
func applyFixtureResultTree(t *testing.T, root, baseCommit string, files []applyFixtureFile) string {
	t.Helper()
	// Two paths, so a failed apply can be checked for having left the *other*
	// one alone rather than only the path a test happened to sabotage.
	for _, file := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, file.name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, file.name), []byte(file.contents), 0o600); err != nil {
			t.Fatal(err)
		}
		// Add by name: `-A` would track .partitur, which the reset below then wipes.
		runGit(t, root, "add", file.name)
	}
	runGit(t, root, "commit", "--quiet", "-m", "candidate")
	resultTree := "git-" + applyFixtureGit(t, root, "rev-parse", "--show-object-format") + ":" +
		applyFixtureGit(t, root, "rev-parse", "HEAD^{tree}")
	_, baseObject, ok := strings.Cut(baseCommit, ":")
	if !ok {
		t.Fatalf("base commit %q is not qualified", baseCommit)
	}
	runGit(t, root, "reset", "--hard", "--quiet", baseObject)
	for _, file := range files {
		if file.inBase {
			// The reset already restored it; removing it would dirty the checkout.
			continue
		}
		if err := os.Remove(filepath.Join(root, file.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte(".partitur/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return resultTree
}

func applyFixtureGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return strings.TrimSpace(string(output))
}

func (gate applyGate) gateMode() string {
	if gate.humanGate == "" {
		return "on_contested"
	}
	return gate.humanGate
}

// reviewCriterion is declared only where the gate needs it. A declared review
// criterion makes `status` demand the findings artifact behind the REVIEWED
// mark, which a gate that never asks for review has no reason to produce.
func (gate applyGate) reviewCriterion() string {
	if len(gate.predicates) == 0 && !slices.Contains(gate.require, "reviewed") {
		return ""
	}
	return `      review:
        - id: goal-review
          findings: findings
          rubric: [requirement_coverage]
`
}

func applyRequireScore(gate applyGate) []byte {
	return []byte(fmt.Sprintf(`score: "0.2"
name: apply-require-fixture
revision: 1
status: finalized
goal: fixture
verification:
  expectation:
    intent: pass-existing-tests
    apply_gate:
      require: [%s]
      predicates: [%s]
  final_movement: check
parts:
  reviewer:
    capabilities: [repo_read]
movements:
  - id: check
    part: reviewer
    grants: [repo_read]
    instruction: inspect
    outputs:
      - id: findings
        kind: findings
    acceptance:
      hard:
        - id: command-passes
          run: ["true"]
%s
%s      human_gate: %s
policy:
  allowed_paths: ["**"]
  budget:
    active_wall_clock_min: 10
`, strings.Join(gate.require, ", "), strings.Join(gate.predicates, ", "), gate.artifactCriterion(), gate.reviewCriterion(), gate.gateMode()))
}

func (gate applyGate) artifactCriterion() string {
	if !gate.declaredArtifactHard {
		return ""
	}
	return `        - id: findings-present
          artifact: findings
`
}

// appendApplyRequireRun journals the run that earns the marks: one final
// movement whose acceptance binds to the candidate's result tree.
func appendApplyRequireRun(t *testing.T, store *runstore.Store, baseTree, resultTree string, gate applyGate) string {
	t.Helper()
	compositionHash, err := workspace.CandidateCompositionHash(baseTree, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	candidateID, err := canonical.Hash(canonical.DomainCandidate, map[string]any{
		"base_tree": baseTree, "result_tree": resultTree, "ordered_change_sets": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	versions := resumeIdentityVersions()
	movement := func(eventType runstate.EventType, payload map[string]any) runstate.Event {
		return runstate.Event{RunID: "run-1", ScoreRevision: 1, MovementID: "check", Type: eventType, Payload: resumePayload(t, payload)}
	}
	attempt := func(eventType runstate.EventType, payload map[string]any) runstate.Event {
		event := movement(eventType, payload)
		event.AttemptID = "attempt-1"
		return event
	}
	evidenceTree := resultTree
	if gate.evidenceSubjectIsBase {
		evidenceTree = baseTree
	}
	gateTree := evidenceTree
	if gate.gateSubjectIsBase {
		gateTree = baseTree
	}
	plannedCriterionIDs := gate.plannedCriterionIDs
	if plannedCriterionIDs == nil {
		plannedCriterionIDs = []string{"command-passes"}
	}
	acceptanceSpecHash := applyRequireAcceptanceSpecHash(t, gate)
	if gate.acceptanceSpecHash != "" {
		acceptanceSpecHash = gate.acceptanceSpecHash
	}
	criterionOutcomes := make([]any, len(plannedCriterionIDs))
	for index, id := range plannedCriterionIDs {
		criterionOutcomes[index] = map[string]any{
			"criterion_id": id, "criterion_spec_hash": "sha256:criterion", "outcome": "PASS",
		}
	}
	evaluation := map[string]any{
		"subject_tree": evidenceTree, "acceptance_spec_hash": acceptanceSpecHash, "identity_versions": versions,
		"criterion_outcomes": criterionOutcomes,
	}
	blockers := []any{}
	if gate.reviewOutcome == "CONTESTED" {
		blockers = []any{map[string]any{"artifact_instance_id": "findings@attempt-1", "finding_id": "finding-1"}}
	}
	if gate.reviewOutcome != "" {
		evaluation["review_outcome"] = gate.reviewOutcome
		evaluation["blocking_findings"] = blockers
	}
	events := []runstate.Event{
		{RunID: "run-1", ScoreRevision: 1, Type: runstate.EventApplicationCandidateRecorded, Payload: resumePayload(t, map[string]any{
			"candidate_id": candidateID, "base_tree": baseTree, "result_tree": resultTree,
			"ordered_change_sets": []any{}, "contributors": []any{},
			"candidate_composition_dependency_hash": compositionHash, "identity_versions": versions,
		})},
		movement(runstate.EventMovementReady, map[string]any{}),
		movement(runstate.EventMovementStarted, map[string]any{}),
		attempt(runstate.EventPerformerSelected, map[string]any{"reason": "initial", "performer_id": "reviewer", "adapter_id": "adapter", "model": "model"}),
		attempt(runstate.EventAttemptStarted, map[string]any{
			"attempt_number": 1,
			"adapter_process": map[string]any{"pid": 999999, "session_id": 999999,
				"start_identity": map[string]any{"platform": "linux", "boot_id": "fixture", "start_ticks": "0"}},
			"granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{"**"}, "shell": false, "network": false},
			"identity_versions": versions,
		}),
		attempt(runstate.EventAdapterProbed, map[string]any{
			"adapter_version":     "1",
			"capabilities":        map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false},
			"enforcement":         map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true},
			"negotiated_features": []any{}, "truncated_resolutions": []any{}, "delivered_resolutions": []any{},
			"delivered_feedback": []any{}, "advisory_dimensions": []any{},
			"execution_dependency_hash": "sha256:dependency", "identity_versions": versions,
		}),
		attempt(runstate.EventPerformerCompleted, map[string]any{"session_hint_stored": false}),
		attempt(runstate.EventVerificationPassed, map[string]any{}),
		attempt(runstate.EventAcceptanceStarted, map[string]any{
			"subject_tree": evidenceTree, "acceptance_spec_hash": acceptanceSpecHash,
			"planned_criterion_ids": applyRequireStringsToAny(plannedCriterionIDs), "identity_versions": versions,
		}),
	}
	for _, id := range plannedCriterionIDs {
		events = append(events,
			attempt(runstate.EventCriterionStarted, map[string]any{
				"criterion_id": id, "criterion_spec_hash": "sha256:criterion",
				"subject_tree": evidenceTree, "identity_versions": versions,
			}),
			attempt(runstate.EventCriterionCompleted, map[string]any{
				"criterion_id": id, "criterion_spec_hash": "sha256:criterion",
				"subject_tree": evidenceTree, "outcome": "PASS", "identity_versions": versions,
			}),
		)
	}
	events = append(events, attempt(runstate.EventAcceptanceEvaluationCompleted, evaluation))
	if gate.resolveGate {
		request := map[string]any{
			"decision_id": "decision-1", "decision_type": "human_gate", "gate_id": "gate-1",
			"gate_mode": gate.gateMode(), "subject_tree": gateTree, "blocking_findings": blockers,
		}
		if gate.reviewOutcome != "" {
			request["review_outcome"] = gate.reviewOutcome
		}
		resolution := map[string]any{
			"decision_id": "decision-1", "decision_type": "human_gate", "gate_id": "gate-1",
			"disposition": "approved", "scope": map[string]any{"subject_tree": gateTree},
			"overridden_findings": blockers,
		}
		if len(blockers) != 0 {
			resolution["override_reason"] = "fixture override"
		}
		events = append(events,
			attempt(runstate.EventDecisionRequested, request),
			attempt(runstate.EventDecisionResolved, resolution),
		)
	}
	events = append(events,
		attempt(runstate.EventAttemptCompleted, map[string]any{}),
		attempt(runstate.EventMovementSucceeded, map[string]any{
			"approved_artifact_instance_ids": []any{}, "identity_versions": versions, "run_succeeded": true,
		}),
	)
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		for _, event := range events {
			if _, err := tx.At("fixture.require").Append(event); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return candidateID
}

func applyRequireStringsToAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func applyRequireAcceptanceSpecHash(t *testing.T, gate applyGate) string {
	t.Helper()
	compiled, diagnostics := score.Compile(applyRequireScore(gate))
	if len(diagnostics) != 0 {
		t.Fatalf("score diagnostics=%v", diagnostics)
	}
	view, ok := applicationMovementForTest(compiled, "check")
	if !ok {
		t.Fatal("check movement is missing")
	}
	plan, err := acceptance.Compile(view)
	if err != nil {
		t.Fatal(err)
	}
	return string(plan.Hash())
}

func applicationMovementForTest(compiled *score.Score, id string) (score.MovementView, bool) {
	for _, movement := range compiled.Movements() {
		if movement.ID == id {
			return movement, true
		}
	}
	return score.MovementView{}, false
}

// applyRequireCheckout runs `apply` and reports the exit code alongside what the
// checkout holds afterwards, which is the only evidence that separates a granted
// judgment from a refused one.
func applyRequireCheckout(t *testing.T, root string) (int, string, string) {
	t.Helper()
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", "run-1"}, &stdout, &stderr)
	if stdout.Len() != 0 {
		t.Fatalf("apply wrote stdout=%q", stdout.String())
	}
	contents, err := os.ReadFile(filepath.Join(root, "applied.txt"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return code, string(contents), stderr.String()
}

func TestApplyRequireVerifiedMaterializesCandidate(t *testing.T) {
	root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
	code, contents, stderr := applyRequireCheckout(t, root)
	if code != 0 || contents != "candidate result\n" || stderr != "" {
		t.Fatalf("apply exit=%d contents=%q stderr=%q", code, contents, stderr)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if tail := journal.Events[len(journal.Events)-2:]; tail[0].Type != runstate.EventApplyStarted || tail[1].Type != runstate.EventApplyCompleted {
		t.Fatalf("application journal tail=%v", eventKinds(tail))
	}
}

func TestApplyRequireVerifiedRefusesAcceptancePlansMissingDeclaredHardCriteria(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		gate       applyGate
		wantDetail string
	}{
		{
			name:       "acceptance hash does not match the final movement",
			gate:       applyGate{require: []string{"verified"}, acceptanceSpecHash: "sha256:not-the-final-movement"},
			wantDetail: "acceptance spec hash does not match the final movement",
		},
		{
			name:       "empty plan omits the declared run criterion",
			gate:       applyGate{require: []string{"verified"}, plannedCriterionIDs: []string{}},
			wantDetail: `declared hard criterion \"command-passes\" is missing from the acceptance plan`,
		},
		{
			name:       "plan names only an undeclared criterion",
			gate:       applyGate{require: []string{"verified"}, plannedCriterionIDs: []string{"not-declared"}},
			wantDetail: `declared hard criterion \"command-passes\" is missing from the acceptance plan`,
		},
		{
			name: "plan omits a declared artifact criterion",
			gate: applyGate{
				require: []string{"verified"}, declaredArtifactHard: true,
				plannedCriterionIDs: []string{"command-passes"},
			},
			wantDetail: `declared hard criterion \"findings-present\" is missing from the acceptance plan`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root, store, _ := applyRequireFixture(t, testCase.gate)
			code, contents, stderr := applyRequireCheckout(t, root)
			if code != 2 || contents != "" || !strings.Contains(stderr, testCase.wantDetail) {
				t.Fatalf("apply exit=%d contents=%q stderr=%q", code, contents, stderr)
			}
			if status := applyFixtureGit(t, root, "status", "--porcelain=v1", "--untracked-files=all"); status != "" {
				t.Fatalf("refused apply changed the checkout: %q", status)
			}
			journal, err := store.ReadJournal("run-1")
			if err != nil {
				t.Fatal(err)
			}
			if countEvents(journal.Events, runstate.EventApplyStarted) != 0 {
				t.Fatal("refused apply opened a transaction")
			}
		})
	}
}

func TestApplyRequireReviewedAndPredicateMaterializeCandidate(t *testing.T) {
	root, _, _ := applyRequireFixture(t, applyGate{
		require:       []string{"verified", "reviewed"},
		predicates:    []string{"no_unresolved_blocking_findings", "no_blocking_findings"},
		reviewOutcome: "CLEAN",
	})
	code, contents, stderr := applyRequireCheckout(t, root)
	if code != 0 || contents != "candidate result\n" || stderr != "" {
		t.Fatalf("apply exit=%d contents=%q stderr=%q", code, contents, stderr)
	}
}

// The two predicates differ only on an overridden blocker, so a run whose
// blockers were all overridden separates them: the lenient one passes.
func TestApplyRequireOverriddenBlockersSatisfyUnresolvedPredicate(t *testing.T) {
	root, _, _ := applyRequireFixture(t, applyGate{
		require:       []string{"verified", "reviewed"},
		predicates:    []string{"no_unresolved_blocking_findings"},
		reviewOutcome: "CONTESTED",
		resolveGate:   true,
	})
	code, contents, stderr := applyRequireCheckout(t, root)
	if code != 0 || contents != "candidate result\n" || stderr != "" {
		t.Fatalf("apply exit=%d contents=%q stderr=%q", code, contents, stderr)
	}
}

func TestApplyRequireOverriddenBlockersRefuseStrictPredicate(t *testing.T) {
	root, store, _ := applyRequireFixture(t, applyGate{
		require:       []string{"verified", "reviewed"},
		predicates:    []string{"no_blocking_findings"},
		reviewOutcome: "CONTESTED",
		resolveGate:   true,
	})
	code, contents, stderr := applyRequireCheckout(t, root)
	if code != 2 || contents != "" || !strings.Contains(stderr, "no_blocking_findings is not satisfied") {
		t.Fatalf("apply exit=%d contents=%q stderr=%q", code, contents, stderr)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventApplyStarted) != 0 {
		t.Fatal("refused apply opened a transaction")
	}
}

// An unresolved blocker fails both predicates: the run is CONTESTED with no
// override, so nothing distinguishes it from a failed review.
func TestApplyRequireUnresolvedBlockersRefuseBothPredicates(t *testing.T) {
	for _, predicate := range []string{"no_unresolved_blocking_findings", "no_blocking_findings"} {
		t.Run(predicate, func(t *testing.T) {
			root, _, _ := applyRequireFixture(t, applyGate{
				require:       []string{"verified", "reviewed"},
				predicates:    []string{predicate},
				reviewOutcome: "CONTESTED",
			})
			code, contents, stderr := applyRequireCheckout(t, root)
			if code != 2 || contents != "" || !strings.Contains(stderr, predicate+" is not satisfied") {
				t.Fatalf("apply exit=%d contents=%q stderr=%q", code, contents, stderr)
			}
		})
	}
}

// An approved gate is the one grade a compiling score can withhold: rule 11 only
// forces `human_gate: always`, never the resolution that satisfies it.
func TestApplyRequireApprovedGrantsAndWithholds(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		gate        applyGate
		wantCode    int
		wantStderr  string
		wantApplied string
	}{
		{
			name:        "resolved gate covers the candidate",
			gate:        applyGate{require: []string{"verified", "approved"}, humanGate: "always", resolveGate: true},
			wantCode:    0,
			wantApplied: "candidate result\n",
		},
		{
			name:       "no resolution at all",
			gate:       applyGate{require: []string{"verified", "approved"}, humanGate: "always"},
			wantCode:   2,
			wantStderr: "required approved evidence is absent",
		},
		{
			name: "resolution scoped to the base tree",
			gate: applyGate{
				require: []string{"verified", "approved"}, humanGate: "always",
				resolveGate: true, gateSubjectIsBase: true,
			},
			wantCode:   2,
			wantStderr: "required approved evidence is absent",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root, _, _ := applyRequireFixture(t, testCase.gate)
			code, contents, stderr := applyRequireCheckout(t, root)
			if code != testCase.wantCode || contents != testCase.wantApplied ||
				!strings.Contains(stderr, testCase.wantStderr) {
				t.Fatalf("apply exit=%d contents=%q stderr=%q", code, contents, stderr)
			}
		})
	}
}

// Acceptance bound to the base tree is evidence about something other than what
// `apply` would write, so every grade drops at once.
func TestApplyRequireEvidenceOffTheCandidateRefuses(t *testing.T) {
	root, store, _ := applyRequireFixture(t, applyGate{
		require:               []string{"verified", "reviewed"},
		reviewOutcome:         "CLEAN",
		evidenceSubjectIsBase: true,
	})
	code, contents, stderr := applyRequireCheckout(t, root)
	if code != 2 || contents != "" || !strings.Contains(stderr, "required verified evidence is absent") {
		t.Fatalf("apply exit=%d contents=%q stderr=%q", code, contents, stderr)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventApplyStarted) != 0 {
		t.Fatal("refused apply opened a transaction")
	}
}

// A candidate that cannot be laid down must fail cleanly: DESIGN §8 step 3
// restores the touched paths to the base tree and re-verifies the base hash,
// and exit 5 belongs to `--recover` alone.
func TestApplySabotagedPatchFailsCleanly(t *testing.T) {
	root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
	// The candidate creates applied.txt. Squat that path with different content,
	// ignored so the computed worktree tree still equals the candidate base and
	// the checkout still reads as clean.
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte(".partitur/\napplied.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "applied.txt"), []byte("squatter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, contents, stderr := applyRequireCheckout(t, root)
	if code != 4 || contents != "squatter\n" {
		t.Fatalf("apply exit=%d contents=%q stderr=%q", code, contents, stderr)
	}
	// The other touched path is the one the sabotage did not name: a rollback
	// that only undid the failing hunk would leave it behind.
	if _, err := os.Stat(filepath.Join(root, "second.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second touched path survived the failed apply: %v", err)
	}
	// Re-verify the base from disk, not from the exit code: nothing tracked may
	// differ, so the checkout still computes to the candidate's base tree.
	if status := applyFixtureGit(t, root, "status", "--porcelain=v1", "--untracked-files=all"); status != "" {
		t.Fatalf("checkout is not back at the base: %q", status)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventApplyStarted) != 1 ||
		countEvents(journal.Events, runstate.EventApplyFailed) != 1 ||
		countEvents(journal.Events, runstate.EventApplyRecoveryRequired) != 0 {
		t.Fatalf("journal=%v", eventKinds(journal.Events))
	}
	// No temporary index may outlive a failed apply either.
	leftovers, err := filepath.Glob(filepath.Join(root, ".git", "partitur-apply-index-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary indexes=%v err=%v", leftovers, err)
	}
	// FAILED_CLEAN is a normal apply entry state. Once the obstruction is gone,
	// retrying must be able to materialize the same candidate.
	if err := os.Remove(filepath.Join(root, "applied.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte(".partitur/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, contents, stderr = applyRequireCheckout(t, root)
	if code != 0 || contents != "candidate result\n" || stderr != "" {
		t.Fatalf("retry after failed-clean exit=%d contents=%q stderr=%q", code, contents, stderr)
	}
}

// TestApplyBeforeItsTransactionRefusesRatherThanPromisingRecovery pins the
// boundary the exit table draws. Code 6 promises a continuation — "Application
// remains APPLYING and recoverable with apply --recover" — so a failure before
// apply.started is durable may not claim it: the projection is still
// NOT_APPLIED and recovery correctly refuses, leaving the caller with an
// instruction that cannot work.
func TestApplyBeforeItsTransactionRefusesRatherThanPromisingRecovery(t *testing.T) {
	root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
	// Take write permission off .git, so the first fallible step — computing the
	// checkout's tree through a temporary index — fails with nothing recorded.
	if err := os.Chmod(filepath.Join(root, ".git"), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, ".git"), 0o700) })

	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", "run-1"}, &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "precondition refused") {
		t.Fatalf("pre-transaction failure exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventApplyStarted) != 0 {
		t.Fatalf("a refused apply opened a transaction: %v", journal.Events)
	}
	// The continuation exit 6 would have named is genuinely unusable from here,
	// which is why naming it would have been the defect rather than a nicety.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"apply", "run-1", "--recover"}, &stdout, &stderr); code != 2 ||
		!strings.Contains(stderr.String(), "--recover is refused from") {
		t.Fatalf("recover from a never-started transaction exit=%d stderr=%q", code, stderr.String())
	}
}

// TestApplyRefusesWhenItsFirstAppendLeavesNothing pins the other side of the
// exit-6 boundary by measuring instead of reasoning. A failed append may or may
// not have put its line on disk, and no reasoning about where the failure
// happened can settle that — so the outcome comes from re-reading the
// projection. Here the journal could not even be opened, so nothing durable
// exists, the projection is NOT_APPLIED, and a refusal is the truthful answer:
// exit 6 would name a recovery that refuses.
func TestApplyRefusesWhenItsFirstAppendLeavesNothing(t *testing.T) {
	root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
	journalPath := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	// Readable, so every precondition still passes; unwritable, so the first
	// durable append of the transaction is what fails.
	if err := os.Chmod(journalPath, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(journalPath, 0o600) })

	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", "run-1"}, &stdout, &stderr)
	after, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	// The assertion order matters: the journal being byte-identical is what
	// makes exit 2 the honest code rather than a guess.
	if !bytes.Equal(before, after) {
		t.Fatal("the refused append still changed the journal")
	}
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "precondition refused") {
		t.Fatalf("failed first append exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := store.ReadJournal("run-1"); err != nil {
		t.Fatalf("journal is no longer replayable: %v", err)
	}
	// And the continuation exit 6 would have named is indeed unusable.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"apply", "run-1", "--recover"}, &stdout, &stderr); code != 2 ||
		!strings.Contains(stderr.String(), "--recover is refused from") {
		t.Fatalf("recover from a never-started transaction exit=%d stderr=%q", code, stderr.String())
	}
}

// TestRecoverReportsRecoverableWhenItsAppendFails is the same boundary read from
// the other direction. Here a transaction is already durable, so whatever the
// failed append did, the surviving projection is APPLYING — and exit 6's promise
// of a usable `--recover` is true no matter where the failure landed.
func TestRecoverReportsRecoverableWhenItsAppendFails(t *testing.T) {
	root, store, candidateID := applyRequireFixture(t, applyGate{require: []string{"verified"}})
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	candidate := input.Projection.State.ApplicationCandidate
	if candidate == nil || candidate.ID != candidateID {
		t.Fatalf("fixture candidate = %+v", candidate)
	}
	// Open a transaction the way a crash would leave one, so recovery is the
	// legal entry rather than a normal apply.
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		_, err := tx.At("fixture.apply.started").Append(resumeEvent("run-1", runstate.EventApplyStarted, map[string]any{
			"txn_id": "apply-fixture", "candidate_id": candidate.ID,
			"before_tree": candidate.BaseTree, "result_tree": candidate.ResultTree,
			"touched_paths":     []any{},
			"recovery":          map[string]any{"base_tree": candidate.BaseTree, "result_tree": candidate.ResultTree},
			"identity_versions": resumeIdentityVersions(),
		}))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
	if err := os.Chmod(journalPath, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(journalPath, 0o600) })

	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	code := run([]string{"apply", "run-1", "--recover"}, &stdout, &stderr)
	if code != 6 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "partitur apply run-1 --recover") {
		t.Fatalf("failed recovery append exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
