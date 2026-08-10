package queryruntime

import "intelligent-report-generation-system/internal/askdata"

type snapshotIndexIdentity struct {
	tenantID          askdata.ID
	materializationID askdata.ID
}

// cacheIndex is guarded by ResultCache.mu.
type cacheIndex struct {
	bySnapshot map[snapshotIndexIdentity]map[string]struct{}
}

func newCacheIndex() *cacheIndex {
	return &cacheIndex{bySnapshot: make(map[snapshotIndexIdentity]map[string]struct{})}
}

func (index *cacheIndex) add(
	cacheKey string,
	tenantID askdata.ID,
	snapshots []SnapshotVersion,
) {
	for _, snapshot := range snapshots {
		identity := snapshotIndexIdentity{
			tenantID: tenantID, materializationID: snapshot.MaterializationID,
		}
		keys := index.bySnapshot[identity]
		if keys == nil {
			keys = make(map[string]struct{})
			index.bySnapshot[identity] = keys
		}
		keys[cacheKey] = struct{}{}
	}
}

func (index *cacheIndex) remove(
	cacheKey string,
	tenantID askdata.ID,
	snapshots []SnapshotVersion,
) {
	for _, snapshot := range snapshots {
		identity := snapshotIndexIdentity{
			tenantID: tenantID, materializationID: snapshot.MaterializationID,
		}
		keys := index.bySnapshot[identity]
		delete(keys, cacheKey)
		if len(keys) == 0 {
			delete(index.bySnapshot, identity)
		}
	}
}

func (index *cacheIndex) keys(tenantID, materializationID askdata.ID) []string {
	keys := index.bySnapshot[snapshotIndexIdentity{
		tenantID: tenantID, materializationID: materializationID,
	}]
	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	return result
}
