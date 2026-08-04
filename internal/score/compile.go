package score

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
)

var expectedHashPattern = regexp.MustCompile(`^sha256:[0-9A-Fa-f]+$`)

// Compile parses, validates, defaults, and materializes a score. Restricted
// YAML ingress failures end validation before a representation graph exists.
// Once a graph exists, all independently evaluable diagnostics are collected.
func Compile(src []byte) (*Score, []Diagnostic) {
	value, err := canonical.ParseYAML(src)
	if err != nil {
		return nil, []Diagnostic{{
			Rule:   RuleIngress,
			Detail: "invalid_restricted_yaml",
		}}
	}
	return CompileValue(value)
}

// CompileValue validates and defaults a canonical JSON value as a score. It is
// the value-level counterpart to Compile, used when a proposal has already
// been applied to the canonical score representation.
func CompileValue(value any) (*Score, []Diagnostic) {

	decoder := schemaDecoder{
		partsComplete:          true,
		movementsComplete:      true,
		movementIDsComplete:    true,
		movementPhasesComplete: true,
		outputsComplete:        true,
		invalidPointers:        make(map[string]struct{}),
	}
	document, usable := decoder.decodeDocument(value)
	if usable {
		validateRules(&document, &decoder.diagnostics)
	}
	sortDiagnostics(decoder.diagnostics)
	if len(decoder.diagnostics) != 0 {
		return nil, decoder.diagnostics
	}
	return &Score{document: document}, nil
}

type schemaDecoder struct {
	diagnostics            []Diagnostic
	partsComplete          bool
	movementsComplete      bool
	movementIDsComplete    bool
	movementPhasesComplete bool
	outputsComplete        bool
	invalidPointers        map[string]struct{}
}

func (d *schemaDecoder) decodeDocument(value any) (document, bool) {
	root, ok := d.object(value, "")
	if !ok {
		return document{}, false
	}
	d.fields(root, "", "score", "name", "revision", "status", "goal", "context",
		"draft", "open_questions", "verification", "parts", "movements", "policy",
		"extensions")

	var result document
	result.Version, _ = d.requiredString(root, "", "score")
	if result.Version != "" && result.Version != "0.2" {
		d.schema("/score", "invalid_value")
	}
	result.Name, _ = d.requiredString(root, "", "name")
	result.Revision, _ = d.requiredInteger(root, "", "revision", 1)
	result.Status, _ = d.requiredString(root, "", "status")
	if result.Status != "" && result.Status != "draft" && result.Status != "finalized" {
		d.schema("/status", "invalid_value")
	}
	result.Goal, _ = d.requiredString(root, "", "goal")
	result.Context = d.optionalString(root, "", "context")
	result.Draft = d.decodeDraft(root)
	result.OpenQuestions = d.decodeQuestions(root)
	result.Verification = d.decodeVerification(root)
	result.Parts = d.decodeParts(root)
	result.Movements = d.decodeMovements(root)
	result.Policy = d.decodePolicy(root)
	result.Extensions = d.decodeExtensions(root)
	result.PartsComplete = d.partsComplete
	result.MovementsComplete = d.movementsComplete
	result.MovementIDsComplete = d.movementIDsComplete
	result.MovementPhasesComplete = d.movementPhasesComplete
	result.OutputsComplete = d.outputsComplete
	result.InvalidPointers = d.invalidPointers

	for index := range result.Movements {
		movement := &result.Movements[index]
		if movement.Phase != nil && *movement.Phase == "draft" {
			if movement.MayProposeSet && !movement.MayPropose {
				addDiagnostic(&d.diagnostics, Rule19,
					indexPointer("/movements", movement.SourceIndex)+"/may_propose",
					"draft_may_propose_false")
			}
			movement.MayPropose = true
		}
	}
	applyDefaults(&result)
	return result, true
}

func (d *schemaDecoder) decodeDraft(root map[string]any) *draft {
	value, present := root["draft"]
	if !present {
		return nil
	}
	object, ok := d.object(value, "/draft")
	if !ok {
		return nil
	}
	d.fields(object, "/draft", "interview_movement")
	interview, _ := d.requiredString(object, "/draft", "interview_movement")
	return &draft{InterviewMovement: interview}
}

func (d *schemaDecoder) decodeQuestions(root map[string]any) []question {
	value, present := root["open_questions"]
	if !present {
		return []question{}
	}
	values, ok := d.array(value, "/open_questions")
	if !ok {
		return nil
	}
	result := make([]question, 0, len(values))
	for index, value := range values {
		pointer := indexPointer("/open_questions", index)
		object, ok := d.object(value, pointer)
		if !ok {
			continue
		}
		d.fields(object, pointer, "id", "question", "resolution", "waived")
		id, _ := d.requiredString(object, pointer, "id")
		text, _ := d.requiredString(object, pointer, "question")
		result = append(result, question{
			SourceIndex: index,
			ID:          id,
			Question:    text,
			Resolution:  d.optionalString(object, pointer, "resolution"),
			Waived:      d.optionalBool(object, pointer, "waived"),
		})
	}
	return result
}

func (d *schemaDecoder) decodeVerification(root map[string]any) *verification {
	value, present := root["verification"]
	if !present {
		return nil
	}
	object, ok := d.object(value, "/verification")
	if !ok {
		return nil
	}
	d.fields(object, "/verification", "expectation", "final_movement")
	result := &verification{
		FinalMovement: d.optionalString(object, "/verification", "final_movement"),
	}
	expectationValue, present := object["expectation"]
	if !present {
		return result
	}
	expectationObject, ok := d.object(expectationValue, "/verification/expectation")
	if !ok {
		return result
	}
	d.fields(expectationObject, "/verification/expectation", "intent", "apply_gate")
	expectation := &expectation{
		Intent: d.optionalString(expectationObject, "/verification/expectation", "intent"),
	}
	if expectation.Intent != nil &&
		*expectation.Intent != "write-basic-tests" &&
		*expectation.Intent != "pass-existing-tests" &&
		*expectation.Intent != "none" {
		d.schema("/verification/expectation/intent", "invalid_value")
	}
	gateValue, present := expectationObject["apply_gate"]
	if present {
		if gateObject, ok := d.object(gateValue, "/verification/expectation/apply_gate"); ok {
			expectation.ApplyGate = d.decodeApplyGate(gateObject)
		}
	}
	result.Expectation = expectation
	return result
}

func (d *schemaDecoder) decodeApplyGate(object map[string]any) *applyGate {
	pointer := "/verification/expectation/apply_gate"
	d.fields(object, pointer, "require", "waived", "reason", "predicates")
	result := &applyGate{
		Reason:     d.optionalString(object, pointer, "reason"),
		Waived:     d.optionalBool(object, pointer, "waived"),
		Predicates: []string{},
	}
	if value, present := object["require"]; present {
		result.RequireSet = true
		result.Require = d.stringArray(value, pointer+"/require")
		if len(result.Require) == 0 {
			d.schema(pointer+"/require", "must_be_non_empty")
		}
		d.requireUnique(result.Require, pointer+"/require")
		for index, item := range result.Require {
			if item != "verified" && item != "approved" && item != "reviewed" {
				d.schema(indexPointer(pointer+"/require", index), "invalid_value")
			}
		}
	}
	if value, present := object["predicates"]; present {
		result.Predicates = d.stringArray(value, pointer+"/predicates")
		d.requireUnique(result.Predicates, pointer+"/predicates")
		for index, item := range result.Predicates {
			if item != "no_unresolved_blocking_findings" && item != "no_blocking_findings" {
				d.schema(indexPointer(pointer+"/predicates", index), "invalid_value")
			}
		}
	}
	return result
}

func (d *schemaDecoder) decodeParts(root map[string]any) map[string]part {
	value, present := d.required(root, "", "parts")
	if !present {
		d.partsComplete = false
		return nil
	}
	object, ok := d.object(value, "/parts")
	if !ok {
		d.partsComplete = false
		return nil
	}
	result := make(map[string]part, len(object))
	for id, value := range object {
		pointer := pointerJoin("/parts", id)
		partObject, ok := d.object(value, pointer)
		if !ok {
			result[id] = part{}
			continue
		}
		d.fields(partObject, pointer, "capabilities", "read_only")
		capabilityValue, present := d.required(partObject, pointer, "capabilities")
		var capabilities []string
		if present {
			capabilities = d.stringArray(capabilityValue, pointer+"/capabilities")
			if len(capabilities) == 0 {
				d.schema(pointer+"/capabilities", "must_be_non_empty")
			}
			d.requireUnique(capabilities, pointer+"/capabilities")
		}
		readOnly := false
		if value := d.optionalBool(partObject, pointer, "read_only"); value != nil {
			readOnly = *value
		}
		result[id] = part{Capabilities: capabilities, ReadOnly: readOnly, Valid: true}
	}
	return result
}

func (d *schemaDecoder) decodeMovements(root map[string]any) []movement {
	value, present := d.required(root, "", "movements")
	if !present {
		d.movementsComplete = false
		return nil
	}
	values, ok := d.array(value, "/movements")
	if !ok {
		d.movementsComplete = false
		return nil
	}
	result := make([]movement, 0, len(values))
	for index, value := range values {
		pointer := indexPointer("/movements", index)
		object, ok := d.object(value, pointer)
		if !ok {
			d.movementsComplete = false
			continue
		}
		d.fields(object, pointer, "id", "phase", "part", "needs", "grants",
			"instruction", "inputs", "outputs", "may_propose", "acceptance")
		item := movement{
			SourceIndex:     index,
			Needs:           []string{},
			Grants:          []string{},
			Inputs:          []string{},
			Outputs:         []output{},
			OutputsComplete: true,
			Acceptance:      acceptance{Hard: []hardCriterion{}, Review: []reviewCriterion{}, HumanGate: "never"},
		}
		var idValid bool
		item.ID, idValid = d.requiredString(object, pointer, "id")
		if !idValid {
			d.movementIDsComplete = false
		}
		item.Phase = d.optionalString(object, pointer, "phase")
		if _, present := object["phase"]; present && item.Phase == nil {
			d.movementPhasesComplete = false
		}
		if item.Phase != nil && *item.Phase != "draft" {
			d.schema(pointer+"/phase", "invalid_value")
			d.movementPhasesComplete = false
		}
		item.PartID, _ = d.requiredString(object, pointer, "part")
		item.Instruction, _ = d.requiredString(object, pointer, "instruction")
		if listValue, present := object["needs"]; present {
			item.Needs = d.stringArray(listValue, pointer+"/needs")
			d.requireUnique(item.Needs, pointer+"/needs")
		}
		if listValue, present := object["grants"]; present {
			item.Grants = d.stringArray(listValue, pointer+"/grants")
			d.requireUnique(item.Grants, pointer+"/grants")
		}
		if listValue, present := object["inputs"]; present {
			item.Inputs = d.stringArray(listValue, pointer+"/inputs")
			d.requireUnique(item.Inputs, pointer+"/inputs")
		}
		if outputValue, present := object["outputs"]; present {
			var idsComplete bool
			item.Outputs, idsComplete, item.OutputsComplete =
				d.decodeOutputs(outputValue, pointer+"/outputs")
			if !idsComplete {
				d.outputsComplete = false
			}
		}
		if mayPropose := d.optionalBool(object, pointer, "may_propose"); mayPropose != nil {
			item.MayPropose = *mayPropose
			item.MayProposeSet = true
		}
		if acceptanceValue, present := object["acceptance"]; present {
			item.Acceptance = d.decodeAcceptance(acceptanceValue, pointer+"/acceptance")
		}
		result = append(result, item)
	}
	return result
}

func (d *schemaDecoder) decodeOutputs(value any, pointer string) ([]output, bool, bool) {
	values, ok := d.array(value, pointer)
	if !ok {
		return nil, false, false
	}
	idsComplete := true
	definitionsComplete := true
	result := make([]output, 0, len(values))
	for index, value := range values {
		itemPointer := indexPointer(pointer, index)
		object, ok := d.object(value, itemPointer)
		if !ok {
			idsComplete = false
			definitionsComplete = false
			continue
		}
		d.fields(object, itemPointer, "id", "kind")
		id, idValid := d.requiredString(object, itemPointer, "id")
		if !idValid {
			idsComplete = false
			definitionsComplete = false
		}
		kind, kindValid := d.requiredString(object, itemPointer, "kind")
		if !kindValid {
			definitionsComplete = false
		}
		if kind == "" {
			d.schema(itemPointer+"/kind", "must_be_non_empty")
			definitionsComplete = false
		}
		result = append(result, output{SourceIndex: index, ID: id, Kind: kind})
	}
	return result, idsComplete, definitionsComplete
}

func (d *schemaDecoder) decodeAcceptance(value any, pointer string) acceptance {
	result := acceptance{Hard: []hardCriterion{}, Review: []reviewCriterion{}, HumanGate: "never"}
	object, ok := d.object(value, pointer)
	if !ok {
		return result
	}
	d.fields(object, pointer, "hard", "review", "human_gate")
	if hardValue, present := object["hard"]; present {
		result.Hard = d.decodeHardCriteria(hardValue, pointer+"/hard")
	}
	if reviewValue, present := object["review"]; present {
		result.Review = d.decodeReviewCriteria(reviewValue, pointer+"/review")
	}
	if humanGate := d.optionalString(object, pointer, "human_gate"); humanGate != nil {
		result.HumanGate = *humanGate
		if *humanGate != "always" && *humanGate != "on_contested" && *humanGate != "never" {
			d.schema(pointer+"/human_gate", "invalid_value")
		}
	}
	return result
}

func (d *schemaDecoder) decodeHardCriteria(value any, pointer string) []hardCriterion {
	values, ok := d.array(value, pointer)
	if !ok {
		return nil
	}
	result := make([]hardCriterion, 0, len(values))
	for index, value := range values {
		itemPointer := indexPointer(pointer, index)
		object, ok := d.object(value, itemPointer)
		if !ok {
			continue
		}
		d.fields(object, itemPointer, "id", "run", "artifact", "timeout_min", "expected_hash")
		item := hardCriterion{SourceIndex: index}
		if id, present := object["id"]; present {
			if value, ok := d.string(id, itemPointer+"/id"); ok {
				item.ID = value
				item.IDSet = true
			}
		}
		runValue, hasRun := object["run"]
		artifactValue, hasArtifact := object["artifact"]
		if hasRun == hasArtifact {
			d.schema(itemPointer, "criterion_kind")
		}
		if hasRun {
			item.Run = d.stringArray(runValue, itemPointer+"/run")
		}
		if hasArtifact {
			if artifact, ok := d.string(artifactValue, itemPointer+"/artifact"); ok {
				item.Artifact = &artifact
			}
		}
		if timeout, present := object["timeout_min"]; present {
			if value, ok := d.integer(timeout, itemPointer+"/timeout_min", 1); ok {
				item.TimeoutMin = &value
			}
			if !hasRun {
				d.schema(itemPointer+"/timeout_min", "criterion_field")
			}
		}
		if hashValue, present := object["expected_hash"]; present {
			if value, ok := d.string(hashValue, itemPointer+"/expected_hash"); ok {
				item.ExpectedHash = &value
				if !expectedHashPattern.MatchString(value) {
					d.schema(itemPointer+"/expected_hash", "invalid_value")
				}
			}
			if !hasArtifact {
				d.schema(itemPointer+"/expected_hash", "criterion_field")
			}
		}
		result = append(result, item)
	}
	return result
}

func (d *schemaDecoder) decodeReviewCriteria(value any, pointer string) []reviewCriterion {
	values, ok := d.array(value, pointer)
	if !ok {
		return nil
	}
	result := make([]reviewCriterion, 0, len(values))
	for index, value := range values {
		itemPointer := indexPointer(pointer, index)
		object, ok := d.object(value, itemPointer)
		if !ok {
			continue
		}
		d.fields(object, itemPointer, "id", "findings", "rubric")
		item := reviewCriterion{SourceIndex: index}
		if id, present := object["id"]; present {
			if value, ok := d.string(id, itemPointer+"/id"); ok {
				item.ID = value
				item.IDSet = true
			}
		}
		item.Findings, _ = d.requiredString(object, itemPointer, "findings")
		if rubricValue, present := d.required(object, itemPointer, "rubric"); present {
			item.Rubric = d.stringArray(rubricValue, itemPointer+"/rubric")
			d.requireUnique(item.Rubric, itemPointer+"/rubric")
		}
		result = append(result, item)
	}
	return result
}

func (d *schemaDecoder) decodePolicy(root map[string]any) policy {
	result := policy{
		AllowedPaths: []string{},
		SideEffects:  []string{},
		Amendment:    amendment{Auto: "off"},
	}
	value, present := d.required(root, "", "policy")
	if !present {
		return result
	}
	object, ok := d.object(value, "/policy")
	if !ok {
		return result
	}
	d.fields(object, "/policy", "allowed_paths", "side_effects", "budget", "amendment")
	if pathsValue, present := object["allowed_paths"]; present {
		result.AllowedPaths = d.stringArray(pathsValue, "/policy/allowed_paths")
	}
	if sideEffectsValue, present := object["side_effects"]; present {
		result.SideEffects = d.stringArray(sideEffectsValue, "/policy/side_effects")
		if len(result.SideEffects) != 0 {
			d.schema("/policy/side_effects", "must_be_empty")
		}
	}
	budgetValue, present := d.required(object, "/policy", "budget")
	if present {
		if budgetObject, ok := d.object(budgetValue, "/policy/budget"); ok {
			d.fields(budgetObject, "/policy/budget",
				"active_wall_clock_min", "retries_per_movement")
			result.Budget.ActiveWallClockMin, _ = d.requiredInteger(
				budgetObject, "/policy/budget", "active_wall_clock_min", 1)
			result.Budget.RetriesPerMovement = 0
			if retryValue, present := budgetObject["retries_per_movement"]; present {
				result.Budget.RetriesPerMovement, _ = d.integer(
					retryValue, "/policy/budget/retries_per_movement", 0)
			}
		}
	}
	if amendmentValue, present := object["amendment"]; present {
		if amendmentObject, ok := d.object(amendmentValue, "/policy/amendment"); ok {
			d.fields(amendmentObject, "/policy/amendment", "auto")
			if auto := d.optionalString(amendmentObject, "/policy/amendment", "auto"); auto != nil {
				result.Amendment.Auto = *auto
				if *auto != "off" && *auto != "envelope" {
					d.schema("/policy/amendment/auto", "invalid_value")
				}
			}
		}
	}
	return result
}

func (d *schemaDecoder) decodeExtensions(root map[string]any) map[string]any {
	value, present := root["extensions"]
	if !present {
		return nil
	}
	object, ok := d.object(value, "/extensions")
	if !ok {
		return nil
	}
	result := make(map[string]any, len(object))
	for id, extension := range object {
		result[id] = cloneJSON(extension)
	}
	return result
}

func (d *schemaDecoder) required(object map[string]any, base, name string) (any, bool) {
	value, present := object[name]
	if !present {
		d.schema(pointerJoin(base, name), "required")
		return nil, false
	}
	return value, true
}

func (d *schemaDecoder) requiredString(object map[string]any, base, name string) (string, bool) {
	value, present := d.required(object, base, name)
	if !present {
		return "", false
	}
	return d.string(value, pointerJoin(base, name))
}

func (d *schemaDecoder) optionalString(object map[string]any, base, name string) *string {
	value, present := object[name]
	if !present {
		return nil
	}
	decoded, ok := d.string(value, pointerJoin(base, name))
	if !ok {
		return nil
	}
	return &decoded
}

func (d *schemaDecoder) string(value any, pointer string) (string, bool) {
	if value == nil {
		d.schema(pointer, "must_not_be_null")
		return "", false
	}
	decoded, ok := value.(string)
	if !ok {
		d.schema(pointer, "expected_string")
		return "", false
	}
	return decoded, true
}

func (d *schemaDecoder) optionalBool(object map[string]any, base, name string) *bool {
	value, present := object[name]
	if !present {
		return nil
	}
	pointer := pointerJoin(base, name)
	if value == nil {
		d.schema(pointer, "must_not_be_null")
		return nil
	}
	decoded, ok := value.(bool)
	if !ok {
		d.schema(pointer, "expected_boolean")
		return nil
	}
	return &decoded
}

func (d *schemaDecoder) requiredInteger(
	object map[string]any,
	base, name string,
	minimum float64,
) (float64, bool) {
	value, present := d.required(object, base, name)
	if !present {
		return 0, false
	}
	return d.integer(value, pointerJoin(base, name), minimum)
}

func (d *schemaDecoder) integer(value any, pointer string, minimum float64) (float64, bool) {
	if value == nil {
		d.schema(pointer, "must_not_be_null")
		return 0, false
	}
	decoded, ok := value.(float64)
	if !ok {
		d.schema(pointer, "expected_number")
		return 0, false
	}
	if err := canonical.ValidateSafeInteger(decoded); err != nil {
		addDiagnostic(&d.diagnostics, Rule13, pointer, "not_safe_integer")
		return decoded, false
	}
	if decoded < minimum {
		d.schema(pointer, "below_minimum")
		return decoded, false
	}
	return decoded, true
}

func (d *schemaDecoder) object(value any, pointer string) (map[string]any, bool) {
	if value == nil {
		d.schema(pointer, "must_not_be_null")
		return nil, false
	}
	decoded, ok := value.(map[string]any)
	if !ok {
		d.schema(pointer, "expected_object")
		return nil, false
	}
	return decoded, true
}

func (d *schemaDecoder) array(value any, pointer string) ([]any, bool) {
	if value == nil {
		d.schema(pointer, "must_not_be_null")
		return nil, false
	}
	decoded, ok := value.([]any)
	if !ok {
		d.schema(pointer, "expected_array")
		return nil, false
	}
	return decoded, true
}

func (d *schemaDecoder) stringArray(value any, pointer string) []string {
	values, ok := d.array(value, pointer)
	if !ok {
		return nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		if decoded, ok := d.string(value, indexPointer(pointer, index)); ok {
			result[index] = decoded
		}
	}
	return result
}

func (d *schemaDecoder) requireUnique(values []string, pointer string) {
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if _, invalid := d.invalidPointers[indexPointer(pointer, index)]; invalid {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			d.schema(indexPointer(pointer, index), "duplicate_value")
		}
		seen[value] = struct{}{}
	}
}

func (d *schemaDecoder) fields(object map[string]any, pointer string, allowed ...string) {
	set := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		set[name] = struct{}{}
	}
	for name := range object {
		if _, ok := set[name]; !ok {
			addDiagnostic(&d.diagnostics, Rule06, pointerJoin(pointer, name), "unknown_field")
		}
	}
}

func (d *schemaDecoder) schema(pointer, detail string) {
	addDiagnostic(&d.diagnostics, RuleSchema, pointer, detail)
	d.invalidPointers[pointer] = struct{}{}
}

func addDiagnostic(diagnostics *[]Diagnostic, rule RuleID, pointer, detail string) {
	*diagnostics = append(*diagnostics, Diagnostic{
		Rule:    rule,
		Pointer: pointer,
		Detail:  detail,
	})
}

func sortDiagnostics(diagnostics []Diagnostic) {
	slices.SortFunc(diagnostics, func(left, right Diagnostic) int {
		if order := diagnosticRank(left.Rule) - diagnosticRank(right.Rule); order != 0 {
			return order
		}
		if order := strings.Compare(left.Pointer, right.Pointer); order != 0 {
			return order
		}
		return strings.Compare(left.Detail, right.Detail)
	})
}

func diagnosticRank(rule RuleID) int {
	switch rule {
	case RuleIngress:
		return 0
	case RuleSchema:
		return 1
	case Rule01:
		return 2
	case Rule02:
		return 3
	case Rule03:
		return 4
	case Rule04:
		return 5
	case Rule06:
		return 6
	case Rule07:
		return 7
	case Rule08:
		return 8
	case Rule09:
		return 9
	case Rule10:
		return 10
	case Rule11:
		return 11
	case Rule12:
		return 12
	case Rule13:
		return 13
	case Rule14:
		return 14
	case Rule15:
		return 15
	case Rule16:
		return 16
	case Rule17:
		return 17
	case Rule18:
		return 18
	case Rule19:
		return 19
	default:
		return 100
	}
}

func pointerJoin(base, token string) string {
	escaped := strings.ReplaceAll(token, "~", "~0")
	escaped = strings.ReplaceAll(escaped, "/", "~1")
	return base + "/" + escaped
}

func indexPointer(base string, index int) string {
	return base + "/" + strconv.Itoa(index)
}

func cloneJSON(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			result[key] = cloneJSON(item)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for index, item := range value {
			result[index] = cloneJSON(item)
		}
		return result
	default:
		return value
	}
}

func applyDefaults(document *document) {
	if document.OpenQuestions == nil {
		document.OpenQuestions = []question{}
	}
	for id, part := range document.Parts {
		if part.Capabilities == nil {
			part.Capabilities = []string{}
		}
		document.Parts[id] = part
	}
	for index := range document.Movements {
		movement := &document.Movements[index]
		if movement.Needs == nil {
			movement.Needs = []string{}
		}
		if movement.Grants == nil {
			movement.Grants = []string{}
		}
		if movement.Inputs == nil {
			movement.Inputs = []string{}
		}
		if movement.Outputs == nil {
			movement.Outputs = []output{}
		}
		if movement.Acceptance.Hard == nil {
			movement.Acceptance.Hard = []hardCriterion{}
		}
		if movement.Acceptance.Review == nil {
			movement.Acceptance.Review = []reviewCriterion{}
		}
		if movement.Acceptance.HumanGate == "" {
			movement.Acceptance.HumanGate = "never"
		}
	}
	if document.Verification != nil &&
		document.Verification.Expectation != nil &&
		document.Verification.Expectation.ApplyGate != nil &&
		document.Verification.Expectation.ApplyGate.Predicates == nil {
		document.Verification.Expectation.ApplyGate.Predicates = []string{}
	}
	if document.Policy.AllowedPaths == nil {
		document.Policy.AllowedPaths = []string{}
	}
	if document.Policy.SideEffects == nil {
		document.Policy.SideEffects = []string{}
	}
	if document.Policy.Amendment.Auto == "" {
		document.Policy.Amendment.Auto = "off"
	}
	document.DefaultsApplied = true
}
