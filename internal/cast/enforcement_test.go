package cast

import (
	"slices"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

func TestFailClosedTruthTable(t *testing.T) {
	t.Parallel()
	all := protocol.Enforcement{
		PathGrants:    true,
		ReadOnly:      true,
		NetworkGrants: true,
		ShellGrants:   true,
		ReadGrants:    true,
	}
	tests := []struct {
		name      string
		grants    []string
		paths     []string
		disable   func(*protocol.Enforcement)
		dimension EnforcementDimension
	}{
		{
			name:      "repo_write_withheld_requires_read_only",
			grants:    []string{"repo_read", "shell", "network"},
			paths:     []string{"**"},
			disable:   func(value *protocol.Enforcement) { value.ReadOnly = false },
			dimension: DimensionReadOnly,
		},
		{
			name:      "repo_write_path_scoped_requires_path_grants",
			grants:    []string{"repo_write", "shell", "network"},
			paths:     []string{"internal/**"},
			disable:   func(value *protocol.Enforcement) { value.PathGrants = false },
			dimension: DimensionPathGrants,
		},
		{
			name:      "repo_read_withheld_requires_read_grants",
			grants:    []string{"repo_write", "shell", "network"},
			paths:     []string{"**"},
			disable:   func(value *protocol.Enforcement) { value.ReadGrants = false },
			dimension: DimensionReadGrants,
		},
		{
			name:      "repo_read_path_scoped_requires_path_grants",
			grants:    []string{"repo_read", "shell", "network"},
			paths:     []string{"src/**"},
			disable:   func(value *protocol.Enforcement) { value.PathGrants = false },
			dimension: DimensionPathGrants,
		},
		{
			name:      "shell_withheld_requires_shell_grants",
			grants:    []string{"repo_read", "repo_write", "network"},
			paths:     []string{"**"},
			disable:   func(value *protocol.Enforcement) { value.ShellGrants = false },
			dimension: DimensionShellGrants,
		},
		{
			name:      "network_withheld_requires_network_grants",
			grants:    []string{"repo_read", "repo_write", "shell"},
			paths:     []string{"**"},
			disable:   func(value *protocol.Enforcement) { value.NetworkGrants = false },
			dimension: DimensionNetworkGrants,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			enforcement := all
			test.disable(&enforcement)
			got := EvaluateEnforcement(
				score.MovementView{Grants: test.grants},
				score.PolicyView{AllowedPaths: test.paths},
				false,
				enforcement,
			)
			want := EnforcementResult{
				Disposition: EnforcementRefused,
				Unmet:       []EnforcementDimension{test.dimension},
			}
			if !equalEnforcement(got, want) {
				t.Fatalf("result = %#v, want %#v", got, want)
			}

			enforcement = all
			strict := EvaluateEnforcement(
				score.MovementView{Grants: test.grants},
				score.PolicyView{AllowedPaths: test.paths},
				false,
				enforcement,
			)
			if !equalEnforcement(strict, EnforcementResult{
				Disposition: EnforcementStrict,
			}) {
				t.Fatalf("satisfied result = %#v", strict)
			}
		})
	}
}

func TestWholeRepositoryExceptionInBothDirections(t *testing.T) {
	t.Parallel()
	movement := score.MovementView{
		Grants: []string{"repo_read", "repo_write", "shell", "network"},
	}
	enforcement := protocol.Enforcement{}
	enforcement.ReadOnly = true
	enforcement.ReadGrants = true
	enforcement.ShellGrants = true
	enforcement.NetworkGrants = true

	notScoped := EvaluateEnforcement(
		movement,
		score.PolicyView{AllowedPaths: []string{"**"}},
		false,
		enforcement,
	)
	if !equalEnforcement(notScoped, EnforcementResult{
		Disposition: EnforcementStrict,
	}) {
		t.Fatalf("[\"**\"] result = %#v", notScoped)
	}

	for name, paths := range map[string][]string{
		"narrow":              {"internal/**"},
		"empty":               {},
		"not_exact_singleton": {"**", "internal/**"},
	} {
		name, paths := name, paths
		t.Run(name, func(t *testing.T) {
			scoped := EvaluateEnforcement(
				movement,
				score.PolicyView{AllowedPaths: paths},
				false,
				enforcement,
			)
			want := EnforcementResult{
				Disposition: EnforcementRefused,
				Unmet:       []EnforcementDimension{DimensionPathGrants},
			}
			if !equalEnforcement(scoped, want) {
				t.Fatalf("paths %#v result = %#v, want %#v", paths, scoped, want)
			}
		})
	}
}

func TestAdvisoryCarriesExactSortedDimensionSet(t *testing.T) {
	t.Parallel()
	movement := score.MovementView{}
	policy := score.PolicyView{AllowedPaths: []string{"internal/**"}}
	strict := EvaluateEnforcement(
		movement, policy, false, protocol.Enforcement{})
	advisory := EvaluateEnforcement(
		movement, policy, true, protocol.Enforcement{})
	wantDimensions := []EnforcementDimension{
		DimensionNetworkGrants,
		DimensionReadGrants,
		DimensionReadOnly,
		DimensionShellGrants,
	}
	if strict.Disposition != EnforcementRefused ||
		!slices.Equal(strict.Unmet, wantDimensions) {
		t.Fatalf("strict result = %#v", strict)
	}
	if advisory.Disposition != EnforcementAdvisory ||
		!slices.Equal(advisory.Unmet, wantDimensions) {
		t.Fatalf("advisory result = %#v", advisory)
	}
}

func TestPathGrantsDimensionIsDeduplicated(t *testing.T) {
	t.Parallel()
	result := EvaluateEnforcement(
		score.MovementView{
			Grants: []string{"repo_read", "repo_write", "shell", "network"},
		},
		score.PolicyView{AllowedPaths: []string{"internal/**"}},
		false,
		protocol.Enforcement{
			ReadOnly:      true,
			ReadGrants:    true,
			ShellGrants:   true,
			NetworkGrants: true,
		},
	)
	want := EnforcementResult{
		Disposition: EnforcementRefused,
		Unmet:       []EnforcementDimension{DimensionPathGrants},
	}
	if !equalEnforcement(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

func TestOnePartTwoMovementsDifferPerMovement(t *testing.T) {
	t.Parallel()
	resolved := mustResolve(t, Layer{
		Origin: "project",
		Data: encodeFixture(t, map[string]any{
			"cast": "0.1",
			"performers": map[string]any{
				"primary": performerFixture("adapter", "model", nil),
			},
			"bindings": map[string]any{
				"plan": map[string]any{"performer": "primary"},
			},
		}),
	})
	policy := score.PolicyView{AllowedPaths: []string{"**"}}
	probes := map[string]Probe{
		"adapter": {
			Capabilities: protocol.Capabilities{
				RepoRead: true, RepoWrite: true, Shell: true, Network: true,
			},
			Enforcement: protocol.Enforcement{
				ReadOnly:      false,
				ReadGrants:    true,
				ShellGrants:   true,
				NetworkGrants: true,
			},
		},
	}
	write := resolved.EvaluateMovement(
		score.MovementView{
			ID: "write", PartID: "plan",
			Grants: []string{"repo_read", "repo_write", "shell", "network"},
		},
		policy,
		probes,
	)
	readOnly := resolved.EvaluateMovement(
		score.MovementView{
			ID: "read", PartID: "plan",
			Grants: []string{"repo_read", "shell", "network"},
		},
		policy,
		probes,
	)
	if len(write) != 1 || write[0].Result.Disposition != EnforcementStrict {
		t.Fatalf("write assessment = %#v", write)
	}
	wantRead := EnforcementResult{
		Disposition: EnforcementRefused,
		Unmet:       []EnforcementDimension{DimensionReadOnly},
	}
	if len(readOnly) != 1 || !equalEnforcement(readOnly[0].Result, wantRead) {
		t.Fatalf("read assessment = %#v, want %#v", readOnly, wantRead)
	}
}

func TestPrimaryStrictFallbackAdvisoryMakesBindingNonStrict(t *testing.T) {
	t.Parallel()
	strict := mustResolve(t, Layer{
		Origin: "project",
		Data:   encodeFixture(t, completeCastFixture()),
	})
	strictBinding, _ := strict.Binding("plan")
	if !strictBinding.Strict {
		t.Fatal("all-strict chain was not strict")
	}

	primaryAdvisory := completeCastFixture()
	objectAtKey(primaryAdvisory, "performers", "primary")["allow_advisory_enforcement"] = true
	primaryResolved := mustResolve(t, Layer{
		Origin: "project",
		Data:   encodeFixture(t, primaryAdvisory),
	})
	primaryBinding, _ := primaryResolved.Binding("plan")
	if primaryBinding.Strict {
		t.Fatal("advisory primary was ignored by part strictness")
	}

	document := completeCastFixture()
	objectAtKey(document, "performers", "backup")["allow_advisory_enforcement"] = true
	resolved := mustResolve(t, Layer{
		Origin: "project",
		Data:   encodeFixture(t, document),
	})
	binding, _ := resolved.Binding("plan")
	if binding.Strict {
		t.Fatal("advisory fallback was ignored by part strictness")
	}
	assessments := resolved.EvaluateMovement(
		score.MovementView{
			ID: "read", PartID: "plan", Grants: []string{"repo_read"},
		},
		score.PolicyView{AllowedPaths: []string{"**"}},
		map[string]Probe{
			"adapter": {
				Capabilities: protocol.Capabilities{RepoRead: true},
				Enforcement: protocol.Enforcement{
					ReadOnly:      true,
					ReadGrants:    true,
					ShellGrants:   true,
					NetworkGrants: true,
				},
			},
			"backup-adapter": {
				Capabilities: protocol.Capabilities{RepoRead: true},
				Enforcement:  protocol.Enforcement{},
			},
		},
	)
	if len(assessments) != 2 {
		t.Fatalf("assessments = %#v", assessments)
	}
	if assessments[0].PerformerID != "primary" ||
		assessments[0].Result.Disposition != EnforcementStrict ||
		len(assessments[0].Result.Unmet) != 0 {
		t.Fatalf("primary assessment = %#v", assessments[0])
	}
	wantUnmet := []EnforcementDimension{
		DimensionNetworkGrants,
		DimensionReadOnly,
		DimensionShellGrants,
	}
	if assessments[1].PerformerID != "backup" ||
		assessments[1].Result.Disposition != EnforcementAdvisory ||
		!slices.Equal(assessments[1].Result.Unmet, wantUnmet) {
		t.Fatalf("fallback assessment = %#v", assessments[1])
	}
}

func TestSameAdapterPerformersAreAssessedIndependently(t *testing.T) {
	t.Parallel()
	document := completeCastFixture()
	objectAtKey(document, "performers", "backup")["adapter"] = "adapter"
	objectAtKey(document, "performers", "backup")["allow_advisory_enforcement"] = true
	resolved := mustResolve(t, Layer{
		Origin: "project",
		Data:   encodeFixture(t, document),
	})
	probes := map[string]Probe{
		"adapter": {
			Capabilities: protocol.Capabilities{RepoRead: true},
		},
	}

	capabilities := resolved.EvaluatePart(
		score.PartView{ID: "plan", Capabilities: []string{"repo_read"}},
		probes,
	)
	if len(capabilities) != 2 ||
		capabilities[0].PerformerID != "primary" ||
		capabilities[1].PerformerID != "backup" ||
		len(capabilities[0].MissingCapabilities) != 0 ||
		len(capabilities[1].MissingCapabilities) != 0 {
		t.Fatalf("same-adapter capability assessments = %#v", capabilities)
	}

	enforcement := resolved.EvaluateMovement(
		score.MovementView{
			ID: "read", PartID: "plan", Grants: []string{"repo_read"},
		},
		score.PolicyView{AllowedPaths: []string{"**"}},
		probes,
	)
	wantUnmet := []EnforcementDimension{
		DimensionNetworkGrants,
		DimensionReadOnly,
		DimensionShellGrants,
	}
	if len(enforcement) != 2 ||
		enforcement[0].PerformerID != "primary" ||
		enforcement[0].Result.Disposition != EnforcementRefused ||
		!slices.Equal(enforcement[0].Result.Unmet, wantUnmet) ||
		enforcement[1].PerformerID != "backup" ||
		enforcement[1].Result.Disposition != EnforcementAdvisory ||
		!slices.Equal(enforcement[1].Result.Unmet, wantUnmet) {
		t.Fatalf("same-adapter enforcement assessments = %#v", enforcement)
	}
}

func TestCapabilitiesApplyToPrimaryAndEveryFallback(t *testing.T) {
	t.Parallel()
	resolved := mustResolve(t, Layer{
		Origin: "project",
		Data:   encodeFixture(t, completeCastFixture()),
	})
	part := score.PartView{
		ID: "plan",
		Capabilities: []string{
			"network", "repo_read", "repo_write", "resumable_sessions", "shell",
		},
	}
	probes := map[string]Probe{
		"adapter": {
			Capabilities: protocol.Capabilities{RepoRead: true},
			Enforcement: protocol.Enforcement{
				ReadOnly: true, ReadGrants: true,
				ShellGrants: true, NetworkGrants: true,
			},
		},
		"backup-adapter": {
			Capabilities: protocol.Capabilities{
				RepoRead: true, Network: true,
			},
			Enforcement: protocol.Enforcement{
				ReadOnly: true, ReadGrants: true,
				ShellGrants: true, NetworkGrants: true,
			},
		},
	}
	assessments := resolved.EvaluatePart(
		part,
		probes,
	)
	if len(assessments) != 2 {
		t.Fatalf("assessments = %#v", assessments)
	}
	wantPrimary := []string{"network", "repo_write", "resumable_sessions", "shell"}
	wantBackup := []string{"repo_write", "resumable_sessions", "shell"}
	if assessments[0].PerformerID != "primary" ||
		!slices.Equal(assessments[0].MissingCapabilities, wantPrimary) {
		t.Fatalf("primary capabilities = %#v", assessments[0])
	}
	if assessments[1].PerformerID != "backup" ||
		!slices.Equal(assessments[1].MissingCapabilities, wantBackup) {
		t.Fatalf("fallback capabilities = %#v", assessments[1])
	}
}

func TestCapabilityTruthTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		enable func(*protocol.Capabilities)
	}{
		{"repo_read", func(value *protocol.Capabilities) { value.RepoRead = true }},
		{"repo_write", func(value *protocol.Capabilities) { value.RepoWrite = true }},
		{"shell", func(value *protocol.Capabilities) { value.Shell = true }},
		{"network", func(value *protocol.Capabilities) { value.Network = true }},
		{"resumable_sessions", func(value *protocol.Capabilities) {
			value.ResumableSessions = true
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			part := score.PartView{ID: "part", Capabilities: []string{test.name}}
			if got := MissingCapabilities(part, protocol.Capabilities{}); !slices.Equal(
				got, []string{test.name},
			) {
				t.Fatalf("missing result = %#v", got)
			}
			var available protocol.Capabilities
			test.enable(&available)
			if got := MissingCapabilities(part, available); len(got) != 0 {
				t.Fatalf("available result = %#v", got)
			}
		})
	}
	unknown := MissingCapabilities(
		score.PartView{ID: "part", Capabilities: []string{"future_capability"}},
		protocol.Capabilities{
			RepoRead: true, RepoWrite: true, Shell: true, Network: true,
			ResumableSessions: true,
		},
	)
	if !slices.Equal(unknown, []string{"future_capability"}) {
		t.Fatalf("unknown capability result = %#v", unknown)
	}
}

func TestCapabilityChecksAreNotAdvisoryAndDoNotCheckModels(t *testing.T) {
	t.Parallel()
	document := map[string]any{
		"cast": "0.1",
		"performers": map[string]any{
			"primary": map[string]any{
				"adapter":                    "adapter",
				"model":                      "cast-model-not-in-probe",
				"allow_advisory_enforcement": true,
			},
		},
		"bindings": map[string]any{
			"plan": map[string]any{"performer": "primary"},
		},
	}
	resolved := mustResolve(t, Layer{
		Origin: "project", Data: encodeFixture(t, document),
	})
	assessments := resolved.EvaluatePart(
		score.PartView{
			ID: "plan", Capabilities: []string{"network", "repo_read"},
		},
		map[string]Probe{
			"adapter": {
				Capabilities: protocol.Capabilities{
					RepoRead: true,
					Models:   []protocol.Model{{ID: "some-other-model"}},
				},
			},
		},
	)
	want := []CapabilityAssessment{{
		PerformerID: "primary", MissingCapabilities: []string{"network"},
	}}
	if len(assessments) != 1 ||
		assessments[0].PerformerID != want[0].PerformerID ||
		!slices.Equal(
			assessments[0].MissingCapabilities,
			want[0].MissingCapabilities,
		) {
		t.Fatalf("assessments = %#v, want %#v", assessments, want)
	}
}

func TestMissingProbeSuppressesDerivativeAssessment(t *testing.T) {
	t.Parallel()
	resolved := mustResolve(t, Layer{
		Origin: "project",
		Data:   encodeFixture(t, completeCastFixture()),
	})
	assessments := resolved.EvaluateMovement(
		score.MovementView{ID: "read", PartID: "plan", Grants: []string{"repo_read"}},
		score.PolicyView{AllowedPaths: []string{"**"}},
		map[string]Probe{
			"adapter": {
				Capabilities: protocol.Capabilities{RepoRead: true},
				Enforcement: protocol.Enforcement{
					ReadOnly: true, ReadGrants: true,
					ShellGrants: true, NetworkGrants: true,
				},
			},
		},
	)
	if len(assessments) != 1 || assessments[0].PerformerID != "primary" {
		t.Fatalf("missing fallback probe suppressed observed primary: %#v", assessments)
	}
	capabilities := resolved.EvaluatePart(
		score.PartView{ID: "plan", Capabilities: []string{"repo_read"}},
		map[string]Probe{
			"adapter": {
				Capabilities: protocol.Capabilities{RepoRead: true},
			},
		},
	)
	if len(capabilities) != 1 || capabilities[0].PerformerID != "primary" {
		t.Fatalf("missing fallback probe suppressed observed capabilities: %#v",
			capabilities)
	}

	backupProbe := map[string]Probe{
		"backup-adapter": {
			Capabilities: protocol.Capabilities{RepoRead: true},
			Enforcement: protocol.Enforcement{
				ReadOnly: true, ReadGrants: true,
				ShellGrants: true, NetworkGrants: true,
			},
		},
	}
	reverseEnforcement := resolved.EvaluateMovement(
		score.MovementView{
			ID: "read", PartID: "plan", Grants: []string{"repo_read"},
		},
		score.PolicyView{AllowedPaths: []string{"**"}},
		backupProbe,
	)
	if len(reverseEnforcement) != 1 ||
		reverseEnforcement[0].PerformerID != "backup" {
		t.Fatalf("missing primary probe suppressed observed fallback enforcement: %#v",
			reverseEnforcement)
	}
	reverseCapabilities := resolved.EvaluatePart(
		score.PartView{ID: "plan", Capabilities: []string{"repo_read"}},
		backupProbe,
	)
	if len(reverseCapabilities) != 1 ||
		reverseCapabilities[0].PerformerID != "backup" {
		t.Fatalf("missing primary probe suppressed observed fallback capabilities: %#v",
			reverseCapabilities)
	}
}

func equalEnforcement(left, right EnforcementResult) bool {
	return left.Disposition == right.Disposition &&
		slices.Equal(left.Unmet, right.Unmet)
}
