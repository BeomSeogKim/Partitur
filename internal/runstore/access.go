package runstore

// RepositoryRoot returns the fixed root supplied to New. It is exposed only
// for read-only command projections; it does not discover or retarget roots.
func (store *Store) RepositoryRoot() string {
	if store == nil {
		return ""
	}
	return store.root
}
