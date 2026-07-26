package score

import (
	"regexp"
	"strings"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func validateRules(document *document, diagnostics *[]Diagnostic) {
	validateRule01(document, diagnostics)
	validateRule02(document, diagnostics)
	validateRule03(document, diagnostics)
	validateRule04(document, diagnostics)
	validateRule07(document, diagnostics)
	validateRule08(document, diagnostics)
	validateRule09(document, diagnostics)
	validateRule10(document, diagnostics)
	validateRule11(document, diagnostics)
	validateRule12(document, diagnostics)
	validateRule14(document, diagnostics)
	validateRule15(document, diagnostics)
	validateRule16(document, diagnostics)
	validateRule17(document, diagnostics)
	validateRule18(document, diagnostics)
}

func validateRule01(document *document, diagnostics *[]Diagnostic) {
	if document.Status != "finalized" || schemaInvalid(document, "/status") {
		return
	}
	for _, question := range document.OpenQuestions {
		base := indexPointer("/open_questions", question.SourceIndex)
		if schemaInvalid(document, base+"/resolution") ||
			schemaInvalid(document, base+"/waived") {
			continue
		}
		if question.Resolution == nil && (question.Waived == nil || !*question.Waived) {
			addDiagnostic(diagnostics, Rule01, base,
				"finalized_question_unresolved")
		}
	}
	if document.Verification == nil ||
		document.Verification.Expectation == nil ||
		document.Verification.Expectation.Intent == nil {
		if !schemaInvalid(document, "/verification") &&
			!schemaInvalid(document, "/verification/expectation") &&
			!schemaInvalid(document, "/verification/expectation/intent") {
			addDiagnostic(diagnostics, Rule01, "/verification/expectation/intent",
				"finalized_intent_missing")
		}
	}
	if document.Verification == nil || document.Verification.Expectation == nil ||
		document.Verification.Expectation.ApplyGate == nil {
		if !schemaInvalid(document, "/verification") &&
			!schemaInvalid(document, "/verification/expectation") &&
			!schemaInvalid(document, "/verification/expectation/apply_gate") {
			addDiagnostic(diagnostics, Rule01, "/verification/expectation/apply_gate",
				"finalized_apply_gate_missing")
		}
		return
	}
	gate := document.Verification.Expectation.ApplyGate
	if schemaInvalid(document, "/verification/expectation/apply_gate/require") ||
		schemaInvalid(document, "/verification/expectation/apply_gate/waived") {
		return
	}
	hasWaiver := gate.Waived != nil && *gate.Waived
	if gate.RequireSet == hasWaiver {
		addDiagnostic(diagnostics, Rule01, "/verification/expectation/apply_gate",
			"apply_gate_not_xor")
	}
	if gate.Waived != nil && !*gate.Waived {
		addDiagnostic(diagnostics, Rule01, "/verification/expectation/apply_gate/waived",
			"waiver_must_be_true")
	}
	if hasWaiver && !schemaInvalid(document, "/verification/expectation/apply_gate/reason") &&
		(gate.Reason == nil || strings.TrimSpace(*gate.Reason) == "") {
		addDiagnostic(diagnostics, Rule01, "/verification/expectation/apply_gate/reason",
			"waiver_reason_missing")
	}
}

func validateRule02(document *document, diagnostics *[]Diagnostic) {
	for _, movement := range document.Movements {
		base := movementPointer(movement)
		if schemaInvalidBelow(document, base+"/acceptance/hard") ||
			schemaInvalid(document, base+"/acceptance/human_gate") {
			continue
		}
		if containsValid(document, movement.Grants, base+"/grants", "repo_write") &&
			len(movement.Acceptance.Hard) == 0 &&
			movement.Acceptance.HumanGate != "always" {
			addDiagnostic(diagnostics, Rule02, movementPointer(movement)+"/acceptance",
				"write_acceptance_missing")
		}
	}
}

func validateRule03(document *document, diagnostics *[]Diagnostic) {
	for _, movement := range document.Movements {
		base := movementPointer(movement)
		if schemaInvalid(document, base+"/part") {
			continue
		}
		part, exists := document.Parts[movement.PartID]
		if !exists || !part.Valid {
			continue
		}
		capabilitiesPointer := pointerJoin("/parts", movement.PartID) + "/capabilities"
		capabilitiesValid := !schemaInvalidBelow(document, capabilitiesPointer)
		readOnlyValid := !schemaInvalid(
			document, pointerJoin("/parts", movement.PartID)+"/read_only")
		for grantIndex, grant := range movement.Grants {
			pointer := indexPointer(
				base+"/grants",
				grantIndex,
			)
			if schemaInvalid(document, pointer) {
				continue
			}
			if capabilitiesValid && !contains(part.Capabilities, grant) {
				addDiagnostic(diagnostics, Rule03, pointer, "grant_not_capability")
			}
			if readOnlyValid && part.ReadOnly && grant == "repo_write" {
				addDiagnostic(diagnostics, Rule03, pointer, "read_only_repo_write")
			}
		}
	}
}

func validateRule04(document *document, diagnostics *[]Diagnostic) {
	movementIndex := make(map[string]int, len(document.Movements))
	for index, movement := range document.Movements {
		if schemaInvalid(document, movementPointer(movement)+"/id") {
			continue
		}
		if _, duplicate := movementIndex[movement.ID]; duplicate {
			addDiagnostic(diagnostics, Rule04, movementPointer(movement)+"/id",
				"duplicate_movement_id")
		} else {
			movementIndex[movement.ID] = index
		}
	}

	questionIDs := make(map[string]struct{}, len(document.OpenQuestions))
	for _, question := range document.OpenQuestions {
		pointer := indexPointer("/open_questions", question.SourceIndex) + "/id"
		if schemaInvalid(document, pointer) {
			continue
		}
		if _, duplicate := questionIDs[question.ID]; duplicate {
			addDiagnostic(diagnostics, Rule04, pointer,
				"duplicate_question_id")
		}
		questionIDs[question.ID] = struct{}{}
	}

	outputProducer := make(map[string]int)
	for movementIndex, movement := range document.Movements {
		base := movementPointer(movement)
		if _, exists := document.Parts[movement.PartID]; !exists &&
			!schemaInvalid(document, base+"/part") {
			if document.PartsComplete {
				addDiagnostic(diagnostics, Rule04, movementPointer(movement)+"/part",
					"part_missing")
			}
		}
		for needIndex, need := range movement.Needs {
			pointer := indexPointer(
				movementPointer(movement)+"/needs",
				needIndex,
			)
			if schemaInvalid(document, pointer) ||
				schemaInvalid(document, base+"/id") {
				continue
			}
			if _, exists := movementByID(document, need); !exists {
				if document.MovementsComplete && document.MovementIDsComplete {
					addDiagnostic(diagnostics, Rule04, pointer, "need_missing")
				}
			} else if reachesMovement(document, need, movement.ID, nil) {
				addDiagnostic(diagnostics, Rule04, pointer, "needs_cycle")
			}
		}
		for _, output := range movement.Outputs {
			pointer := indexPointer(
				base+"/outputs",
				output.SourceIndex,
			) + "/id"
			if schemaInvalid(document, pointer) {
				continue
			}
			if _, duplicate := outputProducer[output.ID]; duplicate {
				addDiagnostic(diagnostics, Rule04, pointer, "duplicate_output_id")
			} else {
				outputProducer[output.ID] = movementIndex
			}
			if strings.HasPrefix(output.ID, "partitur.") {
				addDiagnostic(diagnostics, Rule04, pointer, "reserved_output_id")
			}
		}
	}

	for _, movement := range document.Movements {
		base := movementPointer(movement)
		for inputIndex, input := range movement.Inputs {
			pointer := indexPointer(
				base+"/inputs",
				inputIndex,
			)
			if schemaInvalid(document, pointer) ||
				schemaInvalid(document, base+"/id") {
				continue
			}
			producerIndex, exists := outputProducer[input]
			if !exists {
				if document.OutputsComplete && document.MovementsComplete {
					addDiagnostic(diagnostics, Rule04, pointer, "input_output_missing")
				}
				continue
			}
			producer := document.Movements[producerIndex]
			if document.MovementsComplete && graphSchemaComplete(document) &&
				!reachesMovement(document, movement.ID, producer.ID, nil) {
				addDiagnostic(diagnostics, Rule04, pointer, "input_not_reachable")
			}
		}
	}
}

func validateRule07(document *document, diagnostics *[]Diagnostic) {
	for _, movement := range document.Movements {
		base := movementPointer(movement)
		outputs := make(map[string]string, len(movement.Outputs))
		for _, output := range movement.Outputs {
			outputBase := indexPointer(base+"/outputs", output.SourceIndex)
			if schemaInvalid(document, outputBase+"/id") ||
				schemaInvalid(document, outputBase+"/kind") {
				continue
			}
			outputs[output.ID] = output.Kind
		}
		for _, review := range movement.Acceptance.Review {
			pointer := indexPointer(
				base+"/acceptance/review",
				review.SourceIndex,
			) + "/findings"
			if schemaInvalid(document, pointer) {
				continue
			}
			kind, exists := outputs[review.Findings]
			if !exists {
				if movement.OutputsComplete {
					addDiagnostic(diagnostics, Rule07, pointer, "review_findings_missing")
				}
			} else if kind != "findings" {
				addDiagnostic(diagnostics, Rule07, pointer, "review_findings_wrong_kind")
			}
		}
	}
}

func validateRule08(document *document, diagnostics *[]Diagnostic) {
	draftIndexes := make([]int, 0, 1)
	for index, movement := range document.Movements {
		base := movementPointer(movement)
		if schemaInvalid(document, base+"/phase") {
			continue
		}
		if movement.Phase != nil && *movement.Phase == "draft" {
			draftIndexes = append(draftIndexes, index)
			for grantIndex, grant := range movement.Grants {
				pointer := indexPointer(base+"/grants", grantIndex)
				if grant == "repo_write" && !schemaInvalid(document, pointer) {
					addDiagnostic(diagnostics, Rule08,
						pointer,
						"draft_repo_write")
				}
			}
		}
	}
	if len(draftIndexes) > 1 {
		for _, index := range draftIndexes[1:] {
			addDiagnostic(diagnostics, Rule08,
				movementPointer(document.Movements[index])+"/phase",
				"multiple_draft_movements")
		}
	}

	switch {
	case len(draftIndexes) == 0 && document.Draft != nil &&
		document.MovementsComplete &&
		document.MovementPhasesComplete &&
		!schemaInvalid(document, "/draft/interview_movement"):
		addDiagnostic(diagnostics, Rule08, "/draft/interview_movement",
			"draft_phase_missing")
	case len(draftIndexes) > 0 && document.Draft == nil &&
		!schemaInvalid(document, "/draft"):
		addDiagnostic(diagnostics, Rule08, "/draft/interview_movement",
			"draft_reference_missing")
	case len(draftIndexes) > 0 && document.Draft != nil &&
		!schemaInvalid(document, "/draft/interview_movement") &&
		!schemaInvalid(document,
			movementPointer(document.Movements[draftIndexes[0]])+"/id") &&
		document.Movements[draftIndexes[0]].ID != document.Draft.InterviewMovement:
		addDiagnostic(diagnostics, Rule08, "/draft/interview_movement",
			"draft_reference_mismatch")
	}
	if document.Status == "draft" && !schemaInvalid(document, "/status") &&
		document.MovementsComplete && document.MovementPhasesComplete &&
		len(draftIndexes) != 1 {
		addDiagnostic(diagnostics, Rule08, "/movements", "draft_status_requires_movement")
	}
}

func validateRule09(document *document, diagnostics *[]Diagnostic) {
	for _, movement := range document.Movements {
		base := movementPointer(movement)
		seen := make(map[string]struct{},
			len(movement.Acceptance.Hard)+len(movement.Acceptance.Review))
		for _, criterion := range movement.Acceptance.Hard {
			pointer := indexPointer(
				base+"/acceptance/hard",
				criterion.SourceIndex,
			) + "/id"
			if schemaInvalid(document, pointer) {
				continue
			}
			validateCriterionID(criterion.ID, pointer, seen, diagnostics)
		}
		for _, criterion := range movement.Acceptance.Review {
			pointer := indexPointer(
				base+"/acceptance/review",
				criterion.SourceIndex,
			) + "/id"
			if schemaInvalid(document, pointer) {
				continue
			}
			validateCriterionID(criterion.ID, pointer, seen, diagnostics)
		}
	}
}

func validateCriterionID(
	id, pointer string,
	seen map[string]struct{},
	diagnostics *[]Diagnostic,
) {
	if id == "" {
		addDiagnostic(diagnostics, Rule09, pointer, "criterion_id_missing")
		return
	}
	if _, duplicate := seen[id]; duplicate {
		addDiagnostic(diagnostics, Rule09, pointer, "duplicate_criterion_id")
	}
	seen[id] = struct{}{}
}

func validateRule10(document *document, diagnostics *[]Diagnostic) {
	seen := make(map[string]struct{}, len(document.Policy.AllowedPaths))
	for index, pattern := range document.Policy.AllowedPaths {
		pointer := indexPointer("/policy/allowed_paths", index)
		if schemaInvalid(document, pointer) {
			continue
		}
		if _, duplicate := seen[pattern]; duplicate {
			addDiagnostic(diagnostics, Rule10, pointer,
				"duplicate_allowed_path")
		}
		seen[pattern] = struct{}{}
	}
}

func validateRule11(document *document, diagnostics *[]Diagnostic) {
	if document.Status != "finalized" ||
		document.Verification == nil ||
		document.Verification.Expectation == nil ||
		document.Verification.Expectation.ApplyGate == nil {
		return
	}
	gate := document.Verification.Expectation.ApplyGate
	if gate.Waived != nil && *gate.Waived {
		return
	}
	final, exists := finalMovement(document)
	if !exists {
		return
	}
	finalBase := movementPointer(final)
	hasHard := false
	for _, criterion := range final.Acceptance.Hard {
		item := indexPointer(finalBase+"/acceptance/hard", criterion.SourceIndex)
		if !schemaInvalidBelow(document, item) {
			hasHard = true
			break
		}
	}
	hardComplete := !schemaInvalidBelow(document, finalBase+"/acceptance/hard")
	hasTypedReview, reviewComplete := movementHasTypedReview(document, final)
	for index, grade := range gate.Require {
		pointer := indexPointer("/verification/expectation/apply_gate/require", index)
		if schemaInvalid(document, pointer) {
			continue
		}
		switch {
		case grade == "verified" && !hasHard && hardComplete:
			addDiagnostic(diagnostics, Rule11, pointer, "verified_unachievable")
		case grade == "reviewed" && !hasTypedReview && reviewComplete:
			addDiagnostic(diagnostics, Rule11, pointer, "reviewed_unachievable")
		case grade == "approved" &&
			!schemaInvalidBelow(document, finalBase+"/acceptance/human_gate") &&
			final.Acceptance.HumanGate != "always":
			addDiagnostic(diagnostics, Rule11, pointer, "approved_unachievable")
		}
	}
	hasValidPredicate := false
	for index := range gate.Predicates {
		if !schemaInvalid(document,
			indexPointer("/verification/expectation/apply_gate/predicates", index)) {
			hasValidPredicate = true
		}
	}
	if hasValidPredicate && !hasTypedReview && reviewComplete {
		addDiagnostic(diagnostics, Rule11,
			"/verification/expectation/apply_gate/predicates",
			"predicate_unachievable")
	}
}

func validateRule12(document *document, diagnostics *[]Diagnostic) {
	if document.Status != "finalized" ||
		document.Verification == nil ||
		document.Verification.Expectation == nil ||
		document.Verification.Expectation.ApplyGate == nil {
		return
	}
	gate := document.Verification.Expectation.ApplyGate
	if schemaInvalid(document, "/verification/expectation/apply_gate/waived") ||
		schemaInvalid(document, "/verification/expectation/apply_gate/require") {
		return
	}
	if gate.Waived != nil && *gate.Waived {
		if document.Verification.FinalMovement != nil {
			addDiagnostic(diagnostics, Rule12, "/verification/final_movement",
				"waived_final_movement_present")
		}
		return
	}
	if document.Verification.FinalMovement == nil {
		if !schemaInvalid(document, "/verification/final_movement") {
			addDiagnostic(diagnostics, Rule12, "/verification/final_movement",
				"final_movement_missing")
		}
		return
	}
	if schemaInvalid(document, "/verification/final_movement") {
		return
	}
	finalID := *document.Verification.FinalMovement
	final, exists := movementByID(document, finalID)
	if !exists {
		if document.MovementsComplete && document.MovementIDsComplete {
			addDiagnostic(diagnostics, Rule12, "/verification/final_movement",
				"final_movement_unknown")
		}
		return
	}
	finalBase := movementPointer(final)
	if containsValid(document, final.Grants, finalBase+"/grants", "repo_write") {
		addDiagnostic(diagnostics, Rule12, "/verification/final_movement",
			"final_movement_repo_write")
	}
	for _, movement := range document.Movements {
		for needIndex, need := range movement.Needs {
			pointer := indexPointer(movementPointer(movement)+"/needs", needIndex)
			if need == finalID && !schemaInvalid(document, pointer) {
				addDiagnostic(diagnostics, Rule12,
					pointer,
					"final_movement_has_downstream")
			}
		}
		if movement.ID == finalID ||
			(movement.Phase != nil && *movement.Phase == "draft") {
			continue
		}
		if document.MovementsComplete && graphSchemaComplete(document) &&
			!reachesMovement(document, finalID, movement.ID, nil) {
			addDiagnostic(diagnostics, Rule12, movementPointer(movement),
				"outside_final_movement_closure")
		}
	}
}

func validateRule14(document *document, diagnostics *[]Diagnostic) {
	for _, movement := range document.Movements {
		base := movementPointer(movement)
		if schemaInvalid(document, base+"/phase") ||
			movement.Phase == nil || *movement.Phase != "draft" {
			continue
		}
		for _, output := range movement.Outputs {
			pointer := indexPointer(
				base+"/outputs",
				output.SourceIndex,
			)
			if schemaInvalidBelow(document, pointer) {
				continue
			}
			if output.Kind == "change_set" {
				addDiagnostic(diagnostics, Rule14, pointer, "draft_change_set_output")
			} else {
				addDiagnostic(diagnostics, Rule14, pointer, "draft_artifact_output")
			}
		}
	}
}

func validateRule15(document *document, diagnostics *[]Diagnostic) {
	for _, movement := range document.Movements {
		base := movementPointer(movement)
		changeSets := 0
		for _, output := range movement.Outputs {
			outputBase := indexPointer(base+"/outputs", output.SourceIndex)
			if output.Kind == "change_set" &&
				!schemaInvalid(document, outputBase+"/kind") {
				changeSets++
			}
		}
		hasWrite := containsValid(document, movement.Grants, base+"/grants", "repo_write")
		grantsComplete := !schemaInvalidBelow(document, base+"/grants")
		outputsComplete := movement.OutputsComplete
		if hasWrite && (changeSets > 1 || outputsComplete && changeSets != 1) {
			addDiagnostic(diagnostics, Rule15,
				base+"/outputs",
				"write_change_set_count")
		}
		if !hasWrite && grantsComplete && changeSets != 0 {
			addDiagnostic(diagnostics, Rule15,
				base+"/outputs",
				"nonwrite_change_set")
		}
	}
}

func validateRule16(document *document, diagnostics *[]Diagnostic) {
	for id := range document.Parts {
		pointer := pointerJoin("/parts", id)
		if !schemaInvalid(document, pointer) {
			validateIdentifier(id, pointer, diagnostics)
		}
	}
	if document.Draft != nil {
		pointer := "/draft/interview_movement"
		if !schemaInvalid(document, pointer) {
			validateIdentifier(document.Draft.InterviewMovement, pointer, diagnostics)
		}
	}
	if document.Verification != nil && document.Verification.FinalMovement != nil {
		pointer := "/verification/final_movement"
		if !schemaInvalid(document, pointer) {
			validateIdentifier(*document.Verification.FinalMovement, pointer, diagnostics)
		}
	}
	for _, question := range document.OpenQuestions {
		pointer := indexPointer("/open_questions", question.SourceIndex) + "/id"
		if !schemaInvalid(document, pointer) {
			validateIdentifier(question.ID, pointer, diagnostics)
		}
	}
	for _, movement := range document.Movements {
		base := movementPointer(movement)
		validateIdentifierIfValid(document, movement.ID, base+"/id", diagnostics)
		validateIdentifierIfValid(document, movement.PartID, base+"/part", diagnostics)
		for index, need := range movement.Needs {
			validateIdentifierIfValid(document, need,
				indexPointer(base+"/needs", index), diagnostics)
		}
		for index, input := range movement.Inputs {
			validateIdentifierIfValid(document, input,
				indexPointer(base+"/inputs", index), diagnostics)
		}
		for _, output := range movement.Outputs {
			validateIdentifierIfValid(document, output.ID,
				indexPointer(base+"/outputs", output.SourceIndex)+"/id", diagnostics)
		}
		for _, criterion := range movement.Acceptance.Hard {
			criterionBase := indexPointer(base+"/acceptance/hard", criterion.SourceIndex)
			if criterion.IDSet {
				validateIdentifierIfValid(
					document, criterion.ID, criterionBase+"/id", diagnostics)
			}
			if criterion.Artifact != nil {
				validateIdentifierIfValid(document, *criterion.Artifact,
					criterionBase+"/artifact", diagnostics)
			}
		}
		for _, criterion := range movement.Acceptance.Review {
			criterionBase := indexPointer(base+"/acceptance/review", criterion.SourceIndex)
			if criterion.IDSet {
				validateIdentifierIfValid(
					document, criterion.ID, criterionBase+"/id", diagnostics)
			}
			validateIdentifierIfValid(document, criterion.Findings,
				criterionBase+"/findings", diagnostics)
			for rubricIndex, rubric := range criterion.Rubric {
				validateIdentifierIfValid(document, rubric,
					indexPointer(criterionBase+"/rubric", rubricIndex), diagnostics)
			}
		}
	}
}

func validateIdentifier(id, pointer string, diagnostics *[]Diagnostic) {
	if !identifierPattern.MatchString(id) {
		addDiagnostic(diagnostics, Rule16, pointer, "invalid_identifier")
	}
}

func validateRule17(document *document, diagnostics *[]Diagnostic) {
	for _, movement := range document.Movements {
		base := movementPointer(movement)
		ordinary := make(map[string]struct{})
		for _, output := range movement.Outputs {
			outputBase := indexPointer(base+"/outputs", output.SourceIndex)
			if schemaInvalidBelow(document, outputBase) {
				continue
			}
			if output.Kind != "change_set" {
				ordinary[output.ID] = struct{}{}
			}
		}
		seen := make(map[string]struct{})
		for _, criterion := range movement.Acceptance.Hard {
			if criterion.Artifact == nil {
				continue
			}
			criterionBase := indexPointer(base+"/acceptance/hard", criterion.SourceIndex)
			if schemaInvalid(document, criterionBase+"/artifact") {
				continue
			}
			if _, exists := ordinary[*criterion.Artifact]; !exists {
				continue
			}
			if _, duplicate := seen[*criterion.Artifact]; duplicate {
				addDiagnostic(diagnostics, Rule17,
					indexPointer(
						base+"/acceptance/hard",
						criterion.SourceIndex,
					)+"/artifact",
					"duplicate_artifact_criterion")
			}
			seen[*criterion.Artifact] = struct{}{}
		}
	}
}

func validateRule18(document *document, diagnostics *[]Diagnostic) {
	changeSets := make(map[string]struct{})
	for _, movement := range document.Movements {
		for _, output := range movement.Outputs {
			outputBase := indexPointer(
				movementPointer(movement)+"/outputs", output.SourceIndex)
			if output.Kind == "change_set" && !schemaInvalidBelow(document, outputBase) {
				changeSets[output.ID] = struct{}{}
			}
		}
	}
	for _, movement := range document.Movements {
		for _, criterion := range movement.Acceptance.Hard {
			if criterion.Artifact == nil {
				continue
			}
			criterionBase := indexPointer(
				movementPointer(movement)+"/acceptance/hard", criterion.SourceIndex)
			if schemaInvalid(document, criterionBase+"/artifact") {
				continue
			}
			if _, changeSet := changeSets[*criterion.Artifact]; changeSet {
				addDiagnostic(diagnostics, Rule18,
					indexPointer(
						movementPointer(movement)+"/acceptance/hard",
						criterion.SourceIndex,
					)+"/artifact",
					"change_set_artifact_criterion")
			}
		}
	}
}

func movementByID(document *document, id string) (movement, bool) {
	for _, movement := range document.Movements {
		if movement.ID == id &&
			!schemaInvalid(document, movementPointer(movement)+"/id") {
			return movement, true
		}
	}
	return movement{}, false
}

func finalMovement(document *document) (movement, bool) {
	if document.Verification == nil || document.Verification.FinalMovement == nil {
		return movement{}, false
	}
	return movementByID(document, *document.Verification.FinalMovement)
}

func reachesMovement(
	document *document,
	from, target string,
	visited map[string]struct{},
) bool {
	if visited == nil {
		visited = make(map[string]struct{})
	}
	if _, seen := visited[from]; seen {
		return false
	}
	visited[from] = struct{}{}
	movement, exists := movementByID(document, from)
	if !exists {
		return false
	}
	for index, need := range movement.Needs {
		if schemaInvalid(document,
			indexPointer(movementPointer(movement)+"/needs", index)) {
			continue
		}
		if need == target || reachesMovement(document, need, target, visited) {
			return true
		}
	}
	return false
}

func movementHasTypedReview(document *document, movement movement) (bool, bool) {
	base := movementPointer(movement)
	complete := !schemaInvalidBelow(document, base+"/acceptance/review") &&
		movement.OutputsComplete
	outputs := make(map[string]string, len(movement.Outputs))
	for _, output := range movement.Outputs {
		outputBase := indexPointer(base+"/outputs", output.SourceIndex)
		if schemaInvalidBelow(document, outputBase) {
			continue
		}
		outputs[output.ID] = output.Kind
	}
	for _, review := range movement.Acceptance.Review {
		reviewBase := indexPointer(base+"/acceptance/review", review.SourceIndex)
		if schemaInvalidBelow(document, reviewBase) {
			continue
		}
		if outputs[review.Findings] == "findings" {
			return true, complete
		}
	}
	return false, complete
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func movementPointer(movement movement) string {
	return indexPointer("/movements", movement.SourceIndex)
}

func validateIdentifierIfValid(
	document *document,
	id, pointer string,
	diagnostics *[]Diagnostic,
) {
	if !schemaInvalid(document, pointer) {
		validateIdentifier(id, pointer, diagnostics)
	}
}

func schemaInvalid(document *document, pointer string) bool {
	_, invalid := document.InvalidPointers[pointer]
	return invalid
}

func schemaInvalidBelow(document *document, pointer string) bool {
	for invalid := range document.InvalidPointers {
		if invalid == pointer ||
			strings.HasPrefix(invalid, pointer+"/") ||
			strings.HasPrefix(pointer, invalid+"/") {
			return true
		}
	}
	return false
}

func containsValid(
	document *document,
	values []string,
	base, target string,
) bool {
	for index, value := range values {
		if value == target && !schemaInvalid(document, indexPointer(base, index)) {
			return true
		}
	}
	return false
}

func graphSchemaComplete(document *document) bool {
	for _, movement := range document.Movements {
		base := movementPointer(movement)
		if schemaInvalid(document, base+"/id") ||
			schemaInvalidBelow(document, base+"/needs") {
			return false
		}
	}
	return true
}
