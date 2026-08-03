package runstore

import (
	"reflect"
	"testing"

	"github.com/BeomSeogKim/Partitur/internal/runstate"
)

func TestFindingPairsSortsOverrideReferences(t *testing.T) {
	got := findingPairs([]runstate.FindingReference{
		{ArtifactInstanceID: "findings@b", FindingID: "F-2"},
		{ArtifactInstanceID: "findings@a", FindingID: "F-2"},
		{ArtifactInstanceID: "findings@a", FindingID: "F-1"},
	})
	want := []any{
		map[string]any{"artifact_instance_id": "findings@a", "finding_id": "F-1"},
		map[string]any{"artifact_instance_id": "findings@a", "finding_id": "F-2"},
		map[string]any{"artifact_instance_id": "findings@b", "finding_id": "F-2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findingPairs() = %#v, want %#v", got, want)
	}
}
