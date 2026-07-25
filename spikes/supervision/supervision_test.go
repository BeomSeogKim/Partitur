package supervision

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProcessStartIdentity(t *testing.T) {
	first, err := currentIdentity()
	if err != nil {
		t.Fatal(err)
	}
	second, err := processByPID(first.PID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Start == "" || first.Start != second.Start {
		t.Fatalf("identity unstable: %q != %q", first.Start, second.Start)
	}
	reused, err := identityMatches(first.PID, first.Start+"-reused")
	if err != nil {
		t.Fatalf("identity check errored: %v", err)
	}
	if reused {
		t.Fatal("PID reuse simulation with mismatched start identity was accepted")
	}
	t.Logf("IDENTITY_RESULT os=%s pid=%d start=%s", runtime.GOOS, first.PID, first.Start)
}

func TestLeaseStaleAfterSIGKILLAndPIDReuse(t *testing.T) {
	directory := t.TempDir()
	holder := helperCommand("lease-holder", directory)
	stdout, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "LEASE ") {
		t.Fatalf("holder did not acquire: %q", scanner.Text())
	}
	old, err := readLease(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = holder.Wait()
	reclaimed, err := acquireLease(directory)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.Epoch != old.Epoch+1 {
		t.Fatalf("reclaimed epoch=%d want=%d", reclaimed.Epoch, old.Epoch+1)
	}

	owner, err := currentIdentity()
	if err != nil {
		t.Fatal(err)
	}
	fake := lease{PID: owner.PID, Start: owner.Start + "-old", Token: "old", Epoch: reclaimed.Epoch}
	if err := withStateLock(directory, func() error {
		if err := writeJSONAtomic(filepath.Join(directory, "authority.json"), authority{
			Token: fake.Token, Epoch: fake.Epoch,
		}); err != nil {
			return err
		}
		return writeJSONAtomic(filepath.Join(directory, "driver.lease"), fake)
	}); err != nil {
		t.Fatal(err)
	}
	reuseReclaimed, err := acquireLease(directory)
	if err != nil {
		t.Fatal(err)
	}
	if reuseReclaimed.Epoch != fake.Epoch+1 {
		t.Fatalf("PID reuse epoch=%d want=%d", reuseReclaimed.Epoch, fake.Epoch+1)
	}
}

func TestTwoConcurrentLeaseAcquisitions(t *testing.T) {
	directory := t.TempDir()
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer gateRead.Close()

	type contender struct {
		command *exec.Cmd
		output  bytes.Buffer
	}
	contenders := make([]contender, 2)
	for index := range contenders {
		command := helperCommand("lease-contender", directory)
		command.ExtraFiles = []*os.File{gateRead}
		command.Stdout = &contenders[index].output
		command.Stderr = &contenders[index].output
		contenders[index].command = command
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
	}
	_ = gateWrite.Close()
	winners := 0
	for index := range contenders {
		if err := contenders[index].command.Wait(); err != nil {
			t.Fatalf("contender %d: %v %s", index, err, &contenders[index].output)
		}
		if strings.Contains(contenders[index].output.String(), "WON") {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d outputs=%q %q", winners, contenders[0].output.String(), contenders[1].output.String())
	}
}

func TestFencedIncarnationCannotMutateAfterResume(t *testing.T) {
	directory := t.TempDir()
	reviver := helperCommand("lease-reviver", directory)
	stdout, err := reviver.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := reviver.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "READY" {
		t.Fatalf("reviver not ready: %q", scanner.Text())
	}
	owner, err := readLease(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(reviver.Process.Pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	if err := fence(directory, owner); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(reviver.Process.Pid, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	if !scanner.Scan() || scanner.Text() != "FENCED" {
		t.Fatalf("reviver mutation result: %q", scanner.Text())
	}
	if err := reviver.Wait(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(directory, "mutations")); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fenced mutation reached storage: data=%q err=%v", data, err)
	}
}

func TestJournalWakeupWhileStdoutReadIsBlocked(t *testing.T) {
	directory := t.TempDir()
	journal := filepath.Join(directory, "journal.jsonl")
	if err := os.WriteFile(journal, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := helperCommand("blocked-adapter", "")
	adapter.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := adapter.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 1)
	readerDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		readerDone <- scanner.Err()
	}()
	select {
	case line := <-lines:
		if line != "ADAPTER_READY" {
			t.Fatalf("adapter line=%q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("adapter did not become ready")
	}

	observed := make(chan time.Time, 1)
	stopWatch := make(chan struct{})
	go watchJournal(journal, 20*time.Millisecond, observed, stopWatch)
	start := time.Now()
	file, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(file, `{"type":"cancel.requested"}`); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()

	var detection time.Time
	select {
	case detection = <-observed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("driver did not observe journal cancellation")
	}
	close(stopWatch)
	latency := detection.Sub(start)
	if latency > 250*time.Millisecond {
		t.Fatalf("journal detection latency=%v", latency)
	}
	_ = syscall.Kill(-adapter.Process.Pid, syscall.SIGKILL)
	_ = adapter.Wait()
	select {
	case <-readerDone:
	case <-time.After(time.Second):
		t.Fatal("blocked stdout reader did not exit after adapter kill")
	}
	t.Logf("CONTROL_RESULT os=%s latency=%s", runtime.GOOS, latency)
}

func TestWedgedAdapterVendorSessionSweep(t *testing.T) {
	adapter := helperCommand("wedged-adapter", "")
	adapter.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stdout, err := adapter.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("adapter did not report process chain")
	}
	var chain struct {
		Adapter processRecord `json:"adapter"`
		Vendor  processRecord `json:"vendor"`
	}
	line := strings.TrimPrefix(scanner.Text(), "CHAIN ")
	if err := json.Unmarshal([]byte(line), &chain); err != nil {
		t.Fatalf("chain=%q: %v", line, err)
	}
	if chain.Adapter.SID != chain.Adapter.PID || chain.Vendor.SID != chain.Adapter.SID {
		t.Fatalf("session containment failed: %+v", chain)
	}
	if chain.Adapter.PGID == chain.Vendor.PGID {
		t.Fatalf("vendor did not use its own process group: %+v", chain)
	}
	if err := sweepSession(chain.Adapter.SID, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	_ = adapter.Wait()
	members, err := liveSessionMembers(chain.Adapter.SID)
	if err != nil {
		t.Fatalf("enumerate session members: %v", err)
	}
	if len(members) != 0 {
		t.Fatalf("session survivors: %+v", members)
	}
	t.Logf("SUPERVISION_RESULT os=%s adapter=%d vendor=%d separate_pgid=true survivors=0",
		runtime.GOOS, chain.Adapter.PID, chain.Vendor.PID)
}

func TestDescendantSetsidEscapesPortableSessionSelection(t *testing.T) {
	adapter := helperCommand("escaping-adapter", "")
	adapter.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stdout, err := adapter.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("adapter did not report escaped chain")
	}
	var chain struct {
		Adapter processRecord `json:"adapter"`
		Vendor  processRecord `json:"vendor"`
	}
	line := strings.TrimPrefix(scanner.Text(), "CHAIN ")
	if err := json.Unmarshal([]byte(line), &chain); err != nil {
		t.Fatalf("chain=%q: %v", line, err)
	}
	if chain.Vendor.SID == chain.Adapter.SID {
		t.Fatalf("escape helper failed to create a new session: %+v", chain)
	}
	escapees, err := liveSessionMembers(chain.Adapter.SID)
	if err != nil {
		t.Fatalf("enumerate session members: %v", err)
	}
	for _, member := range escapees {
		if member.PID == chain.Vendor.PID {
			t.Fatalf("escaped vendor remained selectable by outer session: %+v", chain)
		}
	}
	t.Logf("ESCAPE_RESULT os=%s outer_sid=%d escaped_sid=%d excluded_from_outer_sweep=true",
		runtime.GOOS, chain.Adapter.SID, chain.Vendor.SID)
	if err := sweepSession(chain.Vendor.SID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := sweepSession(chain.Adapter.SID, 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	_ = adapter.Wait()
}

func watchJournal(path string, interval time.Duration, observed chan<- time.Time, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var offset int64
	for {
		select {
		case <-ticker.C:
			file, err := os.Open(path)
			if err != nil {
				continue
			}
			_, _ = file.Seek(offset, io.SeekStart)
			data, _ := io.ReadAll(file)
			_ = file.Close()
			offset += int64(len(data))
			if bytes.Contains(data, []byte(`"type":"cancel.requested"`)) {
				observed <- time.Now()
				return
			}
		case <-stop:
			return
		}
	}
}

func helperCommand(mode, directory string) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=TestProcessHelper")
	command.Env = append(os.Environ(),
		"PARTITUR_SPIKE_HELPER="+mode,
		"PARTITUR_SPIKE_DIR="+directory,
	)
	return command
}

func TestProcessHelper(t *testing.T) {
	mode := os.Getenv("PARTITUR_SPIKE_HELPER")
	if mode == "" {
		return
	}
	directory := os.Getenv("PARTITUR_SPIKE_DIR")
	switch mode {
	case "lease-holder":
		owner, err := acquireLease(directory)
		if err != nil {
			fmt.Println("ERROR", err)
			os.Exit(2)
		}
		fmt.Printf("LEASE %s\n", owner.Token)
		select {}
	case "lease-contender":
		gate := os.NewFile(3, "gate")
		_, _ = gate.Read(make([]byte, 1))
		_ = gate.Close()
		if _, err := acquireLease(directory); err != nil {
			fmt.Println("LOST")
			return
		}
		fmt.Println("WON")
		time.Sleep(250 * time.Millisecond)
	case "lease-reviver":
		owner, err := acquireLease(directory)
		if err != nil {
			os.Exit(2)
		}
		fmt.Println("READY")
		time.Sleep(200 * time.Millisecond)
		if err := mutate(directory, owner, "revived-write"); err != nil {
			fmt.Println("FENCED")
			return
		}
		fmt.Println("MUTATED")
	case "blocked-adapter":
		fmt.Println("ADAPTER_READY")
		select {}
	case "wedged-adapter":
		runAdapterHelper(false)
	case "escaping-adapter":
		runAdapterHelper(true)
	case "vendor":
		signal.Ignore(syscall.SIGHUP, syscall.SIGTERM)
		fmt.Println("VENDOR_READY")
		select {}
	default:
		if _, err := strconv.Atoi(mode); err == nil {
			return
		}
		os.Exit(4)
	}
}

func runAdapterHelper(escapeSession bool) {
	vendor := helperCommand("vendor", "")
	ready, err := vendor.StdoutPipe()
	if err != nil {
		os.Exit(2)
	}
	if escapeSession {
		vendor.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	} else {
		vendor.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if err := vendor.Start(); err != nil {
		os.Exit(2)
	}
	readyScanner := bufio.NewScanner(ready)
	if !readyScanner.Scan() || readyScanner.Text() != "VENDOR_READY" {
		os.Exit(2)
	}
	adapterInfo, err := currentIdentity()
	if err != nil {
		os.Exit(3)
	}
	var vendorInfo processRecord
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		vendorInfo, err = processByPID(vendor.Process.Pid)
		if err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	encoded, _ := json.Marshal(struct {
		Adapter processRecord `json:"adapter"`
		Vendor  processRecord `json:"vendor"`
	}{Adapter: adapterInfo, Vendor: vendorInfo})
	fmt.Printf("CHAIN %s\n", encoded)
	select {}
}
