package validate

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/adapter"
	"github.com/BeomSeogKim/Partitur/internal/cast"
	"github.com/BeomSeogKim/Partitur/internal/protocol"
)

func TestAcquisitionUsesWorkingDirectoryAndLayerPrecedence(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "work", "repo")
	home := filepath.Join(string(filepath.Separator), "users", "operator")
	scorePath := filepath.Join(root, "partitur.yaml")
	projectPath := filepath.Join(root, ".partitur", "cast.yaml")
	userPath := filepath.Join(home, ".config", "partitur", "cast.yaml")
	files := map[string][]byte{
		scorePath:   []byte("score"),
		projectPath: []byte("project"),
		userPath:    []byte("user"),
	}
	var requested []string
	input, refusal := acquire(acquisitionDependencies{
		workingDirectory: func() (string, error) { return root, nil },
		userHome:         func() (string, error) { return home, nil },
		readFile: func(path string) ([]byte, error) {
			requested = append(requested, path)
			data, exists := files[path]
			if !exists {
				return nil, os.ErrNotExist
			}
			return data, nil
		},
	})
	if refusal != nil {
		t.Fatalf("refusal = %#v", refusal)
	}
	if string(input.score) != "score" ||
		!reflect.DeepEqual(input.layers, []cast.Layer{
			{Origin: "project", Data: []byte("project")},
			{Origin: "user-global", Data: []byte("user")},
		}) {
		t.Fatalf("inputs = %#v", input)
	}
	if !slices.Equal(requested, []string{
		scorePath,
		projectPath,
		userPath,
	}) {
		t.Fatalf("read paths = %#v", requested)
	}
}

func TestPrepareReturnsAnchoredValidatedInputs(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "work", "repo")
	home := filepath.Join(string(filepath.Separator), "users", "operator")
	scorePath := filepath.Join(root, "partitur.yaml")
	castPath := filepath.Join(root, ".partitur", "cast.yaml")
	scoreSource := encode(t, validDraftScore("plan"))
	castSource := encode(
		t,
		validCast(map[string]string{"plan": "performer"}),
	)
	workingDirectoryCalls := 0
	preparation, result := prepareValidated(acquisitionDependencies{
		workingDirectory: func() (string, error) {
			workingDirectoryCalls++
			return root, nil
		},
		userHome: func() (string, error) { return home, nil },
		readFile: func(path string) ([]byte, error) {
			switch path {
			case scorePath:
				return scoreSource, nil
			case castPath:
				return castSource, nil
			default:
				return nil, os.ErrNotExist
			}
		},
	})
	if result.Refusal != nil || len(result.Entries) != 0 {
		t.Fatalf("result = %#v", result)
	}
	if preparation == nil {
		t.Fatal("preparation is nil")
	}
	if workingDirectoryCalls != 1 {
		t.Fatalf("working directory calls = %d, want 1", workingDirectoryCalls)
	}
	if preparation.RepositoryRoot != root {
		t.Fatalf("repository root = %q, want %q", preparation.RepositoryRoot, root)
	}
	if got := preparation.Score.Execution().Goal; got != "Validate the fixture." {
		t.Fatalf("score goal = %q", got)
	}
	firstSource := preparation.ScoreSource()
	firstSource[0] = '!'
	if got := preparation.ScoreSource(); bytes.Equal(got, firstSource) ||
		!bytes.Equal(got, scoreSource) {
		t.Fatalf("score source was not defensive: %q", got)
	}
	binding, exists := preparation.Cast.Binding("plan")
	if !exists || binding.Performer != "performer" {
		t.Fatalf("binding = %#v, exists = %t", binding, exists)
	}
}

func TestPrepareRejectsInvalidOrIncompatibleInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		score      map[string]any
		cast       map[string]any
		wantRule   string
		wantDetail string
	}{
		{
			name: "invalid_score",
			score: func() map[string]any {
				document := validDraftScore("plan")
				delete(document, "goal")
				return document
			}(),
			cast:       validCast(map[string]string{"plan": "performer"}),
			wantRule:   "score.schema",
			wantDetail: "required",
		},
		{
			name:  "invalid_cast",
			score: validDraftScore("plan"),
			cast: func() map[string]any {
				document := validCast(map[string]string{"plan": "performer"})
				delete(objectAt(objectAt(document["performers"])["performer"]), "model")
				return document
			}(),
			wantRule:   "cast.schema",
			wantDetail: "required",
		},
		{
			name:       "missing_binding",
			score:      validDraftScore("plan"),
			cast:       validCast(map[string]string{}),
			wantRule:   "cast.score",
			wantDetail: "binding_missing",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(string(filepath.Separator), "repo")
			preparation, result := prepareValidated(acquisitionDependencies{
				workingDirectory: func() (string, error) { return root, nil },
				userHome:         func() (string, error) { return "/home/operator", nil },
				readFile: func(path string) ([]byte, error) {
					switch path {
					case filepath.Join(root, "partitur.yaml"):
						return encode(t, test.score), nil
					case filepath.Join(root, ".partitur", "cast.yaml"):
						return encode(t, test.cast), nil
					default:
						return nil, os.ErrNotExist
					}
				},
			})
			if preparation != nil {
				t.Fatalf("preparation = %#v, want nil", preparation)
			}
			if len(result.Entries) != 1 {
				t.Fatalf("entries = %#v", result.Entries)
			}
			entry := result.Entries[0]
			if entry.Rule != test.wantRule || entry.Detail != test.wantDetail {
				t.Fatalf(
					"entry = %#v, want rule %q detail %q",
					entry,
					test.wantRule,
					test.wantDetail,
				)
			}
		})
	}
}

func TestMissingOptionalLayersAreAbsent(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "repo")
	scorePath := filepath.Join(root, "partitur.yaml")
	input, refusal := acquire(acquisitionDependencies{
		workingDirectory: func() (string, error) { return root, nil },
		userHome:         func() (string, error) { return "/home/operator", nil },
		readFile: func(path string) ([]byte, error) {
			if path == scorePath {
				return []byte("score"), nil
			}
			return nil, os.ErrNotExist
		},
	})
	if refusal != nil || len(input.layers) != 0 {
		t.Fatalf("inputs = %#v, refusal = %#v", input, refusal)
	}
}

func TestAcquisitionRefusalBoundaries(t *testing.T) {
	t.Parallel()
	denied := errors.New("permission denied")
	tests := []struct {
		name string
		deps acquisitionDependencies
		want Refusal
	}{
		{
			name: "working_directory",
			deps: acquisitionDependencies{
				workingDirectory: func() (string, error) { return "", denied },
			},
			want: Refusal{
				Kind:   RefusalWorkingDirectory,
				Detail: denied.Error(),
			},
		},
		{
			name: "required_score",
			deps: acquisitionDependencies{
				workingDirectory: func() (string, error) { return "/repo", nil },
				readFile:         func(string) ([]byte, error) { return nil, os.ErrNotExist },
			},
			want: Refusal{
				Kind:   RefusalRequiredInput,
				Path:   filepath.Join("/repo", "partitur.yaml"),
				Detail: os.ErrNotExist.Error(),
			},
		},
		{
			name: "discovered_project_cast",
			deps: acquisitionDependencies{
				workingDirectory: func() (string, error) { return "/repo", nil },
				readFile: func(path string) ([]byte, error) {
					if path == filepath.Join("/repo", "partitur.yaml") {
						return []byte("score"), nil
					}
					return nil, denied
				},
			},
			want: Refusal{
				Kind:   RefusalDiscoveredInput,
				Path:   filepath.Join("/repo", ".partitur", "cast.yaml"),
				Detail: denied.Error(),
			},
		},
		{
			name: "user_home",
			deps: acquisitionDependencies{
				workingDirectory: func() (string, error) { return "/repo", nil },
				userHome:         func() (string, error) { return "", denied },
				readFile: func(path string) ([]byte, error) {
					if path == filepath.Join("/repo", "partitur.yaml") {
						return []byte("score"), nil
					}
					return nil, os.ErrNotExist
				},
			},
			want: Refusal{
				Kind:   RefusalUserHomeDirectory,
				Detail: denied.Error(),
			},
		},
		{
			name: "discovered_user_cast",
			deps: acquisitionDependencies{
				workingDirectory: func() (string, error) { return "/repo", nil },
				userHome:         func() (string, error) { return "/home/operator", nil },
				readFile: func(path string) ([]byte, error) {
					switch path {
					case filepath.Join("/repo", "partitur.yaml"):
						return []byte("score"), nil
					case filepath.Join("/repo", ".partitur", "cast.yaml"):
						return nil, os.ErrNotExist
					default:
						return nil, denied
					}
				},
			},
			want: Refusal{
				Kind: RefusalDiscoveredInput,
				Path: filepath.Join(
					"/home/operator",
					".config",
					"partitur",
					"cast.yaml",
				),
				Detail: denied.Error(),
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			_, got := acquire(test.deps)
			if got == nil || *got != test.want {
				t.Fatalf("refusal = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestScoreThenCastOrderAndDerivativeSuppression(t *testing.T) {
	t.Parallel()
	scoreDocument := validDraftScore("plan")
	delete(scoreDocument, "goal")
	castDocument := validCast(map[string]string{"plan": "performer"})
	delete(objectAt(objectAt(castDocument["performers"])["performer"]), "model")

	proberCreated := false
	result := evaluate(
		encode(t, scoreDocument),
		[]cast.Layer{{Origin: "project", Data: encode(t, castDocument)}},
		func() prober {
			proberCreated = true
			return fakeProber{}
		},
	)
	want := []Entry{
		{
			Kind:    EntryScore,
			Rule:    "score.schema",
			Pointer: "/goal",
			Detail:  "required",
		},
		{
			Kind:    EntryCast,
			Rule:    "cast.schema",
			Origin:  "project",
			Pointer: "/performers/performer/model",
			Detail:  "required",
		},
	}
	if !reflect.DeepEqual(result.Entries, want) {
		t.Fatalf("entries differ\n got: %#v\nwant: %#v", result.Entries, want)
	}
	if proberCreated {
		t.Fatal("derivative probe was created")
	}
}

func TestScoreFailureSuppressesDerivativeStages(t *testing.T) {
	t.Parallel()
	scoreDocument := validDraftScore("plan")
	delete(scoreDocument, "goal")
	proberCreated := false
	result := evaluate(
		encode(t, scoreDocument),
		[]cast.Layer{{
			Origin: "project",
			Data: encode(
				t,
				validCast(map[string]string{"plan": "performer"}),
			),
		}},
		func() prober {
			proberCreated = true
			return fakeProber{}
		},
	)
	if proberCreated || len(result.Entries) != 1 ||
		result.Entries[0].Kind != EntryScore {
		t.Fatalf(
			"proberCreated=%t entries=%#v",
			proberCreated,
			result.Entries,
		)
	}
}

func TestCastResolutionFailureSuppressesDerivativeStages(t *testing.T) {
	t.Parallel()
	castDocument := validCast(map[string]string{"plan": "performer"})
	delete(objectAt(objectAt(castDocument["performers"])["performer"]), "model")
	proberCreated := false
	result := evaluate(
		encode(t, validDraftScore("plan")),
		[]cast.Layer{{Origin: "project", Data: encode(t, castDocument)}},
		func() prober {
			proberCreated = true
			return fakeProber{}
		},
	)
	if proberCreated || len(result.Entries) != 1 ||
		result.Entries[0].Kind != EntryCast {
		t.Fatalf(
			"proberCreated=%t entries=%#v",
			proberCreated,
			result.Entries,
		)
	}
}

func TestCastAdapterCapabilityEnforcementOrderAndSuppression(t *testing.T) {
	t.Parallel()
	scoreDocument := validDraftScore(
		"bad",
		"capability",
		"enforcement",
		"missing",
	)
	castDocument := validCast(map[string]string{
		"bad":         "bad-performer",
		"capability":  "capability-performer",
		"enforcement": "enforcement-performer",
	})
	performers := objectAt(castDocument["performers"])
	objectAt(performers["bad-performer"])["adapter"] = "bad"
	objectAt(performers["capability-performer"])["adapter"] = "capability"
	objectAt(performers["enforcement-performer"])["adapter"] = "enforcement"

	fake := &recordingProber{report: adapter.Report{
		Diagnostics: []adapter.Diagnostic{{
			AdapterID: "bad",
			Kind:      adapter.DiagnosticExecutableAbsent,
			Detail:    "partitur-adapter-bad not found",
		}},
		Probes: []adapter.Probe{
			{
				AdapterID: "capability",
				Result: probeResult(
					"capability",
					protocol.Capabilities{
						RepoRead: true,
						Shell:    true,
					},
					allEnforcement(),
				),
			},
			{
				AdapterID: "enforcement",
				Result: probeResult(
					"enforcement",
					allCapabilities(),
					protocol.Enforcement{
						PathGrants:    true,
						ReadGrants:    true,
						ShellGrants:   true,
						NetworkGrants: true,
					},
				),
			},
		},
	}}
	result := evaluate(
		encode(t, scoreDocument),
		[]cast.Layer{{Origin: "project", Data: encode(t, castDocument)}},
		func() prober { return fake },
	)
	wantAdapters := []string{"bad", "capability", "enforcement"}
	if !slices.Equal(fake.adapterIDs, wantAdapters) {
		t.Fatalf("adapter ids = %#v, want %#v", fake.adapterIDs, wantAdapters)
	}
	want := []Entry{
		{
			Kind:    EntryCast,
			Rule:    "cast.score",
			Pointer: "/bindings/missing",
			Detail:  "binding_missing",
		},
		{
			Kind:        EntryAdapterEnvironment,
			AdapterID:   "bad",
			AdapterKind: "executable_absent",
			Detail:      "partitur-adapter-bad not found",
		},
		{
			Kind:                EntryCapability,
			Detail:              "capability_missing",
			PartID:              "capability",
			PerformerID:         "capability-performer",
			MissingCapabilities: []string{"network"},
		},
		{
			Kind:            EntryEnforcement,
			Detail:          "enforcement_unmet",
			PartID:          "enforcement",
			MovementID:      "enforcement-movement",
			PerformerID:     "enforcement-performer",
			UnmetDimensions: []cast.EnforcementDimension{cast.DimensionReadOnly},
		},
	}
	if !reflect.DeepEqual(result.Entries, want) {
		t.Fatalf("entries differ\n got: %#v\nwant: %#v", result.Entries, want)
	}
}

func TestAdapterDeduplicationAndBindingLocalSuppression(t *testing.T) {
	t.Parallel()
	scoreDocument := validDraftScore("alpha", "beta", "missing")
	castDocument := validCast(map[string]string{
		"alpha": "alpha-primary",
		"beta":  "beta-primary",
	})
	performers := objectAt(castDocument["performers"])
	objectAt(performers["alpha-primary"])["adapter"] = "shared"
	objectAt(performers["beta-primary"])["adapter"] = "shared"
	bindings := objectAt(castDocument["bindings"])
	objectAt(bindings["alpha"])["fallbacks"] = []any{"alpha-fallback"}
	performers["alpha-fallback"] = map[string]any{
		"adapter": "shared",
		"model":   "model",
	}

	fake := &recordingProber{report: adapter.Report{
		Probes: []adapter.Probe{{
			AdapterID: "shared",
			Result:    probeResult("shared", allCapabilities(), allEnforcement()),
		}},
	}}
	result := evaluate(
		encode(t, scoreDocument),
		[]cast.Layer{{Origin: "project", Data: encode(t, castDocument)}},
		func() prober { return fake },
	)
	if !slices.Equal(fake.adapterIDs, []string{"shared"}) {
		t.Fatalf("adapter ids = %#v", fake.adapterIDs)
	}
	want := []Entry{{
		Kind:    EntryCast,
		Rule:    "cast.score",
		Pointer: "/bindings/missing",
		Detail:  "binding_missing",
	}}
	if !reflect.DeepEqual(result.Entries, want) {
		t.Fatalf("entries = %#v, want %#v", result.Entries, want)
	}
}

func TestAdvisoryReportIsExactAndNonFatal(t *testing.T) {
	t.Parallel()
	scoreDocument := validDraftScore("plan")
	objectAt(scoreDocument["movements"].([]any)[0])["grants"] = []any{"repo_read"}
	castDocument := validCast(map[string]string{"plan": "performer"})
	objectAt(objectAt(castDocument["performers"])["performer"])["allow_advisory_enforcement"] = true
	fake := fakeProber{report: adapter.Report{
		Probes: []adapter.Probe{{
			AdapterID: "performer-adapter",
			Result: probeResult(
				"performer-adapter",
				allCapabilities(),
				protocol.Enforcement{},
			),
		}},
	}}
	result := evaluate(
		encode(t, scoreDocument),
		[]cast.Layer{{Origin: "project", Data: encode(t, castDocument)}},
		func() prober { return fake },
	)
	want := []Entry{{
		Kind:        EntryEnforcementAdvisory,
		Detail:      "enforcement_unmet",
		PartID:      "plan",
		MovementID:  "plan-movement",
		PerformerID: "performer",
		UnmetDimensions: []cast.EnforcementDimension{
			cast.DimensionNetworkGrants,
			cast.DimensionReadOnly,
			cast.DimensionShellGrants,
		},
	}}
	if !reflect.DeepEqual(result.Entries, want) {
		t.Fatalf("entries = %#v, want %#v", result.Entries, want)
	}
	if result.HasDiagnostics() {
		t.Fatal("advisory report was treated as a diagnostic")
	}
}

func TestRefusedEnforcementIsFatal(t *testing.T) {
	t.Parallel()
	scoreDocument := validDraftScore("plan")
	castDocument := validCast(map[string]string{"plan": "performer"})
	fake := fakeProber{report: adapter.Report{
		Probes: []adapter.Probe{{
			AdapterID: "performer-adapter",
			Result: probeResult(
				"performer-adapter",
				allCapabilities(),
				protocol.Enforcement{},
			),
		}},
	}}
	result := evaluate(
		encode(t, scoreDocument),
		[]cast.Layer{{Origin: "project", Data: encode(t, castDocument)}},
		func() prober { return fake },
	)
	if len(result.Entries) != 1 ||
		result.Entries[0].Kind != EntryEnforcement ||
		!result.HasDiagnostics() {
		t.Fatalf("result = %#v", result)
	}
}

func TestCapabilityDoesNotSuppressIndependentEnforcement(t *testing.T) {
	t.Parallel()
	scoreDocument := validDraftScore("plan")
	castDocument := validCast(map[string]string{"plan": "performer"})
	fake := fakeProber{report: adapter.Report{
		Probes: []adapter.Probe{{
			AdapterID: "performer-adapter",
			Result: probeResult(
				"performer-adapter",
				protocol.Capabilities{
					RepoRead: true,
					Shell:    true,
				},
				protocol.Enforcement{},
			),
		}},
	}}
	result := evaluate(
		encode(t, scoreDocument),
		[]cast.Layer{{Origin: "project", Data: encode(t, castDocument)}},
		func() prober { return fake },
	)
	wantKinds := []EntryKind{EntryCapability, EntryEnforcement}
	gotKinds := make([]EntryKind, len(result.Entries))
	for index, entry := range result.Entries {
		gotKinds[index] = entry.Kind
	}
	if !slices.Equal(gotKinds, wantKinds) {
		t.Fatalf("entry kinds = %#v, want %#v", gotKinds, wantKinds)
	}
}

type fakeProber struct {
	report adapter.Report
}

func (f fakeProber) ProbeAll([]string) adapter.Report {
	return f.report
}

type recordingProber struct {
	report     adapter.Report
	adapterIDs []string
}

func (f *recordingProber) ProbeAll(adapterIDs []string) adapter.Report {
	f.adapterIDs = slices.Clone(adapterIDs)
	return f.report
}

func validDraftScore(parts ...string) map[string]any {
	partValues := make(map[string]any, len(parts))
	movements := make([]any, 0, len(parts))
	for index, partID := range parts {
		partValues[partID] = map[string]any{
			"capabilities": []any{
				"repo_read",
				"shell",
				"network",
			},
		}
		movement := map[string]any{
			"id":          partID + "-movement",
			"part":        partID,
			"grants":      []any{"repo_read", "shell", "network"},
			"instruction": "Perform " + partID + ".",
		}
		if index == 0 {
			movement["phase"] = "draft"
		}
		movements = append(movements, movement)
	}
	return map[string]any{
		"score":    "0.2",
		"name":     "validate-fixture",
		"revision": float64(1),
		"status":   "draft",
		"goal":     "Validate the fixture.",
		"draft": map[string]any{
			"interview_movement": parts[0] + "-movement",
		},
		"parts":     partValues,
		"movements": movements,
		"policy": map[string]any{
			"allowed_paths": []any{"**"},
			"budget": map[string]any{
				"active_wall_clock_min": float64(10),
			},
		},
	}
}

func validCast(bindings map[string]string) map[string]any {
	performers := make(map[string]any, len(bindings))
	bindingValues := make(map[string]any, len(bindings))
	for partID, performerID := range bindings {
		performers[performerID] = map[string]any{
			"adapter": performerID + "-adapter",
			"model":   "model",
		}
		bindingValues[partID] = map[string]any{
			"performer": performerID,
		}
	}
	return map[string]any{
		"cast":       "0.1",
		"performers": performers,
		"bindings":   bindingValues,
	}
}

func probeResult(
	adapterID string,
	capabilities protocol.Capabilities,
	enforcement protocol.Enforcement,
) protocol.ProbeResult {
	return protocol.ProbeResult{
		Protocol: protocol.ProtocolVersion,
		Adapter: protocol.AdapterIdentity{
			ID:      adapterID,
			Version: "1.2.3",
		},
		Capabilities: capabilities,
		Enforcement:  enforcement,
	}
}

func allCapabilities() protocol.Capabilities {
	return protocol.Capabilities{
		RepoRead:          true,
		RepoWrite:         true,
		Shell:             true,
		Network:           true,
		ResumableSessions: true,
	}
}

func allEnforcement() protocol.Enforcement {
	return protocol.Enforcement{
		PathGrants:    true,
		ReadOnly:      true,
		NetworkGrants: true,
		ShellGrants:   true,
		ReadGrants:    true,
	}
}

func encode(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func objectAt(value any) map[string]any {
	return value.(map[string]any)
}
