package docmarker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
)

type remainingSpecimenExpectation struct {
	language string
	prefix   string
	hash     string
}

type remainingBlockExpectation struct {
	name               string
	start              string
	end                string
	carrierMarker      string
	carrierHash        string
	carrierAssignments []int
	specimens          []remainingSpecimenExpectation
}

func TestRemainingFenceDecomposition(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	inventory := remainingOriginalAnnotationInventory(t)

	blocks := []remainingBlockExpectation{
		{
			name:          "repository-layout",
			start:         "**Repository-layout clauses.**",
			end:           "Git refs the core owns — never user-visible branches",
			carrierMarker: "**Repository-layout clauses.**",
			carrierHash:   "977875d71f10194b31881a0390534872c912e9079f1106dd50526f55bd2c638e",
			carrierAssignments: []int{
				2, 3, 2, 1, 3, 2, 5, 1, 4, 5, 1, 1, 3, 5, 2, 2, 7, 1, 1, 1, 1, 2, 3, 1, 1,
			},
			specimens: []remainingSpecimenExpectation{
				{language: "text", prefix: "<repo>/\n", hash: "e9d8317897c69d1389551fa42b898e858be535750437a0afc9f103f007197847"},
				{language: "text", prefix: "~/.config/partitur/cast.yaml", hash: "64a8522db5b9a0a0acbb23479be84849f40b636fef757a0e0d46c745051f5ad3"},
			},
		},
		{
			name:          "score-example",
			start:         "**Score-example field clauses.**",
			end:           "**Path policy semantics.**",
			carrierMarker: "**Score-example field clauses.**",
			carrierHash:   "9388db72963a7256d59fccf27aa1589e1240dd04a6b6a4dfd136b36bf594ca1e",
			carrierAssignments: []int{
				1, 1, 1, 2, 2, 1, 2, 1, 2, 1, 5, 2, 3, 2, 7, 2, 1, 3, 1,
				1, 3, 1, 1, 3, 2, 2, 1, 1, 1, 1, 1, 1, 1, 4, 2, 5, 1, 4,
			},
			specimens: []remainingSpecimenExpectation{
				{language: "yaml", prefix: "score: \"0.2\"\n", hash: "5b3f5c65fc2c32177336e28cc38cdc07600a84e3810fe6bf536fa2275ac1fa7c"},
			},
		},
		{
			name:          "adapter-methods",
			start:         "**Adapter-method field clauses.**",
			end:           "**Validation probing.**",
			carrierMarker: "**Adapter-method field clauses.**",
			carrierHash:   "63d368bbac94db68bf91f9d4e241b92f394bde40a77471dc5f2680f119ece6fb",
			carrierAssignments: []int{
				7, 3, 1, 1, 1, 2, 1, 1, 1, 2, 2, 1, 4, 1, 1, 3, 1, 1, 1, 2, 2,
				2, 2, 1, 1, 2, 5, 1, 1, 3, 3,
			},
			specimens: []remainingSpecimenExpectation{
				{language: "text", prefix: "probe() -> {\n", hash: "6b3a469e2260c0ff61eba49c64e199045008154f49aa6dca160e8dc3c59ac2bb"},
				{language: "text", prefix: "execute(request) -> streams `event` notifications", hash: "211c14a07c39e6f5943491fb634db41d76ed4519383c3e21d7d9d97f75159053"},
				{language: "text", prefix: "cancel(attempt_id) ->", hash: "e0a1ee505422a22289df57eb0662c372c5b1a485f4d96effe5dd4c1c33d2a472"},
			},
		},
		{
			name:               "event-notifications",
			start:              "**Event-notification field clauses.**",
			end:                "Code changes are never communicated as `artifact` events",
			carrierMarker:      "**Event-notification field clauses.**",
			carrierHash:        "469c5e4b65987886f5ee1a0a39dda289db045ad4c27a887897adfc5ce40ef98a",
			carrierAssignments: []int{5, 1, 2, 1},
			specimens: []remainingSpecimenExpectation{
				{language: "text", prefix: "log {", hash: "da6b056b719b96a35d46bdcdf085db33189b705ba6b29d4487983e262e5c03b3"},
				{language: "text", prefix: "progress {", hash: "eed2e976cc47077f22e1f5fe72bd464e57f0652fd59bc9c0026a97394d2c3e81"},
				{language: "text", prefix: "artifact {", hash: "dca9f393ce6a517b5495b51ce1fe074f31f718bb0cfc80721160921d9142b1e3"},
				{language: "text", prefix: "proposal {", hash: "a8cab1df96b74ce4fa52a72986c4120fc3b2431125d6ab49c4386caf6b783a8a"},
				{language: "text", prefix: "question {", hash: "9e1122bc693350906450a36c94559221c3fc5d3cb42592491c416124ca860dc5"},
			},
		},
		{
			name:               "reserved-inputs",
			start:              "**Reserved-input field clauses.**",
			end:                "**Review-subject publication.**",
			carrierMarker:      "**Reserved-input field clauses.**",
			carrierHash:        "f0d93a7a46f87def6910c76a1a8bb94d24768cf84dba5c4ef8af98425f77a313",
			carrierAssignments: []int{1, 1, 1, 3, 1, 1, 1, 3},
			specimens: []remainingSpecimenExpectation{
				{language: "text", prefix: "artifact_id: partitur.score-base\n", hash: "f7020681f700501152537c9a2fa4ae499ed51a40f44a1e35627c045bb52a608f"},
				{language: "text", prefix: "artifact_id: partitur.subject-tree\n", hash: "e0c33bb7d4e547a49fb27e6962288b5dc1a57c6137dedf0c6a31dacd1e5bd7a5"},
			},
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
			seenOriginals := make(map[string]struct{}, len(originals))
			for _, original := range originals {
				if original == "" {
					t.Fatal("original annotation inventory contains an empty run")
				}
				if _, duplicate := seenOriginals[original]; duplicate {
					t.Fatalf("original annotation run assigned twice: %q", original)
				}
				seenOriginals[original] = struct{}{}
			}

			if len(block.specimens) == 0 {
				t.Fatal("block has no expected specimens")
			}
			requireOccurrence(t, section, block.carrierMarker, 1)
			carrierEnd := strings.Index(section, "```"+block.specimens[0].language+"\n")
			if carrierEnd <= 0 {
				t.Fatalf("carrier marker %q has no adjacent specimen", block.carrierMarker)
			}
			carrierText := section[:carrierEnd]
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
			seenCarriers := make(map[string]struct{}, len(carriers))
			for _, carrier := range carriers {
				if _, duplicate := seenCarriers[carrier]; duplicate {
					t.Fatalf("carrier assigned twice: %q", carrier)
				}
				seenCarriers[carrier] = struct{}{}
			}

			language := block.specimens[0].language
			for _, expected := range block.specimens {
				if expected.language != language {
					t.Fatal("one decomposition block uses multiple fence languages")
				}
			}
			fences := languageFences(t, section, language)
			if len(fences) != len(block.specimens) {
				t.Fatalf("top-level payloads = %d, want %d", len(fences), len(block.specimens))
			}

			seenSpecimens := make(map[string]struct{}, len(block.specimens))
			for _, expected := range block.specimens {
				var matches []string
				for _, specimen := range fences {
					if strings.HasPrefix(specimen, expected.prefix) {
						matches = append(matches, specimen)
					}
				}
				if len(matches) != 1 {
					t.Fatalf("specimens with prefix %q = %d, want 1", expected.prefix, len(matches))
				}
				if _, duplicate := seenSpecimens[expected.prefix]; duplicate {
					t.Fatalf("duplicate specimen prefix %q", expected.prefix)
				}
				seenSpecimens[expected.prefix] = struct{}{}
				if count := strings.Count(matches[0], "#"); count != 0 {
					t.Fatalf("specimen %q comment-marker count = %d, want 0", expected.prefix, count)
				}
				if got := sha256Text(matches[0]); got != expected.hash {
					t.Fatalf("specimen %q bytes hash = %s, want %s", expected.prefix, got, expected.hash)
				}
				if expected.language == "yaml" {
					if _, err := canonical.ParseYAML([]byte(matches[0])); err != nil {
						t.Fatalf("specimen %q is not one safe YAML document: %v", expected.prefix, err)
					}
				}
			}
			if len(seenSpecimens) != len(block.specimens) {
				t.Fatalf("top-level payloads = %d, want %d", len(seenSpecimens), len(block.specimens))
			}
			t.Logf("%s decomposition: %d original annotation runs, %d prose carriers, %d independently copyable top-level payloads, 0 comment markers",
				block.name, len(originals), len(carriers), len(seenSpecimens))
		})
	}
}

func remainingOriginalAnnotationInventory(t *testing.T) map[string][]string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("testdata", "remaining_original_annotations.txt"))
	if err != nil {
		t.Fatal(err)
	}
	inventory := make(map[string][]string)
	block := ""
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			block = strings.TrimSuffix(strings.TrimPrefix(line, "["), "]")
			if _, duplicate := inventory[block]; duplicate {
				t.Fatalf("duplicate annotation inventory block %q", block)
			}
			inventory[block] = nil
			continue
		}
		if line == "" {
			continue
		}
		if block == "" {
			t.Fatalf("annotation outside an inventory block: %q", line)
		}
		inventory[block] = append(inventory[block], line)
	}
	return inventory
}

func languageFences(t *testing.T, section, language string) []string {
	t.Helper()
	open := "```" + language + "\n"
	const close = "\n```"
	var fences []string
	for {
		start := strings.Index(section, open)
		if start < 0 {
			return fences
		}
		section = section[start+len(open):]
		end := strings.Index(section, close)
		if end < 0 {
			t.Fatalf("unclosed %s fence", language)
		}
		fences = append(fences, section[:end])
		section = section[end+len(close):]
	}
}
