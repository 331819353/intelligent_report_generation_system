// Package runtimeconfig owns validated, non-secret online configuration
// versions. It never reads or resolves deployment secret plaintext.
package runtimeconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/askdata"
)

var (
	ErrInvalid   = errors.New("runtime configuration is invalid")
	ErrForbidden = errors.New("runtime configuration access is forbidden")
	ErrNotFound  = errors.New("runtime configuration version was not found")
	ErrConflict  = errors.New("runtime configuration changed concurrently")
)

type Definition struct {
	Key           string   `json:"key"`
	Type          string   `json:"type"`
	ScopeTypes    []string `json:"scopeTypes"`
	Compatibility string   `json:"compatibility"`
	Minimum       *int64   `json:"minimum,omitempty"`
	Maximum       *int64   `json:"maximum,omitempty"`
	Enum          []string `json:"enum,omitempty"`
	Description   string   `json:"description"`
}

var definitions = map[string]Definition{
	"domain.askdataEnabled":        {Key: "domain.askdataEnabled", Type: "boolean", ScopeTypes: []string{"DOMAIN"}, Compatibility: "HOT_RELOAD", Description: "Enable the Ask Data entry for one domain"},
	"budget.dailyRuns":             {Key: "budget.dailyRuns", Type: "integer", ScopeTypes: []string{"TENANT", "DOMAIN"}, Compatibility: "HOT_RELOAD", Minimum: i64(1), Maximum: i64(10000000), Description: "Daily governed run budget"},
	"degradation.narrativeEnabled": {Key: "degradation.narrativeEnabled", Type: "boolean", ScopeTypes: []string{"TENANT", "DOMAIN"}, Compatibility: "HOT_RELOAD", Description: "Allow verified narrative generation"},
	"worker.maxConcurrentJobs":     {Key: "worker.maxConcurrentJobs", Type: "integer", ScopeTypes: []string{"WORKER"}, Compatibility: "HOT_RELOAD", Minimum: i64(1), Maximum: i64(128), Description: "Per-worker concurrency ceiling"},
	"provider.routingMode":         {Key: "provider.routingMode", Type: "string", ScopeTypes: []string{"TENANT"}, Compatibility: "NEXT_RESTART", Enum: []string{"ROUND_ROBIN", "PRIMARY_FAILOVER"}, Description: "Provider routing policy; credentials remain deployment managed"},
}

type DeploymentParameter struct {
	Name           string `json:"name"`
	Category       string `json:"category"`
	Configured     bool   `json:"configured"`
	MutableOnline  bool   `json:"mutableOnline"`
	ChangeGuidance string `json:"changeGuidance"`
}
type Version struct {
	ID              askdata.ID          `json:"id"`
	ScopeType       string              `json:"scopeType"`
	ScopeID         string              `json:"scopeId"`
	VersionNo       int                 `json:"versionNo"`
	BaseVersionID   askdata.ID          `json:"baseVersionId,omitempty"`
	Config          json.RawMessage     `json:"config"`
	ConfigHash      askdata.ContentHash `json:"configHash"`
	State           string              `json:"state"`
	Compatibility   string              `json:"compatibility"`
	ImpactSummary   string              `json:"impactSummary"`
	CreatedBy       askdata.ID          `json:"createdBy"`
	ApprovedBy      askdata.ID          `json:"approvedBy,omitempty"`
	RejectedBy      askdata.ID          `json:"rejectedBy,omitempty"`
	RejectionReason string              `json:"rejectionReason,omitempty"`
	RecordVersion   int64               `json:"recordVersion"`
	CreatedAt       time.Time           `json:"createdAt"`
	UpdatedAt       time.Time           `json:"updatedAt"`
	SubmittedAt     *time.Time          `json:"submittedAt,omitempty"`
	ApprovedAt      *time.Time          `json:"approvedAt,omitempty"`
	RejectedAt      *time.Time          `json:"rejectedAt,omitempty"`
	ActivatedAt     *time.Time          `json:"activatedAt,omitempty"`
	Nodes           []RolloutNode       `json:"rolloutNodes,omitempty"`
}
type RolloutNode struct {
	ID           askdata.ID          `json:"id"`
	ConsumerType string              `json:"consumerType"`
	Ordinal      int                 `json:"ordinal"`
	State        string              `json:"state"`
	ExpectedHash askdata.ContentHash `json:"expectedHash"`
	AppliedHash  askdata.ContentHash `json:"appliedHash,omitempty"`
	FailureCode  string              `json:"failureCode,omitempty"`
	Attempt      int                 `json:"attempt"`
	AppliedAt    *time.Time          `json:"appliedAt,omitempty"`
}
type CreateInput struct {
	ScopeType     string          `json:"scopeType"`
	ScopeID       string          `json:"scopeId"`
	BaseVersionID askdata.ID      `json:"baseVersionId"`
	Config        json.RawMessage `json:"config"`
	ImpactSummary string          `json:"impactSummary"`
}
type VersionInput struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}
type RejectInput struct {
	ExpectedVersion int64  `json:"expectedVersion"`
	Reason          string `json:"reason"`
}

func Definitions() []Definition {
	values := make([]Definition, 0, len(definitions))
	for _, v := range definitions {
		values = append(values, v)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Key < values[j].Key })
	return values
}
func ValidateConfig(scope string, raw json.RawMessage) (json.RawMessage, askdata.ContentHash, string, error) {
	if scope != "TENANT" && scope != "DOMAIN" && scope != "WORKER" {
		return nil, "", "", ErrInvalid
	}
	var object map[string]any
	var strictObject map[string]json.RawMessage
	if askdata.DecodeStrictJSON(raw, &strictObject) != nil || len(strictObject) == 0 || len(strictObject) > 32 {
		return nil, "", "", ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&object) != nil {
		return nil, "", "", ErrInvalid
	}
	compatibility := "HOT_RELOAD"
	for key, value := range object {
		definition, ok := definitions[key]
		if !ok || !contains(definition.ScopeTypes, scope) {
			return nil, "", "", fmt.Errorf("%w: unsupported key %s", ErrInvalid, key)
		}
		if definition.Compatibility == "NEXT_RESTART" {
			compatibility = "NEXT_RESTART"
		}
		switch definition.Type {
		case "boolean":
			if _, ok = value.(bool); !ok {
				return nil, "", "", ErrInvalid
			}
		case "integer":
			number, ok := value.(json.Number)
			if !ok {
				return nil, "", "", ErrInvalid
			}
			parsed, e := number.Int64()
			if e != nil || (definition.Minimum != nil && parsed < *definition.Minimum) || (definition.Maximum != nil && parsed > *definition.Maximum) {
				return nil, "", "", ErrInvalid
			}
		case "string":
			text, ok := value.(string)
			if !ok || !contains(definition.Enum, text) {
				return nil, "", "", ErrInvalid
			}
		}
	}
	canonical, e := json.Marshal(object)
	if e != nil {
		return nil, "", "", e
	}
	return canonical, askdata.HashBytes(canonical), compatibility, nil
}

var scopeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func ValidateScope(scope, id string) error {
	if scope == "DOMAIN" {
		parsed, e := uuid.Parse(id)
		if e != nil || parsed.String() != id {
			return ErrInvalid
		}
		return nil
	}
	if !scopeIDPattern.MatchString(id) {
		return ErrInvalid
	}
	return nil
}
func contains(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
func i64(v int64) *int64           { return &v }
func safeImpact(value string) bool { return value == strings.TrimSpace(value) && len(value) <= 2000 }
func safeReason(value string) bool {
	if value != strings.TrimSpace(value) || len(value) < 1 || len(value) > 1000 {
		return false
	}
	for _, character := range value {
		if character < 32 || character == 127 {
			return false
		}
	}
	return true
}
