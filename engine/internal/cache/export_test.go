package cache

// PruneDirty runs one background prune step synchronously so tests can assert
// on it without depending on the insert-count trigger.
func (h *HistoryStore) PruneDirty() error { return h.pruneDirty() }
