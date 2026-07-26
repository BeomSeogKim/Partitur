package cast

import (
	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

// RuleID identifies the stable owner of a cast diagnostic.
type RuleID string

const (
	RuleIngress RuleID = "cast.ingress"
	RuleSchema  RuleID = "cast.schema"
	RuleStatic  RuleID = "cast.static"
	RuleScore   RuleID = "cast.score"
)

// Diagnostic describes one cast defect. Origin identifies the source layer;
// it is empty for a missing effective declaration rather than a source value.
// Pointer is an RFC 6901 pointer.
type Diagnostic struct {
	Rule    RuleID
	Origin  string
	Pointer string
	Detail  string
}

// Layer is one already-discovered cast layer. Layers are ordered from highest
// to lowest precedence.
type Layer struct {
	Origin string
	Data   []byte
}

// Cast is a validated, defaulted resolved cast. Its representation is private
// so callers cannot mutate the effective performers or bindings.
type Cast struct {
	performers         map[string]performer
	bindings           map[string]binding
	performersComplete bool
}

// PerformerView is a defensive view of an effective performer.
type PerformerView struct {
	ID                       string
	Adapter                  string
	Model                    string
	AllowAdvisoryEnforcement bool
	Extensions               map[string]any
}

// BindingView is a defensive view of an effective part binding.
type BindingView struct {
	PartID    string
	Performer string
	Fallbacks []string
	Strict    bool
}

// Probe contains the successful, deterministic facts consumed from one
// adapter probe. Discovery, transport, and probe failures belong to the caller.
type Probe struct {
	Capabilities protocol.Capabilities
	Enforcement  protocol.Enforcement
}

// EnforcementDimension names one independently enforceable withheld authority.
type EnforcementDimension string

const (
	DimensionReadOnly      EnforcementDimension = "read_only"
	DimensionPathGrants    EnforcementDimension = "path_grants"
	DimensionReadGrants    EnforcementDimension = "read_grants"
	DimensionShellGrants   EnforcementDimension = "shell_grants"
	DimensionNetworkGrants EnforcementDimension = "network_grants"
)

// EnforcementDisposition is the result of the fail-closed predicate.
type EnforcementDisposition string

const (
	EnforcementStrict   EnforcementDisposition = "strict"
	EnforcementAdvisory EnforcementDisposition = "advisory"
	EnforcementRefused  EnforcementDisposition = "refused"
)

// EnforcementResult carries the exact unmet dimension set. Strict results have
// an empty set; advisory and refused results differ only in cast authorization.
type EnforcementResult struct {
	Disposition EnforcementDisposition
	Unmet       []EnforcementDimension
}

// CapabilityAssessment is one primary or fallback performer's capability
// compatibility with a part.
type CapabilityAssessment struct {
	PerformerID         string
	MissingCapabilities []string
}

// EnforcementAssessment is one primary or fallback performer's result for one
// movement.
type EnforcementAssessment struct {
	PerformerID string
	Result      EnforcementResult
}

type performer struct {
	Adapter                  string
	Model                    string
	AllowAdvisoryEnforcement bool
	Extensions               map[string]any
	Origin                   string
}

type binding struct {
	Performer      string
	PerformerValid bool
	Fallbacks      []string
	FallbacksValid []bool
	Origin         string
	Valid          bool
}

type layerDocument struct {
	Performers       map[string]performer
	Bindings         map[string]binding
	PerformersUsable bool
	BindingsUsable   bool
}
