package semanticgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var objectTag = map[string]string{
	"DOMAIN": "domain", "BUSINESS_TERM": "business_term",
	"ENTITY": "entity", "SEMANTIC_MODEL": "dataset", "MEASURE": "metric",
	"METRIC": "metric", "DIMENSION": "dimension", "DIMENSION_VALUE": "dimension_value",
	"TIME": "dimension", "COHORT": "dimension", "DATASET": "dataset",
	"TABLE_COLUMN": "table_column", "POLICY": "role", "QUALITY_RULE": "quality_rule",
	"CERTIFIED_EXAMPLE": "certified_example", "PARSING_RULE": "business_term",
}

var allowedEdgeTypes = map[string]bool{
	"contains": true, "measures": true, "depends_on": true,
	"sourced_from": true, "groupable_by": true, "belongs_to": true,
	"has_value": true, "joins_to": true, "synonym_of": true,
	"can_access": true, "derived_from": true, "guards": true, "uses": true,
}

var stableVIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type ReleaseObject struct {
	ObjectType    string
	ObjectID      string
	ObjectVersion string
	DomainID      string
	ContentHash   string
	Certification string
	Sensitivity   string
	ValidFrom     time.Time
	ValidTo       *time.Time
	Contract      json.RawMessage
}

type ReleaseManifest struct {
	TenantID        string
	ReleaseID       string
	SemanticVersion string
	ContentHash     string
	Objects         []ReleaseObject
}

type Vertex struct {
	VID   string
	Tag   string
	Props map[string]any
}

type Edge struct {
	RelationID string
	FromVID    string
	ToVID      string
	Rank       int64
	Type       string
	Props      map[string]any
}

type Projection struct {
	SemanticVersion string
	ContentHash     string
	ObjectCount     int
	Vertices        []Vertex
	Edges           []Edge
}

type ProjectionVerification struct {
	VertexCount int `json:"vertexCount"`
	EdgeCount   int `json:"edgeCount"`
	OrphanCount int `json:"orphanCount"`
}

type ProjectionSink interface {
	UpsertVertex(context.Context, Vertex) error
	UpsertEdge(context.Context, Edge) error
	Verify(context.Context, Projection) (ProjectionVerification, error)
}

type Projector struct{ sink ProjectionSink }

func NewProjector(sink ProjectionSink) *Projector { return &Projector{sink: sink} }

func (projector *Projector) Project(
	ctx context.Context,
	manifest ReleaseManifest,
) (ProjectionVerification, error) {
	if projector == nil || projector.sink == nil {
		return ProjectionVerification{}, ErrInvalidRequest
	}
	projection, err := BuildProjection(manifest)
	if err != nil {
		return ProjectionVerification{}, err
	}
	for _, vertex := range projection.Vertices {
		if err := ctx.Err(); err != nil {
			return ProjectionVerification{}, err
		}
		if err := projector.sink.UpsertVertex(ctx, vertex); err != nil {
			return ProjectionVerification{}, err
		}
	}
	for _, edge := range projection.Edges {
		if err := ctx.Err(); err != nil {
			return ProjectionVerification{}, err
		}
		if err := projector.sink.UpsertEdge(ctx, edge); err != nil {
			return ProjectionVerification{}, err
		}
	}
	verification, err := projector.sink.Verify(ctx, projection)
	if err != nil {
		return ProjectionVerification{}, err
	}
	if verification.VertexCount != len(projection.Vertices) ||
		verification.EdgeCount != len(projection.Edges) || verification.OrphanCount != 0 {
		return verification, fmt.Errorf("%w: projection verification mismatch", ErrNoCertifiedPath)
	}
	return verification, nil
}

func BuildProjection(manifest ReleaseManifest) (Projection, error) {
	if strings.TrimSpace(manifest.TenantID) == "" ||
		strings.TrimSpace(manifest.SemanticVersion) == "" ||
		len(manifest.ContentHash) != 64 || len(manifest.Objects) == 0 {
		return Projection{}, ErrInvalidRequest
	}
	projection := Projection{
		SemanticVersion: manifest.SemanticVersion, ContentHash: manifest.ContentHash,
		ObjectCount: len(manifest.Objects), Vertices: []Vertex{}, Edges: []Edge{},
	}
	objectIndexes := map[string][]int{}
	contracts := make([]map[string]any, len(manifest.Objects))
	for index, object := range manifest.Objects {
		object.ObjectType = strings.ToUpper(strings.TrimSpace(object.ObjectType))
		tag, projected := objectTag[object.ObjectType]
		if object.ObjectType == "RELATION" {
			projected = false
		}
		if object.ObjectID == "" || object.ObjectVersion == "" ||
			object.Certification != "CERTIFIED" {
			return Projection{}, fmt.Errorf("%w: uncertified or incomplete object", ErrInvalidRequest)
		}
		var contract map[string]any
		if err := json.Unmarshal(object.Contract, &contract); err != nil || contract == nil {
			return Projection{}, fmt.Errorf("%w: invalid contract", ErrInvalidRequest)
		}
		contracts[index] = contract
		if !projected {
			if object.ObjectType != "RELATION" {
				return Projection{}, fmt.Errorf("%w: unsupported object type %s", ErrInvalidRequest, object.ObjectType)
			}
			continue
		}
		stableObjectID := object.ObjectID
		if object.ObjectType == "DIMENSION_VALUE" {
			dimensionID := stringValue(contract["dimensionId"])
			if dimensionID == "" {
				return Projection{}, fmt.Errorf("%w: dimension value has no scoped dimension", ErrInvalidRequest)
			}
			stableObjectID = dimensionID + "::" + object.ObjectID
		}
		vid := StableVID(manifest.TenantID, tag, stableObjectID, object.ObjectVersion)
		contractJSON, _ := json.Marshal(contract)
		vertex := Vertex{VID: vid, Tag: tag, Props: map[string]any{
			"object_id": object.ObjectID, "object_version": object.ObjectVersion,
			"object_type": object.ObjectType, "domain_id": object.DomainID,
			"tenant_scope": manifest.TenantID, "status": "certified",
			"sensitivity": object.Sensitivity, "semantic_version": manifest.SemanticVersion,
			"content_hash": object.ContentHash, "valid_from": object.ValidFrom.UTC().Unix(),
			"valid_to": unixOrZero(object.ValidTo), "contract_json": string(contractJSON),
		}}
		objectIndexes[object.ObjectID] = append(objectIndexes[object.ObjectID], len(projection.Vertices))
		projection.Vertices = append(projection.Vertices, vertex)
	}

	edgeKeys := map[string]bool{}
	addEdge := func(relationID, relationType, fromRef, toRef string, contract map[string]any) error {
		relationType = normalizeRelationType(relationType)
		if !allowedEdgeTypes[relationType] {
			return fmt.Errorf("%w: unsupported relation type %s", ErrInvalidRequest, relationType)
		}
		from, err := resolveProjectionVertex(projection.Vertices, objectIndexes, fromRef, stringValue(contract["fromType"]))
		if err != nil {
			return err
		}
		to, err := resolveProjectionVertex(projection.Vertices, objectIndexes, toRef, stringValue(contract["toType"]))
		if err != nil {
			return err
		}
		if from.VID == to.VID {
			return fmt.Errorf("%w: self relation", ErrInvalidRequest)
		}
		key := relationType + "\x00" + from.VID + "\x00" + to.VID + "\x00" + relationID
		if edgeKeys[key] {
			return nil
		}
		edgeKeys[key] = true
		attributes, _ := json.Marshal(contract)
		certified, exists := boolValue(contract["certified"])
		if !exists {
			certified = true
		}
		allowed, exists := boolValue(contract["allowedForQuery"])
		if !exists {
			allowed = certified
		}
		projection.Edges = append(projection.Edges, Edge{
			RelationID: relationID, FromVID: from.VID, ToVID: to.VID,
			Rank: stableRank(relationID), Type: relationType,
			Props: map[string]any{
				"relation_id": relationID, "tenant_scope": manifest.TenantID,
				"certified": certified, "allowed_for_query": allowed,
				"cardinality":          defaultString(stringValue(contract["cardinality"]), "not_applicable"),
				"base_cost":            defaultFloat(numberValue(contract["baseCost"]), defaultBaseCost(contract)),
				"fanout_penalty":       numberValue(contract["fanoutPenalty"]),
				"stale_penalty":        numberValue(contract["staleDatasetPenalty"]),
				"cross_source_penalty": numberValue(contract["crossSourcePenalty"]),
				"policy_penalty":       numberValue(contract["policyComplexityPenalty"]),
				"semantic_version":     manifest.SemanticVersion,
				"effective_from":       unixFromContract(contract["effectiveFrom"]),
				"effective_to":         unixFromContract(contract["effectiveTo"]),
				"attributes_json":      string(attributes),
			},
		})
		return nil
	}

	for index, object := range manifest.Objects {
		contract := contracts[index]
		if object.ObjectType == "RELATION" {
			if err := addEdge(object.ObjectID, stringValue(contract["relationType"]),
				stringValue(contract["fromId"]), stringValue(contract["toId"]), contract); err != nil {
				return Projection{}, err
			}
			continue
		}
		for _, inferred := range inferredRelations(object, contract) {
			if err := addEdge(inferred.id, inferred.relationType, inferred.fromRef, inferred.toRef, inferred.contract); err != nil {
				return Projection{}, err
			}
		}
	}
	sort.Slice(projection.Vertices, func(left, right int) bool { return projection.Vertices[left].VID < projection.Vertices[right].VID })
	sort.Slice(projection.Edges, func(left, right int) bool {
		if projection.Edges[left].Type != projection.Edges[right].Type {
			return projection.Edges[left].Type < projection.Edges[right].Type
		}
		if projection.Edges[left].FromVID != projection.Edges[right].FromVID {
			return projection.Edges[left].FromVID < projection.Edges[right].FromVID
		}
		return projection.Edges[left].RelationID < projection.Edges[right].RelationID
	})
	return projection, nil
}

type inferredRelation struct {
	id, relationType, fromRef, toRef string
	contract                         map[string]any
}

func inferredRelations(object ReleaseObject, contract map[string]any) []inferredRelation {
	relations := []inferredRelation{}
	appendMany := func(relationType, field, fromRef, fromType, toType string) {
		for _, target := range stringSlice(contract[field]) {
			relations = append(relations, inferredRelation{
				id:           object.ObjectID + ":" + relationType + ":" + target,
				relationType: relationType, fromRef: fromRef, toRef: target,
				contract: map[string]any{"certified": true, "allowedForQuery": true,
					"fromType": fromType, "toType": toType},
			})
		}
	}
	switch object.ObjectType {
	case "METRIC", "MEASURE":
		appendMany("sourced_from", "sourceDatasetIds", object.ObjectID, object.ObjectType, "DATASET")
		appendMany("groupable_by", "groupableDimensionIds", object.ObjectID, object.ObjectType, "DIMENSION")
		if target := stringValue(contract["defaultTimeDimensionId"]); target != "" {
			relations = append(relations, inferredRelation{
				id:           object.ObjectID + ":groupable_by:" + target,
				relationType: "groupable_by", fromRef: object.ObjectID, toRef: target,
				contract: map[string]any{
					"certified": true, "allowedForQuery": true,
					"fromType": object.ObjectType, "toType": "TIME",
				},
			})
		}
		appendMany("depends_on", "dependsOnMetricIds", object.ObjectID, object.ObjectType, "METRIC")
		for _, policyID := range stringSlice(contract["permissionPolicyIds"]) {
			relations = append(relations, inferredRelation{
				id:           policyID + ":can_access:" + object.ObjectID,
				relationType: "can_access", fromRef: policyID, toRef: object.ObjectID,
				contract: map[string]any{"certified": true, "allowedForQuery": true,
					"fromType": "POLICY", "toType": object.ObjectType},
			})
		}
	case "DIMENSION_VALUE":
		if target := stringValue(contract["dimensionId"]); target != "" {
			relations = append(relations, inferredRelation{id: target + ":has_value:" + object.ObjectID,
				relationType: "has_value", fromRef: target, toRef: object.ObjectID,
				contract: map[string]any{"certified": true, "allowedForQuery": true,
					"hierarchyLevel": contract["hierarchyLevel"]}})
		}
	case "DIMENSION", "TIME", "COHORT":
		if target := stringValue(contract["entityId"]); target != "" {
			relations = append(relations, inferredRelation{id: object.ObjectID + ":belongs_to:" + target,
				relationType: "belongs_to", fromRef: object.ObjectID, toRef: target,
				contract: map[string]any{"certified": true, "allowedForQuery": true}})
		}
	case "QUALITY_RULE":
		if target := stringValue(contract["targetId"]); target != "" {
			relations = append(relations, inferredRelation{id: object.ObjectID + ":guards:" + target,
				relationType: "guards", fromRef: object.ObjectID, toRef: target,
				contract: map[string]any{"certified": true, "allowedForQuery": true}})
		}
	case "POLICY":
		appendMany("can_access", "accessibleObjectIds", object.ObjectID, "POLICY", "")
	case "CERTIFIED_EXAMPLE":
		appendMany("uses", "objectIds", object.ObjectID, "CERTIFIED_EXAMPLE", "")
	}
	return relations
}

func StableVID(tenantID, tag, objectID, version string) string {
	tenantHash := sha256.Sum256([]byte(strings.TrimSpace(tenantID)))
	prefix := strings.ToLower(strings.TrimSpace(tag)) + ":" + hex.EncodeToString(tenantHash[:6]) + ":"
	vid := prefix + strings.TrimSpace(objectID) + ":" + strings.TrimSpace(version)
	if stableVIDPattern.MatchString(vid) {
		return vid
	}
	hash := sha256.Sum256([]byte(strings.TrimSpace(tenantID) + "\x00" + tag + "\x00" + objectID + "\x00" + version))
	return prefix + hex.EncodeToString(hash[:])
}

func resolveProjectionVertex(vertices []Vertex, indexes map[string][]int, ref, objectType string) (Vertex, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Vertex{}, fmt.Errorf("%w: empty relation endpoint", ErrInvalidRequest)
	}
	if separator := strings.IndexByte(ref, ':'); separator > 0 {
		possibleType := strings.ToUpper(ref[:separator])
		if _, known := objectTag[possibleType]; known {
			objectType, ref = possibleType, ref[separator+1:]
		}
	}
	candidates := indexes[ref]
	if objectType != "" {
		objectType = strings.ToUpper(strings.TrimSpace(objectType))
		filtered := candidates[:0]
		for _, index := range candidates {
			if vertices[index].Props["object_type"] == objectType {
				filtered = append(filtered, index)
			}
		}
		candidates = filtered
	}
	if len(candidates) != 1 {
		return Vertex{}, fmt.Errorf("%w: relation endpoint %s is missing or ambiguous", ErrInvalidRequest, ref)
	}
	return vertices[candidates[0]], nil
}

func normalizeRelationType(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, "-", "_")))
}

func stableRank(value string) int64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(value))
	return int64(hash.Sum64() & uint64(^uint64(0)>>1))
}

func unixOrZero(value *time.Time) int64 {
	if value == nil {
		return 0
	}
	return value.UTC().Unix()
}

func unixFromContract(value any) int64 {
	text := stringValue(value)
	if text == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return 0
	}
	return parsed.UTC().Unix()
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text := stringValue(item); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func boolValue(value any) (bool, bool) {
	result, ok := value.(bool)
	return result, ok
}

func numberValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case json.Number:
		result, _ := typed.Float64()
		return result
	case string:
		result, _ := strconv.ParseFloat(typed, 64)
		return result
	default:
		return 0
	}
}

func defaultFloat(value, fallback float64) float64 {
	if value > 0 {
		return value
	}
	return fallback
}

func defaultBaseCost(contract map[string]any) float64 {
	switch strings.ToLower(stringValue(contract["cardinality"])) {
	case "one_to_one":
		return 0.5
	case "many_to_one":
		return 1
	case "many_to_many":
		return 4
	default:
		return 1
	}
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
