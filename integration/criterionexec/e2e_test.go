package criterionexec_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/faultpoint"
	"github.com/BeomSeogKim/Partitur/internal/runstate"
	"github.com/BeomSeogKim/Partitur/internal/runstore"
)

const vendorEnvironment = "PARTITUR_CRITERIONEXEC_VENDOR"

func TestMain(m *testing.M) {
	if os.Getenv(vendorEnvironment) == "1" {
		runVendor()
		return
	}
	os.Exit(m.Run())
}

func TestHardRunCriterionEndToEnd(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	partitur := buildBinary(t, root, bin, "partitur")
	buildBinary(t, root, bin, "partitur-adapter-codex")
	buildBinary(t, root, bin, "partitur-trampoline")
	vendor, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	writeJSON(t, filepath.Join(repository, "partitur.yaml"), scoreFixture())
	writeJSON(t, filepath.Join(repository, ".partitur", "cast.yaml"), castFixture())
	runGit(t, repository, "init")
	runGit(t, repository, "config", "user.name", "Partitur Test")
	runGit(t, repository, "config", "user.email", "partitur@example.invalid")
	runGit(t, repository, "add", "partitur.yaml", ".partitur/cast.yaml")
	runGit(t, repository, "commit", "-m", "fixture")

	var stdout, stderr bytes.Buffer
	command := exec.Command(partitur, "run")
	command.Dir = repository
	command.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PARTITUR_CODEX_BIN="+vendor,
		vendorEnvironment+"=1",
	)
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("partitur run: %v stderr=%s", err, stderr.String())
	}
	runID := runstate.RunID(strings.TrimSpace(stdout.String()))
	if runID == "" || stderr.Len() != 0 {
		t.Fatalf("run output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	store, err := runstore.New(repository, faultpoint.Nop{})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := store.ReadJournal(runID)
	if err != nil {
		t.Fatal(err)
	}
	started, completed := false, false
	for _, event := range journal.Events {
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["criterion_id"] != "command-passes" {
			continue
		}
		switch event.Type {
		case runstate.EventCriterionStarted:
			if _, ok := payload["criterion_process"].(map[string]any); !ok || payload["spawn_failed"] != nil {
				t.Fatalf("run criterion start payload = %#v", payload)
			}
			started = true
		case runstate.EventCriterionCompleted:
			if payload["outcome"] != "PASS" || payload["output_ref"] == "" {
				t.Fatalf("run criterion completion payload = %#v", payload)
			}
			completed = true
		}
	}
	if !started || !completed {
		t.Fatalf("hard.run lifecycle started=%t completed=%t", started, completed)
	}
}

func runVendor() {
	for _, argument := range os.Args[1:] {
		if argument == "--version" {
			_, _ = os.Stdout.WriteString("codex 1.0.0\n")
			return
		}
	}
	prompt, err := os.ReadFile("/dev/stdin")
	if err != nil {
		os.Exit(91)
	}
	outputDirectory, artifactID := "", ""
	for _, line := range strings.Split(string(prompt), "\n") {
		if strings.HasPrefix(line, "- Writable artifact directory: ") {
			outputDirectory = strings.TrimSpace(strings.TrimPrefix(line, "- Writable artifact directory: "))
		}
		if strings.HasPrefix(line, "- artifact_id=\"") {
			artifactID, _, _ = strings.Cut(strings.TrimPrefix(line, "- artifact_id=\""), "\"")
		}
	}
	if outputDirectory == "" || artifactID == "" {
		os.Exit(92)
	}
	if err := os.WriteFile(filepath.Join(outputDirectory, "report.txt"), []byte("report\n"), 0o600); err != nil {
		os.Exit(93)
	}
	result, err := json.Marshal(map[string]any{
		"version":   1,
		"artifacts": []any{map[string]any{"artifact_id": artifactID, "path": "report.txt"}},
		"questions": []any{}, "proposal": nil, "summary": "completed",
	})
	if err != nil {
		os.Exit(94)
	}
	if err := os.WriteFile(filepath.Join(outputDirectory, "partitur-result.json"), result, 0o600); err != nil {
		os.Exit(95)
	}
}

func buildBinary(t *testing.T, root, bin, name string) string {
	t.Helper()
	output := filepath.Join(bin, name)
	command := exec.Command("go", "build", "-o", output, "./cmd/"+name)
	command.Dir = root
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, data)
	}
	return output
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, data)
	}
}

func scoreFixture() map[string]any {
	return map[string]any{
		"score": "0.2", "name": "criterion-exec", "revision": 1, "status": "finalized", "goal": "run a declared criterion",
		"verification": map[string]any{
			"expectation":    map[string]any{"intent": "pass-existing-tests", "apply_gate": map[string]any{"require": []any{"verified"}}},
			"final_movement": "inspect",
		},
		"parts": map[string]any{"reader": map[string]any{"capabilities": []any{"repo_read", "shell", "network"}, "read_only": true}},
		"movements": []any{map[string]any{
			"id": "inspect", "part": "reader", "grants": []any{"repo_read", "shell", "network"}, "instruction": "Write the declared report.",
			"outputs": []any{map[string]any{"id": "report", "kind": "artifact"}},
			"acceptance": map[string]any{"hard": []any{
				map[string]any{"id": "report-present", "artifact": "report"},
				map[string]any{"id": "command-passes", "run": []any{"true"}},
			}},
		}},
		"policy": map[string]any{"allowed_paths": []any{"**"}, "budget": map[string]any{"active_wall_clock_min": 10}},
	}
}

func castFixture() map[string]any {
	return map[string]any{
		"cast": "0.1", "performers": map[string]any{"worker": map[string]any{"adapter": "codex", "model": "fixture"}},
		"bindings": map[string]any{"reader": map[string]any{"performer": "worker"}},
	}
}
