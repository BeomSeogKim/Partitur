package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/driver"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	logstream "github.com/BeomSeogKim/Partitur/internal/logs"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/recoveryexec"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	statusprojection "github.com/BeomSeogKim/Partitur/internal/status"
	validation "github.com/BeomSeogKim/Partitur/internal/validate"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

func TestVersion(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if strings.TrimSpace(stdout.String()) != "dev" || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestStatusRendersProjectionAndClassifiesOutcomes(t *testing.T) {
	base := statusprojection.Report{
		Schema: "partitur/status+json;v=1",
		Run: statusprojection.Run{
			ID:        "run-1",
			Lifecycle: string(runstate.RunSucceeded),
			Score:     statusprojection.ScoreHead{Revision: 4, SemanticHash: "sha256:score", FileHash: "sha256:file"},
			Movements: []statusprojection.Movement{{
				ID:    "inspect",
				State: string(runstate.MovementSucceeded),
				Marks: []statusprojection.Mark{{
					Grade: "VERIFIED", AttemptID: "attempt-2", SubjectTree: "tree", ScoreRevision: 4,
					FailedAttempts: 1,
					Criteria:       []statusprojection.Criterion{{ID: "lint", SpecHash: "sha256:lint"}},
				}},
			}},
		},
		Application: statusprojection.Application{State: "NOT_APPLIED"},
		Promotion:   statusprojection.Promotion{State: "NOT_PROMOTED"},
		Journal:     statusprojection.Journal{Integrity: "INTACT"},
		Recovery:    statusprojection.Recovery{State: "NOT_REQUIRED"},
	}
	for _, test := range []struct {
		name       string
		args       []string
		adjust     func(*statusprojection.Report)
		wantCode   int
		wantStderr string
	}{
		{name: "success", args: []string{"status", "run-1"}, wantCode: 0},
		{name: "terminal failed is reported data", args: []string{"status", "run-1"}, adjust: func(report *statusprojection.Report) { report.Run.Lifecycle = string(runstate.RunFailed) }, wantCode: 0},
		{name: "shipping recovery is reported data", args: []string{"status", "run-1"}, adjust: func(report *statusprojection.Report) {
			report.Application.State = "RECOVERY_REQUIRED"
			report.Recovery = statusprojection.Recovery{State: "RECOVERY_REQUIRED", Reason: "tree mismatch"}
		}, wantCode: 0},
		{name: "torn tail is reported data", args: []string{"status", "run-1"}, adjust: func(report *statusprojection.Report) {
			report.Journal = statusprojection.Journal{Integrity: "TAIL_UNPARSEABLE", TruncatedSeq: 9, DiscardedBytes: 7}
		}, wantCode: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			report := base
			if test.adjust != nil {
				test.adjust(&report)
			}
			var stdout, stderr bytes.Buffer
			code := runWithStatusReader(
				test.args, &stdout, &stderr, nil, nil, nil,
				func(requested string) (statusprojection.Report, error) {
					if requested != "run-1" {
						t.Fatalf("requested run = %q", requested)
					}
					return report, nil
				},
			)
			if code != test.wantCode || stderr.String() != test.wantStderr {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), "Mark: VERIFIED (1 criteria: lint [sha256:lint]; tree tree; rev 4; after 1 failed attempt)") {
				t.Fatalf("mark provenance missing from %q", stdout.String())
			}
		})
	}
}

func TestStatusCorruptProjectionExitsFive(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runWithStatusReader(
		[]string{"status", "run-1"}, &stdout, &stderr, nil, nil, nil,
		func(string) (statusprojection.Report, error) {
			return statusprojection.Report{}, runstore.ErrJournalCorrupt
		},
	)
	if code != 5 || stdout.Len() != 0 ||
		stderr.String() != "recovery halted: detail=\"journal_corrupt\"\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestStatusJSONAndArgumentErrors(t *testing.T) {
	report := statusprojection.Report{
		Schema:      "partitur/status+json;v=1",
		Run:         statusprojection.Run{ID: "run-1", Lifecycle: string(runstate.RunRunning)},
		Application: statusprojection.Application{State: "NOT_APPLIED"},
		Promotion:   statusprojection.Promotion{State: "NOT_PROMOTED"},
		Journal:     statusprojection.Journal{Integrity: "INTACT"},
		Recovery:    statusprojection.Recovery{State: "NOT_REQUIRED"},
	}
	var stdout, stderr bytes.Buffer
	code := runWithStatusReader(
		[]string{"status", "--json", "run-1"}, &stdout, &stderr, nil, nil, nil,
		func(string) (statusprojection.Report, error) { return report, nil },
	)
	var decoded statusprojection.Report
	if code != 0 || stderr.Len() != 0 || json.Unmarshal(stdout.Bytes(), &decoded) != nil ||
		decoded.Schema != report.Schema || decoded.Run.ID != report.Run.ID {
		t.Fatalf("exit=%d stdout=%q stderr=%q decoded=%+v", code, stdout.String(), stderr.String(), decoded)
	}

	stdout.Reset()
	stderr.Reset()
	code = runWithStatusReader(
		[]string{"status", "--nope"}, &stdout, &stderr, nil, nil, nil,
		func(string) (statusprojection.Report, error) {
			t.Fatal("reader called")
			return statusprojection.Report{}, nil
		},
	)
	if code != 1 || stdout.Len() != 0 || stderr.String() != "usage: partitur <command>\ncommands: version, validate, run, resume, status, logs\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestLogsClassifiesObservationOutcomes(t *testing.T) {
	base := logstream.Snapshot{
		RunID:     "run-1",
		Lifecycle: string(runstate.RunRunning),
		Entries: []logstream.Entry{{
			Schema: "partitur/logs+jsonl;v=1", RunID: "run-1", Seq: 2,
			TS: "2026-07-28T00:00:00.000Z", Type: "log", Level: "info", Message: "started",
		}},
	}
	for _, test := range []struct {
		name     string
		adjust   func(*logstream.Snapshot)
		err      error
		wantCode int
	}{
		{name: "terminal run is reported data", adjust: func(snapshot *logstream.Snapshot) {
			snapshot.Lifecycle = string(runstate.RunSucceeded)
		}, wantCode: 0},
		{name: "torn tail is reported data", wantCode: 0},
		{name: "corrupt prefix cannot stream", err: runstore.ErrJournalCorrupt, wantCode: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := base
			var stdout, stderr bytes.Buffer
			code := runWithReaders(
				[]string{"logs", "run-1", "--jsonl"}, &stdout, &stderr,
				func() validation.Result { t.Fatal("validator called"); return validation.Result{} },
				func() (*validation.Preparation, validation.Result) {
					t.Fatal("preparer called")
					return nil, validation.Result{}
				},
				func(context.Context, *validation.Preparation, driver.StartedObserver) driver.Result {
					t.Fatal("driver called")
					return driver.Result{}
				},
				func(string) (statusprojection.Report, error) {
					t.Fatal("status reader called")
					return statusprojection.Report{}, nil
				},
				func(requested string) (logstream.Snapshot, error) {
					if requested != "run-1" {
						t.Fatalf("requested run = %q", requested)
					}
					return snapshot, test.err
				},
				logstream.Stream,
			)
			if code != test.wantCode {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if test.wantCode == 0 && !strings.Contains(stdout.String(), `"schema":"partitur/logs+jsonl;v=1"`) {
				t.Fatalf("logs JSONL missing from %q", stdout.String())
			}
			if test.wantCode == 5 && (stdout.Len() != 0 || stderr.String() != "recovery halted: detail=\"journal_corrupt\"\n") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestObservationOutputFailuresAreNotRecoveryHalts(t *testing.T) {
	report := statusprojection.Report{
		Schema:      "partitur/status+json;v=1",
		Run:         statusprojection.Run{ID: "run-1", Lifecycle: string(runstate.RunRunning)},
		Application: statusprojection.Application{State: "NOT_APPLIED"},
		Promotion:   statusprojection.Promotion{State: "NOT_PROMOTED"},
		Journal:     statusprojection.Journal{Integrity: "INTACT"},
		Recovery:    statusprojection.Recovery{State: "NOT_REQUIRED"},
	}
	snapshot := logstream.Snapshot{
		RunID:     "run-1",
		Lifecycle: string(runstate.RunRunning),
		Entries: []logstream.Entry{{
			Schema: "partitur/logs+jsonl;v=1", RunID: "run-1", Seq: 2,
			TS: "2026-07-28T00:00:00.000Z", Type: "log", Level: "info", Message: "started",
		}},
	}
	for _, test := range []struct {
		name       string
		args       []string
		writeErr   error
		wantCode   int
		wantStderr string
	}{
		{name: "logs broken pipe is silent success", args: []string{"logs", "run-1", "--jsonl"}, writeErr: syscall.EPIPE, wantCode: 0},
		{name: "logs other write failure is refused", args: []string{"logs", "run-1", "--jsonl"}, writeErr: errors.New("disk full"), wantCode: 2,
			wantStderr: "precondition refused: detail=\"output stream is unwritable: logs output failed: disk full\"\n"},
		{name: "status broken pipe is silent success", args: []string{"status", "run-1", "--json"}, writeErr: syscall.EPIPE, wantCode: 0},
		{name: "status other write failure is refused", args: []string{"status", "run-1", "--json"}, writeErr: errors.New("disk full"), wantCode: 2,
			wantStderr: "precondition refused: detail=\"output stream is unwritable: disk full\"\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout := &failingWriter{err: test.writeErr}
			var stderr bytes.Buffer
			code := runWithReaders(
				test.args, stdout, &stderr,
				func() validation.Result { t.Fatal("validator called"); return validation.Result{} },
				func() (*validation.Preparation, validation.Result) {
					t.Fatal("preparer called")
					return nil, validation.Result{}
				},
				func(context.Context, *validation.Preparation, driver.StartedObserver) driver.Result {
					t.Fatal("driver called")
					return driver.Result{}
				},
				func(requested string) (statusprojection.Report, error) {
					if requested != "run-1" {
						t.Fatalf("requested status run = %q", requested)
					}
					return report, nil
				},
				func(requested string) (logstream.Snapshot, error) {
					if requested != "run-1" {
						t.Fatalf("requested logs run = %q", requested)
					}
					return snapshot, nil
				},
				logstream.Stream,
			)
			if code != test.wantCode || stderr.String() != test.wantStderr {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestOnlyImplementedCommandsAreAdvertised(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"init"},
		{"answer"},
		{"approve"},
		{"amend"},
		{"cancel"},
		{"promote-score"},
		{"apply"},
		{"version", "extra"},
		{"validate", "extra"},
		{"validate", "--json"},
	} {
		args := args
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runWithValidate(
				args,
				&stdout,
				&stderr,
				func() validation.Result {
					t.Fatal("validator called for a usage error")
					return validation.Result{}
				},
			); code != 1 {
				t.Fatalf("args=%v exit code=%d", args, code)
			}
			if stdout.Len() != 0 ||
				stderr.String() != "usage: partitur <command>\ncommands: version, validate, run, resume, status, logs\n" {
				t.Fatalf(
					"args=%v stdout=%q stderr=%q",
					args,
					stdout.String(),
					stderr.String(),
				)
			}
		})
	}
}

func TestRunPrintsDurableIDOnceBeforeTerminalOutcome(t *testing.T) {
	tests := []struct {
		name    string
		outcome driver.Outcome
		reason  string
		err     error
		code    int
		stderr  string
	}{
		{
			name:    "succeeded ignores lease cleanup error",
			outcome: driver.OutcomeSucceeded,
			err:     errors.New("lease cleanup failed"),
			code:    0,
		},
		{
			name:    "failed ignores last incidental error",
			outcome: driver.OutcomeFailed,
			reason:  "movement_failed",
			err:     errors.New("lease cleanup also failed"),
			code:    4,
			stderr:  "run terminal: state=\"FAILED\" reason=\"movement_failed\"\n",
		},
		{
			name:    "cancelled is not success",
			outcome: driver.OutcomeCancelled,
			reason:  "cancelled",
			code:    4,
			stderr:  "run terminal: state=\"CANCELLED\" reason=\"cancelled\"\n",
		},
		{
			name:    "halt",
			outcome: driver.OutcomeHalted,
			reason:  "sweep_unverifiable",
			code:    5,
			stderr:  "recovery halted: reason=\"sweep_unverifiable\"\n",
		},
		{
			name:    "operational interruption",
			outcome: driver.OutcomeInterrupted,
			err:     errors.New("driver lease unavailable"),
			code:    6,
			stderr: "run interrupted: " +
				"run_id=\"019d0000-0000-7000-8000-000000000001\" " +
				"state=\"nonterminal\" " +
				"resume=\"partitur resume 019d0000-0000-7000-8000-000000000001\" " +
				"detail=\"driver lease unavailable\"\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runWithRunners(
				[]string{"run"},
				&stdout,
				&stderr,
				func() validation.Result {
					t.Fatal("validate runner called")
					return validation.Result{}
				},
				func() (*validation.Preparation, validation.Result) {
					return &validation.Preparation{}, validation.Result{}
				},
				func(
					_ context.Context,
					_ *validation.Preparation,
					started driver.StartedObserver,
				) driver.Result {
					if err := started("019d0000-0000-7000-8000-000000000001"); err != nil {
						t.Fatal(err)
					}
					if stdout.String() != "019d0000-0000-7000-8000-000000000001\n" {
						t.Fatalf("id was not observable before terminal result: %q", stdout.String())
					}
					return driver.Result{
						RunID:   runstate.RunID("019d0000-0000-7000-8000-000000000001"),
						Outcome: test.outcome,
						Reason:  test.reason,
						Err:     test.err,
					}
				},
			)
			if code != test.code ||
				stdout.String() != "019d0000-0000-7000-8000-000000000001\n" ||
				stderr.String() != test.stderr {
				t.Fatalf(
					"exit=%d stdout=%q stderr=%q",
					code,
					stdout.String(),
					stderr.String(),
				)
			}
		})
	}
}

func TestRunIDWriteFailureIsOperationalInterruption(t *testing.T) {
	stdout := &failingWriter{err: errors.New("stdout unavailable")}
	var stderr bytes.Buffer
	code := runWithRunners(
		[]string{"run"},
		stdout,
		&stderr,
		func() validation.Result {
			t.Fatal("validate runner called")
			return validation.Result{}
		},
		func() (*validation.Preparation, validation.Result) {
			return &validation.Preparation{}, validation.Result{}
		},
		func(
			_ context.Context,
			_ *validation.Preparation,
			started driver.StartedObserver,
		) driver.Result {
			runID := runstate.RunID("019d0000-0000-7000-8000-000000000002")
			err := started(runID)
			if !errors.Is(err, stdout.err) {
				t.Fatalf("id write error = %v", err)
			}
			return driver.Result{
				RunID:   runID,
				Outcome: driver.OutcomeInterrupted,
				Err:     err,
			}
		},
	)
	wantStderr := "run interrupted: " +
		"run_id=\"019d0000-0000-7000-8000-000000000002\" " +
		"state=\"nonterminal\" " +
		"resume=\"partitur resume 019d0000-0000-7000-8000-000000000002\" " +
		"detail=\"stdout unavailable\"\n"
	if code != 6 || stdout.calls != 1 || stderr.String() != wantStderr {
		t.Fatalf(
			"exit=%d stdout_calls=%d stderr=%q",
			code,
			stdout.calls,
			stderr.String(),
		)
	}
}

func TestResumeMapsOnlyExecutorOutcomesAndNeverWritesStdout(t *testing.T) {
	tests := []struct {
		name       string
		result     recoveryexec.Result
		wantCode   int
		wantStderr string
	}{
		{name: "succeeded", result: recoveryexec.Result{Outcome: recoveryexec.OutcomeSucceeded}, wantCode: 0},
		{name: "quiescent", result: recoveryexec.Result{Outcome: recoveryexec.OutcomeQuiescent}, wantCode: 0},
		{name: "failed", result: recoveryexec.Result{Outcome: recoveryexec.OutcomeFailed}, wantCode: 4},
		{name: "cancelled", result: recoveryexec.Result{Outcome: recoveryexec.OutcomeCancelled}, wantCode: 4},
		{name: "live owner refusal", result: recoveryexec.Result{Outcome: recoveryexec.OutcomeRefused}, wantCode: 2, wantStderr: "precondition refused: detail=\"driver authority is already held\"\n"},
		{name: "halt", result: recoveryexec.Result{Outcome: recoveryexec.OutcomeHalted, Decision: recovery.Decision{Halt: recovery.HaltRootSnapshotDivergence}}, wantCode: 5, wantStderr: "recovery halted: run_id=\"run-1\" reason=\"root_snapshot_divergence\"\n"},
		{name: "no outcome is operational interruption", result: recoveryexec.Result{}, wantCode: 6, wantStderr: "run interrupted: run_id=\"run-1\" state=\"nonterminal\" resume=\"partitur resume run-1\" detail=\"recovery produced no command outcome\"\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runResume("run-1", &stdout, &stderr, func(context.Context, string) (recoveryexec.Result, error) {
				return test.result, nil
			})
			if code != test.wantCode || stdout.Len() != 0 || stderr.String() != test.wantStderr {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestResumeRootObservationAndCastIsolationUseRealStore(t *testing.T) {
	for _, test := range []struct {
		name       string
		root       []byte
		wantCode   int
		wantStderr string
	}{
		{name: "absent root is ignored", wantCode: 6, wantStderr: resumeUnreachableStderr()},
		{name: "malformed root is ignored", root: []byte("score: ["), wantCode: 6, wantStderr: resumeUnreachableStderr()},
		{name: "different root revision is ignored", root: resumeScore(2, "same otherwise"), wantCode: 6, wantStderr: resumeUnreachableStderr()},
		{name: "same revision semantic divergence halts", root: resumeScore(1, "different semantics"), wantCode: 5, wantStderr: "recovery halted: run_id=\"run-1\" reason=\"root_snapshot_divergence\"\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, _ := resumeFixture(t, "")
			if test.root != nil {
				if err := os.WriteFile(filepath.Join(root, "partitur.yaml"), test.root, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.MkdirAll(filepath.Join(root, ".partitur"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".partitur", "cast.yaml"), []byte("cast: ["), 0o600); err != nil {
				t.Fatal(err)
			}
			journal := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
			before, err := os.ReadFile(journal)
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(root)
			var stdout, stderr bytes.Buffer
			if code := run([]string{"resume", "run-1"}, &stdout, &stderr); code != test.wantCode || stdout.Len() != 0 || stderr.String() != test.wantStderr {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			after, err := os.ReadFile(journal)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantCode == 5 && !bytes.Equal(before, after) {
				t.Fatal("halt mutated journal")
			}
		})
	}
}

func TestResumeRefusesLiveOwnerWithoutMutation(t *testing.T) {
	root, store := resumeFixture(t, "")
	driver, err := store.AcquireRecoveryDriver("run-1")
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Release()
	journal := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
	lease := filepath.Join(root, ".partitur", "runs", "run-1", "driver.lease")
	beforeJournal, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	beforeLease, err := os.ReadFile(lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"resume", "run-1"}, &stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.String() != "precondition refused: detail=\"driver authority is already held\"\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	afterJournal, _ := os.ReadFile(journal)
	afterLease, _ := os.ReadFile(lease)
	if !bytes.Equal(beforeJournal, afterJournal) || !bytes.Equal(beforeLease, afterLease) {
		t.Fatal("live-owner refusal mutated journal or lease")
	}
}

func TestResumeTreatsTerminalProjectionIdempotently(t *testing.T) {
	for _, test := range []struct {
		state string
		code  int
	}{
		{state: "SUCCEEDED", code: 0},
		{state: "FAILED", code: 4},
		{state: "CANCELLED", code: 4},
	} {
		t.Run(test.state, func(t *testing.T) {
			root, _ := resumeFixture(t, test.state)
			t.Chdir(root)
			var stdout, stderr bytes.Buffer
			if code := run([]string{"resume", "run-1"}, &stdout, &stderr); code != test.code || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestResumeMapsRealStoreHaltsToExitFive(t *testing.T) {
	for _, test := range []struct {
		name       string
		fixture    func(*testing.T) string
		wantStderr string
	}{
		{
			name: "journal corrupt during selection",
			fixture: func(t *testing.T) string {
				root, _ := resumeFixture(t, "")
				journal := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
				contents, err := os.ReadFile(journal)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(journal, append(contents, []byte("\n{}\n{}\n")...), 0o600); err != nil {
					t.Fatal(err)
				}
				return root
			},
			wantStderr: "recovery halted: detail=\"journal_corrupt: unparseable non-final line: invalid JSON\"\n",
		},
		{
			name: "recorded adapter session is unverifiable",
			fixture: func(t *testing.T) string {
				root, store := resumeAttemptFixture(t)
				appendResumeAttempt(t, store, true)
				return root
			},
			wantStderr: "recovery halted: run_id=\"run-1\" reason=\"sweep_unverifiable\"\n",
		},
		{
			name: "spawn handoff is unverifiable",
			fixture: func(t *testing.T) string {
				root, store := resumeAttemptFixture(t)
				appendResumeAttempt(t, store, false)
				launchDir := filepath.Join(root, ".partitur", "work", "run-1", "attempt-1", "launch-1")
				if err := os.MkdirAll(launchDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(launchDir, "marker"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return root
			},
			wantStderr: "recovery halted: run_id=\"run-1\" reason=\"spawn_handoff_unverifiable\"\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := test.fixture(t)
			t.Chdir(root)
			var stdout, stderr bytes.Buffer
			if code := run([]string{"resume", "run-1"}, &stdout, &stderr); code != 5 || stdout.Len() != 0 || stderr.String() != test.wantStderr {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestResumeTerminalCleanupRemovesLeaseAndStagingRoot(t *testing.T) {
	root, store := resumeFixture(t, "")
	driver, err := store.AcquireRecoveryDriver("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Append(resumeEvent("run-1", runstate.EventRunFailed, map[string]any{"reason": "fixture"}), "fixture.failed"); err != nil {
		t.Fatal(err)
	}
	stagingRoot := filepath.Join(root, ".partitur", "work", "run-1")
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingRoot, "residue"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"resume", "run-1"}, &stdout, &stderr); code != 4 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".partitur", "runs", "run-1", "driver.lease")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lease stat error=%v, want not exist", err)
	}
	if _, err := os.Stat(stagingRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging root stat error=%v, want not exist", err)
	}
}

func resumeFixture(t *testing.T, terminal string) (string, *runstore.Store) {
	return resumeFixtureWithInputs(t, terminal, resumeScore(1, "pinned run authority"), []byte("cast: \"0.1\"\nperformers: {}\nbindings: {}\n"))
}

func resumeAttemptFixture(t *testing.T) (string, *runstore.Store) {
	return resumeFixtureWithInputs(t, "", resumeAttemptScore(), []byte("cast: \"0.1\"\nperformers:\n  reviewer:\n    adapter: adapter\n    model: model\nbindings:\n  reviewer:\n    performer: reviewer\n"))
}

func resumeFixtureWithInputs(t *testing.T, terminal string, snapshot, resolvedCast []byte) (string, *runstore.Store) {
	t.Helper()
	root := t.TempDir()
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, diagnostics := score.Compile(snapshot)
	if len(diagnostics) != 0 {
		t.Fatalf("score diagnostics=%v", diagnostics)
	}
	scoreHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	resolved, castDiagnostics := cast.Resolve([]cast.Layer{{Origin: "fixture", Data: resolvedCast}})
	if len(castDiagnostics) != 0 {
		t.Fatalf("cast diagnostics=%v", castDiagnostics)
	}
	castHash, err := resolved.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		if _, err := tx.At("fixture.score").PublishImmutable("scores/revision-1.yaml", snapshot, runstore.Hash(resumeHash(snapshot))); err != nil {
			return err
		}
		if _, err := tx.At("fixture.cast").PublishImmutable("resolved-cast.yaml", resolvedCast, runstore.Hash(resumeHash(resolvedCast))); err != nil {
			return err
		}
		if _, err := tx.At("fixture.start").Append(resumeEvent("run-1", runstate.EventRunStarted, map[string]any{"base_commit": "base", "base_tree": "tree", "score_hash": scoreHash, "score_file_hash": resumeHash(snapshot), "resolved_cast_hash": castHash, "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}})); err != nil {
			return err
		}
		if terminal != "" {
			return appendFixtureTerminal(tx, terminal)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return root, store
}

func appendResumeAttempt(t *testing.T, store *runstore.Store, started bool) {
	t.Helper()
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		for _, event := range []runstate.Event{
			{RunID: "run-1", ScoreRevision: 1, MovementID: "review", Type: runstate.EventMovementReady, Payload: resumePayload(t, map[string]any{})},
			{RunID: "run-1", ScoreRevision: 1, MovementID: "review", Type: runstate.EventMovementStarted, Payload: resumePayload(t, map[string]any{})},
			{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventPerformerSelected, Payload: resumePayload(t, map[string]any{"reason": "initial", "performer_id": "reviewer", "adapter_id": "adapter", "model": "model"})},
		} {
			if _, err := tx.At("fixture.attempt").Append(event); err != nil {
				return err
			}
		}
		if !started {
			return nil
		}
		for _, event := range []runstate.Event{
			{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventAttemptStarted, Payload: resumePayload(t, map[string]any{"attempt_number": 1, "adapter_process": map[string]any{"pid": os.Getpid(), "session_id": os.Getpid(), "start_identity": map[string]any{"platform": "linux", "boot_id": "fixture", "start_ticks": "0"}}, "granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{"**"}, "shell": false, "network": false}, "identity_versions": resumeIdentityVersions()})},
			{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventAdapterProbed, Payload: resumePayload(t, map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": resumeIdentityVersions()})},
		} {
			if _, err := tx.At("fixture.attempt").Append(event); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func appendFixtureTerminal(tx *runstore.Txn, terminal string) error {
	switch terminal {
	case "FAILED":
		_, err := tx.At("fixture.failed").Append(resumeEvent("run-1", runstate.EventRunFailed, map[string]any{"reason": "fixture"}))
		return err
	case "CANCELLED":
		_, err := tx.At("fixture.cancelled").Append(resumeEvent("run-1", runstate.EventRunCancelled, map[string]any{"cancelled_movement_ids": []any{}, "cancelled_attempt_ids": []any{}, "obsoleted_decision_ids": []any{}}))
		return err
	case "SUCCEEDED":
		_, err := tx.At("fixture.succeeded").Append(resumeEvent("run-1", runstate.EventRunSucceeded, map[string]any{"candidate": map[string]any{"candidate_id": "candidate", "base_tree": "base", "result_tree": "result", "ordered_change_sets": []any{}, "contributors": []any{}, "candidate_composition_dependency_hash": "hash"}, "waiver": map[string]any{"reason": "fixture"}, "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}}))
		return err
	default:
		return fmt.Errorf("unknown terminal %q", terminal)
	}
}

func resumeEvent(runID string, eventType runstate.EventType, payload any) runstate.Event {
	return runstate.Event{RunID: runstate.RunID(runID), ScoreRevision: 1, Type: eventType, Payload: resumePayload(nil, payload)}
}

func resumePayload(t *testing.T, payload any) json.RawMessage {
	encoded, err := json.Marshal(payload)
	if err != nil && t != nil {
		t.Fatal(err)
	}
	return encoded
}

func resumeHash(value []byte) string {
	digest := sha256.Sum256(value)
	return fmt.Sprintf("sha256:%x", digest)
}

func resumeScore(revision int, goal string) []byte {
	return []byte(fmt.Sprintf("score: \"0.2\"\nname: resume-fixture\nrevision: %d\nstatus: finalized\ngoal: %s\nverification:\n  expectation:\n    intent: pass-existing-tests\n    apply_gate:\n      waived: true\n      reason: fixture\nparts: {}\nmovements: []\npolicy:\n  allowed_paths: [\"**\"]\n  budget:\n    active_wall_clock_min: 10\n", revision, goal))
}

func resumeAttemptScore() []byte {
	return []byte("score: \"0.2\"\nname: resume-attempt-fixture\nrevision: 1\nstatus: finalized\ngoal: fixture\nverification:\n  expectation:\n    intent: pass-existing-tests\n    apply_gate:\n      waived: true\n      reason: fixture\nparts:\n  reviewer:\n    capabilities: [repo_read]\nmovements:\n  - id: review\n    part: reviewer\n    grants: [repo_read]\n    instruction: inspect\npolicy:\n  allowed_paths: [\"**\"]\n  budget:\n    active_wall_clock_min: 10\n")
}

func resumeIdentityVersions() map[string]any {
	return map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}
}

func resumeUnreachableStderr() string {
	return "run interrupted: run_id=\"run-1\" state=\"nonterminal\" resume=\"partitur resume run-1\" detail=\"recovery action is unreachable in this slice: proceed_c4\"\n"
}

type failingWriter struct {
	calls int
	err   error
}

func (writer *failingWriter) Write([]byte) (int, error) {
	writer.calls++
	return 0, writer.err
}

func TestRunPreStartFailureDoesNotPrintID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runWithRunners(
		[]string{"run"},
		&stdout,
		&stderr,
		func() validation.Result { return validation.Result{} },
		func() (*validation.Preparation, validation.Result) {
			return &validation.Preparation{}, validation.Result{}
		},
		func(
			context.Context,
			*validation.Preparation,
			driver.StartedObserver,
		) driver.Result {
			return driver.Result{Err: workspace.ErrNotRepository}
		},
	)
	if code != 2 || stdout.Len() != 0 ||
		!strings.Contains(stderr.String(), "precondition refused") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestValidateRefusalIsExitTwo(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	result := validation.Result{Refusal: &validation.Refusal{
		Kind:   validation.RefusalRequiredInput,
		Path:   "/repo/partitur.yaml",
		Detail: "file does not exist",
	}}
	code := runWithValidate(
		[]string{"validate"},
		&stdout,
		&stderr,
		func() validation.Result { return result },
	)
	if code != 2 || stdout.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout.String())
	}
	want := "precondition refused: kind=\"required_input_unavailable\" " +
		"path=\"/repo/partitur.yaml\" detail=\"file does not exist\"\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestValidateRendersOrderedBlocksAndExitsThree(t *testing.T) {
	t.Parallel()
	result := validation.Result{Entries: []validation.Entry{
		{
			Kind:    validation.EntryScore,
			Rule:    "§2.4",
			Pointer: "/movements/1/id",
			Detail:  "duplicate_movement_id",
		},
		{
			Kind:    validation.EntryCast,
			Rule:    "cast.score",
			Pointer: "/bindings/build",
			Detail:  "binding_missing",
		},
		{
			Kind:        validation.EntryAdapterEnvironment,
			AdapterID:   "missing",
			AdapterKind: "executable_absent",
			Detail:      "not found\nsecond line",
			Stderr:      "safe\tstderr",
		},
		{
			Kind:                validation.EntryCapability,
			PartID:              "plan",
			PerformerID:         "primary",
			MissingCapabilities: []string{"network", "shell"},
		},
		{
			Kind:        validation.EntryEnforcement,
			MovementID:  "build",
			PartID:      "write",
			PerformerID: "writer",
			UnmetDimensions: []cast.EnforcementDimension{
				cast.DimensionPathGrants,
				cast.DimensionReadOnly,
			},
		},
	}}
	var stdout, stderr bytes.Buffer
	code := runWithValidate(
		[]string{"validate"},
		&stdout,
		&stderr,
		func() validation.Result { return result },
	)
	if code != 3 || stdout.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout.String())
	}
	want := "" +
		"score: rule=\"§2.4\" pointer=\"/movements/1/id\" detail=\"duplicate_movement_id\"\n" +
		"cast: rule=\"cast.score\" origin=\"\" pointer=\"/bindings/build\" detail=\"binding_missing\"\n" +
		"adapter-environment: adapter=\"missing\" kind=\"executable_absent\" detail=\"not found\\nsecond line\" stderr=\"safe\\tstderr\"\n" +
		"capability: part=\"plan\" performer=\"primary\" missing=[\"network\" \"shell\"]\n" +
		"enforcement: movement=\"build\" part=\"write\" performer=\"writer\" unmet=[\"path_grants\" \"read_only\"]\n"
	if stderr.String() != want {
		t.Fatalf("stderr differs\n got: %q\nwant: %q", stderr.String(), want)
	}
}

func TestAdvisoryReportUsesStderrAndKeepsExitZero(t *testing.T) {
	t.Parallel()
	result := validation.Result{Entries: []validation.Entry{{
		Kind:            validation.EntryEnforcementAdvisory,
		MovementID:      "build",
		PartID:          "write",
		PerformerID:     "writer",
		UnmetDimensions: []cast.EnforcementDimension{cast.DimensionPathGrants},
	}}}
	var stdout, stderr bytes.Buffer
	code := runWithValidate(
		[]string{"validate"},
		&stdout,
		&stderr,
		func() validation.Result { return result },
	)
	if code != 0 || stdout.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout.String())
	}
	want := "enforcement advisory: movement=\"build\" part=\"write\" " +
		"performer=\"writer\" unmet=[\"path_grants\"]\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestValidateSuccessIsSilent(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := runWithValidate(
		[]string{"validate"},
		&stdout,
		&stderr,
		func() validation.Result { return validation.Result{} },
	)
	if code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf(
			"exit=%d stdout=%q stderr=%q",
			code,
			stdout.String(),
			stderr.String(),
		)
	}
}
