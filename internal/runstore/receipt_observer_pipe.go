//go:build faultprobe

package runstore

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"syscall"
)

const (
	receiptNotifyFDEnv  = "PARTITUR_RECEIPT_NOTIFY_FD"
	receiptReleaseFDEnv = "PARTITUR_RECEIPT_RELEASE_FD"
)

// ReceiptObserverFromEnvironment selects the optional receipt rendezvous for
// a faultprobe build. Invalid or absent descriptors remain fail-safe no-op.
func ReceiptObserverFromEnvironment() ReceiptObserver {
	notifyFD, notifyOK := receiptFDFromEnvironment(receiptNotifyFDEnv)
	releaseFD, releaseOK := receiptFDFromEnvironment(receiptReleaseFDEnv)
	if !notifyOK || !releaseOK {
		return receiptObserverFunc(func(DurabilityReceipt) {})
	}
	return newPipeReceiptObserver(
		os.NewFile(uintptr(notifyFD), receiptNotifyFDEnv),
		os.NewFile(uintptr(releaseFD), receiptReleaseFDEnv),
	)
}

func receiptFDFromEnvironment(name string) (int, bool) {
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

func newPipeReceiptObserver(notify io.Writer, release io.Reader) ReceiptObserver {
	if notify == nil || release == nil {
		return receiptObserverFunc(func(DurabilityReceipt) {})
	}
	return &pipeReceiptObserver{notify: notify, release: release}
}

type pipeReceiptObserver struct {
	notify  io.Writer
	release io.Reader
	mu      sync.Mutex
}

func (observer *pipeReceiptObserver) Observed(receipt DurabilityReceipt) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if _, err := fmt.Fprintln(observer.notify, receipt.Address, os.Getpid()); err != nil {
		os.Exit(1)
	}
	var release [1]byte
	if _, err := io.ReadFull(observer.release, release[:]); err != nil {
		os.Exit(1)
	}
}
