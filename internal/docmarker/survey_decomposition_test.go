package docmarker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
)

type surveyBlockExpectation struct {
	name               string
	start              string
	end                string
	language           string
	prefix             string
	carrierHash        string
	carrierAssignments []int
	specimenHash       string
}

func TestSurveyFenceDecomposition(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	inventory := surveyOriginalAnnotationInventory(t)

	blocks := []surveyBlockExpectation{
		{
			name: "owned-git-refs", start: "**Owned Git-ref clauses.**", end: "**Everything the run will need later is pinned",
			language: "text", prefix: "refs/partitur/runs/<run-id>/base\n",
			carrierHash: "7e8f6858a20bbc391a7a34ae6dc3f35dde35edd9d3e083bd79e1fc01b3adfc27", carrierAssignments: []int{1, 1, 1, 1, 1}, specimenHash: "a91f892caa5d149ab4be0157442154fa0822786650aa65ce63a39e93b0c74f9a",
		},
		{
			name: "may-propose", start: "**`may_propose` example field clauses.**", end: "**A non-blocking proposal is advisory and expires.**",
			language: "yaml", prefix: "  - id: design\n",
			carrierHash: "09854c7647594a37c96794aebb17d58abfbe246eab1768fb3c8436c6b5239ac6", carrierAssignments: []int{4}, specimenHash: "02d654450406cd0e82578185b026d6330d94fe24eebcea126a203e0f91c09ec5",
		},
		{
			name: "inapplicable-state", start: "**`INAPPLICABLE` state clauses.**", end: "Plus two **per-run projections on separate axes**",
			language: "text", prefix: "Run:      RUNNING | WAITING_HUMAN",
			carrierHash: "dc4996ef68e70834080c1c4f8763152815e270605c74703fffc5db0754c476fa", carrierAssignments: []int{2}, specimenHash: "38f8d396011d914f77f6c2378bab5eded32f2ea81bac8bf58b5ecef510d58e64",
		},
		{
			name: "cli-surface", start: "**CLI operand and option clauses.**", end: "Every command is **non-interactive**",
			language: "text", prefix: "partitur init\n",
			carrierHash: "0b94cf1b5c5d839cae9d283e016cb4f3e8bf39bd3bb5d3487e967b4f96ce613b", carrierAssignments: []int{1, 1, 1, 1}, specimenHash: "d3713a1c62ad3035c9de3045539606d6704ac8fb8c9d79bc55b225419d0c2713",
		},
		{
			name: "acceptance-spec", start: "**Acceptance-spec field clauses.**", end: "Hashing criterion *hashes* rather than inlining their bodies",
			language: "text", prefix: "{\n  hard:   [criterion_spec_hash],\n",
			carrierHash: "bc407c10819def20911f4bd3cfdca6aeb0537e897893847f4a96203881b4a6e1", carrierAssignments: []int{1, 1, 1}, specimenHash: "236acfab37e5d20745e09bbb17f286e91f9533eaafa585411eda41c5202b4ac0",
		},
		{
			name: "disposition", start: "**Disposition field clauses.**", end: "`charged: \"none\"` with `movement_terminal: true`",
			language: "text", prefix: "disposition: {\n",
			carrierHash: "567eec2143de0e112b14f85f4dd449a5342ed1686e1759c32ffa228da4426530", carrierAssignments: []int{1, 2}, specimenHash: "76111bd88317b94d53957f4d68b4f887dd779b0a972d42706ce86c908fae9cae",
		},
	}
	if len(inventory) != len(blocks) {
		t.Fatalf("original annotation inventory blocks = %d, decomposition blocks = %d", len(inventory), len(blocks))
	}

	for _, block := range blocks {
		t.Run(block.name, func(t *testing.T) {
			section := between(t, string(contents), block.start, block.end)
			originals, found := inventory[block.name]
			if !found {
				t.Fatalf("original annotation inventory has no block %q", block.name)
			}
			if len(originals) != len(block.carrierAssignments) {
				t.Fatalf("original annotation runs = %d, assignments = %d", len(originals), len(block.carrierAssignments))
			}

			fenceStart := strings.Index(section, "```"+block.language+"\n")
			if fenceStart < 0 {
				t.Fatalf("%s specimen is absent", block.language)
			}
			carrierText := section[:fenceStart]
			if got := sha256Text(carrierText); got != block.carrierHash {
				t.Fatalf("carrier bytes hash = %s, want %s", got, block.carrierHash)
			}
			carriers := markdownBullets(carrierText)
			assigned := 0
			for index, count := range block.carrierAssignments {
				if count <= 0 || assigned+count > len(carriers) {
					t.Fatalf("original annotation run %d has invalid carrier assignment %d", index+1, count)
				}
				assigned += count
			}
			if assigned != len(carriers) {
				t.Fatalf("assigned carriers = %d, resulting carriers = %d", assigned, len(carriers))
			}

			fences := languageFences(t, section, block.language)
			if len(fences) != 1 {
				t.Fatalf("top-level payloads = %d, want 1", len(fences))
			}
			specimen := fences[0]
			if !strings.HasPrefix(specimen, block.prefix) {
				t.Fatalf("specimen prefix = %q, want %q", specimen, block.prefix)
			}
			if count := strings.Count(specimen, "#"); count != 0 {
				t.Fatalf("comment markers = %d, want 0", count)
			}
			if got := sha256Text(specimen); got != block.specimenHash {
				t.Fatalf("specimen bytes hash = %s, want %s", got, block.specimenHash)
			}
			if block.language == "yaml" {
				if _, err := canonical.ParseYAML([]byte(specimen)); err != nil {
					t.Fatalf("specimen is not one safe YAML document: %v", err)
				}
			}
			t.Logf("%s decomposition: %d original annotation runs, %d prose carriers, 1 independently copyable top-level payload, 0 comment markers",
				block.name, len(originals), len(carriers))
		})
	}
}

func surveyOriginalAnnotationInventory(t *testing.T) map[string][]string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("testdata", "survey_original_annotations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sha256Text(string(contents)), "673c28b4d632793176b4f99d14cc91df5cda40accbb2e8e02f504204393e3f13"; got != want {
		t.Fatalf("original annotation inventory bytes hash = %s, want %s", got, want)
	}
	var inventory map[string][]string
	if err := json.Unmarshal(contents, &inventory); err != nil {
		t.Fatal(err)
	}
	for name, runs := range inventory {
		if len(runs) == 0 {
			t.Fatalf("annotation inventory block %q is empty", name)
		}
		for index, run := range runs {
			if run == "" || !strings.Contains(run, "#") {
				t.Fatalf("annotation inventory block %q run %d is not an exact comment-bearing run", name, index+1)
			}
		}
	}
	return inventory
}
