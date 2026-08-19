package recoveryexec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/recovery"
)

var stepDispatchedActionKinds = map[recovery.ActionKind]struct{}{
	recovery.ActionAppendAcceptanceFailure:    {},
	recovery.ActionAppendAcceptanceSuccess:    {},
	recovery.ActionRecoverIncompleteCriterion: {},
	recovery.ActionVerifyAcceptanceSubject:    {},
	recovery.ActionFailWorktreeLost:           {},
	recovery.ActionRecoverUnstartedAttempt:    {},
	recovery.ActionRecoverUnprobedAttempt:     {},
	recovery.ActionRecoverIncompleteAttempt:   {},
}

var continuationActionKinds = map[recovery.ActionKind]recovery.Continuation{
	recovery.ActionProceedAttempt:    recovery.ContinuationC2,
	recovery.ActionProceedAcceptance: recovery.ContinuationC3,
	recovery.ActionProceedScheduler:  recovery.ContinuationC4,
}

var preDispatchActionKinds = map[recovery.ActionKind]struct{}{
	recovery.ActionReclaimAuthority:    {},
	recovery.ActionRebuildFinalization: {},
}

var unitIdentifier = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

func TestRecoveryActionKindCompleteness(t *testing.T) {
	declared := actionKindsFromPlannerSource(t, filepath.Join("..", "recovery", "planner.go"))
	if len(declared) != 49 {
		t.Fatalf("planner ActionKind count = %d, want 49", len(declared))
	}

	deferrals := recovery.UnitOwnedDeferrals()
	if len(deferrals) != 0 {
		t.Fatalf("unit-owned deferral count = %d, want 0", len(deferrals))
	}
	deferred := make(map[recovery.ActionKind]string, len(deferrals))
	for _, deferral := range deferrals {
		if !unitIdentifier.MatchString(deferral.Unit) {
			t.Fatalf("unit-owned deferral %q owner = %q, want unit identifier such as 3.1", deferral.Kind, deferral.Unit)
		}
		if _, duplicate := deferred[deferral.Kind]; duplicate {
			t.Fatalf("unit-owned deferral %q has multiple owners", deferral.Kind)
		}
		deferred[deferral.Kind] = deferral.Unit
	}
	if len(stepDispatchedActionKinds) != 8 {
		t.Fatalf("step-dispatched action count = %d, want 8", len(stepDispatchedActionKinds))
	}
	if len(continuationActionKinds) != 3 {
		t.Fatalf("continuation action count = %d, want 3", len(continuationActionKinds))
	}
	if len(preDispatchActionKinds) != 2 {
		t.Fatalf("pre-dispatch action count = %d, want 2", len(preDispatchActionKinds))
	}

	handlers := defaultKinds()
	if len(handlers) != 36 {
		t.Fatalf("implemented defaultKinds count = %d, want 36", len(handlers))
	}

	bucketCounts := map[string]int{}
	for kind := range declared {
		buckets := make([]string, 0, 5)
		if _, ok := deferred[kind]; ok {
			buckets = append(buckets, "named owner")
		} else if _, ok := handlers[kind]; ok {
			buckets = append(buckets, "defaultKinds")
		}
		if _, ok := stepDispatchedActionKinds[kind]; ok {
			buckets = append(buckets, "Steps")
		}
		if continuation, ok := continuationActionKinds[kind]; ok {
			if !isContinuation(recovery.Action{Kind: kind, Continuation: continuation}) {
				t.Fatalf("ActionKind %q is classified as a continuation but isContinuation rejects it", kind)
			}
			buckets = append(buckets, "continuation")
		}
		if _, ok := preDispatchActionKinds[kind]; ok {
			buckets = append(buckets, "pre-dispatch special case")
		}
		switch len(buckets) {
		case 0:
			t.Fatalf("ActionKind %q is missing a classification bucket: defaultKinds, Steps, continuation, pre-dispatch special case, or named owner", kind)
		case 1:
			bucketCounts[buckets[0]]++
		default:
			t.Fatalf("ActionKind %q has multiple classification buckets: %v", kind, buckets)
		}
	}
	if bucketCounts["defaultKinds"] != 36 || bucketCounts["Steps"] != 8 || bucketCounts["continuation"] != 3 || bucketCounts["pre-dispatch special case"] != 2 || bucketCounts["named owner"] != 0 {
		t.Fatalf("classification counts = defaultKinds:%d Steps:%d continuation:%d pre-dispatch:%d named-owner:%d, want 36, 8, 3, 2, 0", bucketCounts["defaultKinds"], bucketCounts["Steps"], bucketCounts["continuation"], bucketCounts["pre-dispatch special case"], bucketCounts["named owner"])
	}
}

// actionKindsFromPlannerSource recognizes only explicit, typed string const
// declarations in planner.go. Recovery currently has one non-test source file;
// extending ActionKind declarations beyond that form or file requires widening
// this lock rather than treating it as package-wide coverage.
func actionKindsFromPlannerSource(t *testing.T, path string) map[recovery.ActionKind]struct{} {
	t.Helper()

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, contents, 0)
	if err != nil {
		t.Fatal(err)
	}

	kinds := make(map[recovery.ActionKind]struct{})
	for _, declaration := range file.Decls {
		constants, ok := declaration.(*ast.GenDecl)
		if !ok || constants.Tok != token.CONST {
			continue
		}
		for _, spec := range constants.Specs {
			values, ok := spec.(*ast.ValueSpec)
			if !ok {
				t.Fatalf("unexpected const spec %T", spec)
			}
			typeName, ok := values.Type.(*ast.Ident)
			if !ok || typeName.Name != "ActionKind" {
				continue
			}
			if len(values.Names) != len(values.Values) {
				t.Fatalf("ActionKind declaration has %d names and %d values", len(values.Names), len(values.Values))
			}
			for index, expression := range values.Values {
				literal, ok := expression.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("ActionKind %q must have a string literal value", values.Names[index].Name)
				}
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatalf("ActionKind %q literal %q: %v", values.Names[index].Name, literal.Value, err)
				}
				kind := recovery.ActionKind(value)
				if _, exists := kinds[kind]; exists {
					t.Fatalf("duplicate ActionKind value %q", kind)
				}
				kinds[kind] = struct{}{}
			}
		}
	}
	if len(kinds) == 0 {
		t.Fatalf("ActionKind extraction from %s produced no declarations", path)
	}
	return kinds
}
