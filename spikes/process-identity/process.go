package processidentity

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

type startIdentity struct {
	Platform    string `json:"platform"`
	BootID      string `json:"boot_id,omitempty"`
	StartTicks  string `json:"start_ticks,omitempty"`
	StartTVSec  uint64 `json:"start_tvsec,omitempty"`
	StartTVUsec uint64 `json:"start_tvusec,omitempty"`
}

type processRecord struct {
	PID      int           `json:"pid"`
	PPID     int           `json:"ppid"`
	PGID     int           `json:"pgid"`
	SID      int           `json:"session_id"`
	Start    startIdentity `json:"start_identity"`
	IsZombie bool          `json:"is_zombie,omitempty"`
}

type launchHandoff struct {
	Nonce   string        `json:"nonce"`
	Process processRecord `json:"process"`
}

func currentIdentity() (processRecord, error) {
	return processByPID(os.Getpid())
}

func identityMatches(record processRecord) (bool, error) {
	current, err := processByPID(record.PID)
	if err != nil {
		if isProcessGone(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect pid %d: %w", record.PID, err)
	}
	return !current.IsZombie && current.Start == record.Start, nil
}

func isProcessGone(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, fs.ErrNotExist)
}

func liveSessionMembers(sid int) ([]processRecord, error) {
	processes, err := listProcesses()
	if err != nil {
		return nil, fmt.Errorf("enumerate processes: %w", err)
	}
	var members []processRecord
	for _, process := range processes {
		if process.SID == sid && !process.IsZombie {
			members = append(members, process)
		}
	}
	return members, nil
}

func sweepSession(sid int, grace time.Duration) error {
	if sid <= 0 {
		return errors.New("invalid session id")
	}
	if err := signalSession(sid, syscall.SIGTERM); err != nil {
		return err
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		members, err := liveSessionMembers(sid)
		if err != nil {
			return err
		}
		if len(members) == 0 {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	killDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(killDeadline) {
		members, err := liveSessionMembers(sid)
		if err != nil {
			return err
		}
		if len(members) == 0 {
			return nil
		}
		signalRecords(members, syscall.SIGKILL)
		time.Sleep(5 * time.Millisecond)
	}
	members, err := liveSessionMembers(sid)
	if err != nil {
		return err
	}
	return fmt.Errorf("session %d survived SIGKILL: %+v", sid, members)
}

func signalSession(sid int, signal syscall.Signal) error {
	members, err := liveSessionMembers(sid)
	if err != nil {
		return err
	}
	signalRecords(members, signal)
	return nil
}

func signalRecords(records []processRecord, signal syscall.Signal) {
	groups := map[int]bool{}
	for _, process := range records {
		if process.PGID > 0 {
			groups[process.PGID] = true
		}
	}
	var orderedGroups []int
	for group := range groups {
		orderedGroups = append(orderedGroups, group)
	}
	sort.Ints(orderedGroups)
	for _, group := range orderedGroups {
		_ = syscall.Kill(-group, signal)
	}
	for _, process := range records {
		matches, err := identityMatches(process)
		if err == nil && matches {
			_ = syscall.Kill(process.PID, signal)
		}
	}
}

func writeDurableJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
