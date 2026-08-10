package understanding

import (
	"sync"

	"intelligent-report-generation-system/internal/askdata"
)

type dictionaryCacheKey struct {
	tenantID, domainID, releaseID string
}

type dictionaryCacheEntry struct {
	releaseHash askdata.ContentHash
	index       *dictionaryIndex
}

// DictionaryCache stores immutable release-pinned indexes. A release content
// hash change replaces the old entry even if the release ID is reused.
type DictionaryCache struct {
	mu      sync.RWMutex
	entries map[dictionaryCacheKey]dictionaryCacheEntry
}

func NewDictionaryCache() *DictionaryCache {
	return &DictionaryCache{entries: map[dictionaryCacheKey]dictionaryCacheEntry{}}
}

func (cache *DictionaryCache) get(
	tenantID, domainID askdata.ID,
	release askdata.ReleaseRef,
) (*dictionaryIndex, bool) {
	if cache == nil {
		return nil, false
	}
	key := dictionaryCacheKey{string(tenantID), string(domainID), string(release.ReleaseID)}
	cache.mu.RLock()
	entry, exists := cache.entries[key]
	cache.mu.RUnlock()
	return entry.index, exists && entry.releaseHash == release.ContentHash && entry.index != nil
}

func (cache *DictionaryCache) put(
	tenantID, domainID askdata.ID,
	release askdata.ReleaseRef,
	index *dictionaryIndex,
) {
	if cache == nil || index == nil {
		return
	}
	key := dictionaryCacheKey{string(tenantID), string(domainID), string(release.ReleaseID)}
	cache.mu.Lock()
	if cache.entries == nil {
		cache.entries = map[dictionaryCacheKey]dictionaryCacheEntry{}
	}
	cache.entries[key] = dictionaryCacheEntry{releaseHash: release.ContentHash, index: index}
	cache.mu.Unlock()
}

func (cache *DictionaryCache) Invalidate(tenantID, domainID, releaseID askdata.ID) {
	if cache == nil {
		return
	}
	cache.mu.Lock()
	delete(cache.entries, dictionaryCacheKey{string(tenantID), string(domainID), string(releaseID)})
	cache.mu.Unlock()
}

func (cache *DictionaryCache) Len() int {
	if cache == nil {
		return 0
	}
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return len(cache.entries)
}
