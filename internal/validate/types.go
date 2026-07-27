package validate

import (
	"slices"

	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

// EntryKind identifies one validation output class. EnforcementAdvisory is the
// sole non-fatal class; it is not a general diagnostic severity.
type EntryKind string

const (
	EntryScore               EntryKind = "score"
	EntryCast                EntryKind = "cast"
	EntryAdapterEnvironment  EntryKind = "adapter_environment"
	EntryCapability          EntryKind = "capability"
	EntryEnforcement         EntryKind = "enforcement"
	EntryEnforcementAdvisory EntryKind = "enforcement_advisory"
)

// Entry is one already-ordered validation output. Only fields belonging to its
// Kind are populated.
type Entry struct {
	Kind                EntryKind
	Rule                string
	Origin              string
	Pointer             string
	Detail              string
	AdapterID           string
	AdapterKind         string
	Stderr              string
	PartID              string
	MovementID          string
	PerformerID         string
	MissingCapabilities []string
	UnmetDimensions     []cast.EnforcementDimension
}

// IsDiagnostic reports whether the entry is fatal to validation.
func (e Entry) IsDiagnostic() bool {
	return e.Kind != EntryEnforcementAdvisory
}

// RefusalKind identifies an input-acquisition precondition failure.
type RefusalKind string

const (
	RefusalWorkingDirectory  RefusalKind = "working_directory_unavailable"
	RefusalRequiredInput     RefusalKind = "required_input_unavailable"
	RefusalDiscoveredInput   RefusalKind = "discovered_input_unreadable"
	RefusalUserHomeDirectory RefusalKind = "user_home_unavailable"
)

// Refusal is a precondition failure that occurs before validation.
type Refusal struct {
	Kind   RefusalKind
	Path   string
	Detail string
}

// Result contains either a refusal or the complete ordered validation output.
type Result struct {
	Refusal *Refusal
	Entries []Entry
}

// Preparation is the non-mutating, validated input graph shared by validate
// and run. RepositoryRoot is the invocation working directory.
type Preparation struct {
	RepositoryRoot string
	Score          *score.Score
	Cast           *cast.Cast
	scoreSource    []byte
}

// ScoreSource returns a defensive copy of the exact root score bytes acquired
// for this preparation.
func (p *Preparation) ScoreSource() []byte {
	if p == nil {
		return nil
	}
	return slices.Clone(p.scoreSource)
}

// HasDiagnostics reports whether any fatal validation output exists.
func (r Result) HasDiagnostics() bool {
	for _, entry := range r.Entries {
		if entry.IsDiagnostic() {
			return true
		}
	}
	return false
}
