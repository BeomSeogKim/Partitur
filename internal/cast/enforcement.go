package cast

import (
	"slices"

	"github.com/BeomSeogKim/Partitur/internal/protocol"
	"github.com/BeomSeogKim/Partitur/internal/score"
)

// EvaluateEnforcement evaluates §4's six-row fail-closed table. The result is
// a disposition plus the exact unmet dimension set, never a boolean.
func EvaluateEnforcement(
	movement score.MovementView,
	policy score.PolicyView,
	allowAdvisory bool,
	enforcement protocol.Enforcement,
) EnforcementResult {
	grants := make(map[string]struct{}, len(movement.Grants))
	for _, grant := range movement.Grants {
		grants[grant] = struct{}{}
	}
	_, hasWrite := grants["repo_write"]
	_, hasRead := grants["repo_read"]
	_, hasShell := grants["shell"]
	_, hasNetwork := grants["network"]
	pathScoped := !wholeRepository(policy.AllowedPaths)

	var unmet []EnforcementDimension
	if !hasWrite && !enforcement.ReadOnly {
		unmet = append(unmet, DimensionReadOnly)
	}
	if hasWrite && pathScoped && !enforcement.PathGrants {
		unmet = append(unmet, DimensionPathGrants)
	}
	if !hasRead && !enforcement.ReadGrants {
		unmet = append(unmet, DimensionReadGrants)
	}
	if hasRead && pathScoped && !enforcement.PathGrants &&
		!containsDimension(unmet, DimensionPathGrants) {
		unmet = append(unmet, DimensionPathGrants)
	}
	if !hasShell && !enforcement.ShellGrants {
		unmet = append(unmet, DimensionShellGrants)
	}
	if !hasNetwork && !enforcement.NetworkGrants {
		unmet = append(unmet, DimensionNetworkGrants)
	}
	slices.Sort(unmet)

	disposition := EnforcementStrict
	if len(unmet) != 0 {
		disposition = EnforcementRefused
		if allowAdvisory {
			disposition = EnforcementAdvisory
		}
	}
	return EnforcementResult{
		Disposition: disposition,
		Unmet:       unmet,
	}
}

func wholeRepository(allowedPaths []string) bool {
	return len(allowedPaths) == 1 && allowedPaths[0] == "**"
}

func containsDimension(
	values []EnforcementDimension,
	target EnforcementDimension,
) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
