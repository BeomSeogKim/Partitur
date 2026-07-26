package validate

import (
	"os"
	"sort"

	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

type prober interface {
	ProbeAll([]string) adapter.Report
}

type dependencies struct {
	acquisition acquisitionDependencies
	newProber   func() prober
}

// Run discovers inputs relative to the invocation working directory and
// returns the complete validation result in rendering order.
func Run() Result {
	return run(dependencies{
		acquisition: acquisitionDependencies{
			workingDirectory: os.Getwd,
			userHome:         os.UserHomeDir,
			readFile:         os.ReadFile,
		},
		newProber: func() prober {
			return adapter.NewClient()
		},
	})
}

func run(dependencies dependencies) Result {
	input, refusal := acquire(dependencies.acquisition)
	if refusal != nil {
		return Result{Refusal: refusal}
	}
	return evaluate(input.score, input.layers, dependencies.newProber)
}

func evaluate(
	source []byte,
	layers []cast.Layer,
	newProber func() prober,
) Result {
	compiled, scoreDiagnostics := score.Compile(source)
	resolved, castDiagnostics := cast.Resolve(layers)

	result := Result{
		Entries: make([]Entry, 0, len(scoreDiagnostics)+len(castDiagnostics)),
	}
	for _, diagnostic := range scoreDiagnostics {
		result.Entries = append(result.Entries, Entry{
			Kind:    EntryScore,
			Rule:    string(diagnostic.Rule),
			Pointer: diagnostic.Pointer,
			Detail:  diagnostic.Detail,
		})
	}
	for _, diagnostic := range castDiagnostics {
		result.Entries = append(result.Entries, castEntry(diagnostic))
	}

	for _, diagnostic := range resolved.ValidateScore(compiled) {
		result.Entries = append(result.Entries, castEntry(diagnostic))
	}

	adapterIDs := referencedAdapters(compiled, resolved)
	if len(adapterIDs) == 0 {
		return result
	}
	probeReport := newProber().ProbeAll(adapterIDs)
	for _, diagnostic := range probeReport.Diagnostics {
		result.Entries = append(result.Entries, Entry{
			Kind:        EntryAdapterEnvironment,
			AdapterID:   diagnostic.AdapterID,
			AdapterKind: string(diagnostic.Kind),
			Detail:      diagnostic.Detail,
			Stderr:      diagnostic.Stderr,
		})
	}

	probes := make(map[string]cast.Probe, len(probeReport.Probes))
	for _, probe := range probeReport.Probes {
		probes[probe.AdapterID] = cast.Probe{
			Capabilities: probe.Result.Capabilities,
			Enforcement:  probe.Result.Enforcement,
		}
	}
	appendCapabilityEntries(&result, compiled, resolved, probes)
	appendEnforcementEntries(&result, compiled, resolved, probes)
	return result
}

func castEntry(diagnostic cast.Diagnostic) Entry {
	return Entry{
		Kind:    EntryCast,
		Rule:    string(diagnostic.Rule),
		Origin:  diagnostic.Origin,
		Pointer: diagnostic.Pointer,
		Detail:  diagnostic.Detail,
	}
}

func referencedAdapters(compiled *score.Score, resolved *cast.Cast) []string {
	unique := make(map[string]struct{})
	for _, part := range compiled.Parts() {
		binding, exists := resolved.Binding(part.ID)
		if !exists {
			continue
		}
		performers := append([]string{binding.Performer}, binding.Fallbacks...)
		for _, performerID := range performers {
			performer, exists := resolved.Performer(performerID)
			if exists {
				unique[performer.Adapter] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(unique))
	for adapterID := range unique {
		result = append(result, adapterID)
	}
	sort.Strings(result)
	return result
}

func appendCapabilityEntries(
	result *Result,
	compiled *score.Score,
	resolved *cast.Cast,
	probes map[string]cast.Probe,
) {
	for _, part := range compiled.Parts() {
		for _, assessment := range resolved.EvaluatePart(part, probes) {
			if len(assessment.MissingCapabilities) == 0 {
				continue
			}
			result.Entries = append(result.Entries, Entry{
				Kind:                EntryCapability,
				Detail:              "capability_missing",
				PartID:              part.ID,
				PerformerID:         assessment.PerformerID,
				MissingCapabilities: assessment.MissingCapabilities,
			})
		}
	}
}

func appendEnforcementEntries(
	result *Result,
	compiled *score.Score,
	resolved *cast.Cast,
	probes map[string]cast.Probe,
) {
	policy := compiled.EffectivePolicy()
	for _, movement := range compiled.Movements() {
		for _, assessment := range resolved.EvaluateMovement(
			movement,
			policy,
			probes,
		) {
			if assessment.Result.Disposition == cast.EnforcementStrict {
				continue
			}
			kind := EntryEnforcement
			if assessment.Result.Disposition == cast.EnforcementAdvisory {
				kind = EntryEnforcementAdvisory
			}
			result.Entries = append(result.Entries, Entry{
				Kind:            kind,
				Detail:          "enforcement_unmet",
				PartID:          movement.PartID,
				MovementID:      movement.ID,
				PerformerID:     assessment.PerformerID,
				UnmetDimensions: assessment.Result.Unmet,
			})
		}
	}
}
