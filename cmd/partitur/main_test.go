package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if strings.TrimSpace(stdout.String()) != "dev" || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestMilestoneCommandsFailExplicitly(t *testing.T) {
	for command := range milestoneCommands {
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run([]string{command}, &stdout, &stderr); code != 2 {
				t.Fatalf("exit code = %d", code)
			}
			if stderr.String() != "not implemented in this milestone\n" {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"version", "extra"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Fatalf("args=%v exit code=%d", args, code)
		}
		if !strings.HasPrefix(stderr.String(), "usage:") {
			t.Fatalf("args=%v stderr=%q", args, stderr.String())
		}
	}
}
