package docmarker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendixB2AttemptStartedDecomposition(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	section := between(t, string(contents),
		"## B.2 Attempt lifecycle and performer selection",
		"## B.3 Evidence",
	)

	type annotationCarrier struct {
		original string
		carriers []string
	}
	inventory := []annotationCarrier{
		{
			original: "per-movement display ordinal — never the identifier (§6)",
			carriers: []string{
				"- `attempt.started.attempt_number` is the per-movement display ordinal.",
				"- `attempt.started.attempt_number` is never the attempt identifier (§6).",
			},
		},
		{
			original: "recorded BEFORE the trampoline's gate is released, so an",
			carriers: []string{
				"- `attempt.started.adapter_process` is recorded before the trampoline's gate is released.",
			},
		},
		{
			original: "unrecorded process can never have executed adapter code (§4)",
			carriers: []string{
				"- An adapter process absent from `attempt.started.adapter_process` can never have executed adapter\n  code (§4).",
			},
		},
		{
			original: "== pid; the trampoline is the session leader",
			carriers: []string{
				"- `attempt.started.adapter_process.session_id` equals `attempt.started.adapter_process.pid`.",
				"- The trampoline recorded by `attempt.started.adapter_process` is the session leader.",
			},
		},
		{
			original: "PGID is deliberately absent: §4 enumerates the recorded session and discovers its process groups at sweep time",
			carriers: []string{
				"- `attempt.started.adapter_process` deliberately has no `pgid` field.",
				"- At sweep time, §4 enumerates the recorded session and discovers its process groups.",
			},
		},
		{
			original: "movement_composition_dependency_hash (A.4); present iff the movement has dependencies. Journaled here because §5 records it and §9 checks it — a declared identity that is never persisted protects nothing. A dependency set with zero contributing change sets uses `composition_mode: identity`",
			carriers: []string{
				"- `attempt.started.base_composition_hash` is the `movement_composition_dependency_hash` (A.4).",
				"- `attempt.started.base_composition_hash` is present if and only if the movement has dependencies.",
				"- `attempt.started.base_composition_hash` is journaled because §5 records it and §9 checks it.",
				"- A declared composition identity that is never persisted protects nothing.",
				"- A dependency set with zero contributing change sets uses `composition_mode: identity`.",
			},
		},
		{
			original: "present iff this movement declares a review criterion (§2):",
			carriers: []string{
				"- `attempt.started.review_subject_input` is present if and only if the movement declares a review\n  criterion (§2).",
			},
		},
		{
			original: "the durable, raw-byte commitment to §4's reserved input; its path is derived from this envelope, not trusted input",
			carriers: []string{
				"- When present, `attempt.started.review_subject_input` is `{instance_id, hash}`, the durable raw-byte\n  commitment to §4's reserved input.",
				"- The reserved-input path is derived from the `attempt.started` envelope and is never trusted input.",
			},
		},
		{
			original: "exactly the wire `grants` object of §4",
			carriers: []string{
				"- `attempt.started.granted_authority` is exactly the wire `grants` object of §4.",
			},
		},
	}

	if len(inventory) != 9 {
		t.Fatalf("attempt.started annotation inventory = %d, want 9", len(inventory))
	}
	originals := make(map[string]struct{}, len(inventory))
	carriers := make(map[string]struct{})
	for _, item := range inventory {
		if item.original == "" || len(item.carriers) == 0 {
			t.Fatalf("incomplete annotation inventory item: %+v", item)
		}
		if _, duplicate := originals[item.original]; duplicate {
			t.Fatalf("duplicate original annotation %q", item.original)
		}
		originals[item.original] = struct{}{}
		for _, carrier := range item.carriers {
			if _, duplicate := carriers[carrier]; duplicate {
				t.Fatalf("carrier assigned twice: %q", carrier)
			}
			carriers[carrier] = struct{}{}
			requireOccurrence(t, section, carrier, 1)
		}
	}
	if len(carriers) != 17 {
		t.Fatalf("attempt.started prose carriers = %d, want 17", len(carriers))
	}

	wantSpecimen := `attempt.started {
  attempt_number,
  adapter_process: {
    pid,
    session_id,
    start_identity: (
        {platform: "linux",  boot_id, start_ticks}
      | {platform: "darwin", start_tvsec, start_tvusec}
    )
  },
  base_composition_hash?,
  review_subject_input?: {instance_id, hash},
  granted_authority: {
    paths_rw: [pattern], paths_ro: [pattern], shell: bool, network: bool
  },
  identity_versions
}`
	var specimens []string
	for _, specimen := range textFences(t, section) {
		if strings.HasPrefix(specimen, "attempt.started {\n") {
			specimens = append(specimens, specimen)
		}
	}
	if len(specimens) != 1 {
		t.Fatalf("standalone attempt.started specimens = %d, want 1", len(specimens))
	}
	if specimens[0] != wantSpecimen {
		t.Fatalf("attempt.started specimen =\n%s\nwant:\n%s", specimens[0], wantSpecimen)
	}
	if strings.Contains(specimens[0], "#") {
		t.Fatal("attempt.started specimen still carries an internal annotation")
	}
	topLevelPayloads := 0
	for _, line := range strings.Split(specimens[0], "\n") {
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(line, " {") {
			topLevelPayloads++
		}
	}
	if topLevelPayloads != 1 {
		t.Fatalf("attempt.started top-level payloads = %d, want 1", topLevelPayloads)
	}
	t.Logf("attempt.started decomposition: %d original annotations, %d prose carriers, one standalone comment-free specimen", len(inventory), len(carriers))
}

func textFences(t *testing.T, section string) []string {
	t.Helper()
	const open = "```text\n"
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
			t.Fatal("unclosed text fence")
		}
		fences = append(fences, section[:end])
		section = section[end+len(close):]
	}
}

func between(t *testing.T, document, start, end string) string {
	t.Helper()
	if strings.Count(document, start) != 1 || strings.Count(document, end) != 1 {
		t.Fatalf("section boundaries %q and %q must each occur once", start, end)
	}
	from := strings.Index(document, start)
	to := strings.Index(document[from:], end)
	if to <= 0 {
		t.Fatalf("section end %q does not follow %q", end, start)
	}
	return document[from : from+to]
}
