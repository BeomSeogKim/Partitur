// Package examples keeps the shipped example scores honest: every directory
// here must still compile as a score and bind as a cast.
package examples

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	validation "github.com/BeomSeogKim/Partitur/internal/validate"
)

// TestExamplesValidate compiles each example score and resolves its cast
// against it, exactly as `partitur validate` does before it probes adapters.
// Probing is what needs a vendor CLI on PATH; compilation and binding do not.
func TestExamplesValidate(t *testing.T) {
	for _, name := range exampleDirectories(t) {
		t.Run(name, func(t *testing.T) {
			preparation, result := prepareExample(t, name)
			for _, entry := range result.Entries {
				if !entry.IsDiagnostic() {
					continue
				}
				t.Errorf("%s: %s rule=%q pointer=%q detail=%q",
					name, entry.Kind, entry.Rule, entry.Pointer, entry.Detail)
			}
			if result.Refusal != nil {
				t.Fatalf("%s: refusal %s: %s",
					name, result.Refusal.Kind, result.Refusal.Detail)
			}
			if preparation == nil {
				t.Fatalf("%s: no preparation", name)
			}
			if got := preparation.Score.Status(); got != "finalized" {
				t.Errorf("%s: status = %q, want finalized", name, got)
			}
			// Every binding carries fallbacks: a vendor capacity error must
			// not end the run. See examples/README.md, rule 4.
			for _, part := range preparation.Score.Parts() {
				binding, bound := preparation.Cast.Binding(part.ID)
				if !bound {
					t.Errorf("%s: part %q has no binding", name, part.ID)
					continue
				}
				if len(binding.Fallbacks) == 0 {
					t.Errorf("%s: binding %q declares no fallbacks",
						name, part.ID)
				}
			}
		})
	}
}

// prepareExample lays the example out the way the CLI discovers its inputs —
// partitur.yaml at the root, cast.yaml under .partitur/ — in a temporary
// repository, then runs the non-probing half of validation there.
func prepareExample(t *testing.T, name string) (*validation.Preparation, validation.Result) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".partitur"), 0o755); err != nil {
		t.Fatal(err)
	}
	copyFile(t, filepath.Join(name, "partitur.yaml"),
		filepath.Join(root, "partitur.yaml"))
	copyFile(t, filepath.Join(name, "cast.yaml"),
		filepath.Join(root, ".partitur", "cast.yaml"))
	// An empty HOME keeps a developer's user-global cast out of the result.
	t.Setenv("HOME", t.TempDir())
	t.Chdir(root)
	return validation.Prepare()
}

func copyFile(t *testing.T, source, destination string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// exampleDirectories returns every example directory, and fails if one is
// missing either file — a new shape is covered the moment it is added.
func exampleDirectories(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		for _, file := range []string{"partitur.yaml", "cast.yaml"} {
			path := filepath.Join(entry.Name(), file)
			if _, err := os.Stat(path); err != nil {
				t.Fatal(fmt.Errorf("example %s: %w", entry.Name(), err))
			}
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		t.Fatal("no example directories found")
	}
	return names
}
