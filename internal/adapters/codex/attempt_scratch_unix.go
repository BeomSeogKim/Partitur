//go:build linux || darwin

package codex

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
)

const attemptScratchBase = "/tmp"

func attemptScratchDirectory(runID, attemptID string) (string, error) {
	run, err := compactScratchID(runID, 'u')
	if err != nil {
		return "", err
	}
	attempt, err := compactScratchID(attemptID, 'c')
	if err != nil {
		return "", err
	}
	return filepath.Join(attemptScratchBase, "p"+run, attempt), nil
}

func prepareAttemptScratch(workdir, runID, attemptID string) (string, error) {
	scratch, err := attemptScratchDirectory(runID, attemptID)
	if err != nil {
		return "", err
	}
	if err := ensurePrivateScratchDirectory(filepath.Dir(scratch)); err != nil {
		return "", fmt.Errorf("secure run scratch root: %w", err)
	}
	if err := ensurePrivateScratchDirectory(scratch); err != nil {
		return "", fmt.Errorf("secure attempt scratch directory: %w", err)
	}
	resolvedWorkdir, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		return "", fmt.Errorf("resolve workdir: %w", err)
	}
	resolvedScratch, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		return "", fmt.Errorf("resolve attempt scratch: %w", err)
	}
	if !outside(resolvedWorkdir, resolvedScratch) || !outside(resolvedScratch, resolvedWorkdir) {
		return "", errors.New("attempt scratch and workdir must not contain each other")
	}
	return scratch, nil
}

func childEnvironment(parent []string, scratch string) []string {
	child := make([]string, 0, len(parent)+5)
	for _, value := range parent {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "TMPDIR", "TMP", "TEMP", "GOTMPDIR", "GOCACHE":
			continue
		}
		child = append(child, value)
	}
	return append(child,
		"TMPDIR="+scratch,
		"TMP="+scratch,
		"TEMP="+scratch,
		"GOTMPDIR="+scratch,
		"GOCACHE="+filepath.Join(scratch, "g"),
	)
}

func compactScratchID(value string, prefix byte) (string, error) {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return "", errors.New("adapter scratch id is not a UUIDv7")
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	if err != nil || decoded[6]>>4 != 7 || decoded[8]&0xc0 != 0x80 {
		return "", errors.New("adapter scratch id is not a UUIDv7")
	}
	return string(prefix) + base64.RawURLEncoding.EncodeToString(decoded), nil
}

func ensurePrivateScratchDirectory(path string) error {
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
