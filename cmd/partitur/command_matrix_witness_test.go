package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	logstream "github.com/BeomSeogKim/Partitur/internal/logs"
	"github.com/BeomSeogKim/Partitur/internal/recoveryexec"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	statusprojection "github.com/BeomSeogKim/Partitur/internal/status"
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
	runAmendCommandWitnesses(t, registry)
	runCancelCommandWitnesses(t, registry)
	runApplyCommandWitnesses(t, registry)
	runVersionCommandWitnesses(t, registry)
	runValidateCommandWitnesses(t, registry)
	runLogsCommandWitnesses(t, registry)
	runStatusCommandWitnesses(t, registry)
	runInitCommandWitnesses(t, registry)
	reconcileCommandWitnesses(t, registry.returned, registry.completed, commandMatrixCatalogIDs(t))
}

func runVersionCommandWitnesses(t *testing.T, registry *commandWitnessRegistry) {
	registry.run(t, "VERSION-001", witnessDischarged, 0, func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".partitur"), []byte("invalid state anchor\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(root)
		before := snapshotInitTree(t, root)

		code, stdout, stderr := invokeCommand("version")

		if code != 0 || stdout != version+"\n" || stderr != "" {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want core version line", code, stdout, stderr)
		}
		assertCommandWitnessTree(t, root, before)
	})
}

func runValidateCommandWitnesses(t *testing.T, registry *commandWitnessRegistry) {
	registry.run(t, "VALIDATE-001", witnessDischarged, 0, func(t *testing.T) {
		root := t.TempDir()
		t.Setenv("HOME", t.TempDir())
		t.Chdir(root)
		before := snapshotInitTree(t, root)

		code, stdout, stderr := invokeCommand("validate")

		if code != 2 || stdout != "" || !strings.Contains(stderr, "kind=\"required_input_unavailable\"") ||
			!strings.Contains(stderr, filepath.Join(root, "partitur.yaml")) {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want refused score acquisition", code, stdout, stderr)
		}
		assertCommandWitnessTree(t, root, before)
	})

	registry.run(t, "VALIDATE-002", witnessDischarged, 0, func(t *testing.T) {
		root := validateCommandWitnessFixture(t, "enforcement", false)
		t.Chdir(root)
		before := snapshotInitTree(t, root)

		code, stdout, stderr := invokeCommand("validate")

		want := "enforcement: movement=\"plan-movement\" part=\"plan\" performer=\"performer\" unmet=[\"read_only\"]\n"
		if code != 3 || stdout != "" || stderr != want {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want complete diagnostic output %q", code, stdout, stderr, want)
		}
		assertCommandWitnessTree(t, root, before)
	})

	registry.run(t, "VALIDATE-003", witnessDischarged, 0, func(t *testing.T) {
		root := validateCommandWitnessFixture(t, "advisory", true)
		t.Chdir(root)
		before := snapshotInitTree(t, root)

		code, stdout, stderr := invokeCommand("validate")

		want := "enforcement advisory: movement=\"plan-movement\" part=\"plan\" performer=\"performer\" unmet=[\"read_only\"]\n"
		if code != 0 || stdout != "" || stderr != want {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want complete advisory output %q", code, stdout, stderr, want)
		}
		assertCommandWitnessTree(t, root, before)
	})
}

func runLogsCommandWitnesses(t *testing.T, registry *commandWitnessRegistry) {
	registry.run(t, "LOGS-001", witnessDischarged, 0, func(t *testing.T) {
		t.Run("output completes", func(t *testing.T) {
			root, store := resumeAttemptFixture(t)
			want := appendCommandWitnessLog(t, store)
			t.Chdir(root)
			before := snapshotInitTree(t, root)

			code, stdout, stderr := invokeCommand("logs", "run-1", "--jsonl")

			var got logstream.Entry
			if err := json.Unmarshal([]byte(stdout), &got); err != nil {
				t.Fatalf("decode logs JSONL %q: %v", stdout, err)
			}
			if code != 0 || stderr != "" || !reflect.DeepEqual(got, want) {
				t.Fatalf("exit=%d stderr=%q entry=%+v, want %+v", code, stderr, got, want)
			}
			assertCommandWitnessTree(t, root, before)
		})
		for _, test := range []struct {
			name string
			err  error
		}{
			{name: "broken pipe", err: syscall.EPIPE},
			{name: "closed pipe", err: io.ErrClosedPipe},
		} {
			t.Run(test.name, func(t *testing.T) {
				root, store := resumeAttemptFixture(t)
				appendCommandWitnessLog(t, store)
				t.Chdir(root)
				before := snapshotInitTree(t, root)
				var stderr bytes.Buffer

				code := run([]string{"logs", "run-1", "--jsonl"}, &failingWriter{err: test.err}, &stderr)

				if code != 0 || stderr.Len() != 0 {
					t.Fatalf("exit=%d stderr=%q, want silent successful pipe close", code, stderr.String())
				}
				assertCommandWitnessTree(t, root, before)
			})
		}
	})

	registry.run(t, "LOGS-002", witnessDischarged, 0, func(t *testing.T) {
		root, _ := resumeAttemptFixture(t)
		t.Chdir(root)
		before := snapshotInitTree(t, root)

		code, stdout, stderr := invokeCommand("logs", "bad/id")

		if code != 1 || stdout != "" || stderr != "usage error: detail=\"invalid run id\"\n" {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want semantic run-id usage error", code, stdout, stderr)
		}
		assertCommandWitnessTree(t, root, before)
	})

	registry.run(t, "LOGS-003", witnessDischarged, 0, func(t *testing.T) {
		root, _ := resumeAttemptFixture(t)
		t.Chdir(root)
		before := snapshotInitTree(t, root)

		code, stdout, stderr := invokeCommand("logs", "missing-run")

		if code != 2 || stdout != "" || !strings.Contains(stderr, "precondition refused:") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want observation refusal", code, stdout, stderr)
		}
		assertCommandWitnessTree(t, root, before)
	})

	registry.run(t, "LOGS-004", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		appendCommandWitnessLog(t, store)
		journalPath, corrupted := corruptFixtureJournal(t, root)
		t.Chdir(root)
		before := snapshotInitTree(t, root)

		code, stdout, stderr := invokeCommand("logs", "run-1")

		if code != 5 || stdout != "" || !strings.Contains(stderr, "recovery halted:") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want unavailable stream", code, stdout, stderr)
		}
		assertFileBytes(t, journalPath, corrupted)
		assertCommandWitnessTree(t, root, before)
	})

	registry.run(t, "LOGS-005", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		appendCommandWitnessLog(t, store)
		t.Chdir(root)
		before := snapshotInitTree(t, root)
		var stderr bytes.Buffer

		code := run([]string{"logs", "run-1", "--jsonl"}, &failingWriter{err: errors.New("disk full")}, &stderr)

		want := "precondition refused: detail=\"output stream is unwritable: logs output failed: disk full\"\n"
		if code != 2 || stderr.String() != want {
			t.Fatalf("exit=%d stderr=%q, want unwritable-output refusal %q", code, stderr.String(), want)
		}
		assertCommandWitnessTree(t, root, before)
	})
}

func runStatusCommandWitnesses(t *testing.T, registry *commandWitnessRegistry) {
	registry.run(t, "STATUS-001", witnessDischarged, 0, func(t *testing.T) {
		t.Run("output completes", func(t *testing.T) {
			root, _ := resumeAttemptFixture(t)
			t.Chdir(root)
			before := snapshotInitTree(t, root)

			code, stdout, stderr := invokeCommand("status", "run-1", "--json")

			var report statusprojection.Report
			if err := json.Unmarshal([]byte(stdout), &report); err != nil {
				t.Fatalf("decode status JSON %q: %v", stdout, err)
			}
			if code != 0 || stderr != "" || report.Schema != "partitur/status+json;v=1" ||
				report.Run.ID != "run-1" || report.Run.Lifecycle != string(runstate.RunRunning) {
				t.Fatalf("exit=%d stderr=%q report=%+v, want selected RUNNING projection", code, stderr, report)
			}
			assertCommandWitnessTree(t, root, before)
		})
		for _, test := range []struct {
			name string
			err  error
		}{
			{name: "broken pipe", err: syscall.EPIPE},
			{name: "closed pipe", err: io.ErrClosedPipe},
		} {
			t.Run(test.name, func(t *testing.T) {
				root, _ := resumeAttemptFixture(t)
				t.Chdir(root)
				before := snapshotInitTree(t, root)
				var stderr bytes.Buffer

				code := run([]string{"status", "run-1", "--json"}, &failingWriter{err: test.err}, &stderr)

				if code != 0 || stderr.Len() != 0 {
					t.Fatalf("exit=%d stderr=%q, want silent successful pipe close", code, stderr.String())
				}
				assertCommandWitnessTree(t, root, before)
			})
		}
	})

	registry.run(t, "STATUS-002", witnessDischarged, 0, func(t *testing.T) {
		root, _ := resumeAttemptFixture(t)
		t.Chdir(root)
		before := snapshotInitTree(t, root)

		code, stdout, stderr := invokeCommand("status", "bad/id")

		if code != 1 || stdout != "" || stderr != "usage error: detail=\"invalid run id\"\n" {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want semantic run-id usage error", code, stdout, stderr)
		}
		assertCommandWitnessTree(t, root, before)
	})

	registry.run(t, "STATUS-003", witnessDischarged, 0, func(t *testing.T) {
		root, _ := resumeAttemptFixture(t)
		t.Chdir(root)
		before := snapshotInitTree(t, root)

		code, stdout, stderr := invokeCommand("status", "missing-run")

		if code != 2 || stdout != "" || !strings.Contains(stderr, "precondition refused:") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want observation refusal", code, stdout, stderr)
		}
		assertCommandWitnessTree(t, root, before)
	})

	registry.run(t, "STATUS-004", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		appendCommandWitnessLog(t, store)
		journalPath, corrupted := corruptFixtureJournal(t, root)
		t.Chdir(root)
		before := snapshotInitTree(t, root)

		code, stdout, stderr := invokeCommand("status", "run-1")

		if code != 5 || stdout != "" || !strings.Contains(stderr, "recovery halted:") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want unavailable projection", code, stdout, stderr)
		}
		assertFileBytes(t, journalPath, corrupted)
		assertCommandWitnessTree(t, root, before)
	})

	registry.run(t, "STATUS-005", witnessDischarged, 0, func(t *testing.T) {
		root, _ := resumeAttemptFixture(t)
		t.Chdir(root)
		before := snapshotInitTree(t, root)
		var stderr bytes.Buffer

		code := run([]string{"status", "run-1", "--json"}, &failingWriter{err: errors.New("disk full")}, &stderr)

		want := "precondition refused: detail=\"output stream is unwritable: disk full\"\n"
		if code != 2 || stderr.String() != want {
			t.Fatalf("exit=%d stderr=%q, want unwritable-output refusal %q", code, stderr.String(), want)
		}
		assertCommandWitnessTree(t, root, before)
	})
}

func runInitCommandWitnesses(t *testing.T, registry *commandWitnessRegistry) {
	registry.run(t, "INIT-001", witnessDischarged, 0, func(t *testing.T) {
		witnessInitCommand(t, initFixture{})
	})
	registry.run(t, "INIT-002", witnessDischarged, 0, func(t *testing.T) {
		witnessInitCommand(t, initFixture{score: []byte("existing score bytes\n")})
	})
	registry.run(t, "INIT-003", witnessDischarged, 0, func(t *testing.T) {
		witnessInitCommand(t, initFixture{stateExists: true})
	})
	registry.run(t, "INIT-004", witnessDischarged, 0, func(t *testing.T) {
		witnessInitCommand(t, initFixture{stateExists: true, score: []byte("existing score bytes\n")})
	})
	registry.run(t, "INIT-005", witnessDischarged, 0, func(t *testing.T) {
		witnessInitCommand(t, initFixture{stateExists: true, ignore: []byte(initIgnoreContents)})
	})
	registry.run(t, "INIT-006", witnessDischarged, 0, func(t *testing.T) {
		witnessInitCommand(t, initFixture{
			stateExists: true,
			ignore:      []byte(initIgnoreContents),
			score:       []byte("existing score bytes\n"),
		})
	})
	registry.run(t, "INIT-007", witnessDischarged, 0, func(t *testing.T) {
		witnessInitCommand(t, initFixture{stateExists: true, ignore: []byte("work/\nruns/\n")})
	})
	registry.run(t, "INIT-008", witnessDischarged, 0, func(t *testing.T) {
		witnessInitCommand(t, initFixture{
			stateExists: true,
			ignore:      []byte("work/\nruns/\n"),
			score:       []byte("existing score bytes\n"),
		})
	})
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

func runAmendCommandWitnesses(t *testing.T, registry *commandWitnessRegistry) {
	registry.run(t, "AMEND-001", witnessDischarged, 0, func(t *testing.T) {
		for _, test := range []struct {
			name  string
			setup func(*testing.T, string)
			args  []string
		}{
			{name: "run selection", args: []string{"amend", "missing-run", "--patch", "patch.json", "--reason", "fixture"}},
			{name: "patch source", args: []string{"amend", "run-1", "--patch", "missing-patch.json", "--reason", "fixture"}},
			{
				name: "claimed impact source",
				setup: func(t *testing.T, root string) {
					if err := os.WriteFile(filepath.Join(root, "patch.json"), []byte(`[{"op":"replace","path":"/goal","value":"fixture"}]`), 0o600); err != nil {
						t.Fatal(err)
					}
				},
				args: []string{"amend", "run-1", "--patch", "patch.json", "--reason", "fixture", "--claimed-impact", "missing-impact.json"},
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				root, store := amendCommandFixture(t, true)
				if test.setup != nil {
					test.setup(t, root)
				}
				before := journalLength(t, store)

				code, stdout, stderr := invokeCommand(test.args...)

				if code != 2 || stdout != "" || !strings.Contains(stderr, "precondition refused:") {
					t.Fatalf("exit=%d stdout=%q stderr=%q, want input refusal", code, stdout, stderr)
				}
				assertJournalLength(t, store, before)
			})
		}
	})

	registry.run(t, "AMEND-002", witnessDischarged, 0, func(t *testing.T) {
		root, store := amendCommandFixture(t, true)
		patch := filepath.Join(root, "patch.json")
		if err := os.WriteFile(patch, []byte(`[{"op":"replace","path":"/goal","value":"fixture"}]`), 0o600); err != nil {
			t.Fatal(err)
		}
		journalPath, corrupted := corruptFixtureJournal(t, root)

		code, stdout, stderr := invokeCommand("amend", "run-1", "--patch", patch, "--reason", "fixture")

		if code != 5 || stdout != "" || !strings.Contains(stderr, "recovery halted:") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want unavailable projection", code, stdout, stderr)
		}
		assertFileBytes(t, journalPath, corrupted)
		_ = store
	})

	registry.run(t, "AMEND-003", witnessDischarged, 0, func(t *testing.T) {
		root, store := amendCommandFixture(t, true)
		patch := filepath.Join(root, "patch.json")
		if err := os.WriteFile(patch, []byte(`[{"op":"replace","path":"/revision","value":2}]`), 0o600); err != nil {
			t.Fatal(err)
		}
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("amend", "run-1", "--patch", patch, "--reason", "fixture")

		if code != 3 || stdout != "" || !strings.Contains(stderr, "amendment rejected:") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want rejected amendment", code, stdout, stderr)
		}
		delta := assertCommandWitnessJournalDelta(t, store, before, runstate.EventAmendmentRejected)
		if payload := eventPayloadMap(t, delta[0]); payload["reason"] != "reserved_field" {
			t.Fatalf("amendment.rejected payload=%#v, want reserved_field", payload)
		}
	})

	registry.run(t, "AMEND-004", witnessDischarged, 0, func(t *testing.T) {
		root, store := amendCommandFixture(t, true)
		patch := filepath.Join(root, "patch.json")
		if err := os.WriteFile(patch, []byte(`[{"op":"replace","path":"/goal","value":"needs-review"}]`), 0o600); err != nil {
			t.Fatal(err)
		}
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("amend", "run-1", "--patch", patch, "--reason", "fixture")

		if code != 0 || stdout != "" || !strings.Contains(stderr, "amendment routed:") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want routed amendment", code, stdout, stderr)
		}
		delta := assertCommandWitnessJournalDelta(t, store, before,
			runstate.EventAmendmentRoutedHuman,
			runstate.EventDecisionRequested,
		)
		routed := eventPayloadMap(t, delta[0])
		requested := eventPayloadMap(t, delta[1])
		if routed["proposal_id"] == "" || requested["proposal_id"] != routed["proposal_id"] ||
			requested["decision_type"] != "amendment" || requested["blocking"] != false {
			t.Fatalf("routed=%#v requested=%#v", routed, requested)
		}
		input, err := store.LoadRunInput("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if input.Projection.State.Run != runstate.RunRunning || len(input.Projection.State.PendingDecisions) != 1 {
			t.Fatalf("routed state run=%s pending=%+v", input.Projection.State.Run, input.Projection.State.PendingDecisions)
		}
	})

	registry.run(t, "AMEND-005", witnessDischarged, 0, func(t *testing.T) {
		root, store := amendCommandFixture(t, true)
		patch := filepath.Join(root, "patch.json")
		if err := os.WriteFile(patch, []byte(`[{"op":"replace","path":"/policy/budget/active_wall_clock_min","value":9}]`), 0o600); err != nil {
			t.Fatal(err)
		}
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("amend", "run-1", "--patch", patch, "--reason", "fixture")

		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want approved amendment", code, stdout, stderr)
		}
		delta := assertCommandWitnessJournalDelta(t, store, before,
			runstate.EventAmendmentApprovalPrepared,
			runstate.EventAmendmentApproved,
		)
		prepared := eventPayloadMap(t, delta[0])
		approved := eventPayloadMap(t, delta[1])
		if prepared["prepare_id"] == "" || prepared["proposal_id"] == "" ||
			approved["proposal_id"] != prepared["proposal_id"] {
			t.Fatalf("prepared=%#v approved=%#v, want one bound transaction", prepared, approved)
		}
		input, err := store.LoadRunInput("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if input.Projection.State.ScoreHead.Revision != 2 || input.Projection.State.PendingPrepare != nil {
			t.Fatalf("approved state head=%+v pending_prepare=%+v", input.Projection.State.ScoreHead, input.Projection.State.PendingPrepare)
		}
		if _, present, err := store.ReadLease("run-1"); err != nil || present {
			t.Fatalf("approved amendment lease present=%t error=%v", present, err)
		}
	})

	registry.run(t, "AMEND-006", witnessDischarged, 0, func(t *testing.T) {
		root, store := amendCommandFixture(t, true)
		patch := filepath.Join(root, "patch.json")
		if err := os.WriteFile(patch, []byte(`[{"op":"replace","path":"/policy/budget/active_wall_clock_min","value":9}]`), 0o600); err != nil {
			t.Fatal(err)
		}
		before := journalLength(t, store)
		filesystem := &journalFailureFS{failAppendAt: 2}
		installJournalFailureStore(t, filesystem)

		code, stdout, stderr := invokeCommand("amend", "run-1", "--patch", patch, "--reason", "fixture")

		if !filesystem.reached || code != 6 || stdout != "" || !strings.Contains(stderr, "partitur resume run-1") {
			t.Fatalf("injection_reached=%t exit=%d stdout=%q stderr=%q, want interrupted amendment", filesystem.reached, code, stdout, stderr)
		}
		delta := assertCommandWitnessJournalDelta(t, store, before, runstate.EventAmendmentApprovalPrepared)
		if payload := eventPayloadMap(t, delta[0]); payload["prepare_id"] == "" || payload["proposal_id"] == "" {
			t.Fatalf("confirmed amendment prefix payload=%#v", payload)
		}
		input, err := store.LoadRunInput("run-1")
		if err != nil {
			t.Fatal(err)
		}
		if input.Projection.State.Run != runstate.RunRunning || input.Projection.State.ScoreHead.Revision != 1 {
			t.Fatalf("interrupted amendment state run=%s head=%+v", input.Projection.State.Run, input.Projection.State.ScoreHead)
		}
	})
}

func runCancelCommandWitnesses(t *testing.T, registry *commandWitnessRegistry) {
	registry.run(t, "CANCEL-001", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeFixture(t, "")
		t.Chdir(root)
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("cancel", "bad/id")

		if code != 1 || stdout != "" || stderr != "usage error: detail=\"invalid run id\"\n" {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want semantic run-id usage error", code, stdout, stderr)
		}
		assertJournalLength(t, store, before)
	})

	registry.run(t, "CANCEL-002", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeFixture(t, "")
		t.Chdir(root)
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("cancel", "missing-run")

		if code != 2 || stdout != "" || !strings.Contains(stderr, "precondition refused:") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want selection refusal", code, stdout, stderr)
		}
		assertJournalLength(t, store, before)
	})

	registry.run(t, "CANCEL-003", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeAttemptFixture(t)
		appendCommandWitnessLog(t, store)
		journalPath, corrupted := corruptFixtureJournal(t, root)
		t.Chdir(root)

		code, stdout, stderr := invokeCommand("cancel", "run-1")

		if code != 5 || stdout != "" || !strings.Contains(stderr, "recovery halted:") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want unavailable projection", code, stdout, stderr)
		}
		assertFileBytes(t, journalPath, corrupted)
	})

	registry.run(t, "CANCEL-004", witnessDischarged, 0, func(t *testing.T) {
		witnessTerminalCancelCleanup(t, "SUCCEEDED", 0)
	})

	registry.run(t, "CANCEL-005", witnessDischarged, 0, func(t *testing.T) {
		for _, terminal := range []string{"FAILED", "CANCELLED"} {
			t.Run(terminal, func(t *testing.T) {
				witnessTerminalCancelCleanup(t, terminal, 4)
			})
		}
	})

	registry.run(t, "CANCEL-006", witnessDischarged, 0, func(t *testing.T) {
		t.Run("cancellation completes", func(t *testing.T) {
			root, store := resumeFixture(t, "")
			t.Chdir(root)
			before := journalLength(t, store)

			code, stdout, stderr := invokeCommand("cancel", "run-1")

			if code != 4 || stdout != "" || stderr != "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want completed cancellation", code, stdout, stderr)
			}
			assertCommandWitnessJournalDelta(t, store, before, runstate.EventCancelRequested, runstate.EventRunCancelled)
			assertCommandWitnessRunState(t, store, runstate.RunCancelled)
		})
		t.Run("terminal failure wins the race", func(t *testing.T) {
			root, store := resumeFixture(t, "")
			t.Chdir(root)
			before := journalLength(t, store)
			var stdout, stderr bytes.Buffer

			code := runCancel("run-1", &stdout, &stderr, func(context.Context, string) (recoveryexec.Result, error) {
				if err := store.RequestCancellation("run-1"); err != nil {
					return recoveryexec.Result{}, err
				}
				if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
					_, err := tx.At("command-witness.failed-race").Append(resumeEvent("run-1", runstate.EventRunFailed, map[string]any{"reason": "fixture race"}))
					return err
				}); err != nil {
					return recoveryexec.Result{}, err
				}
				return recoveryexec.Result{Outcome: recoveryexec.OutcomeFailed}, nil
			})

			if code != 4 || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want durable failed race", code, stdout.String(), stderr.String())
			}
			assertCommandWitnessJournalDelta(t, store, before, runstate.EventCancelRequested, runstate.EventRunFailed)
			assertCommandWitnessRunState(t, store, runstate.RunFailed)
		})
	})

	registry.run(t, "CANCEL-007", witnessDischarged, 0, func(t *testing.T) {
		root, store := resumeFixture(t, "")
		driver, err := store.AcquireRecoveryDriver("run-1")
		if err != nil {
			t.Fatal(err)
		}
		defer driver.Release()
		leasePath := filepath.Join(root, ".partitur", "runs", "run-1", "driver.lease")
		if err := os.WriteFile(leasePath, []byte("malformed lease"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Chdir(root)
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("cancel", "run-1")

		if code != 5 || stdout != "" || !strings.Contains(stderr, `reason="owner_unverifiable"`) {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want owner-unverifiable halt", code, stdout, stderr)
		}
		assertCommandWitnessJournalDelta(t, store, before, runstate.EventCancelRequested)
		assertCommandWitnessRunState(t, store, runstate.RunRunning)
		assertFileBytes(t, leasePath, []byte("malformed lease"))
	})

	registry.run(t, "CANCEL-008", witnessDischarged, 0, func(t *testing.T) {
		t.Run("intermediate owner wait", func(t *testing.T) {
			root, store := resumeFixture(t, "")
			driver, err := store.AcquireRecoveryDriver("run-1")
			if err != nil {
				t.Fatal(err)
			}
			defer driver.Release()
			t.Chdir(root)
			before := journalLength(t, store)
			var stdout, stderr bytes.Buffer

			code := runCancel("run-1", &stdout, &stderr, func(context.Context, string) (recoveryexec.Result, error) {
				if err := store.RequestCancellation("run-1"); err != nil {
					return recoveryexec.Result{}, err
				}
				return recoveryexec.Result{Outcome: recoveryexec.OutcomeRefused}, nil
			})

			if code != 6 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "partitur resume run-1") {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want interrupted owner wait", code, stdout.String(), stderr.String())
			}
			assertCommandWitnessJournalDelta(t, store, before, runstate.EventCancelRequested)
			assertCommandWitnessRunState(t, store, runstate.RunRunning)
			if _, present, err := store.ReadLease("run-1"); err != nil || !present {
				t.Fatalf("intermediate wait lease present=%t error=%v", present, err)
			}
		})
		t.Run("selected action interrupted", func(t *testing.T) {
			root, store := resumeFixture(t, "")
			t.Chdir(root)
			before := journalLength(t, store)
			var stdout, stderr bytes.Buffer

			code := runCancel("run-1", &stdout, &stderr, func(context.Context, string) (recoveryexec.Result, error) {
				if err := store.RequestCancellation("run-1"); err != nil {
					return recoveryexec.Result{}, err
				}
				return recoveryexec.Result{}, nil
			})

			if code != 6 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "partitur resume run-1") {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want interrupted selected action", code, stdout.String(), stderr.String())
			}
			assertCommandWitnessJournalDelta(t, store, before, runstate.EventCancelRequested)
			assertCommandWitnessRunState(t, store, runstate.RunRunning)
		})
	})
}

func runApplyCommandWitnesses(t *testing.T, registry *commandWitnessRegistry) {
	registry.run(t, "APPLY-001", witnessDivergent, 301, func(t *testing.T) {
		root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
		t.Chdir(root)
		before := journalLength(t, store)

		code, stdout, stderr := invokeCommand("apply", "bad/id")

		if code != 2 || stdout != "" || stderr != "precondition refused: detail=\"invalid runstore path: run id\"\n" {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want observed divergence exit 2 refusal", code, stdout, stderr)
		}
		assertCommandWitnessJournalDelta(t, store, before)
		assertCommandWitnessApplicationState(t, store, runstate.ApplicationNotApplied)
	})

	registry.run(t, "APPLY-002", witnessDischarged, 0, func(t *testing.T) {
		t.Run("run selection", func(t *testing.T) {
			root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
			t.Chdir(root)
			before := journalLength(t, store)

			code, stdout, stderr := invokeCommand("apply", "missing-run")

			if code != 2 || stdout != "" || !strings.Contains(stderr, "precondition refused:") {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want selection refusal", code, stdout, stderr)
			}
			assertCommandWitnessJournalDelta(t, store, before)
		})
		for _, protected := range []string{"partitur.yaml", ".partitur/injected.txt"} {
			t.Run("protected path "+protected, func(t *testing.T) {
				files := append([]applyFixtureFile(nil), applyFixtureCandidateFiles...)
				files = append(files, applyFixtureFile{name: protected, contents: applyProtectedPathContents})
				root, store, _ := applyRequireFixtureWithFiles(t, applyGate{require: []string{"verified"}}, files)
				t.Chdir(root)
				before := journalLength(t, store)

				code, stdout, stderr := invokeCommand("apply", "run-1")

				if code != 2 || stdout != "" || !strings.Contains(stderr, "precondition refused:") {
					t.Fatalf("exit=%d stdout=%q stderr=%q, want protected-path refusal", code, stdout, stderr)
				}
				assertCommandWitnessJournalDelta(t, store, before)
				assertCommandWitnessApplicationState(t, store, runstate.ApplicationNotApplied)
				applyFixtureApplicationClean(t, root)
				if protected == ".partitur/injected.txt" {
					if _, err := os.Stat(filepath.Join(root, protected)); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("protected candidate path materialized: %v", err)
					}
				}
			})
		}
	})

	registry.run(t, "APPLY-003", witnessDischarged, 0, func(t *testing.T) {
		for _, state := range []runstate.ApplicationState{runstate.ApplicationApplying, runstate.ApplicationRecoveryRequired} {
			t.Run(string(state), func(t *testing.T) {
				root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
				txnID := appendCommandWitnessApplyStarted(t, store)
				if state == runstate.ApplicationRecoveryRequired {
					appendCommandWitnessApplyRecoveryRequired(t, store, txnID, "fixture cause")
				}
				t.Chdir(root)
				before := journalLength(t, store)

				code, stdout, stderr := invokeCommand("apply", "run-1")

				if code != 2 || stdout != "" || !strings.Contains(stderr, "normal apply is refused") {
					t.Fatalf("state=%s exit=%d stdout=%q stderr=%q", state, code, stdout, stderr)
				}
				assertCommandWitnessJournalDelta(t, store, before)
				assertCommandWitnessApplicationState(t, store, state)
			})
		}
	})

	registry.run(t, "APPLY-004", witnessDischarged, 0, func(t *testing.T) {
		for _, state := range []runstate.ApplicationState{
			runstate.ApplicationNotApplied,
			runstate.ApplicationFailedClean,
			runstate.ApplicationApplied,
		} {
			t.Run(string(state), func(t *testing.T) {
				root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
				t.Chdir(root)
				switch state {
				case runstate.ApplicationFailedClean:
					makeCommandWitnessApplyFailedClean(t, root, store)
				case runstate.ApplicationApplied:
					code, stdout, stderr := invokeCommand("apply", "run-1")
					if code != 0 || stdout != "" || stderr != "" {
						t.Fatalf("prepare APPLIED exit=%d stdout=%q stderr=%q", code, stdout, stderr)
					}
				}
				before := journalLength(t, store)

				code, stdout, stderr := invokeCommand("apply", "run-1", "--recover")

				if code != 2 || stdout != "" || !strings.Contains(stderr, "--recover is refused from "+string(state)) {
					t.Fatalf("state=%s exit=%d stdout=%q stderr=%q", state, code, stdout, stderr)
				}
				assertCommandWitnessJournalDelta(t, store, before)
				assertCommandWitnessApplicationState(t, store, state)
			})
		}
	})

	registry.run(t, "APPLY-005", witnessDischarged, 0, func(t *testing.T) {
		root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
		t.Chdir(root)
		if code, stdout, stderr := invokeCommand("apply", "run-1"); code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("prepare APPLIED exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		before := journalLength(t, store)
		beforeTree := snapshotInitTree(t, root)

		code, stdout, stderr := invokeCommand("apply", "run-1")

		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want idempotent apply", code, stdout, stderr)
		}
		assertCommandWitnessJournalDelta(t, store, before)
		assertCommandWitnessApplicationState(t, store, runstate.ApplicationApplied)
		assertCommandWitnessTree(t, root, beforeTree)
	})

	registry.run(t, "APPLY-006", witnessDischarged, 0, func(t *testing.T) {
		for _, state := range []runstate.ApplicationState{runstate.ApplicationNotApplied, runstate.ApplicationFailedClean} {
			t.Run(string(state), func(t *testing.T) {
				root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
				t.Chdir(root)
				if state == runstate.ApplicationFailedClean {
					makeCommandWitnessApplyFailedClean(t, root, store)
				}
				before := journalLength(t, store)

				code, stdout, stderr := invokeCommand("apply", "run-1")

				if code != 0 || stdout != "" || stderr != "" {
					t.Fatalf("entry=%s exit=%d stdout=%q stderr=%q", state, code, stdout, stderr)
				}
				delta := assertCommandWitnessJournalDelta(t, store, before, runstate.EventApplyStarted, runstate.EventApplyCompleted)
				assertCommandWitnessApplicationEvents(t, store, delta)
				assertCommandWitnessApplicationState(t, store, runstate.ApplicationApplied)
				assertCommandWitnessCandidateCheckout(t, root)
			})
		}
	})

	registry.run(t, "APPLY-007", witnessDischarged, 0, func(t *testing.T) {
		for _, state := range []runstate.ApplicationState{runstate.ApplicationNotApplied, runstate.ApplicationFailedClean} {
			t.Run(string(state), func(t *testing.T) {
				root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
				t.Chdir(root)
				if state == runstate.ApplicationFailedClean {
					makeCommandWitnessApplyFailedClean(t, root, store)
				}
				prepareCommandWitnessApplySabotage(t, root)
				before := journalLength(t, store)

				code, stdout, stderr := invokeCommand("apply", "run-1")

				if code != 4 || stdout != "" || !strings.Contains(stderr, "application failed cleanly:") {
					t.Fatalf("entry=%s exit=%d stdout=%q stderr=%q", state, code, stdout, stderr)
				}
				delta := assertCommandWitnessJournalDelta(t, store, before, runstate.EventApplyStarted, runstate.EventApplyFailed)
				assertCommandWitnessApplicationEvents(t, store, delta)
				assertCommandWitnessApplicationState(t, store, runstate.ApplicationFailedClean)
				assertCommandWitnessBaseCheckout(t, root)
			})
		}
	})

	registry.run(t, "APPLY-008", witnessDischarged, 0, func(t *testing.T) {
		for _, state := range []runstate.ApplicationState{runstate.ApplicationNotApplied, runstate.ApplicationFailedClean} {
			t.Run(string(state), func(t *testing.T) {
				root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
				t.Chdir(root)
				if state == runstate.ApplicationFailedClean {
					makeCommandWitnessApplyFailedClean(t, root, store)
				}
				before := journalLength(t, store)
				filesystem := &journalFailureFS{failAppendAt: 2}
				installJournalFailureStore(t, filesystem)

				code, stdout, stderr := invokeCommand("apply", "run-1")

				if !filesystem.reached || code != 6 || stdout != "" || !strings.Contains(stderr, "partitur apply run-1 --recover") {
					t.Fatalf("entry=%s injection_reached=%t exit=%d stdout=%q stderr=%q", state, filesystem.reached, code, stdout, stderr)
				}
				delta := assertCommandWitnessJournalDelta(t, store, before, runstate.EventApplyStarted)
				assertCommandWitnessApplicationEvents(t, store, delta)
				assertCommandWitnessApplicationState(t, store, runstate.ApplicationApplying)
				assertCommandWitnessCandidateCheckout(t, root)
			})
		}
	})

	registry.run(t, "APPLY-009", witnessDischarged, 0, func(t *testing.T) {
		for _, state := range []runstate.ApplicationState{runstate.ApplicationApplying, runstate.ApplicationRecoveryRequired} {
			t.Run(string(state), func(t *testing.T) {
				root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
				txnID := appendCommandWitnessApplyStarted(t, store)
				if state == runstate.ApplicationRecoveryRequired {
					appendCommandWitnessApplyRecoveryRequired(t, store, txnID, "fixture cause")
				}
				materializeCommandWitnessCandidate(t, root)
				t.Chdir(root)
				before := journalLength(t, store)

				code, stdout, stderr := invokeCommand("apply", "run-1", "--recover")

				if code != 0 || stdout != "" || stderr != "" {
					t.Fatalf("entry=%s exit=%d stdout=%q stderr=%q", state, code, stdout, stderr)
				}
				want := []runstate.EventType{runstate.EventApplyCompleted}
				if state == runstate.ApplicationApplying {
					want = append([]runstate.EventType{runstate.EventApplyRecoveryRequired}, want...)
				}
				delta := assertCommandWitnessJournalDelta(t, store, before, want...)
				assertCommandWitnessApplicationEvents(t, store, delta)
				assertCommandWitnessApplicationState(t, store, runstate.ApplicationApplied)
				assertCommandWitnessCandidateCheckout(t, root)
			})
		}
	})

	registry.run(t, "APPLY-010", witnessDischarged, 0, func(t *testing.T) {
		for _, state := range []runstate.ApplicationState{runstate.ApplicationApplying, runstate.ApplicationRecoveryRequired} {
			t.Run(string(state), func(t *testing.T) {
				root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
				txnID := appendCommandWitnessApplyStarted(t, store)
				if state == runstate.ApplicationRecoveryRequired {
					appendCommandWitnessApplyRecoveryRequired(t, store, txnID, "fixture cause")
				}
				t.Chdir(root)
				before := journalLength(t, store)

				code, stdout, stderr := invokeCommand("apply", "run-1", "--recover")

				if code != 4 || stdout != "" || stderr != "" {
					t.Fatalf("entry=%s exit=%d stdout=%q stderr=%q", state, code, stdout, stderr)
				}
				want := []runstate.EventType{runstate.EventApplyRecoveryResolved}
				if state == runstate.ApplicationApplying {
					want = append([]runstate.EventType{runstate.EventApplyRecoveryRequired}, want...)
				}
				delta := assertCommandWitnessJournalDelta(t, store, before, want...)
				assertCommandWitnessApplicationEvents(t, store, delta)
				assertCommandWitnessApplicationState(t, store, runstate.ApplicationFailedClean)
				assertCommandWitnessBaseCheckout(t, root)
			})
		}
	})

	registry.run(t, "APPLY-011", witnessDischarged, 0, func(t *testing.T) {
		for _, state := range []runstate.ApplicationState{runstate.ApplicationApplying, runstate.ApplicationRecoveryRequired} {
			t.Run(string(state), func(t *testing.T) {
				root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
				txnID := appendCommandWitnessApplyStarted(t, store)
				if state == runstate.ApplicationRecoveryRequired {
					appendCommandWitnessApplyRecoveryRequired(t, store, txnID, "fixture cause")
				}
				if err := os.WriteFile(filepath.Join(root, resumeFixtureUntouchedFile), []byte("third state\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Chdir(root)
				before := journalLength(t, store)

				code, stdout, stderr := invokeCommand("apply", "run-1", "--recover")

				if code != 5 || stdout != "" || !strings.Contains(stderr, "matches neither candidate tree") {
					t.Fatalf("entry=%s exit=%d stdout=%q stderr=%q", state, code, stdout, stderr)
				}
				var delta []runstate.Event
				if state == runstate.ApplicationApplying {
					delta = assertCommandWitnessJournalDelta(t, store, before, runstate.EventApplyRecoveryRequired)
					assertCommandWitnessApplicationEvents(t, store, delta)
				} else {
					assertCommandWitnessJournalDelta(t, store, before)
				}
				assertCommandWitnessApplicationState(t, store, runstate.ApplicationRecoveryRequired)
			})
		}
	})

	registry.run(t, "APPLY-012", witnessDischarged, 0, func(t *testing.T) {
		root, store, _ := applyRequireFixture(t, applyGate{require: []string{"verified"}})
		appendCommandWitnessApplyStarted(t, store)
		t.Chdir(root)
		before := journalLength(t, store)
		filesystem := &journalFailureFS{failAppend: true}
		installJournalFailureStore(t, filesystem)

		code, stdout, stderr := invokeCommand("apply", "run-1", "--recover")

		if !filesystem.reached || code != 6 || stdout != "" || !strings.Contains(stderr, "partitur apply run-1 --recover") {
			t.Fatalf("injection_reached=%t exit=%d stdout=%q stderr=%q", filesystem.reached, code, stdout, stderr)
		}
		assertCommandWitnessJournalDelta(t, store, before)
		assertCommandWitnessApplicationState(t, store, runstate.ApplicationApplying)
		assertCommandWitnessBaseCheckout(t, root)
	})
}

func validateCommandWitnessFixture(t *testing.T, adapterID string, advisory bool) string {
	t.Helper()
	repository := t.TempDir()
	bin := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, filepath.Join(bin, "partitur-adapter-"+adapterID)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", bin)
	t.Setenv(fakeAdapterEnvironment, "1")

	scoreDocument := e2eScore("plan")
	castDocument := e2eCast(map[string]string{"plan": "performer"})
	performer := castDocument["performers"].(map[string]any)["performer"].(map[string]any)
	performer["adapter"] = adapterID
	if advisory {
		performer["allow_advisory_enforcement"] = true
	}
	writeValidateInputs(t, repository, scoreDocument, castDocument)
	return repository
}

func appendCommandWitnessLog(t *testing.T, store *runstore.Store) logstream.Entry {
	t.Helper()
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		_, err := tx.At("command-witness.log").Append(resumeEvent("run-1", runstate.EventLog, map[string]any{
			"level": "info", "message": "witness observation",
		}))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	event := journal.Events[len(journal.Events)-1]
	return logstream.Entry{
		Schema: "partitur/logs+jsonl;v=1", RunID: "run-1", Seq: event.Seq,
		TS: event.Timestamp, Type: "log", Level: "info", Message: "witness observation",
	}
}

func witnessInitCommand(t *testing.T, fixture initFixture) {
	t.Helper()
	repository := writeInitFixture(t, fixture)
	t.Chdir(repository)
	before := snapshotInitTree(t, repository)

	code, stdout, stderr := invokeCommand("init")

	if fixture.ignore != nil && !bytes.Equal(fixture.ignore, []byte(initIgnoreContents)) {
		if code != 2 || stdout != "" || !strings.Contains(stderr, "precondition refused:") {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want differing-ignore refusal", code, stdout, stderr)
		}
		assertCommandWitnessTree(t, repository, before)
		return
	}
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want initialized repository", code, stdout, stderr)
	}
	ignore, err := os.ReadFile(filepath.Join(repository, ".partitur", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ignore, []byte(initIgnoreContents)) {
		t.Fatalf("ignore bytes=%q, want %q", ignore, initIgnoreContents)
	}
	scoreBytes, err := os.ReadFile(filepath.Join(repository, "partitur.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if fixture.score != nil {
		if !bytes.Equal(scoreBytes, fixture.score) {
			t.Fatalf("score bytes=%q, want preserved %q", scoreBytes, fixture.score)
		}
	} else {
		compiled, diagnostics := score.Compile(scoreBytes)
		if len(diagnostics) != 0 {
			t.Fatalf("created score diagnostics=%v", diagnostics)
		}
		movements := compiled.Movements()
		parts := compiled.Parts()
		if compiled.Status() != "draft" || len(movements) != 1 || len(parts) != 1 ||
			compiled.DraftInterviewMovement() != movements[0].ID || movements[0].Phase != "draft" ||
			movements[0].PartID != parts[0].ID {
			t.Fatalf(
				"created score status=%q interview=%q movements=%+v parts=%+v, want one-part interview draft",
				compiled.Status(), compiled.DraftInterviewMovement(), movements, parts,
			)
		}
	}
	want := make(map[string][]byte, len(before)+3)
	for path, contents := range before {
		want[path] = append([]byte(nil), contents...)
	}
	if !fixture.stateExists {
		want[".partitur/"] = nil
	}
	want[".partitur/.gitignore"] = []byte(initIgnoreContents)
	want["partitur.yaml"] = append([]byte(nil), scoreBytes...)
	assertCommandWitnessTree(t, repository, want)
}

func assertCommandWitnessTree(t *testing.T, root string, want map[string][]byte) {
	t.Helper()
	if got := snapshotInitTree(t, root); !reflect.DeepEqual(got, want) {
		t.Fatalf("command mutated repository tree\n before=%#v\n after=%#v", want, got)
	}
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

func commandWitnessJournalDelta(t *testing.T, store *runstore.Store, before int) []runstate.Event {
	t.Helper()
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Events) < before {
		t.Fatalf("journal shrank from %d to %d events", before, len(journal.Events))
	}
	return journal.Events[before:]
}

func assertCommandWitnessJournalDelta(
	t *testing.T,
	store *runstore.Store,
	before int,
	want ...runstate.EventType,
) []runstate.Event {
	t.Helper()
	delta := commandWitnessJournalDelta(t, store, before)
	if len(delta) != len(want) {
		t.Fatalf("journal delta=%v, want %v", eventKinds(delta), want)
	}
	for index, event := range delta {
		if event.Type != want[index] {
			t.Fatalf("journal delta=%v, want %v", eventKinds(delta), want)
		}
	}
	return delta
}

func assertCommandWitnessRunState(t *testing.T, store *runstore.Store, want runstate.RunLifecycle) {
	t.Helper()
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Run != want {
		t.Fatalf("run state=%s, want %s", input.Projection.State.Run, want)
	}
}

func witnessTerminalCancelCleanup(t *testing.T, terminal string, wantCode int) {
	t.Helper()
	root, store := resumeFixture(t, "")
	if _, err := store.AcquireRecoveryDriver("run-1"); err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		return appendFixtureTerminal(t, tx, root, input.BaseCommit, input.BaseTree, terminal)
	}); err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Join(root, ".partitur", "runs", "run-1")
	residues := map[string][]byte{
		filepath.Join(root, ".partitur", "work", "run-1", "attempt-staging"):          []byte("staging"),
		filepath.Join(runRoot, "driver.quiesced.prepare-1"):                           []byte("sidecar"),
		filepath.Join(runRoot, "prepares", "prepare-1.json"):                          []byte("prepare"),
		filepath.Join(runRoot, "inputs", "review", "revision-1", "subject-tree.json"): []byte("review input"),
	}
	for path, contents := range residues {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	before := journalLength(t, store)

	code, stdout, stderr := invokeCommand("cancel", "run-1")

	if code != wantCode || stdout != "" || stderr != "" {
		t.Fatalf("terminal=%s exit=%d stdout=%q stderr=%q, want exit %d", terminal, code, stdout, stderr, wantCode)
	}
	assertCommandWitnessJournalDelta(t, store, before)
	for path := range residues {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("terminal cancel retained residue %q: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(runRoot, "driver.lease")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal cancel retained driver lease: %v", err)
	}
}

func appendCommandWitnessApplyStarted(t *testing.T, store *runstore.Store) string {
	t.Helper()
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	candidate := input.Projection.State.ApplicationCandidate
	if candidate == nil {
		t.Fatal("apply witness candidate is absent")
	}
	txnID := "command-witness-apply"
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		_, err := tx.At("command-witness.apply.started").Append(resumeEvent("run-1", runstate.EventApplyStarted, map[string]any{
			"txn_id": txnID, "candidate_id": candidate.ID, "before_tree": candidate.BaseTree, "result_tree": candidate.ResultTree,
			"touched_paths":     []any{"applied.txt", resumeFixtureBaseFile, "second.txt"},
			"recovery":          map[string]any{"base_tree": candidate.BaseTree, "result_tree": candidate.ResultTree},
			"identity_versions": resumeIdentityVersions(),
		}))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return txnID
}

func appendCommandWitnessApplyRecoveryRequired(t *testing.T, store *runstore.Store, txnID, detail string) {
	t.Helper()
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	candidate := input.Projection.State.ApplicationCandidate
	if candidate == nil {
		t.Fatal("apply witness candidate is absent")
	}
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		_, err := tx.At("command-witness.apply.recovery-required").Append(resumeEvent("run-1", runstate.EventApplyRecoveryRequired, map[string]any{
			"txn_id": txnID, "candidate_id": candidate.ID, "failure_detail": detail,
			"observed_tree": candidate.BaseTree, "identity_versions": resumeIdentityVersions(),
		}))
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func assertCommandWitnessApplicationState(t *testing.T, store *runstore.Store, want runstate.ApplicationState) {
	t.Helper()
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	application := input.Projection.State.Application
	if application.State != want {
		t.Fatalf("application=%+v, want state %s", application, want)
	}
	if (want == runstate.ApplicationApplying || want == runstate.ApplicationRecoveryRequired ||
		want == runstate.ApplicationApplied || want == runstate.ApplicationFailedClean) &&
		(application.TransactionID == "" || application.CandidateID == "") {
		t.Fatalf("application=%+v, want bound transaction and candidate", application)
	}
	if want == runstate.ApplicationRecoveryRequired && application.Reason == "" {
		t.Fatalf("application=%+v, want durable recovery cause", application)
	}
}

func assertCommandWitnessApplicationEvents(t *testing.T, store *runstore.Store, events []runstate.Event) {
	t.Helper()
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	candidate := input.Projection.State.ApplicationCandidate
	if candidate == nil {
		t.Fatal("apply witness candidate is absent")
	}
	wantTxn := input.Projection.State.Application.TransactionID
	for _, event := range events {
		payload := eventPayloadMap(t, event)
		if payload["txn_id"] != wantTxn || payload["candidate_id"] != candidate.ID || payload["identity_versions"] == nil {
			t.Fatalf("%s payload=%#v, want txn=%q candidate=%q with identity versions", event.Type, payload, wantTxn, candidate.ID)
		}
		switch event.Type {
		case runstate.EventApplyStarted:
			recovery, ok := payload["recovery"].(map[string]any)
			if payload["before_tree"] != candidate.BaseTree || payload["result_tree"] != candidate.ResultTree || !ok ||
				recovery["base_tree"] != candidate.BaseTree || recovery["result_tree"] != candidate.ResultTree ||
				!reflect.DeepEqual(payload["touched_paths"], []any{"applied.txt", resumeFixtureBaseFile, "second.txt"}) {
				t.Fatalf("apply.started payload=%#v", payload)
			}
		case runstate.EventApplyCompleted:
			if payload["result_tree"] != candidate.ResultTree {
				t.Fatalf("apply.completed payload=%#v", payload)
			}
		case runstate.EventApplyFailed:
			if payload["rollback_verified"] != true || payload["failure_detail"] == "" {
				t.Fatalf("apply.failed payload=%#v", payload)
			}
		case runstate.EventApplyRecoveryRequired:
			if payload["failure_detail"] == "" {
				t.Fatalf("apply.recovery_required payload=%#v", payload)
			}
		case runstate.EventApplyRecoveryResolved:
			if payload["outcome"] != "rolled_back" {
				t.Fatalf("apply.recovery_resolved payload=%#v", payload)
			}
		default:
			t.Fatalf("unexpected application event %s", event.Type)
		}
	}
}

func prepareCommandWitnessApplySabotage(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("applied.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "applied.txt"), []byte("squatter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func makeCommandWitnessApplyFailedClean(t *testing.T, root string, store *runstore.Store) {
	t.Helper()
	prepareCommandWitnessApplySabotage(t, root)
	t.Chdir(root)
	before := journalLength(t, store)
	code, stdout, stderr := invokeCommand("apply", "run-1")
	if code != 4 || stdout != "" || !strings.Contains(stderr, "application failed cleanly:") {
		t.Fatalf("prepare FAILED_CLEAN exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	delta := assertCommandWitnessJournalDelta(t, store, before, runstate.EventApplyStarted, runstate.EventApplyFailed)
	assertCommandWitnessApplicationEvents(t, store, delta)
	assertCommandWitnessApplicationState(t, store, runstate.ApplicationFailedClean)
	if err := os.Remove(filepath.Join(root, "applied.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	assertCommandWitnessBaseCheckout(t, root)
}

func materializeCommandWitnessCandidate(t *testing.T, root string) {
	t.Helper()
	for _, file := range applyFixtureCandidateFiles {
		path := filepath.Join(root, file.name)
		if file.remove {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(file.contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertCommandWitnessCandidateCheckout(t *testing.T, root string) {
	t.Helper()
	for _, file := range applyFixtureCandidateFiles {
		path := filepath.Join(root, file.name)
		contents, err := os.ReadFile(path)
		if file.remove {
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("removed candidate path %q error=%v", file.name, err)
			}
			continue
		}
		if err != nil || string(contents) != file.contents {
			t.Fatalf("candidate path %q contents=%q error=%v", file.name, contents, err)
		}
	}
}

func assertCommandWitnessBaseCheckout(t *testing.T, root string) {
	t.Helper()
	if contents, err := os.ReadFile(filepath.Join(root, resumeFixtureBaseFile)); err != nil || string(contents) != resumeFixtureBaseContents {
		t.Fatalf("base path contents=%q error=%v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(root, "second.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second candidate path survives rollback: %v", err)
	}
	if contents, err := os.ReadFile(filepath.Join(root, "applied.txt")); err == nil && string(contents) != "squatter\n" {
		t.Fatalf("applied path contents=%q, want absent or ignored squatter", contents)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	applyFixtureApplicationClean(t, root)
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
