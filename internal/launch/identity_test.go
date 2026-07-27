package launch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func TestReadHandoffRequiresBothNonceCopies(t *testing.T) {
	identity := runstate.ProcessIdentity{
		PID:       42,
		SessionID: 42,
		Start: runstate.LinuxStartIdentity{
			BootID:     "boot",
			StartTicks: "10",
		},
	}
	for _, test := range []struct {
		name          string
		markerNonce   string
		identityNonce string
		wantMatched   bool
	}{
		{
			name:          "matching",
			markerNonce:   "expected",
			identityNonce: "expected",
			wantMatched:   true,
		},
		{
			name:          "marker_mismatch_ignores_pair",
			markerNonce:   "stale",
			identityNonce: "expected",
		},
		{
			name:          "identity_mismatch_ignores_pair",
			markerNonce:   "expected",
			identityNonce: "stale",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(
				filepath.Join(directory, markerName),
				[]byte(test.markerNonce),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			contents, err := encodeIdentity(test.identityNonce, identity)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				filepath.Join(directory, identityName),
				contents,
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			got, matched, err := ReadHandoff(directory, "expected")
			if err != nil {
				t.Fatal(err)
			}
			if matched != test.wantMatched {
				t.Fatalf("matched = %v, want %v", matched, test.wantMatched)
			}
			if matched && (got.PID != identity.PID ||
				got.SessionID != identity.SessionID ||
				got.Start != identity.Start) {
				t.Fatalf("identity = %+v, want %+v", got, identity)
			}
			if !matched && got.Start != nil {
				t.Fatalf("stale pair leaked identity %+v", got)
			}
		})
	}
}

func TestReadHandoffRejectsUnequalPIDAndSession(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(directory, markerName),
		[]byte("expected"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	contents := []byte(`{
		"nonce":"expected",
		"pid":42,
		"session_id":41,
		"start_identity":{
			"platform":"linux",
			"boot_id":"boot",
			"start_ticks":"10"
		}
	}`)
	if err := os.WriteFile(
		filepath.Join(directory, identityName),
		contents,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, matched, err := ReadHandoff(
		directory,
		"expected",
	); !errors.Is(err, ErrInvalidHandoff) || matched {
		t.Fatalf("ReadHandoff matched=%v error=%v", matched, err)
	}
}

func TestReadHandoffRejectsUnknownFields(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(directory, markerName),
		[]byte("expected"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	contents := []byte(`{
		"nonce":"expected",
		"pid":42,
		"session_id":42,
		"start_identity":{
			"platform":"linux",
			"boot_id":"boot",
			"start_ticks":"10"
		},
		"unprescribed":true
	}`)
	if err := os.WriteFile(
		filepath.Join(directory, identityName),
		contents,
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, matched, err := ReadHandoff(
		directory,
		"expected",
	); !errors.Is(err, ErrInvalidHandoff) || matched {
		t.Fatalf("ReadHandoff matched=%v error=%v", matched, err)
	}
}

func TestReadHandoffRejectsInvalidStartIdentity(t *testing.T) {
	for _, test := range []struct {
		name  string
		start string
	}{
		{
			name: "linux_ticks",
			start: `{
				"platform":"linux",
				"boot_id":"boot",
				"start_ticks":"not-a-number"
			}`,
		},
		{
			name: "darwin_microseconds",
			start: `{
				"platform":"darwin",
				"start_tvsec":1,
				"start_tvusec":1000000
			}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(
				filepath.Join(directory, markerName),
				[]byte("expected"),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			contents := []byte(`{
				"nonce":"expected",
				"pid":42,
				"session_id":42,
				"start_identity":` + test.start + `
			}`)
			if err := os.WriteFile(
				filepath.Join(directory, identityName),
				contents,
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			if _, matched, err := ReadHandoff(
				directory,
				"expected",
			); !errors.Is(err, ErrInvalidHandoff) || matched {
				t.Fatalf("ReadHandoff matched=%v error=%v", matched, err)
			}
		})
	}
}
