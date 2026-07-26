package cast

import (
	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

// ValidateScore evaluates cast rules whose truth depends on the compiled score.
// A cast may be valid without bindings until a score declares parts.
func (c *Cast) ValidateScore(compiled *score.Score) []Diagnostic {
	if c == nil || compiled == nil {
		return nil
	}
	var diagnostics []Diagnostic
	for _, part := range compiled.Parts() {
		if _, exists := c.bindings[part.ID]; !exists {
			addDiagnostic(&diagnostics, RuleScore, "",
				pointerJoin("/bindings", part.ID), "binding_missing")
		}
	}
	sortDiagnostics(diagnostics)
	return diagnostics
}

// MissingCapabilities returns every capability required by part that the
// successful probe does not advertise.
func MissingCapabilities(
	part score.PartView,
	capabilities protocol.Capabilities,
) []string {
	var missing []string
	for _, required := range part.Capabilities {
		if !supportsCapability(capabilities, required) {
			missing = append(missing, required)
		}
	}
	return sortedStrings(missing)
}

// EvaluatePart evaluates the primary and every fallback independently, in
// fallback-chain order, against the part's required capabilities.
func (c *Cast) EvaluatePart(
	part score.PartView,
	probes map[string]Probe,
) []CapabilityAssessment {
	if c == nil {
		return nil
	}
	binding, exists := c.bindings[part.ID]
	if !exists {
		return nil
	}
	chain := append([]string{binding.Performer}, binding.Fallbacks...)
	result := make([]CapabilityAssessment, 0, len(chain))
	for _, performerID := range chain {
		performer, exists := c.performers[performerID]
		if !exists {
			continue
		}
		probe, observed := probes[performer.Adapter]
		if !observed {
			continue
		}
		result = append(result, CapabilityAssessment{
			PerformerID:         performerID,
			MissingCapabilities: MissingCapabilities(part, probe.Capabilities),
		})
	}
	return result
}

// EvaluateMovement evaluates the primary and every fallback independently, in
// fallback-chain order, against one movement's effective grants. Adapters
// absent from probes are suppressed: a caller that owns the probe failure must
// not manufacture capability or enforcement outcomes without observed input.
func (c *Cast) EvaluateMovement(
	movement score.MovementView,
	policy score.PolicyView,
	probes map[string]Probe,
) []EnforcementAssessment {
	if c == nil {
		return nil
	}
	binding, exists := c.bindings[movement.PartID]
	if !exists {
		return nil
	}
	chain := append([]string{binding.Performer}, binding.Fallbacks...)
	result := make([]EnforcementAssessment, 0, len(chain))
	for _, performerID := range chain {
		performer, exists := c.performers[performerID]
		if !exists {
			continue
		}
		probe, observed := probes[performer.Adapter]
		if !observed {
			continue
		}
		result = append(result, EnforcementAssessment{
			PerformerID: performerID,
			Result: EvaluateEnforcement(
				movement,
				policy,
				performer.AllowAdvisoryEnforcement,
				probe.Enforcement,
			),
		})
	}
	return result
}

func supportsCapability(capabilities protocol.Capabilities, required string) bool {
	switch required {
	case "repo_read":
		return capabilities.RepoRead
	case "repo_write":
		return capabilities.RepoWrite
	case "shell":
		return capabilities.Shell
	case "network":
		return capabilities.Network
	case "resumable_sessions":
		return capabilities.ResumableSessions
	default:
		return false
	}
}
