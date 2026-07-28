package adapter

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"syscall"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

type processRecord struct {
	PID      int
	PPID     int
	PGID     int
	SID      int
	Start    string
	IsZombie bool
}

type sessionController interface {
	verifyEmpty(sid int, leaderStart string) (bool, error)
	terminate(sid int, leaderStart string, grace time.Duration) error
}

type systemSessionController struct{}

// SessionEmpty observes whether a recorded launch session has no live member.
// It never signals or otherwise mutates a process.
func SessionEmpty(identity runstate.ProcessIdentity) (bool, error) {
	if identity.SessionID <= 0 || identity.Start == nil {
		return false, errors.New("recorded session identity is incomplete")
	}
	start, err := processStartIdentity(identity.Start)
	if err != nil {
		return false, err
	}
	members, err := liveSessionMembers(identity.SessionID, start)
	if err != nil {
		return false, err
	}
	return len(members) == 0, nil
}

func (systemSessionController) verifyEmpty(sid int, leaderStart string) (bool, error) {
	members, err := liveSessionMembers(sid, leaderStart)
	return len(members) == 0, err
}

func (systemSessionController) terminate(sid int, leaderStart string, grace time.Duration) error {
	return sweepSession(sid, leaderStart, grace)
}

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

func isProcessGone(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, fs.ErrNotExist)
}

func sweepSession(sid int, leaderStart string, grace time.Duration) error {
	if sid <= 0 {
		return errors.New("invalid session id")
	}
	if leaderStart == "" {
		return errors.New("adapter session leader identity is absent")
	}
	if err := signalSession(sid, leaderStart, syscall.SIGTERM); err != nil {
		return err
	}
	deadline := time.Now().Add(grace)
	for {
		members, err := liveSessionMembers(sid, leaderStart)
		if err != nil {
			return err
		}
		if len(members) == 0 {
			return nil
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	for {
		members, err := liveSessionMembers(sid, leaderStart)
		if err != nil {
			return err
		}
		if len(members) == 0 {
			return nil
		}
		if err := signalRecords(members, syscall.SIGKILL); err != nil {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func signalSession(sid int, leaderStart string, signal syscall.Signal) error {
	members, err := liveSessionMembers(sid, leaderStart)
	if err != nil {
		return err
	}
	return signalRecords(members, signal)
}

func signalRecords(records []processRecord, signal syscall.Signal) error {
	return signalRecordsWith(records, signal, syscall.Kill)
}

func signalRecordsWith(records []processRecord, signal syscall.Signal, kill func(int, syscall.Signal) error) error {
	groups := make(map[int]struct{})
	for _, process := range records {
		if process.PGID > 0 {
			groups[process.PGID] = struct{}{}
		}
	}
	orderedGroups := make([]int, 0, len(groups))
	for group := range groups {
		orderedGroups = append(orderedGroups, group)
	}
	sort.Ints(orderedGroups)
	for _, group := range orderedGroups {
		// Every enumerated member is also signalled below after an identity
		// re-check. A group signal may race the group's last member exiting;
		// the individual pass and the next enumeration remain authoritative.
		_ = kill(-group, signal)
	}

	for _, process := range records {
		matches, err := identityMatches(process.PID, process.Start)
		if err != nil {
			return err
		}
		if !matches {
			continue
		}
		if err := kill(process.PID, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf("signal pid %d: %w", process.PID, err)
		}
	}
	return nil
}

func liveSessionMembers(sid int, leaderStart string) ([]processRecord, error) {
	if err := verifySessionLeaderIdentity(sid, leaderStart); err != nil {
		return nil, err
	}
	processes, err := listProcesses()
	if err != nil {
		return nil, fmt.Errorf("enumerate processes: %w", err)
	}
	members := make([]processRecord, 0)
	for _, process := range processes {
		if process.SID == sid && !process.IsZombie {
			members = append(members, process)
		}
	}
	sort.Slice(members, func(i, j int) bool {
		return members[i].PID < members[j].PID
	})
	return members, nil
}

func verifySessionLeaderIdentity(sid int, expectedStart string) error {
	if expectedStart == "" {
		return errors.New("adapter session leader identity is absent")
	}
	process, err := processByPID(sid)
	if err != nil {
		if isProcessGone(err) {
			return nil
		}
		return fmt.Errorf("inspect adapter session leader pid %d: %w", sid, err)
	}
	if process.Start != expectedStart {
		return fmt.Errorf("adapter session leader pid %d was reused", sid)
	}
	return nil
}
