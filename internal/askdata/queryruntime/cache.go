package queryruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	DefaultResultCacheTTL = time.Hour
	MaxCachedResultBytes  = 16 << 20
	MaxCachedResultRows   = 20000
	maxCacheSnapshots     = 64
	cacheSegmentSeparator = "\x1f"
)

var ErrInvalidCacheResult = errors.New("AskData cache result is invalid")

// SnapshotVersion is the immutable control-plane freshness fact for one
// materialization. SnapshotCompletedAt is deliberately excluded from the key;
// it supplies the cached result's conservative asOf timestamp.
type SnapshotVersion struct {
	MaterializationID   askdata.ID `json:"materializationId"`
	SnapshotVersion     askdata.ID `json:"snapshotVersion"`
	SnapshotCompletedAt time.Time  `json:"snapshotCompletedAt"`
}

// CacheRequest names every boundary that can change a semantic query result.
// Release is repeated explicitly so a confused caller cannot silently use the
// release embedded in a different PolicyScope.
type CacheRequest struct {
	Scope     askdata.PolicyScope
	Release   askdata.ReleaseRef
	IRHash    askdata.ContentHash
	Snapshots []SnapshotVersion
}

type CachedResult struct {
	ResultHash askdata.ContentHash
	RowCount   int
	Payload    json.RawMessage
}

// CacheEntry exposes only the required cache metadata. Result returns a clone
// of the private payload so callers cannot mutate a shared cache entry.
type CacheEntry struct {
	ResultHash askdata.ContentHash `json:"resultHash"`
	AsOf       time.Time           `json:"asOf"`
	RowCount   int                 `json:"rowCount"`
	CreatedAt  time.Time           `json:"createdAt"`
	TTL        time.Duration       `json:"ttl"`

	payload   json.RawMessage
	snapshots []SnapshotVersion
	tenantID  askdata.ID
}

func (entry CacheEntry) Result() json.RawMessage {
	return cloneBytes(entry.payload)
}

type LookupOptions struct {
	ForceFresh       bool
	CurrentSnapshots []SnapshotVersion
}

// ResultCache is a bounded-lifetime in-process cache. Snapshot-version keys
// are the correctness boundary; the reverse index is only an eager eviction
// optimization when a refresh-completion notification arrives.
type ResultCache struct {
	mu      sync.Mutex
	entries map[string]CacheEntry
	index   *cacheIndex
	now     func() time.Time
	ttl     time.Duration
}

func NewResultCache() *ResultCache {
	return newResultCache(DefaultResultCacheTTL, time.Now)
}

func newResultCache(ttl time.Duration, now func() time.Time) *ResultCache {
	return &ResultCache{
		entries: make(map[string]CacheEntry),
		index:   newCacheIndex(),
		now:     now,
		ttl:     ttl,
	}
}

// BuildKey hashes the five required segments in their specified order. An
// empty string means the request is not safe to cache.
func BuildKey(
	scope askdata.PolicyScope,
	release askdata.ReleaseRef,
	ir askdata.ContentHash,
	snapshots []SnapshotVersion,
) string {
	ordered, ok := normalizeSnapshots(snapshots, false)
	if scope.Validate() != nil || release.Validate() != nil || scope.Release != release ||
		ir.Validate() != nil || !ok {
		return ""
	}
	pairs := make([]string, len(ordered))
	for index, snapshot := range ordered {
		pairs[index] = string(snapshot.MaterializationID) + ":" + string(snapshot.SnapshotVersion)
	}
	payload := strings.Join([]string{
		string(scope.TenantID),
		string(scope.PolicyHash),
		string(release.ContentHash),
		string(ir),
		strings.Join(pairs, ","),
	}, cacheSegmentSeparator)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// Put writes only fully scoped entries. Missing or invalid key material is a
// safe no-op rather than an invitation to fall back to a weaker key.
func (cache *ResultCache) Put(
	request CacheRequest,
	result CachedResult,
) (CacheEntry, bool, error) {
	if cache == nil || cache.now == nil || cache.ttl <= 0 {
		return CacheEntry{}, false, nil
	}
	key := BuildKey(request.Scope, request.Release, request.IRHash, request.Snapshots)
	ordered, ok := normalizeSnapshots(request.Snapshots, true)
	if key == "" || !ok {
		return CacheEntry{}, false, nil
	}
	if result.ResultHash.Validate() != nil || result.RowCount < 0 ||
		result.RowCount > MaxCachedResultRows ||
		len(result.Payload) == 0 || len(result.Payload) > MaxCachedResultBytes ||
		!json.Valid(result.Payload) {
		return CacheEntry{}, false, ErrInvalidCacheResult
	}
	createdAt := cache.now().UTC()
	entry := CacheEntry{
		ResultHash: result.ResultHash,
		AsOf:       minimumCompletedAt(ordered),
		RowCount:   result.RowCount,
		CreatedAt:  createdAt,
		TTL:        cache.ttl,
		payload:    cloneBytes(result.Payload),
		snapshots:  cloneSnapshots(ordered),
		tenantID:   request.Scope.TenantID,
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.deleteLocked(key)
	cache.entries[key] = entry
	cache.index.add(key, request.Scope.TenantID, ordered)
	return cloneEntry(entry), true, nil
}

// Get returns the cached asOf unchanged. Normal lookups must carry the current
// snapshot versions in request, so a refresh naturally creates a different
// key. forceFresh additionally fails closed unless the caller supplies current
// snapshots that exactly match the cached snapshot set.
func (cache *ResultCache) Get(
	request CacheRequest,
	options LookupOptions,
) (CacheEntry, bool) {
	if cache == nil || cache.now == nil {
		return CacheEntry{}, false
	}
	key := BuildKey(request.Scope, request.Release, request.IRHash, request.Snapshots)
	if key == "" {
		return CacheEntry{}, false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, exists := cache.entries[key]
	if !exists {
		return CacheEntry{}, false
	}
	if !cache.now().UTC().Before(entry.CreatedAt.Add(entry.TTL)) {
		cache.deleteLocked(key)
		return CacheEntry{}, false
	}
	if options.ForceFresh {
		current, valid := normalizeSnapshots(options.CurrentSnapshots, false)
		if !valid || !sameSnapshotVersions(entry.snapshots, current) {
			return CacheEntry{}, false
		}
	}
	return cloneEntry(entry), true
}

func (cache *ResultCache) Delete(key string) bool {
	if cache == nil {
		return false
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.deleteLocked(key)
}

// InvalidateBySnapshot implements materialization.SnapshotCacheInvalidator.
// Tenant is part of the reverse-index identity so a forged notification cannot
// evict another tenant's entries even if logical IDs are accidentally reused.
func (cache *ResultCache) InvalidateBySnapshot(tenantID, materializationID string) int {
	if cache == nil || askdata.ID(tenantID).Validate() != nil ||
		askdata.ID(materializationID).Validate() != nil {
		return 0
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	keys := cache.index.keys(askdata.ID(tenantID), askdata.ID(materializationID))
	removed := 0
	for _, key := range keys {
		if cache.deleteLocked(key) {
			removed++
		}
	}
	return removed
}

func (cache *ResultCache) deleteLocked(key string) bool {
	entry, exists := cache.entries[key]
	if !exists {
		return false
	}
	delete(cache.entries, key)
	cache.index.remove(key, entry.tenantID, entry.snapshots)
	return true
}

func normalizeSnapshots(snapshots []SnapshotVersion, requireCompletedAt bool) ([]SnapshotVersion, bool) {
	if len(snapshots) == 0 || len(snapshots) > maxCacheSnapshots {
		return nil, false
	}
	ordered := cloneSnapshots(snapshots)
	for _, snapshot := range ordered {
		if snapshot.MaterializationID.Validate() != nil || snapshot.SnapshotVersion.Validate() != nil ||
			strings.Contains(string(snapshot.MaterializationID), ":") ||
			strings.Contains(string(snapshot.SnapshotVersion), ":") ||
			(requireCompletedAt && snapshot.SnapshotCompletedAt.IsZero()) {
			return nil, false
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].MaterializationID < ordered[j].MaterializationID
	})
	for index := 1; index < len(ordered); index++ {
		if ordered[index-1].MaterializationID == ordered[index].MaterializationID {
			return nil, false
		}
	}
	return ordered, true
}

func minimumCompletedAt(snapshots []SnapshotVersion) time.Time {
	minimum := snapshots[0].SnapshotCompletedAt.UTC()
	for _, snapshot := range snapshots[1:] {
		completedAt := snapshot.SnapshotCompletedAt.UTC()
		if completedAt.Before(minimum) {
			minimum = completedAt
		}
	}
	return minimum
}

func sameSnapshotVersions(left, right []SnapshotVersion) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].MaterializationID != right[index].MaterializationID ||
			left[index].SnapshotVersion != right[index].SnapshotVersion {
			return false
		}
	}
	return true
}

func cloneEntry(entry CacheEntry) CacheEntry {
	entry.payload = cloneBytes(entry.payload)
	entry.snapshots = cloneSnapshots(entry.snapshots)
	return entry
}

func cloneSnapshots(snapshots []SnapshotVersion) []SnapshotVersion {
	return append([]SnapshotVersion(nil), snapshots...)
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
