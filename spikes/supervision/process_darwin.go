//go:build darwin

package supervision

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	procInfoCallListPIDs = 1
	procInfoCallPIDInfo  = 2
	procAllPIDs          = 1
	procUIDOnly          = 4
	procPIDTBSDInfo      = 3
	darwinZombieStatus   = 5
)

// Layout from <sys/proc_info.h> struct proc_bsdinfo. The spike intentionally
// calls the public proc_info syscall directly, so it does not need cgo.
type procBSDInfo struct {
	Flags         uint32
	Status        uint32
	XStatus       uint32
	PID           uint32
	PPID          uint32
	UID           uint32
	GID           uint32
	RUID          uint32
	RGID          uint32
	SVUID         uint32
	SVGID         uint32
	Reserved      uint32
	Command       [16]byte
	Name          [32]byte
	NFiles        uint32
	PGID          uint32
	PJobC         uint32
	Controlling   uint32
	TerminalPGID  uint32
	Nice          int32
	StartSeconds  uint64
	StartUseconds uint64
}

func processByPID(pid int) (processRecord, error) {
	var info procBSDInfo
	result, _, errno := syscall.Syscall6(
		syscall.SYS_PROC_INFO,
		procInfoCallPIDInfo,
		uintptr(pid),
		procPIDTBSDInfo,
		0,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if errno != 0 {
		return processRecord{}, errno
	}
	if result != unsafe.Sizeof(info) {
		return processRecord{}, fmt.Errorf("proc_info returned %d bytes, want %d", result, unsafe.Sizeof(info))
	}
	sid, err := syscall.Getsid(pid)
	if err != nil {
		return processRecord{}, err
	}
	return processRecord{
		PID:  pid,
		PPID: int(info.PPID),
		PGID: int(info.PGID),
		SID:  sid,
		Start: fmt.Sprintf("darwin-proc-start:%d.%06d",
			info.StartSeconds, info.StartUseconds),
		IsZombie: info.Status == darwinZombieStatus,
	}, nil
}

// listProcesses enumerates only THIS uid's processes. macOS offers no
// session-scoped listing (there is no PROC_SESSION_ONLY), and enumerating all
// PIDs then inspecting each one is unworkable: inspecting another user's process
// returns EPERM, so a fail-closed inspection rule would abort on every ordinary
// system. Scoping by uid is not a weakening — a session the core created contains
// only processes it spawned, which run as this uid — and it makes every listed PID
// inspectable, so an inspection failure becomes genuinely alarming rather than
// routine.
func listProcesses() ([]processRecord, error) {
	pids := make([]int32, 65536)
	result, _, errno := syscall.Syscall6(
		syscall.SYS_PROC_INFO,
		procInfoCallListPIDs,
		procUIDOnly,
		uintptr(os.Getuid()),
		0,
		uintptr(unsafe.Pointer(&pids[0])),
		uintptr(len(pids)*4),
	)
	if errno != 0 {
		return nil, errno
	}
	count := int(result) / 4
	processes := make([]processRecord, 0, count)
	for _, rawPID := range pids[:count] {
		if rawPID <= 0 {
			continue
		}
		process, err := processByPID(int(rawPID))
		if err != nil {
			if isProcessGone(err) {
				// Exited between listing and inspection: an ordinary race.
				continue
			}
			// A process we cannot inspect is a process we cannot rule out. Fail
			// closed rather than silently shrinking the member set.
			return nil, fmt.Errorf("inspect pid %d: %w", int(rawPID), err)
		}
		processes = append(processes, process)
	}
	return processes, nil
}
