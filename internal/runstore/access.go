package runstore

import "github.com/BeomSeogKim/Partitur/internal/faultpoint"

// RepositoryRoot returns the fixed root supplied to New. It is exposed only
// for read-only command projections; it does not discover or retarget roots.
func (store *Store) RepositoryRoot() string {
	if store == nil {
		return ""
	}
	return store.root
}

// Reached emits an Appendix E boundary point for a caller-owned protocol seam.
func (store *Store) Reached(point faultpoint.PointID) {
	if store != nil {
		store.probe.Reached(point)
	}
}
