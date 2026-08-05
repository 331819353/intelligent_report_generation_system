package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

type ReleaseObjectType string

const (
	ReleaseObjectDomain        ReleaseObjectType = "DOMAIN"
	ReleaseObjectEntity        ReleaseObjectType = "ENTITY"
	ReleaseObjectSemanticModel ReleaseObjectType = "SEMANTIC_MODEL"
	ReleaseObjectMeasure       ReleaseObjectType = "MEASURE"
	ReleaseObjectMetric        ReleaseObjectType = "METRIC"
	ReleaseObjectDimension     ReleaseObjectType = "DIMENSION"
	ReleaseObjectMember        ReleaseObjectType = "MEMBER"
	ReleaseObjectHierarchy     ReleaseObjectType = "HIERARCHY"
	ReleaseObjectRelationship  ReleaseObjectType = "RELATIONSHIP"
	ReleaseObjectQualityRule   ReleaseObjectType = "QUALITY_RULE"
	ReleaseObjectBusinessTerm  ReleaseObjectType = "BUSINESS_TERM"
	ReleaseObjectExample       ReleaseObjectType = "CERTIFIED_EXAMPLE"
)

type ReleaseObject struct {
	Type            ReleaseObjectType   `json:"type"`
	ObjectID        string              `json:"objectId"`
	ObjectVersionID string              `json:"objectVersionId"`
	ContentHash     askdata.ContentHash `json:"contentHash"`
	Sensitivity     Sensitivity         `json:"sensitivity"`
	Contract        json.RawMessage     `json:"contract"`
}

type ReleaseManifest struct {
	ContentHash askdata.ContentHash `json:"contentHash"`
	Objects     []ReleaseObject     `json:"objects"`
}

type SemanticRelease struct {
	ID              string              `json:"id"`
	TenantID        string              `json:"tenantId"`
	DomainID        string              `json:"domainId"`
	SemanticVersion string              `json:"semanticVersion"`
	ContentHash     askdata.ContentHash `json:"contentHash"`
	Status          string              `json:"status"`
	ObjectCount     int                 `json:"objectCount"`
	Version         int64               `json:"version"`
	CreatedBy       string              `json:"createdBy"`
	UpdatedBy       string              `json:"updatedBy"`
	CreatedAt       time.Time           `json:"createdAt,omitempty"`
	UpdatedAt       time.Time           `json:"updatedAt,omitempty"`
}

// BuildReleaseManifest validates, canonicalizes and stable-sorts an exact set
// of immutable semantic versions. Its hash algorithm intentionally matches
// askdata.release_manifest_hash in migration 000216.
func BuildReleaseManifest(objects []ReleaseObject) (ReleaseManifest, error) {
	if len(objects) == 0 || len(objects) > 10000 {
		return ReleaseManifest{}, errors.New("release must contain 1 to 10000 objects")
	}
	manifest := ReleaseManifest{Objects: make([]ReleaseObject, len(objects))}
	seen := make(map[string]struct{}, len(objects))
	for index, object := range objects {
		if !validReleaseObjectType(object.Type) {
			return ReleaseManifest{}, fmt.Errorf("objects[%d].type is unsupported", index)
		}
		if _, err := uuid.Parse(object.ObjectID); err != nil {
			return ReleaseManifest{}, fmt.Errorf("objects[%d].objectId must be a UUID", index)
		}
		if _, err := uuid.Parse(object.ObjectVersionID); err != nil {
			return ReleaseManifest{}, fmt.Errorf("objects[%d].objectVersionId must be a UUID", index)
		}
		if err := object.ContentHash.Validate(); err != nil {
			return ReleaseManifest{}, fmt.Errorf("objects[%d].contentHash: %w", index, err)
		}
		if !validSensitivity(object.Sensitivity) {
			return ReleaseManifest{}, fmt.Errorf("objects[%d].sensitivity is unsupported", index)
		}
		canonical, err := CanonicalJSON(object.Contract)
		if err != nil {
			return ReleaseManifest{}, fmt.Errorf("objects[%d].contract: %w", index, err)
		}
		if askdata.HashBytes(canonical) != object.ContentHash {
			return ReleaseManifest{}, fmt.Errorf("objects[%d].contentHash does not match canonical contract", index)
		}
		object.Contract = canonical
		key := fmt.Sprintf("%s:%s:%s", object.Type, object.ObjectID, object.ObjectVersionID)
		if _, exists := seen[key]; exists {
			return ReleaseManifest{}, fmt.Errorf("objects[%d] duplicates %s", index, key)
		}
		seen[key] = struct{}{}
		manifest.Objects[index] = object
	}
	sort.Slice(manifest.Objects, func(left, right int) bool {
		leftObject, rightObject := manifest.Objects[left], manifest.Objects[right]
		if leftObject.Type != rightObject.Type {
			return leftObject.Type < rightObject.Type
		}
		if leftObject.ObjectID != rightObject.ObjectID {
			return leftObject.ObjectID < rightObject.ObjectID
		}
		return leftObject.ObjectVersionID < rightObject.ObjectVersionID
	})
	var lines bytes.Buffer
	for index, object := range manifest.Objects {
		if index > 0 {
			lines.WriteByte('\n')
		}
		fmt.Fprintf(&lines, "%s:%s:%s:%s", object.Type, object.ObjectID, object.ObjectVersionID, object.ContentHash)
	}
	manifest.ContentHash = askdata.HashBytes(lines.Bytes())
	return manifest, nil
}

func validReleaseObjectType(value ReleaseObjectType) bool {
	switch value {
	case ReleaseObjectDomain, ReleaseObjectEntity, ReleaseObjectSemanticModel,
		ReleaseObjectMeasure, ReleaseObjectMetric, ReleaseObjectDimension,
		ReleaseObjectMember, ReleaseObjectHierarchy, ReleaseObjectRelationship,
		ReleaseObjectQualityRule, ReleaseObjectBusinessTerm, ReleaseObjectExample:
		return true
	default:
		return false
	}
}

func ReleaseIdempotencyKey(tenantID, domainID, semanticVersion string, hash askdata.ContentHash) (askdata.ContentHash, error) {
	if _, err := uuid.Parse(tenantID); err != nil {
		return "", errors.New("tenant ID must be a UUID")
	}
	if _, err := uuid.Parse(domainID); err != nil {
		return "", errors.New("domain ID must be a UUID")
	}
	if strings.TrimSpace(semanticVersion) == "" {
		return "", errors.New("semantic version is required")
	}
	if err := hash.Validate(); err != nil {
		return "", err
	}
	return askdata.HashBytes([]byte(tenantID + "\x00" + domainID + "\x00" + semanticVersion + "\x00" + string(hash))), nil
}
