package runstate

import "testing"

func TestInitialMovementStateRequiresFinalizedDraft(t *testing.T) {
	for _, test := range []struct {
		name, status, phase string
		want                MovementState
	}{
		{name: "draft status retains interview", status: "draft", phase: "draft", want: MovementPending},
		{name: "finalized status retains ordinary movement", status: "finalized", phase: "", want: MovementPending},
		{name: "finalized draft is inapplicable", status: "finalized", phase: "draft", want: MovementInapplicable},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := InitialMovementState(test.status, test.phase); got != test.want {
				t.Fatalf("InitialMovementState(%q, %q) = %s, want %s", test.status, test.phase, got, test.want)
			}
		})
	}
}
