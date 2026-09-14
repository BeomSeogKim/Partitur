package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

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

func TestStatusTextRendererStaysByteFrozenForDeadOwner(t *testing.T) {
	root, _ := deadOwnerCommandFixture(t)
	report, err := statusprojection.Read(root, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	renderStatusProjection(&rendered, report)
	for _, forbidden := range []string{"authority", "Authority", "owner", "Owner", " pid", "PID"} {
		if strings.Contains(rendered.String(), forbidden) {
			t.Fatalf("text status %q contains forbidden substring %q", rendered.String(), forbidden)
		}
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
