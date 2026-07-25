package canonical

import (
	"errors"
	"fmt"
)

// Domain is a registered canonical-AST identity domain.
type Domain string

const (
	DomainScore                  Domain = "partitur/score"
	DomainScoreSubtree           Domain = "partitur/score-subtree"
	DomainResolvedCast           Domain = "partitur/resolved-cast"
	DomainCriterionSpec          Domain = "partitur/criterion-spec"
	DomainAcceptanceSpec         Domain = "partitur/acceptance-spec"
	DomainChangeSet              Domain = "partitur/change-set"
	DomainCandidate              Domain = "partitur/candidate"
	DomainCandidateComposition   Domain = "partitur/candidate-composition"
	DomainMovementComposition    Domain = "partitur/movement-composition"
	DomainCompositionEnvironment Domain = "partitur/composition-environment"
	DomainCompositionSubject     Domain = "partitur/composition-subject"
	DomainExecutionDependency    Domain = "partitur/execution-dependency"
	DomainPatchOperations        Domain = "partitur/patch-operations"
	DomainResolutionBody         Domain = "partitur/resolution-body"
)

const (
	CanonicalEncodingVersion    = 1
	AmendmentClassifierVersion  = 1
	CompositionAlgorithmVersion = 1

	ProjectionVersionScore                  = 1
	ProjectionVersionScoreSubtree           = 1
	ProjectionVersionResolvedCast           = 1
	ProjectionVersionCriterionSpec          = 1
	ProjectionVersionAcceptanceSpec         = 1
	ProjectionVersionChangeSet              = 1
	ProjectionVersionCandidate              = 1
	ProjectionVersionCandidateComposition   = 1
	ProjectionVersionMovementComposition    = 1
	ProjectionVersionCompositionEnvironment = 1
	ProjectionVersionCompositionSubject     = 1
	ProjectionVersionExecutionDependency    = 1
	ProjectionVersionPatchOperations        = 1
	ProjectionVersionResolutionBody         = 1
)

// ErrUnknownDomain means a domain is absent from the complete A.4 registry.
var ErrUnknownDomain = errors.New("unknown canonical identity domain")

// Versions is the version tuple recorded with a canonical-AST identity.
type Versions struct {
	CanonicalEncoding int
	Projection        int
}

// CurrentVersions returns the version tuple used for new identities.
func CurrentVersions(domain Domain) (Versions, error) {
	projection, ok := projectionVersion(domain)
	if !ok {
		return Versions{}, fmt.Errorf("%w: %q", ErrUnknownDomain, domain)
	}
	return Versions{
		CanonicalEncoding: CanonicalEncodingVersion,
		Projection:        projection,
	}, nil
}

func projectionVersion(domain Domain) (int, bool) {
	switch domain {
	case DomainScore:
		return ProjectionVersionScore, true
	case DomainScoreSubtree:
		return ProjectionVersionScoreSubtree, true
	case DomainResolvedCast:
		return ProjectionVersionResolvedCast, true
	case DomainCriterionSpec:
		return ProjectionVersionCriterionSpec, true
	case DomainAcceptanceSpec:
		return ProjectionVersionAcceptanceSpec, true
	case DomainChangeSet:
		return ProjectionVersionChangeSet, true
	case DomainCandidate:
		return ProjectionVersionCandidate, true
	case DomainCandidateComposition:
		return ProjectionVersionCandidateComposition, true
	case DomainMovementComposition:
		return ProjectionVersionMovementComposition, true
	case DomainCompositionEnvironment:
		return ProjectionVersionCompositionEnvironment, true
	case DomainCompositionSubject:
		return ProjectionVersionCompositionSubject, true
	case DomainExecutionDependency:
		return ProjectionVersionExecutionDependency, true
	case DomainPatchOperations:
		return ProjectionVersionPatchOperations, true
	case DomainResolutionBody:
		return ProjectionVersionResolutionBody, true
	default:
		return 0, false
	}
}
