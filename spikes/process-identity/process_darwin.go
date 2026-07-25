//go:build darwin

package processidentity

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	procInfoCallListPIDs = 1
	procInfoCallPIDInfo  = 2
	procUIDOnly          = 4
	procPIDTBSDInfo      = 3
	darwinZombieStatus   = 5
)

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
		Start: startIdentity{
			Platform: "darwin", StartTVSec: info.StartSeconds, StartTVUsec: info.StartUseconds,
		},
		IsZombie: info.Status == darwinZombieStatus,
	}, nil
}

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
				continue
			}
			return nil, fmt.Errorf("inspect pid %d: %w", int(rawPID), err)
		}
		processes = append(processes, process)
	}
	return processes, nil
}
