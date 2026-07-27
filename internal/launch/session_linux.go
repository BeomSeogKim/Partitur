//go:build linux

package launch

import "syscall"

func currentSessionID() (int, error) {
	value, _, errno := syscall.Syscall(syscall.SYS_GETSID, 0, 0, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(value), nil
}
