package adapter

import (
	"sort"
	"time"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

const (
	ProbeCompletionDeadline = 15_000 * time.Millisecond
	OuterTerminationGrace   = 30_000 * time.Millisecond
	MaxProbeStderrBytes     = 65_536
)

type DiagnosticKind string

const (
	DiagnosticExecutableAbsent    DiagnosticKind = "executable_absent"
	DiagnosticSpawnFailed         DiagnosticKind = "spawn_failed"
	DiagnosticRequestIO           DiagnosticKind = "request_io_failed"
	DiagnosticResponseIO          DiagnosticKind = "response_io_failed"
	DiagnosticPrematureEOF        DiagnosticKind = "premature_eof"
	DiagnosticMalformedResponse   DiagnosticKind = "malformed_response"
	DiagnosticOversizedResponse   DiagnosticKind = "oversized_response"
	DiagnosticDuplicateKey        DiagnosticKind = "duplicate_key"
	DiagnosticInvalidUTF8         DiagnosticKind = "invalid_utf8"
	DiagnosticErrorResponse       DiagnosticKind = "error_response"
	DiagnosticWrongAdapter        DiagnosticKind = "wrong_adapter_id"
	DiagnosticUnsupportedProtocol DiagnosticKind = "unsupported_protocol"
	DiagnosticNonzeroExit         DiagnosticKind = "nonzero_exit"
	DiagnosticDeadline            DiagnosticKind = "deadline_exceeded"
	DiagnosticSessionNotEmpty     DiagnosticKind = "session_not_empty"
	DiagnosticCleanupUnverifiable DiagnosticKind = "cleanup_unverifiable"
)

// Diagnostic is a structured adapter-environment validation diagnostic.
// Detail and Stderr are sanitized diagnostic text, not stable rendering prose.
type Diagnostic struct {
	AdapterID string
	Kind      DiagnosticKind
	Detail    string
	Stderr    string
}

// Probe is one successfully negotiated adapter probe.
type Probe struct {
	AdapterID string
	Result    protocol.ProbeResult
}

// Report contains every successful probe and every independent diagnostic.
// Both slices are ordered by adapter id; diagnostics then order by kind and
// detail.
type Report struct {
	Probes      []Probe
	Diagnostics []Diagnostic
}

func sortReport(report *Report) {
	sort.Slice(report.Probes, func(i, j int) bool {
		return report.Probes[i].AdapterID < report.Probes[j].AdapterID
	})
	sort.Slice(report.Diagnostics, func(i, j int) bool {
		left, right := report.Diagnostics[i], report.Diagnostics[j]
		if left.AdapterID != right.AdapterID {
			return left.AdapterID < right.AdapterID
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}
		if left.Detail != right.Detail {
			return left.Detail < right.Detail
		}
		return left.Stderr < right.Stderr
	})
}
