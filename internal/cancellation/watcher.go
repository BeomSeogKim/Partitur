package cancellation

import (
	"context"
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
	stop      context.CancelFunc
	done      chan struct{}

	mu  sync.Mutex
	err error
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
		}
	}
}

// observe returns true once the watch has reached a terminal state. Keeping
// this separate lets the test drive a journal interleaving without a sleep.
func (watcher *Watcher) observe() bool {
	journal, err := watcher.store.ReadJournal(watcher.runID)
	if err != nil {
		watcher.mu.Lock()
		watcher.err = err
		watcher.mu.Unlock()
		return true
	}
	for _, event := range journal.Events {
		if event.Type == runstate.EventCancelRequested {
			close(watcher.cancelled)
			return true
		}
	}
	return false
}
