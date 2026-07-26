//go:build darwin

package procid

import (
	"fmt"
	"syscall"
	"unsafe"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

const (
	procInfoCallPIDInfo = 2
	procPIDTBSDInfo     = 3
	darwinZombieStatus  = 5
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

func read(pid int) (runstate.StartIdentity, bool, error) {
	if pid <= 0 {
		return nil, false, fmt.Errorf("invalid pid %d", pid)
	}
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
		return nil, false, errno
	}
	if result != unsafe.Sizeof(info) {
		return nil, false, fmt.Errorf("proc_info returned %d bytes, want %d", result, unsafe.Sizeof(info))
	}
	return runstate.DarwinStartIdentity{
		StartTVSec:  info.StartSeconds,
		StartTVUsec: info.StartUseconds,
	}, info.Status == darwinZombieStatus, nil
}
