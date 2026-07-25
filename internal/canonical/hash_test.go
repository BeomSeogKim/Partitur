package canonical

import (
	"errors"
	"testing"
)

func TestDomainRegistryConstants(t *testing.T) {
	tests := []struct {
		domain  Domain
		name    string
		version int
	}{
		{DomainScore, "partitur/score", ProjectionVersionScore},
		{DomainScoreSubtree, "partitur/score-subtree", ProjectionVersionScoreSubtree},
		{DomainResolvedCast, "partitur/resolved-cast", ProjectionVersionResolvedCast},
		{DomainCriterionSpec, "partitur/criterion-spec", ProjectionVersionCriterionSpec},
		{DomainAcceptanceSpec, "partitur/acceptance-spec", ProjectionVersionAcceptanceSpec},
		{DomainChangeSet, "partitur/change-set", ProjectionVersionChangeSet},
		{DomainCandidate, "partitur/candidate", ProjectionVersionCandidate},
		{DomainCandidateComposition, "partitur/candidate-composition", ProjectionVersionCandidateComposition},
		{DomainMovementComposition, "partitur/movement-composition", ProjectionVersionMovementComposition},
		{DomainCompositionEnvironment, "partitur/composition-environment", ProjectionVersionCompositionEnvironment},
		{DomainCompositionSubject, "partitur/composition-subject", ProjectionVersionCompositionSubject},
		{DomainExecutionDependency, "partitur/execution-dependency", ProjectionVersionExecutionDependency},
		{DomainPatchOperations, "partitur/patch-operations", ProjectionVersionPatchOperations},
		{DomainResolutionBody, "partitur/resolution-body", ProjectionVersionResolutionBody},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if string(test.domain) != test.name {
				t.Fatalf("domain = %q, want %q", test.domain, test.name)
			}
			versions, err := CurrentVersions(test.domain)
			if err != nil {
				t.Fatal(err)
			}
			if versions.CanonicalEncoding != CanonicalEncodingVersion ||
				versions.Projection != test.version {
				t.Fatalf("versions = %+v", versions)
			}
		})
	}
}

func TestHashConstruction(t *testing.T) {
	hash, err := Hash(DomainScore, map[string]any{"x": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	const expected = "sha256:7168df783e53fbad780d0032bd1e21d4c6e192f1ae9deb78f304d6c9d96a13b9"
	if hash != expected {
		t.Fatalf("hash = %q, want %q", hash, expected)
	}
}

func TestHashSeparatesDomainsAndProjectionVersions(t *testing.T) {
	value := map[string]any{"same": true}
	scoreHash, err := Hash(DomainScore, value)
	if err != nil {
		t.Fatal(err)
	}
	subtreeHash, err := Hash(DomainScoreSubtree, value)
	if err != nil {
		t.Fatal(err)
	}
	if scoreHash == subtreeHash {
		t.Fatalf("domains collided: %s", scoreHash)
	}

	versionOne, err := hashPreimage(DomainScore, Versions{CanonicalEncoding: 1, Projection: 1}, value)
	if err != nil {
		t.Fatal(err)
	}
	versionTwo, err := hashPreimage(DomainScore, Versions{CanonicalEncoding: 1, Projection: 2}, value)
	if err != nil {
		t.Fatal(err)
	}
	if versionOne == versionTwo {
		t.Fatalf("projection versions collided: %s", versionOne)
	}
}

func TestHistoricalHashRecomputesRecordedTuple(t *testing.T) {
	value := []any{"value"}
	current, err := Hash(DomainCandidate, value)
	if err != nil {
		t.Fatal(err)
	}
	recomputed, err := hashWithVersions(
		DomainCandidate,
		Versions{CanonicalEncoding: 1, Projection: 1},
		value,
	)
	if err != nil {
		t.Fatal(err)
	}
	if recomputed != current {
		t.Fatalf("recomputed = %q, current = %q", recomputed, current)
	}
}

func TestHistoricalHashFailsClosed(t *testing.T) {
	tests := []Versions{
		{CanonicalEncoding: 2, Projection: 1},
		{CanonicalEncoding: 1, Projection: 2},
		{CanonicalEncoding: 0, Projection: 0},
	}
	for _, versions := range tests {
		if _, err := hashWithVersions(DomainScore, versions, nil); !errors.Is(err, ErrUnsupportedRunFormat) {
			t.Fatalf("versions %+v: error = %v", versions, err)
		}
	}
}

func TestHashRejectsUnknownDomain(t *testing.T) {
	if _, err := Hash(Domain("partitur/not-registered"), nil); !errors.Is(err, ErrUnknownDomain) {
		t.Fatalf("error = %v", err)
	}
}
