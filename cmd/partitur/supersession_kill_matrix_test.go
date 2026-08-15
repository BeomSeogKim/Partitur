package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

type supersessionObligation struct {
	edge      faultpoint.EdgeID
	branch    string
	side      string
	endpoint  supersessionEndpoint
	assertion string
}

func (obligation supersessionObligation) String() string {
	return fmt.Sprintf("%s/%s/%s", obligation.edge, obligation.branch, obligation.side)
}

type supersessionEndpoint struct {
	point   faultpoint.PointID
	receipt faultpoint.ReceiptAddress
}

func (endpoint supersessionEndpoint) String() string {
	if endpoint.point != "" {
		return string(endpoint.point)
	}
	return string(endpoint.receipt)
}

type supersessionBranch struct {
	name         string
	liveApprover bool
}

func TestSupersessionKillMatrix(t *testing.T) {
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

	required := requiredSupersessionObligations(t)
	scenarios := supersessionKillScenarios(t, required)
	discharged := make(map[supersessionObligation]bool, len(required))
	for _, scenario := range scenarios {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			repository, environment, runID, driver, epoch := supersessionFixture(t, bin, vendor, scenario.branch)
			defer driver.stop(t)

			var cut supersessionCut
			if scenario.branch.liveApprover {
				cut = killLiveApproverAtSupersessionEndpoint(t, partitur, repository, environment, runID, scenario.endpoint)
				if scenario.endpoint.point == faultpoint.PointSupersedeSessionsSwept {
					driver.kill(t)
				}
			} else {
				if err := appendRecoveryControlPrepare(mustSupersessionStore(t, repository), runID, repository); err != nil {
					t.Fatal(err)
				}
				driver.kill(t)
				cut = killSupersessionCommandAtEndpoint(t, partitur, repository, environment, scenario.endpoint, "resume", string(runID))
			}

			assertSupersessionCutState(t, repository, runID, scenario.endpoint, epoch)
			assertSupersessionCrashAssertions(t, repository, runID, scenario.obligations, cut, epoch)
			assertRecoveryFixedPoint(t, partitur, repository, environment, string(runID), nil, fixedPointNoneFixture)
			assertSupersessionFixedPoint(t, repository, runID, epoch)
			for _, obligation := range scenario.obligations {
				discharged[obligation] = true
			}
		})
	}
	assertSupersessionObligationCoverage(t, required, discharged)
}

type supersessionKillScenario struct {
	name        string
	branch      supersessionBranch
	endpoint    supersessionEndpoint
	obligations []supersessionObligation
}

func supersessionKillScenarios(t *testing.T, obligations map[supersessionObligation]bool) []supersessionKillScenario {
	t.Helper()
	branches := supersessionBranches()
	byName := make(map[string]*supersessionKillScenario)
	for obligation := range obligations {
		branch, ok := branches[obligation.branch]
		if !ok {
			t.Fatalf("supersession obligation names unknown branch %q", obligation.branch)
		}
		key := branch.name + "/" + obligation.endpoint.String()
		scenario := byName[key]
		if scenario == nil {
			scenario = &supersessionKillScenario{name: key, branch: branch, endpoint: obligation.endpoint}
			byName[key] = scenario
		}
		scenario.obligations = append(scenario.obligations, obligation)
	}
	if len(byName) == 0 {
		t.Fatal("supersession matrix produced no kill cuts")
	}
	keys := make([]string, 0, len(byName))
	for key := range byName {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	scenarios := make([]supersessionKillScenario, 0, len(keys))
	for _, key := range keys {
		scenario := byName[key]
		sort.Slice(scenario.obligations, func(left, right int) bool {
			return scenario.obligations[left].String() < scenario.obligations[right].String()
		})
		scenarios = append(scenarios, *scenario)
	}
	return scenarios
}

func requiredSupersessionObligations(t *testing.T) map[supersessionObligation]bool {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(contents), "\n")
	start, end := supersessionTableBounds(t, lines)
	rows := make(map[faultpoint.EdgeID]struct {
		left, right, assertion string
	})
	for _, line := range lines[start:end] {
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "| Edge |") || strings.HasPrefix(line, "|---") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) != 7 {
			t.Fatalf("unparseable DESIGN supersession row %q", line)
		}
		id := faultpoint.EdgeID(strings.Trim(strings.TrimSpace(cells[1]), "`"))
		left := strings.TrimSpace(cells[2])
		right := strings.TrimSpace(cells[3])
		assertion := strings.TrimSpace(cells[5])
		if !strings.HasPrefix(string(id), "supersede.") || left == "" || right == "" || assertion == "" {
			t.Fatalf("unparseable DESIGN supersession row %q", line)
		}
		if _, duplicate := rows[id]; duplicate {
			t.Fatalf("duplicate DESIGN supersession edge %q", id)
		}
		rows[id] = struct{ left, right, assertion string }{left, right, assertion}
	}
	if len(rows) == 0 {
		t.Fatal("DESIGN supersession extraction produced no rows")
	}
	if len(rows) != 4 {
		t.Fatalf("DESIGN supersession edge count=%d, want four", len(rows))
	}

	required := make(map[supersessionObligation]bool)
	for edge, row := range rows {
		left := supersessionEndpointFromDESIGNCell(t, row.left)
		right := supersessionEndpointFromDESIGNCell(t, row.right)
		assertSupersessionAssertionShape(t, edge, row.assertion)
		for name := range supersessionBranches() {
			required[supersessionObligation{edge: edge, branch: name, side: "before", endpoint: left, assertion: row.assertion}] = true
			required[supersessionObligation{edge: edge, branch: name, side: "after", endpoint: right, assertion: row.assertion}] = true
		}
	}
	if len(required) == 0 {
		t.Fatal("DESIGN supersession obligation extraction produced no obligations")
	}
	if len(required) != 16 {
		t.Fatalf("DESIGN supersession endpoint obligations=%d, want sixteen", len(required))
	}
	return required
}

func supersessionTableBounds(t *testing.T, lines []string) (int, int) {
	t.Helper()
	const heading = "**Supersession fencing** — the commit table's silence-expiry and dead-owner branches."
	const nextHeading = "**Authority acquisition**"
	start, end := -1, -1
	for index, line := range lines {
		switch strings.TrimSpace(line) {
		case heading:
			if start >= 0 {
				t.Fatalf("DESIGN supersession heading appears more than once")
			}
			start = index + 1
		case nextHeading:
			if end >= 0 {
				t.Fatalf("DESIGN authority heading appears more than once")
			}
			end = index
		}
	}
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("DESIGN supersession table bounds start=%d end=%d", start, end)
	}
	return start, end
}

func supersessionEndpointFromDESIGNCell(t *testing.T, cell string) supersessionEndpoint {
	t.Helper()
	switch {
	case strings.Contains(cell, "sweep verified empty"):
		return supersessionEndpoint{point: faultpoint.PointSupersedeSessionsSwept}
	case strings.Contains(cell, "execution.stopped"):
		return supersessionEndpoint{receipt: "prepare.commit.execution.stopped"}
	case strings.Contains(cell, "fence branch taken"):
		return supersessionEndpoint{point: faultpoint.PointSupersedeFenceDecided}
	case strings.Contains(cell, "amendment.approved"):
		return supersessionEndpoint{receipt: "prepare.commit.approved"}
	case strings.Contains(cell, "stale lease removed"):
		return supersessionEndpoint{receipt: "prepare.commit.lease"}
	default:
		t.Fatalf("DESIGN supersession endpoint has no harness mapping: %q", cell)
		return supersessionEndpoint{}
	}
}

func assertSupersessionAssertionShape(t *testing.T, edge faultpoint.EdgeID, assertion string) {
	t.Helper()
	switch {
	case strings.Contains(assertion, "nothing else in this group attests"):
	case strings.Contains(assertion, "Same obligation as `cancel.interval_stopped_to_terminal`"):
	case strings.Contains(assertion, "advancing the epoch"):
	case strings.Contains(assertion, "removal follows the append"):
	default:
		t.Fatalf("DESIGN supersession assertion has no durable oracle: edge=%q assertion=%q", edge, assertion)
	}
}

func supersessionBranches() map[string]supersessionBranch {
	return map[string]supersessionBranch{
		"silence_expiry": {name: "silence_expiry", liveApprover: true},
		"dead_owner":     {name: "dead_owner"},
	}
}

func assertSupersessionObligationCoverage(t *testing.T, required, discharged map[supersessionObligation]bool) {
	t.Helper()
	if len(required) != len(discharged) {
		t.Fatalf("supersession obligation coverage=%d, want %d; missing=%s", len(discharged), len(required), supersessionObligationDifference(required, discharged))
	}
	for obligation := range required {
		if !discharged[obligation] {
			t.Fatalf("supersession obligation was not discharged: %s", obligation)
		}
	}
	for obligation := range discharged {
		if !required[obligation] {
			t.Fatalf("supersession test discharged an undeclared obligation: %s", obligation)
		}
	}
}

func supersessionObligationDifference(required, discharged map[supersessionObligation]bool) string {
	missing := make([]string, 0)
	for obligation := range required {
		if !discharged[obligation] {
			missing = append(missing, obligation.String())
		}
	}
	sort.Strings(missing)
	return strings.Join(missing, ", ")
}

func supersessionFixture(t *testing.T, bin, vendor string, branch supersessionBranch) (string, []string, runstate.RunID, *preparedLiveRun, uint64) {
	t.Helper()
	repository, environment := killHarnessRepositoryWithInputs(t, bin, vendor, supersessionScore(), runCast())
	driver := startPreparedLiveRun(t, mustE2EBinary(t, bin, "partitur"), repository, environment)
	driver.waitProbe(t, faultpoint.PointAuthorityLeaseCreated)
	runID, err := soleRunID(repository)
	if err != nil {
		driver.stop(t)
		t.Fatal(err)
	}
	store := mustSupersessionStore(t, repository)
	input, err := store.LoadRunInput(runID)
	if err != nil {
		driver.stop(t)
		t.Fatal(err)
	}
	if input.Projection.State.Authority.Epoch == 0 {
		driver.stop(t)
		t.Fatal("supersession fixture has no driver authority")
	}
	appendSupersessionFixtureInterval(t, store, runID, input.Projection.State.ScoreHead.Revision)
	return repository, environment, runID, driver, input.Projection.State.Authority.Epoch
}

func supersessionScore() map[string]any {
	score := runScore()
	score["policy"].(map[string]any)["amendment"] = map[string]any{"auto": "envelope"}
	return score
}

func mustE2EBinary(t *testing.T, directory, name string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustSupersessionStore(t *testing.T, repository string) *runstore.Store {
	t.Helper()
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func appendSupersessionFixtureInterval(t *testing.T, store *runstore.Store, runID runstate.RunID, revision uint64) {
	t.Helper()
	if err := store.Mutate(runID, "", func(transaction *runstore.Txn) error {
		_, err := transaction.At("supersession.fixture.execution.started").Append(runstate.Event{
			RunID: runID, ScoreRevision: revision, Type: runstate.EventExecutionStarted,
			Payload: cancellationFixturePayload(t, map[string]any{
				"interval_id": "supersession-interval", "phase": "fixture",
				"wall_start": time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), "remaining_at_start": 600000,
			}),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

type supersessionCut struct {
	points   map[faultpoint.PointID]bool
	receipts map[faultpoint.ReceiptAddress]bool
}

func killLiveApproverAtSupersessionEndpoint(t *testing.T, binary, repository string, environment []string, runID runstate.RunID, endpoint supersessionEndpoint) supersessionCut {
	t.Helper()
	patch := filepath.Join(repository, "supersession-amendment.json")
	if err := os.WriteFile(patch, []byte(`[{"op":"replace","path":"/policy/budget/active_wall_clock_min","value":9}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	return killSupersessionCommandAtEndpoint(t, binary, repository, environment, endpoint, "amend", string(runID), "--patch", patch, "--reason", "supersession fixture")
}

func killSupersessionCommandAtEndpoint(t *testing.T, binary, repository string, environment []string, endpoint supersessionEndpoint, arguments ...string) supersessionCut {
	t.Helper()
	receiptRead, receiptWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer receiptRead.Close()
	defer receiptWrite.Close()
	receiptReleaseRead, receiptReleaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer receiptReleaseRead.Close()
	defer receiptReleaseWrite.Close()
	probeRead, probeWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer probeRead.Close()
	defer probeWrite.Close()
	probeReleaseRead, probeReleaseWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer probeReleaseRead.Close()
	defer probeReleaseWrite.Close()

	files := make([]*os.File, 0, 10)
	for range 6 {
		file, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
		defer file.Close()
	}
	files = append(files, receiptWrite, receiptReleaseRead, probeWrite, probeReleaseRead)
	var stdout, stderr bytes.Buffer
	command := exec.Command(binary, arguments...)
	command.Dir = repository
	command.Env = replaceEnvironment(environment, map[string]string{
		"PARTITUR_RECEIPT_NOTIFY_FD":     "9",
		"PARTITUR_RECEIPT_RELEASE_FD":    "10",
		"PARTITUR_FAULTPOINT_HARNESS":    "1",
		"PARTITUR_FAULTPOINT_NOTIFY_FD":  "11",
		"PARTITUR_FAULTPOINT_RELEASE_FD": "12",
	})
	command.ExtraFiles = files
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = receiptWrite.Close()
	_ = receiptReleaseRead.Close()
	_ = probeWrite.Close()
	_ = probeReleaseRead.Close()

	type notification struct {
		point   faultpoint.PointID
		receipt faultpoint.ReceiptAddress
		err     error
	}
	notifications := make(chan notification, 16)
	scan := func(reader *os.File, receipt bool) {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) != 2 {
				notifications <- notification{err: fmt.Errorf("malformed supersession notification %q", scanner.Text())}
				return
			}
			if fields[1] != fmt.Sprint(command.Process.Pid) {
				notifications <- notification{err: fmt.Errorf("supersession notification pid=%s, want %d", fields[1], command.Process.Pid)}
				return
			}
			if receipt {
				notifications <- notification{receipt: faultpoint.ReceiptAddress(fields[0])}
			} else {
				notifications <- notification{point: faultpoint.PointID(fields[0])}
			}
		}
		if err := scanner.Err(); err != nil {
			notifications <- notification{err: err}
		}
	}
	go scan(receiptRead, true)
	go scan(probeRead, false)

	cut := supersessionCut{points: make(map[faultpoint.PointID]bool), receipts: make(map[faultpoint.ReceiptAddress]bool)}
	backdated := false
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case notice := <-notifications:
			if notice.err != nil {
				_ = command.Process.Kill()
				_ = command.Wait()
				t.Fatal(notice.err)
			}
			if notice.point != "" {
				cut.points[notice.point] = true
				if notice.point == endpoint.point {
					killSupersessionCommand(t, command, arguments, &stdout, &stderr)
					return cut
				}
				if _, err := probeReleaseWrite.Write([]byte{1}); err != nil {
					t.Fatal(err)
				}
				continue
			}
			cut.receipts[notice.receipt] = true
			if notice.receipt == "amendment.approval_prepared" {
				backdateSupersessionPrepare(t, repository)
				backdated = true
			}
			if notice.receipt == endpoint.receipt {
				killSupersessionCommand(t, command, arguments, &stdout, &stderr)
				if strings.HasPrefix(strings.Join(arguments, " "), "amend ") && !backdated {
					t.Fatal("live approver reached a supersession cut without backdating its no-ack prepare")
				}
				return cut
			}
			if _, err := receiptReleaseWrite.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
		case <-timer.C:
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("supersession command %q did not reach %q\nstdout:\n%s\nstderr:\n%s", arguments[0], endpoint, &stdout, &stderr)
		}
	}
}

func killSupersessionCommand(t *testing.T, command *exec.Cmd, arguments []string, stdout, stderr *bytes.Buffer) {
	t.Helper()
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatalf("supersession command %q at cut exited successfully\nstdout:\n%s\nstderr:\n%s", arguments[0], stdout, stderr)
	}
}

func backdateSupersessionPrepare(t *testing.T, repository string) {
	t.Helper()
	runID, err := soleRunID(repository)
	if err != nil {
		t.Fatal(err)
	}
	path := runstorePath(repository, runID, "journal.jsonl")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(contents, []byte("\n")), []byte("\n"))
	backdated := false
	for index := len(lines) - 1; index >= 0; index-- {
		var event runstate.Event
		if err := json.Unmarshal(lines[index], &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != runstate.EventAmendmentApprovalPrepared {
			continue
		}
		event.Timestamp = time.Now().Add(-61 * time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		lines[index] = encoded
		backdated = true
		break
	}
	if !backdated {
		t.Fatal("live approver did not durably append amendment.approval_prepared")
	}
	if err := os.WriteFile(path, append(bytes.Join(lines, []byte("\n")), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	journal, err := mustSupersessionStore(t, repository).ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Events) == 0 || journal.Events[len(journal.Events)-1].Type != runstate.EventAmendmentApprovalPrepared {
		t.Fatalf("backdated prepare journal tail=%+v", journal.Events)
	}
}

func assertSupersessionCutState(t *testing.T, repository string, runID runstate.RunID, endpoint supersessionEndpoint, initialEpoch uint64) {
	t.Helper()
	store := mustSupersessionStore(t, repository)
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	approval := supersessionApprovalEvent(journal.Events)
	stopped := supersessionStoppedEvent(journal.Events)
	lease, present, err := store.ReadLease(runID)
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case endpoint.point == faultpoint.PointSupersedeSessionsSwept:
		if approval.Type != "" || !present || input.Projection.State.Authority.Epoch != initialEpoch {
			t.Fatalf("sweep cut state approval=%+v lease=%+v present=%t epoch=%d", approval, lease, present, input.Projection.State.Authority.Epoch)
		}
	case endpoint.receipt == "prepare.commit.execution.stopped":
		if stopped.Type == "" || approval.Type != "" || !present || input.Projection.State.Authority.Epoch != initialEpoch {
			t.Fatalf("interval cut state stopped=%+v approval=%+v lease=%+v present=%t epoch=%d", stopped, approval, lease, present, input.Projection.State.Authority.Epoch)
		}
	case endpoint.point == faultpoint.PointSupersedeFenceDecided:
		if approval.Type != "" || !present || input.Projection.State.Authority.Epoch != initialEpoch || lease.Epoch != initialEpoch {
			t.Fatalf("fence cut durable state approval=%+v lease=%+v present=%t epoch=%d", approval, lease, present, input.Projection.State.Authority.Epoch)
		}
	case endpoint.receipt == "prepare.commit.approved":
		if approval.Type == "" || !present || input.Projection.State.Authority.Epoch != initialEpoch+1 || lease.Epoch != initialEpoch {
			t.Fatalf("approval cut state approval=%+v lease=%+v present=%t epoch=%d", approval, lease, present, input.Projection.State.Authority.Epoch)
		}
	case endpoint.receipt == "prepare.commit.lease":
		if approval.Type == "" || present || input.Projection.State.Authority.Epoch != initialEpoch+1 {
			t.Fatalf("lease cut state approval=%+v lease=%+v present=%t epoch=%d", approval, lease, present, input.Projection.State.Authority.Epoch)
		}
	default:
		t.Fatalf("unknown supersession endpoint %q", endpoint)
	}
}

func assertSupersessionCrashAssertions(t *testing.T, repository string, runID runstate.RunID, obligations []supersessionObligation, cut supersessionCut, initialEpoch uint64) {
	t.Helper()
	journal, err := mustSupersessionStore(t, repository).ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, obligation := range obligations {
		if obligation.endpoint.point != "" && !cut.points[obligation.endpoint.point] {
			t.Fatalf("supersession B endpoint was not reached: %s", obligation)
		}
		if obligation.endpoint.receipt != "" && !cut.receipts[obligation.endpoint.receipt] {
			t.Fatalf("supersession R endpoint was not reached: %s", obligation)
		}
		if strings.Contains(obligation.assertion, "nothing else in this group attests") {
			if !cut.points[faultpoint.PointSupersedeSessionsSwept] {
				t.Fatalf("approval path did not pass the survivor sweep: %s", obligation)
			}
			if obligation.endpoint.point == faultpoint.PointSupersedeSessionsSwept && supersessionApprovalEvent(journal.Events).Type != "" {
				t.Fatalf("sweep cut already contains amendment.approved: %s", obligation)
			}
		}
		if strings.Contains(obligation.assertion, "Same obligation as `cancel.interval_stopped_to_terminal`") && !cut.receipts["prepare.commit.execution.stopped"] {
			t.Fatalf("approval path did not durably close its interval: %s", obligation)
		}
		if strings.Contains(obligation.assertion, "advancing the epoch") && !cut.points[faultpoint.PointSupersedeFenceDecided] {
			t.Fatalf("approval path did not pass the fence decision: %s", obligation)
		}
		if strings.Contains(obligation.assertion, "advancing the epoch") && obligation.endpoint.point == faultpoint.PointSupersedeFenceDecided {
			input, err := mustSupersessionStore(t, repository).LoadRunInput(runID)
			if err != nil {
				t.Fatal(err)
			}
			if input.Projection.State.Authority.Epoch != initialEpoch {
				t.Fatalf("fence B cut advanced authority epoch=%d want %d", input.Projection.State.Authority.Epoch, initialEpoch)
			}
		}
		if strings.Contains(obligation.assertion, "removal follows the append") && !cut.receipts["prepare.commit.approved"] {
			t.Fatalf("lease cleanup path did not durably append amendment.approved first: %s", obligation)
		}
	}
}

func assertSupersessionFixedPoint(t *testing.T, repository string, runID runstate.RunID, initialEpoch uint64) {
	t.Helper()
	store := mustSupersessionStore(t, repository)
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	stoppedIndex, approvalIndex := -1, -1
	var approval runstate.Event
	for index, event := range journal.Events {
		switch event.Type {
		case runstate.EventExecutionStopped:
			if stoppedIndex < 0 {
				stoppedIndex = index
				assertSupersessionStoppedPayload(t, event)
			}
		case runstate.EventAmendmentApproved:
			if approvalIndex >= 0 {
				t.Fatal("supersession recovery appended amendment.approved more than once")
			}
			approvalIndex, approval = index, event
		}
	}
	if stoppedIndex < 0 || approvalIndex < 0 || stoppedIndex >= approvalIndex {
		t.Fatalf("supersession journal ordering stopped=%d approval=%d", stoppedIndex, approvalIndex)
	}
	var payload map[string]any
	if err := json.Unmarshal(approval.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["fenced_epoch"] != float64(initialEpoch+1) {
		t.Fatalf("fenced approval payload=%v want fenced_epoch=%d", payload, initialEpoch+1)
	}
	input, err := store.LoadRunInput(runID)
	if err != nil {
		t.Fatal(err)
	}
	if input.Projection.State.Authority.Epoch < initialEpoch+1 {
		t.Fatalf("supersession final authority epoch=%d want at least %d", input.Projection.State.Authority.Epoch, initialEpoch+1)
	}
	if _, present, err := store.ReadLease(runID); err != nil || present {
		t.Fatalf("supersession fixed point retains lease: present=%t err=%v", present, err)
	}
}

func supersessionApprovalEvent(events []runstate.Event) runstate.Event {
	for _, event := range events {
		if event.Type == runstate.EventAmendmentApproved {
			return event
		}
	}
	return runstate.Event{}
}

func supersessionStoppedEvent(events []runstate.Event) runstate.Event {
	for _, event := range events {
		if event.Type == runstate.EventExecutionStopped {
			return event
		}
	}
	return runstate.Event{}
}

func assertSupersessionStoppedPayload(t *testing.T, event runstate.Event) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["reason"] != "superseded" || payload["charging"] != "clamped" || payload["interval_id"] != "supersession-interval" || event.CausationID == "" {
		t.Fatalf("supersession execution.stopped payload=%v causation=%q", payload, event.CausationID)
	}
}
