package recovery

// unimplementedActionOwners is the single owner assignment for recovery
// actions whose implementation belongs to a later unit. The unit assignments
// are documentation whose authority is the project roadmap; completion is
// blocked until this map is empty.
var unimplementedActionOwners = map[ActionKind]string{
	ActionAppendBlockedProposalRoute: "4.2",
	ActionAppendRoutedRequest:        "4.2",
}

// UnimplementedActionOwner returns the owning unit for one deferred recovery
// action. Callers must refuse the action rather than manufacture durable state
// which that unit has not implemented yet.
func UnimplementedActionOwner(kind ActionKind) (string, bool) {
	unit, ok := unimplementedActionOwners[kind]
	return unit, ok
}

// UnimplementedActionOwners returns a copy for completeness checks.
func UnimplementedActionOwners() map[ActionKind]string {
	owners := make(map[ActionKind]string, len(unimplementedActionOwners))
	for kind, unit := range unimplementedActionOwners {
		owners[kind] = unit
	}
	return owners
}
