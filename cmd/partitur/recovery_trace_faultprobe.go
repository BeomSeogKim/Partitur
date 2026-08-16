//go:build faultprobe

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/BeomSeogKim/Partitur/internal/recovery"
)

type fileRecoveryDecisionTrace struct {
	mu      sync.Mutex
	encoder *json.Encoder
	file    *os.File
	err     error
}

func recoveryDecisionTraceFromEnvironment() recoveryDecisionTrace {
	path := os.Getenv(recoveryTraceFileEnvironment)
	if path == "" {
		return noopRecoveryDecisionTrace{}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return failedRecoveryDecisionTrace{err: fmt.Errorf("open recovery decision trace: %w", err)}
	}
	return &fileRecoveryDecisionTrace{encoder: json.NewEncoder(file), file: file}
}

func (trace *fileRecoveryDecisionTrace) Observe(decision recovery.Decision) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.err == nil {
		trace.err = trace.encoder.Encode(decision)
	}
}

func (trace *fileRecoveryDecisionTrace) Close() error {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.file != nil {
		if err := trace.file.Close(); trace.err == nil {
			trace.err = err
		}
		trace.file = nil
	}
	if trace.err != nil {
		return fmt.Errorf("write recovery decision trace: %w", trace.err)
	}
	return nil
}

type failedRecoveryDecisionTrace struct{ err error }

func (trace failedRecoveryDecisionTrace) Observe(recovery.Decision) {}

func (trace failedRecoveryDecisionTrace) Close() error { return trace.err }
