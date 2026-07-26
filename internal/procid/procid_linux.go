//go:build linux

package procid

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func read(pid int) (runstate.StartIdentity, bool, error) {
	if pid <= 0 {
		return nil, false, fmt.Errorf("invalid pid %d", pid)
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nil, false, err
	}
	closeName := strings.LastIndexByte(string(data), ')')
	if closeName < 0 {
		return nil, false, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(data[closeName+1:]))
	if len(fields) < 20 {
		return nil, false, fmt.Errorf("short /proc/%d/stat", pid)
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return nil, false, fmt.Errorf("invalid /proc/%d start ticks: %w", pid, err)
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, false, err
	}
	boot := strings.TrimSpace(string(bootID))
	if boot == "" {
		return nil, false, errors.New("empty Linux boot id")
	}
	return runstate.LinuxStartIdentity{
		BootID:     boot,
		StartTicks: fields[19],
	}, fields[0] == "Z", nil
}
