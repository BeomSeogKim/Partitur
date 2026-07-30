package canonical

import (
	"crypto/sha256"
	"errors"
	"fmt"
)

// ErrUnsupportedRunFormat means the recorded identity version tuple is not
// implemented by this binary.
var ErrUnsupportedRunFormat = errors.New("unsupported_run_format")

// Hash computes an identity using the current versions for domain.
func Hash(domain Domain, value any) (string, error) {
	versions, err := CurrentVersions(domain)
	if err != nil {
		return "", err
	}
	return hashWithVersions(domain, versions, value)
}

// hashWithVersions is package-private so callers cannot attach a historical
// version tuple to an arbitrary AST. Once A.4 projectors exist, historical
// recomputation must reach this helper only after dispatching the projector
// selected by domain and projection version.
func hashWithVersions(domain Domain, versions Versions, value any) (string, error) {
	if _, ok := projectionVersion(domain); !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownDomain, domain)
	}
	if !supported(domain, versions) {
		return "", fmt.Errorf(
			"%w: domain=%q canonical_encoding=%d projection=%d",
			ErrUnsupportedRunFormat,
			domain,
			versions.CanonicalEncoding,
			versions.Projection,
		)
	}
	return hashPreimage(domain, versions, value)
}

func supported(domain Domain, versions Versions) bool {
	// Keep implemented historical tuples separate from CurrentVersions.
	// When a current version advances, old tuples remain explicit cases here.
	if versions.CanonicalEncoding != 1 {
		return false
	}
	switch domain {
	case DomainCompositionSubject:
		return versions.Projection == 1 || versions.Projection == 2
	case DomainScore,
		DomainScoreSubtree,
		DomainResolvedCast,
		DomainCriterionSpec,
		DomainAcceptanceSpec,
		DomainChangeSet,
		DomainCandidate,
		DomainCandidateComposition,
		DomainMovementComposition,
		DomainCompositionEnvironment,
		DomainExecutionDependency,
		DomainPatchOperations,
		DomainResolutionBody:
		return versions.Projection == 1
	default:
		return false
	}
}

func hashPreimage(domain Domain, versions Versions, value any) (string, error) {
	preimage, err := Encode(map[string]any{
		"domain":                     string(domain),
		"canonical_encoding_version": float64(versions.CanonicalEncoding),
		"projection_version":         float64(versions.Projection),
		"value":                      value,
	})
	if err != nil {
		return "", fmt.Errorf("canonical hash preimage: %w", err)
	}
	digest := sha256.Sum256(preimage)
	return fmt.Sprintf("sha256:%x", digest), nil
}
