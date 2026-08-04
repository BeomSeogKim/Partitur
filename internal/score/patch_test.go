package score

import (
	"strings"
	"testing"
)

func TestApplyPatchRFC6902Operations(t *testing.T) {
	document := map[string]any{
		"array":  []any{"first", "third"},
		"object": map[string]any{"keep": "value", "copy": "before"},
	}
	operations := []any{
		map[string]any{"op": "add", "path": "/array/1", "value": "second"},
		map[string]any{"op": "replace", "path": "/object/keep", "value": "replaced"},
		map[string]any{"op": "copy", "from": "/object/keep", "path": "/object/copied"},
		map[string]any{"op": "move", "from": "/object/copy", "path": "/object/moved"},
		map[string]any{"op": "test", "path": "/array/2", "value": "third"},
		map[string]any{"op": "remove", "path": "/array/0"},
	}

	got, err := ApplyPatch(document, operations)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"array":  []any{"second", "third"},
		"object": map[string]any{"keep": "replaced", "copied": "replaced", "moved": "before"},
	}
	if !canonicalEqual(got, want) {
		t.Fatalf("patched value = %#v, want %#v", got, want)
	}
	if !canonicalEqual(document, map[string]any{"array": []any{"first", "third"}, "object": map[string]any{"keep": "value", "copy": "before"}}) {
		t.Fatalf("ApplyPatch mutated input: %#v", document)
	}
}

func TestApplyPatchRejectsUndefinedArrayForms(t *testing.T) {
	for _, operation := range []map[string]any{
		{"op": "add", "path": "/array/-1", "value": "x"},
		{"op": "replace", "path": "/array/01", "value": "x"},
		{"op": "add", "path": "/array/+1", "value": "x"},
		{"op": "remove", "path": "/array/-"},
	} {
		_, err := ApplyPatch(map[string]any{"array": []any{"x"}}, []any{operation})
		if err == nil {
			t.Fatalf("operation %#v succeeded", operation)
		}
	}
}

func TestApplyPatchRejectsMoveIntoChild(t *testing.T) {
	_, err := ApplyPatch(map[string]any{
		"array": []any{map[string]any{"source": "value"}, map[string]any{}},
	}, []any{map[string]any{
		"op": "move", "from": "/array/0", "path": "/array/0/child",
	}})
	if err == nil || !strings.Contains(err.Error(), "move destination is within source") {
		t.Fatalf("error = %v, want move-into-child rejection", err)
	}
}

func TestApplyPatchRejectsLeadingZeroArrayIndex(t *testing.T) {
	_, err := ApplyPatch(map[string]any{"array": []any{"x"}}, []any{
		map[string]any{"op": "add", "path": "/array/01", "value": "y"},
	})
	if err == nil {
		t.Fatal("add accepted leading-zero array index")
	}
}

func TestApplyPatchTestFailureDoesNotContinue(t *testing.T) {
	_, err := ApplyPatch(map[string]any{"value": "before"}, []any{
		map[string]any{"op": "test", "path": "/value", "value": "other"},
		map[string]any{"op": "replace", "path": "/value", "value": "after"},
	})
	if err == nil || !strings.Contains(err.Error(), "test value does not match") {
		t.Fatalf("error = %v, want failed test", err)
	}
}

func TestApplyPatchMoveToSameLocationIsUnchanged(t *testing.T) {
	document := map[string]any{"array": []any{"first", "second"}}
	got, err := ApplyPatch(document, []any{map[string]any{"op": "move", "from": "/array/1", "path": "/array/1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !canonicalEqual(got, document) {
		t.Fatalf("same-location move = %#v, want %#v", got, document)
	}
}
