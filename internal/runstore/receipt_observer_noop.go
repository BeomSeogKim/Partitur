//go:build !faultprobe

package runstore

// ReceiptObserverFromEnvironment selects the optional harness receipt
// observer. Ordinary builds deliberately carry no receipt-harness behavior.
func ReceiptObserverFromEnvironment() ReceiptObserver {
	return receiptObserverFunc(func(DurabilityReceipt) {})
}
