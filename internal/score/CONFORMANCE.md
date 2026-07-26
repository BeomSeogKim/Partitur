# Score compiler conformance

This table is executable evidence, not a prose restatement of §2. `finalizedFixture` and
`draftFixture` are structurally valid accepting fixtures in `score_test.go`. Named rejecting
fixtures are executable subtests in `score_test.go` or `mutation_coverage_test.go`: each clones an
accepting fixture, applies one mutation, compiles the canonical JSON form (which is in the
restricted-YAML subset), and asserts the exact targeted `(RuleID, pointer, detail)`. Deleting the
named check therefore makes that subtest fail even when another rule also rejects the same source.

The part-id exception uses literal YAML because a map value cannot represent duplicate keys.

| Rule / clause | Accepting fixture | Rejecting fixture | Mutation evidence |
|---|---|---|---|
| 1a — finalized questions resolved or waived | `finalizedFixture` and `TestRule01WaivedQuestionIsAccepted` | `TestRuleConformance/1a_finalized_question_resolved` | Delete `finalized_question_unresolved` check → rejecting subtest loses `Rule01`; deleting the waiver arm rejects the accepting waiver fixture. |
| 1b — finalized intent present | `finalizedFixture` | `TestRuleConformance/1b_finalized_intent` | Delete `finalized_intent_missing` check → named subtest loses `Rule01`. |
| 1c — gate is require XOR waived | `finalizedFixture` | `TestRuleConformance/1c_apply_gate_xor` and `/1c_apply_gate_rejects_both_arms` | Weakening XOR to either “neither allowed” or “at least one” loses `Rule01` in one rejecting subtest. |
| 1d — finalized gate present | `finalizedFixture` | `TestRuleConformance/1d_apply_gate_required` | Delete `finalized_apply_gate_missing` check → named subtest loses `Rule01`. |
| 1e — waiver reason present | `finalizedFixture` | `TestRuleConformance/1e_waiver_reason` | Delete `waiver_reason_missing` check → named subtest loses `Rule01`. |
| 1f — waiver is literal true | `finalizedFixture` | `TestRuleConformance/1f_waiver_is_true` | Delete `waiver_must_be_true` check → named subtest loses `Rule01`. |
| 2a — write acceptance floor | `finalizedFixture` | `TestRuleConformance/2_write_acceptance_floor` | Delete `write_acceptance_missing` check → named subtest loses `Rule02`. |
| 2b — always gate satisfies floor | `TestRule02AlternativeSatisfiersAreAccepted/human_gate_always` | `TestRuleConformance/2_write_acceptance_floor` | Removing the `human_gate: always` alternative rejects the accepting subtest. |
| 2c — artifact criterion satisfies floor | `TestRule02AlternativeSatisfiersAreAccepted/artifact_criterion` | `TestRuleConformance/2_write_acceptance_floor` | Narrowing hard criteria to run-only rejects the accepting subtest. |
| 3a — grants subset capabilities | `finalizedFixture` | `TestRuleConformance/3a_grant_subset` | Delete `grant_not_capability` check → named subtest loses `Rule03`. |
| 3b — read-only part gets no write | `finalizedFixture` | `TestRuleConformance/3b_read_only_write` | Delete `read_only_repo_write` check → named subtest loses `Rule03`. |
| 4a — movement ids unique | `finalizedFixture` | `TestRuleConformance/4a_movement_ids_unique` | Delete `duplicate_movement_id` check → named subtest loses `Rule04`. |
| 4b — part ids unique | `draftFixture` | `TestRule04PartIDUniquenessIsCoveredByIngress` | **Covered by `RuleIngress` — duplicate mapping key.** Restricted-YAML ingress owns the rejection; there is deliberately no semantic guard to delete. |
| 4c — question/decision ids unique | `finalizedFixture` | `TestRuleConformance/4c_question_ids_unique` | Delete `duplicate_question_id` check → named subtest loses `Rule04`. |
| 4d — needs references exist | `finalizedFixture` | `TestRuleConformance/4d_needs_exist` | Delete `need_missing` check → named subtest loses `Rule04`. |
| 4e — needs is a DAG | `finalizedFixture` | `TestRuleConformance/4e_needs_dag` | Delete `needs_cycle` check → named subtest loses `Rule04`. |
| 4f — part references exist | `finalizedFixture` | `TestRuleConformance/4f_part_reference` | Delete `part_missing` check → named subtest loses `Rule04`. |
| 4g — input output is declared | `finalizedFixture` | `TestRuleConformance/4g_input_declared` | Delete `input_output_missing` check → named subtest loses `Rule04`. |
| 4h — input producer is transitively reachable | `TestRule04TransitiveInputIsAccepted` | `TestRuleConformance/4h_input_reachable` | Delete `input_not_reachable` check → rejecting subtest loses `Rule04`; narrowing reachability to direct parents rejects the accepting test. |
| 4i — logical output ids unique | `finalizedFixture` | `TestRuleConformance/4i_output_ids_unique` | Delete `duplicate_output_id` check → named subtest loses `Rule04`. |
| 4j — output id not reserved | `finalizedFixture` | `TestRuleConformance/4j_reserved_output_id` | Delete `reserved_output_id` check → named subtest loses `Rule04`; `Rule16` remains an intentional sibling diagnostic. |
| 5 — retired tombstone | N/A — no active compiler rule | N/A — score has no artifact path | N/A — containment is owned independently by adapter and core runtime ingest. |
| 6a — unknown core field | `finalizedFixture` | `TestRuleConformance/6a_unknown_core_field` and `TestRule06EveryCoreObjectIsStrict` | Every strict core object has a targeted unknown-field subtest; deleting any object-specific field allowlist call loses `Rule06`. |
| 6b — adapter data only under extensions | `finalizedFixture` plus opaque extension | `TestRuleConformance/6b_extensions_namespace` | Delete root strict-field check → named subtest loses `Rule06`; the accepting half proves nested opaque data and null remain legal. |
| 7a — review output belongs to movement | `finalizedFixture` | `TestRuleConformance/7a_review_output_same_movement` | Delete `review_findings_missing` check → named subtest loses `Rule07`. |
| 7b — review output is findings-kind | `finalizedFixture` | `TestRuleConformance/7b_review_output_kind` | Delete `review_findings_wrong_kind` check → named subtest loses `Rule07`. |
| 8a — at most one draft movement | `draftFixture` plus ordinary movement | `TestRuleConformance/8a_at_most_one_draft` | Delete `multiple_draft_movements` check → named subtest loses `Rule08`. |
| 8b — draft phase requires reference | `draftFixture` | `TestRuleConformance/8b_draft_phase_requires_reference` | Delete `draft_reference_missing` check → named subtest loses `Rule08`. |
| 8c — draft reference requires phase | `draftFixture` | `TestRuleConformance/8c_draft_reference_requires_phase` | Delete `draft_phase_missing` check → named subtest loses `Rule08`. |
| 8d — draft phase and reference agree | `draftFixture` plus ordinary movement | `TestRuleConformance/8d_draft_reference_matches` | Delete `draft_reference_mismatch` check → named subtest loses `Rule08`. |
| 8e — draft status has one draft movement | `finalizedFixture` | `TestRuleConformance/8e_draft_status_requires_draft` | Delete `draft_status_requires_movement` check → named subtest loses `Rule08`. |
| 8f — draft movement has no write | `draftFixture` | `TestRuleConformance/8f_draft_read_only` | Delete `draft_repo_write` check → named subtest loses `Rule08`. |
| 8g — finalized score retains draft movement | `TestRule08FinalizedScoreMayRetainDraftMovement` | N/A — acceptance-only property | Forbidding draft phase solely because status is finalized rejects the accepting test. |
| 9a — criterion id required | `finalizedFixture` | `TestRuleConformance/9a_criterion_id_required` and `TestRule09ReviewCriterionIDIsRequired` | Hard and review paths each lose `Rule09` if their missing-id handling is deleted. |
| 9b — criterion ids unique per movement | `finalizedFixture` | `TestRuleConformance/9b_criterion_ids_unique` | Delete `duplicate_criterion_id` check → named subtest loses `Rule09`. |
| 10 — allowed paths unique | `finalizedFixture` | `TestRuleConformance/10_allowed_paths_unique` | Delete `duplicate_allowed_path` check → named subtest loses `Rule10`. |
| 11a — verified achievable | `finalizedFixture` | `TestRuleConformance/11a_verified_achievable` | Delete `verified_unachievable` check → named subtest loses `Rule11`. |
| 11b — reviewed has a criterion | `finalizedFixture` | `TestRuleConformance/11b_reviewed_achievable` | Delete review-achievability check → named subtest loses `Rule11`. |
| 11c — reviewed criterion is typed | `finalizedFixture` | `TestRuleConformance/11c_review_must_be_typed` | Replace typed-review predicate with “any review” → named subtest loses `Rule11`. |
| 11d — predicates have typed review | `finalizedFixture` | `TestRuleConformance/11d_predicate_achievable` | Delete `predicate_unachievable` check → named subtest loses `Rule11`. |
| 11e — predicate review is typed | `finalizedFixture` | `TestRuleConformance/11e_predicate_review_must_be_typed` | Weakening predicate achievability to “any review” loses `Rule11` on the untyped-review fixture. |
| 11f — approved has always gate | `finalizedFixture` | `TestRuleConformance/11f_approved_achievable` | Delete `approved_unachievable` check → named subtest loses `Rule11`. |
| 12a — waived gate omits final | `TestWaivedFinalizedScoreIsAccepted` | `TestRuleConformance/12a_waived_omits_final` | Delete `waived_final_movement_present` check → rejecting subtest loses `Rule12`; unconditionally requiring final rejects the accepting waived score. |
| 12b — non-waived gate declares final | `finalizedFixture` | `TestRuleConformance/12b_final_declared` | Delete `final_movement_missing` check → named subtest loses `Rule12`. |
| 12c — final reference exists | `finalizedFixture` | `TestRuleConformance/12c_final_reference_exists` | Delete `final_movement_unknown` check → named subtest loses `Rule12`. |
| 12d — final has no write | `finalizedFixture` | `TestRuleConformance/12d_final_no_write` | Delete `final_movement_repo_write` check → named subtest loses `Rule12`. |
| 12e — final is a sink | `finalizedFixture` | `TestRuleConformance/12e_final_is_sink` | Delete `final_movement_has_downstream` check → named subtest loses `Rule12`. |
| 12f — all non-draft movements in closure | `finalizedFixture` | `TestRuleConformance/12f_final_closure` | Delete `outside_final_movement_closure` check → named subtest loses `Rule12`. The two prose formulations of closure are one predicate. |
| 13a — core number is safe integer | `finalizedFixture` | `TestRuleConformance/13a_schema_number_safe` | Delete the `canonical.ValidateSafeInteger` call → named subtest loses `Rule13`. |
| 13b — every bad numeric pointer collected | `finalizedFixture` | `TestRule13CollectsEverySchemaControlledNumber` | Delete any numeric-path decode/check → exact four-pointer assertion fails. |
| 13c — extension numbers opaque | `finalizedFixture` plus fractional extension | `TestRuleConformance/13c_extensions_are_opaque` | Applying Rule13 recursively under extensions breaks the accepting half; removing core check breaks the rejecting half. |
| 13d — safe-range boundary | `TestRule13SafeIntegerBoundaries` at `2^53−1` | `TestRule13SafeIntegerBoundaries` above `2^53−1` | Weakening validation to integrality-only accepts the rejecting half; narrowing the valid boundary rejects the accepting half. |
| 13e — unknown fields are not schema-controlled | `finalizedFixture` | `TestRule13DoesNotOwnUnknownCoreFields` | Walking arbitrary unknown values adds a forbidden `Rule13`; the exact result must remain the owning `Rule06` only. |
| 14a — draft has no ordinary artifact output | `draftFixture` | `TestRuleConformance/14a_draft_no_artifact_output` | Delete `draft_artifact_output` check → named subtest loses `Rule14`. |
| 14b — draft has no change set | `draftFixture` | `TestRuleConformance/14b_draft_no_change_set` | Delete `draft_change_set_output` check → named subtest loses `Rule14`; `Rule15` remains an intentional sibling diagnostic. |
| 15a — write has at least one change set | `finalizedFixture` | `TestRuleConformance/15a_write_has_one_change_set` | Replace exact-count check with no check → named subtest loses `Rule15`. |
| 15b — write has at most one change set | `finalizedFixture` | `TestRuleConformance/15b_write_has_at_most_one_change_set` | Weaken exact-count check to “at least one” → named subtest loses `Rule15`. |
| 15c — non-write has no change set | `finalizedFixture` | `TestRuleConformance/15c_nonwrite_has_no_change_set` | Delete `nonwrite_change_set` check → named subtest loses `Rule15`. |
| 16 — identifier grammar at every declaration/reference path | `finalizedFixture`, `draftFixture`, and `TestRule16GrammarBoundaries` at 128 characters | `TestRule16EveryIdentifierPath`, `TestRule16GrammarBoundaries`, and `TestRuleConformance/16_identifier_grammar` | Every call site plus empty, leading punctuation, and 128/129-length boundaries is mutation-visible. |
| 17 — one artifact criterion per output | `TestRule02AlternativeSatisfiersAreAccepted/artifact_criterion` | `TestRuleConformance/17_one_artifact_criterion` | Delete `duplicate_artifact_criterion` check → rejecting subtest loses `Rule17`; rejecting all artifact criteria breaks the accepting test. |
| 18a — no local artifact criterion for change set | `finalizedFixture` | `TestRuleConformance/18_no_change_set_artifact_criterion` | Delete `change_set_artifact_criterion` check → named subtest loses `Rule18`. |
| 18b — change-set reference is global | `finalizedFixture` | `TestRuleConformance/18_change_set_reference_is_global` | Restrict the check to same-movement outputs → named subtest loses `Rule18`. |
| 19a — effective default is hash-visible | `TestDefaultsGoldenDraft` and `TestDefaultMayProposeFalseOutsideDraft` | N/A — default/hash property, not a rejection | Delete either defaults pass → static golden/equivalence assertion fails. |
| 19b — draft explicit false rejected | `draftFixture` | `TestRuleConformance/19b_draft_explicit_false` | Delete `draft_may_propose_false` check → named subtest loses `Rule19`. |
| 19c — true permitted on ordinary movement | `TestRule19MayProposeIsPermittedOnOrdinaryMovement` | N/A — acceptance-only property | Rejecting or overriding ordinary explicit true breaks its effective-view and projection assertions. |

## Defaults and cross-rule properties

| Property | Evidence | Mutation caught |
|---|---|---|
| Defaults phase boundary | `TestProjectionRequiresDefaultsPass` | Removing the defaults pass makes projection and hashing fail closed; deleting its guard lets an unmaterialized score project. |
| Material defaults and draft implicit authority | `TestDefaultsGoldenDraft` | Changing any projected effective default, or changing draft effective `may_propose`, changes the static JCS golden. |
| Apply-gate predicates default | `TestApplyGatePredicatesDefault` | Omitting the predicates defaults pass breaks equivalence and removes the asserted explicit empty set. |
| Ordinary `may_propose: false` default | `TestDefaultMayProposeFalseOutsideDraft` | Omitting the ordinary defaults pass changes bytes or removes three explicit false values. |
| Absent defaults and null rejection | `TestAbsentDefaultsOmitAndNullRejects` | Encoding an absent field or accepting explicit null fails a named subtest. |
| Material defaults reject null | `TestMaterialDefaultFieldsRejectNull` | Treating explicit null as omission loses a targeted `RuleSchema` diagnostic. |
| Apply-gate structural constraints | `TestApplyGateSchemaConformance` | Removing non-empty, duplicate-free, or closed-enum checks loses a targeted `RuleSchema` diagnostic. |
| Defaults-table ranges and constraints | `TestNormativeTableSchemaConstraints` | Covers version/status enums, numeric minima, non-empty/duplicate-free capabilities, phase/output/human-gate/hash shapes, empty side effects, and amendment enum. |
| Semantic set/order split | `TestProjectionOrderingSemantics` | Covers capabilities, grants, allowed paths, require, predicates, rubric, questions, needs, and inputs as sets; movements, outputs, hard criteria, and review criteria as declaration-ordered sequences. |
| Canonical string ordering, including non-BMP values | `TestProjectionDelegatesNonBMPKeyOrderingToCanonical` | Replacing canonical ordering with Go string ordering reverses both extension object keys and `allowed_paths` set elements. |
| Score-domain hash | `TestHashUsesScoreDomainProjection` | Bypassing `canonical.Hash(canonical.DomainScore, value)` changes the independently computed hash. |
| Defensive read access | `TestReadViewsAreDefensive` | Returning interior slices changes the projection after the test mutates a view. |
| Semantic aggregation and deterministic ordering | `TestSemanticDiagnosticsAreCompleteAndOrdered` | Removing any included guard, short-circuiting, or changing sort order breaks the exact diagnostic list. |
| Source-pointer stability after malformed elements | `TestMalformedElementsDoNotShiftPointers` | Compacting invalid array elements moves a later targeted semantic diagnostic to the wrong RFC 6901 pointer. |
| No false semantic absence from partial decode | `TestMalformedDeclarationsDoNotCreateFalseAbsence` | Treating malformed capability, movement-id/phase, output-id/kind, grant, or criterion-id operands as known values creates a forbidden derivative diagnostic; over-suppressing independent read-only, draft-mismatch, output-absence, and known-positive change-set checks also fails. |
| Dynamic pointer escaping | `TestDiagnosticPointersEscapeDynamicKeys` | Omitting RFC 6901 escaping changes the asserted part-key pointer. |
| Ingress phase boundary | `TestIngressEndsBeforeSemanticCompleteness` | Continuing after an unusable graph or returning semantic diagnostics breaks the exact ingress-only list. |
| Numeric ingress hazards | `TestScoreIngressRejectsCanonicalNumericHazards` | Accepting negative zero, underflow, overflow, NaN, or infinity breaks an ingress-only subtest. |
| Production import boundary | `TestProductionImportsAreExplicitlyAllowed` | Adding any production import outside the exact allowlist fails. |
