package cast

import (
	"reflect"
	"slices"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

func TestEmptyLayersResolveToEmptyCast(t *testing.T) {
	t.Parallel()
	resolved, diagnostics := Resolve(nil)
	if resolved == nil || len(diagnostics) != 0 {
		t.Fatalf("Resolve(nil) = %#v, %#v", resolved, diagnostics)
	}
	if len(resolved.Performers()) != 0 || len(resolved.Bindings()) != 0 {
		t.Fatalf("empty cast views = %#v, %#v",
			resolved.Performers(), resolved.Bindings())
	}
	if diagnostics := resolved.ValidateScore(compileScore(t, "plan")); len(diagnostics) != 1 ||
		diagnostics[0] != (Diagnostic{
			Rule: RuleScore, Pointer: "/bindings/plan", Detail: "binding_missing",
		}) {
		t.Fatalf("empty cast score diagnostics = %#v", diagnostics)
	}
}

func TestLayerPrecedenceCombinations(t *testing.T) {
	t.Parallel()
	definitions := []struct {
		name   string
		origin string
		model  string
	}{
		{"project", "project", "project-model"},
		{"user", "user-global", "user-model"},
		{"factory", "factory", "factory-model"},
	}
	for mask := 1; mask < 1<<len(definitions); mask++ {
		mask := mask
		t.Run(layerMaskName(definitions, mask), func(t *testing.T) {
			var layers []Layer
			wantModel := ""
			wantAdapter := ""
			wantBinding := ""
			wantCount := 0
			for index, definition := range definitions {
				if mask&(1<<index) == 0 {
					continue
				}
				if wantModel == "" {
					wantModel = definition.model
					wantAdapter = definition.name + "-adapter"
					wantBinding = definition.name + "-performer"
				}
				wantCount++
				layers = append(layers, Layer{
					Origin: definition.origin,
					Data: encodeFixture(t, layerFixture(
						definition.name,
						definition.model,
					)),
				})
			}
			resolved := mustResolve(t, layers...)
			shared, ok := resolved.Performer("shared")
			if !ok || shared.Model != wantModel || shared.Adapter != wantAdapter {
				t.Fatalf("shared performer = %#v, want adapter/model %q/%q",
					shared, wantAdapter, wantModel)
			}
			if len(resolved.Performers()) != wantCount+1 {
				t.Fatalf("performers = %#v", resolved.Performers())
			}
			if len(resolved.Bindings()) != wantCount+1 {
				t.Fatalf("bindings = %#v", resolved.Bindings())
			}
			sharedBinding, ok := resolved.Binding("shared-part")
			if !ok || sharedBinding.Performer != wantBinding {
				t.Fatalf("shared binding = %#v, want performer %q",
					sharedBinding, wantBinding)
			}
			for index, definition := range definitions {
				if mask&(1<<index) == 0 {
					continue
				}
				performerID := definition.name + "-performer"
				performer, ok := resolved.Performer(performerID)
				if !ok ||
					performer.Adapter != definition.name+"-adapter" ||
					performer.Model != definition.name+"-model" {
					t.Fatalf("%s performer = %#v", definition.name, performer)
				}
				binding, ok := resolved.Binding(definition.name + "-part")
				if !ok || binding.Performer != performerID ||
					len(binding.Fallbacks) != 0 {
					t.Fatalf("%s binding = %#v", definition.name, binding)
				}
			}
		})
	}
}

func TestWholeObjectReplacementNeverDeepMerges(t *testing.T) {
	t.Parallel()
	project := map[string]any{
		"cast": "0.1",
		"performers": map[string]any{
			"shared": map[string]any{
				"adapter": "project-adapter",
			},
		},
	}
	user := map[string]any{
		"cast": "0.1",
		"performers": map[string]any{
			"shared": map[string]any{
				"adapter":                    "user-adapter",
				"model":                      "user-model",
				"allow_advisory_enforcement": true,
			},
		},
	}
	resolved, diagnostics := Resolve([]Layer{
		{Origin: "project", Data: encodeFixture(t, project)},
		{Origin: "user-global", Data: encodeFixture(t, user)},
	})
	if resolved != nil {
		t.Fatal("project performer inherited the lower model")
	}
	want := []Diagnostic{{
		Rule: RuleSchema, Origin: "project",
		Pointer: "/performers/shared/model", Detail: "required",
	}}
	if !slices.Equal(diagnostics, want) {
		t.Fatalf("diagnostics = %#v, want %#v", diagnostics, want)
	}
}

func TestWinningDefaultsDoNotInheritLowerFields(t *testing.T) {
	t.Parallel()
	project := map[string]any{
		"cast": "0.1",
		"performers": map[string]any{
			"shared": performerFixture("project-adapter", "project-model", nil),
			"backup": performerFixture("backup-adapter", "backup-model", nil),
		},
		"bindings": map[string]any{
			"plan": map[string]any{"performer": "shared"},
		},
	}
	user := map[string]any{
		"cast": "0.1",
		"performers": map[string]any{
			"shared": performerFixture("user-adapter", "user-model", boolPointer(true)),
			"backup": performerFixture("backup-adapter", "backup-model", nil),
		},
		"bindings": map[string]any{
			"plan": map[string]any{
				"performer": "shared",
				"fallbacks": []any{"backup"},
			},
		},
	}
	objectAtKey(user, "performers", "shared")["extensions"] =
		map[string]any{"user-adapter": map[string]any{"inherited": true}}
	resolved := mustResolve(t,
		Layer{Origin: "project", Data: encodeFixture(t, project)},
		Layer{Origin: "user-global", Data: encodeFixture(t, user)},
	)
	performer, _ := resolved.Performer("shared")
	if performer.AllowAdvisoryEnforcement {
		t.Fatal("winning performer inherited lower advisory opt-in")
	}
	if performer.Extensions != nil {
		t.Fatalf("winning performer inherited lower extensions: %#v",
			performer.Extensions)
	}
	binding, _ := resolved.Binding("plan")
	if len(binding.Fallbacks) != 0 {
		t.Fatalf("winning binding inherited lower fallbacks: %#v", binding)
	}
}

func TestCastDefaultsHaveEffectiveViewGoldens(t *testing.T) {
	t.Parallel()
	omitted := map[string]any{
		"cast": "0.1",
		"performers": map[string]any{
			"primary": performerFixture("adapter", "model", nil),
		},
		"bindings": map[string]any{
			"plan": map[string]any{"performer": "primary"},
		},
	}
	explicit := map[string]any{
		"cast": "0.1",
		"performers": map[string]any{
			"primary": performerFixture("adapter", "model", boolPointer(false)),
		},
		"bindings": map[string]any{
			"plan": map[string]any{
				"performer": "primary",
				"fallbacks": []any{},
			},
		},
	}
	omittedCast := mustResolve(t, Layer{
		Origin: "project", Data: encodeFixture(t, omitted),
	})
	explicitCast := mustResolve(t, Layer{
		Origin: "project", Data: encodeFixture(t, explicit),
	})
	wantPerformers := []PerformerView{{
		ID: "primary", Adapter: "adapter", Model: "model",
		AllowAdvisoryEnforcement: false,
		Extensions:               nil,
	}}
	wantBindings := []BindingView{{
		PartID: "plan", Performer: "primary", Fallbacks: []string{}, Strict: true,
	}}
	for name, resolved := range map[string]*Cast{
		"omitted":  omittedCast,
		"explicit": explicitCast,
	} {
		if got := resolved.Performers(); !reflect.DeepEqual(got, wantPerformers) {
			t.Fatalf("%s performers = %#v, want %#v",
				name, got, wantPerformers)
		}
		if got := resolved.Bindings(); !reflect.DeepEqual(got, wantBindings) {
			t.Fatalf("%s bindings = %#v, want %#v",
				name, got, wantBindings)
		}
	}
}

func TestFallbacksAreReplacedWholesaleAndOrderIsPreserved(t *testing.T) {
	t.Parallel()
	project := completeCastFixture()
	objectAtKey(project, "bindings", "plan")["fallbacks"] =
		[]any{"third", "backup"}
	user := completeCastFixture()
	objectAtKey(user, "bindings", "plan")["fallbacks"] =
		[]any{"backup", "third"}
	resolved := mustResolve(t,
		Layer{Origin: "project", Data: encodeFixture(t, project)},
		Layer{Origin: "user-global", Data: encodeFixture(t, user)},
	)
	binding, _ := resolved.Binding("plan")
	want := []string{"third", "backup"}
	if !slices.Equal(binding.Fallbacks, want) {
		t.Fatalf("fallbacks = %#v, want %#v", binding.Fallbacks, want)
	}
}

func TestCastSchemaConformance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(map[string]any)
		pointer string
		detail  string
	}{
		{"cast_required", func(root map[string]any) { delete(root, "cast") },
			"/cast", "required"},
		{"cast_version", func(root map[string]any) { root["cast"] = "0.2" },
			"/cast", "invalid_value"},
		{"cast_not_null", func(root map[string]any) { root["cast"] = nil },
			"/cast", "must_not_be_null"},
		{"cast_string", func(root map[string]any) { root["cast"] = true },
			"/cast", "expected_string"},
		{"root_unknown", func(root map[string]any) { root["unknown"] = true },
			"/unknown", "unknown_field"},
		{"performers_object", func(root map[string]any) { root["performers"] = []any{} },
			"/performers", "expected_object"},
		{"performers_not_null", func(root map[string]any) { root["performers"] = nil },
			"/performers", "must_not_be_null"},
		{"performer_object", func(root map[string]any) {
			objectAt(root["performers"])["primary"] = nil
		}, "/performers/primary", "must_not_be_null"},
		{"performer_expected_object", func(root map[string]any) {
			objectAt(root["performers"])["primary"] = "performer"
		}, "/performers/primary", "expected_object"},
		{"adapter_required", func(root map[string]any) {
			delete(objectAtKey(root, "performers", "primary"), "adapter")
		}, "/performers/primary/adapter", "required"},
		{"adapter_string", func(root map[string]any) {
			objectAtKey(root, "performers", "primary")["adapter"] = true
		}, "/performers/primary/adapter", "expected_string"},
		{"adapter_not_null", func(root map[string]any) {
			objectAtKey(root, "performers", "primary")["adapter"] = nil
		}, "/performers/primary/adapter", "must_not_be_null"},
		{"model_required", func(root map[string]any) {
			delete(objectAtKey(root, "performers", "primary"), "model")
		}, "/performers/primary/model", "required"},
		{"model_not_null", func(root map[string]any) {
			objectAtKey(root, "performers", "primary")["model"] = nil
		}, "/performers/primary/model", "must_not_be_null"},
		{"model_string", func(root map[string]any) {
			objectAtKey(root, "performers", "primary")["model"] = true
		}, "/performers/primary/model", "expected_string"},
		{"advisory_boolean", func(root map[string]any) {
			objectAtKey(root, "performers", "primary")["allow_advisory_enforcement"] = "true"
		}, "/performers/primary/allow_advisory_enforcement", "expected_boolean"},
		{"advisory_not_null", func(root map[string]any) {
			objectAtKey(root, "performers", "primary")["allow_advisory_enforcement"] = nil
		}, "/performers/primary/allow_advisory_enforcement", "must_not_be_null"},
		{"extensions_object", func(root map[string]any) {
			objectAtKey(root, "performers", "primary")["extensions"] = []any{}
		}, "/performers/primary/extensions", "expected_object"},
		{"extensions_not_null", func(root map[string]any) {
			objectAtKey(root, "performers", "primary")["extensions"] = nil
		}, "/performers/primary/extensions", "must_not_be_null"},
		{"performer_unknown", func(root map[string]any) {
			objectAtKey(root, "performers", "primary")["unknown"] = true
		}, "/performers/primary/unknown", "unknown_field"},
		{"bindings_object", func(root map[string]any) { root["bindings"] = []any{} },
			"/bindings", "expected_object"},
		{"bindings_not_null", func(root map[string]any) { root["bindings"] = nil },
			"/bindings", "must_not_be_null"},
		{"binding_object", func(root map[string]any) {
			objectAt(root["bindings"])["plan"] = nil
		}, "/bindings/plan", "must_not_be_null"},
		{"binding_expected_object", func(root map[string]any) {
			objectAt(root["bindings"])["plan"] = "binding"
		}, "/bindings/plan", "expected_object"},
		{"binding_performer_required", func(root map[string]any) {
			delete(objectAtKey(root, "bindings", "plan"), "performer")
		}, "/bindings/plan/performer", "required"},
		{"binding_performer_not_null", func(root map[string]any) {
			objectAtKey(root, "bindings", "plan")["performer"] = nil
		}, "/bindings/plan/performer", "must_not_be_null"},
		{"binding_performer_string", func(root map[string]any) {
			objectAtKey(root, "bindings", "plan")["performer"] = true
		}, "/bindings/plan/performer", "expected_string"},
		{"fallbacks_array", func(root map[string]any) {
			objectAtKey(root, "bindings", "plan")["fallbacks"] = "backup"
		}, "/bindings/plan/fallbacks", "expected_array"},
		{"fallbacks_not_null", func(root map[string]any) {
			objectAtKey(root, "bindings", "plan")["fallbacks"] = nil
		}, "/bindings/plan/fallbacks", "must_not_be_null"},
		{"fallback_string", func(root map[string]any) {
			objectAtKey(root, "bindings", "plan")["fallbacks"] = []any{true}
		}, "/bindings/plan/fallbacks/0", "expected_string"},
		{"fallback_not_null", func(root map[string]any) {
			objectAtKey(root, "bindings", "plan")["fallbacks"] = []any{nil}
		}, "/bindings/plan/fallbacks/0", "must_not_be_null"},
		{"binding_unknown", func(root map[string]any) {
			objectAtKey(root, "bindings", "plan")["unknown"] = true
		}, "/bindings/plan/unknown", "unknown_field"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			document := completeCastFixture()
			test.mutate(document)
			_, diagnostics := Resolve([]Layer{{
				Origin: "project",
				Data:   encodeFixture(t, document),
			}})
			want := Diagnostic{
				Rule: RuleSchema, Origin: "project",
				Pointer: test.pointer, Detail: test.detail,
			}
			if !slices.Contains(diagnostics, want) {
				t.Fatalf("missing diagnostic %#v\ngot: %#v", want, diagnostics)
			}
		})
	}
}

func TestCastRootSchema(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		value  any
		detail string
	}{
		{"null", nil, "must_not_be_null"},
		{"non_object", []any{}, "expected_object"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			_, diagnostics := Resolve([]Layer{{
				Origin: "project", Data: encodeFixture(t, test.value),
			}})
			want := []Diagnostic{{
				Rule: RuleSchema, Origin: "project", Detail: test.detail,
			}}
			if !slices.Equal(diagnostics, want) {
				t.Fatalf("diagnostics = %#v, want %#v", diagnostics, want)
			}
		})
	}
}

func TestStaticRulesAreDeletionVisible(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(map[string]any)
		pointer string
		detail  string
	}{
		{"primary_exists", func(root map[string]any) {
			objectAtKey(root, "bindings", "plan")["performer"] = "missing"
		}, "/bindings/plan/performer", "performer_missing"},
		{"fallback_exists", func(root map[string]any) {
			objectAtKey(root, "bindings", "plan")["fallbacks"] = []any{"missing"}
		}, "/bindings/plan/fallbacks/0", "performer_missing"},
		{"fallback_unique", func(root map[string]any) {
			objectAtKey(root, "bindings", "plan")["fallbacks"] =
				[]any{"backup", "backup"}
		}, "/bindings/plan/fallbacks/1", "duplicate_fallback"},
		{"fallback_excludes_primary", func(root map[string]any) {
			objectAtKey(root, "bindings", "plan")["fallbacks"] =
				[]any{"primary"}
		}, "/bindings/plan/fallbacks/0", "fallback_is_primary"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			document := completeCastFixture()
			test.mutate(document)
			_, diagnostics := Resolve([]Layer{{
				Origin: "project",
				Data:   encodeFixture(t, document),
			}})
			want := Diagnostic{
				Rule: RuleStatic, Origin: "project",
				Pointer: test.pointer, Detail: test.detail,
			}
			if !slices.Contains(diagnostics, want) {
				t.Fatalf("missing diagnostic %#v\ngot: %#v", want, diagnostics)
			}
		})
	}
}

func TestStaticDiagnosticsAggregateExactlyAndSort(t *testing.T) {
	t.Parallel()
	document := completeCastFixture()
	objectAtKey(document, "bindings", "plan")["performer"] = "missing-primary"
	objectAtKey(document, "bindings", "plan")["fallbacks"] =
		[]any{"missing-fallback", "missing-fallback", "missing-primary"}
	_, diagnostics := Resolve([]Layer{{
		Origin: "project",
		Data:   encodeFixture(t, document),
	}})
	want := []Diagnostic{
		{RuleStatic, "project", "/bindings/plan/fallbacks/0", "performer_missing"},
		{RuleStatic, "project", "/bindings/plan/fallbacks/1", "duplicate_fallback"},
		{RuleStatic, "project", "/bindings/plan/fallbacks/1", "performer_missing"},
		{RuleStatic, "project", "/bindings/plan/fallbacks/2", "fallback_is_primary"},
		{RuleStatic, "project", "/bindings/plan/fallbacks/2", "performer_missing"},
		{RuleStatic, "project", "/bindings/plan/performer", "performer_missing"},
	}
	if !slices.Equal(diagnostics, want) {
		t.Fatalf("diagnostics differ\n got: %#v\nwant: %#v", diagnostics, want)
	}
}

func TestLayerDiagnosticsAggregateExactly(t *testing.T) {
	t.Parallel()
	user := completeCastFixture()
	delete(objectAtKey(user, "performers", "primary"), "model")
	objectAtKey(user, "bindings", "plan")["fallbacks"] =
		[]any{"missing-fallback"}
	factory := completeCastFixture()
	objectAtKey(factory, "bindings", "plan")["fallbacks"] = "backup"
	resolved, diagnostics := Resolve([]Layer{
		{Origin: "project", Data: []byte("cast: [\n")},
		{Origin: "user-global", Data: encodeFixture(t, user)},
		{Origin: "factory", Data: encodeFixture(t, factory)},
	})
	if resolved != nil {
		t.Fatal("invalid layers produced a cast")
	}
	want := []Diagnostic{
		{RuleIngress, "project", "", "invalid_restricted_yaml"},
		{RuleSchema, "factory", "/bindings/plan/fallbacks", "expected_array"},
		{RuleSchema, "user-global", "/performers/primary/model", "required"},
	}
	if !slices.Equal(diagnostics, want) {
		t.Fatalf("diagnostics differ\n got: %#v\nwant: %#v", diagnostics, want)
	}
}

func TestUsableSchemaGraphStillAggregatesIndependentStaticRules(t *testing.T) {
	t.Parallel()
	document := completeCastFixture()
	objectAtKey(document, "performers", "primary")["unknown"] = true
	objectAtKey(document, "bindings", "plan")["fallbacks"] =
		[]any{"missing-fallback"}
	_, diagnostics := Resolve([]Layer{{
		Origin: "project", Data: encodeFixture(t, document),
	}})
	want := []Diagnostic{
		{RuleSchema, "project", "/performers/primary/unknown", "unknown_field"},
		{RuleStatic, "project", "/bindings/plan/fallbacks/0", "performer_missing"},
	}
	if !slices.Equal(diagnostics, want) {
		t.Fatalf("diagnostics differ\n got: %#v\nwant: %#v", diagnostics, want)
	}
}

func TestInvalidSchemaOperandsDoNotCreateDerivativeStaticDiagnostics(t *testing.T) {
	t.Parallel()
	t.Run("known_malformed_performer", func(t *testing.T) {
		document := completeCastFixture()
		objectAt(document["performers"])["primary"] = nil
		_, diagnostics := Resolve([]Layer{{
			Origin: "project", Data: encodeFixture(t, document),
		}})
		want := []Diagnostic{{
			Rule: RuleSchema, Origin: "project",
			Pointer: "/performers/primary", Detail: "must_not_be_null",
		}}
		if !slices.Equal(diagnostics, want) {
			t.Fatalf("diagnostics = %#v, want %#v", diagnostics, want)
		}
	})
	t.Run("invalid_fallback_element", func(t *testing.T) {
		document := completeCastFixture()
		objectAtKey(document, "bindings", "plan")["fallbacks"] =
			[]any{nil, "backup"}
		_, diagnostics := Resolve([]Layer{{
			Origin: "project", Data: encodeFixture(t, document),
		}})
		want := []Diagnostic{{
			Rule: RuleSchema, Origin: "project",
			Pointer: "/bindings/plan/fallbacks/0", Detail: "must_not_be_null",
		}}
		if !slices.Equal(diagnostics, want) {
			t.Fatalf("diagnostics = %#v, want %#v", diagnostics, want)
		}
	})
	t.Run("unknown_performer_namespace", func(t *testing.T) {
		document := completeCastFixture()
		document["performers"] = []any{}
		objectAtKey(document, "bindings", "plan")["fallbacks"] =
			[]any{"primary", "primary"}
		_, diagnostics := Resolve([]Layer{{
			Origin: "project", Data: encodeFixture(t, document),
		}})
		want := []Diagnostic{
			{RuleSchema, "project", "/performers", "expected_object"},
			{RuleStatic, "project", "/bindings/plan/fallbacks/0", "fallback_is_primary"},
			{RuleStatic, "project", "/bindings/plan/fallbacks/1", "duplicate_fallback"},
			{RuleStatic, "project", "/bindings/plan/fallbacks/1", "fallback_is_primary"},
		}
		if !slices.Equal(diagnostics, want) {
			t.Fatalf("diagnostics differ\n got: %#v\nwant: %#v", diagnostics, want)
		}
	})
}

func TestMissingBindingsAggregateExactly(t *testing.T) {
	t.Parallel()
	resolved := mustResolve(t)
	diagnostics := resolved.ValidateScore(compileScore(t, "verify", "plan"))
	want := []Diagnostic{
		{Rule: RuleScore, Pointer: "/bindings/plan", Detail: "binding_missing"},
		{Rule: RuleScore, Pointer: "/bindings/verify", Detail: "binding_missing"},
	}
	if !slices.Equal(diagnostics, want) {
		t.Fatalf("diagnostics differ\n got: %#v\nwant: %#v", diagnostics, want)
	}
}

func TestExtraPerformersAndBindingsAreAccepted(t *testing.T) {
	t.Parallel()
	document := completeCastFixture()
	objectAt(document["bindings"])["unused"] =
		map[string]any{"performer": "primary"}
	resolved := mustResolve(t, Layer{
		Origin: "project",
		Data:   encodeFixture(t, document),
	})
	if diagnostics := resolved.ValidateScore(compileScore(t, "plan")); len(diagnostics) != 0 {
		t.Fatalf("extra declarations were rejected: %#v", diagnostics)
	}
}

func TestIngressFailureCarriesOriginAndDoesNotBecomeAbsentLayer(t *testing.T) {
	t.Parallel()
	resolved, diagnostics := Resolve([]Layer{
		{Origin: "project", Data: nil},
		{Origin: "factory", Data: encodeFixture(t, completeCastFixture())},
	})
	if resolved != nil {
		t.Fatal("invalid explicit layer was treated as absent")
	}
	want := []Diagnostic{{
		Rule: RuleIngress, Origin: "project", Detail: "invalid_restricted_yaml",
	}}
	if !slices.Equal(diagnostics, want) {
		t.Fatalf("diagnostics = %#v, want %#v", diagnostics, want)
	}
}

func TestOpaqueExtensionsAndViewsAreDefensive(t *testing.T) {
	t.Parallel()
	document := completeCastFixture()
	objectAtKey(document, "performers", "primary")["extensions"] =
		map[string]any{
			"adapter": map[string]any{"fraction": 1.5, "nullable": nil},
			"array":   []any{"opaque", map[string]any{"nested": true}},
			"null":    nil,
			"scalar":  "opaque",
		}
	resolved := mustResolve(t, Layer{
		Origin: "project",
		Data:   encodeFixture(t, document),
	})
	performer, _ := resolved.Performer("primary")
	objectAt(objectAt(performer.Extensions)["adapter"])["fraction"] = 2.5
	array := performer.Extensions["array"].([]any)
	array[0] = "changed"
	objectAt(array[1])["nested"] = false
	bindings := resolved.Bindings()
	bindings[0].Fallbacks[0] = "changed"
	freshPerformer, _ := resolved.Performer("primary")
	wantExtensions := map[string]any{
		"adapter": map[string]any{"fraction": 1.5, "nullable": nil},
		"array":   []any{"opaque", map[string]any{"nested": true}},
		"null":    nil,
		"scalar":  "opaque",
	}
	if !reflect.DeepEqual(freshPerformer.Extensions, wantExtensions) {
		t.Fatalf("mutating a performer view changed the cast: %#v",
			freshPerformer.Extensions)
	}
	freshBinding, _ := resolved.Binding("plan")
	if !slices.Equal(freshBinding.Fallbacks, []string{"backup"}) {
		t.Fatalf("mutating a binding view changed the cast: %#v",
			freshBinding.Fallbacks)
	}
}

func TestDiagnosticPointersEscapeDynamicKeys(t *testing.T) {
	t.Parallel()
	document := completeCastFixture()
	objectAt(document["bindings"])["bad/key~"] = map[string]any{
		"performer": "missing",
	}
	_, diagnostics := Resolve([]Layer{{
		Origin: "project",
		Data:   encodeFixture(t, document),
	}})
	want := Diagnostic{
		Rule: RuleStatic, Origin: "project",
		Pointer: "/bindings/bad~1key~0/performer", Detail: "performer_missing",
	}
	if !slices.Contains(diagnostics, want) {
		t.Fatalf("missing escaped diagnostic %#v\ngot: %#v", want, diagnostics)
	}
}

func layerFixture(name, model string) map[string]any {
	uniquePerformer := name + "-performer"
	uniquePart := name + "-part"
	return map[string]any{
		"cast": "0.1",
		"performers": map[string]any{
			"shared": performerFixture(name+"-adapter", model, nil),
			uniquePerformer: performerFixture(
				name+"-adapter", name+"-model", nil),
		},
		"bindings": map[string]any{
			"shared-part": map[string]any{"performer": uniquePerformer},
			uniquePart:    map[string]any{"performer": uniquePerformer},
		},
	}
}

func completeCastFixture() map[string]any {
	return map[string]any{
		"cast": "0.1",
		"performers": map[string]any{
			"primary": performerFixture("adapter", "primary-model", nil),
			"backup":  performerFixture("backup-adapter", "backup-model", nil),
			"third":   performerFixture("other", "third-model", nil),
		},
		"bindings": map[string]any{
			"plan": map[string]any{
				"performer": "primary",
				"fallbacks": []any{"backup"},
			},
		},
	}
}

func performerFixture(adapter, model string, advisory *bool) map[string]any {
	result := map[string]any{"adapter": adapter, "model": model}
	if advisory != nil {
		result["allow_advisory_enforcement"] = *advisory
	}
	return result
}

func boolPointer(value bool) *bool {
	return &value
}

func encodeFixture(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := canonical.Encode(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func mustResolve(t *testing.T, layers ...Layer) *Cast {
	t.Helper()
	resolved, diagnostics := Resolve(layers)
	if resolved == nil || len(diagnostics) != 0 {
		t.Fatalf("Resolve() = %#v, %#v", resolved, diagnostics)
	}
	return resolved
}

func objectAt(value any) map[string]any {
	return value.(map[string]any)
}

func objectAtKey(root map[string]any, parent, key string) map[string]any {
	return objectAt(objectAt(root[parent])[key])
}

func layerMaskName(definitions []struct {
	name   string
	origin string
	model  string
}, mask int) string {
	var result string
	for index, definition := range definitions {
		if mask&(1<<index) == 0 {
			continue
		}
		if result != "" {
			result += "+"
		}
		result += definition.name
	}
	return result
}

func compileScore(t *testing.T, parts ...string) *score.Score {
	t.Helper()
	partValues := make(map[string]any, len(parts))
	for _, id := range parts {
		partValues[id] = map[string]any{
			"capabilities": []any{"repo_read"},
		}
	}
	document := map[string]any{
		"score":    "0.2",
		"name":     "cast-test",
		"revision": float64(1),
		"status":   "draft",
		"goal":     "Exercise cast validation.",
		"draft": map[string]any{
			"interview_movement": "clarify",
		},
		"parts": partValues,
		"movements": []any{
			map[string]any{
				"id":          "clarify",
				"phase":       "draft",
				"part":        parts[0],
				"grants":      []any{"repo_read"},
				"instruction": "Clarify.",
			},
		},
		"policy": map[string]any{
			"budget": map[string]any{
				"active_wall_clock_min": float64(10),
			},
		},
	}
	compiled, diagnostics := score.Compile(encodeFixture(t, document))
	if compiled == nil || len(diagnostics) != 0 {
		t.Fatalf("score.Compile = %#v, %#v", compiled, diagnostics)
	}
	return compiled
}
