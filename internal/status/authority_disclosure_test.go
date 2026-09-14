package status

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

const (
	deadOwnerPID         = 89528
	deadOwnerStartTVSec  = uint64(1789292195)
	deadOwnerStartTVUsec = uint64(717953)
)

func TestStatusDisclosesDeadOwnerAuthority(t *testing.T) {
	root, runID := deadOwnerStatusFixture(t)
	report, err := Read(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range [][]byte{
		[]byte(`"authority":{"epoch":1`),
		[]byte(`"pid":89528`),
		[]byte(`"platform":"darwin"`),
		[]byte(`"start_tvsec":1789292195`),
		[]byte(`"start_tvusec":717953`),
	} {
		if !bytes.Contains(encoded, field) {
			t.Fatalf("status JSON %s does not contain %s", encoded, field)
		}
	}
}

func TestStatusAuthorityFencedVsNeverGrantedDistinguishable(t *testing.T) {
	fenced := authorityProjection(runstate.State{
		Run:       runstate.RunCancelled,
		Authority: runstate.Authority{Epoch: 2},
	})
	neverGranted := authorityProjection(runstate.State{
		Run:       runstate.RunRunning,
		Authority: runstate.Authority{Epoch: 0},
	})
	fencedJSON, err := json.Marshal(fenced)
	if err != nil {
		t.Fatal(err)
	}
	neverGrantedJSON, err := json.Marshal(neverGranted)
	if err != nil {
		t.Fatal(err)
	}
	if string(fencedJSON) != `{"epoch":2,"owner":null}` {
		t.Fatalf("fenced authority JSON = %s, want epoch 2 with null owner", fencedJSON)
	}
	if string(neverGrantedJSON) != `{"epoch":0,"owner":null}` {
		t.Fatalf("never-granted authority JSON = %s, want epoch 0 with null owner", neverGrantedJSON)
	}
	if bytes.Equal(fencedJSON, neverGrantedJSON) {
		t.Fatalf("fenced and never-granted authority JSON are equal: %s", fencedJSON)
	}
}

func TestStatusTerminalRunDoesNotPaintOwner(t *testing.T) {
	owner := &runstate.AuthorityOwner{
		PID: deadOwnerPID,
		Start: runstate.DarwinStartIdentity{
			StartTVSec:  deadOwnerStartTVSec,
			StartTVUsec: deadOwnerStartTVUsec,
		},
	}
	terminal := authorityProjection(runstate.State{
		Run:       runstate.RunFailed,
		Authority: runstate.Authority{Epoch: 1, Owner: owner},
	})
	if terminal.Epoch != 1 || terminal.Owner != nil {
		t.Fatalf("terminal authority = %+v, want epoch 1 with null owner", terminal)
	}
	running := authorityProjection(runstate.State{
		Run:       runstate.RunRunning,
		Authority: runstate.Authority{Epoch: 1, Owner: owner},
	})
	if running.Owner == nil || running.Owner.PID != deadOwnerPID {
		t.Fatalf("running authority = %+v, want recorded owner pid %d", running, deadOwnerPID)
	}
}

func TestStatusDeadOwnerKeepsRecoveryAndLifecycle(t *testing.T) {
	root, runID := deadOwnerStatusFixture(t)
	report, err := Read(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Recovery.State != "NOT_REQUIRED" {
		t.Fatalf("recovery state = %q, want NOT_REQUIRED", report.Recovery.State)
	}
	if report.Run.Lifecycle != "RUNNING" {
		t.Fatalf("run lifecycle = %q, want RUNNING", report.Run.Lifecycle)
	}
}

func deadOwnerStatusFixture(t *testing.T) (string, string) {
	t.Helper()
	root, runID := statusFixture(t)
	appendBudgetEvent(t, root, runID, runstate.EventAuthorityGranted, map[string]any{
		"authority_epoch": 1,
		"owner_pid":       deadOwnerPID,
		"owner_start_identity": map[string]any{
			"platform":     "darwin",
			"start_tvsec":  deadOwnerStartTVSec,
			"start_tvusec": deadOwnerStartTVUsec,
		},
	})
	return root, runID
}
