package cast

import (
	"slices"
	"strings"
)

// Performers returns effective performers sorted by id.
func (c *Cast) Performers() []PerformerView {
	if c == nil {
		return nil
	}
	result := make([]PerformerView, 0, len(c.performers))
	for id, performer := range c.performers {
		result = append(result, performerView(id, performer))
	}
	slices.SortFunc(result, func(left, right PerformerView) int {
		return strings.Compare(left.ID, right.ID)
	})
	return result
}

// Performer returns a defensive effective performer view.
func (c *Cast) Performer(id string) (PerformerView, bool) {
	if c == nil {
		return PerformerView{}, false
	}
	value, ok := c.performers[id]
	if !ok {
		return PerformerView{}, false
	}
	return performerView(id, value), true
}

// Bindings returns effective bindings sorted by part id.
func (c *Cast) Bindings() []BindingView {
	if c == nil {
		return nil
	}
	result := make([]BindingView, 0, len(c.bindings))
	for partID, binding := range c.bindings {
		result = append(result, c.bindingView(partID, binding))
	}
	slices.SortFunc(result, func(left, right BindingView) int {
		return strings.Compare(left.PartID, right.PartID)
	})
	return result
}

// Binding returns a defensive effective binding view.
func (c *Cast) Binding(partID string) (BindingView, bool) {
	if c == nil {
		return BindingView{}, false
	}
	value, ok := c.bindings[partID]
	if !ok {
		return BindingView{}, false
	}
	return c.bindingView(partID, value), true
}

func performerView(id string, value performer) PerformerView {
	return PerformerView{
		ID:                       id,
		Adapter:                  value.Adapter,
		Model:                    value.Model,
		AllowAdvisoryEnforcement: value.AllowAdvisoryEnforcement,
		Extensions:               cloneMap(value.Extensions),
	}
}

func (c *Cast) bindingView(partID string, value binding) BindingView {
	strict := true
	chain := append([]string{value.Performer}, value.Fallbacks...)
	for _, performerID := range chain {
		if c.performers[performerID].AllowAdvisoryEnforcement {
			strict = false
			break
		}
	}
	return BindingView{
		PartID:    partID,
		Performer: value.Performer,
		Fallbacks: slices.Clone(value.Fallbacks),
		Strict:    strict,
	}
}

func sortedStrings(values []string) []string {
	result := slices.Clone(values)
	slices.Sort(result)
	return result
}
