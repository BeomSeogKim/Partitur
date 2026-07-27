//go:build darwin

package launch

import "syscall"

func currentSessionID() (int, error) {
	return syscall.Getsid(0)
}
