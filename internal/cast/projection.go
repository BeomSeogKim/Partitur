package cast

import (
	"errors"

	"github.com/BeomSeogKim/Partitur/internal/canonical"
)

// ProjectionBytes returns JCS bytes for the Appendix A.4
// partitur/resolved-cast value.
func (c *Cast) ProjectionBytes() ([]byte, error) {
	if c == nil {
		return nil, errors.New("cast: nil Cast")
	}
	return canonical.Encode(c.projectionValue())
}

// Hash returns the versioned partitur/resolved-cast identity.
func (c *Cast) Hash() (string, error) {
	if c == nil {
		return "", errors.New("cast: nil Cast")
	}
	return canonical.Hash(canonical.DomainResolvedCast, c.projectionValue())
}

func (c *Cast) projectionValue() map[string]any {
	performers := make(map[string]any, len(c.performers))
	for id, performer := range c.performers {
		value := map[string]any{
			"adapter":                    performer.Adapter,
			"model":                      performer.Model,
			"allow_advisory_enforcement": performer.AllowAdvisoryEnforcement,
		}
		if performer.Extensions != nil {
			value["extensions"] = cloneMap(performer.Extensions)
		}
		performers[id] = value
	}
	bindings := make(map[string]any, len(c.bindings))
	for partID, binding := range c.bindings {
		bindings[partID] = map[string]any{
			"performer": binding.Performer,
			"fallbacks": stringsToAny(binding.Fallbacks),
		}
	}
	return map[string]any{
		"cast":       "0.1",
		"performers": performers,
		"bindings":   bindings,
	}
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}
