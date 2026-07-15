package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
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
