//go:build darwin || linux

package adapterkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunProcessTerminatesProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pidChannel := make(chan int, 1)
	done := make(chan error, 1)
	go func() {
		_, err := runProcess(ctx, ProcessSpec{
			Path: os.Args[0],
			Args: []string{"-test.run=TestProcessHelper"},
			Env:  processHelperEnv("parent"),
		}, func(line []byte) error {
			text := string(line)
			if !strings.HasPrefix(text, "grandchild:") {
				return nil
			}
			pid, err := strconv.Atoi(strings.TrimPrefix(text, "grandchild:"))
			if err != nil {
				return err
			}
			pidChannel <- pid
			return nil
		}, 200*time.Millisecond)
		done <- err
	}()

	var grandchildPID int
	select {
	case grandchildPID = <-pidChannel:
	case <-time.After(5 * time.Second):
		t.Fatal("grandchild did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for processExists(grandchildPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processExists(grandchildPID) {
		t.Fatalf("grandchild process %d survived cancellation", grandchildPID)
	}
}

func TestRunProcessScansLinesAndPassesStderr(t *testing.T) {
	var lines []string
	var stderr strings.Builder
	result, err := runProcess(context.Background(), ProcessSpec{
		Path:   os.Args[0],
		Args:   []string{"-test.run=TestProcessHelper"},
		Env:    processHelperEnv("output"),
		Stderr: &stderr,
	}, func(line []byte) error {
		lines = append(lines, string(line))
		return nil
	}, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || strings.Join(lines, ",") != "one,two" || stderr.String() != "diagnostic\n" {
		t.Fatalf("result=%+v lines=%v stderr=%q", result, lines, stderr.String())
	}
}

func TestProcessHelper(t *testing.T) {
	switch os.Getenv("PARTITUR_PROCESS_HELPER") {
	case "":
		return
	case "output":
		fmt.Println("one")
		fmt.Println("two")
		fmt.Fprintln(os.Stderr, "diagnostic")
		os.Exit(0)
	case "parent":
		command := exec.Command(os.Args[0], "-test.run=TestProcessHelper")
		command.Env = processHelperEnv("grandchild")
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			os.Exit(3)
		}
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		<-signals
		_ = command.Wait()
		os.Exit(0)
	case "grandchild":
		fmt.Printf("grandchild:%d\n", os.Getpid())
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		<-signals
		os.Exit(0)
	default:
		os.Exit(4)
	}
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func processHelperEnv(mode string) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PARTITUR_PROCESS_HELPER=") {
			environment = append(environment, entry)
		}
	}
	return append(environment, "PARTITUR_PROCESS_HELPER="+mode)
}
