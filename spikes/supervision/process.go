package supervision

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"syscall"
	"time"
)

type processRecord struct {
	PID      int
	PPID     int
	PGID     int
	SID      int
	Start    string
	IsZombie bool
}

func currentIdentity() (processRecord, error) {
	return processByPID(os.Getpid())
}

// identityMatches fails closed. A process that is genuinely gone is "not live"
// and safe to report as such, but an inspection error the caller cannot explain —
// permission, sandbox policy — is NOT evidence of absence. Collapsing the two
// would let a live holder be treated as dead and its lease reclaimed.
func identityMatches(pid int, start string) (bool, error) {
	process, err := processByPID(pid)
	if err != nil {
		if isProcessGone(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect pid %d: %w", pid, err)
	}
	return !process.IsZombie && process.Start == start, nil
}

// isProcessGone reports whether err means "that process does not exist", which is
// an ordinary race between enumerating and inspecting, not a visibility failure.
func isProcessGone(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, fs.ErrNotExist)
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
		time.Sleep(10 * time.Millisecond)
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
		time.Sleep(10 * time.Millisecond)
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
	// Also signal individual identities. This covers a process that changed
	// groups between enumeration and the group signal.
	for _, process := range records {
		matches, err := identityMatches(process.PID, process.Start)
		if err != nil {
			// Cannot verify: do not signal a PID we cannot confirm is still the
			// process we recorded, since it may have been reused.
			continue
		}
		if matches {
			_ = syscall.Kill(process.PID, signal)
		}
	}
}

// liveSessionMembers fails closed: an enumeration error is reported rather than
// returning an empty set, because an empty set is indistinguishable from "swept
// clean" and would let the caller claim zero survivors it never verified.
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
