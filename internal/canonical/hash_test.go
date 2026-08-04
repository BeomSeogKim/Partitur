package canonical

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
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

func TestProjectionVersionsMatchDesignA3(t *testing.T) {
	declared := projectionVersionsFromDesignA3(t)
	for _, test := range []struct {
		domain  Domain
		version int
	}{
		{DomainScore, ProjectionVersionScore},
		{DomainScoreSubtree, ProjectionVersionScoreSubtree},
		{DomainResolvedCast, ProjectionVersionResolvedCast},
		{DomainCriterionSpec, ProjectionVersionCriterionSpec},
		{DomainAcceptanceSpec, ProjectionVersionAcceptanceSpec},
		{DomainChangeSet, ProjectionVersionChangeSet},
		{DomainCandidate, ProjectionVersionCandidate},
		{DomainCandidateComposition, ProjectionVersionCandidateComposition},
		{DomainMovementComposition, ProjectionVersionMovementComposition},
		{DomainCompositionEnvironment, ProjectionVersionCompositionEnvironment},
		{DomainCompositionSubject, ProjectionVersionCompositionSubject},
		{DomainExecutionDependency, ProjectionVersionExecutionDependency},
		{DomainPatchOperations, ProjectionVersionPatchOperations},
		{DomainResolutionBody, ProjectionVersionResolutionBody},
	} {
		want, ok := declared[test.domain]
		if !ok {
			t.Fatalf("A.3 declares no projection version for domain %q", test.domain)
		}
		if test.version != want {
			t.Fatalf("projection version for domain %q = %d, want A.3 value %d", test.domain, test.version, want)
		}
	}
}

var a3ProjectionVersionRow = regexp.MustCompile("(?m)^\\| per-domain `projection_version` \\| .+ \\| ([0-9]+), except (.+) \\|$")
var a3ProjectionVersionException = regexp.MustCompile("`([^`]+)`: ([0-9]+)")

// projectionVersionsFromDesignA3 recognizes A.3's one default projection
// version plus inline domain exceptions. It does not understand a per-domain
// table or exceptions expressed outside that row; widen this lock if A.3 takes
// either form rather than treating absence of parsed versions as agreement.
func projectionVersionsFromDesignA3(t *testing.T) map[Domain]int {
	t.Helper()

	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "DESIGN.md"))
	if err != nil {
		t.Fatal(err)
	}
	const heading = "## A.3 Independent versions"
	if count := strings.Count(string(contents), heading); count != 1 {
		t.Fatalf("A.3 heading count = %d, want 1", count)
	}
	section := string(contents)[strings.Index(string(contents), heading):]
	if next := strings.Index(section[len(heading):], "\n## "); next >= 0 {
		section = section[:len(heading)+next]
	}

	match := a3ProjectionVersionRow.FindStringSubmatch(section)
	if match == nil {
		t.Fatal("A.3 projection_version row is absent or not in the default-plus-inline-exception form")
	}
	defaultVersion, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatal(err)
	}
	versions := map[Domain]int{}
	for _, domain := range []Domain{
		DomainScore,
		DomainScoreSubtree,
		DomainResolvedCast,
		DomainCriterionSpec,
		DomainAcceptanceSpec,
		DomainChangeSet,
		DomainCandidate,
		DomainCandidateComposition,
		DomainMovementComposition,
		DomainCompositionEnvironment,
		DomainCompositionSubject,
		DomainExecutionDependency,
		DomainPatchOperations,
		DomainResolutionBody,
	} {
		versions[domain] = defaultVersion
	}
	for _, exception := range a3ProjectionVersionException.FindAllStringSubmatch(match[2], -1) {
		domain := Domain(exception[1])
		if _, ok := versions[domain]; !ok {
			t.Fatalf("A.3 projection_version exception domain %q is unregistered", domain)
		}
		exceptionVersion, err := strconv.Atoi(exception[2])
		if err != nil {
			t.Fatal(err)
		}
		versions[domain] = exceptionVersion
	}
	if len(versions) == 0 {
		t.Fatal("A.3 projection_version extraction produced no versions")
	}
	return versions
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

func TestExecutionDependencyHashChangesWithProjectionVersion(t *testing.T) {
	value := map[string]any{"same": true}
	versionTwo, err := hashPreimage(
		DomainExecutionDependency,
		Versions{CanonicalEncoding: 1, Projection: 2},
		value,
	)
	if err != nil {
		t.Fatal(err)
	}
	versionThree, err := hashPreimage(
		DomainExecutionDependency,
		Versions{CanonicalEncoding: 1, Projection: 3},
		value,
	)
	if err != nil {
		t.Fatal(err)
	}
	if versionTwo == versionThree {
		t.Fatalf("execution dependency projection versions collided: %s", versionTwo)
	}
	current, err := Hash(DomainExecutionDependency, value)
	if err != nil {
		t.Fatal(err)
	}
	if current != versionThree {
		t.Fatalf("execution dependency current hash = %q, want v3 hash %q", current, versionThree)
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

func TestCompositionSubjectHashSupportsHistoricalAndCurrentVersions(t *testing.T) {
	value := map[string]any{"subject_tree": "sha256:tree"}
	historical, err := hashWithVersions(
		DomainCompositionSubject,
		Versions{CanonicalEncoding: 1, Projection: 1},
		value,
	)
	if err != nil {
		t.Fatal(err)
	}
	current, err := Hash(DomainCompositionSubject, value)
	if err != nil {
		t.Fatal(err)
	}
	if historical == current {
		t.Fatalf("composition-subject v1 and v2 hashes collided: %s", current)
	}
}

func TestProjectionVersionTwoIsSupportedOnlyForCompositionSubject(t *testing.T) {
	for _, domain := range []Domain{
		DomainScore,
		DomainScoreSubtree,
		DomainResolvedCast,
		DomainCriterionSpec,
		DomainAcceptanceSpec,
		DomainChangeSet,
		DomainCandidate,
		DomainCandidateComposition,
		DomainMovementComposition,
		DomainCompositionEnvironment,
		DomainPatchOperations,
		DomainResolutionBody,
	} {
		if _, err := hashWithVersions(domain, Versions{CanonicalEncoding: 1, Projection: 2}, nil); !errors.Is(err, ErrUnsupportedRunFormat) {
			t.Fatalf("domain %q projection version 2 error = %v, want unsupported format", domain, err)
		}
	}
}

func TestExecutionDependencyHistoricalV2FailsClosed(t *testing.T) {
	if _, err := hashWithVersions(
		DomainExecutionDependency,
		Versions{CanonicalEncoding: 1, Projection: 2},
		nil,
	); !errors.Is(err, ErrUnsupportedRunFormat) {
		t.Fatalf("execution dependency v2 error = %v, want unsupported format", err)
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
