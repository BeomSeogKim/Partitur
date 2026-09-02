//go:build linux || darwin

package criterionexec

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

const criterionTemporaryBase = "/tmp"

// AttemptTemporaryDirectory returns the short, attempt-exclusive directory
// exported to criterion commands as TMPDIR, TMP, and TEMP.
func AttemptTemporaryDirectory(runID runstate.RunID, attemptID runstate.AttemptID) (string, error) {
	run, err := compactTemporaryID(string(runID))
	if err != nil {
		return "", err
	}
	attempt, err := compactTemporaryID(string(attemptID))
	if err != nil {
		return "", err
	}
	return filepath.Join(runTemporaryDirectory(run), attempt), nil
}

// CleanupRunTemporary removes every criterion temporary directory owned by a
// terminal run. It is idempotent so recovery can retry it after a crash.
func CleanupRunTemporary(runID runstate.RunID) error {
	run, err := compactTemporaryID(string(runID))
	if err != nil {
		return err
	}
	return os.RemoveAll(runTemporaryDirectory(run))
}

// RunTemporaryDirectory returns the run-owned criterion temporary root for
// cleanup witnesses and recovery residue observation.
func RunTemporaryDirectory(runID runstate.RunID) (string, error) {
	run, err := compactTemporaryID(string(runID))
	if err != nil {
		return "", err
	}
	return runTemporaryDirectory(run), nil
}

func runTemporaryDirectory(compactRunID string) string {
	return filepath.Join(criterionTemporaryBase, "p"+compactRunID)
}

func compactTemporaryID(value string) (string, error) {
	if value == "" {
		return "", errors.New("criterion temporary id is empty")
	}
	if uuid, ok := compactUUID(value); ok {
		return "u" + base64.RawURLEncoding.EncodeToString(uuid), nil
	}
	return "s" + base64.RawURLEncoding.EncodeToString([]byte(value)), nil
}

func compactUUID(value string) ([]byte, bool) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return nil, false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return decoded, err == nil
}
