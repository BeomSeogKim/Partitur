package main

import "github.com/BeomSeogKim/Partitur/internal/recovery"

const recoveryTraceFileEnvironment = "PARTITUR_RECOVERY_TRACE_FILE"

// recoveryDecisionTrace is an optional harness observation of production
// recovery. It is deliberately separate from the durable journal.
type recoveryDecisionTrace interface {
	Observe(recovery.Decision)
	Close() error
}

type noopRecoveryDecisionTrace struct{}

func (noopRecoveryDecisionTrace) Observe(recovery.Decision) {}

func (noopRecoveryDecisionTrace) Close() error { return nil }
