package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/procid"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
	statusprojection "github.com/BeomSeogKim/Partitur/internal/status"
)

func TestStatusJSONCarriesAuthorityObject(t *testing.T) {
	root, _ := deadOwnerCommandFixture(t)
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"status", "run-1", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("status exit code = %d, want 0; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var document struct {
		Schema    string `json:"schema"`
		Authority struct {
			Epoch uint64 `json:"epoch"`
			Owner *struct {
				PID           int `json:"pid"`
				StartIdentity struct {
					Platform    string `json:"platform"`
					StartTVSec  uint64 `json:"start_tvsec"`
					StartTVUsec uint64 `json:"start_tvusec"`
				} `json:"start_identity"`
			} `json:"owner"`
		} `json:"authority"`
	}
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode status JSON %q: %v", stdout.String(), err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("second status JSON decode = %v, want io.EOF; stdout=%q", err, stdout.String())
	}
	if document.Schema != "partitur/status+json;v=1" || document.Authority.Epoch != 1 || document.Authority.Owner == nil {
		t.Fatalf("status document = %+v, want schema v1 and authority epoch 1 with owner", document)
	}
	owner := document.Authority.Owner
	if owner.PID != 89528 || owner.StartIdentity.Platform != "darwin" || owner.StartIdentity.StartTVSec != 1789292195 || owner.StartIdentity.StartTVUsec != 717953 {
		t.Fatalf("authority owner = %+v, want recorded Darwin owner", owner)
	}
}

func TestStatusTextRendersOwnerAndVerdict(t *testing.T) {
	// The owner is this live process, so the verdict is MATCHING on every
	// platform. This e2e test only proves the CLI plumbs the owner and verdict
	// through to the text surface; the per-state mapping is covered by the
	// internal/status unit tests.
	root, _ := liveOwnerCommandFixture(t)
	report, err := statusprojection.ReadObservation(root, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	renderStatusProjection(&rendered, report)
	ownerPrefix := fmt.Sprintf("Authority owner: pid %d ", os.Getpid())
	if !strings.Contains(rendered.String(), ownerPrefix) {
		t.Fatalf("text status %q does not render owner line %q", rendered.String(), ownerPrefix)
	}
	if !strings.Contains(rendered.String(), "process match MATCHING") {
		t.Fatalf("text status %q does not render the MATCHING verdict", rendered.String())
	}
}

func TestStatusTextOmitsOwnerLineWithoutOwner(t *testing.T) {
	root, _ := resumeFixture(t, "")
	report, err := statusprojection.Read(root, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	renderStatusProjection(&rendered, report)
	for _, forbidden := range []string{"Authority owner", "process match", "Skipped unreadable"} {
		if strings.Contains(rendered.String(), forbidden) {
			t.Fatalf("text status %q contains %q for an owner-less, unskipped run", rendered.String(), forbidden)
		}
	}
}

func TestStatusTextRendersSkippedUnreadableRuns(t *testing.T) {
	report := statusprojection.Report{
		Observation: statusprojection.Observation{
			SkippedUnreadableRuns: []statusprojection.SkippedUnreadableRun{
				{ID: "legacy-run", Reason: "JOURNAL_CORRUPT"},
				{ID: "other-run", Reason: "UNSUPPORTED_EVENT_TYPE"},
			},
		},
	}
	var rendered bytes.Buffer
	renderStatusProjection(&rendered, report)
	want := "Skipped unreadable runs: 2 (legacy-run: JOURNAL_CORRUPT, other-run: UNSUPPORTED_EVENT_TYPE)"
	if !strings.Contains(rendered.String(), want) {
		t.Fatalf("text status %q does not render skipped-run line %q", rendered.String(), want)
	}
}

func TestStatusJSONCarriesProcessMatchAndObservation(t *testing.T) {
	// A live current-process owner yields MATCHING on every platform, so this
	// e2e assertion of the CLI plumbing is platform-independent by construction.
	root, _ := liveOwnerCommandFixture(t)
	t.Chdir(root)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"status", "run-1", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("status exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var document struct {
		RecordedOwnerProcessMatch string `json:"recorded_owner_process_match"`
		Observation               struct {
			SkippedUnreadableRuns []struct {
				ID     string `json:"id"`
				Reason string `json:"reason"`
			} `json:"skipped_unreadable_runs"`
		} `json:"observation"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatalf("decode status JSON %q: %v", stdout.String(), err)
	}
	if document.RecordedOwnerProcessMatch != "MATCHING" {
		t.Fatalf("recorded_owner_process_match = %q, want MATCHING", document.RecordedOwnerProcessMatch)
	}
	if document.Observation.SkippedUnreadableRuns == nil {
		t.Fatalf("observation.skipped_unreadable_runs absent; want present (empty) for an explicit selection")
	}
	if len(document.Observation.SkippedUnreadableRuns) != 0 {
		t.Fatalf("skipped runs = %+v, want empty for an explicit selection", document.Observation.SkippedUnreadableRuns)
	}
}

func deadOwnerCommandFixture(t *testing.T) (string, *runstore.Store) {
	t.Helper()
	root, store := resumeFixture(t, "")
	if err := store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
		_, err := transaction.At("fixture.authority.granted").Append(resumeEvent("run-1", runstate.EventAuthorityGranted, map[string]any{
			"authority_epoch": 1,
			"owner_pid":       89528,
			"owner_start_identity": map[string]any{
				"platform":     "darwin",
				"start_tvsec":  1789292195,
				"start_tvusec": 717953,
			},
		}))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return root, store
}

// liveOwnerCommandFixture records this live process as the running run's
// authority owner. A process-table sample of the current process's own recorded
// identity is MATCHING on every platform, so verdict-carrying e2e tests built on
// it are platform-independent by construction.
func liveOwnerCommandFixture(t *testing.T) (string, *runstore.Store) {
	t.Helper()
	root, store := resumeFixture(t, "")
	identity, err := procid.Read(os.Getpid())
	if err != nil {
		t.Fatalf("read current process identity: %v", err)
	}
	if err := store.Mutate("run-1", "", func(transaction *runstore.Txn) error {
		_, err := transaction.At("fixture.authority.granted").Append(resumeEvent("run-1", runstate.EventAuthorityGranted, map[string]any{
			"authority_epoch":      1,
			"owner_pid":            os.Getpid(),
			"owner_start_identity": commandStartIdentityPayload(t, identity),
		}))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return root, store
}

// commandStartIdentityPayload encodes a recorded start identity into the journal
// authority.granted payload shape.
func commandStartIdentityPayload(t *testing.T, identity runstate.StartIdentity) map[string]any {
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
