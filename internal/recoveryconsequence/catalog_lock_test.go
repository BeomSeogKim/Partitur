package recoveryconsequence

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

func TestDurableConsequenceCatalogMatchesDesignClosure(t *testing.T) {
	designPath := filepath.Join("..", "..", "docs", "DESIGN.md")
	design, err := os.ReadFile(designPath)
	if err != nil {
		t.Fatal(err)
	}
	const anchor = "Durable consequences close before a prepare can exist."
	if strings.Count(string(design), anchor) != 1 {
		t.Fatalf("%q occurrences = %d, want 1", anchor, strings.Count(string(design), anchor))
	}
	section := string(design[strings.Index(string(design), anchor):])
	next := strings.Index(section, "This is narrower than quiescing")
	if next < 0 {
		t.Fatal("durable-consequence closure has no following paragraph boundary")
	}
	section = section[:next]
	listStart := strings.Index(section, "selected by ")
	listEnd := strings.Index(section, ". These are")
	if listStart < 0 || listEnd < listStart {
		t.Fatal("durable-consequence closure has no parseable selected-case list")
	}
	list := section[listStart+len("selected by ") : listEnd]
	list = strings.ReplaceAll(list, "`", "")
	re := regexp.MustCompile(`(?:RC-RESUME-)?([0-9]{3})(?:–([0-9]{3}))?`)
	matches := re.FindAllStringSubmatch(list, -1)
	if len(matches) == 0 {
		t.Fatal("durable-consequence closure parsed no recovery cases")
	}
	want := make([]recovery.CaseID, 0, len(matches))
	for _, match := range matches {
		if match[2] == "" {
			want = append(want, recovery.CaseID("RC-RESUME-"+match[1]))
			continue
		}
		for value := match[1]; ; value = nextID(value) {
			want = append(want, recovery.CaseID("RC-RESUME-"+value))
			if value == match[2] {
				break
			}
			if nextID(value) == "" {
				t.Fatalf("unparseable recovery-case range %q", match[0])
			}
		}
	}
	slices.Sort(want)
	if slices.Contains(want, recovery.CaseID("RC-RESUME-047")) {
		want = slices.DeleteFunc(want, func(caseID recovery.CaseID) bool { return caseID == "RC-RESUME-047" })
	} else {
		t.Fatal("closure did not contain mechanically exempt RC-RESUME-047")
	}
	got := Cases()
	if !slices.Equal(got, want) {
		t.Fatalf("durable-consequence catalog = %v, want exact DESIGN §6 closure %v", got, want)
	}
}

func TestCatalogAndRecoveryDispatchFailClosed(t *testing.T) {
	if err := Apply(context.Background(), HandlerContext{}, "RC-RESUME-unknown", recovery.Action{}); !errors.Is(err, ErrUnrecognizedCase) {
		t.Fatalf("unknown catalog case error = %v, want ErrUnrecognizedCase", err)
	}
	executor, err := os.ReadFile(filepath.Join("..", "recoveryexec", "executor.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, caller := range []string{"recoveryconsequence.ApplyStep", "recoveryconsequence.Apply"} {
		if !strings.Contains(string(executor), caller) {
			t.Fatalf("recovery executor no longer delegates %s to the consequence catalog", caller)
		}
	}
}

func TestFrozenRoutePayloadRejectsRecordHashMismatch(t *testing.T) {
	root := t.TempDir()
	store, err := runstore.New(root, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	const runID = runstate.RunID("run-1")
	record := []byte(`{"proposal_id":"proposal-1","attempt_id":"attempt-1","emitted_id":"emitted-1","requires_decision":true}`)
	path := filepath.Join(root, ".partitur", "runs", string(runID), "proposals")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "proposal-1.json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
	source := runstate.Event{AttemptID: "attempt-1"}
	raised := map[string]any{"proposal_id": "proposal-1", "decision_id": "decision-1", "blocking": true}
	descriptor := map[string]any{"proposal_record_hash": rawHash(record), "reason": "requires_decision"}
	payload, err := frozenRoutePayload(HandlerContext{Store: store, RunID: runID}, source, raised, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if payload["emitted_id"] != "emitted-1" || payload["decision_id"] != "decision-1" || payload["blocking"] != true {
		t.Fatalf("frozen route payload = %#v", payload)
	}
	if err := os.WriteFile(filepath.Join(path, "proposal-1.json"), append(append([]byte(nil), record...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := frozenRoutePayload(HandlerContext{Store: store, RunID: runID}, source, raised, descriptor); !errors.Is(err, ErrMissingProposalRecord) {
		t.Fatalf("tampered record error = %v, want ErrMissingProposalRecord", err)
	}
}

func nextID(value string) string {
	number, err := strconv.Atoi(value)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%03d", number+1)
}
