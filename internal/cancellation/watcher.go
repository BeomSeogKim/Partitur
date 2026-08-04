package cancellation

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

const watchPollInterval = 20 * time.Millisecond

// Watcher tails one run's authoritative journal until cancellation is visible
// or its owner stops it. It is observational: terminalization remains the
// caller's explicit cancellation-oracle invocation.
type Watcher struct {
	store *runstore.Store
	runID runstate.RunID

	cancelled chan struct{}
	prepared  chan struct{}
	interrupt chan struct{}
	wake      chan struct{}
	stop      context.CancelFunc
	done      chan struct{}

	mu        sync.Mutex
	err       error
	prepareID runstate.PrepareID

	cancelOnce    sync.Once
	prepareOnce   sync.Once
	interruptOnce sync.Once
}

// Watch starts the continuous control watch required while a driver owns its
// lease. A torn final journal line is retried: only its durable prefix is
// observable, so it is not a watcher error.
func Watch(store *runstore.Store, runID runstate.RunID) (*Watcher, error) {
	if store == nil || runID == "" {
		return nil, errors.New("cancellation watcher requires store and run id")
	}
	context, stop := context.WithCancel(context.Background())
	w := &Watcher{
		store:     store,
		runID:     runID,
		cancelled: make(chan struct{}),
		prepared:  make(chan struct{}),
		interrupt: make(chan struct{}),
		wake:      make(chan struct{}, 1),
		stop:      stop,
		done:      make(chan struct{}),
	}
	if w.observe() {
		close(w.done)
		return w, nil
	}
	go w.watch(context, time.NewTicker(watchPollInterval))
	return w, nil
}

// Cancelled closes once a durable cancel.requested is visible.
func (watcher *Watcher) Cancelled() <-chan struct{} {
	if watcher == nil {
		return nil
	}
	return watcher.cancelled
}

// Prepared closes once a durable, still-pending approval prepare is visible.
// Unlike cancellation, preparation does not stop observation: a later
// cancel.requested must still take priority.
func (watcher *Watcher) Prepared() <-chan struct{} {
	if watcher == nil {
		return nil
	}
	watcher.ensureSignals()
	return watcher.prepared
}

// Interrupt closes for either durable control intent. Driver-authorized
// processes use it so their response recording stops at control observation.
func (watcher *Watcher) Interrupt() <-chan struct{} {
	if watcher == nil {
		return nil
	}
	watcher.ensureSignals()
	return watcher.interrupt
}

// PrepareID returns the exact durable prepare that closed Prepared.
func (watcher *Watcher) PrepareID() runstate.PrepareID {
	if watcher == nil {
		return ""
	}
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	return watcher.prepareID
}

// Err reports a journal-read failure that stopped the watch.
func (watcher *Watcher) Err() error {
	if watcher == nil {
		return errors.New("nil cancellation watcher")
	}
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	return watcher.err
}

// Execute applies the single cancellation oracle for this watcher’s run.
func (watcher *Watcher) Execute(ctx context.Context) error {
	if watcher == nil {
		return errors.New("nil cancellation watcher")
	}
	return Execute(ctx, watcher.store, watcher.runID)
}

// Wake asks the watcher to read the journal now. It is safe to call from the
// driver's SIGUSR1 relay; polling remains the correctness mechanism.
func (watcher *Watcher) Wake() {
	if watcher == nil {
		return
	}
	select {
	case watcher.wake <- struct{}{}:
	default:
	}
}

// Stop waits for the watcher goroutine, so it cannot outlive its driver.
func (watcher *Watcher) Stop() {
	if watcher == nil {
		return
	}
	watcher.stop()
	<-watcher.done
}

func (watcher *Watcher) watch(ctx context.Context, ticker *time.Ticker) {
	defer close(watcher.done)
	defer ticker.Stop()
	for {
		if watcher.observe() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-watcher.wake:
		}
	}
}

// observe returns true once the watch has reached a terminal state. Keeping
// this separate lets the test drive a journal interleaving without a sleep.
func (watcher *Watcher) observe() bool {
	watcher.ensureSignals()
	journal, err := watcher.store.ReadJournal(watcher.runID)
	if err != nil {
		watcher.mu.Lock()
		watcher.err = err
		watcher.mu.Unlock()
		return true
	}
	var pending runstate.PrepareID
	for _, event := range journal.Events {
		if event.Type == runstate.EventCancelRequested {
			watcher.cancelOnce.Do(func() { close(watcher.cancelled) })
			watcher.interruptOnce.Do(func() { close(watcher.interrupt) })
			return true
		}
		switch event.Type {
		case runstate.EventAmendmentApprovalPrepared:
			var payload struct {
				PrepareID runstate.PrepareID `json:"prepare_id"`
			}
			if json.Unmarshal(event.Payload, &payload) == nil {
				pending = payload.PrepareID
			}
		case runstate.EventAmendmentApprovalAbandoned, runstate.EventAmendmentApproved:
			pending = ""
		}
	}
	if pending != "" {
		watcher.mu.Lock()
		watcher.prepareID = pending
		watcher.mu.Unlock()
		watcher.prepareOnce.Do(func() { close(watcher.prepared) })
		watcher.interruptOnce.Do(func() { close(watcher.interrupt) })
	}
	return false
}

func (watcher *Watcher) ensureSignals() {
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	if watcher.cancelled == nil {
		watcher.cancelled = make(chan struct{})
	}
	if watcher.prepared == nil {
		watcher.prepared = make(chan struct{})
	}
	if watcher.interrupt == nil {
		watcher.interrupt = make(chan struct{})
	}
}
