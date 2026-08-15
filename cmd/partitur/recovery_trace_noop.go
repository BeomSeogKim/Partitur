//go:build !faultprobe

package main

func recoveryDecisionTraceFromEnvironment() recoveryDecisionTrace {
	return noopRecoveryDecisionTrace{}
}
