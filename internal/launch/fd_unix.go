//go:build linux || darwin

package launch

import "syscall"

func keepAcrossExec(file descriptorFile) error {
	_, _, errno := syscall.Syscall(
		syscall.SYS_FCNTL,
		file.Fd(),
		syscall.F_SETFD,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

type descriptorFile interface {
	Fd() uintptr
}
