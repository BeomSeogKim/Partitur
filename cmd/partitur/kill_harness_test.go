package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/launch"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

type killEdge struct {
	id      faultpoint.EdgeID
	before  faultpoint.PointID
	after   faultpoint.PointID
	fixture func(*testing.T, string, string) (string, []string)
}

// receiptKillRecord keeps receipt-addressed cuts separate from the probe
// matrix. Its executed result comes from the real fixture's subtest result,
// not from this declaration.
type receiptKillRecord struct {
	edge     faultpoint.EdgeID
	endpoint faultpoint.ReceiptAddress
	test     string
}

type receiptKillKey struct {
	edge     faultpoint.EdgeID
	endpoint faultpoint.ReceiptAddress
}

type killHarnessJSONResult struct {
	passed map[string]bool
	output string
	err    error
}

const (
	// Only the failure path observes this, so a generous bound costs nothing on
	// a green run and a tight one risks misclassifying an exited child as live
	// under battery load - which would silently retarget every assertion that
	// anchors on the exited-child message.
	killHarnessExitObservationTimeout = 5 * time.Second
	killHarnessReapTimeout            = 5 * time.Second
)

type killHarnessWait struct {
	done     chan struct{}
	err      error
	exitCode int
}

var receiptKillHarnessRun struct {
	once    sync.Once
	records map[receiptKillKey]bool
	err     error
	output  string
}

var prepareQuiesceKillHarnessRun struct {
	once   sync.Once
	passed map[string]bool
	err    error
	output string
}

var supersessionKillHarnessRun struct {
	once   sync.Once
	passed map[string]bool
	err    error
	output string
}

var acceptanceSubjectKillHarnessRun struct {
	once   sync.Once
	passed map[string]bool
	err    error
	output string
}

type killFixture struct {
	name          string
	build         func(*testing.T, string, string) (string, []string)
	gateMode      string
	reviewOutcome string
	fixedPoint    fixedPointFixture
}

// fixedPointFixture is fixture metadata, not a conclusion inferred from a
// recovered projection. An unexpected unsettled projection must therefore
// stay in the rejecting none branch instead of selecting its own exception.
type fixedPointFixture struct {
	commandSpecificRecovery fixedPointCommandSpecificRecovery
}

type fixedPointCommandSpecificRecovery string

const (
	fixedPointRecoveryNone        fixedPointCommandSpecificRecovery = "none"
	fixedPointRecoveryApplication fixedPointCommandSpecificRecovery = "application"
	fixedPointRecoveryPromotion   fixedPointCommandSpecificRecovery = "promotion"
)

var fixedPointNoneFixture = fixedPointFixture{commandSpecificRecovery: fixedPointRecoveryNone}

type expectedFailure struct {
	event          runstate.EventType
	kind           string
	reason         string
	terminalReason string
	runReason      string
}

func TestSubprocessKillHarness(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, edge := range nonCancellationKillHarnessEdges() {
		edge := edge
		if edge.fixture == nil {
			// Composition and criterion-launch kill cuts run in their integration
			// packages so this already-saturated command package retains its
			// measured runtime.
			continue
		}
		t.Run(string(edge.id), func(t *testing.T) {
			for _, fixture := range fixturesForKillEdge(edge) {
				fixture := fixture
				t.Run(fixture.name, func(t *testing.T) {
					if edge.id == faultpoint.EdgeAcceptanceCriterionErrorToFailed && os.Geteuid() == 0 {
						t.Fatal("criterion-error kill fixture requires non-root permission enforcement")
					}
					for _, side := range []struct {
						name  string
						point faultpoint.PointID
					}{
						{name: "before", point: edge.before},
						{name: "after", point: edge.after},
					} {
						side := side
						t.Run(side.name, func(t *testing.T) {
							repository, environment := fixture.build(t, bin, vendor)
							runID := killAtPoint(t, partitur, repository, environment, side.point)
							if edge.id == faultpoint.EdgeAcceptanceCriterionErrorToFailed {
								restoreCriterionErrorFixtureWorktree(t, repository, runID)
							}
							assertCrashedStateBeforeResume(t, repository, runID, side.point)
							assertRecoveryFixedPoint(t, partitur, repository, environment, runID, expectedFailureFor(side.point), fixture.fixedPoint)
							if edge.id == faultpoint.EdgeAcceptanceCriterionErrorToFailed {
								assertCriterionErrorEndpoint(t, readHarnessEvents(t, repository, runID), true)
							}
							if fixture.gateMode != "" {
								assertHumanGateFixture(t, readHarnessJournal(t, repository, runID), fixture.gateMode, fixture.reviewOutcome)
							}
						})
					}
				})
			}
		})
	}
}

func TestRetryDispositionCanFollowExecuteCut(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	score := runScore()
	score["policy"].(map[string]any)["budget"].(map[string]any)["retries_per_movement"] = float64(1)
	for _, point := range retryCoveragePoints() {
		repository, environment := killHarnessRepositoryWithInputs(t, bin, vendor, score, runCast())
		runID := killAtPoint(t, partitur, repository, environment, point)
		code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "resume", runID)
		journal, err := os.ReadFile(filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(journal, []byte(`"charged":"quality_retry"`)) {
			t.Fatalf("retry disposition was not recorded at %s: resume exit=%d stdout=%q stderr=%q journal=%s", point, code, stdout, stderr, journal)
		}
		if started := bytes.Count(journal, []byte(`"type":"attempt.started"`)); started == 0 {
			t.Fatalf("recovery-selected retry never reached attempt.started at %s: journal=%s", point, journal)
		}
		if unstarted := bytes.Count(journal, []byte(`"reason":"attempt_never_started"`)); unstarted != 1 {
			t.Fatalf("attempt_never_started failures=%d at %s, want one crashed attempt before the retry: journal=%s", unstarted, point, journal)
		}
		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("retry recovery at %s: exit=%d stdout=%q stderr=%q", point, code, stdout, stderr)
		}
		assertFixedPointReplay(t, partitur, repository, environment, runID, journal, fixedPointNoneFixture)
		t.Logf("recovery-selected retry at %s reached attempt.started", point)
	}
}

func TestFailureOutcomeHarness(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, scenario := range []struct {
		name      string
		outcome   string
		score     map[string]any
		expected  expectedFailure
		recovered expectedFailure
	}{
		{
			name:      "adapter_task_failed",
			outcome:   "task_failed",
			score:     runScore(),
			expected:  expectedFailure{event: runstate.EventAttemptFailed, kind: "task_failed", terminalReason: "retries_exhausted"},
			recovered: expectedFailure{event: runstate.EventAttemptFailed, kind: "task_failed", terminalReason: "retries_exhausted"},
		},
		{
			name:      "acceptance_artifact_hash_mismatch",
			outcome:   "success",
			score:     hashMismatchScore(),
			expected:  expectedFailure{event: runstate.EventAcceptanceFailed, reason: "artifact_hash_mismatch", terminalReason: "retries_exhausted"},
			recovered: expectedFailure{event: runstate.EventAcceptanceFailed, reason: "artifact_hash_mismatch", terminalReason: "retries_exhausted"},
		},
		{
			name:      "read_only_verification_rejected",
			outcome:   "read_only_violation",
			score:     runScore(),
			expected:  expectedFailure{event: runstate.EventAttemptFailed, kind: "grant_denied", reason: "read_only_violation", terminalReason: "grant_denied"},
			recovered: expectedFailure{event: runstate.EventAttemptFailed, kind: "grant_denied", reason: "candidate_mismatch", terminalReason: "grant_denied"},
		},
	} {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			t.Run("uncrashed", func(t *testing.T) {
				repository, environment := killHarnessRepositoryWithInputs(t, bin, vendor, scenario.score, runCast())
				environment = fixtureOutcomeEnvironment(environment, scenario.outcome)
				runID := runUncrashed(t, partitur, repository, environment)
				journal := readHarnessJournal(t, repository, runID)
				assertExpectedFailure(t, journal, scenario.expected)
				assertRecoveryFixedPoint(t, partitur, repository, environment, runID, &scenario.expected, fixedPointNoneFixture)
			})
			t.Run("crashed_after_outcome", func(t *testing.T) {
				repository, environment := killHarnessRepositoryWithInputs(t, bin, vendor, scenario.score, runCast())
				environment = fixtureOutcomeEnvironment(environment, scenario.outcome)
				runID := killAtPoint(t, partitur, repository, environment, faultpoint.PointExecuteOutcomeRecorded)
				assertRecoveryFixedPoint(t, partitur, repository, environment, runID, &scenario.recovered, fixedPointNoneFixture)
			})
		})
	}
}

func TestAcceptanceFailureWindowHarness(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	buildE2EBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	repository, environment := killHarnessRepositoryWithInputs(t, bin, vendor, hashMismatchScore(), runCast())
	environment = fixtureOutcomeEnvironment(environment, "success")
	runID := killAtPoint(t, partitur, repository, environment, faultpoint.PointAcceptanceFailureRecorded)
	assertRecoveryFixedPoint(t, partitur, repository, environment, runID, &expectedFailure{
		event: runstate.EventAcceptanceFailed, reason: "artifact_hash_mismatch", terminalReason: "retries_exhausted",
	}, fixedPointNoneFixture)
}

func killHarnessEdges() []killEdge {
	edges := nonCancellationKillHarnessEdges()
	return append(edges,
		killEdge{faultpoint.EdgeCancelSweptToTerminal, faultpoint.PointCancelSessionsSwept, faultpoint.PointCancelRunCancelled, nil},
		killEdge{faultpoint.EdgeCancelSweptToQuarantined, faultpoint.PointCancelSessionsSwept, faultpoint.PointCancelSnapshotQuarantined, nil},
		killEdge{faultpoint.EdgeCancelIntervalStoppedToTerminal, faultpoint.PointCancelExecutionStopped, faultpoint.PointCancelRunCancelled, nil},
		killEdge{faultpoint.EdgeCancelFenceDecidedToTerminal, faultpoint.PointCancelFenceDecided, faultpoint.PointCancelRunCancelled, nil},
		killEdge{faultpoint.EdgeCancelTerminalToLeaseRemoved, faultpoint.PointCancelRunCancelled, faultpoint.PointCancelLeaseRemoved, nil},
	)
}

func nonCancellationKillHarnessEdges() []killEdge {
	return []killEdge{
		{faultpoint.EdgeAuthorityGrantedToLeaseCreated, faultpoint.PointAuthorityGranted, faultpoint.PointAuthorityLeaseCreated, killHarnessRepository},
		{faultpoint.EdgeLaunchAdapterMarkerHeldToIdentity, faultpoint.PointLaunchAdapterMarkerHeld, faultpoint.PointLaunchAdapterIdentityPublished, killHarnessRepository},
		{faultpoint.EdgeLaunchAdapterIdentityPublishedToRecorded, faultpoint.PointLaunchAdapterIdentityPublished, faultpoint.PointLaunchAdapterIdentityRecorded, killHarnessRepository},
		{faultpoint.EdgeLaunchAdapterRecordedToGate, faultpoint.PointLaunchAdapterIdentityRecorded, faultpoint.PointLaunchAdapterGateReleased, killHarnessRepository},
		{faultpoint.EdgeExecuteAdapterSweptToIntervalStopped, faultpoint.PointExecuteAdapterSwept, faultpoint.PointExecuteIntervalStopped, killHarnessRepository},
		{faultpoint.EdgeExecuteIntervalStoppedToOutcome, faultpoint.PointExecuteIntervalStopped, faultpoint.PointExecuteOutcomeRecorded, killHarnessRepository},
		{faultpoint.EdgeLifecycleAttemptCompletedToMovementSucceeded, faultpoint.PointLifecycleAttemptCompleted, faultpoint.PointLifecycleMovementSucceeded, killHarnessRepository},
		{faultpoint.EdgeLifecycleMovementFailedToRunFailed, faultpoint.PointLifecycleMovementFailed, faultpoint.PointLifecycleRunFailed, movementFailureKillHarnessRepository},
		{faultpoint.EdgeAcceptanceCriterionErrorToFailed, faultpoint.PointAcceptanceNonPassCompleted, faultpoint.PointAcceptanceFailureRecorded, criterionErrorKillHarnessRepository},
		{faultpoint.EdgeAcceptanceEvaluationCompletedToDecisionRequested, faultpoint.PointAcceptanceEvaluationCompleted, faultpoint.PointHumanGateDecisionRequested, humanGateKillHarnessRepository},
		{faultpoint.EdgeChangeSetCapturedToRecorded, faultpoint.PointChangeSetCaptured, faultpoint.PointChangeSetRecorded, nil},
		{faultpoint.EdgeCompositionMovementEvidenceToTerminal, faultpoint.PointCompositionMovementEvidence, faultpoint.PointCompositionMovementTerminal, nil},
		{faultpoint.EdgeCompositionCandidateEvidenceToTerminal, faultpoint.PointCompositionCandidateEvidence, faultpoint.PointCompositionCandidateTerminal, nil},
		{faultpoint.EdgeLaunchCriterionMarkerHeldToIdentity, faultpoint.PointLaunchCriterionMarkerHeld, faultpoint.PointLaunchCriterionIdentityPublished, nil},
		{faultpoint.EdgeLaunchCriterionIdentityToRecorded, faultpoint.PointLaunchCriterionIdentityPublished, faultpoint.PointLaunchCriterionIdentityRecorded, nil},
		{faultpoint.EdgeLaunchCriterionRecordedToGate, faultpoint.PointLaunchCriterionIdentityRecorded, faultpoint.PointLaunchCriterionGateReleased, nil},
	}
}

func fixturesForKillEdge(edge killEdge) []killFixture {
	if edge.id == faultpoint.EdgeAcceptanceEvaluationCompletedToDecisionRequested {
		return []killFixture{
			{name: "always", build: humanGateKillHarnessRepository, gateMode: "always", fixedPoint: fixedPointNoneFixture},
			{name: "on_contested", build: contestedHumanGateKillHarnessRepository, gateMode: "on_contested", reviewOutcome: "CONTESTED", fixedPoint: fixedPointNoneFixture},
		}
	}
	return []killFixture{{name: "default", build: edge.fixture, fixedPoint: fixedPointNoneFixture}}
}

func assertHumanGateFixture(t *testing.T, journal []byte, gateMode, reviewOutcome string) {
	t.Helper()
	if !bytes.Contains(journal, []byte(`"gate_mode":"`+gateMode+`"`)) {
		t.Fatalf("human gate mode %q was not requested: journal=%s", gateMode, journal)
	}
	if reviewOutcome != "" && !bytes.Contains(journal, []byte(`"review_outcome":"`+reviewOutcome+`"`)) {
		t.Fatalf("review outcome %q was not durable before recovery completed: journal=%s", reviewOutcome, journal)
	}
}

func retryCoveragePoints() []faultpoint.PointID {
	return []faultpoint.PointID{
		faultpoint.PointLaunchAdapterMarkerHeld,
		faultpoint.PointLaunchAdapterIdentityPublished,
	}
}

func TestKillHarnessCatalogCrossCheck(t *testing.T) {
	design, _ := edgeIDsFromAppendixE(t)
	dispositions := gateCutDispositions(t)
	if len(design) == 0 || len(dispositions) == 0 {
		t.Fatal("catalog extraction must not be empty")
	}
	if len(design) != len(dispositions) {
		t.Fatalf("catalog count mismatch: DESIGN=%d HARNESS=%d", len(design), len(dispositions))
	}

	assertReceiptKillRegistry(t, design, receiptKillHarnessRecords(t))
	reachable := reachableKillHarnessEdges(t)
	if len(retryCoveragePoints()) != 2 || retryCoveragePoints()[0] == retryCoveragePoints()[1] {
		t.Fatalf("retry coverage must name two distinct cut sides: %v", retryCoveragePoints())
	}

	for edge := range design {
		disposition, present := dispositions[edge]
		if !present {
			t.Fatalf("HARNESS has no disposition for DESIGN edge %q", edge)
		}
		if disposition.reason == "" || disposition.clause == "" {
			t.Fatalf("HARNESS disposition for %q lacks reason or owning clause", edge)
		}
		switch disposition.kind {
		case "reachable":
			if !reachable[edge] {
				t.Fatalf("HARNESS calls %q reachable but this gate has no two-sided cut", edge)
			}
		case "not reached by this gate's cuts":
			if reachable[edge] {
				t.Fatalf("HARNESS marks executed edge %q as not reached", edge)
			}
		default:
			t.Fatalf("HARNESS disposition for %q = %q", edge, disposition.kind)
		}
	}
	for edge := range dispositions {
		if !design[edge] {
			t.Fatalf("HARNESS names edge %q absent from DESIGN E.2", edge)
		}
	}
}

func assertReceiptKillRegistry(t *testing.T, design map[string]bool, records map[receiptKillKey]bool) {
	t.Helper()
	if len(records) != 29 {
		t.Fatalf("receipt registry records=%d, want twenty-nine", len(records))
	}
	counts := make(map[faultpoint.EdgeID]int)
	for key, passed := range records {
		if !design[string(key.edge)] {
			t.Fatalf("receipt registry names edge %q absent from DESIGN E.2", key.edge)
		}
		if key.endpoint == "" {
			t.Fatalf("receipt registry has empty endpoint for %q", key.edge)
		}
		if !passed {
			t.Fatalf("receipt registry did not execute %q at %q", key.edge, key.endpoint)
		}
		counts[key.edge]++
	}
	if len(counts) != 17 {
		t.Fatalf("receipt registry edges=%d, want seventeen", len(counts))
	}
	for edge, want := range map[faultpoint.EdgeID]int{
		faultpoint.EdgePrepareSnapshotToPlan:                               2,
		faultpoint.EdgePreparePlanToPrepared:                               2,
		faultpoint.EdgeQuiesceObservedToSwept:                              1,
		faultpoint.EdgePreparePreparedToObserved:                           2,
		faultpoint.EdgeQuiesceSweptToLeaseMoved:                            1,
		faultpoint.EdgeQuiesceLeaseMovedToCommitLock:                       1,
		faultpoint.EdgePrepareQuarantinedToAbandoned:                       2,
		faultpoint.EdgeProposalPublishedToBlockedRoute:                     2,
		faultpoint.EdgeProposalBlockedRouteToRouted:                        2,
		faultpoint.EdgeProposalPublishedToRouted:                           2,
		faultpoint.EdgeProposalRoutedToDecisionRequested:                   2,
		faultpoint.EdgeSupersedeSweptToApproved:                            1,
		faultpoint.EdgeSupersedeIntervalStoppedToApproved:                  2,
		faultpoint.EdgeSupersedeFenceDecidedToApproved:                     1,
		faultpoint.EdgeSupersedeApprovedToLeaseRemoved:                     2,
		faultpoint.EdgeLifecycleDraftPerformerCompletedToNoBlockingFailure: 2,
		faultpoint.EdgeProposalCoreFinalizationPublishedToRouted:           2,
	} {
		if counts[edge] != want {
			t.Fatalf("receipt registry records for %q = %d, want %d", edge, counts[edge], want)
		}
	}
}

func TestE2CatalogCountClaims(t *testing.T) {
	catalogRows, boundaryRows := edgeIDsFromAppendixE(t)
	design, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	assertCountClaim(t, string(design),
		regexp.MustCompile(`\*\*([A-Z][a-z-]*) of the ([a-z-]+)\s+have a `+"`B`"+` endpoint\*\*`),
		boundaryRows, len(catalogRows),
	)

	reached := reachableKillHarnessEdges(t)
	harness, err := os.ReadFile(filepath.Join("..", "..", "docs", "HARNESS.md"))
	if err != nil {
		t.Fatal(err)
	}
	assertCountClaim(t, string(harness), regexp.MustCompile(`This gate reaches ([a-z-]+) E\.2 edges\.`), len(reached))
}

func reachableKillHarnessEdges(t *testing.T) map[string]bool {
	t.Helper()
	coverage := make(map[string]int)
	for _, edge := range killHarnessEdges() {
		id := string(edge.id)
		if coverage[id] != 0 {
			t.Fatalf("kill harness declares duplicate reachable edge %q", id)
		}
		coverage[id] = 2
	}
	for key, passed := range receiptKillHarnessRecords(t) {
		if !passed {
			t.Fatalf("receipt kill harness did not pass %q at %q", key.edge, key.endpoint)
		}
		coverage[string(key.edge)]++
	}
	for edge, passed := range prepareQuiesceProbeKillHarnessRecords(t) {
		if !passed {
			t.Fatalf("probe kill harness did not pass %q", edge)
		}
		coverage[string(edge)]++
	}
	for edge, passed := range supersessionProbeKillHarnessRecords(t) {
		if !passed {
			t.Fatalf("supersession probe kill harness did not pass %q", edge)
		}
		coverage[string(edge)]++
	}
	for endpoint, passed := range acceptanceSubjectKillHarnessRecords(t) {
		if !passed {
			t.Fatalf("acceptance subject kill harness did not pass %q", endpoint)
		}
		coverage[string(faultpoint.EdgeAcceptanceSubjectPinnedToStarted)]++
	}
	reachable := make(map[string]bool)
	for edge, count := range coverage {
		if count != 2 {
			t.Fatalf("kill harness records for %q = %d, want exactly two endpoints", edge, count)
		}
		reachable[edge] = true
	}
	if len(reachable) == 0 {
		t.Fatal("kill harness declares no reachable edges")
	}
	return reachable
}

func acceptanceSubjectKillHarnessRecords(t *testing.T) map[string]bool {
	t.Helper()
	acceptanceSubjectKillHarnessRun.once.Do(func() {
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			acceptanceSubjectKillHarnessRun.err = err
			return
		}
		acceptanceSubjectKillHarnessRun.passed, acceptanceSubjectKillHarnessRun.output, acceptanceSubjectKillHarnessRun.err = runKillHarnessJSON(
			root,
			"./cmd/partitur",
			"^TestAcceptanceSubjectPinnedToStartedKillCuts$",
		)
	})
	if acceptanceSubjectKillHarnessRun.err != nil {
		t.Fatalf("acceptance subject kill harness: %v\n%s", acceptanceSubjectKillHarnessRun.err, acceptanceSubjectKillHarnessRun.output)
	}
	return map[string]bool{
		"subject_pinned":     acceptanceSubjectKillHarnessRun.passed["TestAcceptanceSubjectPinnedToStartedKillCuts/subject_pinned"],
		"acceptance_started": acceptanceSubjectKillHarnessRun.passed["TestAcceptanceSubjectPinnedToStartedKillCuts/acceptance_started"],
	}
}

func receiptKillHarnessRecords(t *testing.T) map[receiptKillKey]bool {
	t.Helper()
	receiptKillHarnessRun.once.Do(func() {
		receiptKillHarnessRun.records, receiptKillHarnessRun.output, receiptKillHarnessRun.err = runReceiptKillHarness(t)
	})
	if receiptKillHarnessRun.err != nil {
		t.Fatalf("receipt kill harness: %v\n%s", receiptKillHarnessRun.err, receiptKillHarnessRun.output)
	}
	return receiptKillHarnessRun.records
}

func receiptKillHarnessEdges() []receiptKillRecord {
	const fixture = "TestPreparePublicationKillCuts/"
	const supersessionFixture = "TestSupersessionKillMatrix"
	const cliRoutedProposalFixture = "TestCLIProposalPublishedToRoutedKillCuts/"
	const cliRoutedRequestFixture = "TestCLIRoutedProposalToDecisionRequestedKillCuts/"
	const cancelledPrepareFixture = "TestPrepareQuarantinedToAbandonedKillCuts/"
	const draftResultFixture = "TestDraftResultBoundaryKillCuts/"
	const coreFinalizationFixture = "TestCoreFinalizationPublishedToRoutedKillCuts/"
	return []receiptKillRecord{
		{faultpoint.EdgePrepareSnapshotToPlan, "amendment.approval.snapshot", fixture + "prepare.snapshot_to_plan/snapshot"},
		{faultpoint.EdgePrepareSnapshotToPlan, "amendment.approval.plan", fixture + "prepare.snapshot_to_plan/plan"},
		{faultpoint.EdgePreparePlanToPrepared, "amendment.approval.plan", fixture + "prepare.plan_to_prepared/plan"},
		{faultpoint.EdgePreparePlanToPrepared, "amendment.approval_prepared", fixture + "prepare.plan_to_prepared/prepared"},
		{faultpoint.EdgePreparePreparedToObserved, "amendment.approval_prepared", "TestPrepareQuiesceDriverKillCuts/prepare.prepared_to_observed/prepared"},
		{faultpoint.EdgePreparePreparedToObserved, "prepare.quiesce_observed", "TestPrepareQuiesceDriverKillCuts/prepare.prepared_to_observed/observed"},
		{faultpoint.EdgeQuiesceObservedToSwept, "prepare.quiesce_observed", "TestPrepareQuiesceDriverKillCuts/quiesce.observed_to_swept/observed"},
		{faultpoint.EdgeQuiesceSweptToLeaseMoved, "prepare.ack.lease", "TestPrepareQuiesceDriverKillCuts/quiesce.swept_to_lease_moved/lease_moved"},
		{faultpoint.EdgeQuiesceLeaseMovedToCommitLock, "prepare.ack.lease", "TestPrepareQuiesceDriverKillCuts/quiesce.lease_moved_to_commit_lock/lease_moved/cancellation_wins"},
		{faultpoint.EdgePrepareQuarantinedToAbandoned, "cancellation.prepare.snapshot", cancelledPrepareFixture + "prepare.quarantined_to_abandoned/quarantined"},
		{faultpoint.EdgePrepareQuarantinedToAbandoned, "cancellation.amendment.approval_abandoned", cancelledPrepareFixture + "prepare.quarantined_to_abandoned/abandoned"},
		{faultpoint.EdgeProposalPublishedToBlockedRoute, "proposal.record.published", "TestProposalPublicationKillCuts/proposal.published_to_blocked_route/published"},
		{faultpoint.EdgeProposalPublishedToBlockedRoute, "attempt.blocked", "TestProposalPublicationKillCuts/proposal.published_to_blocked_route/blocked_route"},
		{faultpoint.EdgeProposalBlockedRouteToRouted, "attempt.blocked", "TestBlockedProposalRouteKillCuts/proposal.blocked_route_to_routed/blocked_route"},
		{faultpoint.EdgeProposalBlockedRouteToRouted, "recovery.amendment.routed_human", "TestBlockedProposalRouteKillCuts/proposal.blocked_route_to_routed/blocked_route"},
		{faultpoint.EdgeProposalPublishedToRouted, "proposal.record.published", cliRoutedProposalFixture + "proposal.published_to_routed/published"},
		{faultpoint.EdgeProposalPublishedToRouted, "amendment.routed_human", cliRoutedProposalFixture + "proposal.published_to_routed/routed"},
		{faultpoint.EdgeProposalRoutedToDecisionRequested, "amendment.routed_human", cliRoutedRequestFixture + "proposal.routed_to_decision_requested/routed"},
		{faultpoint.EdgeProposalRoutedToDecisionRequested, "amendment.decision.requested", cliRoutedRequestFixture + "proposal.routed_to_decision_requested/requested"},
		{faultpoint.EdgeSupersedeSweptToApproved, "prepare.commit.approved", supersessionFixture},
		{faultpoint.EdgeSupersedeIntervalStoppedToApproved, "prepare.commit.execution.stopped", supersessionFixture},
		{faultpoint.EdgeSupersedeIntervalStoppedToApproved, "prepare.commit.approved", supersessionFixture},
		{faultpoint.EdgeSupersedeFenceDecidedToApproved, "prepare.commit.approved", supersessionFixture},
		{faultpoint.EdgeSupersedeApprovedToLeaseRemoved, "prepare.commit.approved", supersessionFixture},
		{faultpoint.EdgeSupersedeApprovedToLeaseRemoved, "prepare.commit.lease", supersessionFixture},
		{faultpoint.EdgeLifecycleDraftPerformerCompletedToNoBlockingFailure, "attempt.performer_completed", draftResultFixture + "lifecycle.draft_performer_completed_to_no_blocking_failure/performer_completed"},
		{faultpoint.EdgeLifecycleDraftPerformerCompletedToNoBlockingFailure, "attempt.failed", draftResultFixture + "lifecycle.draft_performer_completed_to_no_blocking_failure/attempt_failed"},
		{faultpoint.EdgeProposalCoreFinalizationPublishedToRouted, "proposal.record.published", coreFinalizationFixture + "published_reconstructs_after_quarantine"},
		{faultpoint.EdgeProposalCoreFinalizationPublishedToRouted, "amendment.routed_human", coreFinalizationFixture + "routed_retains_and_second_resume_is_fixed"},
	}
}

func prepareQuiesceProbeKillHarnessRecords(t *testing.T) map[faultpoint.EdgeID]bool {
	t.Helper()
	passed := prepareQuiesceKillHarnessPassed(t)
	return map[faultpoint.EdgeID]bool{
		faultpoint.EdgeQuiesceObservedToSwept:        passed["TestPrepareQuiesceDriverKillCuts/quiesce.observed_to_swept/swept"],
		faultpoint.EdgeQuiesceSweptToLeaseMoved:      passed["TestPrepareQuiesceDriverKillCuts/quiesce.swept_to_lease_moved/swept"],
		faultpoint.EdgeQuiesceLeaseMovedToCommitLock: passed["TestPrepareQuiesceDriverKillCuts/quiesce.lease_moved_to_commit_lock/commit_lock/hash_mismatch_halts"],
	}
}

func supersessionProbeKillHarnessRecords(t *testing.T) map[faultpoint.EdgeID]bool {
	t.Helper()
	passed := supersessionKillHarnessPassed(t)
	return map[faultpoint.EdgeID]bool{
		faultpoint.EdgeSupersedeSweptToApproved:        passed["TestSupersessionKillMatrix"],
		faultpoint.EdgeSupersedeFenceDecidedToApproved: passed["TestSupersessionKillMatrix"],
	}
}

func runReceiptKillHarness(t *testing.T) (map[receiptKillKey]bool, string, error) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return nil, "", err
	}
	publicationResult := make(chan killHarnessJSONResult, 1)
	prepareResult := make(chan killHarnessJSONResult, 1)
	proposalResult := make(chan killHarnessJSONResult, 1)
	publicationProposalResult := make(chan killHarnessJSONResult, 1)
	cliRoutedProposalResult := make(chan killHarnessJSONResult, 1)
	cliRoutedRequestResult := make(chan killHarnessJSONResult, 1)
	cancelledPrepareResult := make(chan killHarnessJSONResult, 1)
	supersessionResult := make(chan killHarnessJSONResult, 1)
	draftResult := make(chan killHarnessJSONResult, 1)
	coreFinalizationResult := make(chan killHarnessJSONResult, 1)
	go func() {
		passed, output, err := runKillHarnessJSON(root, "./internal/amendmentexec", "^TestPreparePublicationKillCuts$")
		publicationResult <- killHarnessJSONResult{passed: passed, output: output, err: err}
	}()
	go func() {
		passed, output, err := prepareQuiesceKillHarnessResult()
		prepareResult <- killHarnessJSONResult{passed: passed, output: output, err: err}
	}()
	go func() {
		passed, output, err := runKillHarnessJSON(root, "./cmd/partitur", "^TestBlockedProposalRouteKillCuts$")
		proposalResult <- killHarnessJSONResult{passed: passed, output: output, err: err}
	}()
	go func() {
		passed, output, err := runKillHarnessJSON(root, "./cmd/partitur", "^TestProposalPublicationKillCuts$")
		publicationProposalResult <- killHarnessJSONResult{passed: passed, output: output, err: err}
	}()
	go func() {
		passed, output, err := runKillHarnessJSON(root, "./cmd/partitur", "^TestCLIProposalPublishedToRoutedKillCuts$")
		cliRoutedProposalResult <- killHarnessJSONResult{passed: passed, output: output, err: err}
	}()
	go func() {
		passed, output, err := runKillHarnessJSON(root, "./cmd/partitur", "^TestCLIRoutedProposalToDecisionRequestedKillCuts$")
		cliRoutedRequestResult <- killHarnessJSONResult{passed: passed, output: output, err: err}
	}()
	go func() {
		passed, output, err := runKillHarnessJSON(root, "./cmd/partitur", "^TestPrepareQuarantinedToAbandonedKillCuts$")
		cancelledPrepareResult <- killHarnessJSONResult{passed: passed, output: output, err: err}
	}()
	go func() {
		passed, output, err := supersessionKillHarnessResult()
		supersessionResult <- killHarnessJSONResult{passed: passed, output: output, err: err}
	}()
	go func() {
		passed, output, err := runKillHarnessJSON(root, "./cmd/partitur", "^TestDraftResultBoundaryKillCuts$")
		draftResult <- killHarnessJSONResult{passed: passed, output: output, err: err}
	}()
	go func() {
		passed, output, err := runKillHarnessJSON(root, "./cmd/partitur", "^TestCoreFinalizationPublishedToRoutedKillCuts$")
		coreFinalizationResult <- killHarnessJSONResult{passed: passed, output: output, err: err}
	}()

	publication := <-publicationResult
	prepare := <-prepareResult
	proposal := <-proposalResult
	publicationProposal := <-publicationProposalResult
	cliRoutedProposal := <-cliRoutedProposalResult
	cliRoutedRequest := <-cliRoutedRequestResult
	cancelledPrepare := <-cancelledPrepareResult
	supersession := <-supersessionResult
	draft := <-draftResult
	coreFinalization := <-coreFinalizationResult
	if publication.err != nil {
		return nil, publication.output, publication.err
	}
	if prepare.err != nil {
		return nil, prepare.output, prepare.err
	}
	if proposal.err != nil {
		return nil, proposal.output, proposal.err
	}
	if publicationProposal.err != nil {
		return nil, publicationProposal.output, publicationProposal.err
	}
	if cliRoutedProposal.err != nil {
		return nil, cliRoutedProposal.output, cliRoutedProposal.err
	}
	if cliRoutedRequest.err != nil {
		return nil, cliRoutedRequest.output, cliRoutedRequest.err
	}
	if cancelledPrepare.err != nil {
		return nil, cancelledPrepare.output, cancelledPrepare.err
	}
	if supersession.err != nil {
		return nil, supersession.output, supersession.err
	}
	if draft.err != nil {
		return nil, draft.output, draft.err
	}
	if coreFinalization.err != nil {
		return nil, coreFinalization.output, coreFinalization.err
	}
	passed := publication.passed
	preparePassed := prepare.passed
	for test, result := range preparePassed {
		passed[test] = result
	}
	for test, result := range proposal.passed {
		passed[test] = result
	}
	for test, result := range publicationProposal.passed {
		passed[test] = result
	}
	for test, result := range cliRoutedProposal.passed {
		passed[test] = result
	}
	for test, result := range cliRoutedRequest.passed {
		passed[test] = result
	}
	for test, result := range cancelledPrepare.passed {
		passed[test] = result
	}
	for test, result := range supersession.passed {
		passed[test] = result
	}
	for test, result := range draft.passed {
		passed[test] = result
	}
	for test, result := range coreFinalization.passed {
		passed[test] = result
	}

	records := make(map[receiptKillKey]bool)
	for _, record := range receiptKillHarnessEdges() {
		key := receiptKillKey{edge: record.edge, endpoint: record.endpoint}
		if _, duplicate := records[key]; duplicate {
			return nil, publication.output + prepare.output + proposal.output, fmt.Errorf("duplicate receipt registry record for %q at %q", key.edge, key.endpoint)
		}
		records[key] = passed[record.test]
	}
	return records, publication.output + prepare.output + proposal.output + publicationProposal.output + cliRoutedProposal.output + cliRoutedRequest.output + cancelledPrepare.output + supersession.output + draft.output + coreFinalization.output, nil
}

func prepareQuiesceKillHarnessPassed(t *testing.T) map[string]bool {
	t.Helper()
	passed, output, err := prepareQuiesceKillHarnessResult()
	if err != nil {
		t.Fatalf("prepare/quiesce kill harness: %v\n%s", err, output)
	}
	return passed
}

func prepareQuiesceKillHarnessResult() (map[string]bool, string, error) {
	prepareQuiesceKillHarnessRun.once.Do(func() {
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			prepareQuiesceKillHarnessRun.err = err
			return
		}
		prepareQuiesceKillHarnessRun.passed, prepareQuiesceKillHarnessRun.output, prepareQuiesceKillHarnessRun.err = runKillHarnessJSON(root, "./cmd/partitur", "^TestPrepareQuiesceDriverKillCuts$")
	})
	return prepareQuiesceKillHarnessRun.passed, prepareQuiesceKillHarnessRun.output, prepareQuiesceKillHarnessRun.err
}

func supersessionKillHarnessPassed(t *testing.T) map[string]bool {
	t.Helper()
	passed, output, err := supersessionKillHarnessResult()
	if err != nil {
		t.Fatalf("supersession kill harness: %v\n%s", err, output)
	}
	return passed
}

func supersessionKillHarnessResult() (map[string]bool, string, error) {
	supersessionKillHarnessRun.once.Do(func() {
		root, err := filepath.Abs(filepath.Join("..", ".."))
		if err != nil {
			supersessionKillHarnessRun.err = err
			return
		}
		supersessionKillHarnessRun.passed, supersessionKillHarnessRun.output, supersessionKillHarnessRun.err = runKillHarnessJSON(root, "./cmd/partitur", "^TestSupersessionKillMatrix$")
	})
	return supersessionKillHarnessRun.passed, supersessionKillHarnessRun.output, supersessionKillHarnessRun.err
}

func runKillHarnessJSON(root, packagePath, testName string) (map[string]bool, string, error) {
	command := exec.Command("go", "test", "-json", "-count=1", "-timeout=2m", "-tags=faultprobe", packagePath, "-run", testName)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, string(output), fmt.Errorf("run %s: %w", testName, err)
	}
	passed := make(map[string]bool)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		var event struct {
			Action string
			Test   string
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Action == "pass" && event.Test != "" {
			passed[event.Test] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, string(output), fmt.Errorf("read %s output: %w", testName, err)
	}
	return passed, string(output), nil
}

func assertCountClaim(t *testing.T, contents string, claim *regexp.Regexp, counts ...int) {
	t.Helper()
	matches := claim.FindAllStringSubmatch(contents, -1)
	if len(matches) == 0 {
		t.Fatalf("count claim %q is absent", claim)
	}
	if len(matches) != 1 || len(matches[0])-1 != len(counts) {
		t.Fatalf("count claim %q matches=%d groups=%d", claim, len(matches), len(matches[0])-1)
	}
	for index, count := range counts {
		if !strings.EqualFold(matches[0][index+1], countWord(t, count)) {
			t.Fatalf("count claim %q value %q, want %q", claim, matches[0][index+1], countWord(t, count))
		}
	}
}

func countWord(t *testing.T, count int) string {
	t.Helper()
	ones := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen"}
	if count < len(ones) {
		return ones[count]
	}
	tens := []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}
	if count >= 100 {
		t.Fatalf("count %d cannot be expressed by this prose guard", count)
	}
	result := tens[count/10]
	if count%10 != 0 {
		result += "-" + ones[count%10]
	}
	return result
}

type gateCutDisposition struct {
	kind   string
	clause string
	reason string
}

func edgeIDsFromAppendixE(t *testing.T) (map[string]bool, int) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(contents), "\n")
	start, end := tableBoundsCounted(t, lines, "## E.2 The catalog", "## E.3 ", 1, 1)
	pattern := regexp.MustCompile("^`([a-z][a-z0-9_.]*)`$")
	edges := make(map[string]bool)
	boundaryRows := 0
	for _, line := range lines[start:end] {
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "| Edge |") || strings.HasPrefix(line, "|---") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) != 7 {
			t.Fatalf("unparseable DESIGN E.2 row %q", line)
		}
		match := pattern.FindStringSubmatch(strings.TrimSpace(cells[1]))
		if match == nil || edges[match[1]] {
			t.Fatalf("unparseable or duplicate DESIGN E.2 row %q", line)
		}
		edges[match[1]] = true
		if strings.HasSuffix(strings.TrimSpace(cells[2]), "`B`") || strings.HasSuffix(strings.TrimSpace(cells[3]), "`B`") {
			boundaryRows++
		}
	}
	if len(edges) == 0 {
		t.Fatal("DESIGN E.2 extraction produced no rows")
	}
	return edges, boundaryRows
}

func gateCutDispositions(t *testing.T) map[string]gateCutDisposition {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "HARNESS.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(contents), "\n")
	start, end := tableBounds(t, lines, "## Gate-cut dispositions", "## Execution model — deterministic interleaving, not a self-racing process")
	rows := make(map[string]gateCutDisposition)
	for _, line := range lines[start:end] {
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "| Edge |") || strings.HasPrefix(line, "|---") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) != 6 {
			t.Fatalf("unparseable HARNESS disposition row %q", line)
		}
		id := strings.Trim(strings.TrimSpace(cells[1]), "`")
		if id == "" || rows[id].kind != "" {
			t.Fatalf("empty or duplicate HARNESS disposition edge %q", id)
		}
		rows[id] = gateCutDisposition{
			kind: strings.TrimSpace(cells[2]), clause: strings.TrimSpace(cells[3]), reason: strings.TrimSpace(cells[4]),
		}
	}
	if len(rows) == 0 {
		t.Fatal("HARNESS disposition extraction produced no rows")
	}
	return rows
}

func tableBounds(t *testing.T, lines []string, heading, nextHeading string) (int, int) {
	t.Helper()
	return tableBoundsCounted(t, lines, heading, nextHeading, 1, 1)
}

func tableBoundsCounted(t *testing.T, lines []string, heading, nextHeading string, wantHeading, wantNext int) (int, int) {
	t.Helper()
	start, end := -1, -1
	headingCount, nextCount := 0, 0
	for index, line := range lines {
		if line == heading {
			start = index + 1
			headingCount++
		}
		if strings.HasPrefix(line, nextHeading) {
			end = index
			nextCount++
		}
	}
	if headingCount != wantHeading || nextCount != wantNext || end <= start {
		t.Fatalf("table bounds %q -> %q: headings=%d/%d next=%d/%d start=%d end=%d", heading, nextHeading, headingCount, wantHeading, nextCount, wantNext, start, end)
	}
	return start, end
}

func killHarnessRepository(t *testing.T, bin, vendor string) (string, []string) {
	return killHarnessRepositoryWithInputs(t, bin, vendor, runScore(), runCast())
}

func movementFailureKillHarnessRepository(t *testing.T, bin, vendor string) (string, []string) {
	repository, environment := killHarnessRepository(t, bin, vendor)
	return repository, fixtureOutcomeEnvironment(environment, "task_failed")
}

func criterionErrorKillHarnessRepository(t *testing.T, bin, vendor string) (string, []string) {
	score := runScore()
	criterion := score["movements"].([]any)[0].(map[string]any)["acceptance"].(map[string]any)["hard"].([]any)[1].(map[string]any)
	criterion["run"] = []any{"chmod", "000", "."}
	return killHarnessRepositoryWithInputs(t, bin, vendor, score, runCast())
}

func restoreCriterionErrorFixtureWorktree(t *testing.T, repository, runID string) {
	t.Helper()
	worktrees, err := filepath.Glob(filepath.Join(repository, ".partitur", "work", runID, "*", "worktree"))
	if err != nil {
		t.Fatal(err)
	}
	if len(worktrees) != 1 {
		t.Fatalf("criterion-error fixture worktrees=%d, want one", len(worktrees))
	}
	if err := os.Chmod(worktrees[0], 0o700); err != nil {
		t.Fatalf("restore criterion-error fixture worktree: %v", err)
	}
}

func killHarnessRepositoryWithInputs(
	t *testing.T,
	bin, vendor string,
	scoreDocument, castDocument map[string]any,
) (string, []string) {
	t.Helper()
	repository := t.TempDir()
	writeValidateInputs(t, repository, scoreDocument, castDocument)
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")
	return repository, replaceEnvironment(os.Environ(), map[string]string{
		"HOME":               t.TempDir(),
		"PATH":               bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN": vendor,
		runVendorEnvironment: "1",
	})
}

func fixtureOutcomeEnvironment(environment []string, outcome string) []string {
	return replaceEnvironment(environment, map[string]string{runVendorOutcomeEnvironment: outcome})
}

func runUncrashed(t *testing.T, binary, repository string, environment []string) string {
	t.Helper()
	code, stdout, stderr := runCommandBinary(t, binary, repository, environment, "run")
	runID := strings.TrimSpace(stdout)
	if code != 4 || runID == "" || stdout != runID+"\n" || !strings.Contains(stderr, "movement_failed") {
		t.Fatalf("failure run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	return runID
}

func readHarnessJournal(t *testing.T, repository string, runID string) []byte {
	t.Helper()
	journal, err := os.ReadFile(filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return journal
}

func readHarnessEvents(t *testing.T, repository, runID string) []runstate.Event {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	return journal.Events
}

func hashMismatchScore() map[string]any {
	score := runScore()
	criterion := score["movements"].([]any)[0].(map[string]any)["acceptance"].(map[string]any)["hard"].([]any)[0].(map[string]any)
	criterion["expected_hash"] = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	return score
}

func humanGateKillHarnessRepository(t *testing.T, bin, vendor string) (string, []string) {
	score := runScore()
	score["movements"].([]any)[0].(map[string]any)["acceptance"].(map[string]any)["human_gate"] = "always"
	return killHarnessRepositoryWithInputs(t, bin, vendor, score, runCast())
}

func contestedHumanGateKillHarnessRepository(t *testing.T, bin, vendor string) (string, []string) {
	score := runScore()
	movement := score["movements"].([]any)[0].(map[string]any)
	movement["outputs"] = append(movement["outputs"].([]any), map[string]any{"id": "findings", "kind": "findings"})
	acceptance := movement["acceptance"].(map[string]any)
	acceptance["review"] = []any{map[string]any{"id": "review", "findings": "findings", "rubric": []any{"coverage"}}}
	acceptance["human_gate"] = "on_contested"
	repository, baseEnvironment := killHarnessRepositoryWithInputs(t, bin, vendor, score, runCast())
	return repository, replaceEnvironment(baseEnvironment, map[string]string{
		runVendorContestedEnvironment: "1",
		runVendorOutcomeEnvironment:   "success",
	})
}

func killAtPoint(
	t *testing.T,
	binary, repository string,
	environment []string,
	target faultpoint.PointID,
	arguments ...string,
) string {
	t.Helper()
	if len(arguments) == 0 {
		arguments = []string{"run"}
	}
	notifyRead, notifyWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer notifyRead.Close()
	defer notifyWrite.Close()
	releaseRead, releaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer releaseRead.Close()
	defer releaseWrite.Close()

	files := make([]*os.File, 0, 8)
	for range 6 {
		file, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
		defer file.Close()
	}
	files = append(files, notifyWrite, releaseRead)

	var stdout, stderr bytes.Buffer
	command := exec.Command(binary, arguments...)
	command.Dir = repository
	command.Env = replaceEnvironment(environment, map[string]string{
		"PARTITUR_FAULTPOINT_HARNESS":    "1",
		"PARTITUR_FAULTPOINT_NOTIFY_FD":  "9",
		"PARTITUR_FAULTPOINT_RELEASE_FD": "10",
	})
	command.ExtraFiles = files
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	wait := startKillHarnessWait(command)
	defer reapKillHarnessChild(t, command, wait)
	_ = notifyWrite.Close()
	_ = releaseRead.Close()

	scanner := bufio.NewScanner(notifyRead)
	for {
		point, pid, err := scanNextKillPoint(scanner)
		if err != nil {
			t.Fatal(killHarnessScanFailure(arguments[0], target, err, wait))
		}
		if point != target {
			if _, err := releaseWrite.Write([]byte{1}); err != nil {
				t.Fatalf("release %q: %v", point, err)
			}
			continue
		}
		if err := command.Process.Kill(); err != nil {
			t.Fatalf("kill %q at %q: %v", arguments[0], target, err)
		}
		if pid != command.Process.Pid {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		_ = releaseWrite.Close()
		<-wait.done
		if wait.err == nil {
			t.Fatalf("%q at %q exited successfully\nstdout:\n%s\nstderr:\n%s", arguments[0], target, &stdout, &stderr)
		}
		break
	}

	if len(arguments) != 1 || arguments[0] != "run" {
		return ""
	}
	runID := strings.TrimSpace(stdout.String())
	if runID == "" {
		t.Fatalf("run at %q did not publish a run id\nstderr:\n%s", target, &stderr)
	}
	return runID
}

func startKillHarnessWait(command *exec.Cmd) *killHarnessWait {
	wait := &killHarnessWait{done: make(chan struct{})}
	go func() {
		wait.err = command.Wait()
		wait.exitCode = command.ProcessState.ExitCode()
		close(wait.done)
	}()
	return wait
}

func killHarnessScanFailure(command string, target faultpoint.PointID, scanErr error, wait *killHarnessWait) error {
	return killHarnessScanFailureWithin(command, target, scanErr, wait, killHarnessExitObservationTimeout)
}

// killHarnessScanFailureWithin takes the bound so the live-child case can be
// exercised without waiting out the production one. The distinction is what the
// probe-removal mutation anchors on: a child that finished without reaching the
// point is a different fact from a scan error against a child still running.
func killHarnessScanFailureWithin(command string, target faultpoint.PointID, scanErr error, wait *killHarnessWait, within time.Duration) error {
	timer := time.NewTimer(within)
	defer timer.Stop()
	select {
	case <-wait.done:
		return fmt.Errorf("%q completed with exit %d without reaching requested faultpoint %q", command, wait.exitCode, target)
	case <-timer.C:
		return fmt.Errorf("%q probe scan failed while child was still running before requested faultpoint %q: %w", command, target, scanErr)
	}
}

func reapKillHarnessChild(t *testing.T, command *exec.Cmd, wait *killHarnessWait) {
	t.Helper()
	select {
	case <-wait.done:
		return
	default:
	}
	_ = command.Process.Kill()
	select {
	case <-wait.done:
	case <-time.After(killHarnessReapTimeout):
		t.Errorf("timed out after %s reaping kill-harness child pid %d", killHarnessReapTimeout, command.Process.Pid)
	}
}

func TestKillHarnessDistinguishesExitedChildFromLiveScanFailure(t *testing.T) {
	t.Run("exited child", func(t *testing.T) {
		command := exec.Command("sh", "-c", "exit 7")
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		wait := startKillHarnessWait(command)
		err := killHarnessScanFailure("run", faultpoint.PointAcceptanceSubjectPinned, errors.New("read probe: file already closed"), wait)
		if got, want := err.Error(), `"run" completed with exit 7 without reaching requested faultpoint "acceptance.subject_pinned"`; got != want {
			t.Fatalf("failure = %q, want %q", got, want)
		}
	})

	t.Run("live child", func(t *testing.T) {
		command := exec.Command("sh", "-c", "sleep 30")
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		wait := startKillHarnessWait(command)
		defer reapKillHarnessChild(t, command, wait)
		err := killHarnessScanFailureWithin("run", faultpoint.PointAcceptanceSubjectPinned, errors.New("read probe: file already closed"), wait, 50*time.Millisecond)
		if got, want := err.Error(), `"run" probe scan failed while child was still running before requested faultpoint "acceptance.subject_pinned": read probe: file already closed`; got != want {
			t.Fatalf("failure = %q, want %q", got, want)
		}
		if strings.Contains(err.Error(), "completed with exit") {
			t.Fatalf("live-child failure used exited-child signature: %v", err)
		}
	})
}

func nextKillPoint(t *testing.T, scanner *bufio.Scanner) (faultpoint.PointID, int) {
	t.Helper()
	point, pid, err := scanNextKillPoint(scanner)
	if err != nil {
		t.Fatal(err)
	}
	return point, pid
}

func scanNextKillPoint(scanner *bufio.Scanner) (faultpoint.PointID, int, error) {
	type reached struct {
		point faultpoint.PointID
		pid   int
		err   error
	}
	ready := make(chan reached, 1)
	go func() {
		if !scanner.Scan() {
			err := scanner.Err()
			if err == nil {
				err = io.EOF
			}
			ready <- reached{err: err}
			return
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			ready <- reached{err: fmt.Errorf("malformed probe notification %q", scanner.Text())}
			return
		}
		pid, err := strconv.Atoi(fields[1])
		ready <- reached{point: faultpoint.PointID(fields[0]), pid: pid, err: err}
	}()
	select {
	case result := <-ready:
		if result.err != nil || result.point == "" || result.pid <= 0 {
			return "", 0, fmt.Errorf("probe notification = %#v", result)
		}
		return result.point, result.pid, nil
	case <-time.After(15 * time.Second):
		return "", 0, errors.New("timed out waiting for faultpoint probe")
	}
}

func expectedFailureFor(point faultpoint.PointID) *expectedFailure {
	switch point {
	case faultpoint.PointAuthorityGranted, faultpoint.PointAuthorityLeaseCreated:
		return nil
	case faultpoint.PointLaunchAdapterMarkerHeld, faultpoint.PointLaunchAdapterIdentityPublished:
		return &expectedFailure{event: runstate.EventAttemptFailed, kind: "task_failed", reason: "attempt_never_started", terminalReason: "retries_exhausted"}
	case faultpoint.PointLaunchAdapterIdentityRecorded, faultpoint.PointLaunchAdapterGateReleased:
		return &expectedFailure{event: runstate.EventAttemptFailed, kind: "adapter_unavailable", reason: "probe_terminated_incomplete", terminalReason: "fallbacks_exhausted"}
	case faultpoint.PointExecuteAdapterSwept, faultpoint.PointExecuteIntervalStopped:
		return &expectedFailure{event: runstate.EventAttemptFailed, kind: "task_failed", reason: "attempt_terminated_incomplete", terminalReason: "retries_exhausted"}
	case faultpoint.PointExecuteOutcomeRecorded:
		return nil
	case faultpoint.PointLifecycleAttemptCompleted:
		return nil
	case faultpoint.PointLifecycleMovementSucceeded:
		return nil
	case faultpoint.PointLifecycleMovementFailed, faultpoint.PointLifecycleRunFailed:
		return &expectedFailure{event: runstate.EventAttemptFailed, kind: "task_failed", terminalReason: "retries_exhausted", runReason: "movement_failed"}
	case faultpoint.PointAcceptanceNonPassCompleted, faultpoint.PointAcceptanceFailureRecorded:
		return &expectedFailure{event: runstate.EventAcceptanceFailed, reason: "criterion_errored", terminalReason: "retries_exhausted"}
	case faultpoint.PointAcceptanceEvaluationCompleted, faultpoint.PointHumanGateDecisionRequested:
		return nil
	default:
		panic(fmt.Sprintf("no fixed-point failure expectation declared for point %q", point))
	}
}

type crashedStateExpectation uint8

const (
	crashedStateProjectionOnly crashedStateExpectation = iota
	crashedStateMovementFailed
	crashedStateRunFailed
	crashedStateCriterionErrorRecorded
	crashedStateAcceptanceFailureRecorded
	crashedStateCriterionIdentityPublished
)

func crashedStateExpectationFor(point faultpoint.PointID) crashedStateExpectation {
	switch point {
	case faultpoint.PointAuthorityGranted:
		return crashedStateProjectionOnly
	case faultpoint.PointAuthorityLeaseCreated:
		return crashedStateProjectionOnly
	case faultpoint.PointLaunchAdapterMarkerHeld:
		return crashedStateProjectionOnly
	case faultpoint.PointLaunchAdapterIdentityPublished:
		return crashedStateProjectionOnly
	case faultpoint.PointLaunchAdapterIdentityRecorded:
		return crashedStateProjectionOnly
	case faultpoint.PointLaunchAdapterGateReleased:
		return crashedStateProjectionOnly
	case faultpoint.PointLaunchCriterionIdentityPublished:
		return crashedStateCriterionIdentityPublished
	case faultpoint.PointExecuteAdapterSwept:
		return crashedStateProjectionOnly
	case faultpoint.PointExecuteIntervalStopped:
		return crashedStateProjectionOnly
	case faultpoint.PointExecuteOutcomeRecorded:
		return crashedStateProjectionOnly
	case faultpoint.PointLifecycleAttemptCompleted:
		return crashedStateProjectionOnly
	case faultpoint.PointLifecycleMovementSucceeded:
		return crashedStateProjectionOnly
	case faultpoint.PointLifecycleMovementFailed:
		return crashedStateMovementFailed
	case faultpoint.PointLifecycleRunFailed:
		return crashedStateRunFailed
	case faultpoint.PointAcceptanceNonPassCompleted:
		return crashedStateCriterionErrorRecorded
	case faultpoint.PointAcceptanceFailureRecorded:
		return crashedStateAcceptanceFailureRecorded
	case faultpoint.PointAcceptanceEvaluationCompleted, faultpoint.PointHumanGateDecisionRequested:
		return crashedStateProjectionOnly
	default:
		panic(fmt.Sprintf("no crashed-state expectation declared for point %q", point))
	}
}

func assertCrashedStateBeforeResume(t *testing.T, repository, runID string, point faultpoint.PointID) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	state := input.Projection.State

	switch crashedStateExpectationFor(point) {
	case crashedStateProjectionOnly:
		// These points have no lifecycle-window invariant. Loading the durable
		// journal and its projection is the explicit pre-resume observation.
		return
	case crashedStateMovementFailed:
		if !journalContainsEvent(journal.Events, runstate.EventMovementFailed) {
			t.Fatalf("crashed journal at %s has no durable movement.failed", point)
		}
		if journalContainsEvent(journal.Events, runstate.EventRunFailed) {
			t.Fatalf("crashed journal at %s already has run.failed", point)
		}
		if state.Run.Terminal() {
			t.Fatalf("crashed projection at %s is terminal: %q", point, state.Run)
		}
	case crashedStateRunFailed:
		// The edge is ordered: run.failed alone would satisfy Apply, which
		// accepts a run failure from RUNNING without a failed movement.
		if !journalContainsEvent(journal.Events, runstate.EventMovementFailed) {
			t.Fatalf("crashed journal at %s has no durable movement.failed before run.failed", point)
		}
		if !journalContainsEvent(journal.Events, runstate.EventRunFailed) {
			t.Fatalf("crashed journal at %s has no durable run.failed", point)
		}
		if !state.Run.Terminal() {
			t.Fatalf("crashed projection at %s is nonterminal: %q", point, state.Run)
		}
	case crashedStateCriterionErrorRecorded:
		assertCriterionErrorEndpoint(t, journal.Events, false)
	case crashedStateAcceptanceFailureRecorded:
		assertCriterionErrorEndpoint(t, journal.Events, true)
	case crashedStateCriterionIdentityPublished:
		assertCriterionIdentityPublishedBeforeRecord(t, repository, runID, journal.Events)
	default:
		t.Fatalf("unknown crashed-state expectation for point %q", point)
	}
}

// assertCriterionIdentityPublishedBeforeRecord locks the precondition of the
// cross-edge semantic fixture: the criterion handoff is durable enough to
// identify a process, but its criterion.started receipt has not been written.
func assertCriterionIdentityPublishedBeforeRecord(t *testing.T, repository, runID string, events []runstate.Event) {
	t.Helper()
	const criterionID = "command-passes"

	launchDirectories, err := filepath.Glob(filepath.Join(repository, ".partitur", "work", runID, "*", "criterion-"+criterionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(launchDirectories) != 1 {
		t.Fatalf("crashed criterion handoff directories=%d, want one", len(launchDirectories))
	}
	observation, err := launch.ObserveHandoff(launchDirectories[0])
	if err != nil {
		t.Fatalf("observe crashed criterion handoff: %v", err)
	}
	if !observation.HasIdentity {
		t.Fatalf("crashed criterion handoff at %s has no published identity", launchDirectories[0])
	}
	for _, event := range events {
		if event.Type != runstate.EventCriterionStarted {
			continue
		}
		payload := decodeHarnessEventPayload(t, event)
		if payload["criterion_id"] == criterionID {
			t.Fatalf("crashed journal at %s already has criterion.started for %q", faultpoint.PointLaunchCriterionIdentityPublished, criterionID)
		}
	}
}

func assertCriterionErrorEndpoint(t *testing.T, events []runstate.Event, wantAcceptanceFailure bool) {
	t.Helper()
	if err := validateCriterionErrorEndpoint(events, wantAcceptanceFailure); err != nil {
		t.Fatal(err)
	}
}

func validateCriterionErrorEndpoint(events []runstate.Event, wantAcceptanceFailure bool) error {
	const wantCriterionID = "command-passes"

	errorCount := 0
	errorIndex := -1
	errorCriterionID := ""
	errorDetail := ""
	for index, event := range events {
		if event.Type != runstate.EventCriterionCompleted {
			continue
		}
		payload, err := decodeCriterionErrorPayload(event)
		if err != nil {
			return err
		}
		if payload["outcome"] != "ERROR" {
			continue
		}
		errorCount++
		errorIndex = index
		errorCriterionID, _ = payload["criterion_id"].(string)
		errorDetail, _ = payload["error_detail"].(string)
	}
	if errorCount != 1 {
		return fmt.Errorf("criterion.completed ERROR count=%d, want one", errorCount)
	}
	if errorDetail != "workspace_verification_failed" {
		return fmt.Errorf("criterion.completed ERROR detail=%q, want workspace_verification_failed", errorDetail)
	}
	if errorCriterionID != wantCriterionID {
		return fmt.Errorf("criterion.completed ERROR criterion_id=%q, want %q", errorCriterionID, wantCriterionID)
	}
	for _, event := range events[errorIndex+1:] {
		if event.Type == runstate.EventCriterionStarted {
			return errors.New("criterion.started appears after criterion.completed ERROR")
		}
	}

	failureCount := 0
	failureCriterionID := ""
	failureReason := ""
	for _, event := range events {
		if event.Type != runstate.EventAcceptanceFailed {
			continue
		}
		failureCount++
		payload, err := decodeCriterionErrorPayload(event)
		if err != nil {
			return err
		}
		failureCriterionID, _ = payload["failed_criterion_id"].(string)
		failureReason, _ = payload["reason"].(string)
	}
	if !wantAcceptanceFailure {
		if failureCount != 0 {
			return fmt.Errorf("acceptance.failed count=%d at left endpoint, want zero", failureCount)
		}
		return nil
	}
	if failureCount != 1 {
		return fmt.Errorf("acceptance.failed count=%d at right endpoint, want one", failureCount)
	}
	if failureCriterionID != errorCriterionID {
		return fmt.Errorf("acceptance.failed criterion_id=%q, want erroring criterion %q", failureCriterionID, errorCriterionID)
	}
	if failureReason != "criterion_errored" {
		return fmt.Errorf("acceptance.failed reason=%q, want criterion_errored", failureReason)
	}
	return nil
}

func decodeCriterionErrorPayload(event runstate.Event) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return nil, fmt.Errorf("decode %s payload: %w", event.Type, err)
	}
	return payload, nil
}

func decodeHarnessEventPayload(t *testing.T, event runstate.Event) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("decode %s payload: %v", event.Type, err)
	}
	return payload
}

func journalContainsEvent(events []runstate.Event, want runstate.EventType) bool {
	for _, event := range events {
		if event.Type == want {
			return true
		}
	}
	return false
}

func assertRecoveryFixedPoint(t *testing.T, binary, repository string, environment []string, runID string, expected *expectedFailure, fixture fixedPointFixture) {
	t.Helper()
	code, stdout, stderr := runCommandBinary(t, binary, repository, environment, "resume", runID)
	if code == 5 {
		if !strings.Contains(stderr, "recovery halted: run_id=") || !strings.Contains(stderr, "reason=") || stdout != "" {
			t.Fatalf("halt exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		return
	}
	if code != 0 && code != 4 || stdout != "" || stderr != "" {
		t.Fatalf("resume exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	journal := filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl")
	first, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if code == 4 {
		if expected == nil {
			t.Fatalf("unexpected failed fixed point: journal=%s", first)
		}
		assertExpectedFailure(t, first, *expected)
	} else if expected != nil {
		t.Fatalf("resume exit=%d, want failed fixed point %s/%s", code, expected.kind, expected.reason)
	}
	code, stdout, stderr = runCommandBinary(t, binary, repository, environment, "resume", runID)
	assertFixedPointReplayResult(t, code, stdout, stderr, binary, repository, environment, runID, first, fixture)
}

func assertFixedPointReplay(
	t *testing.T,
	binary, repository string,
	environment []string,
	runID string,
	first []byte,
	fixture fixedPointFixture,
) {
	t.Helper()
	code, stdout, stderr := runCommandBinary(t, binary, repository, environment, "resume", runID)
	assertFixedPointReplayResult(t, code, stdout, stderr, binary, repository, environment, runID, first, fixture)
}

func assertFixedPointReplayResult(
	t *testing.T,
	code int,
	stdout, stderr, binary, repository string,
	environment []string,
	runID string,
	first []byte,
	fixture fixedPointFixture,
) {
	t.Helper()
	if code != 0 && code != 4 || stdout != "" || stderr != "" {
		t.Fatalf("fixed-point replay exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	journal := filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl")
	second, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("fixed-point replay appended duplicate durable events")
	}
	assertSettledFixedPointState(t, repository, runID, fixture)
	if _, err := os.Stat(filepath.Join(repository, ".partitur", "work", runID)); !os.IsNotExist(err) {
		if !runWaitingForHumanGate(t, repository, runID) {
			t.Fatalf("attempt worktree after fixed-point recovery = %v", err)
		}
	}
}

func runWaitingForHumanGate(t *testing.T, repository, runID string) bool {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Run != runstate.RunWaitingHuman {
		return false
	}
	for _, decision := range input.Projection.State.PendingDecisions {
		if decision.Type == "human_gate" && decision.Blocking {
			return true
		}
	}
	return false
}

// assertSettledFixedPointState derives fixed-point checks from DESIGN §6's
// durable projection rather than inferring quiescence from the journal bytes.
// The fixture declares command-specific recovery before the projection is
// inspected so an unexpected unsettled projection cannot exempt itself.
func assertSettledFixedPointState(t *testing.T, repository, runID string, fixture fixedPointFixture) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	state := input.Projection.State
	if state.OpenExecution != nil {
		t.Fatalf("open execution interval after fixed-point recovery = %+v", *state.OpenExecution)
	}
	if state.PendingPrepare != nil {
		t.Fatalf("pending prepare after fixed-point recovery = %+v", *state.PendingPrepare)
	}
	if err := fixedPointRecoveryBranchError(fixture, state); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	assertSettledLifecycle(t, state)
	assertRecordedSessionsEmpty(t, state)
	gateModes := make(map[runstate.MovementID]string)
	for _, movement := range input.Score.Movements() {
		gateModes[runstate.MovementID(movement.ID)] = movement.Acceptance.HumanGate
	}
	assertRecoveryOutcomeImplications(t, journal.Events, gateModes, state)
	assertNoResumeOwnedResiduals(t, repository, runID, state)
	assertSettledCandidateAndRefs(t, repository, runID, input.BaseCommit, state)
}

// fixedPointRecoveryBranchError makes the convergence partition explicit:
// a named halt returns before this function; surviving resume is either a
// human wait, ordinary terminal convergence, or the fixture-declared
// command-specific recovery state.
func fixedPointRecoveryBranchError(fixture fixedPointFixture, state runstate.State) error {
	if state.Run == runstate.RunWaitingHuman {
		if fixture.commandSpecificRecovery != fixedPointRecoveryNone {
			return fmt.Errorf("WAITING_HUMAN fixed point declares command-specific recovery %q", fixture.commandSpecificRecovery)
		}
		return fixedPointCommandSpecificProjectionError(fixedPointRecoveryNone, state)
	}
	if !state.Run.Terminal() {
		return fmt.Errorf("non-halted fixed point lifecycle = %q", state.Run)
	}
	return fixedPointCommandSpecificProjectionError(fixture.commandSpecificRecovery, state)
}

// assertFixedPointFixtureBranch is used by the apply and promotion fixtures to
// prove that each command-specific exception has a real durable witness before
// the exception is enabled in the broader resume fixed-point helper.
func assertFixedPointFixtureBranch(t *testing.T, repository, runID string, fixture fixedPointFixture) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixedPointRecoveryBranchError(fixture, input.Projection.State); err != nil {
		t.Fatal(err)
	}
}

func fixedPointCommandSpecificProjectionError(declaration fixedPointCommandSpecificRecovery, state runstate.State) error {
	switch declaration {
	case fixedPointRecoveryNone:
		// Keep the pre-reconciliation assertion exactly: ordinary convergence
		// admits neither in-progress nor recovery-required command state.
		if state.Application.State == runstate.ApplicationApplying || state.Application.State == runstate.ApplicationRecoveryRequired {
			return fmt.Errorf("unsettled application projection after fixed-point recovery = %+v", state.Application)
		}
		if state.Promotion.State == runstate.PromotionPromoting || state.Promotion.State == runstate.PromotionRecoveryRequired {
			return fmt.Errorf("unsettled promotion projection after fixed-point recovery = %+v", state.Promotion)
		}
		return nil
	case fixedPointRecoveryApplication:
		if state.Application.State != runstate.ApplicationRecoveryRequired {
			return fmt.Errorf("application recovery declaration requires RECOVERY_REQUIRED application projection, got %+v", state.Application)
		}
		if state.Promotion.State == runstate.PromotionPromoting || state.Promotion.State == runstate.PromotionRecoveryRequired {
			return fmt.Errorf("application recovery declaration retains unsettled promotion projection = %+v", state.Promotion)
		}
		return nil
	case fixedPointRecoveryPromotion:
		if state.Application.State == runstate.ApplicationApplying || state.Application.State == runstate.ApplicationRecoveryRequired {
			return fmt.Errorf("promotion recovery declaration retains unsettled application projection = %+v", state.Application)
		}
		if state.Promotion.State != runstate.PromotionRecoveryRequired {
			return fmt.Errorf("promotion recovery declaration requires RECOVERY_REQUIRED promotion projection, got %+v", state.Promotion)
		}
		return nil
	default:
		return fmt.Errorf("unknown command-specific recovery declaration %q", declaration)
	}
}

func assertSettledLifecycle(t *testing.T, state runstate.State) {
	t.Helper()
	if !state.Run.Terminal() {
		if state.Run != runstate.RunWaitingHuman || len(state.PendingDecisions) == 0 {
			t.Fatalf("run lifecycle after fixed-point recovery = %q with pending decisions=%d", state.Run, len(state.PendingDecisions))
		}
		blocking := false
		for _, decision := range state.PendingDecisions {
			blocking = blocking || decision.Blocking
		}
		if !blocking {
			t.Fatalf("WAITING_HUMAN fixed point has no blocking decision: %#v", state.PendingDecisions)
		}
	} else if len(state.PendingDecisions) != 0 {
		t.Fatalf("terminal run retains pending decisions: %#v", state.PendingDecisions)
	}
	for movementID, movement := range state.Movements {
		switch movement {
		case runstate.MovementSucceeded, runstate.MovementFailed, runstate.MovementCancelled, runstate.MovementInapplicable:
		case runstate.MovementWaitingHuman:
			if state.Run != runstate.RunWaitingHuman {
				t.Fatalf("movement %q waits for human while run=%q", movementID, state.Run)
			}
		default:
			t.Fatalf("unsettled movement %q after fixed-point recovery = %q", movementID, movement)
		}
	}
	for attemptID, attempt := range state.Attempts {
		switch attempt.State {
		case runstate.AttemptCompleted, runstate.AttemptBlocked, runstate.AttemptFailed, runstate.AttemptCancelled, runstate.AttemptSuperseded:
		case runstate.AttemptVerifying:
			if state.Run == runstate.RunWaitingHuman && state.Movements[attempt.MovementID] == runstate.MovementWaitingHuman &&
				hasPendingHumanGate(state, attemptID) {
				continue
			}
			fallthrough
		default:
			t.Fatalf("unsettled attempt %q after fixed-point recovery = %q", attemptID, attempt.State)
		}
	}
}

func hasPendingHumanGate(state runstate.State, attemptID runstate.AttemptID) bool {
	for _, decision := range state.PendingDecisions {
		if decision.AttemptID == attemptID && decision.Type == "human_gate" && decision.Blocking {
			return true
		}
	}
	return false
}

func assertRecordedSessionsEmpty(t *testing.T, state runstate.State) {
	t.Helper()
	for attemptID, launch := range state.AdapterLaunches {
		assertSessionEmpty(t, "adapter attempt "+string(attemptID), launch.Process)
	}
	for key, launch := range state.CriterionLaunches {
		spawned, ok := launch.(runstate.SpawnedCriterionLaunch)
		if ok {
			assertSessionEmpty(t, "criterion "+string(key.CriterionID), spawned.Process)
		}
	}
}

func assertSessionEmpty(t *testing.T, label string, identity runstate.ProcessIdentity) {
	t.Helper()
	empty, err := adapter.SessionEmpty(identity)
	if err != nil || !empty {
		t.Fatalf("recorded %s session after fixed-point recovery: empty=%t err=%v", label, empty, err)
	}
}

// assertRecoveryOutcomeImplications checks durable effects, rather than which
// recovery case selected them. The four implications are intentionally named
// here because Appendix E owns their edges while convergence proves their
// effects were realized after recovery.
func assertRecoveryOutcomeImplications(t *testing.T, events []runstate.Event, gateModes map[runstate.MovementID]string, state runstate.State) {
	t.Helper()
	if err := recoveryOutcomeImplicationsError(events, gateModes, state); err != nil {
		t.Fatal(err)
	}
}

func recoveryOutcomeImplicationsError(events []runstate.Event, gateModes map[runstate.MovementID]string, state runstate.State) error {
	for index, event := range events {
		switch event.Type {
		case runstate.EventAttemptCompleted:
			if !hasEventAfter(events, index, runstate.EventMovementSucceeded, event.MovementID, event.AttemptID) ||
				state.Movements[event.MovementID] != runstate.MovementSucceeded {
				return fmt.Errorf("completed attempt %q has no durable movement.succeeded effect", event.AttemptID)
			}
		case runstate.EventMovementFailed:
			if !movementFailureProjectsRunFailure(events, index, event, state) {
				return fmt.Errorf("failed movement %q has no durable run.failed effect", event.MovementID)
			}
		case runstate.EventCriterionCompleted:
			payload, err := decodeFixedPointPayload(event)
			if err != nil {
				return err
			}
			if payload["outcome"] != "ERROR" {
				continue
			}
			attempt, present := state.Attempts[event.AttemptID]
			if !hasEventAfter(events, index, runstate.EventAcceptanceFailed, event.MovementID, event.AttemptID) ||
				!present || attempt.State != runstate.AttemptFailed {
				return fmt.Errorf("criterion ERROR for attempt %q has no durable acceptance.failed effect", event.AttemptID)
			}
		case runstate.EventAcceptanceEvaluationCompleted:
			payload, err := decodeFixedPointPayload(event)
			if err != nil {
				return err
			}
			gateMode := gateModes[event.MovementID]
			gateRequired := gateMode == "always" || (gateMode == "on_contested" && payload["review_outcome"] == "CONTESTED")
			if !gateRequired {
				continue
			}
			if !hasHumanGateDecisionAfter(events, index, event.MovementID, event.AttemptID) || !humanGateProjects(state, event.MovementID, event.AttemptID) {
				return fmt.Errorf("completed evaluation for attempt %q has no durable human-gate request effect", event.AttemptID)
			}
		}
	}
	return nil
}

func movementFailureProjectsRunFailure(events []runstate.Event, index int, event runstate.Event, state runstate.State) bool {
	if state.Run != runstate.RunFailed {
		return false
	}
	if hasEventAfter(events, index, runstate.EventRunFailed, "", "") {
		return true
	}
	payload, err := decodeFixedPointPayload(event)
	return err == nil && payload["run_failed"] == true
}

func hasEventAfter(events []runstate.Event, after int, want runstate.EventType, movementID runstate.MovementID, attemptID runstate.AttemptID) bool {
	for _, event := range events[after+1:] {
		if event.Type != want {
			continue
		}
		if movementID != "" && event.MovementID != movementID {
			continue
		}
		if attemptID != "" && event.AttemptID != attemptID {
			continue
		}
		return true
	}
	return false
}

func hasHumanGateDecisionAfter(events []runstate.Event, after int, movementID runstate.MovementID, attemptID runstate.AttemptID) bool {
	for _, event := range events[after+1:] {
		if event.Type != runstate.EventDecisionRequested || event.MovementID != movementID || event.AttemptID != attemptID {
			continue
		}
		payload, err := decodeFixedPointPayload(event)
		if err == nil && payload["decision_type"] == "human_gate" {
			return true
		}
	}
	return false
}

func humanGateProjects(state runstate.State, movementID runstate.MovementID, attemptID runstate.AttemptID) bool {
	for _, decision := range state.PendingDecisions {
		if decision.Type == "human_gate" && decision.MovementID == movementID && decision.AttemptID == attemptID {
			return true
		}
	}
	resolution, present := state.ResolvedHumanGates[attemptID]
	return present && resolution.MovementID == movementID
}

func decodeFixedPointPayload(event runstate.Event) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return nil, fmt.Errorf("decode fixed-point %s payload: %w", event.Type, err)
	}
	return payload, nil
}

type resumeOwnedResidue struct {
	family string
	path   string
}

// resumeOwnedResiduals is a mechanical mirror of recovery terminalCleanup:
// the lease, quiesce sidecars, prepare-plan staging, and the per-run work
// staging tree are the families resume owns. Apply and promotion use their
// own command paths and are deliberately not enumerated here.
func resumeOwnedResiduals(repository, runID string) ([]resumeOwnedResidue, error) {
	runRoot := filepath.Join(repository, ".partitur", "runs", runID)
	entries, err := os.ReadDir(runRoot)
	if err != nil {
		return nil, fmt.Errorf("read fixed-point run root: %w", err)
	}
	residues := make([]resumeOwnedResidue, 0)
	lease := filepath.Join(runRoot, "driver.lease")
	if _, err := os.Lstat(lease); err == nil {
		residues = append(residues, resumeOwnedResidue{family: "lease", path: lease})
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat fixed-point driver lease: %w", err)
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), "driver.quiesced.") && entry.Name() != "driver.quiesced." {
			residues = append(residues, resumeOwnedResidue{family: "sidecar", path: filepath.Join(runRoot, entry.Name())})
		}
	}
	prepares := filepath.Join(runRoot, "prepares")
	entries, err = os.ReadDir(prepares)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read fixed-point prepare staging: %w", err)
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
			residues = append(residues, resumeOwnedResidue{family: "prepare staging", path: filepath.Join(prepares, entry.Name())})
		}
	}
	work := filepath.Join(repository, ".partitur", "work", runID)
	if _, err := os.Lstat(work); err == nil {
		residues = append(residues, resumeOwnedResidue{family: "attempt staging", path: work})
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat fixed-point attempt staging: %w", err)
	}
	return residues, nil
}

func assertNoResumeOwnedResiduals(t *testing.T, repository, runID string, state runstate.State) {
	t.Helper()
	residues, err := resumeOwnedResiduals(repository, runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, residue := range residues {
		// A human-gate wait retains the attempt worktree deliberately so the
		// operator can inspect the accepted subject. The existing caller below
		// still rejects that directory for every other state.
		if residue.family == "attempt staging" && state.Run == runstate.RunWaitingHuman {
			continue
		}
		t.Fatalf("resume-owned fixed-point residue = %+v", residue)
	}
}

func assertSettledCandidateAndRefs(t *testing.T, repository, runID, baseCommit string, state runstate.State) {
	t.Helper()
	baseRef := "refs/partitur/runs/" + runID + "/base"
	if got, want := gitRefCommit(t, repository, baseRef), strings.TrimPrefix(baseCommit, "git-sha1:"); got != want {
		t.Fatalf("base ref = %q, want %q", got, want)
	}
	if state.Run == runstate.RunSucceeded && state.ApplicationCandidate == nil {
		t.Fatal("succeeded run has no application candidate")
	}
	if state.ApplicationCandidate != nil {
		gitRefCommit(t, repository, "refs/partitur/runs/"+runID+"/candidate")
	}
	for attemptID, changeSet := range state.ChangeSets {
		if changeSet.Ref == "" {
			t.Fatalf("change set for attempt %q has no durable ref", attemptID)
		}
		gitRefCommit(t, repository, changeSet.Ref)
	}
	refs := exec.Command("git", "for-each-ref", "--format=%(refname)", "refs/partitur/runs/"+runID)
	refs.Dir = repository
	output, err := refs.Output()
	if err != nil {
		t.Fatalf("list durable run refs: %v", err)
	}
	for _, ref := range strings.Fields(string(output)) {
		gitRefCommit(t, repository, ref)
	}
}

func gitRefCommit(t *testing.T, repository, ref string) string {
	t.Helper()
	command := exec.Command("git", "rev-parse", ref+"^{commit}")
	command.Dir = repository
	output, err := command.Output()
	if err != nil {
		t.Fatalf("durable ref %q is invalid: %v", ref, err)
	}
	return strings.TrimSpace(string(output))
}

func assertExpectedFailure(t *testing.T, journal []byte, expected expectedFailure) {
	t.Helper()
	if expected.event == runstate.EventRunFailed {
		assertRunFailedReason(t, journal, expected.runReason)
		return
	}
	scanner := bufio.NewScanner(bytes.NewReader(journal))
	var last *runstate.Event
	for scanner.Scan() {
		var event runstate.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == runstate.EventAttemptFailed || event.Type == runstate.EventAcceptanceFailed {
			copy := event
			last = &copy
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if last == nil {
		t.Fatalf("failed fixed point has no recorded attempt or acceptance failure: journal=%s", journal)
	}
	var payload map[string]any
	if err := json.Unmarshal(last.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if expected.event == "" {
		expected.event = runstate.EventAttemptFailed
	}
	kind, _ := payload["kind"].(string)
	reason, _ := payload["reason"].(string)
	if last.Type != expected.event || kind != expected.kind || reason != expected.reason {
		t.Fatalf("recorded failure = type=%q kind=%q reason=%q, want %q/%q/%q", last.Type, kind, reason, expected.event, expected.kind, expected.reason)
	}
	assertValidTerminalDisposition(t, payload, expected)
	if expected.runReason != "" {
		assertRunFailedReason(t, journal, expected.runReason)
	}
}

func assertRunFailedReason(t *testing.T, journal []byte, want string) {
	t.Helper()
	var last *runstate.Event
	scanner := bufio.NewScanner(bytes.NewReader(journal))
	for scanner.Scan() {
		var event runstate.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == runstate.EventRunFailed {
			copy := event
			last = &copy
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if last == nil {
		t.Fatalf("failed fixed point has no run.failed: journal=%s", journal)
	}
	var payload map[string]any
	if err := json.Unmarshal(last.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if got, _ := payload["reason"].(string); got != want {
		t.Fatalf("run.failed reason=%q, want %q", got, want)
	}
}

func assertValidTerminalDisposition(t *testing.T, payload map[string]any, expected expectedFailure) {
	t.Helper()
	disposition, ok := payload["disposition"].(map[string]any)
	if !ok || disposition["charged"] != "none" || disposition["movement_terminal"] != true || disposition["terminal_reason"] != expected.terminalReason {
		t.Fatalf("failure %s/%s has invalid terminal disposition %#v", expected.kind, expected.reason, disposition)
	}
}
