//go:build linux

package processidentity

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func processByPID(pid int) (processRecord, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return processRecord{}, err
	}
	closeName := strings.LastIndexByte(string(data), ')')
	if closeName < 0 {
		return processRecord{}, fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(data[closeName+1:]))
	if len(fields) < 20 {
		return processRecord{}, fmt.Errorf("short /proc/%d/stat", pid)
	}
	parse := func(index int) (int, error) {
		return strconv.Atoi(fields[index])
	}
	ppid, err := parse(1)
	if err != nil {
		return processRecord{}, err
	}
	pgid, err := parse(2)
	if err != nil {
		return processRecord{}, err
	}
	sid, err := parse(3)
	if err != nil {
		return processRecord{}, err
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return processRecord{}, err
	}
	return processRecord{
		PID: pid, PPID: ppid, PGID: pgid, SID: sid,
		Start: startIdentity{
			Platform:   "linux",
			BootID:     strings.TrimSpace(string(bootID)),
			StartTicks: fields[19],
		},
		IsZombie: fields[0] == "Z",
	}, nil
}

func listProcesses() ([]processRecord, error) {
	directories, err := filepath.Glob("/proc/[0-9]*")
	if err != nil {
		return nil, err
	}
	processes := make([]processRecord, 0, len(directories))
	for _, directory := range directories {
		pid, err := strconv.Atoi(filepath.Base(directory))
		if err != nil {
			continue
		}
		process, err := processByPID(pid)
		if err != nil {
			if isProcessGone(err) {
				continue
			}
			return nil, fmt.Errorf("inspect pid %d: %w", pid, err)
		}
		processes = append(processes, process)
	}
	return processes, nil
}
