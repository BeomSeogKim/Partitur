package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

type cancellationObligation struct {
	edge       faultpoint.EdgeID
	predicates cancellationFixturePredicates
	side       string
}

func (obligation cancellationObligation) String() string {
	return fmt.Sprintf("%s/%s/%s", obligation.edge, cancellationFixtureName(obligation.predicates), obligation.side)
}

func TestCancellationKillMatrix(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	required := requiredCancellationObligations(t)
	scenarios := cancellationKillScenarios(t, required)
	discharged := make(map[cancellationObligation]bool, len(required))
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			repository, environment, runID, _ := cancellationFixture(t, bin, vendor, scenario.predicates, faultpoint.Nop{})
			killAtPoint(t, partitur, repository, environment, scenario.point, "cancel", runID)
			if scenario.point == faultpoint.PointCancelFenceDecided && scenario.predicates.matchingLease {
				assertFenceDecisionDurableState(t, repository, runID)
			}
			if scenario.point == faultpoint.PointCancelRunCancelled && scenario.predicates.matchingLease {
				assertCancellationTerminalRetainsFencedLease(t, repository, runID)
			}
			assertCancellationRecoveryFixedPoint(t, partitur, repository, environment, runID, scenario.predicates)
			for _, obligation := range scenario.obligations {
				discharged[obligation] = true
			}
		})
	}
	assertCancellationObligationCoverage(t, required, discharged)
}

type cancellationKillScenario struct {
	name        string
	predicates  cancellationFixturePredicates
	point       faultpoint.PointID
	obligations []cancellationObligation
}

func cancellationKillScenarios(t *testing.T, obligations map[cancellationObligation]bool) []cancellationKillScenario {
	t.Helper()
	byCut := make(map[string]*cancellationKillScenario)
	for obligation := range obligations {
		edge, ok := cancellationKillEdge(obligation.edge)
		if !ok {
			t.Fatalf("required cancellation edge %q has no cut mapping", obligation.edge)
		}
		point := edge.before
		if obligation.side == "after" {
			point = edge.after
		}
		key := cancellationFixtureName(obligation.predicates) + "/" + string(point)
		scenario := byCut[key]
		if scenario == nil {
			scenario = &cancellationKillScenario{
				name:       key,
				predicates: obligation.predicates,
				point:      point,
			}
			byCut[key] = scenario
		}
		scenario.obligations = append(scenario.obligations, obligation)
	}
	if len(byCut) == 0 {
		t.Fatal("cancellation matrix produced no kill cuts")
	}
	keys := make([]string, 0, len(byCut))
	for key := range byCut {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	scenarios := make([]cancellationKillScenario, 0, len(keys))
	for _, key := range keys {
		scenario := byCut[key]
		sort.Slice(scenario.obligations, func(left, right int) bool {
			return scenario.obligations[left].String() < scenario.obligations[right].String()
		})
		scenarios = append(scenarios, *scenario)
	}
	return scenarios
}

func requiredCancellationObligations(t *testing.T) map[cancellationObligation]bool {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "HARNESS.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(contents), "\n")
	start, end := tableBoundsCounted(t, lines, "### Cancellation", "### Supersession fencing", 1, 1)
	rows := make(map[faultpoint.EdgeID]string)
	for _, line := range lines[start:end] {
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "| Edge |") || strings.HasPrefix(line, "|---") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) != 6 {
			t.Fatalf("unparseable HARNESS cancellation row %q", line)
		}
		id := faultpoint.EdgeID(strings.Trim(strings.TrimSpace(cells[1]), "`"))
		selection := strings.TrimSpace(cells[4])
		if id == "" || selection == "" || rows[id] != "" {
			t.Fatalf("unparseable or duplicate HARNESS cancellation row %q", line)
		}
		if _, ok := cancellationKillEdge(id); !ok {
			t.Fatalf("unparseable HARNESS cancellation edge %q", id)
		}
		rows[id] = selection
	}
	if len(rows) == 0 {
		t.Fatal("HARNESS cancellation extraction produced no rows")
	}
	if len(rows) != 5 {
		t.Fatalf("HARNESS cancellation edge count=%d, want five", len(rows))
	}

	required := make(map[cancellationObligation]bool)
	for edge, selection := range rows {
		for _, predicates := range cancellationPredicatesForSelection(t, edge, selection) {
			required[cancellationObligation{edge: edge, predicates: predicates, side: "before"}] = true
			required[cancellationObligation{edge: edge, predicates: predicates, side: "after"}] = true
		}
	}
	if len(required) == 0 {
		t.Fatal("HARNESS cancellation obligation extraction produced no obligations")
	}
	if len(required) != 48 {
		t.Fatalf("HARNESS cancellation endpoint obligations=%d, want 48", len(required))
	}
	return required
}

func cancellationPredicatesForSelection(t *testing.T, edge faultpoint.EdgeID, selection string) []cancellationFixturePredicates {
	t.Helper()
	all := cancellationFixtureCombinations()

	var selected []cancellationFixturePredicates
	switch {
	case strings.HasPrefix(selection, "**all eight."):
		selected = all
	case strings.HasPrefix(selection, "all four with `(b)` true"):
		for _, predicates := range all {
			if predicates.preparePending {
				selected = append(selected, predicates)
			}
		}
	case strings.HasPrefix(selection, "all four with `(c)` true"):
		for _, predicates := range all {
			if predicates.intervalOpen {
				selected = append(selected, predicates)
			}
		}
	case strings.HasPrefix(selection, "all four with `(d)` true"):
		for _, predicates := range all {
			if predicates.matchingLease {
				selected = append(selected, predicates)
			}
		}
	default:
		t.Fatalf("unparseable HARNESS cancellation selection for %q: %q", edge, selection)
	}
	if len(selected) == 0 {
		t.Fatalf("HARNESS cancellation selection for %q produced no combinations", edge)
	}
	return selected
}

func cancellationKillEdge(id faultpoint.EdgeID) (killEdge, bool) {
	for _, edge := range killHarnessEdges() {
		if edge.id == id && strings.HasPrefix(string(id), "cancel.") {
			return edge, true
		}
	}
	return killEdge{}, false
}

func assertCancellationObligationCoverage(t *testing.T, required, discharged map[cancellationObligation]bool) {
	t.Helper()
	if len(required) != len(discharged) {
		t.Fatalf("cancellation obligation coverage=%d, want %d; missing=%s", len(discharged), len(required), cancellationObligationDifference(required, discharged))
	}
	for obligation := range required {
		if !discharged[obligation] {
			t.Fatalf("cancellation obligation was not discharged: %s", obligation)
		}
	}
	for obligation := range discharged {
		if !required[obligation] {
			t.Fatalf("cancellation test discharged an undeclared obligation: %s", obligation)
		}
	}
}

func cancellationObligationDifference(required, discharged map[cancellationObligation]bool) string {
	missing := make([]string, 0)
	for obligation := range required {
		if !discharged[obligation] {
			missing = append(missing, obligation.String())
		}
	}
	sort.Strings(missing)
	return strings.Join(missing, ", ")
}

func assertFenceDecisionDurableState(t *testing.T, repository, runID string) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRecoveryInput(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Authority.Epoch != 2 {
		t.Fatalf("fence decision advanced projected authority epoch=%d, want 2", input.Projection.State.Authority.Epoch)
	}
	lease, present, err := store.ReadLease(runstate.RunID(runID))
	if err != nil || !present || lease.Epoch != input.Projection.State.Authority.Epoch {
		t.Fatalf("fence decision did not retain matching lease: present=%t lease=%+v authority=%d err=%v", present, lease, input.Projection.State.Authority.Epoch, err)
	}
	if _, err := os.Stat(filepath.Join(repository, ".partitur", "runs", runID, "authority.json")); !os.IsNotExist(err) {
		t.Fatalf("fence decision published authority.json before journal terminalization: %v", err)
	}
	journal, err := store.ReadJournal(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.Type == runstate.EventRunCancelled {
			t.Fatalf("fence decision journaled terminal event before recovery: %+v", event)
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if epoch, present := payload["fenced_epoch"]; present {
			t.Fatalf("persistent state claims advanced epoch before fencing terminal event: event=%s fenced_epoch=%v", event.Type, epoch)
		}
	}
}

func assertCancellationTerminalRetainsFencedLease(t *testing.T, repository, runID string) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRecoveryInput(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Run != runstate.RunCancelled || input.Projection.State.Authority.Epoch != 3 {
		t.Fatalf("fenced terminal state run=%q authority=%d", input.Projection.State.Run, input.Projection.State.Authority.Epoch)
	}
	lease, present, err := store.ReadLease(runstate.RunID(runID))
	if err != nil || !present || lease.Epoch != 2 {
		t.Fatalf("fenced terminal did not retain old lease before cleanup: present=%t lease=%+v err=%v", present, lease, err)
	}
}

func assertCancellationRecoveryFixedPoint(t *testing.T, binary, repository string, environment []string, runID string, predicates cancellationFixturePredicates) {
	t.Helper()
	code, stdout, stderr := runCommandBinaryWithin(t, 30*time.Second, binary, repository, environment, "resume", runID)
	if code != 4 || stdout != "" || stderr != "" {
		t.Fatalf("cancellation recovery exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	journal := readHarnessJournal(t, repository, runID)
	assertCancellationJournalEffects(t, journal, predicates)
	assertCancellationDurableFixedPoint(t, repository, runID, predicates)
	code, stdout, stderr = runCommandBinaryWithin(t, 30*time.Second, binary, repository, environment, "resume", runID)
	if code != 4 || stdout != "" || stderr != "" {
		t.Fatalf("cancellation fixed-point replay exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if replay := readHarnessJournal(t, repository, runID); !bytes.Equal(journal, replay) {
		t.Fatal("cancellation fixed-point replay appended duplicate durable events")
	}
}

func assertCancellationJournalEffects(t *testing.T, journal []byte, predicates cancellationFixturePredicates) {
	t.Helper()
	events := decodeCancellationJournal(t, journal)
	request := -1
	for index, event := range events {
		if event.Type == runstate.EventCancelRequested {
			request = index
		}
	}
	if request < 0 {
		t.Fatalf("cancellation journal lacks cancel.requested: %s", journal)
	}
	tail := events[request+1:]
	wantTypes := []runstate.EventType{}
	if predicates.preparePending {
		wantTypes = append(wantTypes, runstate.EventAmendmentApprovalAbandoned)
	}
	if predicates.intervalOpen {
		wantTypes = append(wantTypes, runstate.EventExecutionStopped)
	}
	wantTypes = append(wantTypes, runstate.EventRunCancelled)
	if len(tail) != len(wantTypes) {
		t.Fatalf("cancellation journal tail length=%d want=%d tail=%+v", len(tail), len(wantTypes), tail)
	}
	for index, event := range tail {
		if event.Type != wantTypes[index] {
			t.Fatalf("cancellation journal tail[%d]=%q want=%q", index, event.Type, wantTypes[index])
		}
	}
	for _, event := range tail {
		assertCancellationEventPayload(t, event, predicates)
	}
}

func decodeCancellationJournal(t *testing.T, journal []byte) []runstate.Event {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(journal))
	events := make([]runstate.Event, 0)
	for scanner.Scan() {
		var event runstate.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("cancellation journal is empty")
	}
	return events
}

func assertCancellationEventPayload(t *testing.T, event runstate.Event, predicates cancellationFixturePredicates) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	switch event.Type {
	case runstate.EventAmendmentApprovalAbandoned:
		if payload["prepare_id"] != "prepare-1" || payload["proposal_id"] != "proposal-1" || payload["reason"] != "cancelled" {
			t.Fatalf("approval_abandoned payload=%v", payload)
		}
	case runstate.EventExecutionStopped:
		charged, ok := payload["charged_duration"].(float64)
		if !ok || charged < 0 || charged > 600000 || payload["interval_id"] != "interval-1" || payload["reason"] != "cancelled" || payload["charging"] != "clamped" || payload["observed_at"] == "" || event.CausationID == "" {
			t.Fatalf("execution.stopped payload=%v causation=%q", payload, event.CausationID)
		}
	case runstate.EventRunCancelled:
		if !stringSlicesEqual(payload["cancelled_movement_ids"], []string{}) || !stringSlicesEqual(payload["cancelled_attempt_ids"], []string{}) || !stringSlicesEqual(payload["obsoleted_decision_ids"], []string{}) {
			t.Fatalf("run.cancelled payload=%v", payload)
		}
		fenced, present := payload["fenced_epoch"]
		if present != predicates.matchingLease || present && fenced != float64(3) {
			t.Fatalf("run.cancelled fenced_epoch=%v present=%t want iff d=%t", fenced, present, predicates.matchingLease)
		}
	}
}

func stringSlicesEqual(value any, want []string) bool {
	values, ok := value.([]any)
	if !ok || len(values) != len(want) {
		return false
	}
	for index, expected := range want {
		if values[index] != expected {
			return false
		}
	}
	return true
}

func assertCancellationDurableFixedPoint(t *testing.T, repository, runID string, predicates cancellationFixturePredicates) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRecoveryInput(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	state := input.Projection.State
	if state.Run != runstate.RunCancelled || state.OpenExecution != nil || state.PendingPrepare != nil {
		t.Fatalf("cancellation durable state run=%q open=%+v prepare=%+v", state.Run, state.OpenExecution, state.PendingPrepare)
	}
	wantEpoch := uint64(2)
	if predicates.matchingLease {
		wantEpoch = 3
	}
	if state.Authority.Epoch != wantEpoch {
		t.Fatalf("cancellation authority epoch=%d want=%d", state.Authority.Epoch, wantEpoch)
	}
	if _, present, err := store.ReadLease(runstate.RunID(runID)); err != nil || present {
		t.Fatalf("cancellation fixed point retains lease: present=%t err=%v", present, err)
	}
	runRoot := filepath.Join(repository, ".partitur", "runs", runID)
	for _, path := range []string{"scores/revision-2.yaml", "prepares/prepare-1.json", "driver.quiesced.prepare-1"} {
		if _, err := os.Stat(filepath.Join(runRoot, path)); !os.IsNotExist(err) {
			t.Fatalf("cancellation fixed point retains %q: %v", path, err)
		}
	}
}
