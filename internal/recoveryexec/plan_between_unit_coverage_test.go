package recoveryexec

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/recovery"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

// This paired lock requires an executor handler for every ActionKind
// PlanBetweenUnit can return; its sibling in internal/driver requires the live
// dispatch. The guarantee holds for the return shapes this derivation
// recognizes -- a direct construction, a resolvable same-package Decision
// helper, and a returned variable with one unambiguous construction and no
// post-construction Action.Kind write -- each of which fails closed rather than
// being guessed, naming file, line and expression.
//
// A whole-field replacement -- decision.Action = &Action{Kind: ...} -- is now
// rejected from its left-hand side before return shapes are derived. The guard
// scans every production file in internal/recovery, so it does not need to
// recognize what the right-hand side means. See the fuller scope note on the
// sibling lock in internal/driver.
//
// It also does not guarantee that a dispatched case does useful work: a case
// that dispatches to a refusal satisfies this lock.
// TestLiveLoopFailsRunDirectlyWhenBudgetExhaustsBetweenMovements and its
// charged-path sibling TestLiveChainTerminatesWhenBudgetExhaustsMidChain prove
// the behavioural effect.
func TestPlanBetweenUnitActionKindsHaveRecoveryExecutor(t *testing.T) {
	assertCoverageVacuityGuards(t)
	selected, selectedSteps := planBetweenUnitActionCoverage(t)
	if len(selected) == 0 {
		t.Fatal("derived PlanBetweenUnit action set is empty")
	}

	handlers := defaultKinds()
	handled := make([]recovery.ActionKind, 0, len(selected))
	missing := make([]string, 0)
	for kind := range selected {
		if handler, ok := handlers[kind]; ok && handler != nil {
			handled = append(handled, kind)
			continue
		}
		missing = append(missing, string(kind))
	}
	if len(handled) == 0 {
		t.Fatal("found zero recovery executor handlers for derived PlanBetweenUnit action kinds")
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		t.Fatalf("PlanBetweenUnit action kinds missing recovery executor handlers: %v", missing)
	}
	missingSteps := make([]string, 0)
	for step := range selectedSteps {
		if handler, ok := defaultSteps()[step]; !ok || handler == nil {
			missingSteps = append(missingSteps, string(step))
		}
	}
	if len(missingSteps) != 0 {
		sort.Strings(missingSteps)
		t.Fatalf("PlanBetweenUnit action steps missing recovery executor handlers: %v", missingSteps)
	}
}

// TestPlannerWitnessesSelectRegisteredSteps pairs the source-derived step
// coverage with concrete planner inputs. The source scan catches a step added
// to C.2/C.3 without a handler; the witnesses catch a handler registered with
// no planner path that can select it.
func TestPlannerWitnessesSelectRegisteredSteps(t *testing.T) {
	sourceSteps := make(map[recovery.ActionStep]bool)
	for _, planner := range []string{"PlanBetweenUnit", "PlanAttempt", "PlanAcceptance"} {
		_, steps := plannerActionCoverage(t, planner)
		for step := range steps {
			sourceSteps[step] = true
		}
	}
	if len(sourceSteps) == 0 {
		t.Fatal("derived planner action step set is empty")
	}

	type witness struct {
		name     string
		plan     func(recovery.Input) recovery.Decision
		input    recovery.Input
		wantKind recovery.ActionKind
		want     []recovery.ActionStep
	}
	witnesses := []witness{
		{
			name:     "C2 unstarted attempt",
			plan:     recovery.PlanAttempt,
			input:    recoveryC2Witness(runstate.AttemptStarting),
			wantKind: recovery.ActionRecoverUnstartedAttempt,
			want: []recovery.ActionStep{
				recovery.StepStabilizeHandoff,
				recovery.StepCloseAdapterInterval,
				recovery.StepClassifyAndAppendFailure,
			},
		},
		{
			name:     "C2 unprobed attempt",
			plan:     recovery.PlanAttempt,
			input:    recoveryC2Witness(runstate.AttemptRunning),
			wantKind: recovery.ActionRecoverUnprobedAttempt,
			want: []recovery.ActionStep{
				recovery.StepSweepRecordedSession,
				recovery.StepCloseAdapterInterval,
				recovery.StepClassifyAndAppendFailure,
			},
		},
		{
			name:     "C2 incomplete attempt",
			plan:     recovery.PlanAttempt,
			input:    recoveryC2WitnessWithProbe(),
			wantKind: recovery.ActionRecoverIncompleteAttempt,
			want: []recovery.ActionStep{
				recovery.StepSweepRecordedSession,
				recovery.StepCloseAdapterInterval,
				recovery.StepClassifyAndAppendFailure,
			},
		},
		{
			name:     "C2 lost worktree",
			plan:     recovery.PlanAttempt,
			input:    recoveryC2LostWorktreeWitness(),
			wantKind: recovery.ActionFailWorktreeLost,
			want:     []recovery.ActionStep{recovery.StepClassifyAndAppendFailure},
		},
		{
			name:     "C3 incomplete criterion",
			plan:     recovery.PlanAcceptance,
			input:    recoveryC3IncompleteCriterionWitness(),
			wantKind: recovery.ActionRecoverIncompleteCriterion,
			want: []recovery.ActionStep{
				recovery.StepSweepCriterionSession,
				recovery.StepVerifyAcceptanceSubject,
			},
		},
		{
			name:     "C3 unverified subject",
			plan:     recovery.PlanAcceptance,
			input:    recoveryC3BaseWitness(),
			wantKind: recovery.ActionVerifyAcceptanceSubject,
			want:     []recovery.ActionStep{recovery.StepVerifyAcceptanceSubject},
		},
		{
			name:     "C3 failed criterion",
			plan:     recovery.PlanAcceptance,
			input:    recoveryC3FailedCriterionWitness(),
			wantKind: recovery.ActionAppendAcceptanceFailure,
			want:     []recovery.ActionStep{recovery.StepClassifyAcceptanceFailure},
		},
		{
			name:     "C3 accepted evaluation",
			plan:     recovery.PlanAcceptance,
			input:    recoveryC3AcceptedEvaluationWitness(),
			wantKind: recovery.ActionAppendAcceptanceSuccess,
			want: []recovery.ActionStep{
				recovery.StepAppendAttemptCompleted,
				recovery.StepAppendMovementSucceeded,
			},
		},
		{
			name:     "C4 budget exhaustion during movement",
			plan:     recovery.PlanScheduler,
			input:    recoveryC4BudgetWitness(),
			wantKind: recovery.ActionAppendBudgetFailure,
			want: []recovery.ActionStep{
				recovery.StepAppendMovementBudgetFailure,
				recovery.StepAppendRunFailed,
			},
		},
	}

	handlers := defaultSteps()
	witnessedSteps := make(map[recovery.ActionStep]bool)
	witnessedKinds := make(map[recovery.ActionKind]bool)
	for _, witness := range witnesses {
		t.Run(witness.name, func(t *testing.T) {
			decision := witness.plan(witness.input)
			if decision.Action == nil {
				t.Fatalf("planner returned halt %q, want action %q", decision.Halt, witness.wantKind)
			}
			if decision.Action.Kind != witness.wantKind {
				t.Fatalf("action kind = %q, want %q", decision.Action.Kind, witness.wantKind)
			}
			if !slices.Equal(decision.Action.Steps, witness.want) {
				t.Fatalf("action steps = %v, want %v", decision.Action.Steps, witness.want)
			}
			for _, step := range decision.Action.Steps {
				if handler, ok := handlers[step]; !ok || handler == nil {
					t.Fatalf("planner action step %q has no registered handler", step)
				}
				witnessedSteps[step] = true
			}
			witnessedKinds[decision.Action.Kind] = true
		})
	}

	for step := range sourceSteps {
		if handler, ok := handlers[step]; !ok || handler == nil {
			t.Fatalf("planner-source action step %q has no registered handler", step)
		}
	}
	missingWitnesses := make([]string, 0)
	for step, handler := range handlers {
		if handler == nil || !witnessedSteps[step] {
			missingWitnesses = append(missingWitnesses, string(step))
		}
	}
	if len(missingWitnesses) != 0 {
		sort.Strings(missingWitnesses)
		t.Fatalf("registered defaultSteps without planner witness: %v", missingWitnesses)
	}

	missingKinds := make([]string, 0)
	for kind := range stepDispatchedActionKinds {
		if !witnessedKinds[kind] {
			missingKinds = append(missingKinds, string(kind))
		}
	}
	if len(missingKinds) != 0 {
		sort.Strings(missingKinds)
		t.Fatalf("step-dispatched ActionKinds without planner witness: %v", missingKinds)
	}
}

func recoveryC2Witness(state runstate.AttemptState) recovery.Input {
	input := recovery.Input{Projection: recovery.Projection{State: runstate.NewState([]runstate.MovementSeed{{ID: "movement", Initial: runstate.MovementPending}})}}
	input.Projection.State.Run = runstate.RunRunning
	input.Projection.State.ScoreHead.Revision = 1
	input.Projection.CurrentHeadAttempt = &recovery.AttemptRecovery{
		AttemptID: "attempt", MovementID: "movement", ScoreRevision: 1, State: state,
	}
	return input
}

func recoveryC2WitnessWithProbe() recovery.Input {
	input := recoveryC2Witness(runstate.AttemptRunning)
	input.Projection.State.AdapterObservations["attempt"] = runstate.AdapterObservation{}
	return input
}

func recoveryC2LostWorktreeWitness() recovery.Input {
	input := recoveryC2Witness(runstate.AttemptVerifying)
	input.Projection.State.RepoWriteMovements["movement"] = true
	input.Observations.Worktree = recovery.WorktreeMissing
	return input
}

func recoveryC3BaseWitness() recovery.Input {
	input := recoveryC2Witness(runstate.AttemptVerifying)
	input.Projection.CurrentHeadAttempt.AcceptanceStarted = true
	input.Projection.State.Acceptances["attempt"] = runstate.Acceptance{
		Started: true, SubjectTree: "tree", PlannedCriterionIDs: []runstate.CriterionID{"criterion"},
		Criteria: map[runstate.CriterionID]runstate.CriterionRecord{},
	}
	input.Projection.Acceptance = &recovery.AcceptanceRecovery{}
	return input
}

func recoveryC3IncompleteCriterionWitness() recovery.Input {
	input := recoveryC3BaseWitness()
	acceptance := input.Projection.State.Acceptances["attempt"]
	acceptance.Criteria["criterion"] = runstate.CriterionRecord{Started: true}
	input.Projection.State.Acceptances["attempt"] = acceptance
	return input
}

func recoveryC3FailedCriterionWitness() recovery.Input {
	input := recoveryC3BaseWitness()
	acceptance := input.Projection.State.Acceptances["attempt"]
	acceptance.Criteria["criterion"] = runstate.CriterionRecord{Started: true, Completed: true, Outcome: "FAIL"}
	input.Projection.State.Acceptances["attempt"] = acceptance
	return input
}

func recoveryC3AcceptedEvaluationWitness() recovery.Input {
	input := recoveryC3BaseWitness()
	acceptance := input.Projection.State.Acceptances["attempt"]
	acceptance.EvaluationCompleted = true
	input.Projection.State.Acceptances["attempt"] = acceptance
	input.Observations.AcceptanceSubject = recovery.SubjectMatched
	return input
}

func recoveryC4BudgetWitness() recovery.Input {
	input := recovery.Input{Projection: recovery.Projection{State: runstate.NewState([]runstate.MovementSeed{{ID: "movement", Initial: runstate.MovementPending}})}}
	input.Projection.State.Run = runstate.RunRunning
	input.Projection.State.Movements["movement"] = runstate.MovementRunning
	input.Projection.Scheduler = recovery.Scheduler{
		Movements: []recovery.ScheduledMovement{{ID: "movement"}},
	}
	return input
}

func assertCoverageVacuityGuards(t *testing.T) {
	t.Helper()
	_, path, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate recovery executor coverage test source")
	}
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		t.Fatalf("parse recovery executor coverage test source: %v", err)
	}
	found := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		statement, ok := node.(*ast.IfStmt)
		if !ok || !isEmptyGuard(statement.Cond) || !fatalGuardBody(statement.Body) {
			return true
		}
		found[guardedName(statement.Cond)] = true
		return true
	})
	for _, name := range []string{"selected", "handled"} {
		if !found[name] {
			t.Fatalf("coverage vacuity guard for %q is absent from %s", name, path)
		}
	}
}

func isEmptyGuard(expression ast.Expr) bool {
	comparison, ok := expression.(*ast.BinaryExpr)
	if !ok || comparison.Op != token.EQL {
		return false
	}
	call, ok := comparison.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	name, ok := call.Fun.(*ast.Ident)
	if !ok || name.Name != "len" {
		return false
	}
	identifier, ok := call.Args[0].(*ast.Ident)
	if !ok || (identifier.Name != "selected" && identifier.Name != "handled") {
		return false
	}
	literal, ok := comparison.Y.(*ast.BasicLit)
	return ok && literal.Kind == token.INT && literal.Value == "0"
}

func guardedName(expression ast.Expr) string {
	comparison := expression.(*ast.BinaryExpr)
	call := comparison.X.(*ast.CallExpr)
	return call.Args[0].(*ast.Ident).Name
}

func fatalGuardBody(body *ast.BlockStmt) bool {
	for _, statement := range body.List {
		expression, ok := statement.(*ast.ExprStmt)
		if !ok {
			continue
		}
		call, ok := expression.X.(*ast.CallExpr)
		if !ok {
			continue
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if ok && selector.Sel.Name == "Fatal" {
			return true
		}
	}
	return false
}

// planBetweenUnitActionCoverage derives the action kinds returned by the
// PlanBetweenUnit selector, including any same-package Decision helper it
// calls. The AST is the selector's source authority; this intentionally has
// no hand-maintained ActionKind list.
func planBetweenUnitActionCoverage(t *testing.T) (map[recovery.ActionKind]bool, map[recovery.ActionStep]bool) {
	return plannerActionCoverage(t, "PlanBetweenUnit")
}

// plannerActionCoverage derives the action kinds and explicit ActionStep
// values returned by one recovery planner, including same-package Decision
// helpers. It recognizes ActionStep slice literals and append calls that add a
// declared ActionStep, and otherwise retains the fail-closed return-shape
// rules used by the PlanBetweenUnit lock.
func plannerActionCoverage(t *testing.T, planner string) (map[recovery.ActionKind]bool, map[recovery.ActionStep]bool) {
	t.Helper()
	parsed := parseRecoveryPackage(t)
	if planner == "PlanBetweenUnit" {
		rejectDecisionActionFieldWrites(t, parsed)
	}

	selected := make(map[recovery.ActionKind]bool)
	selectedSteps := make(map[recovery.ActionStep]bool)
	visited := make(map[string]bool)
	var visit func(string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		function := parsed.functions[name]
		if function == nil {
			t.Fatalf("PlanBetweenUnit helper %q is absent", name)
		}
		helpers := make([]string, 0)
		ast.Inspect(function.Body, func(node ast.Node) bool {
			literal, ok := node.(*ast.CompositeLit)
			if ok && isActionStepSlice(literal) {
				for _, element := range literal.Elts {
					step, ok := element.(*ast.Ident)
					if !ok {
						t.Fatalf("PlanBetweenUnit action step is %T, want identifier", element)
					}
					value, ok := parsed.actionSteps[step.Name]
					if !ok {
						t.Fatalf("PlanBetweenUnit action step %q has no declared value", step.Name)
					}
					selectedSteps[recovery.ActionStep(value)] = true
				}
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if callee, ok := call.Fun.(*ast.Ident); ok && parsed.functions[callee.Name] != nil && callee.Name != name {
				helpers = append(helpers, callee.Name)
			}
			arguments := actionStepAppendArguments(parsed, call)
			if len(arguments) == 0 {
				return true
			}
			for _, argument := range arguments {
				step, ok := argument.(*ast.Ident)
				if !ok {
					t.Fatalf("%s appended action step is %T, want identifier", planner, argument)
				}
				value, ok := parsed.actionSteps[step.Name]
				if !ok {
					t.Fatalf("%s appended action step %q has no declared value", planner, step.Name)
				}
				selectedSteps[recovery.ActionStep(value)] = true
			}
			return true
		})
		if planner != "PlanBetweenUnit" {
			for _, helper := range helpers {
				visit(helper)
			}
			return
		}
		for _, result := range decisionReturns(function) {
			resolveDecisionExpression(t, parsed, function, result, selected, visit)
		}
	}
	visit(planner)
	return selected, selectedSteps
}

// rejectDecisionActionFieldWrites rejects every production Action-field write
// in recovery. The predicate is deliberately only the assignment left-hand
// side: interpreting right-hand sides led to six evadeable parser revisions.
func rejectDecisionActionFieldWrites(t *testing.T, parsed recoveryPackage) {
	t.Helper()
	for _, file := range parsed.files {
		ast.Inspect(file, func(node ast.Node) bool {
			assignment, ok := node.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for _, left := range assignment.Lhs {
				if isDecisionActionFieldWrite(left) {
					decisionActionFieldWrite(t, parsed, assignment)
				}
			}
			return true
		})
	}
}

// isDecisionActionFieldWrite reports whether an assignment target replaces the
// Action value itself. The target is unwrapped through pointer dereferences and
// parentheses first, so `decision.Action = ...`, `*decision.Action = ...`, and
// `(*decision).Action = ...` are all caught while a write to a field *inside*
// Action -- `decision.Action.BlockedProposalRoute = &route`, which production
// does legitimately -- is not. Only the outermost node is judged, so this stays
// a rule about the left-hand side rather than an interpretation of intent.
func isDecisionActionFieldWrite(expression ast.Expr) bool {
	for {
		switch typed := expression.(type) {
		case *ast.ParenExpr:
			expression = typed.X
		case *ast.StarExpr:
			expression = typed.X
		case *ast.SelectorExpr:
			return typed.Sel != nil && typed.Sel.Name == "Action"
		default:
			return false
		}
	}
}

func decisionActionFieldWrite(t *testing.T, parsed recoveryPackage, node ast.Node) {
	t.Helper()
	var rendered bytes.Buffer
	if err := format.Node(&rendered, parsed.fileSet, node); err != nil {
		rendered.WriteString("<unprintable>")
	}
	position := parsed.fileSet.Position(node.Pos())
	t.Fatalf("Decision Action field write is outside PlanBetweenUnit derivation at %s:%d: %s", position.Filename, position.Line, rendered.String())
}

func isActionStepSlice(literal *ast.CompositeLit) bool {
	array, ok := literal.Type.(*ast.ArrayType)
	if !ok {
		return false
	}
	element, ok := array.Elt.(*ast.Ident)
	return ok && element.Name == "ActionStep"
}

func actionStepAppendArguments(parsed recoveryPackage, call *ast.CallExpr) []ast.Expr {
	appendName, ok := call.Fun.(*ast.Ident)
	if !ok || appendName.Name != "append" || len(call.Args) <= 1 {
		return nil
	}
	for _, argument := range call.Args[1:] {
		step, ok := argument.(*ast.Ident)
		if !ok || !strings.HasPrefix(step.Name, "Step") {
			continue
		}
		if _, ok := parsed.actionSteps[step.Name]; !ok {
			return []ast.Expr{argument}
		}
		return call.Args[1:]
	}
	return nil
}

type recoveryPackage struct {
	fileSet     *token.FileSet
	files       []*ast.File
	functions   map[string]*ast.FuncDecl
	actionKinds map[string]string
	actionSteps map[string]string
}

func parseRecoveryPackage(t *testing.T) recoveryPackage {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate PlanBetweenUnit coverage test source")
	}
	directory := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "recovery"))
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read internal/recovery: %v", err)
	}
	parsed := recoveryPackage{fileSet: token.NewFileSet(), functions: make(map[string]*ast.FuncDecl)}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		file, err := parser.ParseFile(parsed.fileSet, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		parsed.files = append(parsed.files, file)
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv != nil || function.Name == nil {
				continue
			}
			if parsed.functions[function.Name.Name] != nil {
				t.Fatalf("duplicate internal/recovery function %q", function.Name.Name)
			}
			parsed.functions[function.Name.Name] = function
		}
	}
	parsed.actionKinds = declaredStringConstants(t, parsed.files, "ActionKind")
	parsed.actionSteps = declaredStringConstants(t, parsed.files, "ActionStep")
	return parsed
}

func declaredStringConstants(t *testing.T, files []*ast.File, typeName string) map[string]string {
	t.Helper()
	values := make(map[string]string)
	for _, parsed := range files {
		for _, declaration := range parsed.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok || group.Tok != token.CONST {
				continue
			}
			for _, specification := range group.Specs {
				value, ok := specification.(*ast.ValueSpec)
				if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
					continue
				}
				declaredType, ok := value.Type.(*ast.Ident)
				if !ok || declaredType.Name != typeName {
					continue
				}
				literal, ok := value.Values[0].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("%s %q has non-string declaration", typeName, value.Names[0].Name)
				}
				unquoted, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatalf("unquote %s %q: %v", typeName, value.Names[0].Name, err)
				}
				values[value.Names[0].Name] = unquoted
			}
		}
	}
	return values
}

func decisionReturns(function *ast.FuncDecl) []ast.Expr {
	var results []ast.Expr
	ast.Inspect(function.Body, func(node ast.Node) bool {
		statement, ok := node.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		if len(statement.Results) != 1 {
			return true
		}
		results = append(results, statement.Results[0])
		return true
	})
	return results
}

func resolveDecisionExpression(t *testing.T, parsed recoveryPackage, function *ast.FuncDecl, expression ast.Expr, selected map[recovery.ActionKind]bool, visit func(string)) {
	t.Helper()
	switch value := expression.(type) {
	case *ast.CallExpr:
		callee, ok := value.Fun.(*ast.Ident)
		if !ok {
			unresolvableDecisionReturn(t, parsed, expression)
		}
		if callee.Name == "action" {
			if len(value.Args) < 2 {
				unresolvableDecisionReturn(t, parsed, expression)
			}
			kind, ok := value.Args[1].(*ast.Ident)
			if !ok {
				unresolvableDecisionReturn(t, parsed, expression)
			}
			kindValue, ok := parsed.actionKinds[kind.Name]
			if !ok {
				unresolvableDecisionReturn(t, parsed, expression)
			}
			selected[recovery.ActionKind(kindValue)] = true
			return
		}
		helper := parsed.functions[callee.Name]
		if helper == nil || !returnsDecision(helper) {
			unresolvableDecisionReturn(t, parsed, expression)
		}
		visit(callee.Name)
	case *ast.CompositeLit:
		resolveDecisionLiteral(t, parsed, value, selected)
	case *ast.Ident:
		construction, issue := decisionVariableConstruction(function, value.Name, expression.Pos())
		if issue != nil {
			rejectDecisionReturnShape(t, parsed, value.Name, issue)
		}
		if construction == nil {
			unresolvableDecisionReturn(t, parsed, expression)
		}
		resolveDecisionExpression(t, parsed, function, construction, selected, visit)
	default:
		unresolvableDecisionReturn(t, parsed, expression)
	}
}

func resolveDecisionLiteral(t *testing.T, parsed recoveryPackage, decision *ast.CompositeLit, selected map[recovery.ActionKind]bool) {
	t.Helper()
	decisionType, ok := decision.Type.(*ast.Ident)
	if !ok || decisionType.Name != "Decision" {
		unresolvableDecisionReturn(t, parsed, decision)
	}
	hasAction := false
	for _, element := range decision.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			unresolvableDecisionReturn(t, parsed, decision)
		}
		name, ok := field.Key.(*ast.Ident)
		if !ok || name.Name != "Action" {
			continue
		}
		hasAction = true
		action, ok := field.Value.(*ast.UnaryExpr)
		if !ok || action.Op != token.AND {
			unresolvableDecisionReturn(t, parsed, decision)
		}
		literal, ok := action.X.(*ast.CompositeLit)
		if !ok {
			unresolvableDecisionReturn(t, parsed, decision)
		}
		for _, actionElement := range literal.Elts {
			kindField, ok := actionElement.(*ast.KeyValueExpr)
			if !ok {
				unresolvableDecisionReturn(t, parsed, decision)
			}
			kindName, ok := kindField.Key.(*ast.Ident)
			if !ok || kindName.Name != "Kind" {
				continue
			}
			kind, ok := kindField.Value.(*ast.Ident)
			if !ok {
				unresolvableDecisionReturn(t, parsed, decision)
			}
			kindValue, ok := parsed.actionKinds[kind.Name]
			if !ok {
				unresolvableDecisionReturn(t, parsed, decision)
			}
			selected[recovery.ActionKind(kindValue)] = true
			return
		}
		unresolvableDecisionReturn(t, parsed, decision)
	}
	if !hasAction {
		return
	}
	unresolvableDecisionReturn(t, parsed, decision)
}

type decisionReturnShapeIssue struct {
	kind         string
	node         ast.Node
	construction ast.Expr
}

func decisionVariableConstruction(function *ast.FuncDecl, name string, returnedAt token.Pos) (ast.Expr, *decisionReturnShapeIssue) {
	scope := decisionReturnScope(function, returnedAt)
	if scope == nil {
		return nil, &decisionReturnShapeIssue{kind: "unresolvable", node: function}
	}
	var construction ast.Expr
	var issue *decisionReturnShapeIssue
	var constructionAssignment *ast.AssignStmt
	for _, statement := range decisionScopeStatements(scope) {
		assignment, ok := statement.(*ast.AssignStmt)
		if !ok || assignment.Pos() >= returnedAt {
			continue
		}
		for index, left := range assignment.Lhs {
			if identifier, ok := left.(*ast.Ident); ok && identifier.Name == name {
				if index >= len(assignment.Rhs) {
					issue = &decisionReturnShapeIssue{kind: "unresolvable", node: assignment}
					break
				}
				if construction != nil {
					issue = &decisionReturnShapeIssue{kind: "rebinding", node: assignment, construction: construction}
					break
				}
				construction = assignment.Rhs[index]
				constructionAssignment = assignment
				break
			}
		}
		if issue != nil {
			break
		}
	}
	if issue != nil || construction == nil {
		return construction, issue
	}
	ast.Inspect(scope, func(node ast.Node) bool {
		if issue != nil {
			return false
		}
		if _, ok := node.(*ast.FuncLit); ok {
			return false
		}
		assignment, ok := node.(*ast.AssignStmt)
		if ok && assignment != constructionAssignment && assignment.Pos() < returnedAt && assignment.Pos() > construction.Pos() {
			for index, left := range assignment.Lhs {
				if identifier, ok := left.(*ast.Ident); ok && identifier.Name == name {
					issue = &decisionReturnShapeIssue{kind: "rebinding", node: assignment, construction: construction}
					return false
				}
				if isActionKindWrite(left, name) {
					issue = &decisionReturnShapeIssue{kind: "write", node: assignment}
					return false
				}
				if index < len(assignment.Rhs) && isDecisionOrActionAlias(assignment.Rhs[index], name) {
					issue = &decisionReturnShapeIssue{kind: "alias", node: assignment}
					return false
				}
			}
		}
		value, ok := node.(*ast.ValueSpec)
		if !ok || value.Pos() >= returnedAt || value.Pos() <= construction.Pos() {
			return true
		}
		for _, expression := range value.Values {
			if isDecisionOrActionAlias(expression, name) {
				issue = &decisionReturnShapeIssue{kind: "alias", node: value}
				return false
			}
		}
		return true
	})
	return construction, issue
}

func decisionReturnScope(function *ast.FuncDecl, returnedAt token.Pos) ast.Node {
	var scope ast.Node
	ast.Inspect(function.Body, func(node ast.Node) bool {
		switch node.(type) {
		case *ast.BlockStmt, *ast.CaseClause, *ast.CommClause:
		default:
			return true
		}
		if node.Pos() > returnedAt || node.End() < returnedAt {
			return true
		}
		if scope == nil || node.Pos() >= scope.Pos() && node.End() <= scope.End() {
			scope = node
		}
		return true
	})
	return scope
}

func decisionScopeStatements(scope ast.Node) []ast.Stmt {
	switch value := scope.(type) {
	case *ast.BlockStmt:
		return value.List
	case *ast.CaseClause:
		return value.Body
	case *ast.CommClause:
		return value.Body
	default:
		return nil
	}
}

func isActionKindWrite(expression ast.Expr, name string) bool {
	kind, ok := expression.(*ast.SelectorExpr)
	if !ok || kind.Sel.Name != "Kind" {
		return false
	}
	action, ok := kind.X.(*ast.SelectorExpr)
	if !ok || action.Sel.Name != "Action" {
		return false
	}
	decision, ok := action.X.(*ast.Ident)
	return ok && decision.Name == name
}

func isDecisionOrActionAlias(expression ast.Expr, name string) bool {
	if decision, ok := expression.(*ast.Ident); ok {
		return decision.Name == name
	}
	action, ok := expression.(*ast.SelectorExpr)
	if !ok || action.Sel.Name != "Action" {
		return false
	}
	decision, ok := action.X.(*ast.Ident)
	return ok && decision.Name == name
}

func rejectDecisionReturnShape(t *testing.T, parsed recoveryPackage, name string, issue *decisionReturnShapeIssue) {
	t.Helper()
	switch issue.kind {
	case "write":
		postConstructionDecisionWriteReturn(t, parsed, name, issue.node)
	case "rebinding":
		ambiguousDecisionReturn(t, parsed, name, issue.construction, issue.node)
	case "alias":
		decisionReturnAlias(t, parsed, name, issue.node)
	default:
		unresolvableDecisionReturn(t, parsed, issue.node)
	}
}

func postConstructionDecisionWriteReturn(t *testing.T, parsed recoveryPackage, name string, node ast.Node) {
	t.Helper()
	var rendered bytes.Buffer
	if err := format.Node(&rendered, parsed.fileSet, node); err != nil {
		rendered.WriteString("<unprintable>")
	}
	position := parsed.fileSet.Position(node.Pos())
	t.Fatalf("Decision return variable %q has a post-construction Action.Kind write at %s:%d: %s", name, position.Filename, position.Line, rendered.String())
}

func ambiguousDecisionReturn(t *testing.T, parsed recoveryPackage, name string, construction ast.Expr, rebinding ast.Node) {
	t.Helper()
	var first, second bytes.Buffer
	if err := format.Node(&first, parsed.fileSet, construction); err != nil {
		first.WriteString("<unprintable>")
	}
	if err := format.Node(&second, parsed.fileSet, rebinding); err != nil {
		second.WriteString("<unprintable>")
	}
	firstPosition := parsed.fileSet.Position(construction.Pos())
	secondPosition := parsed.fileSet.Position(rebinding.Pos())
	t.Fatalf("Decision return variable %q has ambiguous constructions: first at %s:%d: %s; conflicting line at %s:%d: %s", name, firstPosition.Filename, firstPosition.Line, first.String(), secondPosition.Filename, secondPosition.Line, second.String())
}

func decisionReturnAlias(t *testing.T, parsed recoveryPackage, name string, node ast.Node) {
	t.Helper()
	var rendered bytes.Buffer
	if err := format.Node(&rendered, parsed.fileSet, node); err != nil {
		rendered.WriteString("<unprintable>")
	}
	position := parsed.fileSet.Position(node.Pos())
	t.Fatalf("Decision return variable %q takes a Decision or Action alias at %s:%d: %s", name, position.Filename, position.Line, rendered.String())
}

func unresolvableDecisionReturn(t *testing.T, parsed recoveryPackage, node ast.Node) {
	t.Helper()
	var rendered bytes.Buffer
	if err := format.Node(&rendered, parsed.fileSet, node); err != nil {
		rendered.WriteString("<unprintable>")
	}
	position := parsed.fileSet.Position(node.Pos())
	t.Fatalf("unresolvable Decision return path at %s:%d: %s", position.Filename, position.Line, rendered.String())
}

func returnsDecision(function *ast.FuncDecl) bool {
	if function.Type.Results == nil || len(function.Type.Results.List) != 1 {
		return false
	}
	result, ok := function.Type.Results.List[0].Type.(*ast.Ident)
	return ok && result.Name == "Decision"
}
