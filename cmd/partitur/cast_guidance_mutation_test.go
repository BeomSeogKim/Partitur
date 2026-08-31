//go:build mutation

package main

import (
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/mutationtest"
)

func TestMutationBindingMissingGuidance(t *testing.T) {
	environment, err := mutationtest.SnapshotGoEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, source, before, after, packagePath, testName string
	}{
		{
			name:        "project discovery location",
			source:      "internal/validate/run.go",
			before:      ".partitur/cast.yaml (project)",
			after:       "the project cast",
			packagePath: "./internal/validate",
			testName:    "TestMissingBindingGuidance",
		},
		{
			name:        "user-global discovery location",
			source:      "internal/validate/run.go",
			before:      "~/.config/partitur/cast.yaml (user-global)",
			after:       "the user-global cast",
			packagePath: "./internal/validate",
			testName:    "TestMissingBindingGuidance",
		},
		{
			name:        "binding shape",
			source:      "internal/validate/run.go",
			before:      "bindings.<part>.performer must name an entry in performers",
			after:       "a binding is required",
			packagePath: "./internal/validate",
			testName:    "TestMissingBindingGuidance",
		},
		{
			name:        "guidance carried into the validation entry",
			source:      "internal/validate/run.go",
			before:      "if diagnostic.Rule == cast.RuleScore && diagnostic.Detail == \"binding_missing\" {",
			after:       "if false {\n\t\t_ = diagnostic.Detail",
			packagePath: "./internal/validate",
			testName:    "TestMissingBindingGuidance",
		},
		{
			name:        "guidance rendered by the CLI",
			source:      "cmd/partitur/main.go",
			before:      "if entry.Hint != \"\" {",
			after:       "if false {\n\t\t\t_ = entry.Hint",
			packagePath: "./cmd/partitur",
			testName:    "TestValidateBindingMissingRendersGuidance",
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
