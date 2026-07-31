//go:build faultprobe

package faultpoint

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"syscall"
)

const (
	probeNotifyFDEnv        = "PARTITUR_FAULTPOINT_NOTIFY_FD"
	probeReleaseFDEnv       = "PARTITUR_FAULTPOINT_RELEASE_FD"
	probeHarnessRequiredEnv = "PARTITUR_FAULTPOINT_HARNESS"
)

// ProbeFromEnvironment selects a Probe. See DESIGN Appendix E.
func ProbeFromEnvironment() Probe {
	notifyFD, notifyOK := probeFDFromEnvironment(probeNotifyFDEnv)
	releaseFD, releaseOK := probeFDFromEnvironment(probeReleaseFDEnv)
	if !notifyOK || !releaseOK {
		return Nop{}
	}
	return NewPipeProbe(
		os.NewFile(uintptr(notifyFD), probeNotifyFDEnv),
		os.NewFile(uintptr(releaseFD), probeReleaseFDEnv),
	)
}

// RequireHarnessBuild validates the current build. See DESIGN Appendix E.
func RequireHarnessBuild() error { return nil }

func probeFDFromEnvironment(name string) (int, bool) {
	value := os.Getenv(name)
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 3 {
		return 0, false
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFIFO {
		return 0, false
	}
	return fd, true
}

// NewPipeProbe creates a Probe. See DESIGN Appendix E.
func NewPipeProbe(notify io.Writer, release io.Reader) Probe {
	if notify == nil || release == nil {
		return Nop{}
	}
	return &pipeProbe{notify: notify, release: release}
}

type pipeProbe struct {
	notify  io.Writer
	release io.Reader
	mu      sync.Mutex
}

// Reached implements Probe. See DESIGN Appendix E.
func (probe *pipeProbe) Reached(point PointID) {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if _, err := fmt.Fprintln(probe.notify, point, os.Getpid()); err != nil {
		os.Exit(1)
	}
	var release [1]byte
	if _, err := io.ReadFull(probe.release, release[:]); err != nil {
		os.Exit(1)
	}
}
