package procid

import (
	"context"
	"errors"
	"os"
	"os/exec"
	ossignal "os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

const terminateHelperEnvironment = "PARTITUR_PROCID_TERMINATE_HELPER"
const terminateIgnoreTERMEnvironment = "PARTITUR_PROCID_TERMINATE_IGNORE_TERM"
const terminateReadyEnvironment = "PARTITUR_PROCID_TERMINATE_READY"

func TestTerminateVerifiedOwner(t *testing.T) {
	command := startTerminateHelper(t, false)
	identity, err := Read(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := Terminate(ctx, command.Process.Pid, identity, 0); err != nil {
		t.Fatal(err)
	}
	if result := Matches(command.Process.Pid, identity); result.Status != GoneOrReused {
		t.Fatalf("owner after termination = %+v", result)
	}
}

func TestTerminateEscalatesIgnoredSIGTERMAfterGrace(t *testing.T) {
	command := startTerminateHelper(t, true)
	identity, err := Read(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	grace := 50 * time.Millisecond
	started := time.Now()
	if err := Terminate(ctx, command.Process.Pid, identity, grace); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < grace {
		t.Fatalf("termination elapsed=%s, want grace observation of at least %s", elapsed, grace)
	}
	if result := Matches(command.Process.Pid, identity); result.Status != GoneOrReused {
		t.Fatalf("owner after SIGKILL escalation = %+v", result)
	}
}

func TestTerminateRefusesAbsentRecordedIdentity(t *testing.T) {
	command := startTerminateHelper(t, false)
	if err := Terminate(context.Background(), command.Process.Pid, nil, 0); !errors.Is(err, ErrUnverifiable) {
		t.Fatalf("terminate error=%v, want ErrUnverifiable", err)
	}
	identity, err := Read(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if result := Matches(command.Process.Pid, identity); result.Status != MatchingAndLive {
		t.Fatalf("owner after absent identity refusal = %+v", result)
	}
}

func TestTerminateRefusesDifferentLiveIdentity(t *testing.T) {
	command := startTerminateHelper(t, false)
	identity, err := Read(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	wrong := differentIdentity(t, identity)
	if err := Terminate(context.Background(), command.Process.Pid, wrong, 0); err != nil {
		t.Fatalf("terminate error=%v, want nil for a reused identity", err)
	}
	if result := Matches(command.Process.Pid, identity); result.Status != MatchingAndLive {
		t.Fatalf("owner after different identity refusal = %+v", result)
	}
}

func TestTerminateHelper(t *testing.T) {
	if os.Getenv(terminateHelperEnvironment) != "1" {
		return
	}
	if os.Getenv(terminateIgnoreTERMEnvironment) == "1" {
		ossignal.Ignore(syscall.SIGTERM)
	}
	if path := os.Getenv(terminateReadyEnvironment); path != "" {
		if err := os.WriteFile(path, []byte("ready"), 0o600); err != nil {
			os.Exit(96)
		}
	}
	for {
		time.Sleep(time.Hour)
	}
}

func startTerminateHelper(t *testing.T, ignoreTERM bool) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=TestTerminateHelper")
	command.Env = append(os.Environ(), terminateHelperEnvironment+"=1")
	ready := ""
	if ignoreTERM {
		ready = t.TempDir() + "/ready"
		command.Env = append(command.Env,
			terminateIgnoreTERMEnvironment+"=1",
			terminateReadyEnvironment+"="+ready,
		)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	})
	if ignoreTERM {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(ready); err == nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if _, err := os.Stat(ready); err != nil {
			t.Fatalf("terminate helper never installed SIGTERM ignore: %v", err)
		}
	}
	return command
}

func differentIdentity(t *testing.T, identity runstate.StartIdentity) runstate.StartIdentity {
	t.Helper()
	switch value := identity.(type) {
	case runstate.LinuxStartIdentity:
		value.StartTicks = "0"
		return value
	case runstate.DarwinStartIdentity:
		value.StartTVUsec = (value.StartTVUsec + 1) % 1_000_000
		return value
	default:
		t.Fatalf("unsupported process identity %T", identity)
		return nil
	}
}
