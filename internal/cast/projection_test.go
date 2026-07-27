package cast

import (
	"bytes"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
)

func TestResolvedCastProjectionMaterializesDefaults(t *testing.T) {
	t.Parallel()
	resolved := mustResolve(t, Layer{
		Origin: "project",
		Data: encodeFixture(t, map[string]any{
			"cast": "0.1",
			"performers": map[string]any{
				"primary": map[string]any{
					"adapter": "codex",
					"model":   "model",
				},
				"backup": map[string]any{
					"adapter":                    "claude",
					"model":                      "other",
					"allow_advisory_enforcement": true,
					"extensions": map[string]any{
						"claude": map[string]any{"effort": "high"},
					},
				},
			},
			"bindings": map[string]any{
				"plan": map[string]any{
					"performer": "primary",
					"fallbacks": []any{"backup"},
				},
			},
		}),
	})
	projection, err := resolved.ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"bindings":{"plan":{"fallbacks":["backup"],"performer":"primary"}},"cast":"0.1","performers":{"backup":{"adapter":"claude","allow_advisory_enforcement":true,"extensions":{"claude":{"effort":"high"}},"model":"other"},"primary":{"adapter":"codex","allow_advisory_enforcement":false,"model":"model"}}}`
	if string(projection) != want {
		t.Fatalf("projection = %s, want %s", projection, want)
	}
	value, err := canonical.ParseJSON(projection)
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := canonical.Hash(canonical.DomainResolvedCast, value)
	if err != nil {
		t.Fatal(err)
	}
	gotHash, err := resolved.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if gotHash != wantHash {
		t.Fatalf("hash = %q, want %q", gotHash, wantHash)
	}

	projection[0] = '!'
	again, err := resolved.ProjectionBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, []byte(want)) {
		t.Fatal("projection bytes alias resolved cast state")
	}
}

func TestNilResolvedCastHasNoProjection(t *testing.T) {
	t.Parallel()
	var resolved *Cast
	if _, err := resolved.ProjectionBytes(); err == nil {
		t.Fatal("nil cast projection succeeded")
	}
	if _, err := resolved.Hash(); err == nil {
		t.Fatal("nil cast hash succeeded")
	}
}
