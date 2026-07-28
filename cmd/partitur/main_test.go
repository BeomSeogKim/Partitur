package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/driver"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
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
	if code != 1 || stdout.Len() != 0 || stderr.String() != "usage: partitur <command>\ncommands: version, validate, run, status\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestOnlyImplementedCommandsAreAdvertised(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"init"},
		{"logs"},
		{"answer"},
		{"approve"},
		{"amend"},
		{"cancel"},
		{"resume"},
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
				stderr.String() != "usage: partitur <command>\ncommands: version, validate, run, status\n" {
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
