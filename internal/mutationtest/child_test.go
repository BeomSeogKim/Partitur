//go:build mutation

package mutationtest

import (
	"context"
	"testing"
)

func TestRunRejectsMissingRequiredChildInput(t *testing.T) {
	for _, test := range []struct {
		name   string
		child  Child
		reason string
	}{
		{
			name: "empty target list",
			child: Child{
				Dir:         ".",
				Package:     ".",
				TestPattern: "TestRunRejectsMissingRequiredChildInput",
			},
			reason: "child has no targeted tests",
		},
		{
			name: "empty test pattern",
			child: Child{
				Dir:       ".",
				Package:   ".",
				TestNames: []string{"TestRunRejectsMissingRequiredChildInput"},
			},
			reason: "child has an empty test pattern",
		},
		{
			name: "empty package",
			child: Child{
				Dir:         ".",
				TestPattern: "TestRunRejectsMissingRequiredChildInput",
				TestNames:   []string{"TestRunRejectsMissingRequiredChildInput"},
			},
			reason: "child has an empty package",
		},
		{
			name: "empty working directory",
			child: Child{
				Package:     ".",
				TestPattern: "TestRunRejectsMissingRequiredChildInput",
				TestNames:   []string{"TestRunRejectsMissingRequiredChildInput"},
			},
			reason: "child has an empty working directory",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := Run(context.Background(), test.child)
			if result.Outcome != NonResult {
				t.Fatalf("Run() outcome = %q, want %q", result.Outcome, NonResult)
			}
			if result.Reason != test.reason {
				t.Fatalf("Run() reason = %q, want %q", result.Reason, test.reason)
			}
		})
	}
}
