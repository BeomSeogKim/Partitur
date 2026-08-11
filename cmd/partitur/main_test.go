package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/amendmentexec"
	"github.com/BeomSeogKim/Partitur/internal/cancellation"
	"github.com/BeomSeogKim/Partitur/internal/canonical"
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

func TestApplyMaterializesCandidateWithoutChangingHeadOrIndex(t *testing.T) {
	root, store := resumeFixture(t, "")
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	baseTree := input.BaseTree
	baseCommit := input.BaseCommit
	if err := os.WriteFile(filepath.Join(root, "applied.txt"), []byte("candidate result\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "applied.txt")
	runGit(t, root, "commit", "--quiet", "-m", "candidate")
	gitOutput := func(arguments ...string) string {
		t.Helper()
		command := exec.Command("git", arguments...)
		command.Dir = root
		output, err := command.Output()
		if err != nil {
			t.Fatalf("git %v: %v", arguments, err)
		}
		return strings.TrimSpace(string(output))
	}
	format := gitOutput("rev-parse", "--show-object-format")
	resultTree := "git-" + format + ":" + gitOutput("rev-parse", "HEAD^{tree}")
	_, baseObject, ok := strings.Cut(baseCommit, ":")
	if !ok {
		t.Fatalf("base commit %q is not qualified", baseCommit)
	}
	runGit(t, root, "reset", "--hard", "--quiet", baseObject)
	if err := os.Remove(filepath.Join(root, "applied.txt")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte(".partitur/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		_, err := tx.At("fixture.succeeded").Append(resumeEvent("run-1", runstate.EventRunSucceeded, map[string]any{
			"candidate": map[string]any{
				"candidate_id": candidateID, "base_tree": baseTree, "result_tree": resultTree,
				"ordered_change_sets": []any{}, "contributors": []any{},
				"candidate_composition_dependency_hash": compositionHash,
			},
			"waiver":            map[string]any{"reason": "fixture"},
			"identity_versions": resumeIdentityVersions(),
		}))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	indexPath := gitOutput("rev-parse", "--git-path", "index")
	if !filepath.IsAbs(indexPath) {
		indexPath = filepath.Join(root, indexPath)
	}
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	headBefore, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", "run-1"}, &stdout, &stderr); code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("apply exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if contents, err := os.ReadFile(filepath.Join(root, "applied.txt")); err != nil || string(contents) != "candidate result\n" {
		t.Fatalf("applied checkout contents=%q err=%v", contents, err)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(indexBefore, indexAfter) {
		t.Fatalf("user index changed: before=%x after=%x err=%v", indexBefore, indexAfter, err)
	}
	headAfter, err := os.ReadFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil || !bytes.Equal(headBefore, headAfter) {
		t.Fatalf("HEAD changed: before=%q after=%q err=%v", headBefore, headAfter, err)
	}
	leftovers, err := filepath.Glob(filepath.Join(root, ".git", "partitur-apply-index-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary indexes=%v err=%v", leftovers, err)
	}
	journalPath := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
	journalBeforeRetry, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Events) < 2 || journal.Events[len(journal.Events)-2].Type != runstate.EventApplyStarted || journal.Events[len(journal.Events)-1].Type != runstate.EventApplyCompleted {
		t.Fatalf("application journal tail=%v", journal.Events[len(journal.Events)-2:])
	}
	if code := run([]string{"apply", "run-1"}, &stdout, &stderr); code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("idempotent apply exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	journalAfterRetry, err := os.ReadFile(journalPath)
	if err != nil || !bytes.Equal(journalBeforeRetry, journalAfterRetry) {
		t.Fatalf("idempotent apply changed journal: before=%q after=%q err=%v", journalBeforeRetry, journalAfterRetry, err)
	}
}

func TestApplyRecoverRecordsRequiredThenResolvesBaseTree(t *testing.T) {
	root, store := resumeFixture(t, "SUCCEEDED")
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	candidate := input.Projection.State.ApplicationCandidate
	if candidate == nil {
		t.Fatal("fixture candidate is missing")
	}
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		_, err := tx.At("fixture.apply.started").Append(resumeEvent("run-1", runstate.EventApplyStarted, map[string]any{
			"txn_id": "apply-fixture", "candidate_id": candidate.ID, "before_tree": candidate.BaseTree, "result_tree": candidate.ResultTree,
			"touched_paths": []any{}, "recovery": map[string]any{"base_tree": candidate.BaseTree, "result_tree": candidate.ResultTree},
			"identity_versions": resumeIdentityVersions(),
		}))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"apply", "run-1"}, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "normal apply is refused") {
		t.Fatalf("normal form exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"apply", "run-1", "--recover"}, &stdout, &stderr); code != 4 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("recover exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Events) < 3 || journal.Events[len(journal.Events)-2].Type != runstate.EventApplyRecoveryRequired || journal.Events[len(journal.Events)-1].Type != runstate.EventApplyRecoveryResolved {
		t.Fatalf("recovery journal tail=%v", journal.Events[len(journal.Events)-3:])
	}
}

func TestProductionExecutionDependenciesWireAmendmentDispositioner(t *testing.T) {
	execution := productionExecutionDependencies(faultpoint.Nop{})
	if _, ok := execution.ProposalDisposition.(amendmentexec.ProposalDispositioner); !ok {
		t.Fatalf("production proposal dispositioner = %T, want amendmentexec.ProposalDispositioner", execution.ProposalDisposition)
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
	if code != 1 || stdout.Len() != 0 || stderr.String() != "usage: partitur <command>\ncommands: version, init, validate, run, resume, answer, approve, amend, apply, promote-score, cancel, status, logs\n" {
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
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"answer"},
		{"approve"},
		{"amend"},
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
				stderr.String() != "usage: partitur <command>\ncommands: version, init, validate, run, resume, answer, approve, amend, apply, promote-score, cancel, status, logs\n" {
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

func TestAmendCommandDispositionsAndCommandAuthority(t *testing.T) {
	for _, test := range []struct {
		name      string
		patch     string
		wantCode  int
		wantEvent runstate.EventType
		check     func(*testing.T, *runstore.Store)
	}{
		{
			name: "rejected", patch: `[{"op":"replace","path":"/revision","value":2}]`, wantCode: 3, wantEvent: runstate.EventAmendmentRejected,
		},
		{
			name: "routed stays non-blocking", patch: `[{"op":"replace","path":"/goal","value":"needs-review"}]`, wantCode: 0, wantEvent: runstate.EventDecisionRequested,
			check: func(t *testing.T, store *runstore.Store) {
				t.Helper()
				input, err := store.LoadRunInput("run-1")
				if err != nil {
					t.Fatal(err)
				}
				if input.Projection.State.Run != runstate.RunRunning || len(input.Projection.State.PendingDecisions) != 1 {
					t.Fatalf("routed CLI state = run:%s pending:%+v, want RUNNING with one non-blocking decision", input.Projection.State.Run, input.Projection.State.PendingDecisions)
				}
				for _, decision := range input.Projection.State.PendingDecisions {
					if decision.Type != "amendment" || decision.Blocking {
						t.Fatalf("CLI decision = %+v, want non-blocking amendment", decision)
					}
				}
			},
		},
		{
			name: "auto approved commits but does not acquire driver authority", patch: `[{"op":"replace","path":"/policy/budget/active_wall_clock_min","value":9}]`, wantCode: 0, wantEvent: runstate.EventAmendmentApproved,
			check: func(t *testing.T, store *runstore.Store) {
				t.Helper()
				input, err := store.LoadRunInput("run-1")
				if err != nil {
					t.Fatal(err)
				}
				if input.Projection.State.ScoreHead.Revision != 2 || input.Projection.State.PendingPrepare != nil {
					t.Fatalf("auto command state = head:%+v pending:%+v, want committed revision 2", input.Projection.State.ScoreHead, input.Projection.State.PendingPrepare)
				}
				if _, present, err := store.ReadLease("run-1"); err != nil || present {
					t.Fatalf("CLI amend lease present=%t error=%v, want no driver lease", present, err)
				}
				_, _ = resume(context.Background(), "run-1")
				journal, err := store.ReadJournal("run-1")
				if err != nil {
					t.Fatal(err)
				}
				if !hasCommandRevisionRestartSelection(journal.Events, 2) {
					t.Fatal("subsequent resume did not materialize the committed revision restart")
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, store := amendCommandFixture(t, true)
			patchPath := filepath.Join(root, "patch.json")
			if err := os.WriteFile(patchPath, []byte(test.patch), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if code := runAmend("", patchPath, "operator correction", "", &stdout, &stderr); code != test.wantCode {
				t.Fatalf("amend exit=%d stderr=%q, want %d", code, stderr.String(), test.wantCode)
			}
			if stdout.Len() != 0 {
				t.Fatalf("amend stdout=%q, want empty", stdout.String())
			}
			if test.wantEvent == runstate.EventDecisionRequested {
				for _, fact := range []string{"proposal_id=", "decision_id=", "reason=", "actual_impact="} {
					if !strings.Contains(stderr.String(), fact) {
						t.Fatalf("routed diagnostic=%q, missing %q", stderr.String(), fact)
					}
				}
			}
			journal, err := store.ReadJournal("run-1")
			if err != nil {
				t.Fatal(err)
			}
			if got := journal.Events[len(journal.Events)-1].Type; got != test.wantEvent {
				t.Fatalf("journal tail=%s, want %s; stderr=%q", got, test.wantEvent, stderr.String())
			}
			if test.check != nil {
				test.check(t, store)
			}
		})
	}
}

func TestApproveCommandCommitsRoutedAmendmentWithoutDriverAuthority(t *testing.T) {
	root, store := amendCommandFixture(t, true)
	patchPath := filepath.Join(root, "patch.json")
	if err := os.WriteFile(patchPath, []byte(`[{"op":"replace","path":"/goal","value":"needs-human-approval"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runAmend("", patchPath, "operator correction", "", &stdout, &stderr); code != 0 {
		t.Fatalf("route exit=%d stderr=%q", code, stderr.String())
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	var decisionID string
	for _, event := range journal.Events {
		if event.Type != runstate.EventDecisionRequested {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		decisionID, _ = payload["decision_id"].(string)
	}
	if decisionID == "" {
		t.Fatal("routed amendment did not request a decision")
	}
	stdout.Reset()
	stderr.Reset()
	if code := runApprove(decisionID, true, nil, "", &stderr); code != 0 {
		t.Fatalf("approve exit=%d stderr=%q", code, stderr.String())
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("approve stdout=%q stderr=%q, want empty", stdout.String(), stderr.String())
	}
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.ScoreHead.Revision != 2 || input.Projection.State.PendingPrepare != nil {
		t.Fatalf("approved command state = %+v", input.Projection.State)
	}
	if _, present, err := store.ReadLease("run-1"); err != nil || present {
		t.Fatalf("approve lease present=%t error=%v, want no driver authority", present, err)
	}
	journal, err = store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	approved := journal.Events[len(journal.Events)-1]
	if approved.Type != runstate.EventAmendmentApproved {
		t.Fatalf("terminal event=%s, want amendment.approved", approved.Type)
	}
	var payload map[string]any
	if err := json.Unmarshal(approved.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["mode"] != "human" || payload["decision_id"] != decisionID || payload["envelope_evaluation"] == nil {
		t.Fatalf("approved payload=%#v", payload)
	}
	if _, present := payload["envelope_class"]; present {
		t.Fatalf("human approval carries envelope_class: %#v", payload)
	}
}

func TestAmendCommandRejectsHeadChangedAfterBaseCapture(t *testing.T) {
	root, store := amendCommandFixture(t, false)
	patchPath := filepath.Join(root, "patch.json")
	if err := os.WriteFile(patchPath, []byte(`[{"op":"replace","path":"/goal","value":"stale"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := afterAmendmentBaseCapture
	afterAmendmentBaseCapture = func() {
		appendResumeApprovedSnapshot(t, store)
	}
	t.Cleanup(func() { afterAmendmentBaseCapture = previous })
	var stdout, stderr bytes.Buffer
	if code := runAmend("", patchPath, "stale test", "", &stdout, &stderr); code != 3 {
		t.Fatalf("amend exit=%d stderr=%q, want rejected amendment exit 3", code, stderr.String())
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	last := journal.Events[len(journal.Events)-1]
	if last.Type != runstate.EventAmendmentRejected || !strings.Contains(string(last.Payload), `"reason":"stale"`) {
		t.Fatalf("head-moved result = %s %s, want stale rejection", last.Type, last.Payload)
	}
}

func amendCommandFixture(t *testing.T, withAttempt bool) (string, *runstore.Store) {
	t.Helper()
	root, store := resumeFixtureWithInputs(t, "", amendCommandScore(), []byte("cast: \"0.1\"\nperformers:\n  reviewer:\n    adapter: adapter\n    model: model\nbindings:\n  reviewer:\n    performer: reviewer\n"))
	if withAttempt {
		appendResumeAttempt(t, store, false)
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	return root, store
}

func amendCommandScore() []byte {
	return []byte("score: \"0.2\"\nname: amend-command\nrevision: 1\nstatus: finalized\ngoal: fixture\nverification:\n  expectation:\n    intent: pass-existing-tests\n    apply_gate:\n      waived: true\n      reason: fixture\nparts:\n  reviewer:\n    capabilities: [repo_read]\nmovements:\n  - id: review\n    part: reviewer\n    grants: [repo_read]\n    instruction: inspect\npolicy:\n  allowed_paths: [\"**\"]\n  budget:\n    active_wall_clock_min: 10\n  amendment:\n    auto: envelope\n")
}

func hasCommandRevisionRestartSelection(events []runstate.Event, revision uint64) bool {
	for _, event := range events {
		if event.Type == runstate.EventPerformerSelected && event.ScoreRevision == revision && strings.Contains(string(event.Payload), `"reason":"revision_restart"`) {
			return true
		}
	}
	return false
}

func TestParseAmendArgs(t *testing.T) {
	for _, test := range []struct {
		args                                      []string
		wantRun, wantPatch, wantReason, wantClaim string
		want                                      bool
	}{
		{args: []string{"amend", "--patch", "patch.json", "--reason", "narrow"}, wantPatch: "patch.json", wantReason: "narrow", want: true},
		{args: []string{"amend", "run-1", "--patch", "-", "--reason", "narrow", "--claimed-impact", "impact.json"}, wantRun: "run-1", wantPatch: "-", wantReason: "narrow", wantClaim: "impact.json", want: true},
		{args: []string{"amend", "--reason", "narrow", "--patch", "patch.json"}},
		{args: []string{"amend", "run-1", "--patch", "patch.json", "--reason"}},
		{args: []string{"amend", "run-1", "--patch", "patch.json", "--reason", "narrow", "--claimed-impact"}},
	} {
		runID, patch, reason, claim, ok := parseAmendArgs(test.args)
		if ok != test.want || runID != test.wantRun || patch != test.wantPatch || reason != test.wantReason || claim != test.wantClaim {
			t.Fatalf("parseAmendArgs(%v) = (%q, %q, %q, %q, %t)", test.args, runID, patch, reason, claim, ok)
		}
	}
}

func TestParseApplyArgs(t *testing.T) {
	for _, test := range []struct {
		args              []string
		wantRun           string
		wantRecover, want bool
	}{
		{args: []string{"apply", "run-1"}, wantRun: "run-1", want: true},
		{args: []string{"apply", "run-1", "--recover"}, wantRun: "run-1", wantRecover: true, want: true},
		{args: nil},
		{args: []string{"apply"}},
		{args: []string{"apply", "run-1", "--recover", "extra"}},
		{args: []string{"resume", "run-1"}},
		{args: []string{"apply", ""}},
		{args: []string{"apply", "--run"}},
		{args: []string{"apply", "run-1", "--resume"}},
	} {
		runID, recoverOnly, ok := parseApplyArgs(test.args)
		if runID != test.wantRun || recoverOnly != test.wantRecover || ok != test.want {
			t.Fatalf("parseApplyArgs(%v) = (%q, %t, %t), want (%q, %t, %t)", test.args, runID, recoverOnly, ok, test.wantRun, test.wantRecover, test.want)
		}
	}
}

func TestParseAnswerArgs(t *testing.T) {
	for _, test := range []struct {
		args             []string
		wantID, wantText string
		want             bool
	}{
		{args: []string{"answer", "question-1", "--answer", "yes"}, wantID: "question-1", wantText: "yes", want: true},
		{args: []string{"answer", "question-1", "--answer", ""}, wantID: "question-1", want: true},
		{args: []string{"answer", "question-1", "yes"}},
		{args: []string{"answer", "--answer", "yes"}},
		{args: []string{"answer", "question-1", "--answer"}},
	} {
		id, text, ok := parseAnswerArgs(test.args)
		if id != test.wantID || text != test.wantText || ok != test.want {
			t.Fatalf("parseAnswerArgs(%v) = (%q, %q, %t), want (%q, %q, %t)", test.args, id, text, ok, test.wantID, test.wantText, test.want)
		}
	}
}

func TestParseApproveArgs(t *testing.T) {
	for _, test := range []struct {
		args                    []string
		wantID, wantReason      string
		wantOverrides           []runstate.FindingReference
		wantApproved, wantValid bool
	}{
		{args: []string{"approve", "gate-1", "--approve"}, wantID: "gate-1", wantApproved: true, wantValid: true},
		{args: []string{"approve", "gate-1", "--approve", "--override", "findings@attempt-1:F-1", "--reason", "human judgment"}, wantID: "gate-1", wantApproved: true, wantOverrides: []runstate.FindingReference{{ArtifactInstanceID: "findings@attempt-1", FindingID: "F-1"}}, wantReason: "human judgment", wantValid: true},
		{args: []string{"approve", "gate-1", "--approve", "--override", "findings@attempt-1:F-1", "--override", "findings@attempt-2:F-2", "--reason", "human judgment"}, wantID: "gate-1", wantApproved: true, wantOverrides: []runstate.FindingReference{{ArtifactInstanceID: "findings@attempt-1", FindingID: "F-1"}, {ArtifactInstanceID: "findings@attempt-2", FindingID: "F-2"}}, wantReason: "human judgment", wantValid: true},
		{args: []string{"approve", "gate-1", "--approve", "--override", "findings@attempt-1:perf:n+1", "--reason", "human judgment"}, wantID: "gate-1", wantApproved: true, wantOverrides: []runstate.FindingReference{{ArtifactInstanceID: "findings@attempt-1", FindingID: "perf:n+1"}}, wantReason: "human judgment", wantValid: true},
		{args: []string{"approve", "gate-1", "--reject"}, wantID: "gate-1", wantValid: true},
		{args: []string{"approve", "gate-1", "--reject", "--reason", "not ready"}, wantID: "gate-1", wantReason: "not ready", wantValid: true},
		{args: []string{"approve", "gate-1", "--approve", "--reason", "no"}},
		{args: []string{"approve", "gate-1", "--approve", "--override", "findings@attempt-1:F-1"}},
		{args: []string{"approve", "gate-1", "--reject", "--override", "findings@attempt-1:F-1", "--reason", "no"}},
		{args: []string{"approve", "gate-1", "--approve", "--override", "findings@attempt-1:F-1", "--override", "findings@attempt-1:F-1", "--reason", "human judgment"}},
		{args: []string{"approve", "gate-1", "--approve", "--override", "missing-separator", "--reason", "human judgment"}},
		{args: []string{"approve", "gate-1", "--approve", "--override", ":F-1", "--reason", "human judgment"}},
		{args: []string{"approve", "gate-1", "--approve", "--override", "findings@attempt-1:", "--reason", "human judgment"}},
		{args: []string{"approve", "gate-1", "--reject", "--reason", ""}},
		{args: []string{"approve", "gate-1"}},
	} {
		id, approved, overrides, reason, ok := parseApproveArgs(test.args)
		if id != test.wantID || approved != test.wantApproved || !reflect.DeepEqual(overrides, test.wantOverrides) || reason != test.wantReason || ok != test.wantValid {
			t.Fatalf("parseApproveArgs(%v) = (%q, %t, %#v, %q, %t)", test.args, id, approved, overrides, reason, ok)
		}
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
			name:    "waiting human is a successful quiescent command outcome",
			outcome: driver.OutcomeWaitingHuman,
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
			if code != test.wantCode {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if stderr.String() != test.wantStderr {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

// This is deliberately an injected executor-result test: the CLI exit mapping
// is the seam under test, while durable effects are covered below with a real store.
func TestCancelMapsInjectedExecutorOutcomesAndNeverWritesStdout(t *testing.T) {
	tests := []struct {
		name       string
		result     recoveryexec.Result
		wantCode   int
		wantStderr string
	}{
		{name: "unexpected live-owner result is operational interruption", result: recoveryexec.Result{Outcome: recoveryexec.OutcomeRefused}, wantCode: 6, wantStderr: "run interrupted: run_id=\"run-1\" state=\"nonterminal\" resume=\"partitur resume run-1\" detail=\"cancellation acknowledgement wait ended without a terminal outcome\"\n"},
		{name: "terminal cancellation", result: recoveryexec.Result{Outcome: recoveryexec.OutcomeCancelled}, wantCode: 4},
		{name: "terminal failure", result: recoveryexec.Result{Outcome: recoveryexec.OutcomeFailed}, wantCode: 4},
		{name: "halt", result: recoveryexec.Result{Outcome: recoveryexec.OutcomeHalted, Decision: recovery.Decision{Halt: recovery.HaltOwnerUnverifiable}}, wantCode: 5, wantStderr: "recovery halted: run_id=\"run-1\" reason=\"owner_unverifiable\"\n"},
		{name: "no outcome is operational interruption", result: recoveryexec.Result{}, wantCode: 6, wantStderr: "run interrupted: run_id=\"run-1\" state=\"nonterminal\" resume=\"partitur resume run-1\" detail=\"recovery produced no command outcome\"\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCancel("run-1", &stdout, &stderr, func(context.Context, string) (recoveryexec.Result, error) {
				return test.result, nil
			})
			if code != test.wantCode {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if stderr.String() != test.wantStderr {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestCancelArgumentParsing(t *testing.T) {
	for _, test := range []struct {
		args   []string
		wantID string
		wantOK bool
	}{
		{args: []string{"cancel"}, wantOK: true},
		{args: []string{"cancel", "run-1"}, wantID: "run-1", wantOK: true},
		{args: []string{"cancel", ""}},
		{args: []string{"cancel", "--bad"}},
		{args: []string{"cancel", "run-1", "extra"}},
		{args: []string{"resume", "run-1"}},
	} {
		gotID, gotOK := parseCancelArgs(test.args)
		if gotID != test.wantID {
			t.Fatalf("args=%v id=%q ok=%t, want id=%q ok=%t", test.args, gotID, gotOK, test.wantID, test.wantOK)
		}
		if gotOK != test.wantOK {
			t.Fatalf("args=%v id=%q ok=%t, want id=%q ok=%t", test.args, gotID, gotOK, test.wantID, test.wantOK)
		}
	}
}

func TestCancelSelectsActiveRunAndAppendsThenTerminalizes(t *testing.T) {
	root, store := resumeFixture(t, "")
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	code := run([]string{"cancel"}, &stdout, &stderr)
	if code != 4 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	assertCancellationJournal(t, store, []runstate.EventType{
		runstate.EventRunStarted,
		runstate.EventCancelRequested,
		runstate.EventRunCancelled,
	}, false)
}

// This runs the full request-and-acknowledgement path without calling the
// optional SIGUSR1 wake. The Wait hook is the deterministic entry to the
// driver's already-durable cancellation oracle, not marker-file polling.
func TestCancelWaitsForAcknowledgementWithWakeDisabled(t *testing.T) {
	_, store := resumeFixture(t, "")
	driver, err := store.AcquireRecoveryDriver("run-1")
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Release()
	if err := store.RequestCancellation("run-1"); err != nil {
		t.Fatal(err)
	}
	waiter := newCancellationWaiter(store, "run-1")
	waiter.Deadline = time.Second
	enteredWait := false
	waiter.Wait = func(context.Context, time.Duration) error {
		if enteredWait {
			t.Fatal("waited after the responsive driver acknowledged")
		}
		enteredWait = true
		return cancellation.Execute(context.Background(), store, "run-1")
	}
	result, err := waiter.Run(context.Background())
	if err != nil || result.Outcome != recoveryexec.OutcomeCancelled || !enteredWait {
		t.Fatalf("result=%+v err=%v entered_wait=%t", result, err, enteredWait)
	}
	assertCancellationJournal(t, store, []runstate.EventType{
		runstate.EventRunStarted,
		runstate.EventAuthorityGranted,
		runstate.EventCancelRequested,
		runstate.EventRunCancelled,
	}, true)
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	terminal := journal.Events[len(journal.Events)-1]
	var payload map[string]any
	if err := json.Unmarshal(terminal.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["fenced_epoch"]; !ok {
		t.Fatalf("run.cancelled payload=%v, want fenced_epoch", payload)
	}
	if _, present, err := store.ReadLease("run-1"); err != nil || present {
		t.Fatalf("lease present=%t err=%v, want removed", present, err)
	}
}

func TestCancelAppendsRequestBeforeOwnerUnverifiableHalt(t *testing.T) {
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
	var stdout, stderr bytes.Buffer
	code := run([]string{"cancel", "run-1"}, &stdout, &stderr)
	if code != 5 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if stderr.String() != "recovery halted: run_id=\"run-1\" reason=\"owner_unverifiable\"\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	assertCancellationJournal(t, store, []runstate.EventType{
		runstate.EventRunStarted,
		runstate.EventAuthorityGranted,
		runstate.EventCancelRequested,
	}, false)
}

func TestCancelRefusesNoActiveRunAndMapsTerminalRun(t *testing.T) {
	t.Run("no active run", func(t *testing.T) {
		root := t.TempDir()
		t.Chdir(root)
		var stdout, stderr bytes.Buffer
		code := run([]string{"cancel"}, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
		if !strings.Contains(stderr.String(), "no active run") {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	})
	for _, terminal := range []struct {
		state string
		code  int
	}{{state: "SUCCEEDED", code: 0}, {state: "FAILED", code: 4}, {state: "CANCELLED", code: 4}} {
		t.Run("terminal run "+terminal.state, func(t *testing.T) {
			root, store := resumeFixture(t, terminal.state)
			t.Chdir(root)
			var stdout, stderr bytes.Buffer
			code := run([]string{"cancel", "run-1"}, &stdout, &stderr)
			if code != terminal.code || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			journal, err := store.ReadJournal("run-1")
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range journal.Events {
				if event.Type == runstate.EventCancelRequested {
					t.Fatalf("terminal cancel appended cancel.requested: %v", event)
				}
			}
		})
	}
}

func assertCancellationJournal(t *testing.T, store *runstore.Store, want []runstate.EventType, wantFencedEpoch bool) {
	t.Helper()
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Events) != len(want) {
		t.Fatalf("event count=%d want=%d events=%+v", len(journal.Events), len(want), journal.Events)
	}
	for index, event := range journal.Events {
		if event.Type != want[index] {
			t.Fatalf("event[%d]=%q want=%q", index, event.Type, want[index])
		}
	}
	for _, event := range journal.Events {
		if event.Type != runstate.EventCancelRequested {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload) != 1 {
			t.Fatalf("cancel.requested payload=%v", payload)
		}
		if payload["requested_by"] != "cli" {
			t.Fatalf("cancel.requested payload=%v", payload)
		}
		continue
	}
	for _, event := range journal.Events {
		if event.Type != runstate.EventRunCancelled {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload) != 3+boolToInt(wantFencedEpoch) {
			t.Fatalf("run.cancelled payload=%v", payload)
		}
		for _, key := range []string{"cancelled_movement_ids", "cancelled_attempt_ids", "obsoleted_decision_ids"} {
			values, ok := payload[key].([]any)
			if !ok {
				t.Fatalf("run.cancelled payload=%v missing array %q", payload, key)
			}
			if len(values) != 0 {
				t.Fatalf("run.cancelled payload=%v %q must be empty", payload, key)
			}
		}
		fenced, present := payload["fenced_epoch"]
		if present != wantFencedEpoch {
			t.Fatalf("run.cancelled payload=%v fenced_epoch present=%t want=%t", payload, present, wantFencedEpoch)
		}
		if present {
			if _, ok := fenced.(float64); !ok {
				t.Fatalf("run.cancelled payload=%v fenced_epoch=%T", payload, fenced)
			}
		}
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestResumeMapsEveryAppendixDHaltAndNoOtherReasonToExitFive(t *testing.T) {
	for _, reason := range recovery.AppendixDHaltReasons() {
		t.Run(string(reason), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runResume("run-1", &stdout, &stderr, func(context.Context, string) (recoveryexec.Result, error) {
				return recoveryexec.Result{Outcome: recoveryexec.OutcomeHalted, Decision: recovery.Decision{Halt: reason}}, nil
			})
			if code != 5 || stdout.Len() != 0 || !strings.Contains(stderr.String(), fmt.Sprintf("reason=%q", reason)) {
				t.Fatalf("reason=%q exit=%d stdout=%q stderr=%q", reason, code, stdout.String(), stderr.String())
			}
		})
	}

	var stdout, stderr bytes.Buffer
	code := runResume("run-1", &stdout, &stderr, func(context.Context, string) (recoveryexec.Result, error) {
		return recoveryexec.Result{Outcome: recoveryexec.OutcomeHalted, Decision: recovery.Decision{Halt: "not_an_appendix_d_reason"}}, nil
	})
	if code != 6 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "unknown halt reason") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestResumeSelectionSnapshotInvalidIsNotAnAppendixDHalt(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runResume("run-1", &stdout, &stderr, func(context.Context, string) (recoveryexec.Result, error) {
		return recoveryexec.Result{}, resumeSelectionError{err: statusprojection.ErrSnapshotInvalid}
	})
	if code != 6 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "run score snapshot is invalid") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
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

func TestResumeDispatchesNonterminalWriterScore(t *testing.T) {
	root, store := resumeFixtureWithInputs(t, "", resumeWriterScore(), resumeWriterCast())
	before, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"resume", "run-1"}, &stdout, &stderr); code != 6 || stdout.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	after, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	got := after.Events[len(before.Events):]
	gotTypes := make([]runstate.EventType, len(got))
	for index, event := range got {
		gotTypes[index] = event.Type
	}
	want := []runstate.EventType{
		runstate.EventAuthorityGranted,
		runstate.EventMovementReady,
		runstate.EventMovementStarted,
		runstate.EventPerformerSelected,
	}
	if len(got) != len(want) {
		t.Fatalf("writer resume events=%v, want %v; stderr=%q", gotTypes, want, stderr.String())
	}
	for index, eventType := range want {
		if got[index].Type != eventType {
			t.Fatalf("writer resume events=%v, want %v; stderr=%q", gotTypes, want, stderr.String())
		}
	}
	input, err := store.LoadRunInput("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Run != runstate.RunRunning ||
		input.Projection.State.Movements["write"] != runstate.MovementRunning {
		t.Fatalf("writer resume state: run=%s movement=%s", input.Projection.State.Run, input.Projection.State.Movements["write"])
	}
	if len(input.Projection.State.Attempts) != 1 {
		t.Fatalf("writer resume attempts=%v, want one selected attempt", input.Projection.State.Attempts)
	}
	for _, attempt := range input.Projection.State.Attempts {
		if attempt.MovementID != "write" || attempt.State != runstate.AttemptStarting {
			t.Fatalf("writer resume attempt=%+v, want write STARTING", attempt)
		}
	}
}

func TestResumeKeepsTerminalWriterScoreIdempotent(t *testing.T) {
	root, _ := resumeFixtureWithInputs(t, "FAILED", resumeWriterScore(), resumeWriterCast())
	journal := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"resume", "run-1"}, &stdout, &stderr); code != 4 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	after, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("terminal writer resume appended to journal")
	}
}

func TestCancelTerminalizesNonterminalWriterScore(t *testing.T) {
	root, _ := resumeFixtureWithInputs(t, "", resumeWriterScore(), resumeWriterCast())
	journal := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"cancel", "run-1"}, &stdout, &stderr); code != 4 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	after, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, after) {
		t.Fatal("writer cancellation did not append its terminal path")
	}
	if !bytes.Contains(after, []byte(`"type":"run.cancelled"`)) {
		t.Fatalf("writer cancellation journal lacks run.cancelled: %s", after)
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

func TestResumeCompletesWaivedReadOnlyTailExactlyOnce(t *testing.T) {
	root, store := resumeFixtureWithInputs(t, "", resumeAttemptScore(), []byte("cast: \"0.1\"\nperformers:\n  reviewer:\n    adapter: adapter\n    model: model\nbindings:\n  reviewer:\n    performer: reviewer\n"))
	appendWaivedReadOnlyMovementSucceeded(t, store)
	t.Chdir(root)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"resume", "run-1"}, &stdout, &stderr); code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("first resume: exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	journal, err := store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if countEvents(journal.Events, runstate.EventRunSucceeded) != 1 {
		t.Fatalf("run.succeeded count=%d, want exactly one", countEvents(journal.Events, runstate.EventRunSucceeded))
	}
	terminal := journal.Events[len(journal.Events)-1]
	if terminal.Type != runstate.EventRunSucceeded {
		t.Fatalf("terminal event=%s, want run.succeeded", terminal.Type)
	}
	var payload map[string]any
	if err := json.Unmarshal(terminal.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if candidate, ok := payload["candidate"].(map[string]any); !ok || candidate["candidate_id"] == "" {
		t.Fatalf("run.succeeded lacks candidate: %v", payload)
	}
	before := len(journal.Events)
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"resume", "run-1"}, &stdout, &stderr); code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("second resume: exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	journal, err = store.ReadJournal("run-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Events) != before || countEvents(journal.Events, runstate.EventRunSucceeded) != 1 {
		t.Fatalf("second resume appended events=%d run.succeeded=%d", len(journal.Events)-before, countEvents(journal.Events, runstate.EventRunSucceeded))
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
			wantStderr: "recovery halted: run_id=\"run-1\" reason=\"journal_corrupt\"\n",
		},
		{
			name: "missing resolved cast",
			fixture: func(t *testing.T) string {
				root, _ := resumeFixture(t, "")
				if err := os.Remove(filepath.Join(root, ".partitur", "runs", "run-1", "resolved-cast.yaml")); err != nil {
					t.Fatal(err)
				}
				return root
			},
			wantStderr: "recovery halted: run_id=\"run-1\" reason=\"missing_resolved_cast\"\n",
		},
		{
			name: "resolved cast hash mismatch",
			fixture: func(t *testing.T) string {
				root, _ := resumeFixture(t, "")
				if err := os.WriteFile(filepath.Join(root, ".partitur", "runs", "run-1", "resolved-cast.yaml"), []byte("cast: \"0.1\"\nperformers:\n  reviewer:\n    adapter: adapter\n    model: changed-model\nbindings:\n  reviewer:\n    performer: reviewer\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return root
			},
			wantStderr: "recovery halted: run_id=\"run-1\" reason=\"missing_resolved_cast\"\n",
		},
		{
			name: "missing initial pinned snapshot",
			fixture: func(t *testing.T) string {
				root, _ := resumeFixture(t, "")
				if err := os.Remove(filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-1.yaml")); err != nil {
					t.Fatal(err)
				}
				return root
			},
			wantStderr: "recovery halted: run_id=\"run-1\" reason=\"missing_snapshot_file\"\n",
		},
		{
			name: "initial pinned snapshot hash mismatch",
			fixture: func(t *testing.T) string {
				root, _ := resumeFixture(t, "")
				if err := os.WriteFile(filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-1.yaml"), resumeScore(1, "different bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
				return root
			},
			wantStderr: "recovery halted: run_id=\"run-1\" reason=\"missing_snapshot_file\"\n",
		},
		{
			name: "missing later pinned snapshot",
			fixture: func(t *testing.T) string {
				root, store := resumeFixture(t, "")
				appendResumeApprovedSnapshot(t, store)
				if err := os.Remove(filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-2.yaml")); err != nil {
					t.Fatal(err)
				}
				return root
			},
			wantStderr: "recovery halted: run_id=\"run-1\" reason=\"missing_snapshot_file\"\n",
		},
		{
			name: "later pinned snapshot hash mismatch",
			fixture: func(t *testing.T) string {
				root, store := resumeFixture(t, "")
				appendResumeApprovedSnapshot(t, store)
				if err := os.WriteFile(filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-2.yaml"), resumeScore(2, "different bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
				return root
			},
			wantStderr: "recovery halted: run_id=\"run-1\" reason=\"missing_snapshot_file\"\n",
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
		{
			name: "malformed current driver lease leaves owner unverifiable",
			fixture: func(t *testing.T) string {
				root, store := resumeFixture(t, "")
				if _, err := store.AcquireRecoveryDriver("run-1"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, ".partitur", "runs", "run-1", "driver.lease"), []byte("not a lease"), 0o600); err != nil {
					t.Fatal(err)
				}
				return root
			},
			wantStderr: "recovery halted: run_id=\"run-1\" reason=\"owner_unverifiable\"\n",
		},
		{
			name: "deleted recorded artifact",
			fixture: func(t *testing.T) string {
				root, store := resumeAttemptFixture(t)
				appendResumeAttempt(t, store, true)
				artifact := appendResumeRecordedArtifact(t, store)
				if err := os.Remove(artifact); err != nil {
					t.Fatal(err)
				}
				return root
			},
			wantStderr: "recovery halted: run_id=\"run-1\" reason=\"missing_artifact_file\"\n",
		},
		{
			name: "hash-mismatched recorded artifact",
			fixture: func(t *testing.T) string {
				root, store := resumeAttemptFixture(t)
				appendResumeAttempt(t, store, true)
				artifact := appendResumeRecordedArtifact(t, store)
				if err := os.WriteFile(artifact, []byte("changed artifact"), 0o600); err != nil {
					t.Fatal(err)
				}
				return root
			},
			wantStderr: "recovery halted: run_id=\"run-1\" reason=\"missing_artifact_file\"\n",
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

func TestResumeMapsAuthorityAcquisitionInputHaltsToExitFive(t *testing.T) {
	for _, test := range []struct {
		name       string
		invalidate func(*testing.T, string)
		wantStderr string
	}{
		{
			name: "corrupt journal after initial load",
			invalidate: func(t *testing.T, root string) {
				t.Helper()
				journal := filepath.Join(root, ".partitur", "runs", "run-1", "journal.jsonl")
				contents, err := os.ReadFile(journal)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(journal, append(contents, []byte("\n{}\n{}\n")...), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantStderr: "recovery halted: run_id=\"run-1\" reason=\"journal_corrupt\"\n",
		},
		{
			name: "remove pinned snapshot after initial load",
			invalidate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Remove(filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-1.yaml")); err != nil {
					t.Fatal(err)
				}
			},
			wantStderr: "recovery halted: run_id=\"run-1\" reason=\"missing_snapshot_file\"\n",
		},
		{
			name: "remove resolved cast after initial load",
			invalidate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Remove(filepath.Join(root, ".partitur", "runs", "run-1", "resolved-cast.yaml")); err != nil {
					t.Fatal(err)
				}
			},
			wantStderr: "recovery halted: run_id=\"run-1\" reason=\"missing_resolved_cast\"\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, store := resumeFixture(t, "")
			loaded := false
			executor := &recoveryexec.Executor{Store: store, RunID: "run-1"}
			executor.Load = func(context.Context) (recovery.Input, error) {
				durable, err := store.LoadRunInput("run-1")
				if err != nil {
					return recovery.Input{}, err
				}
				if !loaded {
					loaded = true
					test.invalidate(t, root)
				}
				return recovery.Input{Projection: durable.Projection}, nil
			}

			var stdout, stderr bytes.Buffer
			code := runResume("run-1", &stdout, &stderr, func(ctx context.Context, _ string) (recoveryexec.Result, error) {
				return executor.Execute(ctx)
			})
			if code != 5 || stdout.Len() != 0 || stderr.String() != test.wantStderr {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestResumeTerminalCleanupRemovesEveryC1Residue(t *testing.T) {
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
	sidecar := filepath.Join(root, ".partitur", "runs", "run-1", "driver.quiesced.prepare-1")
	if err := os.WriteFile(sidecar, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(root, ".partitur", "runs", "run-1", "prepares", "prepare-1.json")
	if err := os.MkdirAll(filepath.Dir(plan), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphanReviewInput := filepath.Join(root, ".partitur", "runs", "run-1", "inputs", "review", "revision-1", "subject-tree.json")
	if err := os.MkdirAll(filepath.Dir(orphanReviewInput), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphanReviewInput, []byte("orphan"), 0o400); err != nil {
		t.Fatal(err)
	}

	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"resume", "run-1"}, &stdout, &stderr); code != 4 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, residue := range []string{
		filepath.Join(root, ".partitur", "runs", "run-1", "driver.lease"),
		sidecar,
		plan,
		orphanReviewInput,
		stagingRoot,
	} {
		if _, err := os.Stat(residue); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("residue %q stat error=%v, want not exist", residue, err)
		}
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
	baseCommit, baseTree := resumeFixtureRepository(t, root)
	return resumeFixtureWithInputsAtRepository(t, root, baseCommit, baseTree, terminal, snapshot, resolvedCast)
}

func resumeFixtureWithInputsAtRepository(t *testing.T, root, baseCommit, baseTree, terminal string, snapshot, resolvedCast []byte) (string, *runstore.Store) {
	t.Helper()
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
		if _, err := tx.At("fixture.start").Append(resumeEvent("run-1", runstate.EventRunStarted, map[string]any{"base_commit": baseCommit, "base_tree": baseTree, "score_hash": scoreHash, "score_file_hash": resumeHash(snapshot), "resolved_cast_hash": castHash, "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}})); err != nil {
			return err
		}
		if terminal != "" {
			return appendFixtureTerminal(t, tx, root, baseCommit, baseTree, terminal)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return root, store
}

const (
	resumeFixtureBaseFile      = "base.txt"
	resumeFixtureUntouchedFile = "untouched.txt"
	resumeFixtureBaseContents  = "base\n"
)

func resumeFixtureRepository(t *testing.T, root string) (string, string) {
	t.Helper()
	git := func(arguments ...string) string {
		t.Helper()
		command := exec.Command("git", arguments...)
		command.Dir = root
		output, err := command.Output()
		if err != nil {
			t.Fatalf("git %v: %v", arguments, err)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "--quiet")
	git("config", "user.name", "Partitur Test")
	git("config", "user.email", "partitur@example.invalid")
	// One tracked file in the base, so a candidate can *modify* a path rather
	// than only add one — the case a path-wise rollback has to restore.
	if err := os.WriteFile(filepath.Join(root, resumeFixtureBaseFile), []byte(resumeFixtureBaseContents), 0o600); err != nil {
		t.Fatal(err)
	}
	// A second tracked file no candidate touches, so a test can move the tree
	// away from the base by a path the rollback has no mandate to restore.
	if err := os.WriteFile(filepath.Join(root, resumeFixtureUntouchedFile), []byte(resumeFixtureBaseContents), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", resumeFixtureBaseFile, resumeFixtureUntouchedFile)
	git("commit", "--quiet", "-m", "fixture")
	format := git("rev-parse", "--show-object-format")
	return "git-" + format + ":" + git("rev-parse", "HEAD"), "git-" + format + ":" + git("rev-parse", "HEAD^{tree}")
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
			{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventAdapterProbed, Payload: resumePayload(t, map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "delivered_resolutions": []any{}, "delivered_feedback": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": resumeIdentityVersions()})},
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

func appendWaivedReadOnlyMovementSucceeded(t *testing.T, store *runstore.Store) {
	t.Helper()
	versions := resumeIdentityVersions()
	events := []runstate.Event{
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", Type: runstate.EventMovementReady, Payload: resumePayload(t, map[string]any{})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", Type: runstate.EventMovementStarted, Payload: resumePayload(t, map[string]any{})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventPerformerSelected, Payload: resumePayload(t, map[string]any{"reason": "initial", "performer_id": "reviewer", "adapter_id": "adapter", "model": "model"})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventAttemptStarted, Payload: resumePayload(t, map[string]any{"attempt_number": 1, "adapter_process": map[string]any{"pid": 999999, "session_id": 999999, "start_identity": map[string]any{"platform": "linux", "boot_id": "fixture", "start_ticks": "0"}}, "granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{"**"}, "shell": false, "network": false}, "identity_versions": versions})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventAdapterProbed, Payload: resumePayload(t, map[string]any{"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false}, "enforcement": map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true}, "negotiated_features": []any{}, "truncated_resolutions": []any{}, "delivered_resolutions": []any{}, "delivered_feedback": []any{}, "advisory_dimensions": []any{}, "execution_dependency_hash": "sha256:dependency", "identity_versions": versions})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventPerformerCompleted, Payload: resumePayload(t, map[string]any{"session_hint_stored": false})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventVerificationPassed, Payload: resumePayload(t, map[string]any{})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventAcceptanceStarted, Payload: resumePayload(t, map[string]any{"subject_tree": "git-sha1:subject", "acceptance_spec_hash": "sha256:acceptance", "planned_criterion_ids": []any{}, "identity_versions": versions})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventAcceptanceEvaluationCompleted, Payload: resumePayload(t, map[string]any{"subject_tree": "git-sha1:subject", "acceptance_spec_hash": "sha256:acceptance", "criterion_outcomes": []any{}, "identity_versions": versions})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventAttemptCompleted, Payload: resumePayload(t, map[string]any{})},
		{RunID: "run-1", ScoreRevision: 1, MovementID: "review", AttemptID: "attempt-1", Type: runstate.EventMovementSucceeded, Payload: resumePayload(t, map[string]any{"approved_artifact_instance_ids": []any{}, "identity_versions": versions, "run_succeeded": false})},
	}
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		for _, event := range events {
			if _, err := tx.At("fixture.waived.succeeded").Append(event); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func countEvents(events []runstate.Event, want runstate.EventType) int {
	count := 0
	for _, event := range events {
		if event.Type == want {
			count++
		}
	}
	return count
}

func appendResumeRecordedArtifact(t *testing.T, store *runstore.Store) string {
	t.Helper()
	contents := []byte("durably recorded artifact\n")
	path := filepath.Join(store.RepositoryRoot(), ".partitur", "runs", "run-1", "artifacts", "report", "attempt-1")
	event := runstate.Event{
		RunID: "run-1", ScoreRevision: 1, MovementID: "review", PartID: "reviewer", AttemptID: "attempt-1", Type: runstate.EventArtifactRecorded,
		Payload: resumePayload(t, map[string]any{
			"logical_output_id": "report", "kind": "artifact", "content_hash": resumeHash(contents), "size_bytes": len(contents), "source_path": "report.txt",
		}),
	}
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		if _, err := tx.At("fixture.artifact.published").PublishImmutable("artifacts/report/attempt-1", contents, runstore.Hash(resumeHash(contents))); err != nil {
			return err
		}
		_, err := tx.At("fixture.artifact.recorded").Append(event)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return path
}

func appendResumeApprovedSnapshot(t *testing.T, store *runstore.Store) {
	t.Helper()
	root := store.RepositoryRoot()
	initial, err := os.ReadFile(filepath.Join(root, ".partitur", "runs", "run-1", "scores", "revision-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	initialScore, diagnostics := score.Compile(initial)
	if len(diagnostics) != 0 {
		t.Fatalf("initial score diagnostics=%v", diagnostics)
	}
	initialHash, err := initialScore.Hash()
	if err != nil {
		t.Fatal(err)
	}
	updated := resumeApprovedScore(2, "approved snapshot")
	updatedScore, diagnostics := score.Compile(updated)
	if len(diagnostics) != 0 {
		t.Fatalf("updated score diagnostics=%v", diagnostics)
	}
	updatedHash, err := updatedScore.Hash()
	if err != nil {
		t.Fatal(err)
	}
	versions := resumeIdentityVersions()
	if err := store.Mutate("run-1", "", func(tx *runstore.Txn) error {
		if _, err := tx.At("fixture.approved_snapshot").PublishImmutable("scores/revision-2.yaml", updated, runstore.Hash(resumeHash(updated))); err != nil {
			return err
		}
		if _, err := tx.At("fixture.approval_prepared").Append(resumeEvent("run-1", runstate.EventAmendmentApprovalPrepared, map[string]any{
			"prepare_id": "prepare-1", "proposal_id": "proposal-1", "mode": "auto", "envelope_class": "NARROW_PATHS",
			"base_revision": 1, "base_hash": initialHash, "new_revision": 2, "new_snapshot_hash": updatedHash,
			"new_snapshot_file_hash": resumeHash(updated), "plan_record_hash": "sha256:plan", "target_attempt_ids": []any{},
			"observed_authority_epoch": 0, "quiesce_silence_limit_ms": 60_000, "classifier_version": 1, "identity_versions": versions,
		})); err != nil {
			return err
		}
		approval := resumeEvent("run-1", runstate.EventAmendmentApproved, map[string]any{
			"proposal_id": "proposal-1", "mode": "auto", "envelope_class": "NARROW_PATHS", "base_revision": 1, "base_hash": initialHash,
			"classifier_version": 1, "new_revision": 2, "new_snapshot_hash": updatedHash, "new_snapshot_file_hash": resumeHash(updated),
			"typed_delta": []any{}, "actual_impact": map[string]any{"score_changes": []any{}, "authority": map[string]any{"allowed_paths": map[string]any{"added": []any{}, "removed": []any{}}, "grants": []any{}, "side_effects": map[string]any{"added": []any{}, "removed": []any{}}}, "budget": map[string]any{}},
			"head_movements":         []any{map[string]any{"id": "inspect", "initial": "PENDING", "repo_write": false, "has_dependencies": false, "final": false}},
			"superseded_attempt_ids": []any{}, "obsoleted_decision_ids": []any{}, "finalization": false, "identity_versions": versions,
		})
		approval.ScoreRevision = 2
		_, err := tx.At("fixture.approved").Append(approval)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func appendFixtureTerminal(t *testing.T, tx *runstore.Txn, root, baseCommit, baseTree, terminal string) error {
	t.Helper()
	switch terminal {
	case "FAILED":
		_, err := tx.At("fixture.failed").Append(resumeEvent("run-1", runstate.EventRunFailed, map[string]any{"reason": "fixture"}))
		return err
	case "CANCELLED":
		_, err := tx.At("fixture.cancelled").Append(resumeEvent("run-1", runstate.EventRunCancelled, map[string]any{"cancelled_movement_ids": []any{}, "cancelled_attempt_ids": []any{}, "obsoleted_decision_ids": []any{}}))
		return err
	case "SUCCEEDED":
		candidate, err := fixtureSucceededCandidate(root, baseCommit, baseTree)
		if err != nil {
			return err
		}
		_, err = tx.At("fixture.succeeded").Append(resumeEvent("run-1", runstate.EventRunSucceeded, map[string]any{"candidate": candidate, "waiver": map[string]any{"reason": "fixture"}, "identity_versions": map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}}))
		return err
	default:
		return fmt.Errorf("unknown terminal %q", terminal)
	}
}

func fixtureSucceededCandidate(root, baseCommit, baseTree string) (map[string]any, error) {
	compositionHash, err := workspace.CandidateCompositionHash(baseTree, nil, "")
	if err != nil {
		return nil, err
	}
	candidateID, err := canonical.Hash(canonical.DomainCandidate, map[string]any{
		"base_tree": baseTree, "result_tree": baseTree, "ordered_change_sets": []any{},
	})
	if err != nil {
		return nil, err
	}
	_, commit, ok := strings.Cut(baseCommit, ":")
	if !ok || commit == "" {
		return nil, fmt.Errorf("fixture base commit %q is not qualified", baseCommit)
	}
	command := exec.Command("git", "update-ref", "refs/partitur/runs/run-1/candidate", commit)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pin fixture candidate ref: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return map[string]any{
		"candidate_id":                          candidateID,
		"base_tree":                             baseTree,
		"result_tree":                           baseTree,
		"ordered_change_sets":                   []any{},
		"contributors":                          []any{},
		"candidate_composition_dependency_hash": compositionHash,
	}, nil
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

func resumeApprovedScore(revision int, goal string) []byte {
	return []byte(fmt.Sprintf("score: \"0.2\"\nname: resume-fixture\nrevision: %d\nstatus: finalized\ngoal: %s\nverification:\n  expectation:\n    intent: pass-existing-tests\n    apply_gate:\n      waived: true\n      reason: fixture\nparts:\n  reader:\n    capabilities: [repo_read]\nmovements:\n  - id: inspect\n    part: reader\n    grants: [repo_read]\n    instruction: inspect\npolicy:\n  allowed_paths: [\"**\"]\n  budget:\n    active_wall_clock_min: 10\n", revision, goal))
}

func resumeAttemptScore() []byte {
	return []byte("score: \"0.2\"\nname: resume-attempt-fixture\nrevision: 1\nstatus: finalized\ngoal: fixture\nverification:\n  expectation:\n    intent: pass-existing-tests\n    apply_gate:\n      waived: true\n      reason: fixture\nparts:\n  reviewer:\n    capabilities: [repo_read]\nmovements:\n  - id: review\n    part: reviewer\n    grants: [repo_read]\n    instruction: inspect\npolicy:\n  allowed_paths: [\"**\"]\n  budget:\n    active_wall_clock_min: 10\n")
}

func resumeWriterScore() []byte {
	return []byte("score: \"0.2\"\nname: resume-writer-fixture\nrevision: 1\nstatus: finalized\ngoal: fixture\nverification:\n  expectation:\n    intent: pass-existing-tests\n    apply_gate:\n      waived: true\n      reason: fixture\nparts:\n  writer:\n    capabilities: [repo_read, repo_write]\nmovements:\n  - id: write\n    part: writer\n    grants: [repo_read, repo_write]\n    instruction: write\n    outputs:\n      - id: change-set\n        kind: change_set\n      - id: proof\n        kind: document\n    acceptance:\n      hard:\n        - id: proof-recorded\n          artifact: proof\npolicy:\n  allowed_paths: [\"**\"]\n  budget:\n    active_wall_clock_min: 10\n")
}

func resumeWriterCast() []byte {
	return []byte("cast: \"0.1\"\nperformers:\n  writer:\n    adapter: adapter\n    model: model\nbindings:\n  writer:\n    performer: writer\n")
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
