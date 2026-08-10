package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
)

type CacheKeyInput struct {
	TenantID, UserScope, DatasetVersion, DataWatermark, EngineVersion string
	Parameters                                                        map[string]any
	RowPolicyVersions, ColumnPolicyVersions                           []int64
}

// BuildCacheKey 对影响查询结果的身份、策略和查询上下文生成稳定缓存键。
func BuildCacheKey(input CacheKeyInput) (string, error) {
	if input.TenantID == "" || input.UserScope == "" || input.DatasetVersion == "" {
		return "", errors.New("tenant, user scope and dataset version are required")
	}
	sort.Slice(input.RowPolicyVersions, func(i, j int) bool { return input.RowPolicyVersions[i] < input.RowPolicyVersions[j] })
	sort.Slice(input.ColumnPolicyVersions, func(i, j int) bool { return input.ColumnPolicyVersions[i] < input.ColumnPolicyVersions[j] })
	payload, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "query:" + hex.EncodeToString(sum[:]), nil
}

// AskDataCacheKeyInput names every mutable boundary that can change a
// semantic query result. In particular, Scope contains tenant, actor, roles,
// domains and the pinned release; PolicyHash prevents reuse after any of
// those authorization facts changes.
type AskDataCacheKeyInput struct {
	Scope         askdata.PolicyScope `json:"scope"`
	IRHash        askdata.ContentHash `json:"irHash"`
	SnapshotHash  askdata.ContentHash `json:"snapshotHash"`
	FreshnessHash askdata.ContentHash `json:"freshnessHash"`
	EngineVersion string              `json:"engineVersion"`
}

const AskDataCacheKeyVersion = "askdata-cache-key-v1"

type askDataCacheKeyEnvelope struct {
	Version         string              `json:"version"`
	TenantID        askdata.ID          `json:"tenantId"`
	ActorID         askdata.ID          `json:"actorId"`
	PolicyScopeHash askdata.ContentHash `json:"policyScopeHash"`
	ReleaseID       askdata.ID          `json:"releaseId"`
	ReleaseHash     askdata.ContentHash `json:"releaseHash"`
	IRHash          askdata.ContentHash `json:"irHash"`
	SnapshotHash    askdata.ContentHash `json:"snapshotHash"`
	FreshnessHash   askdata.ContentHash `json:"freshnessHash"`
	EngineVersion   string              `json:"engineVersion"`
}

// BuildAskDataCacheKey builds the dedicated AskData result-cache key without
// weakening the legacy dataset cache contract above.
func BuildAskDataCacheKey(input AskDataCacheKeyInput) (string, error) {
	engineVersion := strings.TrimSpace(input.EngineVersion)
	if input.Scope.Validate() != nil || input.IRHash.Validate() != nil ||
		input.SnapshotHash.Validate() != nil || input.FreshnessHash.Validate() != nil ||
		engineVersion == "" || engineVersion != input.EngineVersion || len(engineVersion) > 128 ||
		!utf8.ValidString(engineVersion) || strings.ContainsAny(engineVersion, "\x00\r\n") {
		return "", errors.New("AskData cache scope is invalid")
	}
	payload, err := json.Marshal(askDataCacheKeyEnvelope{
		Version:  AskDataCacheKeyVersion,
		TenantID: input.Scope.TenantID, ActorID: input.Scope.ActorID,
		PolicyScopeHash: input.Scope.PolicyHash,
		ReleaseID:       input.Scope.Release.ReleaseID,
		ReleaseHash:     input.Scope.Release.ContentHash,
		IRHash:          input.IRHash,
		SnapshotHash:    input.SnapshotHash,
		FreshnessHash:   input.FreshnessHash,
		EngineVersion:   engineVersion,
	})
	if err != nil {
		return "", err
	}
	return "askdata-query:" + string(askdata.HashBytes(payload)), nil
}
