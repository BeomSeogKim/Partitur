package score

import "slices"

// Parts returns effective parts sorted by id. Every returned slice is a copy.
func (s *Score) Parts() []PartView {
	if s == nil {
		return nil
	}
	result := make([]PartView, 0, len(s.document.Parts))
	for id, part := range s.document.Parts {
		result = append(result, PartView{
			ID:           id,
			Capabilities: sortedStrings(part.Capabilities),
			ReadOnly:     part.ReadOnly,
		})
	}
	slices.SortFunc(result, func(left, right PartView) int {
		switch {
		case left.ID < right.ID:
			return -1
		case left.ID > right.ID:
			return 1
		default:
			return 0
		}
	})
	return result
}

// Movements returns effective movements in declaration order. Every returned
// slice is a copy.
func (s *Score) Movements() []MovementView {
	if s == nil {
		return nil
	}
	result := make([]MovementView, 0, len(s.document.Movements))
	for _, movement := range s.document.Movements {
		phase := ""
		if movement.Phase != nil {
			phase = *movement.Phase
		}
		result = append(result, MovementView{
			ID:         movement.ID,
			PartID:     movement.PartID,
			Phase:      phase,
			Grants:     sortedStrings(movement.Grants),
			MayPropose: movement.MayPropose,
		})
	}
	return result
}

// EffectivePolicy returns a defensive view of defaulted policy.
func (s *Score) EffectivePolicy() PolicyView {
	if s == nil {
		return PolicyView{}
	}
	return PolicyView{
		AllowedPaths:       sortedStrings(s.document.Policy.AllowedPaths),
		SideEffects:        sortedStrings(s.document.Policy.SideEffects),
		ActiveWallClockMin: int64(s.document.Policy.Budget.ActiveWallClockMin),
		RetriesPerMovement: int64(s.document.Policy.Budget.RetriesPerMovement),
		AmendmentAuto:      s.document.Policy.Amendment.Auto,
	}
}
