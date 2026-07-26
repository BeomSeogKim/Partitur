# Cast resolver conformance

This table is executable evidence for DESIGN §1 layering, §2 cast defaults and static
rules, §3 capability/binding semantics, and §4's fail-closed predicate. Rejecting
fixtures assert the exact structured
diagnostic or predicate result named below. Deleting the named check therefore makes the
named test fail rather than merely leaving a generic "resolution failed" assertion green.

## Layering, schema, defaults, and static rules

| Rule / clause | Accepting fixture | Rejecting fixture | Deletion-mutation evidence |
|---|---|---|---|
| L0 — no supplied layers resolves a valid empty cast | `TestEmptyLayersResolveToEmptyCast` | No source rejection: missing bindings become score-relative only when a score is supplied | Treating absence as an invalid layer loses the empty-view oracle; omitting score-relative validation loses the named `RuleScore` result. |
| L1 — precedence is project → user-global → factory | `TestLayerPrecedenceCombinations` (all seven present/absent combinations) | Lower-precedence value is the counter-oracle | Delete first-wins selection or reverse layer order → shared performer adapter/model and shared-binding performer assertions fail. |
| L2 — performer entries replace whole objects | `TestWinningDefaultsDoNotInheritLowerFields` | `TestWholeObjectReplacementNeverDeepMerges` | Add field inheritance/deep merge → project entry silently acquires the lower model and loses the exact `RuleSchema` diagnostic. |
| L3 — binding entries replace whole objects | `TestWinningDefaultsDoNotInheritLowerFields` | Lower fallback list is the counter-oracle | Add deep merge → omitted project fallbacks inherit the lower list instead of resolving to `[]`. |
| L4 — fallbacks replace wholesale, never concatenate | `TestFallbacksAreReplacedWholesaleAndOrderIsPreserved` | Concatenated list is the counter-oracle | Concatenation changes the exact effective fallback view. |
| L5 — removal/tombstoning unsupported | Every mapping entry in `completeCastFixture` | `TestCastSchemaConformance/performer_object` and `/binding_object` | Treating `null` as removal loses the targeted `RuleSchema must_not_be_null` diagnostic. |
| S1 — every explicit layer is restricted YAML | All encoded fixtures | `TestIngressFailureCarriesOriginAndDoesNotBecomeAbsentLayer` | Treating zero bytes as an absent layer loses the exact origin-tagged `RuleIngress` diagnostic. |
| S2 — layer root is a non-null object | Every map fixture | `TestCastRootSchema/null`, `/non_object` | Deleting root null/type checks loses the exact `RuleSchema` result. |
| S3a — cast version is required | `completeCastFixture` | `TestCastSchemaConformance/cast_required` | Delete required handling → named test loses `RuleSchema required`. |
| S3b — cast version is a non-null string | `completeCastFixture` | `TestCastSchemaConformance/cast_not_null`, `/cast_string` | Delete null/type handling → a named test loses its exact diagnostic. |
| S3c — cast version is exactly `0.1` | `completeCastFixture` | `TestCastSchemaConformance/cast_version` | Delete equality check → named test loses `RuleSchema invalid_value`. |
| S4a — performers is a non-null mapping of objects | `completeCastFixture` | `TestCastSchemaConformance/performers_object`, `/performers_not_null`, `/performer_object`, `/performer_expected_object` | Delete any container/entry check → its path-specific exact diagnostic disappears. |
| S4b — performer adapter is a required non-null string | `completeCastFixture` | `TestCastSchemaConformance/adapter_required`, `/adapter_string`, `/adapter_not_null` | Delete required/type/null handling → a named exact diagnostic disappears. |
| S4c — performer model is a required non-null string | `completeCastFixture` | `TestCastSchemaConformance/model_required`, `/model_string`, `/model_not_null` | Delete required/type/null handling → a named exact diagnostic disappears. |
| S4d — advisory flag defaults false and is a non-null boolean | `TestCastDefaultsHaveEffectiveViewGoldens` | `TestCastSchemaConformance/advisory_boolean`, `/advisory_not_null` | Removing the default changes the effective-view golden; deleting type/null checks loses a named diagnostic. |
| S4e — performer extensions default absent, are a non-null mapping, and payloads remain opaque | `TestOpaqueExtensionsAndViewsAreDefensive` with object, array, scalar, fractional, and null payloads | `TestCastSchemaConformance/extensions_object`, `/extensions_not_null` | Removing defensive cloning or inventing a payload-shape rule breaks the accepting oracle; weakening field type/null checks loses `RuleSchema`. |
| S5a — bindings is a non-null mapping of objects | `completeCastFixture` | `TestCastSchemaConformance/bindings_object`, `/bindings_not_null`, `/binding_object`, `/binding_expected_object` | Delete any container/entry check → its path-specific exact diagnostic disappears. |
| S5b — binding performer is a required non-null string | `completeCastFixture` | `TestCastSchemaConformance/binding_performer_required`, `/binding_performer_string`, `/binding_performer_not_null` | Delete required/type/null handling → a named exact diagnostic disappears. |
| S5c — fallbacks default `[]` and the field is a non-null array | `TestCastDefaultsHaveEffectiveViewGoldens` | `TestCastSchemaConformance/fallbacks_array`, `/fallbacks_not_null` | Removing default materialization changes the effective-view golden; deleting array/null checks loses `RuleSchema`. |
| S5d — every fallback is a non-null string | `completeCastFixture` | `TestCastSchemaConformance/fallback_string`, `/fallback_not_null` | Delete element type/null handling → a named exact diagnostic disappears. |
| S6a — root core fields are strict | `completeCastFixture` | `TestCastSchemaConformance/root_unknown` | Delete the root allowlist → named test loses `RuleSchema unknown_field`. |
| S6b — performer core fields are strict | `completeCastFixture` | `TestCastSchemaConformance/performer_unknown` | Delete the performer allowlist → named test loses `RuleSchema unknown_field`. |
| S6c — binding core fields are strict | `completeCastFixture` | `TestCastSchemaConformance/binding_unknown` | Delete the binding allowlist → named test loses `RuleSchema unknown_field`. |
| C1 — bound primary performer exists | `completeCastFixture` | `TestStaticRulesAreDeletionVisible/primary_exists` | Delete primary lookup → named test loses `RuleStatic performer_missing`. |
| C2 — every fallback performer exists | `completeCastFixture` | `TestStaticRulesAreDeletionVisible/fallback_exists` | Delete fallback lookup → named test loses `RuleStatic performer_missing`. |
| C3 — fallback chain duplicate-free | `completeCastFixture` | `TestStaticRulesAreDeletionVisible/fallback_unique` | Delete duplicate tracking → named test loses `RuleStatic duplicate_fallback`. |
| C4 — fallback excludes its primary | `completeCastFixture` | `TestStaticRulesAreDeletionVisible/fallback_excludes_primary` | Delete equality check → named test loses `RuleStatic fallback_is_primary`. |
| C5 — every score part has a binding | Empty cast without a score in `TestEmptyLayersResolveToEmptyCast` | `TestMissingBindingsAggregateExactly` | Delete score-part traversal → exact two-diagnostic set becomes empty. |
| C6 — extra performers/bindings are permitted | `TestExtraPerformersAndBindingsAreAccepted` | No rejection: the specification states no reverse-existence rule | Adding an invented “unused declaration” rule breaks the accepting fixture. |
| C7a — configured part strictness includes the primary | All-strict `completeCastFixture` | Primary-advisory case in `TestPrimaryStrictFallbackAdvisoryMakesBindingNonStrict` | Checking fallbacks only makes `BindingView.Strict` incorrectly true. |
| C7b — configured part strictness includes every fallback | All-strict `completeCastFixture` | Fallback-advisory case in `TestPrimaryStrictFallbackAdvisoryMakesBindingNonStrict` | Checking only the primary makes `BindingView.Strict` incorrectly true. |

## Capability and enforcement predicates

| Rule / clause | Accepting fixture | Rejecting fixture | Deletion-mutation evidence |
|---|---|---|---|
| P1 — every primary/fallback supplies the part capabilities | Available halves of `TestCapabilityTruthTable` | `TestCapabilityTruthTable` unavailable halves, `TestCapabilitiesApplyToPrimaryAndEveryFallback`, and `TestSameAdapterPerformersAreAssessedIndependently` | Delete any capability mapping, skip either chain position, or deduplicate entries sharing an adapter → an exact assertion changes. |
| P2 — advisory does not waive capability compatibility | Satisfied capability in `TestCapabilityChecksAreNotAdvisoryAndDoNotCheckModels` | Same test's missing `network` capability | Applying advisory to capabilities incorrectly removes `network` from the exact result. |
| P3 — model availability is not a validate predicate | `TestCapabilityChecksAreNotAdvisoryAndDoNotCheckModels` | No rejection: v0.2 explicitly excludes model matching | Adding id/alias model matching creates a result absent from the accepting oracle. |
| P4 — failed/unobserved probes suppress only that performer's derivative results | Successful probe assessments throughout | Both missing-primary and missing-fallback halves of `TestMissingProbeSuppressesDerivativeAssessment` | Treating absence as a zero-value probe manufactures results; stopping at one missing probe suppresses an independently observed later chain entry. |
| E1 — withheld `repo_write` requires `read_only` | Satisfied half of `TestFailClosedTruthTable/repo_write_withheld_requires_read_only` | Unsatisfied half | Delete the row → exact refused `read_only` result becomes strict. |
| E2 — path-scoped `repo_write` requires `path_grants` | Satisfied half of `TestFailClosedTruthTable/repo_write_path_scoped_requires_path_grants` | Unsatisfied half | Delete the row → exact refused `path_grants` result becomes strict. |
| E3 — withheld `repo_read` requires `read_grants` | Satisfied half of `TestFailClosedTruthTable/repo_read_withheld_requires_read_grants` | Unsatisfied half | Delete the row → exact refused `read_grants` result becomes strict. |
| E4 — path-scoped `repo_read` requires `path_grants` | Satisfied half of `TestFailClosedTruthTable/repo_read_path_scoped_requires_path_grants` | Unsatisfied half | Delete the row → exact refused `path_grants` result becomes strict. |
| E5 — withheld `shell` requires `shell_grants` | Satisfied half of `TestFailClosedTruthTable/shell_withheld_requires_shell_grants` | Unsatisfied half | Delete the row → exact refused `shell_grants` result becomes strict. |
| E6 — withheld `network` requires `network_grants` | Satisfied half of `TestFailClosedTruthTable/network_withheld_requires_network_grants` | Unsatisfied half | Delete the row → exact refused `network_grants` result becomes strict. |
| E7 — exact `["**"]` is not path-scoped | Singleton whole-repository half of `TestWholeRepositoryExceptionInBothDirections` | Narrow, empty, and non-singleton halves | Broadening/narrowing the exception flips one side's exact strict/refused result. |
| E8 — path dimension is a set, not one entry per applicable row | Satisfied truth-table halves | `TestPathGrantsDimensionIsDeduplicated` | Removing deduplication returns two `path_grants` entries instead of the exact singleton set. |
| E9 — result is strict/refused/advisory plus exact sorted dimensions | Strict truth-table halves | `TestAdvisoryCarriesExactSortedDimensionSet` | Returning a boolean, losing advisory, omitting a dimension, or changing set ordering breaks the exact result. |
| E10 — predicate is evaluated per movement | Write movement in `TestOnePartTwoMovementsDifferPerMovement` | Read-only movement in the same test | Caching one result per part makes the two outcomes equal and fails the test. |
| E11 — primary and each fallback use their own advisory flag | All-strict chain | `TestPrimaryStrictFallbackAdvisoryMakesBindingNonStrict` and `TestSameAdapterPerformersAreAssessedIndependently` | Reusing the primary flag makes the fallback refused instead of advisory; deduplicating same-adapter performer entries drops one outcome. |

## Effective-view and cross-cutting properties

| Property | Evidence | Mutation caught |
|---|---|---|
| Effective defaults are materialized | `TestCastDefaultsHaveEffectiveViewGoldens` | Removing false/empty-list materialization changes the exact effective-view golden and omitted/explicit equivalence. |
| Fallback declaration order is semantic | `TestFallbacksAreReplacedWholesaleAndOrderIsPreserved` | Sorting or reversing fallbacks changes the asserted effective view. |
| Defensive effective views | `TestOpaqueExtensionsAndViewsAreDefensive` | Returning interior fallback/extension values lets one returned view mutate a fresh view. |
| Static diagnostic aggregation/order | `TestStaticDiagnosticsAggregateExactlyAndSort` | Short-circuiting or changing ordering breaks the exact six-diagnostic list. |
| Cross-layer diagnostic aggregation/order | `TestLayerDiagnosticsAggregateExactly` | Stopping after one bad layer or changing deterministic order breaks the exact list. |
| Score-relative diagnostic aggregation/order | `TestMissingBindingsAggregateExactly` | Stopping after one missing binding breaks the exact two-diagnostic list. |
| Schema/static completeness boundary | `TestUsableSchemaGraphStillAggregatesIndependentStaticRules`, `TestInvalidSchemaOperandsDoNotCreateDerivativeStaticDiagnostics`, and `TestLayerDiagnosticsAggregateExactly` | Short-circuiting usable graphs loses an independent static diagnostic; treating invalid operands as values invents derivatives; unusable ingress suppresses all static claims. |
| RFC 6901 dynamic-key escaping | `TestDiagnosticPointersEscapeDynamicKeys` | Omitting escaping changes the exact binding pointer. |
| Production import boundary | `TestProductionImportsAreExplicitlyAllowed` | Adding any production import outside the explicit allowlist fails. |
