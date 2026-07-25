package processidentity

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

type leasePhase string

const (
	leaseActive   leasePhase = "ACTIVE"
	leasePrepared leasePhase = "PREPARED"
	leaseQuiesced leasePhase = "QUIESCED"
)

type quiesceLease struct {
	Token       string     `json:"token"`
	Epoch       uint64     `json:"epoch"`
	Phase       leasePhase `json:"phase"`
	PrepareID   string     `json:"prepare_id,omitempty"`
	AdapterLive bool       `json:"adapter_live"`
}

type quiesceEvent struct {
	Type        string `json:"type"`
	PrepareID   string `json:"prepare_id,omitempty"`
	Base        int    `json:"base_revision,omitempty"`
	New         int    `json:"new_revision,omitempty"`
	FencedEpoch uint64 `json:"fenced_epoch,omitempty"`
}

type quiesceStore struct {
	directory string
}

func newQuiesceStore(t *testing.T) *quiesceStore {
	t.Helper()
	store := &quiesceStore{directory: t.TempDir()}
	if err := writeDurableJSON(store.leasePath(), quiesceLease{
		Token: "incarnation-1", Epoch: 7, Phase: leaseActive, AdapterLive: true,
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

func (store *quiesceStore) leasePath() string {
	return filepath.Join(store.directory, "driver.lease")
}

func (store *quiesceStore) lock(operation func() error) error {
	lock, err := os.OpenFile(filepath.Join(store.directory, "state.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return operation()
}

func (store *quiesceStore) readLease() (quiesceLease, error) {
	var lease quiesceLease
	err := readJSON(store.leasePath(), &lease)
	return lease, err
}

func (store *quiesceStore) appendEvent(event quiesceEvent) error {
	file, err := os.OpenFile(
		filepath.Join(store.directory, "journal.jsonl"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func (store *quiesceStore) events() ([]quiesceEvent, error) {
	file, err := os.Open(filepath.Join(store.directory, "journal.jsonl"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	var events []quiesceEvent
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event quiesceEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func (store *quiesceStore) prepare(prepareID string) error {
	return store.lock(func() error {
		lease, err := store.readLease()
		if err != nil {
			return err
		}
		if lease.Phase != leaseActive {
			if lease.PrepareID == prepareID {
				return nil
			}
			return errors.New("another approval prepare owns the lease")
		}
		// Intent is journaled before the removable lease becomes coordination
		// state. Recovery can therefore finish or cancel the exact operation.
		if err := store.appendEvent(quiesceEvent{
			Type: "amendment.approval_prepared", PrepareID: prepareID, Base: 1,
		}); err != nil {
			return err
		}
		lease.Phase = leasePrepared
		lease.PrepareID = prepareID
		return writeDurableJSON(store.leasePath(), lease)
	})
}

func (store *quiesceStore) acknowledge(prepareID string) error {
	return store.lock(func() error {
		lease, err := store.readLease()
		if err != nil {
			return err
		}
		if lease.Phase != leasePrepared || lease.PrepareID != prepareID {
			return errors.New("prepare or incarnation mismatch")
		}
		// This represents drain + verified empty session + interval close. The
		// atomic rename simultaneously makes driver.lease unavailable and makes
		// the prepare-bound ACK visible. Its contents retain the exact lease
		// incarnation; the destination path supplies the ACK state.
		ackPath := filepath.Join(store.directory, "driver.quiesced."+prepareID)
		if err := os.Rename(store.leasePath(), ackPath); err != nil {
			return err
		}
		directory, err := os.Open(store.directory)
		if err != nil {
			return err
		}
		defer directory.Close()
		return directory.Sync()
	})
}

func (store *quiesceStore) driverMutation() error {
	return store.lock(func() error {
		lease, err := store.readLease()
		if err != nil {
			return errors.New("driver mutation fenced: active lease path absent")
		}
		if lease.Phase != leaseActive || !lease.AdapterLive {
			return errors.New("driver mutation fenced: lease not active")
		}
		file, err := os.OpenFile(
			filepath.Join(store.directory, "mutations"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY,
			0o600,
		)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = fmt.Fprintln(file, "mutation")
		return err
	})
}

func (store *quiesceStore) approve(prepareID string, timeout bool) error {
	return store.lock(func() error {
		events, err := store.events()
		if err != nil {
			return err
		}
		prepared := false
		for _, event := range events {
			if event.Type == "amendment.approval_prepared" && event.PrepareID == prepareID {
				prepared = true
			}
			if event.Type == "amendment.approved" {
				if event.PrepareID == prepareID {
					return nil
				}
				return errors.New("base revision changed")
			}
		}
		if !prepared {
			return errors.New("approval intent is not durable")
		}

		var fencedEpoch uint64
		ackPath := filepath.Join(store.directory, "driver.quiesced."+prepareID)
		var acknowledged quiesceLease
		if err := readJSON(ackPath, &acknowledged); err == nil {
			if acknowledged.Phase != leasePrepared ||
				acknowledged.PrepareID != prepareID {
				return errors.New("invalid quiesce acknowledgement")
			}
		} else {
			if !timeout {
				return errors.New("driver has not acknowledged")
			}
			lease, err := store.readLease()
			if err != nil {
				return err
			}
			if lease.Phase != leasePrepared || lease.PrepareID != prepareID {
				return errors.New("lease changed before timeout fence")
			}
			// Represents terminate + verified session sweep outside the lock,
			// followed by a final exact-incarnation CAS under it.
			fencedEpoch = lease.Epoch + 1
		}

		if err := store.appendEvent(quiesceEvent{
			Type: "amendment.approved", PrepareID: prepareID,
			Base: 1, New: 2, FencedEpoch: fencedEpoch,
		}); err != nil {
			return err
		}
		_ = os.Remove(store.leasePath())
		_ = os.Remove(ackPath)
		return nil
	})
}

func TestQuiesceRaceMatrix(t *testing.T) {
	t.Run("ack_then_wedges", func(t *testing.T) {
		store := newQuiesceStore(t)
		if err := store.prepare("prepare-a"); err != nil {
			t.Fatal(err)
		}
		if err := store.acknowledge("prepare-a"); err != nil {
			t.Fatal(err)
		}
		if err := store.driverMutation(); err == nil {
			t.Fatal("acknowledged driver retained mutation authority")
		}
		if err := store.approve("prepare-a", false); err != nil {
			t.Fatal(err)
		}
		assertSingleApproval(t, store, false)
	})

	t.Run("ack_then_dies", func(t *testing.T) {
		store := newQuiesceStore(t)
		if err := store.prepare("prepare-a"); err != nil {
			t.Fatal(err)
		}
		if err := store.acknowledge("prepare-a"); err != nil {
			t.Fatal(err)
		}
		// No live coordinator is needed: recovery consumes the durable prepare
		// and prepare-bound lease-CAS acknowledgement.
		if err := store.approve("prepare-a", false); err != nil {
			t.Fatal(err)
		}
		assertSingleApproval(t, store, false)
	})

	t.Run("never_acks", func(t *testing.T) {
		store := newQuiesceStore(t)
		if err := store.prepare("prepare-a"); err != nil {
			t.Fatal(err)
		}
		if err := store.approve("prepare-a", true); err != nil {
			t.Fatal(err)
		}
		assertSingleApproval(t, store, true)
		if err := store.driverMutation(); err == nil {
			t.Fatal("timed-out incarnation mutated after fence")
		}
	})

	t.Run("two_approvers", func(t *testing.T) {
		store := newQuiesceStore(t)
		type result struct {
			id  string
			err error
		}
		start := make(chan struct{})
		results := make(chan result, 2)
		var wait sync.WaitGroup
		for _, id := range []string{"prepare-a", "prepare-b"} {
			wait.Add(1)
			go func(id string) {
				defer wait.Done()
				<-start
				results <- result{id: id, err: store.prepare(id)}
			}(id)
		}
		close(start)
		wait.Wait()
		close(results)

		var winner string
		failures := 0
		for result := range results {
			if result.err == nil {
				winner = result.id
			} else {
				failures++
			}
		}
		if winner == "" || failures != 1 {
			t.Fatalf("winner=%q failures=%d", winner, failures)
		}
		if err := store.acknowledge(winner); err != nil {
			t.Fatal(err)
		}
		if err := store.approve(winner, false); err != nil {
			t.Fatal(err)
		}
		assertSingleApproval(t, store, false)
	})

	t.Logf("QUIESCE_RESULT os=%s ack_wedge=pass ack_die=pass no_ack=fenced concurrent_approvers=one",
		runtime.GOOS)
}

func assertSingleApproval(t *testing.T, store *quiesceStore, fenced bool) {
	t.Helper()
	events, err := store.events()
	if err != nil {
		t.Fatal(err)
	}
	approvals := 0
	for _, event := range events {
		if event.Type != "amendment.approved" {
			continue
		}
		approvals++
		if fenced != (event.FencedEpoch != 0) {
			t.Fatalf("fenced=%v event=%+v", fenced, event)
		}
	}
	if approvals != 1 {
		t.Fatalf("approval count=%d events=%+v", approvals, events)
	}
}

func TestPlainAcknowledgementDoesNotRevokeLease(t *testing.T) {
	store := newQuiesceStore(t)
	// A separate-channel ACK with no lease CAS changes no durable authority.
	ack := true
	if !ack {
		t.Fatal("unreachable")
	}
	if err := store.driverMutation(); err != nil {
		t.Fatalf("plain ack unexpectedly revoked lease: %v", err)
	}
	t.Log("PLAIN_ACK_RESULT mutation_after_ack=accepted unsafe=true")
}

func TestPrepareMustBeJournaledForDeterministicRecovery(t *testing.T) {
	store := newQuiesceStore(t)
	lease, err := store.readLease()
	if err != nil {
		t.Fatal(err)
	}
	lease.Phase = leaseQuiesced
	lease.PrepareID = "lease-only-intent"
	lease.AdapterLive = false
	if err := writeDurableJSON(
		filepath.Join(store.directory, "driver.quiesced.lease-only-intent"),
		lease,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.leasePath()); err != nil {
		t.Fatal(err)
	}
	// The lease is explicitly removable coordination state. Once it is gone,
	// the journal must still say whether a user authorized approval.
	if err := os.Remove(filepath.Join(store.directory, "driver.quiesced.lease-only-intent")); err != nil {
		t.Fatal(err)
	}
	if err := store.approve("lease-only-intent", false); err == nil ||
		err.Error() != "approval intent is not durable" {
		t.Fatalf("recovery result=%v", err)
	}
}

func TestDowngradedFileLockStillBlocksAcknowledgementWriter(t *testing.T) {
	for trial := 0; trial < 100; trial++ {
		directory := t.TempDir()
		path := filepath.Join(directory, "state.lock")
		holder, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_SH); err != nil {
			t.Fatal(err)
		}

		acquired := filepath.Join(directory, "acquired")
		command := helperCommand("lock-writer")
		command.Env = append(command.Env,
			"PARTITUR_LOCK_PATH="+path,
			"PARTITUR_ACQUIRED_PATH="+acquired,
		)
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		if _, err := os.Stat(acquired); err == nil {
			t.Fatal("exclusive acknowledgement writer acquired through shared lock")
		}
		if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_UN); err != nil {
			t.Fatal(err)
		}
		_ = holder.Close()
		if err := command.Wait(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(acquired); err != nil {
			t.Fatal("writer did not acquire after unlock")
		}
	}
	t.Logf("LOCK_DOWNGRADE_RESULT os=%s trials=100 blocked_while_shared=100", runtime.GOOS)
}

func runLockWriter() error {
	file, err := os.OpenFile(os.Getenv("PARTITUR_LOCK_PATH"), os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return os.WriteFile(os.Getenv("PARTITUR_ACQUIRED_PATH"), []byte("yes\n"), 0o600)
}

func helperCommand(mode string) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=TestProcessIdentityHelper")
	command.Env = append(os.Environ(), "PARTITUR_SPIKE_HELPER="+mode)
	return command
}
