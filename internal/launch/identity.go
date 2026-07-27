package launch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

const (
	identityName = "identity.json"
	markerName   = "marker"
)

type identityDocument struct {
	Nonce         string          `json:"nonce"`
	PID           int             `json:"pid"`
	SessionID     int             `json:"session_id"`
	StartIdentity json.RawMessage `json:"start_identity"`
}

type linuxStartDocument struct {
	Platform   string `json:"platform"`
	BootID     string `json:"boot_id"`
	StartTicks string `json:"start_ticks"`
}

type darwinStartDocument struct {
	Platform    string `json:"platform"`
	StartTVSec  uint64 `json:"start_tvsec"`
	StartTVUsec uint64 `json:"start_tvusec"`
}

func encodeIdentity(
	nonce string,
	identity runstate.ProcessIdentity,
) ([]byte, error) {
	start, err := encodeStartIdentity(identity.Start)
	if err != nil {
		return nil, err
	}
	return json.Marshal(identityDocument{
		Nonce:         nonce,
		PID:           identity.PID,
		SessionID:     identity.SessionID,
		StartIdentity: start,
	})
}

func encodeStartIdentity(identity runstate.StartIdentity) ([]byte, error) {
	switch identity := identity.(type) {
	case runstate.LinuxStartIdentity:
		return json.Marshal(linuxStartDocument{
			Platform:   identity.Platform(),
			BootID:     identity.BootID,
			StartTicks: identity.StartTicks,
		})
	case runstate.DarwinStartIdentity:
		return json.Marshal(darwinStartDocument{
			Platform:    identity.Platform(),
			StartTVSec:  identity.StartTVSec,
			StartTVUsec: identity.StartTVUsec,
		})
	default:
		return nil, fmt.Errorf("%w: unsupported start identity %T", ErrInvalidHandoff, identity)
	}
}

// ReadHandoff returns no identity when either nonce copy is stale. A malformed
// matching handoff is an error rather than an absent observation.
func ReadHandoff(
	launchDir string,
	expectedNonce string,
) (runstate.ProcessIdentity, bool, error) {
	marker, err := os.ReadFile(filepath.Join(launchDir, markerName))
	if errors.Is(err, os.ErrNotExist) {
		return runstate.ProcessIdentity{}, false, nil
	}
	if err != nil {
		return runstate.ProcessIdentity{}, false, fmt.Errorf("read launch marker: %w", err)
	}
	if string(marker) != expectedNonce {
		return runstate.ProcessIdentity{}, false, nil
	}
	contents, err := os.ReadFile(filepath.Join(launchDir, identityName))
	if errors.Is(err, os.ErrNotExist) {
		return runstate.ProcessIdentity{}, false, nil
	}
	if err != nil {
		return runstate.ProcessIdentity{}, false, fmt.Errorf("read launch identity: %w", err)
	}
	document, err := decodeIdentity(contents)
	if err != nil {
		return runstate.ProcessIdentity{}, false, err
	}
	if document.nonce != expectedNonce {
		return runstate.ProcessIdentity{}, false, nil
	}
	return document.identity, true, nil
}

type decodedIdentity struct {
	nonce    string
	identity runstate.ProcessIdentity
}

func decodeIdentity(contents []byte) (decodedIdentity, error) {
	var document identityDocument
	if err := decodeExact(contents, &document); err != nil {
		return decodedIdentity{}, fmt.Errorf("%w: identity.json: %v", ErrInvalidHandoff, err)
	}
	if document.Nonce == "" || document.PID <= 0 ||
		document.SessionID <= 0 || document.PID != document.SessionID {
		return decodedIdentity{}, fmt.Errorf(
			"%w: nonce and positive equal pid/session_id are required",
			ErrInvalidHandoff,
		)
	}
	start, err := decodeStartIdentity(document.StartIdentity)
	if err != nil {
		return decodedIdentity{}, err
	}
	return decodedIdentity{
		nonce: document.Nonce,
		identity: runstate.ProcessIdentity{
			PID:       document.PID,
			SessionID: document.SessionID,
			Start:     start,
		},
	}, nil
}

func decodeStartIdentity(contents []byte) (runstate.StartIdentity, error) {
	var tagged struct {
		Platform string `json:"platform"`
	}
	if err := json.Unmarshal(contents, &tagged); err != nil {
		return nil, fmt.Errorf("%w: start_identity: %v", ErrInvalidHandoff, err)
	}
	switch tagged.Platform {
	case "linux":
		var document linuxStartDocument
		if err := decodeExact(contents, &document); err != nil ||
			document.BootID == "" || !validStartTicks(document.StartTicks) {
			return nil, fmt.Errorf("%w: invalid Linux start_identity", ErrInvalidHandoff)
		}
		return runstate.LinuxStartIdentity{
			BootID:     document.BootID,
			StartTicks: document.StartTicks,
		}, nil
	case "darwin":
		var document darwinStartDocument
		if err := decodeExact(contents, &document); err != nil ||
			document.StartTVUsec >= 1_000_000 {
			return nil, fmt.Errorf("%w: invalid Darwin start_identity", ErrInvalidHandoff)
		}
		return runstate.DarwinStartIdentity{
			StartTVSec:  document.StartTVSec,
			StartTVUsec: document.StartTVUsec,
		}, nil
	default:
		return nil, fmt.Errorf(
			"%w: unknown start_identity platform %q",
			ErrInvalidHandoff,
			tagged.Platform,
		)
	}
}

func validStartTicks(value string) bool {
	if value == "" {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

func decodeExact(contents []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
