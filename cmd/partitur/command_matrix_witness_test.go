package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

type commandWitnessState string

const (
	witnessDischarged commandWitnessState = "discharged"
	witnessDivergent  commandWitnessState = "executed-divergent"
)

type commandWitnessRecord struct {
	state commandWitnessState
	issue int
}

type commandWitnessRegistry struct {
	invoked   map[string]bool
	returned  map[string]bool
	completed map[string]commandWitnessRecord
}

func newCommandWitnessRegistry() *commandWitnessRegistry {
	return &commandWitnessRegistry{
		invoked:   make(map[string]bool),
		returned:  make(map[string]bool),
		completed: make(map[string]commandWitnessRecord),
	}
}

func (registry *commandWitnessRegistry) run(
	t *testing.T,
	catalogID string,
	state commandWitnessState,
	issue int,
	fixture func(*testing.T),
) {
	t.Helper()
	if registry.invoked[catalogID] {
		t.Errorf("command witness %s is invoked more than once", catalogID)
		return
	}
	registry.invoked[catalogID] = true
	t.Run(catalogID, func(t *testing.T) {
		fixture(t)
		if t.Failed() {
			return
		}
		registry.returned[catalogID] = true
		switch state {
		case witnessDischarged:
			if issue != 0 {
				t.Fatalf("discharged command witness %s names issue #%d", catalogID, issue)
			}
			registry.completed[catalogID] = commandWitnessRecord{state: state, issue: issue}
		case witnessDivergent:
			if issue == 0 {
				t.Fatalf("divergent command witness %s has no issue", catalogID)
			}
			t.Logf("command witness %s executed and diverged from DESIGN.md; not discharged (issue #%d)", catalogID, issue)
		default:
			t.Fatalf("command witness %s has unknown state %q", catalogID, state)
		}
	})
}

func TestCommandMatrixWitnesses(t *testing.T) {
	registry := newCommandWitnessRegistry()
	runAnswerCommandWitnesses(t, registry)
	runApproveCommandWitnesses(t, registry)
	reconcileCommandWitnesses(t, registry.returned, registry.completed, commandMatrixCatalogIDs(t))
}

func runAnswerCommandWitnesses(t *testing.T, registry *commandWitnessRegistry) {
	registry.run(t, "ANSWER-001", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		decisionID := appendPendingCLIDecision(t, store, "question")
		t.Chdir(root)
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("answer", decisionID, "--answer-file", "missing-answer.txt")

		if code != 2 || stdout != "" || !strings.Contains(stderr, "missing-answer.txt") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want unreadable-input refusal", code, stdout, stderr)
		}
		assertJournalLength(t, store, before)
	})

	registry.run(t, "ANSWER-002", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		t.Chdir(root)
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("answer", "missing-question", "--answer", "continue")

		if code != 2 || stdout != "" || !strings.Contains(stderr, "precondition refused:") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want decision refusal", code, stdout, stderr)
		}
		assertJournalLength(t, store, before)
	})

	registry.run(t, "ANSWER-003", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		decisionID := appendPendingCLIDecision(t, store, "question")
		journalPath, corrupted := corruptFixtureJournal(t, root)
		t.Chdir(root)

		code, stdout, _ := invokeCommand("answer", decisionID, "--answer", "continue")

		if code != 5 || stdout != "" {
			t.Fatalf("exit=%d stdout=%q, want unavailable-projection exit 5", code, stdout)
		}
		assertFileBytes(t, journalPath, corrupted)
	})

	registry.run(t, "ANSWER-004", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		decisionID := appendPendingCLIDecision(t, store, "question")
		answerPath := filepath.Join(root, "answer.txt")
		if err := os.WriteFile(answerPath, []byte("continue\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(root)
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("answer", decisionID, "--answer-file", "answer.txt")

		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want committed answer", code, stdout, stderr)
		}
		journal, err := store.ReadJournal("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(journal.Events) != before+1 {
			t.Fatalf("answer appended %d events, want exactly one", len(journal.Events)-before)
		}
		resolved := journal.Events[before]
		payload := eventPayloadMap(t, resolved)
		if resolved.Type != runstate.EventDecisionResolved || payload["decision_id"] != decisionID ||
			payload["decision_type"] != "question" || payload["disposition"] != "answered" || payload["answer"] != "continue\n" {
			t.Fatalf("answer resolution = type=%s payload=%#v", resolved.Type, payload)
		}
	})

	registry.run(t, "ANSWER-005", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		decisionID := appendPendingCLIDecision(t, store, "question")
		t.Chdir(root)
		before := journalLength(t, store)
		filesystem := &journalFailureFS{failAppend: true}
		installJournalFailureStore(t, filesystem)

		code, stdout, stderr := invokeCommand("answer", decisionID, "--answer", "continue")

		if !filesystem.reached || code != 6 || stdout != "" || !strings.Contains(stderr, "partitur resume") {
			t.Fatalf("injection_reached=%t exit=%d stdout=%q stderr=%q, want interrupted answer", filesystem.reached, code, stdout, stderr)
		}
		assertJournalLength(t, store, before)
	})
}

func runApproveCommandWitnesses(t *testing.T, registry *commandWitnessRegistry) {
	registry.run(t, "APPROVE-001", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		t.Chdir(root)
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("approve", "missing-decision", "--approve")

		if code != 2 || stdout != "" || !strings.Contains(stderr, "precondition refused:") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want decision refusal", code, stdout, stderr)
		}
		assertJournalLength(t, store, before)
	})

	registry.run(t, "APPROVE-002", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		decisionID := appendPendingCLIDecision(t, store, "human_gate")
		journalPath, corrupted := corruptFixtureJournal(t, root)
		t.Chdir(root)

		code, stdout, _ := invokeCommand("approve", decisionID, "--approve")

		if code != 5 || stdout != "" {
			t.Fatalf("exit=%d stdout=%q, want unavailable-projection exit 5", code, stdout)
		}
		assertFileBytes(t, journalPath, corrupted)
	})

	registry.run(t, "APPROVE-003", witnessDischarged, 0, func(t *testing.T) {
		const override = "findings@attempt-1:F-1"
		for _, test := range []struct {
			name, decisionType string
			operands           []string
		}{
			{name: "amendment reasonless rejection", decisionType: "amendment", operands: []string{"--reject"}},
			{name: "finalization reasonless rejection", decisionType: "finalization", operands: []string{"--reject"}},
			{name: "amendment override", decisionType: "amendment", operands: []string{"--approve", "--override", override, "--reason", "operator judgment"}},
			{name: "finalization override", decisionType: "finalization", operands: []string{"--approve", "--override", override, "--reason", "operator judgment"}},
		} {
			t.Run(test.name, func(t *testing.T) {
				root, store := resumeAttemptFixture(t)
				decisionID := appendPendingCLIDecision(t, store, test.decisionType)
				t.Chdir(root)
				before := journalLength(t, store)
				args := append([]string{"approve", decisionID}, test.operands...)

				code, stdout, stderr := invokeCommand(args...)

				if code != 1 || stdout != "" || !strings.Contains(stderr, "usage error:") ||
					!strings.Contains(stderr, test.decisionType+" decision") {
					t.Fatalf("args=%v exit=%d stdout=%q stderr=%q, want type-specific usage error", args, code, stdout, stderr)
				}
				assertJournalLength(t, store, before)
			})
		}
	})

	registry.run(t, "APPROVE-004", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		decisionID := appendPendingCLIDecision(t, store, "human_gate")
		t.Chdir(root)
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand(
			"approve", decisionID, "--approve", "--override", "findings@attempt-1:F-1", "--reason", "operator judgment",
		)

		if code != 2 || stdout != "" || !strings.Contains(stderr, "precondition refused:") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want gate refusal", code, stdout, stderr)
		}
		assertJournalLength(t, store, before)
	})

	registry.run(t, "APPROVE-005", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		decisionID := appendPendingCLIDecision(t, store, "human_gate")
		t.Chdir(root)
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("approve", decisionID, "--approve")

		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want committed gate approval", code, stdout, stderr)
		}
		journal, err := store.ReadJournal("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(journal.Events) != before+1 {
			t.Fatalf("approval appended %d events, want exactly one", len(journal.Events)-before)
		}
		resolved := journal.Events[before]
		payload := eventPayloadMap(t, resolved)
		if resolved.Type != runstate.EventDecisionResolved || payload["decision_id"] != decisionID ||
			payload["decision_type"] != "human_gate" || payload["disposition"] != "approved" {
			t.Fatalf("gate resolution = type=%s payload=%#v", resolved.Type, payload)
		}
	})

	registry.run(t, "APPROVE-006", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		decisionID := appendPendingCLIDecision(t, store, "human_gate")
		t.Chdir(root)
		before := journalLength(t, store)
		filesystem := &journalFailureFS{failAppend: true}
		installJournalFailureStore(t, filesystem)

		code, stdout, stderr := invokeCommand("approve", decisionID, "--approve")

		if !filesystem.reached || code != 6 || stdout != "" || !strings.Contains(stderr, "partitur resume") {
			t.Fatalf("injection_reached=%t exit=%d stdout=%q stderr=%q, want interrupted gate approval", filesystem.reached, code, stdout, stderr)
		}
		assertJournalLength(t, store, before)
	})

	registry.run(t, "APPROVE-007", witnessDischarged, 0, func(t *testing.T) {
		root, store, decisionID, proposalID := routedAmendmentCommandFixture(t)
		proposalPath := filepath.Join(root, ".partitur", "runs", "run-1", "proposals", proposalID+".json")
		contents, err := os.ReadFile(proposalPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(proposalPath, append(contents, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(root)
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("approve", decisionID, "--approve")

		if code != 2 || stdout != "" || !strings.Contains(stderr, "precondition refused:") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want amendment refusal", code, stdout, stderr)
		}
		assertJournalLength(t, store, before)
	})

	registry.run(t, "APPROVE-008", witnessDischarged, 0, func(t *testing.T) {
		root, store, decisionID, _ := routedAmendmentCommandFixture(t)
		if err := store.RequestCancellation("run-1"); err != nil {
			t.Fatal(err)
		}
		t.Chdir(root)
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("approve", decisionID, "--approve")

		if code != 3 || stdout != "" || !strings.Contains(stderr, "run_cancelling") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want decision-time rejection", code, stdout, stderr)
		}
		journal, err := store.ReadJournal("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(journal.Events) != before+1 || journal.Events[before].Type != runstate.EventAmendmentRejected {
			t.Fatalf("decision-time journal delta=%v, want one amendment.rejected", journal.Events[before:])
		}
		payload := eventPayloadMap(t, journal.Events[before])
		if payload["decision_id"] != decisionID || payload["reason"] != "run_cancelling" {
			t.Fatalf("decision-time rejection payload=%#v", payload)
		}
	})

	registry.run(t, "APPROVE-009", witnessDischarged, 0, func(t *testing.T) {
		root, store, decisionID, _ := routedAmendmentCommandFixture(t)
		t.Chdir(root)
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("approve", decisionID, "--approve")

		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want completed amendment approval", code, stdout, stderr)
		}
		journal, err := store.ReadJournal("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(journal.Events) <= before || journal.Events[len(journal.Events)-1].Type != runstate.EventAmendmentApproved {
			t.Fatalf("approval journal delta=%v, want terminal amendment.approved", journal.Events[before:])
		}
		input, err := store.LoadRunInput("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, pending := input.Projection.State.PendingDecisions[decisionID]; pending {
			t.Fatalf("approved amendment decision %s remains pending", decisionID)
		}
	})

	registry.run(t, "APPROVE-010", witnessDischarged, 0, func(t *testing.T) {
		root, store, decisionID, _ := routedAmendmentCommandFixture(t)
		t.Chdir(root)
		before := journalLength(t, store)
		filesystem := &journalFailureFS{failAppend: true}
		installJournalFailureStore(t, filesystem)

		code, stdout, stderr := invokeCommand("approve", decisionID, "--approve")

		if !filesystem.reached || code != 6 || stdout != "" || !strings.Contains(stderr, "partitur resume") {
			t.Fatalf("injection_reached=%t exit=%d stdout=%q stderr=%q, want interrupted amendment approval", filesystem.reached, code, stdout, stderr)
		}
		assertJournalLength(t, store, before)
	})

	registry.run(t, "APPROVE-011", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		decisionID := appendPendingCLIDecision(t, store, "amendment")
		t.Chdir(root)
		var afterConcurrentRejection int
		newRunStore = func(root string, probe faultpoint.Probe, observers ...runstore.ReceiptObserver) (*runstore.Store, error) {
			opened, err := runstore.New(root, probe, observers...)
			if err != nil {
				return nil, err
			}
			if err := opened.RejectRoutedAmendment("run-1", decisionID, "concurrent operator"); err != nil {
				return nil, err
			}
			afterConcurrentRejection = journalLength(t, opened)
			return opened, nil
		}
		t.Cleanup(func() { newRunStore = runstore.New })

		code, stdout, stderr := invokeCommand("approve", decisionID, "--reject", "--reason", "operator rejected")

		if afterConcurrentRejection == 0 || code != 2 || stdout != "" || !strings.Contains(stderr, "precondition refused:") {
			t.Fatalf("interleave_length=%d exit=%d stdout=%q stderr=%q, want amendment refusal", afterConcurrentRejection, code, stdout, stderr)
		}
		assertJournalLength(t, store, afterConcurrentRejection)
	})

	registry.run(t, "APPROVE-012", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		decisionID := appendPendingCLIDecision(t, store, "amendment")
		t.Chdir(root)
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("approve", decisionID, "--reject", "--reason", "operator rejected")

		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want committed amendment rejection", code, stdout, stderr)
		}
		journal, err := store.ReadJournal("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(journal.Events) != before+1 || journal.Events[before].Type != runstate.EventAmendmentHumanRejected {
			t.Fatalf("rejection journal delta=%v, want one amendment.human_rejected", journal.Events[before:])
		}
		payload := eventPayloadMap(t, journal.Events[before])
		if payload["decision_id"] != decisionID || payload["human_reason"] != "operator rejected" {
			t.Fatalf("human rejection payload=%#v", payload)
		}
		input, err := store.LoadRunInput("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if _, pending := input.Projection.State.PendingDecisions[decisionID]; pending {
			t.Fatalf("rejected amendment decision %s remains pending", decisionID)
		}
	})

	registry.run(t, "APPROVE-013", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		decisionID := appendPendingCLIDecision(t, store, "amendment")
		t.Chdir(root)
		before := journalLength(t, store)
		filesystem := &journalFailureFS{failAppend: true}
		installJournalFailureStore(t, filesystem)

		code, stdout, stderr := invokeCommand("approve", decisionID, "--reject", "--reason", "operator rejected")

		if !filesystem.reached || code != 6 || stdout != "" || !strings.Contains(stderr, "partitur resume") {
			t.Fatalf("injection_reached=%t exit=%d stdout=%q stderr=%q, want interrupted amendment rejection", filesystem.reached, code, stdout, stderr)
		}
		assertJournalLength(t, store, before)
	})
}

func invokeCommand(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func assertJournalLength(t *testing.T, store *runstore.Store, want int) {
	t.Helper()
	if got := journalLength(t, store); got != want {
		t.Fatalf("journal event count=%d, want %d", got, want)
	}
}

func corruptFixtureJournal(t *testing.T, root string) (string, []byte) {
	t.Helper()
	path := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) == 0 {
		t.Fatal("fixture journal is empty")
	}
	contents[0] = '!'
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, contents
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s changed", path)
	}
}

func eventPayloadMap(t *testing.T, event runstate.Event) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func routedAmendmentCommandFixture(t *testing.T) (string, *runstore.Store, string, string) {
	t.Helper()
	root, store := amendCommandFixture(t, true)
	patchPath := filepath.Join(root, "patch.json")
	if err := os.WriteFile(patchPath, []byte(`[{"op":"replace","path":"/goal","value":"needs-human-approval"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runAmend("", patchPath, "operator correction", "", &stdout, &stderr); code != 0 {
		t.Fatalf("route amendment exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	var decisionID, proposalID string
	for _, event := range journal.Events {
		if event.Type != runstate.EventDecisionRequested {
			continue
		}
		payload := eventPayloadMap(t, event)
		if payload["decision_type"] == "amendment" {
			decisionID, _ = payload["decision_id"].(string)
			proposalID, _ = payload["proposal_id"].(string)
		}
	}
	if decisionID == "" || proposalID == "" {
		t.Fatalf("routed amendment identifiers decision=%q proposal=%q", decisionID, proposalID)
	}
	return root, store, decisionID, proposalID
}

func reconcileCommandWitnesses(
	t *testing.T,
	returned map[string]bool,
	completed map[string]commandWitnessRecord,
	denominator []string,
) {
	t.Helper()
	want := make(map[string]bool, len(denominator))
	for _, id := range denominator {
		want[id] = true
	}
	var missing, stale, unreturned []string
	for id := range want {
		if _, ok := completed[id]; !ok {
			missing = append(missing, id)
		}
	}
	for id := range completed {
		if !want[id] {
			stale = append(stale, id)
		}
		if !returned[id] {
			unreturned = append(unreturned, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	sort.Strings(unreturned)
	if len(stale) != 0 {
		t.Errorf("completed command witnesses outside parsed denominator: stale=%d %v", len(stale), stale)
	}
	if len(unreturned) != 0 {
		t.Errorf("command witnesses recorded completion without returning: %v", unreturned)
	}

	progress := commandWitnessProgressFromCompletion(t)
	if progress.denominator != len(denominator) {
		t.Errorf("COMPLETION command-witness denominator=%d, parsed denominator=%d", progress.denominator, len(denominator))
	}
	if progress.witnessed != len(completed) {
		t.Errorf("COMPLETION states %d completed command witnesses, executed witnesses completed %d", progress.witnessed, len(completed))
	}
	wantColour := "red"
	if progress.witnessed == progress.denominator {
		wantColour = "green"
	}
	if progress.colour != wantColour {
		t.Errorf(
			"COMPLETION command-witness row is %s with stated counts %d of %d, want %s",
			progress.colour, progress.witnessed, progress.denominator, wantColour,
		)
	}

	if os.Getenv("PARTITUR_REPORT_MISSING_COMMAND_WITNESSES") == "1" {
		t.Logf("derived missing command witnesses: count=%d ids=%v", len(missing), missing)
	}
}

type commandWitnessProgress struct {
	colour      string
	witnessed   int
	denominator int
}

func commandWitnessProgressFromCompletion(t *testing.T) commandWitnessProgress {
	t.Helper()
	lines := documentLines(t, filepath.Join("..", "..", "docs", "COMPLETION.md"))
	pattern := regexp.MustCompile(
		`^\| Currently (red|green): executed behavioural witnesses completed for ([0-9]+) of the ([0-9]+) parsed command-matrix catalog IDs\. This row is green only when the witnessed and denominator counts are equal\. \|$`,
	)
	index := uniqueLine(t, lines, func(line string) bool { return pattern.MatchString(line) }, "command-witness progress row")
	match := pattern.FindStringSubmatch(lines[index])
	witnessed, err := strconv.Atoi(match[2])
	if err != nil {
		t.Fatal(err)
	}
	denominator, err := strconv.Atoi(match[3])
	if err != nil {
		t.Fatal(err)
	}
	return commandWitnessProgress{colour: match[1], witnessed: witnessed, denominator: denominator}
}

func commandMatrixCatalogIDs(t *testing.T) []string {
	t.Helper()
	design := documentLines(t, filepath.Join("..", "..", "docs", "DESIGN.md"))
	commands := completionCommands(t)
	headingPattern := regexp.MustCompile("^### `partitur ([a-z]+(?:-[a-z]+)*)` precondition matrix$")
	sections := make(map[string][]string)
	for index, line := range design {
		match := headingPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		if _, duplicate := sections[match[1]]; duplicate {
			t.Fatalf("DESIGN has duplicate %s precondition matrix", match[1])
		}
		end := len(design)
		for candidate := index + 1; candidate < len(design); candidate++ {
			if strings.HasPrefix(design[candidate], "### ") || strings.HasPrefix(design[candidate], "## ") {
				end = candidate
				break
			}
		}
		sections[match[1]] = design[index+1 : end]
	}
	if len(sections) != len(commands) {
		t.Fatalf("DESIGN command matrix count=%d, want COMPLETION section 1 count=%d", len(sections), len(commands))
	}
	commandSet := make(map[string]bool, len(commands))
	for _, command := range commands {
		commandSet[command] = true
		if _, ok := sections[command]; !ok {
			t.Fatalf("DESIGN has no %s precondition matrix", command)
		}
	}
	for command := range sections {
		if !commandSet[command] {
			t.Fatalf("DESIGN has matrix for command %s outside COMPLETION section 1", command)
		}
	}

	var ids []string
	seen := make(map[string]bool)
	for _, command := range commands {
		section := sections[command]
		header := uniqueLine(t, section, func(line string) bool {
			cells := markdownCells(line)
			return len(cells) > 0 && cells[0] == "Catalog ID"
		}, command+" Catalog ID header")
		if header+1 >= len(section) || !markdownSeparator(section[header+1], len(markdownCells(section[header]))) {
			t.Fatalf("%s precondition matrix has malformed separator", command)
		}
		pattern := regexp.MustCompile("^`" + regexp.QuoteMeta(strings.ToUpper(command)) + "-[0-9]{3}`$")
		rows := 0
		for index := header + 2; index < len(section) && strings.HasPrefix(section[index], "|"); index++ {
			cells := markdownCells(section[index])
			if len(cells) == 0 || !pattern.MatchString(cells[0]) {
				t.Fatalf("%s precondition matrix has invalid Catalog ID cell %q", command, firstCell(cells))
			}
			id := strings.Trim(cells[0], "`")
			if seen[id] {
				t.Fatalf("command matrix Catalog ID %s is duplicated", id)
			}
			seen[id] = true
			ids = append(ids, id)
			rows++
		}
		if rows == 0 {
			t.Fatalf("%s precondition matrix extracted no Catalog IDs", command)
		}
	}
	return ids
}

func completionCommands(t *testing.T) []string {
	t.Helper()
	lines := documentLines(t, filepath.Join("..", "..", "docs", "COMPLETION.md"))
	start := exactLine(t, lines, "## 1. Commands")
	end := exactLine(t, lines, "## 2. Events")
	if end <= start {
		t.Fatal("COMPLETION section 1 has invalid bounds")
	}
	section := lines[start+1 : end]
	header := exactLine(t, section, "| Command |")
	if header+1 >= len(section) || section[header+1] != "|---|" {
		t.Fatal("COMPLETION section 1 command table has malformed separator")
	}
	pattern := regexp.MustCompile("^`[a-z]+(?:-[a-z]+)*`$")
	var commands []string
	seen := make(map[string]bool)
	for index := header + 2; index < len(section) && strings.HasPrefix(section[index], "|"); index++ {
		cells := markdownCells(section[index])
		if len(cells) != 1 || !pattern.MatchString(cells[0]) {
			t.Fatalf("COMPLETION section 1 has invalid command row %q", section[index])
		}
		command := strings.Trim(cells[0], "`")
		if seen[command] {
			t.Fatalf("COMPLETION section 1 duplicates command %s", command)
		}
		seen[command] = true
		commands = append(commands, command)
	}
	if len(commands) == 0 {
		t.Fatal("COMPLETION section 1 extracted no commands")
	}
	return commands
}

func documentLines(t *testing.T, path string) []string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(string(contents), "\n")
}

func exactLine(t *testing.T, lines []string, want string) int {
	t.Helper()
	return uniqueLine(t, lines, func(line string) bool { return line == want }, fmt.Sprintf("line %q", want))
}

func uniqueLine(t *testing.T, lines []string, match func(string) bool, label string) int {
	t.Helper()
	index := -1
	for candidate, line := range lines {
		if !match(line) {
			continue
		}
		if index != -1 {
			t.Fatalf("%s occurs more than once", label)
		}
		index = candidate
	}
	if index == -1 {
		t.Fatalf("%s is absent", label)
	}
	return index
}

func markdownCells(line string) []string {
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return nil
	}
	raw := strings.Split(line, "|")
	cells := make([]string, len(raw)-2)
	for index := range cells {
		cells[index] = strings.TrimSpace(raw[index+1])
	}
	return cells
}

func markdownSeparator(line string, columns int) bool {
	cells := markdownCells(line)
	if len(cells) != columns {
		return false
	}
	for _, cell := range cells {
		if cell != "---" {
			return false
		}
	}
	return true
}

func firstCell(cells []string) string {
	if len(cells) == 0 {
		return ""
	}
	return cells[0]
}
