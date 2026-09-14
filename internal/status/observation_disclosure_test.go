package status

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func mustReadCurrentIdentity(t *testing.T) runstate.StartIdentity {
	t.Helper()
	identity, err := procid.Read(os.Getpid())
	if err != nil {
		t.Fatalf("read current process identity: %v", err)
	}
	return identity
}

// reapedChildPID returns the pid of a child process that has already exited and
// been reaped, so a process-table sample of it observes no live process.
func reapedChildPID(t *testing.T) int {
	t.Helper()
	command := exec.Command("/bin/sh", "-c", "exit 0")
	if err := command.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := command.Process.Pid
	if err := command.Wait(); err != nil {
		t.Fatalf("wait child: %v", err)
	}
	return pid
}

// oppositePlatformIdentity returns a recorded identity of the platform this host
// is not, so a comparison against a live local process is refused as
// cross-platform.
func oppositePlatformIdentity() runstate.StartIdentity {
	if runtime.GOOS == "darwin" {
		return runstate.LinuxStartIdentity{BootID: "boot", StartTicks: "1"}
	}
	return runstate.DarwinStartIdentity{StartTVSec: 1, StartTVUsec: 1}
}

func TestRecordedOwnerProcessMatchMatching(t *testing.T) {
	state := runstate.State{
		Run:       runstate.RunRunning,
		Authority: runstate.Authority{Epoch: 1, Owner: &runstate.AuthorityOwner{PID: os.Getpid(), Start: mustReadCurrentIdentity(t)}},
	}
	verdict, reason := recordedOwnerProcessMatch(state)
	if verdict != "MATCHING" || reason != "" {
		t.Fatalf("verdict=%q reason=%q, want MATCHING with no reason", verdict, reason)
	}
}

func TestRecordedOwnerProcessMatchGoneOrReused(t *testing.T) {
	state := runstate.State{
		Run:       runstate.RunRunning,
		Authority: runstate.Authority{Epoch: 1, Owner: &runstate.AuthorityOwner{PID: reapedChildPID(t), Start: mustReadCurrentIdentity(t)}},
	}
	verdict, reason := recordedOwnerProcessMatch(state)
	if verdict != "GONE_OR_REUSED" || reason != "" {
		t.Fatalf("verdict=%q reason=%q, want GONE_OR_REUSED with no reason", verdict, reason)
	}
}

func TestRecordedOwnerProcessMatchUnverifiable(t *testing.T) {
	state := runstate.State{
		Run:       runstate.RunRunning,
		Authority: runstate.Authority{Epoch: 1, Owner: &runstate.AuthorityOwner{PID: os.Getpid(), Start: oppositePlatformIdentity()}},
	}
	verdict, reason := recordedOwnerProcessMatch(state)
	if verdict != "UNVERIFIABLE" || reason == "" {
		t.Fatalf("verdict=%q reason=%q, want UNVERIFIABLE with a reason", verdict, reason)
	}
}

func TestRecordedOwnerProcessMatchNotApplicableTerminalOwner(t *testing.T) {
	// A terminal run's owner is historical: the token performs no probe even
	// though the recorded owner is this live process.
	state := runstate.State{
		Run:       runstate.RunSucceeded,
		Authority: runstate.Authority{Epoch: 1, Owner: &runstate.AuthorityOwner{PID: os.Getpid(), Start: mustReadCurrentIdentity(t)}},
	}
	verdict, reason := recordedOwnerProcessMatch(state)
	if verdict != "NOT_APPLICABLE" || reason != "" {
		t.Fatalf("verdict=%q reason=%q, want NOT_APPLICABLE for a terminal run", verdict, reason)
	}
}

func TestRecordedOwnerProcessMatchNotApplicableNoOwner(t *testing.T) {
	state := runstate.State{Run: runstate.RunRunning, Authority: runstate.Authority{Epoch: 0}}
	verdict, reason := recordedOwnerProcessMatch(state)
	if verdict != "NOT_APPLICABLE" || reason != "" {
		t.Fatalf("verdict=%q reason=%q, want NOT_APPLICABLE with no owner", verdict, reason)
	}
}

// startIdentityPayload encodes a recorded start identity into the journal
// authority.granted payload shape.
func startIdentityPayload(t *testing.T, identity runstate.StartIdentity) map[string]any {
	t.Helper()
	switch id := identity.(type) {
	case runstate.DarwinStartIdentity:
		return map[string]any{"platform": "darwin", "start_tvsec": id.StartTVSec, "start_tvusec": id.StartTVUsec}
	case runstate.LinuxStartIdentity:
		return map[string]any{"platform": "linux", "boot_id": id.BootID, "start_ticks": id.StartTicks}
	default:
		t.Fatalf("unsupported start identity %T", identity)
		return nil
	}
}

// ownerStatusFixture appends an authority.granted event recording pid and
// identity as the running run's owner.
func ownerStatusFixture(t *testing.T, pid int, identity runstate.StartIdentity) (string, string) {
	t.Helper()
	root, runID := statusFixture(t)
	appendBudgetEvent(t, root, runID, runstate.EventAuthorityGranted, map[string]any{
		"authority_epoch":      1,
		"owner_pid":            pid,
		"owner_start_identity": startIdentityPayload(t, identity),
	})
	return root, runID
}

func TestReadObservationCarriesMatchingVerdict(t *testing.T) {
	root, _ := ownerStatusFixture(t, os.Getpid(), mustReadCurrentIdentity(t))
	report, err := ReadObservation(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if report.RecordedOwnerProcessMatch != "MATCHING" {
		t.Fatalf("verdict = %q, want MATCHING", report.RecordedOwnerProcessMatch)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"recorded_owner_process_match":"MATCHING"`)) {
		t.Fatalf("status JSON %s does not carry MATCHING verdict", encoded)
	}
	if bytes.Contains(encoded, []byte(`"match_unavailable_reason"`)) {
		t.Fatalf("status JSON %s carries match_unavailable_reason for MATCHING", encoded)
	}
}

// TestReadDoesNotSampleProcessTable pins the observation-scope: the fail-closed
// Read path (used by apply, answer, amend, resume, cancel, and logs' selection)
// never probes procid, so it leaves the verdict unpopulated even for a run whose
// owner is this live process.
func TestReadDoesNotSampleProcessTable(t *testing.T) {
	root, runID := ownerStatusFixture(t, os.Getpid(), mustReadCurrentIdentity(t))
	report, err := Read(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	if report.RecordedOwnerProcessMatch != "" || report.MatchUnavailableReason != "" {
		t.Fatalf("Read verdict = %q reason = %q, want unpopulated (no probe on the fail-closed path)",
			report.RecordedOwnerProcessMatch, report.MatchUnavailableReason)
	}
}

func TestReadObservationCarriesUnverifiableReason(t *testing.T) {
	root, _ := ownerStatusFixture(t, os.Getpid(), oppositePlatformIdentity())
	report, err := ReadObservation(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if report.RecordedOwnerProcessMatch != "UNVERIFIABLE" || report.MatchUnavailableReason == "" {
		t.Fatalf("verdict = %q reason = %q, want UNVERIFIABLE with a reason",
			report.RecordedOwnerProcessMatch, report.MatchUnavailableReason)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"recorded_owner_process_match":"UNVERIFIABLE"`)) {
		t.Fatalf("status JSON %s does not carry UNVERIFIABLE verdict", encoded)
	}
	if !bytes.Contains(encoded, []byte(`"match_unavailable_reason":`)) {
		t.Fatalf("status JSON %s does not carry match_unavailable_reason for UNVERIFIABLE", encoded)
	}
}

func TestReadObservationCarriesNotApplicableVerdict(t *testing.T) {
	root, _ := statusFixture(t)
	report, err := ReadObservation(root, "")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"recorded_owner_process_match":"NOT_APPLICABLE"`)) {
		t.Fatalf("status JSON %s does not carry NOT_APPLICABLE verdict for an owner-less run", encoded)
	}
}

func TestStatusScanDisclosesSkippedUnreadableRuns(t *testing.T) {
	root, runID := statusFixture(t)
	legacyID := writeLegacyUnreadableRun(t, root, runID)

	report, err := ReadObservation(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Observation.SkippedUnreadableRuns) != 1 {
		t.Fatalf("skipped = %+v, want exactly one entry", report.Observation.SkippedUnreadableRuns)
	}
	got := report.Observation.SkippedUnreadableRuns[0]
	if got.ID != legacyID || got.Reason != "JOURNAL_CORRUPT" {
		t.Fatalf("skipped[0] = %+v, want {%s JOURNAL_CORRUPT}", got, legacyID)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"skipped_unreadable_runs":[{"id":"legacy-run","reason":"JOURNAL_CORRUPT"}]`)) {
		t.Fatalf("status JSON %s does not disclose the skipped run", encoded)
	}
}

func TestStatusScanSkippedRunsPresentWhenEmpty(t *testing.T) {
	root, _ := statusFixture(t)

	report, err := ReadObservation(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if report.Observation.SkippedUnreadableRuns == nil || len(report.Observation.SkippedUnreadableRuns) != 0 {
		t.Fatalf("skipped = %+v, want present but empty", report.Observation.SkippedUnreadableRuns)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"skipped_unreadable_runs":[]`)) {
		t.Fatalf("status JSON %s does not carry a present-when-empty skipped array", encoded)
	}
}
