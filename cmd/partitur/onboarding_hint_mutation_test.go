//go:build mutation

package main

import (
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationOnboardingHints(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, source, before, after, packagePath, testName string
	}{
		{
			name:        "executable absent hint assignment",
			source:      "internal/validate/run.go",
			before:      "entry.Hint = installHint",
			after:       "_ = installHint",
			packagePath: "./cmd/partitur",
			testName:    "TestValidateAdapterAbsentRendersInstallHint",
		},
		{
			name:        "enforcement refused hint assignment",
			source:      "internal/validate/run.go",
			before:      "entry.Hint = enforcementHint",
			after:       "_ = enforcementHint",
			packagePath: "./cmd/partitur",
			testName:    "TestValidateEnforcementRefusedRendersAdvisoryHint",
		},
		{
			name:        "trampoline install remediation",
			source:      "internal/driver/driver.go",
			before:      `return "", fmt.Errorf("resolve partitur-trampoline: %w; hint: %s", err, trampolineInstallHint)`,
			after:       `return "", fmt.Errorf("resolve partitur-trampoline: %w", err)`,
			packagePath: "./internal/driver",
			testName:    "TestResolveTrampolineAbsentExplainsInstall",
		},
		{
			name:        "CLI hint render",
			source:      "cmd/partitur/main.go",
			before:      `fmt.Fprintf(w, " hint=%q", entry.Hint)`,
			after:       `_ = entry.Hint`,
			packagePath: "./cmd/partitur",
			testName:    "TestValidateAdapterAbsentRendersInstallHint",
		},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			result := assertPrepareQuiesceMutationKilled(
				t,
				environment,
				mutation.source,
				mutation.before,
				mutation.after,
				mutation.packagePath,
				mutation.testName,
			)
			if result.Outcome != mutationtest.Killed {
				t.Fatalf("mutation non-result: %s\n%s", result.Reason, result.Diagnostic())
			}
		})
	}
}
