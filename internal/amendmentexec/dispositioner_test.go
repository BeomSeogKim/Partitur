package amendmentexec

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/amendment"
	"github.com/BeomSeogKim/Partitur/internal/driver"
	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/recoveryexec"
	"github.com/BeomSeogKim/Partitur/internal/recoveryobs"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	"github.com/BeomSeogKim/Partitur/internal/score"
	"github.com/BeomSeogKim/Partitur/internal/validate"
	"github.com/BeomSeogKim/Partitur/internal/workspace"
)

const (
	prepareCutRepositoryEnvironment = "PARTITUR_PREPARE_CUT_REPOSITORY"
	prepareCutAddressEnvironment    = "PARTITUR_PREPARE_CUT_ADDRESS"
	prepareCutVendorEnvironment     = "PARTITUR_AMENDMENTEXEC_VENDOR"
)

func TestMain(m *testing.M) {
	if os.Getenv(prepareCutVendorEnvironment) == "1" {
		runPrepareCutVendor()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runPrepareCutVendor() {
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
	const outputPrefix = "- Writable artifact directory: "
	for _, line := range strings.Split(string(prompt), "\n") {
		if !strings.HasPrefix(line, outputPrefix) {
			continue
		}
		output := strings.TrimSpace(strings.TrimPrefix(line, outputPrefix))
		if err := os.WriteFile(filepath.Join(output, "report.txt"), []byte("one movement reached its declared verdict\n"), 0o600); err != nil {
			os.Exit(93)
		}
		result := []byte(`{"version":1,"artifacts":[{"artifact_id":"report","path":"report.txt"}],"questions":[],"proposal":null,"summary":"completed"}`)
		if err := os.WriteFile(filepath.Join(output, "partitur-result.json"), result, 0o600); err != nil {
			os.Exit(95)
		}
		fmt.Println(`{"type":"fixture.ignored"}`)
		return
	}
	os.Exit(92)
}

// TestPreparePublicationCutChild is invoked as a subprocess by
// TestPreparePublicationKillCuts. It routes a real adapter proposal, then
// invokes the human approve producer; the receipt observer exits the whole
// approving process after its durable publication and before its next step.
func TestPreparePublicationCutChild(t *testing.T) {
	repository := os.Getenv(prepareCutRepositoryEnvironment)
	address := faultpoint.ReceiptAddress(os.Getenv(prepareCutAddressEnvironment))
	if repository == "" || address == "" {
		return
	}
	preparation, store, authority, started := prepareCutDispositionFixtureAt(t, repository, crashAtReceipt{address: address})
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.RecordRecoveredZeroWriterCandidate(store, authority, input); err != nil {
		t.Fatal(err)
	}
	prepareRoutedHumanApproval(t, store, authority, started.RunID, hash)
	if _, err := fmt.Fprintln(os.Stdout, started.RunID); err != nil {
		t.Fatal(err)
	}
	err = testDispositioner().ApproveRouted(context.Background(), store, started.RunID, "dec-1")
	t.Fatalf("human approve returned after receipt %q: %v", address, err)
}

func prepareRoutedHumanApproval(t *testing.T, store *runstore.Store, authority *runstore.Driver, runID runstate.RunID, hash string) {
	t.Helper()
	disposition, err := testDispositioner().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, runID, hash, true, []any{
		map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if disposition.AppendRoute == nil {
		t.Fatal("human approval crash fixture has no routed proposal")
	}
	appendBlockingProposalSource(t, store, authority, runID, disposition.RouteDescriptor)
	if err := disposition.AppendRoute(context.Background()); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	routed := journal.Events[len(journal.Events)-1]
	payload, err := json.Marshal(map[string]any{
		"decision_id": "dec-1", "decision_type": "amendment", "proposal_id": "prp-1", "routed_reason": "requires_decision", "blocking": true, "emitted_id": "emitted-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Append(runstate.Event{RunID: runID, ScoreRevision: routed.ScoreRevision, MovementID: routed.MovementID, AttemptID: routed.AttemptID, Type: runstate.EventDecisionRequested, Payload: payload}, "fixture.human.decision.requested"); err != nil {
		t.Fatal(err)
	}
}

func TestPreparePublicationKillCuts(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for _, name := range []string{"partitur", "partitur-adapter-codex", "partitur-trampoline"} {
		build := exec.Command("go", "build", "-tags=faultprobe", "-o", filepath.Join(bin, name), "./cmd/"+name)
		build.Dir = root
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, output)
		}
	}
	partitur := filepath.Join(bin, "partitur")
	environment := append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "PARTITUR_CODEX_BIN="+os.Args[0], prepareCutVendorEnvironment+"=1")

	for _, cut := range []struct {
		name        string
		address     faultpoint.ReceiptAddress
		before      func(*testing.T, string, runstate.RunID)
		after       func(*testing.T, string, runstate.RunID)
		wantHead    uint64
		wantPending bool
	}{
		{name: "prepare.snapshot_to_plan/snapshot", address: "amendment.approval.snapshot", before: assertSnapshotCutDurable, after: assertSnapshotCutQuarantined, wantHead: 1},
		{name: "prepare.snapshot_to_plan/plan", address: "amendment.approval.plan", before: assertPlanCutDurable, after: assertSnapshotAndPlanCutRecovered, wantHead: 1},
		{name: "prepare.plan_to_prepared/plan", address: "amendment.approval.plan", before: assertPlanCutDurable, after: assertPlanCutRemoved, wantHead: 1},
		{name: "prepare.plan_to_prepared/prepared", address: "amendment.approval_prepared", before: assertPreparedCutDurable, after: assertPreparedCutRecovered, wantHead: 2},
	} {
		cut := cut
		t.Run(cut.name, func(t *testing.T) {
			repository := t.TempDir()
			runID := killPreparePublicationAtReceipt(t, repository, cut.address)
			cut.before(t, repository, runID)
			assertPrepareCutRecoveryFixedPoint(t, partitur, repository, runID, environment, cut.wantHead, cut.wantPending)
			cut.after(t, repository, runID)
		})
	}
}

type crashAtReceipt struct{ address faultpoint.ReceiptAddress }

func (crash crashAtReceipt) Observed(receipt runstore.DurabilityReceipt) {
	if receipt.Address != crash.address {
		return
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		panic(err)
	}
	// SIGKILL is asynchronous. Returning here lets this goroutine keep running
	// until the kernel reaps the process, so the next durable step can land
	// past the cut and the crash is no longer at the endpoint this record
	// claims. Block instead, and let the signal end the process.
	time.Sleep(time.Hour)
}

func killPreparePublicationAtReceipt(t *testing.T, repository string, address faultpoint.ReceiptAddress) runstate.RunID {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestPreparePublicationCutChild$")
	command.Env = append(os.Environ(), prepareCutRepositoryEnvironment+"="+repository, prepareCutAddressEnvironment+"="+string(address))
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		_ = command.Wait()
		t.Fatalf("cut child published no run id at %q: %v\nstderr:\n%s", address, scanner.Err(), &stderr)
	}
	runID := runstate.RunID(strings.TrimSpace(scanner.Text()))
	if runID == "" {
		_ = command.Wait()
		t.Fatalf("cut child published an empty run id at %q\nstderr:\n%s", address, &stderr)
	}
	if err := command.Wait(); err == nil {
		t.Fatalf("cut child at %q exited successfully\nstderr:\n%s", address, &stderr)
	}
	return runID
}

func assertSnapshotCutDurable(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	path := filepath.Join(repository, ".partitur", "runs", string(runID), "scores", "revision-2.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot cut did not leave its durable snapshot: %v", err)
	}
}

func assertPlanCutDurable(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	plans := filepath.Join(repository, ".partitur", "runs", string(runID), "prepares")
	entries, err := os.ReadDir(plans)
	if err != nil || len(entries) != 1 {
		t.Fatalf("plan cut entries=%v err=%v, want one orphan plan", entries, err)
	}
}

func assertPreparedCutDurable(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.PendingPrepare == nil {
		t.Fatal("approval_prepared receipt did not leave a pending prepare")
	}
}

func assertSnapshotCutQuarantined(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	runRoot := filepath.Join(repository, ".partitur", "runs", string(runID))
	if _, err := os.Stat(filepath.Join(runRoot, "scores", "revision-2.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreferenced snapshot still occupies its head path: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(runRoot, "quarantine", "unreferenced_prepare_snapshot", "*", "revision-2.yaml"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("snapshot quarantine entries=%v err=%v, want one", matches, err)
	}
}

func assertPlanCutRemoved(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	runRoot := filepath.Join(repository, ".partitur", "runs", string(runID))
	entries, err := os.ReadDir(filepath.Join(runRoot, "prepares"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("orphan plan remains after recovery: %v", entries)
	}
	matches, err := filepath.Glob(filepath.Join(runRoot, "quarantine", "*", "*", "*.json"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("orphan plan was quarantined instead of removed: %v err=%v", matches, err)
	}
}

func assertSnapshotAndPlanCutRecovered(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	assertSnapshotCutQuarantined(t, repository, runID)
	assertPlanCutRemoved(t, repository, runID)
}

func assertPreparedCutRecovered(t *testing.T, repository string, runID runstate.RunID) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(repository, ".partitur", "runs", string(runID), "prepares"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("prepared plan remains after recovery: %v", entries)
	}
}

func assertPrepareCutRecoveryFixedPoint(t *testing.T, partitur, repository string, runID runstate.RunID, environment []string, wantHead uint64, wantPending bool) {
	t.Helper()
	run := func() int {
		command := exec.Command(partitur, "resume", string(runID))
		command.Dir = repository
		command.Env = environment
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if stdout.Len() != 0 || stderr.Len() != 0 {
			journal, _ := os.ReadFile(filepath.Join(repository, ".partitur", "runs", string(runID), "journal.jsonl"))
			t.Fatalf("resume stdout=%q stderr=%q journal=%s", stdout.String(), stderr.String(), journal)
		}
		if err == nil {
			return 0
		}
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		return exit.ExitCode()
	}
	if code := run(); code != 0 && code != 4 {
		t.Fatalf("first resume exit=%d, want 0 or 4", code)
	}
	first, err := os.ReadFile(filepath.Join(repository, ".partitur", "runs", string(runID), "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if code := run(); code != 0 && code != 4 {
		t.Fatalf("fixed-point resume exit=%d, want 0 or 4", code)
	}
	second, err := os.ReadFile(filepath.Join(repository, ".partitur", "runs", string(runID), "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("fixed-point recovery appended another durable event")
	}
	input, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := input.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Projection.State.ScoreHead.Revision != wantHead || (loaded.Projection.State.PendingPrepare != nil) != wantPending {
		t.Fatalf("recovery state=%+v, want head revision %d pending prepare=%t", loaded.Projection.State, wantHead, wantPending)
	}
}

func TestDispositionerRejectsBlockingProposalBeforeAttemptBlocked(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	disposition, err := testDispositioner().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, true, []any{map[string]any{"op": "replace", "path": "/revision", "value": float64(2)}}))
	if err != nil {
		t.Fatal(err)
	}
	if disposition.RouteDescriptor != nil || disposition.AppendRoute != nil {
		t.Fatalf("rejected disposition = %#v", disposition)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	last := journal.Events[len(journal.Events)-1]
	if last.Type != runstate.EventAmendmentRejected {
		t.Fatalf("last event = %s, want amendment.rejected before attempt.blocked", last.Type)
	}
	payload := eventPayload(t, last)
	if payload["decision_id"] != "dec-1" || payload["reason"] != "reserved_field" {
		t.Fatalf("rejection payload = %#v", payload)
	}
	for _, path := range []string{"scores/revision-2.yaml", "prepares"} {
		if _, err := os.Stat(filepath.Join(preparation.RepositoryRoot, ".partitur", "runs", string(started.RunID), path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rejection reserved preparation path %s: %v", path, err)
		}
	}
}

func TestDispositionerPublishesFrozenRouteThenAppendsItAfterDriverSource(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	disposition, err := testDispositioner().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, true, []any{map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)}}))
	if err != nil {
		t.Fatal(err)
	}
	route := disposition.RouteDescriptor
	if route == nil || route["reason"] != "requires_decision" || route["decision_type"] != "amendment" || route["proposal_record_hash"] == "" {
		t.Fatalf("route descriptor = %#v", route)
	}
	if disposition.AppendRoute == nil {
		t.Fatal("routed disposition has no append closure")
	}
	appendBlockingProposalSource(t, store, authority, started.RunID, disposition.RouteDescriptor)
	if err := disposition.AppendRoute(context.Background()); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	last := journal.Events[len(journal.Events)-1]
	if last.Type != runstate.EventAmendmentRoutedHuman {
		t.Fatalf("last event = %s, want amendment.routed_human", last.Type)
	}
	payload := eventPayload(t, last)
	if payload["decision_id"] != "dec-1" || payload["proposal_record_hash"] != route["proposal_record_hash"] || payload["reason"] != "requires_decision" {
		t.Fatalf("routed payload = %#v", payload)
	}
	record := filepath.Join(preparation.RepositoryRoot, ".partitur", "runs", string(started.RunID), "proposals", "prp-1.json")
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("published proposal record: %v", err)
	}
}

func TestSubmitCLIHoldsPublicationIntervalAgainstConcurrentRecoveryCleanup(t *testing.T) {
	receipts := &receiptRecorder{}
	preparation, store, authority, started := dispositionFixtureWithReceiptObserver(t, receipts)
	if err := authority.Release(); err != nil {
		t.Fatal(err)
	}
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}

	published := make(chan struct{})
	releasePublisher := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releasePublisher) }) }
	t.Cleanup(release)
	dispositioner := testDispositioner()
	dispositioner.afterCLIProposalRecordPublication = func() {
		close(published)
		<-releasePublisher
	}
	type submissionResult struct {
		result CLIResult
		err    error
	}
	submitted := make(chan submissionResult, 1)
	go func() {
		result, err := dispositioner.SubmitCLI(context.Background(), store, CLIProposal{
			RunID: started.RunID, BaseRevision: 1, BaseHash: runstate.Hash(hash),
			Operations: []byte(`[{"op":"replace","path":"/goal","value":"needs-review"}]`), Reason: "publication interval",
		})
		submitted <- submissionResult{result: result, err: err}
	}()

	select {
	case <-published:
	case <-time.After(5 * time.Second):
		t.Fatal("CLI publisher did not reach the proposal-record publication interval")
	}
	if receipts.index("proposal.record.published") == -1 {
		t.Fatal("proposal.record.published receipt was not observed before the publication interval")
	}
	if receipts.index("amendment.routed_human") != -1 {
		t.Fatal("amendment.routed_human was observed before the publication interval released")
	}

	cleanupProbe := newLockAcquisitionProbe("test.recovery.cleanup_proposal_records.lock")
	contender, err := runstore.New(preparation.RepositoryRoot, cleanupProbe)
	if err != nil {
		t.Fatal(err)
	}
	cleanupAttempted := make(chan struct{})
	cleanupDone := make(chan error, 1)
	go func() {
		close(cleanupAttempted)
		cleanupDone <- contender.Mutate(started.RunID, cleanupProbe.point, func(transaction *runstore.Txn) error {
			return transaction.At("recovery.cleanup_proposal_records").QuarantineUnreferencedProposalRecords()
		})
	}()
	<-cleanupAttempted

	select {
	case <-cleanupProbe.acquired:
		t.Fatal("recovery cleanup acquired the state lock while the CLI publisher held its publication interval")
	case <-time.After(200 * time.Millisecond):
	}

	release()
	var submittedResult submissionResult
	select {
	case submittedResult = <-submitted:
	case <-time.After(5 * time.Second):
		t.Fatal("CLI publisher did not finish after the publication interval released")
	}
	if submittedResult.err != nil {
		t.Fatal(submittedResult.err)
	}
	if submittedResult.result.Outcome.Kind != amendment.Routed {
		t.Fatalf("CLI outcome = %q, want routed", submittedResult.result.Outcome.Kind)
	}
	select {
	case <-cleanupProbe.acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery cleanup did not acquire the state lock after the publication interval released")
	}
	select {
	case err := <-cleanupDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovery cleanup did not finish after the publication interval released")
	}

	record := filepath.Join(preparation.RepositoryRoot, ".partitur", "runs", string(started.RunID), "proposals", string(submittedResult.result.ProposalID)+".json")
	if _, err := os.Stat(record); err != nil {
		t.Fatalf("recovery cleanup removed live publisher's proposal record: %v", err)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	routed, requested := 0, 0
	for _, event := range journal.Events {
		switch event.Type {
		case runstate.EventAmendmentRoutedHuman:
			routed++
		case runstate.EventDecisionRequested:
			requested++
		}
	}
	if routed != 1 || requested != 1 {
		t.Fatalf("routed=%d decision_requested=%d, want one of each", routed, requested)
	}
	if publication, routed := receipts.index("proposal.record.published"), receipts.index("amendment.routed_human"); publication == -1 || routed == -1 || publication >= routed {
		t.Fatalf("receipt order publication=%d routed=%d, want distinct publication before route", publication, routed)
	}
}

func TestSubmitCLIPreservesRejectedAndApprovedOutcomes(t *testing.T) {
	t.Run("rejected", func(t *testing.T) {
		preparation, store, authority, started := dispositionFixture(t)
		defer authority.Release()
		hash, err := preparation.Score.Hash()
		if err != nil {
			t.Fatal(err)
		}
		result, err := testDispositioner().SubmitCLI(context.Background(), store, CLIProposal{
			RunID: started.RunID, BaseRevision: 1, BaseHash: runstate.Hash(hash),
			Operations: []byte(`[{"op":"replace","path":"/goal","value":"goal"}]`), Reason: "no-op CLI amendment",
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome.Kind != amendment.Rejected || result.Outcome.Reason != "no_op" {
			t.Fatalf("CLI rejection outcome = %+v, want no_op rejection", result.Outcome)
		}
		journal, err := store.ReadJournal(started.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if last := journal.Events[len(journal.Events)-1]; last.Type != runstate.EventAmendmentRejected {
			t.Fatalf("last event = %s, want amendment.rejected", last.Type)
		}
	})

	t.Run("approved", func(t *testing.T) {
		preparation, store, authority, started := dispositionFixture(t)
		if err := authority.Release(); err != nil {
			t.Fatal(err)
		}
		hash, err := preparation.Score.Hash()
		if err != nil {
			t.Fatal(err)
		}
		result, err := testDispositioner().SubmitCLI(context.Background(), store, CLIProposal{
			RunID: started.RunID, BaseRevision: 1, BaseHash: runstate.Hash(hash),
			Operations: []byte(`[{"op":"replace","path":"/policy/budget/active_wall_clock_min","value":9}]`), Reason: "approved CLI amendment",
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome.Kind != amendment.Approved {
			t.Fatalf("CLI approval outcome = %+v, want approved", result.Outcome)
		}
		input, err := store.LoadRunInput(started.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if input.Projection.State.ScoreHead.Revision != 2 || input.Projection.State.PendingPrepare != nil {
			t.Fatalf("CLI approval state = %+v, want revision 2 with no pending prepare", input.Projection.State)
		}
	})
}

func TestDispositionerAppendsRoutedHumanAfterDriverBlockedSource(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	attempt, err := started.Run.CreateAttempt("inspect")
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []runstate.EventType{runstate.EventMovementReady, runstate.EventMovementStarted} {
		if _, err := authority.Append(runstate.Event{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: eventType, Payload: []byte(`{}`)}, faultpoint.ReceiptAddress("test."+string(eventType))); err != nil {
			t.Fatal(err)
		}
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	result := driver.ExecuteAttempt(context.Background(), driver.AttemptExecution{
		RepositoryRoot: preparation.RepositoryRoot, Score: input.Score, Cast: input.Cast, RunID: started.RunID,
		Attempt: attempt, BaseTree: input.BaseTree, CandidateTree: input.BaseTree, Authority: authority,
		PerformerID: "worker", SelectionReason: "initial", RemainingMS: input.Projection.Scheduler.RemainingTime,
	}, driver.ExecutionDependencies{Probe: faultpoint.Nop{}, Client: waitingProposalExecutor{t: t, baseHash: hash, requiresDecision: true}, ResolveTrampoline: func() (string, error) { return "/fixture/trampoline", nil }, Now: time.Now, NewID: workspace.NewID, ProposalDisposition: testDispositioner()})
	if result.Outcome != driver.OutcomeWaitingHuman || result.Err != nil {
		t.Fatalf("execute result = %+v", result)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Events) < 3 ||
		journal.Events[len(journal.Events)-3].Type != runstate.EventAttemptBlocked ||
		journal.Events[len(journal.Events)-2].Type != runstate.EventAmendmentRoutedHuman ||
		journal.Events[len(journal.Events)-1].Type != runstate.EventDecisionRequested {
		t.Fatalf("terminal ordering = %#v", journal.Events[len(journal.Events)-3:])
	}
}

func TestDispositionerPreparesAutoApproval(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	disposition, err := testDispositioner().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, false, []any{map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)}}))
	if err != nil {
		t.Fatal(err)
	}
	if disposition.PreparedReceipt == nil || disposition.PreparedReceipt.Mutation.EventType != string(runstate.EventAmendmentApprovalPrepared) {
		t.Fatalf("auto disposition = %#v, want approval_prepared receipt", disposition)
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.PendingPrepare == nil {
		t.Fatal("auto approval did not leave a pending prepare")
	}
	prepare := input.Projection.State.PendingPrepare
	planBytes, err := os.ReadFile(filepath.Join(preparation.RepositoryRoot, ".partitur", "runs", string(started.RunID), "prepares", string(prepare.ID)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := runstate.DecodeApprovalPlan(planBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.MatchesPrepare(*prepare) {
		t.Fatalf("plan does not match pending prepare: plan=%+v prepare=%+v", plan, *prepare)
	}
	snapshot, err := os.ReadFile(filepath.Join(preparation.RepositoryRoot, ".partitur", "runs", string(started.RunID), "scores", "revision-2.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	compiled, diagnostics := score.Compile(snapshot)
	if len(diagnostics) != 0 || compiled.Revision() != 2 {
		t.Fatalf("prepared snapshot = revision %d diagnostics %v\n%s", compiled.Revision(), diagnostics, snapshot)
	}
	_, err = testDispositioner().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, false, []any{map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)}}))
	if !errors.Is(err, ErrPreparePending) {
		t.Fatalf("second auto prepare error = %v, want ErrPreparePending", err)
	}
}

func TestApproveRoutedPreparesHumanApprovalFromImmutableRecord(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	dispositioner := testDispositioner()
	disposition, err := dispositioner.PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, true, []any{
		map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if disposition.AppendRoute == nil {
		t.Fatal("routed proposal has no route append")
	}
	appendBlockingProposalSource(t, store, authority, started.RunID, disposition.RouteDescriptor)
	if err := disposition.AppendRoute(context.Background()); err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	routed := journal.Events[len(journal.Events)-1]
	requestPayload, err := json.Marshal(map[string]any{
		"decision_id": "dec-1", "decision_type": "amendment", "proposal_id": "prp-1", "routed_reason": "requires_decision", "blocking": true, "emitted_id": "emitted-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Append(runstate.Event{RunID: started.RunID, ScoreRevision: routed.ScoreRevision, MovementID: routed.MovementID, AttemptID: routed.AttemptID, Type: runstate.EventDecisionRequested, Payload: requestPayload}, "test.human.decision.requested"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- dispositioner.ApproveRouted(ctx, store, started.RunID, "dec-1") }()
	var prepare runstate.PendingPrepare
	deadline := time.After(5 * time.Second)
	for {
		input, err := store.LoadRunInput(started.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if input.Projection.State.PendingPrepare != nil {
			prepare = *input.Projection.State.PendingPrepare
			break
		}
		select {
		case err := <-done:
			t.Fatalf("approve returned before preparing: %v", err)
		case <-deadline:
			t.Fatal("approve did not append a human prepare")
		case <-time.After(10 * time.Millisecond):
		}
	}
	journal, err = store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	prepared := journal.Events[len(journal.Events)-1]
	if prepared.Type != runstate.EventAmendmentApprovalPrepared {
		t.Fatalf("prepared event = %s, want amendment.approval_prepared", prepared.Type)
	}
	var payload map[string]any
	if err := json.Unmarshal(prepared.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["mode"] != "human" || payload["decision_id"] != "dec-1" {
		t.Fatalf("human prepare payload = %#v", payload)
	}
	if _, present := payload["envelope_class"]; present {
		t.Fatalf("human prepare carries envelope_class: %#v", payload)
	}
	planBytes, err := os.ReadFile(filepath.Join(preparation.RepositoryRoot, ".partitur", "runs", string(started.RunID), "prepares", string(prepare.ID)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := runstate.DecodeApprovalPlan(planBytes)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != "human" || plan.DecisionID == nil || *plan.DecisionID != "dec-1" || plan.EnvelopeClass != nil || plan.EnvelopeEvaluation == nil {
		t.Fatalf("human approval plan = %+v", plan)
	}
	if plan.EnvelopeEvaluation["guard_passed"] != true {
		t.Fatalf("human approval plan envelope evaluation = %#v", plan.EnvelopeEvaluation)
	}
	if len(plan.ObsoletedDecisionIDs) != 0 {
		t.Fatalf("human approval plan obsoletes its own decision: %+v", plan.ObsoletedDecisionIDs)
	}
	if err := store.AcknowledgePrepare(ctx, authority, prepare.ID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("approve after quiesce acknowledgement: %v", err)
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.PendingPrepare != nil || input.Projection.State.ScoreHead.Revision != 2 {
		t.Fatalf("human approval state = %+v", input.Projection.State)
	}
	journal, err = store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	approved := journal.Events[len(journal.Events)-1]
	if approved.Type != runstate.EventAmendmentApproved {
		t.Fatalf("terminal approval event = %s", approved.Type)
	}
	if err := json.Unmarshal(approved.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["mode"] != "human" || payload["decision_id"] != "dec-1" || payload["envelope_evaluation"] == nil {
		t.Fatalf("human approval payload = %#v", payload)
	}
	if obsoleted, ok := payload["obsoleted_decision_ids"].([]any); !ok || len(obsoleted) != 0 {
		t.Fatalf("human approval obsoleted decisions = %#v, want none", payload["obsoleted_decision_ids"])
	}
	if _, present := payload["envelope_class"]; present {
		t.Fatalf("human approval carries envelope_class: %#v", payload)
	}
}

func TestApproveRoutedRejectsTamperedImmutableRecord(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	prepareRoutedHumanApproval(t, store, authority, started.RunID, hash)
	path := filepath.Join(preparation.RepositoryRoot, ".partitur", "runs", string(started.RunID), "proposals", "prp-1.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(contents, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := testDispositioner().ApproveRouted(ctx, store, started.RunID, "dec-1"); !errors.Is(err, runstore.ErrDecisionResolutionNotAllowed) {
		t.Fatalf("tampered record error = %v, want decision resolution refusal", err)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.Type == runstate.EventAmendmentApprovalPrepared {
			t.Fatalf("tampered record produced a prepare: %s", event.Payload)
		}
	}
}

func TestApproveRoutedRequiresLiveRoutedDecision(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	disposition, err := testDispositioner().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, true, []any{
		map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if disposition.AppendRoute == nil {
		t.Fatal("routed proposal has no route append")
	}
	appendBlockingProposalSource(t, store, authority, started.RunID, disposition.RouteDescriptor)
	if err := disposition.AppendRoute(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := testDispositioner().ApproveRouted(ctx, store, started.RunID, "dec-1"); !errors.Is(err, runstore.ErrDecisionResolutionNotAllowed) {
		t.Fatalf("approval without pending routed decision = %v, want decision resolution refusal", err)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.Type == runstate.EventAmendmentApprovalPrepared {
			t.Fatalf("missing routed decision produced a prepare: %s", event.Payload)
		}
	}
}

func TestApproveRoutedAppendsDecisionTimeRejection(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	prepareRoutedHumanApproval(t, store, authority, started.RunID, hash)
	if err := store.RequestCancellation(started.RunID); err != nil {
		t.Fatal(err)
	}
	err = testDispositioner().ApproveRouted(context.Background(), store, started.RunID, "dec-1")
	var rejected *DecisionRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != "run_cancelling" {
		t.Fatalf("decision-time result = %v, want run_cancelling rejection", err)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	last := journal.Events[len(journal.Events)-1]
	if last.Type != runstate.EventAmendmentRejected {
		t.Fatalf("decision-time terminal = %s, want amendment.rejected", last.Type)
	}
	var payload map[string]any
	if err := json.Unmarshal(last.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["decision_id"] != "dec-1" || payload["reason"] != "run_cancelling" {
		t.Fatalf("decision-time rejection payload = %#v", payload)
	}
}

func TestEnvelopeEvaluationForPreservesDecisionTimeGuardReason(t *testing.T) {
	evaluation := envelopeEvaluationFor(amendment.Outcome{Reason: "unclassified_change", GuardPass: false})
	if evaluation["guard_passed"] != false || evaluation["guard_failure_reason"] != "unclassified_change" {
		t.Fatalf("envelope evaluation=%#v", evaluation)
	}
}

func TestDispositionerReceiptObserverBracketsPreparePublications(t *testing.T) {
	for _, cut := range []struct {
		name    string
		address faultpoint.ReceiptAddress
	}{
		{name: "snapshot", address: "amendment.approval.snapshot"},
		{name: "plan", address: "amendment.approval.plan"},
	} {
		t.Run(cut.name, func(t *testing.T) {
			gate := newReceiptPauseGate(cut.address)
			t.Cleanup(gate.Release)
			preparation, store, authority, started := dispositionFixtureWithReceiptObserver(t, gate)
			defer authority.Release()
			hash, err := preparation.Score.Hash()
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				_, err := testDispositioner().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, false, []any{map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)}}))
				result <- err
			}()

			receipt := gate.Wait(t)
			if receipt.Mutation.Kind != faultpoint.FilePublication {
				t.Fatalf("receipt kind = %q, want file publication", receipt.Mutation.Kind)
			}
			snapshot := filepath.Join(preparation.RepositoryRoot, ".partitur", "runs", string(started.RunID), "scores", "revision-2.yaml")
			plans := filepath.Join(preparation.RepositoryRoot, ".partitur", "runs", string(started.RunID), "prepares")
			if _, err := os.Stat(snapshot); err != nil {
				t.Fatalf("snapshot is not durable at %s receipt: %v", cut.name, err)
			}
			if cut.address == "amendment.approval.snapshot" {
				entries, err := os.ReadDir(plans)
				if !errors.Is(err, os.ErrNotExist) || len(entries) != 0 {
					t.Fatalf("plan entries at snapshot receipt = %v, read error = %v", entries, err)
				}
			} else if entries, err := os.ReadDir(plans); err != nil || len(entries) != 1 {
				t.Fatalf("plan entries at plan receipt = %v, read error = %v", entries, err)
			}
			journal, err := store.ReadJournal(started.RunID)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range journal.Events {
				if event.Type == runstate.EventAmendmentApprovalPrepared {
					t.Fatalf("approval_prepared exists at %s receipt", cut.name)
				}
			}
			select {
			case err := <-result:
				t.Fatalf("disposition returned while paused at %s: %v", cut.name, err)
			default:
			}

			gate.Release()
			select {
			case err := <-result:
				if err != nil {
					t.Fatalf("disposition after %s release: %v", cut.name, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("disposition remained paused after %s release", cut.name)
			}
		})
	}
}

func TestDispositionerUsesFixedQuiesceSilenceLimit(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = New().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, false, []any{map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)}})); err != nil {
		t.Fatal(err)
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.PendingPrepare == nil || input.Projection.State.PendingPrepare.QuiesceSilenceLimitMS != quiesceSilenceLimitMillis {
		t.Fatalf("fixed-silence prepare = %+v", input.Projection.State.PendingPrepare)
	}
}

func TestDriverAutoApprovalCommitsAndContinuesWithoutWaitingHuman(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	attempt, err := started.Run.CreateAttempt("inspect")
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []runstate.EventType{runstate.EventMovementReady, runstate.EventMovementStarted} {
		if _, err := authority.Append(runstate.Event{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: eventType, Payload: []byte(`{}`)}, faultpoint.ReceiptAddress("test."+string(eventType))); err != nil {
			t.Fatal(err)
		}
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.RecordRecoveredZeroWriterCandidate(store, authority, input); err != nil {
		t.Fatal(err)
	}
	input, err = store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	client := &autoThenInterruptedExecutor{first: waitingProposalExecutor{t: t, baseHash: hash}}
	result := driver.ExecuteAttempt(context.Background(), driver.AttemptExecution{
		RepositoryRoot: preparation.RepositoryRoot, Score: input.Score, Cast: input.Cast, RunID: started.RunID,
		Attempt: attempt, BaseTree: input.BaseTree, CandidateTree: input.BaseTree, Authority: authority,
		PerformerID: "worker", SelectionReason: "initial", RemainingMS: input.Projection.Scheduler.RemainingTime,
	}, driver.ExecutionDependencies{Probe: faultpoint.Nop{}, Client: client, ResolveTrampoline: func() (string, error) { return "/fixture/trampoline", nil }, Now: time.Now, NewID: workspace.NewID, ProposalDisposition: testDispositioner()})
	if result.Outcome != driver.OutcomeInterrupted || !errors.Is(result.Err, errContinuedAutoApproval) || client.calls != 2 {
		t.Fatalf("execute result = %+v calls=%d, want second revision attempt interruption", result, client.calls)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var rounds []uint64
	for _, event := range journal.Events {
		if event.Type != runstate.EventAmendmentQuiesceObserved {
			continue
		}
		payload := eventPayload(t, event)
		rounds = append(rounds, uint64(payload["sweep_round"].(float64)))
	}
	if !reflect.DeepEqual(rounds, []uint64{1, 2}) {
		t.Fatalf("auto prepare quiesce receipts = %v, want [1 2]", rounds)
	}
	quiesced, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if quiesced.Projection.State.PendingPrepare != nil || quiesced.Projection.State.ScoreHead.Revision != 2 {
		t.Fatalf("auto approval state = %+v, want committed revision 2", quiesced.Projection.State)
	}
	journal, err = store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.Type == runstate.EventAmendmentApproved {
			if _, present := eventPayload(t, event)["fenced_epoch"]; present {
				t.Fatalf("matching-sidecar approval unexpectedly fenced: %+v", event)
			}
		}
		if event.Type == runstate.EventPerformerSelected && event.ScoreRevision == 2 {
			payload := eventPayload(t, event)
			if payload["reason"] != "revision_restart" {
				t.Fatalf("revision-2 selection = %#v", payload)
			}
		}
	}
}

func TestDriverAutoApprovalFreshAcquisitionFailureKeepsCommittedApprovalForResume(t *testing.T) {
	preparation, store, authority, started, attempt, input, hash := autoApprovalAttemptFixture(t)
	defer authority.Release()
	acquireErr := errors.New("fresh driver acquisition failed")
	result := driver.ExecuteAttempt(context.Background(), driver.AttemptExecution{
		RepositoryRoot: preparation.RepositoryRoot, Score: input.Score, Cast: input.Cast, RunID: started.RunID,
		Attempt: attempt, BaseTree: input.BaseTree, CandidateTree: input.BaseTree, Authority: authority,
		PerformerID: "worker", SelectionReason: "initial", RemainingMS: input.Projection.Scheduler.RemainingTime,
	}, driver.ExecutionDependencies{
		Probe: faultpoint.Nop{}, Client: waitingProposalExecutor{t: t, baseHash: hash},
		ResolveTrampoline: func() (string, error) { return "/fixture/trampoline", nil }, Now: time.Now, NewID: workspace.NewID,
		ProposalDisposition: testDispositioner(),
		AcquireDriver: func(*runstore.Store, runstate.RunID, []runstate.MovementSeed) (*runstore.Driver, error) {
			return nil, acquireErr
		},
	})
	if result.Outcome != driver.OutcomeInterrupted || !errors.Is(result.Err, acquireErr) {
		t.Fatalf("execute result = %+v, want interrupted fresh-acquisition failure", result)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if last := journal.Events[len(journal.Events)-1]; last.Type != runstate.EventAmendmentApproved {
		t.Fatalf("journal head = %s, want durable amendment.approved after fresh-acquisition failure", last.Type)
	}
	committed, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Projection.State.PendingPrepare != nil || committed.Projection.State.ScoreHead.Revision != 2 {
		t.Fatalf("post-acquisition-failure state = %+v, want committed revision 2", committed.Projection.State)
	}

	_, _ = recoveryExecutor(store, started.RunID).Execute(context.Background())
	journal, err = store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRevisionRestartSelection(t, journal.Events, 2) {
		t.Fatal("resume did not perform the committed approval's revision-restart continuation")
	}
}

func TestDriverAutoApprovalCancellationAfterCommitBeforeFreshAcquisition(t *testing.T) {
	preparation, store, authority, started, attempt, input, hash := autoApprovalAttemptFixture(t)
	defer authority.Release()
	acknowledged := false
	result := driver.ExecuteAttempt(context.Background(), driver.AttemptExecution{
		RepositoryRoot: preparation.RepositoryRoot, Score: input.Score, Cast: input.Cast, RunID: started.RunID,
		Attempt: attempt, BaseTree: input.BaseTree, CandidateTree: input.BaseTree, Authority: authority,
		PerformerID: "worker", SelectionReason: "initial", RemainingMS: input.Projection.Scheduler.RemainingTime,
	}, driver.ExecutionDependencies{
		Probe: faultpoint.Nop{}, Client: waitingProposalExecutor{t: t, baseHash: hash},
		ResolveTrampoline: func() (string, error) { return "/fixture/trampoline", nil }, Now: time.Now, NewID: workspace.NewID,
		ProposalDisposition:      testDispositioner(),
		AfterPrepareAcknowledged: func() { acknowledged = true },
		AcquireDriver: func(store *runstore.Store, runID runstate.RunID, seeds []runstate.MovementSeed) (*runstore.Driver, error) {
			if !acknowledged {
				t.Fatal("fresh acquisition began before the acknowledged-prepare seam")
			}
			if err := store.RequestCancellation(runID); err != nil {
				t.Fatal(err)
			}
			return store.AcquireDriver(runID, seeds)
		},
	})
	if result.Outcome != driver.OutcomeCancelled || result.Err != nil {
		t.Fatalf("execute result = %+v, want cancellation after committed approval", result)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	approved, cancelled := -1, -1
	for index, event := range journal.Events {
		switch event.Type {
		case runstate.EventAmendmentApproved:
			approved = index
		case runstate.EventRunCancelled:
			cancelled = index
		}
	}
	if approved < 0 || cancelled <= approved {
		t.Fatalf("approval/cancellation order = approved:%d cancelled:%d, want committed approval before cancellation", approved, cancelled)
	}
	if hasRevisionRestartSelection(t, journal.Events, 2) {
		t.Fatal("cancellation after commit continued into revision restart")
	}
}

func TestRecoveryCommitsPrepareAfterCrashBetweenLeaseMoveAndCommit(t *testing.T) {
	preparation, store, authority, started, attempt, input, hash := autoApprovalAttemptFixture(t)
	defer authority.Release()
	crash := errors.New("crash after lease move")
	func() {
		defer func() {
			if recovered := recover(); recovered != crash {
				t.Fatalf("panic = %#v, want crash after lease move", recovered)
			}
		}()
		_ = driver.ExecuteAttempt(context.Background(), driver.AttemptExecution{
			RepositoryRoot: preparation.RepositoryRoot, Score: input.Score, Cast: input.Cast, RunID: started.RunID,
			Attempt: attempt, BaseTree: input.BaseTree, CandidateTree: input.BaseTree, Authority: authority,
			PerformerID: "worker", SelectionReason: "initial", RemainingMS: input.Projection.Scheduler.RemainingTime,
		}, driver.ExecutionDependencies{
			Probe: faultpoint.Nop{}, Client: waitingProposalExecutor{t: t, baseHash: hash},
			ResolveTrampoline: func() (string, error) { return "/fixture/trampoline", nil }, Now: time.Now, NewID: workspace.NewID,
			ProposalDisposition: testDispositioner(), AfterPrepareAcknowledged: func() { panic(crash) },
		})
	}()
	precrash, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if precrash.Projection.State.PendingPrepare == nil {
		t.Fatal("crash did not retain the pending prepare for RC-RESUME-007")
	}
	prepareID := precrash.Projection.State.PendingPrepare.ID
	planBytes, err := os.ReadFile(filepath.Join(preparation.RepositoryRoot, ".partitur", "runs", string(started.RunID), "prepares", string(prepareID)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := runstate.DecodeApprovalPlan(planBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recoveryExecutor(store, started.RunID).Execute(context.Background()); err == nil {
		t.Fatal("recovery did not reach the recovered continuation adapter boundary")
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range journal.Events {
		if event.Type != runstate.EventAmendmentApproved {
			continue
		}
		want, err := plan.ApprovedPayload(nil)
		if err != nil {
			t.Fatal(err)
		}
		if string(event.Payload) != string(want) {
			t.Fatalf("recovered approval payload = %s, want prepared unfenced plan %s", event.Payload, want)
		}
		if _, fenced := eventPayload(t, event)["fenced_epoch"]; fenced {
			t.Fatalf("recovered approval unexpectedly fenced: %+v", event)
		}
		return
	}
	t.Fatal("RC-RESUME-007 did not commit amendment.approved after the lease-move crash")
}

func TestDispositionerBarrierClosesEachCataloguedConsequenceBeforePrepare(t *testing.T) {
	store, authority, started := interruptedBlockingProposalFixture(t)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := input.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	disposition, err := testDispositioner().PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, false, []any{map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)}}))
	if err != nil {
		t.Fatal(err)
	}
	if disposition.PreparedReceipt == nil {
		t.Fatalf("disposition = %#v, want prepared receipt", disposition)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	_, routed, requested := blockingRouteEvents(t, journal.Events)
	if routed.Type != runstate.EventAmendmentRoutedHuman || requested.Type != runstate.EventDecisionRequested {
		t.Fatalf("barrier did not close route then request: routed=%+v requested=%+v", routed, requested)
	}
	if last := journal.Events[len(journal.Events)-1]; last.Type != runstate.EventAmendmentApprovalPrepared {
		t.Fatalf("last event = %s, want approval prepare after barrier closure", last.Type)
	}
}

func TestDispositionerBarrierLimitRejectsASecondSelectedConsequence(t *testing.T) {
	store, authority, started := interruptedBlockingProposalFixture(t)
	defer authority.Release()
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := input.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	dispositioner := testDispositioner()
	dispositioner.barrierLimit = 1
	_, err = dispositioner.PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, false, []any{map[string]any{"op": "replace", "path": "/policy/budget/active_wall_clock_min", "value": float64(9)}}))
	if !errors.Is(err, ErrBarrierDidNotConverge) {
		t.Fatalf("barrier error = %v, want ErrBarrierDidNotConverge", err)
	}
}

func TestDispositionerRejectsBarrierConsequenceWithoutDurableEffect(t *testing.T) {
	_, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	before, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	decision := recovery.Decision{CaseID: recovery.CaseRealizeDisposition, Action: &recovery.Action{
		Kind:                recovery.ActionRealizeRecordedDisposition,
		RecordedDisposition: &runstate.Disposition{Charged: "quality_retry"},
		PendingSuccessor:    &recovery.PendingSuccessor{MovementID: "inspect", AttemptID: "retry-1", Performer: "worker", Reason: "quality_retry"},
	}}
	open, err := testDispositioner().applyBarrierDecision(context.Background(), amendmentProposal{AdapterProposal: driver.AdapterProposal{Store: store, Authority: authority, RunID: started.RunID}}, recovery.Input{}, decision)
	if open || !errors.Is(err, ErrBarrierConsequenceNoEffect) {
		t.Fatalf("barrier result open=%t error=%v, want no-effect refusal", open, err)
	}
	after, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) != len(before.Events) {
		t.Fatalf("no-effect barrier appended %d events", len(after.Events)-len(before.Events))
	}
}

func TestRebuildFinalizationRefusesStaleIneligibleSelection(t *testing.T) {
	_, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	if err := testDispositioner().RebuildFinalization(context.Background(), store, started.RunID); !errors.Is(err, ErrFinalizationNotEligible) {
		t.Fatalf("rebuild error = %v, want ErrFinalizationNotEligible", err)
	}
}

func TestDispositionerReestablishesApprovalIntentAfterBarrierInterleave(t *testing.T) {
	preparation, store, authority, started := dispositionFixture(t)
	defer authority.Release()
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	dispositioner := testDispositioner()
	var interleaveErr error
	dispositioner.afterBarrier = func() {
		dispositioner.afterBarrier = nil
		for _, event := range []runstate.Event{
			{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: runstate.EventMovementReady, Payload: []byte(`{}`)},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: runstate.EventMovementStarted, Payload: []byte(`{}`)},
			{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", AttemptID: "retry-1", Type: runstate.EventPerformerSelected, Payload: []byte(`{"reason":"quality_retry","performer_id":"worker","adapter_id":"fixture","model":"fixture"}`)},
		} {
			if _, err := authority.Append(event, faultpoint.ReceiptAddress("test.reestablish."+string(event.Type))); err != nil {
				interleaveErr = err
				return
			}
		}
	}
	disposition, err := dispositioner.PrepareAdapterProposal(context.Background(), adapterProposal(store, authority, started.RunID, hash, false, []any{map[string]any{"op": "replace", "path": "/policy/allowed_paths", "value": []any{}}}))
	if interleaveErr != nil {
		t.Fatal(interleaveErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if disposition.RouteDescriptor == nil || disposition.RouteDescriptor["reason"] != "runtime_scope_started" || disposition.PreparedReceipt != nil {
		t.Fatalf("re-established disposition = %#v, want runtime_scope_started route", disposition)
	}
}

func TestRecoveryCompletesFrozenBlockingProposalRouteThenRequest(t *testing.T) {
	store, authority, started := interruptedBlockingProposalFixture(t)
	if err := authority.Release(); err != nil {
		t.Fatal(err)
	}
	executor := recoveryExecutor(store, started.RunID)
	result, err := executor.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != recoveryexec.OutcomeQuiescent || !reflect.DeepEqual(result.Kinds, []recovery.ActionKind{recovery.ActionAppendBlockedProposalRoute, recovery.ActionAppendRoutedRequest, recovery.ActionReturnWaitingHuman}) {
		t.Fatalf("recovery result = %+v", result)
	}

	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	blocked, routed, requested := blockingRouteEvents(t, journal.Events)
	blockedPayload := eventPayload(t, blocked)
	raised := blockedPayload["raised"].([]any)[0].(map[string]any)
	descriptor := raised["route"].(map[string]any)
	routedPayload := eventPayload(t, routed)
	for _, key := range []string{"proposal_record_hash", "reason", "decision_type", "base_revision", "base_hash", "classifier_version", "typed_delta", "actual_impact", "identity_versions"} {
		if !reflect.DeepEqual(routedPayload[key], descriptor[key]) {
			t.Fatalf("routed %s = %#v, want frozen descriptor %#v", key, routedPayload[key], descriptor[key])
		}
	}
	if routedPayload["proposal_id"] != raised["proposal_id"] || routedPayload["emitted_id"] != raised["emitted_id"] || routedPayload["decision_id"] != raised["decision_id"] || routedPayload["blocking"] != true {
		t.Fatalf("routed payload does not preserve frozen source = %#v", routedPayload)
	}
	requestedPayload := eventPayload(t, requested)
	for key, want := range map[string]any{
		"decision_id": routedPayload["decision_id"], "decision_type": routedPayload["decision_type"], "proposal_id": routedPayload["proposal_id"],
		"routed_reason": routedPayload["reason"], "blocking": routedPayload["blocking"], "emitted_id": routedPayload["emitted_id"],
	} {
		if !reflect.DeepEqual(requestedPayload[key], want) {
			t.Fatalf("requested %s = %#v, want routed source %#v", key, requestedPayload[key], want)
		}
	}

	before := len(journal.Events)
	second, err := executor.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Outcome != recoveryexec.OutcomeQuiescent {
		t.Fatalf("second recovery result = %+v", second)
	}
	journal, err = store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Events) != before {
		t.Fatalf("idempotent recovery appended %d events", len(journal.Events)-before)
	}
}

var errInterruptedRouteAppend = fmt.Errorf("fixture crash after attempt.blocked")

type interruptedRouteDispositioner struct{}

func (interruptedRouteDispositioner) PrepareAdapterProposal(ctx context.Context, proposal driver.AdapterProposal) (driver.AdapterProposalDisposition, error) {
	disposition, err := testDispositioner().PrepareAdapterProposal(ctx, proposal)
	if err != nil || disposition.AppendRoute == nil {
		return disposition, err
	}
	disposition.AppendRoute = func(context.Context) error { return errInterruptedRouteAppend }
	return disposition, nil
}

func interruptedBlockingProposalFixture(t *testing.T) (*runstore.Store, *runstore.Driver, workspace.StartResult) {
	t.Helper()
	preparation, store, authority, started := dispositionFixture(t)
	attempt, err := started.Run.CreateAttempt("inspect")
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []runstate.EventType{runstate.EventMovementReady, runstate.EventMovementStarted} {
		if _, err := authority.Append(runstate.Event{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: eventType, Payload: []byte(`{}`)}, faultpoint.ReceiptAddress("test."+string(eventType))); err != nil {
			t.Fatal(err)
		}
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	result := driver.ExecuteAttempt(context.Background(), driver.AttemptExecution{
		RepositoryRoot: preparation.RepositoryRoot, Score: input.Score, Cast: input.Cast, RunID: started.RunID,
		Attempt: attempt, BaseTree: input.BaseTree, CandidateTree: input.BaseTree, Authority: authority,
		PerformerID: "worker", SelectionReason: "initial", RemainingMS: input.Projection.Scheduler.RemainingTime,
	}, driver.ExecutionDependencies{Probe: faultpoint.Nop{}, Client: waitingProposalExecutor{t: t, baseHash: hash, requiresDecision: true}, ResolveTrampoline: func() (string, error) { return "/fixture/trampoline", nil }, Now: time.Now, NewID: workspace.NewID, ProposalDisposition: interruptedRouteDispositioner{}})
	if result.Err == nil || result.Err.Error() == "" {
		t.Fatalf("interrupted route result = %+v, want append failure after attempt.blocked", result)
	}
	journal, err := store.ReadJournal(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	blocked, routed, _ := blockingRouteEvents(t, journal.Events)
	if blocked.Type != runstate.EventAttemptBlocked || routed.Type != "" {
		t.Fatalf("interrupted route journal has routed marker: %#v", journal.Events)
	}
	return store, authority, started
}

func recoveryExecutor(store *runstore.Store, runID runstate.RunID) *recoveryexec.Executor {
	return &recoveryexec.Executor{Store: store, RunID: runID, Load: func(context.Context) (recovery.Input, error) {
		input, err := store.LoadRunInput(runID)
		if err != nil {
			return recovery.Input{}, err
		}
		observations, err := recoveryobs.Collect(store, runID, input.Projection)
		return recovery.Input{Projection: input.Projection, Observations: observations}, err
	}}
}

func blockingRouteEvents(t *testing.T, events []runstate.Event) (runstate.Event, runstate.Event, runstate.Event) {
	t.Helper()
	var blocked, routed, requested runstate.Event
	for _, event := range events {
		switch event.Type {
		case runstate.EventAttemptBlocked:
			blocked = event
		case runstate.EventAmendmentRoutedHuman:
			routed = event
		case runstate.EventDecisionRequested:
			payload := eventPayload(t, event)
			if payload["decision_type"] == "amendment" {
				requested = event
			}
		}
	}
	return blocked, routed, requested
}

func adapterProposal(store *runstore.Store, authority *runstore.Driver, runID runstate.RunID, hash string, requiresDecision bool, operations []any) driver.AdapterProposal {
	amendment, err := json.Marshal(map[string]any{"base_revision": 1, "base_hash": hash, "operations": operations, "reason": "adapter request"})
	if err != nil {
		panic(err)
	}
	return driver.AdapterProposal{Store: store, Authority: authority, RunID: runID, AttemptID: "attempt-1", MovementID: "inspect", PartID: "reader", ProposalID: "prp-1", DecisionID: "dec-1", Event: protocol.ProposalEvent{ID: "emitted-1", Amendment: amendment, RequiresDecision: requiresDecision}}
}

func appendBlockingProposalSource(t *testing.T, store *runstore.Store, authority *runstore.Driver, runID runstate.RunID, descriptor map[string]any) {
	t.Helper()
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	binding, ok := input.Cast.Binding("reader")
	if !ok {
		t.Fatal("reader binding is absent")
	}
	performer, ok := input.Cast.Performer(binding.Performer)
	if !ok {
		t.Fatal("reader performer is absent")
	}
	versions := map[string]any{"canonical_encoding": 1, "projections": map[string]any{}}
	adapterProcess := deadAdapterProcessPayload(t)
	for _, event := range []runstate.Event{
		{RunID: runID, ScoreRevision: 1, MovementID: "inspect", Type: runstate.EventMovementReady, Payload: []byte(`{}`)},
		{RunID: runID, ScoreRevision: 1, MovementID: "inspect", Type: runstate.EventMovementStarted, Payload: []byte(`{}`)},
		{RunID: runID, ScoreRevision: 1, MovementID: "inspect", PartID: "reader", AttemptID: "attempt-1", Type: runstate.EventPerformerSelected, Payload: testPayload(t, map[string]any{"reason": "initial", "performer_id": performer.ID, "adapter_id": performer.Adapter, "model": performer.Model})},
		{RunID: runID, ScoreRevision: 1, MovementID: "inspect", PartID: "reader", AttemptID: "attempt-1", Type: runstate.EventAttemptStarted, Payload: testPayload(t, map[string]any{
			"attempt_number": 1, "adapter_process": adapterProcess,
			"granted_authority": map[string]any{"paths_rw": []any{}, "paths_ro": []any{"**"}, "shell": false, "network": false}, "identity_versions": versions,
		})},
		{RunID: runID, ScoreRevision: 1, MovementID: "inspect", PartID: "reader", AttemptID: "attempt-1", Type: runstate.EventAdapterProbed, Payload: testPayload(t, map[string]any{
			"adapter_version": "1", "capabilities": map[string]any{"repo_read": true, "repo_write": false, "shell": false, "network": false, "resumable_sessions": false},
			"enforcement":         map[string]any{"path_grants": true, "read_only": true, "network_grants": true, "shell_grants": true, "read_grants": true},
			"negotiated_features": []any{}, "truncated_resolutions": []any{}, "delivered_resolutions": []any{}, "delivered_feedback": []any{}, "advisory_dimensions": []any{},
			"execution_dependency_hash": "sha256:dependency", "identity_versions": versions,
		})},
		{RunID: runID, ScoreRevision: 1, MovementID: "inspect", PartID: "reader", AttemptID: "attempt-1", Type: runstate.EventAttemptBlocked, Payload: testPayload(t, map[string]any{
			"raised":               []any{map[string]any{"decision_id": "dec-1", "emitted_id": "emitted-1", "kind": "proposal", "proposal_id": "prp-1", "blocking": true, "route": descriptor}},
			"pending_decision_ids": []any{"dec-1"},
		})},
	} {
		if _, err := authority.Append(event, faultpoint.ReceiptAddress("test.blocking_source."+string(event.Type))); err != nil {
			t.Fatal(err)
		}
	}
}

func deadAdapterProcessPayload(t *testing.T) map[string]any {
	t.Helper()
	command := exec.Command("sleep", "60")
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pid := command.Process.Pid
	identity, err := procid.Read(pid)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal(err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed adapter fixture exited successfully")
	}
	var start map[string]any
	switch value := identity.(type) {
	case runstate.LinuxStartIdentity:
		start = map[string]any{"platform": "linux", "boot_id": value.BootID, "start_ticks": value.StartTicks}
	case runstate.DarwinStartIdentity:
		start = map[string]any{"platform": "darwin", "start_tvsec": value.StartTVSec, "start_tvusec": value.StartTVUsec}
	default:
		t.Fatalf("unsupported start identity %T", identity)
	}
	return map[string]any{"pid": pid, "session_id": pid, "start_identity": start}
}

func testDispositioner() ProposalDispositioner {
	return New()
}

func dispositionFixture(t *testing.T) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult) {
	return dispositionFixtureWithReceiptObserver(t, nil)
}

func dispositionFixtureWithReceiptObserver(t *testing.T, observer runstore.ReceiptObserver) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult) {
	t.Helper()
	return dispositionFixtureAt(t, t.TempDir(), observer)
}

func dispositionFixtureAt(t *testing.T, root string, observer runstore.ReceiptObserver) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult) {
	t.Helper()
	return dispositionFixtureDocumentsAt(t, root, observer,
		`{"score":"0.2","name":"amendmentexec","revision":1,"status":"finalized","goal":"goal","open_questions":[],"parts":{"reader":{"capabilities":["repo_read"],"read_only":true}},"movements":[{"id":"inspect","part":"reader","grants":["repo_read"],"may_propose":true,"instruction":"inspect","outputs":[{"id":"report","kind":"artifact"}],"acceptance":{"hard":[{"id":"report-present","artifact":"report"}]}}],"policy":{"allowed_paths":["src/**"],"budget":{"active_wall_clock_min":10,"retries_per_movement":2},"amendment":{"auto":"envelope"}},"verification":{"expectation":{"intent":"pass-existing-tests","apply_gate":{"require":["verified"]}},"final_movement":"inspect"}}`,
		`{"cast":"0.1","performers":{"worker":{"adapter":"fixture","model":"fixture"}},"bindings":{"reader":{"performer":"worker"}}}`,
	)
}

func dispositionFixtureDocumentsAt(t *testing.T, root string, observer runstore.ReceiptObserver, scoreDocument, castDocument string) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult) {
	t.Helper()
	write := func(path, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "partitur.yaml"), scoreDocument)
	write(filepath.Join(root, ".partitur", "cast.yaml"), castDocument)
	for _, args := range [][]string{{"init"}, {"config", "user.name", "Partitur Test"}, {"config", "user.email", "partitur@example.invalid"}, {"add", "partitur.yaml", ".partitur/cast.yaml"}, {"commit", "-m", "fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })
	preparation, result := validate.Prepare()
	if preparation == nil || result.HasDiagnostics() {
		t.Fatalf("prepare result = %#v", result)
	}
	started, err := workspace.Start(preparation, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.New(root, faultpoint.Nop{}, observer)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := store.AcquireDriver(started.RunID, movementSeeds(preparation.Score))
	if err != nil {
		t.Fatal(err)
	}
	return preparation, store, authority, started
}

func prepareCutDispositionFixtureAt(t *testing.T, root string, observer runstore.ReceiptObserver) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult) {
	t.Helper()
	return dispositionFixtureDocumentsAt(t, root, observer,
		`{"score":"0.2","name":"amendmentexec","revision":1,"status":"finalized","goal":"goal","open_questions":[],"parts":{"reader":{"capabilities":["repo_read","shell","network"],"read_only":true}},"movements":[{"id":"inspect","part":"reader","grants":["repo_read","shell","network"],"may_propose":true,"instruction":"inspect","outputs":[{"id":"report","kind":"artifact"}],"acceptance":{"hard":[{"id":"report-present","artifact":"report"}]}}],"policy":{"allowed_paths":["**"],"budget":{"active_wall_clock_min":10,"retries_per_movement":2},"amendment":{"auto":"envelope"}},"verification":{"expectation":{"intent":"pass-existing-tests","apply_gate":{"require":["verified"]}},"final_movement":"inspect"}}`,
		`{"cast":"0.1","performers":{"worker":{"adapter":"codex","model":"fixture"}},"bindings":{"reader":{"performer":"worker"}}}`,
	)
}

type receiptPauseGate struct {
	address faultpoint.ReceiptAddress
	reached chan faultpoint.DurabilityReceipt
	release chan struct{}
	once    sync.Once
}

type receiptRecorder struct {
	mu        sync.Mutex
	addresses []faultpoint.ReceiptAddress
}

func (recorder *receiptRecorder) Observed(receipt faultpoint.DurabilityReceipt) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.addresses = append(recorder.addresses, receipt.Address)
}

func (recorder *receiptRecorder) index(address faultpoint.ReceiptAddress) int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	for index, observed := range recorder.addresses {
		if observed == address {
			return index
		}
	}
	return -1
}

type lockAcquisitionProbe struct {
	point    faultpoint.PointID
	acquired chan struct{}
	once     sync.Once
}

func newLockAcquisitionProbe(point faultpoint.PointID) *lockAcquisitionProbe {
	return &lockAcquisitionProbe{point: point, acquired: make(chan struct{})}
}

func (probe *lockAcquisitionProbe) Reached(point faultpoint.PointID) {
	if point == probe.point {
		probe.once.Do(func() { close(probe.acquired) })
	}
}

func newReceiptPauseGate(address faultpoint.ReceiptAddress) *receiptPauseGate {
	return &receiptPauseGate{
		address: address,
		reached: make(chan faultpoint.DurabilityReceipt, 1),
		release: make(chan struct{}),
	}
}

func (gate *receiptPauseGate) Observed(receipt faultpoint.DurabilityReceipt) {
	if receipt.Address != gate.address {
		return
	}
	gate.reached <- receipt
	<-gate.release
}

func (gate *receiptPauseGate) Wait(t *testing.T) faultpoint.DurabilityReceipt {
	t.Helper()
	select {
	case receipt := <-gate.reached:
		return receipt
	case <-time.After(5 * time.Second):
		t.Fatalf("receipt observer did not reach %q", gate.address)
		return faultpoint.DurabilityReceipt{}
	}
}

func (gate *receiptPauseGate) Release() {
	gate.once.Do(func() { close(gate.release) })
}

func autoApprovalAttemptFixture(t *testing.T) (*validate.Preparation, *runstore.Store, *runstore.Driver, workspace.StartResult, *workspace.AttemptWorkspace, runstore.RunInput, string) {
	t.Helper()
	preparation, store, authority, started := dispositionFixture(t)
	attempt, err := started.Run.CreateAttempt("inspect")
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []runstate.EventType{runstate.EventMovementReady, runstate.EventMovementStarted} {
		if _, err := authority.Append(runstate.Event{RunID: started.RunID, ScoreRevision: 1, MovementID: "inspect", Type: eventType, Payload: []byte(`{}`)}, faultpoint.ReceiptAddress("test."+string(eventType))); err != nil {
			t.Fatal(err)
		}
	}
	input, err := store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.RecordRecoveredZeroWriterCandidate(store, authority, input); err != nil {
		t.Fatal(err)
	}
	input, err = store.LoadRunInput(started.RunID)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := preparation.Score.Hash()
	if err != nil {
		t.Fatal(err)
	}
	return preparation, store, authority, started, attempt, input, hash
}

func hasRevisionRestartSelection(t *testing.T, events []runstate.Event, revision uint64) bool {
	t.Helper()
	for _, event := range events {
		if event.Type == runstate.EventPerformerSelected && event.ScoreRevision == revision && eventPayload(t, event)["reason"] == "revision_restart" {
			return true
		}
	}
	return false
}

func movementSeeds(compiled *score.Score) []runstate.MovementSeed {
	movements := compiled.Movements()
	result := make([]runstate.MovementSeed, 0, len(movements))
	for _, movement := range movements {
		repoWrite := false
		for _, grant := range movement.Grants {
			repoWrite = repoWrite || grant == "repo_write"
		}
		result = append(result, runstate.MovementSeed{ID: runstate.MovementID(movement.ID), Initial: runstate.MovementPending, RepoWrite: repoWrite, HasDependencies: len(movement.Needs) != 0, Final: movement.ID == compiled.Execution().FinalMovementID})
	}
	return result
}

func eventPayload(t *testing.T, event runstate.Event) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(event.Payload, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func testPayload(t *testing.T, value any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

type waitingProposalExecutor struct {
	t                *testing.T
	baseHash         string
	requiresDecision bool
}

var errContinuedAutoApproval = errors.New("continued auto approval attempt")

type autoThenInterruptedExecutor struct {
	first waitingProposalExecutor
	calls int
}

func (fixture *autoThenInterruptedExecutor) Resolve(adapterID string) (string, error) {
	return fixture.first.Resolve(adapterID)
}

func (fixture *autoThenInterruptedExecutor) Execute(ctx context.Context, plan adapter.ExecutePlan) (adapter.ExecuteReport, error) {
	fixture.calls++
	if fixture.calls == 1 {
		return fixture.first.Execute(ctx, plan)
	}
	return adapter.ExecuteReport{}, errContinuedAutoApproval
}

func (waitingProposalExecutor) Resolve(string) (string, error) { return "/fixture/adapter", nil }

func (fixture waitingProposalExecutor) Execute(_ context.Context, plan adapter.ExecutePlan) (adapter.ExecuteReport, error) {
	fixture.t.Helper()
	start, err := procid.Read(os.Getpid())
	if err != nil {
		return adapter.ExecuteReport{}, err
	}
	if _, err := plan.RecordIdentity(runstate.ProcessIdentity{PID: os.Getpid(), SessionID: os.Getpid(), Start: start}); err != nil {
		return adapter.ExecuteReport{}, err
	}
	probe := protocol.ProbeResult{Protocol: protocol.ProtocolVersion, Adapter: protocol.AdapterIdentity{ID: plan.AdapterID, Version: "fixture"}, Capabilities: protocol.Capabilities{RepoRead: true}, Enforcement: protocol.Enforcement{PathGrants: true, ReadOnly: true, NetworkGrants: true, ShellGrants: true, ReadGrants: true}}
	if _, err := plan.Recorder.RecordProbe(probe); err != nil {
		return adapter.ExecuteReport{}, err
	}
	if _, err := plan.Recorder.RecordExecutionStopped(adapter.ExecutionStop{IntervalID: plan.IntervalID, Reason: "normal", Charging: "measured", ObservedAt: plan.IntervalOpened}); err != nil {
		return adapter.ExecuteReport{}, err
	}
	proposal := protocol.ProposalEvent{Type: protocol.EventProposal, ID: "emitted-1", Amendment: amendmentForPlan(fixture.baseHash), RequiresDecision: fixture.requiresDecision}
	result := protocol.ExecuteResult{Outcome: protocol.OutcomeWaitingHuman}
	if proposal.RequiresDecision {
		result.PendingDecisionIDs = []string{proposal.ID}
	}
	if _, err := plan.Recorder.RecordOutcome(adapter.OutcomeObservation{EventType: string(runstate.EventAttemptBlocked), Result: result, Raised: []adapter.RaisedDecision{{Kind: protocol.EventProposal, Proposal: &proposal}}}); err != nil {
		return adapter.ExecuteReport{}, err
	}
	return adapter.ExecuteReport{Probe: probe, Result: &result, Proposals: []protocol.ProposalEvent{proposal}}, nil
}

func amendmentForPlan(baseHash string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"base_revision":1,"base_hash":%q,"operations":[{"op":"replace","path":"/policy/budget/active_wall_clock_min","value":9}],"reason":"adapter request"}`, baseHash))
}
