package recovery

// UnitOwnedDeferral records a recovery action whose implementation belongs to
// a named later unit. It is the sole production representation for that
// repository-planning fact; it does not define a runtime recovery outcome.
type UnitOwnedDeferral struct {
	Kind ActionKind
	Unit string
}

// unitOwnedDeferrals is deliberately empty. Completion is blocked until every
// entry has been implemented and removed.
var unitOwnedDeferrals []UnitOwnedDeferral

// UnitOwnedDeferrals returns a copy for completeness checks.
func UnitOwnedDeferrals() []UnitOwnedDeferral {
	return append([]UnitOwnedDeferral(nil), unitOwnedDeferrals...)
}
