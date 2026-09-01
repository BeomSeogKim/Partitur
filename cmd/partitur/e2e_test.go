package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	statusprojection "github.com/BeomSeogKim/Partitur/internal/status"
)

const fakeAdapterEnvironment = "PARTITUR_VALIDATE_FAKE_ADAPTER"
const runVendorEnvironment = "PARTITUR_RUN_VENDOR_FIXTURE"
const runVendorOutcomeEnvironment = "PARTITUR_RUN_VENDOR_OUTCOME"
const runVendorMarkerEnvironment = "PARTITUR_RUN_VENDOR_MARKER"
const runVendorContestedEnvironment = "PARTITUR_RUN_VENDOR_CONTESTED"
const runVendorFindingIDEnvironment = "PARTITUR_RUN_VENDOR_FINDING_ID"
const runVendorProposalBaseHashEnvironment = "PARTITUR_RUN_VENDOR_PROPOSAL_BASE_HASH"
const runVendorDraftResultEnvironment = "PARTITUR_RUN_VENDOR_DRAFT_RESULT"

func TestMain(m *testing.M) {
	if os.Getenv(initTestCommandEnvironment) == "1" {
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
	if os.Getenv(runVendorEnvironment) == "1" {
		runVendorFixture()
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("partitur-test 9.8.7")
		os.Exit(0)
	}
	if os.Getenv(fakeAdapterEnvironment) == "1" &&
		strings.HasPrefix(
			filepath.Base(os.Args[0]),
			"partitur-adapter-",
		) {
		runValidateFakeAdapter()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestHarnessMarkerRejectsUntaggedBinary(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "partitur")
	build := exec.Command("go", "build", "-o", output, "./cmd/partitur")
	build.Dir = root
	build.Env = os.Environ()
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build untagged partitur: %v\n%s", err, data)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, output, "run")
	command.Env = append(os.Environ(), "PARTITUR_FAULTPOINT_HARNESS=1")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatal("untagged harness binary did not reject the harness marker before timeout")
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 2 {
		t.Fatalf("untagged harness binary error = %v, want exit 2; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "faultpoint harness requires a binary built with -tags=faultprobe") {
		t.Fatalf("untagged harness binary stdout=%q stderr=%q, want clear build-tag failure", stdout.String(), stderr.String())
	}
}

// The whole chain, end to end: a cancellation that lands while the adapter is genuinely
// mid-execute. §6 step 4 has the driver observe it, stop what it launched, and terminalize
// through the one oracle — so the payoff to assert is the journal, not the exit code.
//
// The request is appended directly rather than through `partitur cancel`, because that
// command's own live-owner exit mapping is PR D's; this test is about what the driver does.
func TestRunTerminalizesACancellationObservedMidExecute(t *testing.T) {
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
	repository := t.TempDir()
	writeValidateInputs(t, repository, runScore(), runCast())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")

	marker := filepath.Join(t.TempDir(), "executing")
	environment := replaceEnvironment(os.Environ(), map[string]string{
		"HOME":                      t.TempDir(),
		"PATH":                      bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN":        vendor,
		runVendorEnvironment:        "1",
		runVendorOutcomeEnvironment: "block_until_killed",
		runVendorMarkerEnvironment:  marker,
	})

	// Append the request only once the vendor says it is executing. Polling a marker the
	// vendor writes is the edge that puts the cancellation inside the execute window.
	requested := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(marker); err != nil {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			store, err := runstore.New(repository, faultpoint.Nop{})
			if err != nil {
				requested <- err
				return
			}
			runID, err := soleRunID(repository)
			if err != nil {
				requested <- err
				return
			}
			requested <- store.RequestCancellation(runID)
			return
		}
		requested <- errors.New("vendor never reported that it was executing")
	}()

	// Bounded: without the cancellation wiring the vendor blocks forever and this test
	// would hang the package instead of naming the guard that went missing.
	code, stdout, runStderr := runCommandBinaryWithin(t, 60*time.Second, partitur, repository, environment, "run")
	if err := <-requested; err != nil {
		t.Fatal(err)
	}
	runID := strings.TrimSpace(stdout)
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	if code != 4 || runID == "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q journal=%v", code, stdout, runStderr, eventKinds(journal.Events))
	}
	var stopped, cancelled *runstate.Event
	for index := range journal.Events {
		event := &journal.Events[index]
		switch event.Type {
		case runstate.EventPerformerCompleted, runstate.EventAttemptCompleted,
			runstate.EventMovementSucceeded:
			t.Fatalf("a cancelled run recorded %q", event.Type)
		case runstate.EventExecutionStopped:
			stopped = event
		case runstate.EventRunCancelled:
			cancelled = event
		}
	}
	if stopped == nil || cancelled == nil {
		t.Fatalf("journal = %v", eventKinds(journal.Events))
	}
	if stopped.Seq > cancelled.Seq {
		t.Fatalf("(c) must precede (e): stopped seq %d, cancelled seq %d", stopped.Seq, cancelled.Seq)
	}
	// §6 (c): the interval is closed by the oracle, clamped whichever canceller runs it.
	var stop map[string]any
	if err := json.Unmarshal(stopped.Payload, &stop); err != nil {
		t.Fatal(err)
	}
	if stop["reason"] != "cancelled" || stop["charging"] != "clamped" {
		t.Fatalf("execution.stopped payload = %v", stop)
	}
	// §6 (d): the driver's own lease still matched, so it self-fenced.
	var terminal map[string]any
	if err := json.Unmarshal(cancelled.Payload, &terminal); err != nil {
		t.Fatal(err)
	}
	if _, ok := terminal["fenced_epoch"]; !ok {
		t.Fatalf("run.cancelled payload = %v, want a fenced_epoch", terminal)
	}
	// §6 (f): the lease is gone.
	if _, err := os.Stat(filepath.Join(repository, ".partitur", "runs", runID, "driver.lease")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("driver.lease stat = %v, want absent", err)
	}
}

func TestRunRoutesAdapterProposalThroughProductionComposition(t *testing.T) {
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
	scoreDocument := runScore()
	scoreDocument["movements"].([]any)[0].(map[string]any)["may_propose"] = true
	compiled, diagnostics := score.CompileValue(scoreDocument)
	if len(diagnostics) != 0 {
		t.Fatalf("compile proposal fixture: %v", diagnostics)
	}
	baseHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	writeValidateInputs(t, repository, scoreDocument, runCast())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")

	environment := replaceEnvironment(os.Environ(), map[string]string{
		"HOME":                               t.TempDir(),
		"PATH":                               bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN":                 vendor,
		runVendorEnvironment:                 "1",
		runVendorProposalBaseHashEnvironment: baseHash,
	})
	code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "run")
	runID := strings.TrimSpace(stdout)
	if code != 0 || runID == "" || stdout != runID+"\n" || stderr != "" {
		t.Fatalf("adapter proposal run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	blocked, routed := -1, -1
	for index, event := range journal.Events {
		switch event.Type {
		case runstate.EventAttemptBlocked:
			blocked = index
		case runstate.EventAmendmentRoutedHuman:
			routed = index
		}
	}
	if blocked < 0 || routed < 0 || blocked >= routed {
		t.Fatalf("production proposal disposition journal=%v", eventKinds(journal.Events))
	}
}

func TestProductionDriverInstallsReceiptObserver(t *testing.T) {
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

	scoreDocument := runScore()
	scoreDocument["movements"].([]any)[0].(map[string]any)["may_propose"] = true
	compiled, diagnostics := score.CompileValue(scoreDocument)
	if len(diagnostics) != 0 {
		t.Fatalf("compile proposal fixture: %v", diagnostics)
	}
	baseHash, err := compiled.Hash()
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	writeValidateInputs(t, repository, scoreDocument, runCast())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")

	environment := replaceEnvironment(os.Environ(), map[string]string{
		"HOME":                               t.TempDir(),
		"PATH":                               bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN":                 vendor,
		runVendorEnvironment:                 "1",
		runVendorProposalBaseHashEnvironment: baseHash,
	})
	child := pauseRunAtReceipt(t, partitur, repository, environment, "movement.movement.ready")
	defer child.stop(t)
	if err := child.command.Process.Kill(); err != nil {
		t.Fatalf("kill receipt-paused driver: %v", err)
	}
	if err := child.command.Wait(); err == nil {
		t.Fatal("receipt-paused driver exited successfully")
	}
	child.commandEnded = true

	runID, err := soleRunID(repository)
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Movements["inspect"] != runstate.MovementReady {
		t.Fatalf("movement-ready receipt did not leave a ready movement: %+v", input.Projection.State.Movements)
	}
}

func TestRunQuiescesWhenPrepareIsObservedMidExecute(t *testing.T) {
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
	repository := t.TempDir()
	writeValidateInputs(t, repository, runScore(), runCast())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")

	marker := filepath.Join(t.TempDir(), "executing")
	environment := replaceEnvironment(os.Environ(), map[string]string{
		"HOME":                      t.TempDir(),
		"PATH":                      bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN":        vendor,
		runVendorEnvironment:        "1",
		runVendorOutcomeEnvironment: "block_until_killed",
		runVendorMarkerEnvironment:  marker,
	})
	prepared := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(marker); err != nil {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			store, err := runstore.New(repository, faultpoint.Nop{})
			if err != nil {
				prepared <- err
				return
			}
			runID, err := soleRunID(repository)
			if err != nil {
				prepared <- err
				return
			}
			prepared <- appendRecoveryControlPrepare(store, runstate.RunID(runID), repository)
			return
		}
		prepared <- errors.New("vendor never reported that it was executing")
	}()
	code, stdout, stderr := runCommandBinaryWithin(t, 60*time.Second, partitur, repository, environment, "run")
	if err := <-prepared; err != nil {
		t.Fatal(err)
	}
	runID := strings.TrimSpace(stdout)
	if code != 0 || runID == "" || stderr != "" {
		t.Fatalf("prepare-observed run: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.PendingPrepare == nil || input.Projection.State.OpenExecution != nil {
		t.Fatalf("prepare acknowledgement state = %+v", input.Projection.State)
	}
	if _, err := os.Stat(filepath.Join(repository, ".partitur", "runs", runID, "driver.quiesced.prepare-control")); err != nil {
		t.Fatalf("prepare sidecar: %v", err)
	}
	resumeEnvironment := fixtureOutcomeEnvironment(environment, "success")
	resumeCode, resumeStdout, resumeStderr := runCommandBinaryWithin(t, 60*time.Second, partitur, repository, resumeEnvironment, "resume", runID)
	if resumeCode != 0 || resumeStdout != "" || resumeStderr != "" {
		t.Fatalf("prepare recovery handoff: exit=%d stdout=%q stderr=%q", resumeCode, resumeStdout, resumeStderr)
	}
	input, err = store.LoadRunInput(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.PendingPrepare != nil || input.Projection.State.ScoreHead.Revision != 2 || input.Projection.State.Run != runstate.RunSucceeded {
		t.Fatalf("prepare recovery state = %+v", input.Projection.State)
	}
	journal, err := store.ReadJournal(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.Type == runstate.EventPerformerSelected && event.ScoreRevision == 2 && bytes.Contains(event.Payload, []byte(`"reason":"revision_restart"`)) && event.CausationID != "" {
			return
		}
	}
	t.Fatalf("recovery never selected the revision restart: journal=%+v", journal.Events)
}

// appendRecoveryControlPrepare is a recovery/control fixture. It derives its
// score and target attempts from the live run; it is not a live amend command.
func appendRecoveryControlPrepare(store *runstore.Store, runID runstate.RunID, repository string) error {
	input, err := store.LoadRunInput(runID)
	if err != nil {
		return err
	}
	contents, err := os.ReadFile(filepath.Join(repository, "partitur.yaml"))
	if err != nil {
		return err
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		return err
	}
	document["revision"] = float64(input.Projection.State.ScoreHead.Revision + 1)
	document["goal"] = "recovery control fixture prepared revision"
	nextContents, err := json.Marshal(document)
	if err != nil {
		return err
	}
	next, diagnostics := score.Compile(nextContents)
	if len(diagnostics) != 0 {
		return fmt.Errorf("control fixture score diagnostics: %v", diagnostics)
	}
	nextHash, err := next.Hash()
	if err != nil {
		return err
	}
	targets := make([]runstate.AttemptID, 0)
	for id, attempt := range input.Projection.State.Attempts {
		if attempt.State == runstate.AttemptStarting || attempt.State == runstate.AttemptRunning || attempt.State == runstate.AttemptVerifying {
			targets = append(targets, id)
		}
	}
	sort.Slice(targets, func(left, right int) bool { return targets[left] < targets[right] })
	head := make([]runstate.HeadMovement, 0, len(next.Movements()))
	for _, movement := range next.Movements() {
		repoWrite := false
		for _, grant := range movement.Grants {
			repoWrite = repoWrite || grant == "repo_write"
		}
		head = append(head, runstate.HeadMovement{ID: runstate.MovementID(movement.ID), Initial: runstate.MovementPending, RepoWrite: repoWrite, HasDependencies: len(movement.Needs) != 0, Final: movement.ID == next.Execution().FinalMovementID})
	}
	envelope := "NARROW_PATHS"
	plan := runstate.ApprovalPlan{Schema: runstate.ApprovalPlanSchema, ProposalID: "proposal-control", Mode: "auto", EnvelopeClass: &envelope,
		BaseRevision: input.Projection.State.ScoreHead.Revision, BaseHash: input.Projection.State.ScoreHead.SemanticHash, ClassifierVersion: 1,
		NewRevision: next.Revision(), NewSnapshotHash: runstate.Hash(nextHash), NewSnapshotFileHash: runstate.Hash(controlFixtureHash(nextContents)),
		TypedDelta: []any{}, ActualImpact: map[string]any{"score_changes": []any{}, "authority": map[string]any{"allowed_paths": map[string]any{"added": []any{}, "removed": []any{}}, "grants": []any{}, "side_effects": map[string]any{"added": []any{}, "removed": []any{}}}, "budget": map[string]any{}},
		HeadMovements: head, SupersededAttemptIDs: targets, ObsoletedDecisionIDs: []string{}, Finalization: false, IdentityVersions: resumeIdentityVersions()}
	planBytes, err := runstate.EncodeApprovalPlan(plan)
	if err != nil {
		return err
	}
	return store.Mutate(runID, "", func(transaction *runstore.Txn) error {
		if _, err := transaction.At("control-fixture.snapshot").PublishImmutable(runstore.Path(fmt.Sprintf("scores/revision-%d.yaml", next.Revision())), nextContents, runstore.Hash(controlFixtureHash(nextContents))); err != nil {
			return err
		}
		if _, err := transaction.At("control-fixture.plan").PublishImmutable("prepares/prepare-control.json", planBytes, runstore.Hash(controlFixtureHash(planBytes))); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{"prepare_id": "prepare-control", "proposal_id": "proposal-control", "mode": "auto", "envelope_class": envelope,
			"base_revision": plan.BaseRevision, "base_hash": plan.BaseHash, "new_revision": plan.NewRevision, "new_snapshot_hash": plan.NewSnapshotHash, "new_snapshot_file_hash": plan.NewSnapshotFileHash,
			"plan_record_hash": controlFixtureHash(planBytes), "target_attempt_ids": targets, "observed_authority_epoch": input.Projection.State.Authority.Epoch,
			"quiesce_silence_limit_ms": 60_000, "classifier_version": 1, "identity_versions": resumeIdentityVersions()})
		if err != nil {
			return err
		}
		_, err = transaction.At("amendment.approval_prepared").Append(runstate.Event{RunID: runID, ScoreRevision: plan.BaseRevision, Type: runstate.EventAmendmentApprovalPrepared, Payload: payload})
		return err
	})
}

func controlFixtureHash(contents []byte) string {
	digest := sha256.Sum256(contents)
	return fmt.Sprintf("sha256:%x", digest)
}

func eventKinds(events []runstate.Event) []runstate.EventType {
	kinds := make([]runstate.EventType, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Type)
	}
	return kinds
}

func soleRunID(repository string) (runstate.RunID, error) {
	entries, err := os.ReadDir(filepath.Join(repository, ".partitur", "runs"))
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return runstate.RunID(entry.Name()), nil
		}
	}
	return "", errors.New("no run directory yet")
}

func TestRunOneMovementRealAdapterEndToEnd(t *testing.T) {
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
	repository := t.TempDir()
	writeValidateInputs(t, repository, runScore(), runCast())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")

	environment := replaceEnvironment(os.Environ(), map[string]string{
		"HOME":               t.TempDir(),
		"PATH":               bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN": vendor,
		runVendorEnvironment: "1",
	})
	code, stdout, stderr := runCommandBinary(
		t,
		partitur,
		repository,
		environment,
		"run",
	)
	runID := strings.TrimSpace(stdout)
	if code != 0 || runID == "" ||
		stdout != runID+"\n" || stderr != "" {
		t.Fatalf(
			"exit=%d stdout=%q stderr=%q",
			code,
			stdout,
			stderr,
		)
	}
	journalPath := filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl")
	journalBeforeStatus, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	code, statusStdout, statusStderr := runCommandBinary(
		t,
		partitur,
		repository,
		environment,
		"status",
		runID,
		"--json",
	)
	journalAfterStatus, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var statusReport statusprojection.Report
	if code != 0 || statusStderr != "" || json.Unmarshal([]byte(statusStdout), &statusReport) != nil ||
		statusReport.Schema != "partitur/status+json;v=1" ||
		statusReport.Run.ID != runID || statusReport.Run.Lifecycle != string(runstate.RunSucceeded) ||
		string(journalBeforeStatus) != string(journalAfterStatus) {
		t.Fatalf(
			"status exit=%d stdout=%q stderr=%q report=%+v journal_before=%q journal_after=%q",
			code,
			statusStdout,
			statusStderr,
			statusReport,
			journalBeforeStatus,
			journalAfterStatus,
		)
	}
	events := journalEventTypes(
		t,
		journalPath,
	)
	want := []string{
		"run.started",
		"authority.granted",
		"application_candidate.recorded",
		"movement.ready",
		"movement.started",
		"performer.selected",
		"execution.started",
		"attempt.started",
		"adapter.probed",
		"log",
		"progress",
		"artifact.recorded",
		"execution.stopped",
		"performer.completed",
		"verification.passed",
		"execution.started",
		"acceptance.started",
		"criterion.started",
		"criterion.completed",
		"criterion.started",
		"criterion.completed",
		"acceptance.evaluation_completed",
		"execution.stopped",
		"attempt.completed",
		"movement.succeeded",
	}
	if !slicesEqual(events, want) {
		t.Fatalf("journal sequence\n got: %v\nwant: %v", events, want)
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.Replay(
		runstate.RunID(runID),
		[]runstate.MovementSeed{{
			ID:      "inspect",
			Initial: runstate.MovementPending,
			Final:   true,
		}},
		"run.e2e.replay",
	)
	if err != nil {
		t.Fatal(err)
	}
	verified := false
	for _, value := range replay.State.VerifiedAttempts {
		verified = verified || value
	}
	if replay.State.Run != runstate.RunSucceeded ||
		replay.State.Movements["inspect"] != runstate.MovementSucceeded ||
		!verified {
		t.Fatalf(
			"run=%s movement=%s verified=%v",
			replay.State.Run,
			replay.State.Movements["inspect"],
			replay.State.VerifiedAttempts,
		)
	}
	if _, err := os.Stat(filepath.Join(
		repository,
		".partitur",
		"runs",
		runID,
		"driver.lease",
	)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal driver lease: %v", err)
	}
}

func TestRunHumanGateApprovalProjectsApprovedMark(t *testing.T) {
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
	repository, environment := humanGateKillHarnessRepository(t, bin, vendor)

	code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "run")
	runID := strings.TrimSpace(stdout)
	if code != 0 || runID == "" || stdout != runID+"\n" || stderr != "" {
		t.Fatalf("waiting-human run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	waiting, err := statusprojection.Read(repository, runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting.Run.PendingDecisions) != 1 || waiting.Run.PendingDecisions[0].Type != "human_gate" {
		t.Fatalf("pending decisions = %#v, want one human gate", waiting.Run.PendingDecisions)
	}
	decisionID := waiting.Run.PendingDecisions[0].ID
	code, stdout, stderr = runCommandBinary(t, partitur, repository, environment, "approve", decisionID, "--approve")
	if code != 0 || stdout != "" || stderr != expectedDecisionResumeHint(runID) {
		t.Fatalf("approve exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = runCommandBinary(t, partitur, repository, environment, "resume", runID)
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("resume exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	report, err := statusprojection.Read(repository, runID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Run.Lifecycle != string(runstate.RunSucceeded) || len(report.Run.Movements) != 1 {
		t.Fatalf("completed report = %#v", report.Run)
	}
	var approved []statusprojection.Mark
	for _, mark := range report.Run.Movements[0].Marks {
		if mark.Grade == "APPROVED" {
			approved = append(approved, mark)
		}
	}
	if len(approved) != 1 || approved[0].GateDecisionID != decisionID {
		t.Fatalf("APPROVED marks = %#v, want gate decision %q", approved, decisionID)
	}
}

func TestRunHumanGateOverrideProjectsOverriddenAndApprovedMarks(t *testing.T) {
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
	repository, environment := contestedHumanGateKillHarnessRepository(t, bin, vendor)
	const findingID = "perf:n+1"
	environment = replaceEnvironment(environment, map[string]string{runVendorFindingIDEnvironment: findingID})

	code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "run")
	runID := strings.TrimSpace(stdout)
	if code != 0 || runID == "" || stdout != runID+"\n" || stderr != "" {
		t.Fatalf("waiting-human run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	waiting := statusCommandReport(t, partitur, repository, environment, runID)
	if waiting.Run.Lifecycle != string(runstate.RunWaitingHuman) || len(waiting.Run.PendingDecisions) != 1 || waiting.Run.PendingDecisions[0].Type != "human_gate" {
		t.Fatalf("waiting report = %#v", waiting.Run)
	}
	decisionID := waiting.Run.PendingDecisions[0].ID
	reviewed := reviewedMark(t, waiting)
	if reviewed.ReviewOutcome != "CONTESTED" || reviewed.FindingsInstanceID == "" {
		t.Fatalf("waiting REVIEWED mark = %#v", reviewed)
	}

	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	pending := input.Projection.State.PendingDecisions[decisionID]
	if pending.GateID == "" || len(pending.BlockingFindings) != 1 || pending.BlockingFindings[0] != (runstate.FindingReference{ArtifactInstanceID: reviewed.FindingsInstanceID, FindingID: findingID}) {
		t.Fatalf("pending contested gate = %#v", pending)
	}
	journalPath := filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl")
	journalBefore, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, override := range []string{
		"missing-separator",
		":fixture-blocker",
		reviewed.FindingsInstanceID + ":",
	} {
		code, stdout, stderr = runCommandBinary(t, partitur, repository, environment, "approve", decisionID, "--approve", "--override", override, "--reason", "human judgment")
		if code != 1 || stdout != "" || !strings.Contains(stderr, "usage:") {
			t.Fatalf("malformed override %q exit=%d stdout=%q stderr=%q", override, code, stdout, stderr)
		}
	}
	code, stdout, stderr = runCommandBinary(t, partitur, repository, environment, "approve", decisionID, "--approve", "--override", reviewed.FindingsInstanceID+":"+findingID, "--override", reviewed.FindingsInstanceID+":"+findingID, "--reason", "human judgment")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "usage:") {
		t.Fatalf("duplicate override exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	journalAfterUsage, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(journalBefore, journalAfterUsage) {
		t.Fatalf("usage error appended a journal event")
	}

	for _, override := range []string{
		"other@attempt:" + findingID,
		reviewed.FindingsInstanceID + ":other",
		reviewed.FindingsInstanceID + ":fixture-blocker:extra",
	} {
		code, stdout, stderr = runCommandBinary(t, partitur, repository, environment, "approve", decisionID, "--approve", "--override", override, "--reason", "human judgment")
		if code != 2 || stdout != "" || !strings.Contains(stderr, "precondition refused") {
			t.Fatalf("out-of-set override %q exit=%d stdout=%q stderr=%q", override, code, stdout, stderr)
		}
	}

	code, stdout, stderr = runCommandBinary(t, partitur, repository, environment, "approve", decisionID, "--approve", "--override", reviewed.FindingsInstanceID+":"+findingID, "--reason", "human judgment")
	if code != 0 || stdout != "" || stderr != expectedDecisionResumeHint(runID) {
		t.Fatalf("approve override exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = runCommandBinary(t, partitur, repository, environment, "resume", runID)
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("resume exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	report := statusCommandReport(t, partitur, repository, environment, runID)
	if report.Run.Lifecycle != string(runstate.RunSucceeded) {
		t.Fatalf("completed report = %#v", report.Run)
	}
	reviewed = reviewedMark(t, report)
	if reviewed.ReviewOutcome != "OVERRIDDEN" {
		t.Fatalf("completed REVIEWED mark = %#v", reviewed)
	}
	var approved []statusprojection.Mark
	for _, mark := range report.Run.Movements[0].Marks {
		if mark.Grade == "APPROVED" {
			approved = append(approved, mark)
		}
	}
	if len(approved) != 1 || approved[0].GateDecisionID != decisionID {
		t.Fatalf("APPROVED marks = %#v, want gate decision %q", approved, decisionID)
	}
}

func statusCommandReport(t *testing.T, partitur, repository string, environment []string, runID string) statusprojection.Report {
	t.Helper()
	code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "status", runID, "--json")
	var report statusprojection.Report
	if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &report) != nil {
		t.Fatalf("status exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	return report
}

func reviewedMark(t *testing.T, report statusprojection.Report) statusprojection.Mark {
	t.Helper()
	if len(report.Run.Movements) != 1 {
		t.Fatalf("movements = %#v", report.Run.Movements)
	}
	var reviewed []statusprojection.Mark
	for _, mark := range report.Run.Movements[0].Marks {
		if mark.Grade == "REVIEWED" {
			reviewed = append(reviewed, mark)
		}
	}
	if len(reviewed) != 1 {
		t.Fatalf("REVIEWED marks = %#v", reviewed)
	}
	return reviewed[0]
}

func TestRunTaskFailureTerminalizesThroughLiveNoneDisposition(t *testing.T) {
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
	repository := t.TempDir()
	writeValidateInputs(t, repository, runScore(), runCast())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")

	environment := replaceEnvironment(os.Environ(), map[string]string{
		"HOME":                      t.TempDir(),
		"PATH":                      bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN":        vendor,
		runVendorEnvironment:        "1",
		runVendorOutcomeEnvironment: "task_failed",
	})
	code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "run")
	runID := strings.TrimSpace(stdout)
	if code != 4 || runID == "" || stdout != runID+"\n" || !strings.Contains(stderr, "movement_failed") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	journalPath := filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl")
	got := journalEventTypes(t, journalPath)
	want := []string{
		"run.started", "authority.granted", "application_candidate.recorded",
		"movement.ready", "movement.started", "performer.selected", "execution.started",
		"attempt.started", "adapter.probed", "execution.stopped", "attempt.failed",
		"movement.failed", "run.failed",
	}
	if !slicesEqual(got, want) {
		t.Fatalf("event order=%v want=%v", got, want)
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runstate.RunID(runID))
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Run != runstate.RunFailed || input.Projection.State.Movements["inspect"] != runstate.MovementFailed {
		t.Fatalf("terminal projection=%+v", input.Projection.State)
	}
}

func TestRunQualityRetryChainTerminatesAtRetryExhaustion(t *testing.T) {
	score := runScore()
	score["policy"].(map[string]any)["budget"].(map[string]any)["retries_per_movement"] = float64(1)
	runID, events := runLiveChargedFailure(t, score, runCast(), "task_failed")
	want := []string{
		"run.started", "authority.granted", "application_candidate.recorded",
		"movement.ready", "movement.started", "performer.selected", "execution.started",
		"attempt.started", "adapter.probed", "execution.stopped", "attempt.failed",
		"performer.selected", "execution.started", "attempt.started", "adapter.probed",
		"execution.stopped", "attempt.failed", "movement.failed", "run.failed",
	}
	if !slicesEqual(events, want) {
		t.Fatalf("run=%s event order=%v want=%v", runID, events, want)
	}
	assertChargedSelections(t, runID, "quality_retry", []string{"worker", "worker"})
}

func TestRunFallbackChainTerminatesWithoutRevisitingPerformer(t *testing.T) {
	cast := runCast()
	performers := cast["performers"].(map[string]any)
	performers["backup-a"] = map[string]any{"adapter": "codex", "model": "gpt-5.6-terra"}
	performers["backup-b"] = map[string]any{"adapter": "codex", "model": "gpt-5.6-terra"}
	cast["bindings"].(map[string]any)["reader"] = map[string]any{
		"performer": "worker", "fallbacks": []any{"backup-a", "backup-b"},
	}
	runID, events := runLiveChargedFailure(t, runScore(), cast, "rate_limited")
	want := []string{
		"run.started", "authority.granted", "application_candidate.recorded",
		"movement.ready", "movement.started", "performer.selected", "execution.started",
		"attempt.started", "adapter.probed", "execution.stopped", "attempt.failed",
		"performer.selected", "execution.started", "attempt.started", "adapter.probed",
		"execution.stopped", "attempt.failed", "performer.selected", "execution.started",
		"attempt.started", "adapter.probed", "execution.stopped", "attempt.failed", "movement.failed", "run.failed",
	}
	if !slicesEqual(events, want) {
		t.Fatalf("run=%s event order=%v want=%v", runID, events, want)
	}
	assertChargedSelections(t, runID, "fallback", []string{"worker", "backup-a", "backup-b"})
}

func runLiveChargedFailure(t *testing.T, score, cast map[string]any, outcome string) (string, []string) {
	t.Helper()
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
	repository := t.TempDir()
	writeValidateInputs(t, repository, score, cast)
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")
	environment := replaceEnvironment(os.Environ(), map[string]string{
		"HOME":                      t.TempDir(),
		"PATH":                      bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN":        vendor,
		runVendorEnvironment:        "1",
		runVendorOutcomeEnvironment: outcome,
	})
	code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "run")
	runID := strings.TrimSpace(stdout)
	if code != 4 || runID == "" || stdout != runID+"\n" || !strings.Contains(stderr, "movement_failed") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	return filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl"), journalEventTypes(t, filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl"))
}

func assertChargedSelections(t *testing.T, journalPath, reason string, wantPerformers []string) {
	t.Helper()
	store, err := runstore.New(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(journalPath)))), faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	runID := runstate.RunID(filepath.Base(filepath.Dir(journalPath)))
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	var selections []runstate.Event
	for _, event := range journal.Events {
		if event.Type == runstate.EventPerformerSelected {
			selections = append(selections, event)
		}
	}
	if len(selections) != len(wantPerformers) {
		t.Fatalf("selected=%d want=%d", len(selections), len(wantPerformers))
	}
	for index, event := range selections {
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["performer_id"] != wantPerformers[index] {
			t.Fatalf("selection %d performer=%q want=%q", index, payload["performer_id"], wantPerformers[index])
		}
		if index == 0 {
			continue
		}
		if payload["reason"] != reason || event.CausationID == "" {
			t.Fatalf("selection %d payload=%v causation=%q", index, payload, event.CausationID)
		}
	}
}

func TestRunGrantAndAcceptanceFailuresTerminalizeThroughLiveNoneDisposition(t *testing.T) {
	tests := []struct {
		name    string
		score   map[string]any
		outcome string
		failed  string
	}{
		{
			name: "grant denial", score: runScore(), outcome: "read_only_violation", failed: "attempt.failed",
		},
		{
			name: "acceptance failure", score: func() map[string]any {
				score := runScore()
				criterion := score["movements"].([]any)[0].(map[string]any)["acceptance"].(map[string]any)["hard"].([]any)[0].(map[string]any)
				criterion["expected_hash"] = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
				return score
			}(), failed: "acceptance.failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runID, events := runLiveNoneFailure(t, test.score, test.outcome)
			failed := -1
			for index, event := range events {
				if event == test.failed {
					failed = index
				}
			}
			if failed == -1 || len(events) < 2 || failed >= len(events)-2 || events[len(events)-2] != "movement.failed" || events[len(events)-1] != "run.failed" {
				t.Fatalf("run=%s event order=%v", runID, events)
			}
		})
	}
}

func runLiveNoneFailure(t *testing.T, score map[string]any, outcome string) (string, []string) {
	t.Helper()
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
	repository := t.TempDir()
	writeValidateInputs(t, repository, score, runCast())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")
	environment := replaceEnvironment(os.Environ(), map[string]string{
		"HOME":                      t.TempDir(),
		"PATH":                      bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN":        vendor,
		runVendorEnvironment:        "1",
		runVendorOutcomeEnvironment: outcome,
	})
	code, stdout, stderr := runCommandBinary(t, partitur, repository, environment, "run")
	runID := strings.TrimSpace(stdout)
	if code != 4 || runID == "" || stdout != runID+"\n" || !strings.Contains(stderr, "movement_failed") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	return runID, journalEventTypes(t, filepath.Join(repository, ".partitur", "runs", runID, "journal.jsonl"))
}

func TestValidateEndToEnd(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildE2EBinary(t, root, bin, "partitur")
	buildE2EBinary(t, root, bin, "partitur-adapter-claude")
	buildE2EBinary(t, root, bin, "partitur-adapter-codex")
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, adapterID := range []string{
		"capability",
		"enforcement",
		"advisory",
	} {
		path := filepath.Join(bin, "partitur-adapter-"+adapterID)
		if err := os.Symlink(testExecutable, path); err != nil {
			t.Fatal(err)
		}
	}
	baseEnvironment := replaceEnvironment(os.Environ(), map[string]string{
		"PATH":                 bin,
		"PARTITUR_CLAUDE_BIN":  testExecutable,
		"PARTITUR_CODEX_BIN":   testExecutable,
		fakeAdapterEnvironment: "1",
	})

	t.Run("working_directory_is_the_only_repository_root", func(t *testing.T) {
		parent := t.TempDir()
		child := filepath.Join(parent, "nested")
		if err := os.Mkdir(child, 0o700); err != nil {
			t.Fatal(err)
		}
		writeValidateInputs(
			t,
			parent,
			e2eScore("plan"),
			e2eCast(map[string]string{"plan": "performer"}),
		)
		code, stdout, stderr := runValidateBinary(
			t,
			partitur,
			child,
			replaceEnvironment(
				baseEnvironment,
				map[string]string{"HOME": t.TempDir()},
			),
		)
		if code != 2 || stdout != "" ||
			!strings.Contains(
				stderr,
				filepath.Join(child, "partitur.yaml"),
			) {
			t.Fatalf(
				"exit=%d stdout=%q stderr=%q",
				code,
				stdout,
				stderr,
			)
		}
	})

	t.Run("real_first_party_adapters", func(t *testing.T) {
		repository := t.TempDir()
		home := t.TempDir()
		scoreDocument := strictRealAdapterScore()
		castDocument := map[string]any{
			"cast": "0.1",
			"performers": map[string]any{
				"claude-primary": map[string]any{
					"adapter": "claude",
					"model":   "claude-fable-5",
				},
				"codex-fallback": map[string]any{
					"adapter": "codex",
					"model":   "gpt-5.6-sol",
				},
			},
			"bindings": map[string]any{
				"implement": map[string]any{
					"performer": "claude-primary",
				},
				"verify": map[string]any{
					"performer": "codex-fallback",
				},
			},
		}
		writeValidateInputs(t, repository, scoreDocument, castDocument)
		before := repositoryTree(t, repository)
		code, stdout, stderr := runValidateBinary(
			t,
			partitur,
			repository,
			replaceEnvironment(baseEnvironment, map[string]string{"HOME": home}),
		)
		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf(
				"exit=%d stdout=%q stderr=%q",
				code,
				stdout,
				stderr,
			)
		}
		after := repositoryTree(t, repository)
		if !slicesEqual(before, after) {
			t.Fatalf("repository tree changed\nbefore=%#v\nafter=%#v", before, after)
		}
	})

	t.Run("one_defect_per_fatal_block", func(t *testing.T) {
		tests := []struct {
			name  string
			score map[string]any
			cast  map[string]any
			want  string
		}{
			{
				name: "score",
				score: func() map[string]any {
					value := e2eScore("plan")
					delete(value, "goal")
					return value
				}(),
				cast: e2eCast(map[string]string{"plan": "performer"}),
				want: "score: rule=\"score.schema\" pointer=\"/goal\" detail=\"required\"\n",
			},
			{
				name:  "cast",
				score: e2eScore("plan"),
				cast: func() map[string]any {
					value := e2eCast(map[string]string{"plan": "performer"})
					delete(
						value["performers"].(map[string]any)["performer"].(map[string]any),
						"model",
					)
					return value
				}(),
				want: "cast: rule=\"cast.schema\" origin=\"project\" pointer=\"/performers/performer/model\" detail=\"required\"\n",
			},
			{
				name:  "adapter_environment",
				score: e2eScore("plan"),
				cast: func() map[string]any {
					value := e2eCast(map[string]string{"plan": "performer"})
					value["performers"].(map[string]any)["performer"].(map[string]any)["adapter"] = "missing"
					return value
				}(),
				want: "adapter-environment: adapter=\"missing\" kind=\"executable_absent\" detail=\"partitur-adapter-missing is absent from PATH\" stderr=\"\"\n",
			},
			{
				name:  "capability",
				score: e2eScore("plan"),
				cast: func() map[string]any {
					value := e2eCast(map[string]string{"plan": "performer"})
					value["performers"].(map[string]any)["performer"].(map[string]any)["adapter"] = "capability"
					return value
				}(),
				want: "capability: part=\"plan\" performer=\"performer\" missing=[\"network\"]\n",
			},
			{
				name:  "enforcement",
				score: e2eScore("plan"),
				cast: func() map[string]any {
					value := e2eCast(map[string]string{"plan": "performer"})
					value["performers"].(map[string]any)["performer"].(map[string]any)["adapter"] = "enforcement"
					return value
				}(),
				want: "enforcement: movement=\"plan-movement\" part=\"plan\" performer=\"performer\" unmet=[\"read_only\"]\n",
			},
		}
		for _, test := range tests {
			test := test
			t.Run(test.name, func(t *testing.T) {
				repository := t.TempDir()
				home := t.TempDir()
				writeValidateInputs(t, repository, test.score, test.cast)
				code, stdout, stderr := runValidateBinary(
					t,
					partitur,
					repository,
					replaceEnvironment(
						baseEnvironment,
						map[string]string{"HOME": home},
					),
				)
				if code != 3 || stdout != "" || stderr != test.want {
					t.Fatalf(
						"exit=%d stdout=%q\nstderr=%q\nwant=%q",
						code,
						stdout,
						stderr,
						test.want,
					)
				}
			})
		}
	})

	t.Run("ordered_blocks_and_suppression", func(t *testing.T) {
		t.Run("score_then_cast", func(t *testing.T) {
			repository := t.TempDir()
			home := t.TempDir()
			scoreDocument := e2eScore("plan")
			delete(scoreDocument, "goal")
			castDocument := e2eCast(map[string]string{"plan": "performer"})
			delete(
				castDocument["performers"].(map[string]any)["performer"].(map[string]any),
				"model",
			)
			writeValidateInputs(
				t,
				repository,
				scoreDocument,
				castDocument,
			)
			code, stdout, stderr := runValidateBinary(
				t,
				partitur,
				repository,
				replaceEnvironment(
					baseEnvironment,
					map[string]string{"HOME": home},
				),
			)
			want := "" +
				"score: rule=\"score.schema\" pointer=\"/goal\" detail=\"required\"\n" +
				"cast: rule=\"cast.schema\" origin=\"project\" pointer=\"/performers/performer/model\" detail=\"required\"\n"
			if code != 3 || stdout != "" || stderr != want {
				t.Fatalf(
					"exit=%d stdout=%q\nstderr=%q\nwant=%q",
					code,
					stdout,
					stderr,
					want,
				)
			}
		})

		t.Run("cast_then_adapter_capability_enforcement", func(t *testing.T) {
			repository := t.TempDir()
			home := t.TempDir()
			scoreDocument := e2eScore(
				"bad",
				"capability",
				"enforcement",
				"missing",
			)
			castDocument := e2eCast(map[string]string{
				"bad":         "bad-performer",
				"capability":  "capability-performer",
				"enforcement": "enforcement-performer",
			})
			performers := castDocument["performers"].(map[string]any)
			performers["bad-performer"].(map[string]any)["adapter"] = "bad"
			performers["capability-performer"].(map[string]any)["adapter"] = "capability"
			performers["enforcement-performer"].(map[string]any)["adapter"] = "enforcement"
			writeValidateInputs(
				t,
				repository,
				scoreDocument,
				castDocument,
			)
			code, stdout, stderr := runValidateBinary(
				t,
				partitur,
				repository,
				replaceEnvironment(
					baseEnvironment,
					map[string]string{"HOME": home},
				),
			)
			want := "" +
				"cast: rule=\"cast.score\" origin=\"\" pointer=\"/bindings/missing\" detail=\"binding_missing\" hint=\"write the missing binding in .partitur/cast.yaml (project) or ~/.config/partitur/cast.yaml (user-global): bindings.<part>.performer must name an entry in performers\"\n" +
				"adapter-environment: adapter=\"bad\" kind=\"executable_absent\" detail=\"partitur-adapter-bad is absent from PATH\" stderr=\"\"\n" +
				"capability: part=\"capability\" performer=\"capability-performer\" missing=[\"network\"]\n" +
				"enforcement: movement=\"enforcement-movement\" part=\"enforcement\" performer=\"enforcement-performer\" unmet=[\"read_only\"]\n"
			if code != 3 || stdout != "" || stderr != want {
				t.Fatalf(
					"exit=%d stdout=%q\nstderr=%q\nwant=%q",
					code,
					stdout,
					stderr,
					want,
				)
			}
		})
	})

	t.Run("advisory_report_is_nonfatal", func(t *testing.T) {
		repository := t.TempDir()
		home := t.TempDir()
		scoreDocument := e2eScore("plan")
		castDocument := e2eCast(map[string]string{"plan": "performer"})
		performer := castDocument["performers"].(map[string]any)["performer"].(map[string]any)
		performer["adapter"] = "advisory"
		performer["allow_advisory_enforcement"] = true
		writeValidateInputs(t, repository, scoreDocument, castDocument)
		code, stdout, stderr := runValidateBinary(
			t,
			partitur,
			repository,
			replaceEnvironment(baseEnvironment, map[string]string{"HOME": home}),
		)
		want := "enforcement advisory: movement=\"plan-movement\" part=\"plan\" " +
			"performer=\"performer\" unmet=[\"read_only\"]\n"
		if code != 0 || stdout != "" || stderr != want {
			t.Fatalf(
				"exit=%d stdout=%q stderr=%q want=%q",
				code,
				stdout,
				stderr,
				want,
			)
		}
	})
}

func runValidateFakeAdapter() {
	adapterID := strings.TrimPrefix(
		filepath.Base(os.Args[0]),
		"partitur-adapter-",
	)
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	capabilities := map[string]any{
		"repo_read":          true,
		"repo_write":         true,
		"shell":              true,
		"network":            true,
		"resumable_sessions": true,
		"models":             []any{},
	}
	enforcement := map[string]any{
		"path_grants":    true,
		"read_only":      true,
		"network_grants": true,
		"shell_grants":   true,
		"read_grants":    true,
	}
	switch adapterID {
	case "capability":
		capabilities["network"] = false
	case "enforcement", "advisory":
		enforcement["read_only"] = false
	default:
		os.Exit(9)
	}
	response := map[string]any{
		"jsonrpc": "2.0",
		"id":      "probe",
		"result": map[string]any{
			"protocol": 2,
			"adapter": map[string]any{
				"id":      adapterID,
				"version": "1.2.3",
			},
			"capabilities": capabilities,
			"enforcement":  enforcement,
		},
	}
	_ = json.NewEncoder(os.Stdout).Encode(response)
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func buildE2EBinary(
	t *testing.T,
	root, outputDirectory, name string,
) string {
	t.Helper()
	output := filepath.Join(outputDirectory, name)
	command := exec.Command("go", "build", "-tags=faultprobe", "-o", output, "./cmd/"+name)
	command.Dir = root
	command.Env = os.Environ()
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, data)
	}
	return output
}

func runValidateBinary(
	t *testing.T,
	binary, repository string,
	environment []string,
) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := exec.Command(binary, "validate")
	command.Dir = repository
	command.Env = environment
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatal(err)
	}
	return exitError.ExitCode(), stdout.String(), stderr.String()
}

func writeValidateInputs(
	t *testing.T,
	repository string,
	scoreDocument, castDocument map[string]any,
) {
	t.Helper()
	scoreData, err := json.Marshal(scoreDocument)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(repository, "partitur.yaml"),
		scoreData,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	castDirectory := filepath.Join(repository, ".partitur")
	if err := os.MkdirAll(castDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	castData, err := json.Marshal(castDocument)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(castDirectory, "cast.yaml"),
		castData,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
}

func strictRealAdapterScore() map[string]any {
	return map[string]any{
		"score":    "0.2",
		"name":     "real-adapter-fixture",
		"revision": float64(1),
		"status":   "finalized",
		"goal":     "Validate real adapters.",
		"verification": map[string]any{
			"expectation": map[string]any{
				"intent": "pass-existing-tests",
				"apply_gate": map[string]any{
					"waived": true,
					"reason": "end-to-end adapter compatibility fixture",
				},
			},
		},
		"parts": map[string]any{
			"implement": map[string]any{
				"capabilities": []any{
					"repo_read",
					"repo_write",
					"shell",
					"network",
					"resumable_sessions",
				},
			},
			"verify": map[string]any{
				"capabilities": []any{
					"repo_read",
					"shell",
					"network",
				},
			},
		},
		"movements": []any{
			map[string]any{
				"id":          "implement",
				"part":        "implement",
				"grants":      []any{"repo_read", "repo_write", "shell", "network"},
				"instruction": "Validate the adapters.",
				"outputs": []any{
					map[string]any{
						"id":   "change-set",
						"kind": "change_set",
					},
				},
				"acceptance": map[string]any{
					"hard": []any{
						map[string]any{
							"id":  "complete",
							"run": []any{"true"},
						},
					},
				},
			},
			map[string]any{
				"id":          "verify",
				"part":        "verify",
				"needs":       []any{"implement"},
				"grants":      []any{"repo_read", "shell", "network"},
				"instruction": "Verify the adapters.",
				"inputs":      []any{"change-set"},
			},
		},
		"policy": map[string]any{
			"allowed_paths": []any{"**"},
			"budget": map[string]any{
				"active_wall_clock_min": float64(10),
			},
		},
	}
}

func e2eScore(parts ...string) map[string]any {
	partValues := make(map[string]any, len(parts))
	movements := make([]any, 0, len(parts))
	for index, partID := range parts {
		partValues[partID] = map[string]any{
			"capabilities": []any{
				"repo_read",
				"shell",
				"network",
			},
		}
		movement := map[string]any{
			"id":          partID + "-movement",
			"part":        partID,
			"grants":      []any{"repo_read", "shell", "network"},
			"instruction": "Perform " + partID + ".",
		}
		if index == 0 {
			movement["phase"] = "draft"
		}
		movements = append(movements, movement)
	}
	return map[string]any{
		"score":    "0.2",
		"name":     "validate-e2e",
		"revision": float64(1),
		"status":   "draft",
		"goal":     "Validate the fixture.",
		"draft": map[string]any{
			"interview_movement": parts[0] + "-movement",
		},
		"parts":     partValues,
		"movements": movements,
		"policy": map[string]any{
			"allowed_paths": []any{"**"},
			"budget": map[string]any{
				"active_wall_clock_min": float64(10),
			},
		},
	}
}

func e2eCast(bindings map[string]string) map[string]any {
	performers := make(map[string]any, len(bindings))
	bindingValues := make(map[string]any, len(bindings))
	for partID, performerID := range bindings {
		performers[performerID] = map[string]any{
			"adapter": performerID + "-adapter",
			"model":   "model",
		}
		bindingValues[partID] = map[string]any{
			"performer": performerID,
		}
	}
	return map[string]any{
		"cast":       "0.1",
		"performers": performers,
		"bindings":   bindingValues,
	}
}

func replaceEnvironment(
	environment []string,
	replacements map[string]string,
) []string {
	result := make([]string, 0, len(environment)+len(replacements))
	seen := make(map[string]bool, len(replacements))
	for _, entry := range environment {
		name := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			name = entry[:index]
		}
		if value, exists := replacements[name]; exists {
			if !seen[name] {
				result = append(result, name+"="+value)
				seen[name] = true
			}
			continue
		}
		result = append(result, entry)
	}
	for name, value := range replacements {
		if !seen[name] {
			result = append(result, name+"="+value)
		}
	}
	return result
}

func repositoryTree(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(
		root,
		func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if path == root {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			paths = append(paths, relative)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return paths
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func runVendorFixture() {
	for _, argument := range os.Args[1:] {
		if argument == "--version" {
			fmt.Println("codex 9.8.7")
			return
		}
	}
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(91)
	}
	const budgetPrefix = "The remaining active wall-clock budget at attempt start is "
	budgetStart := strings.Index(string(prompt), budgetPrefix)
	if budgetStart < 0 {
		os.Exit(96)
	}
	budgetText := string(prompt)[budgetStart+len(budgetPrefix):]
	budgetParts := strings.SplitN(budgetText, " milliseconds.", 2)
	if len(budgetParts) != 2 {
		os.Exit(96)
	}
	remainingMS, err := strconv.ParseInt(budgetParts[0], 10, 64)
	if err != nil || remainingMS <= 0 || remainingMS > 600000 {
		os.Exit(96)
	}
	outputDir := ""
	for _, line := range strings.Split(string(prompt), "\n") {
		const prefix = "- Writable artifact directory: "
		if strings.HasPrefix(line, prefix) {
			outputDir = strings.TrimSpace(strings.TrimPrefix(line, prefix))
			break
		}
	}
	if outputDir == "" {
		os.Exit(92)
	}
	outcome := os.Getenv(runVendorOutcomeEnvironment)
	if outcome == "task_failed" {
		return
	}
	if outcome == "rate_limited" {
		fmt.Fprintln(os.Stderr, "usage limit reached")
		os.Exit(1)
	}
	if outcome == "block_until_killed" {
		// Announce that the attempt is genuinely mid-execute, then wait to be swept.
		// The marker is what lets the test place a cancellation inside the adapter
		// execute rather than before or after it.
		if err := os.WriteFile(os.Getenv(runVendorMarkerEnvironment), []byte("executing"), 0o600); err != nil {
			os.Exit(94)
		}
		// A bare `select {}` is a deadlock panic once this is the only goroutine, which
		// would fail the attempt before the cancellation could land.
		for {
			time.Sleep(time.Hour)
		}
	}
	if outcome != "" && outcome != "success" && outcome != "read_only_violation" {
		os.Exit(97)
	}
	if outcome == "read_only_violation" {
		if err := os.WriteFile(filepath.Join(outputDir, "..", "worktree", "fixture-untracked.txt"), []byte("mutation\n"), 0o600); err != nil {
			os.Exit(98)
		}
	}
	if err := os.WriteFile(
		filepath.Join(outputDir, "report.txt"),
		[]byte("one movement reached its declared verdict\n"),
		0o600,
	); err != nil {
		os.Exit(93)
	}
	artifacts := []any{map[string]any{"artifact_id": "report", "path": "report.txt"}}
	questions := []any{}
	draftResult := os.Getenv(runVendorDraftResultEnvironment)
	if draftResult != "" {
		artifacts = []any{}
		switch draftResult {
		case "empty":
		case "question":
			questions = []any{map[string]any{"id": "fixture-question", "question": "Which direction should the draft take?"}}
		case "proposal":
		default:
			os.Exit(97)
		}
	}
	if os.Getenv(runVendorContestedEnvironment) == "1" {
		tree, err := exec.Command("git", "rev-parse", "HEAD^{tree}").Output()
		if err != nil {
			os.Exit(93)
		}
		findingID := os.Getenv(runVendorFindingIDEnvironment)
		if findingID == "" {
			findingID = "fixture-blocker"
		}
		findings := fmt.Sprintf(`{"schema":"partitur/findings+json;v=1","subject_tree":"git-sha1:%s","coverage":[{"rubric":"coverage","conclusion":"findings_raised"}],"findings":[{"id":%q,"rubric":"coverage","summary":"fixture blocker","blocking":true,"evidence":[{"path":"partitur.yaml"}]}]}`, strings.TrimSpace(string(tree)), findingID)
		if err := os.WriteFile(filepath.Join(outputDir, "findings.json"), []byte(findings), 0o600); err != nil {
			os.Exit(93)
		}
		artifacts = append(artifacts, map[string]any{"artifact_id": "findings", "path": "findings.json"})
	}
	proposal := any(nil)
	if baseHash := os.Getenv(runVendorProposalBaseHashEnvironment); baseHash != "" {
		proposal = map[string]any{
			"id": "fixture-amendment",
			"amendment": map[string]any{
				"base_revision": float64(1),
				"base_hash":     baseHash,
				"operations": []any{map[string]any{
					"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9),
				}},
				"reason": "fixture amendment",
			},
			"requires_decision": true,
		}
	}
	if draftResult == "proposal" && proposal == nil {
		os.Exit(97)
	}
	result := map[string]any{
		"version":   float64(1),
		"artifacts": artifacts,
		"questions": questions,
		"proposal":  proposal,
		"summary":   "completed",
	}
	data, err := json.Marshal(result)
	if err != nil {
		os.Exit(94)
	}
	if err := os.WriteFile(
		filepath.Join(outputDir, "partitur-result.json"),
		data,
		0o600,
	); err != nil {
		os.Exit(95)
	}
	fmt.Println(`{"type":"fixture.ignored"}`)
	fmt.Println(
		`{"type":"item.started","item":{"type":"command_execution","name":"fixture"}}`,
	)
}

func runScore() map[string]any {
	return map[string]any{
		"score":    "0.2",
		"name":     "run-e2e",
		"revision": float64(1),
		"status":   "finalized",
		"goal":     "Produce one declared report.",
		"verification": map[string]any{
			"expectation": map[string]any{
				"intent": "pass-existing-tests",
				"apply_gate": map[string]any{
					"require": []any{"verified"},
				},
			},
			"final_movement": "inspect",
		},
		"parts": map[string]any{
			"reader": map[string]any{
				"capabilities": []any{
					"repo_read",
					"shell",
					"network",
				},
				"read_only": true,
			},
		},
		"movements": []any{
			map[string]any{
				"id":          "inspect",
				"part":        "reader",
				"grants":      []any{"repo_read", "shell", "network"},
				"instruction": "Write the declared report.",
				"outputs": []any{
					map[string]any{"id": "report", "kind": "artifact"},
				},
				"acceptance": map[string]any{
					"hard": []any{
						map[string]any{
							"id":       "report-present",
							"artifact": "report",
						},
						map[string]any{
							"id":  "command-passes",
							"run": []any{"true"},
						},
					},
				},
			},
		},
		"policy": map[string]any{
			"allowed_paths": []any{"**"},
			"budget": map[string]any{
				"active_wall_clock_min": float64(10),
			},
		},
	}
}

func runCast() map[string]any {
	return map[string]any{
		"cast": "0.1",
		"performers": map[string]any{
			"worker": map[string]any{
				"adapter": "codex",
				"model":   "gpt-5.6-sol",
			},
		},
		"bindings": map[string]any{
			"reader": map[string]any{
				"performer": "worker",
			},
		},
	}
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, data)
	}
}

// runCommandBinaryWithin bounds a subprocess so that a lost guard fails this test by name
// rather than hanging until the package timeout, which hides which test broke.
func runCommandBinaryWithin(
	t *testing.T,
	limit time.Duration,
	binary, repository string,
	environment []string,
	arguments ...string,
) (int, string, string) {
	t.Helper()
	type outcome struct {
		code           int
		stdout, stderr string
	}
	done := make(chan outcome, 1)
	go func() {
		code, stdout, stderr := runCommandBinary(t, binary, repository, environment, arguments...)
		done <- outcome{code: code, stdout: stdout, stderr: stderr}
	}()
	select {
	case result := <-done:
		return result.code, result.stdout, result.stderr
	case <-time.After(limit):
		t.Fatalf("%s did not return within %s: the cancellation never reached it", filepath.Base(binary), limit)
		return 0, "", ""
	}
}

func runCommandBinary(
	t *testing.T,
	binary, repository string,
	environment []string,
	arguments ...string,
) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := exec.Command(binary, arguments...)
	command.Dir = repository
	command.Env = environment
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatal(err)
	}
	return exitError.ExitCode(), stdout.String(), stderr.String()
}

func journalEventTypes(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var result []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		result = append(result, event.Type)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
