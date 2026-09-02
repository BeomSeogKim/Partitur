//go:build linux || darwin

package criterionexec

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

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

func createAttemptTemporaryDirectory(runID runstate.RunID, attemptID runstate.AttemptID) (string, error) {
	temporary, err := AttemptTemporaryDirectory(runID, attemptID)
	if err != nil {
		return "", err
	}
	runTemporary, err := RunTemporaryDirectory(runID)
	if err != nil {
		return "", err
	}
	if err := ensurePrivateTemporaryDirectory(runTemporary); err != nil {
		return "", fmt.Errorf("secure run temporary root: %w", err)
	}
	if err := ensurePrivateTemporaryDirectory(temporary); err != nil {
		return "", fmt.Errorf("secure attempt temporary directory: %w", err)
	}
	return temporary, nil
}

func ensurePrivateTemporaryDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("path is not a directory")
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("directory ownership is unavailable")
	}
	if uint64(status.Uid) != uint64(os.Geteuid()) {
		return errors.New("directory is owned by another user")
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("directory mode is %04o, want 0700", info.Mode().Perm())
	}
	return nil
}

// CleanupRunTemporary removes every criterion temporary directory owned by a
// terminal run. It is idempotent so recovery can retry it after a crash.
func CleanupRunTemporary(runID runstate.RunID) error {
	run, err := compactTemporaryID(string(runID))
	if err != nil {
		// Creation rejects non-UUIDv7 identities, so an invalid identity cannot
		// own a criterion temporary root under this layout.
		return nil
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
	if uuid, ok := compactUUID(value); ok {
		return "u" + base64.RawURLEncoding.EncodeToString(uuid), nil
	}
	return "", errors.New("criterion temporary id is not a UUIDv7")
}

func compactUUID(value string) ([]byte, bool) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return nil, false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return decoded, err == nil && decoded[6]>>4 == 7 && decoded[8]&0xc0 == 0x80
}
