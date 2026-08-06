package graph

import (
	"context"
	"errors"
	"fmt"

	nebula "github.com/vesoft-inc/nebula-go/v3"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

// QueryExecutor is intentionally narrower than nebula-go's SessionPool. A
// Client can query one preconfigured Space but cannot switch spaces or expose
// an arbitrary query method to GraphPlan callers.
type QueryExecutor interface {
	ExecuteWithParameter(string, map[string]interface{}) (*nebula.ResultSet, error)
}

type Client struct {
	runner queryRunner
}

func NewClient(executor QueryExecutor) (*Client, error) {
	if executor == nil {
		return nil, errors.New("graph query executor is required")
	}
	return &Client{runner: sessionQueryRunner{executor: executor}}, nil
}

func newClientWithRunner(runner queryRunner) *Client {
	return &Client{runner: runner}
}

// Resolve executes only server-generated, bounded query templates. The
// returned GraphPlan contains stable semantic IDs and risk facts, never nGQL.
func (client *Client) Resolve(ctx context.Context, request PlanRequest) (GraphPlan, error) {
	if client == nil || client.runner == nil {
		return GraphPlan{}, errors.New("graph client is not initialized")
	}
	normalized, err := request.Normalize()
	if err != nil {
		return GraphPlan{}, err
	}
	if err := contextError(ctx); err != nil {
		return GraphPlan{}, err
	}

	metricQuery, err := buildMetricModelQuery(normalized)
	if err != nil {
		return GraphPlan{}, fmt.Errorf("%w: build metric model query", ErrInvalidPlanRequest)
	}
	metricRows, err := client.runner.Run(ctx, metricQuery)
	if err != nil {
		return GraphPlan{}, err
	}
	metricModels, models, err := parseMetricModels(normalized, metricRows)
	if err != nil {
		return GraphPlan{}, err
	}

	var compatibleDimensions []DimensionCompatibility
	if query, ok, buildErr := buildDimensionQuery(normalized, models); buildErr != nil {
		return GraphPlan{}, fmt.Errorf("%w: build dimension query", ErrInvalidPlanRequest)
	} else if ok {
		rows, runErr := client.runner.Run(ctx, query)
		if runErr != nil {
			return GraphPlan{}, runErr
		}
		compatibleDimensions, err = parseCompatibleDimensions(normalized, models, rows)
		if err != nil {
			return GraphPlan{}, err
		}
	}

	var memberOwnerships []MemberOwnership
	if query, ok, buildErr := buildMemberQuery(normalized); buildErr != nil {
		return GraphPlan{}, fmt.Errorf("%w: build member query", ErrInvalidPlanRequest)
	} else if ok {
		rows, runErr := client.runner.Run(ctx, query)
		if runErr != nil {
			return GraphPlan{}, runErr
		}
		memberOwnerships, err = parseMemberOwnerships(normalized, rows)
		if err != nil {
			return GraphPlan{}, err
		}
	}

	var joinPaths []JoinPath
	if query, ok, buildErr := buildJoinPathQuery(normalized, models); buildErr != nil {
		return GraphPlan{}, fmt.Errorf("%w: build join path query", ErrInvalidPlanRequest)
	} else if ok {
		rows, runErr := client.runner.Run(ctx, query)
		if runErr != nil {
			return GraphPlan{}, runErr
		}
		joinPaths, err = parseJoinPaths(normalized, models, rows)
		if err != nil {
			return GraphPlan{}, err
		}
	}
	planModels := append([]ObjectVersionRef(nil), models...)
	requestedModels := refIndex(normalized.ModelRefs)
	for _, path := range joinPaths {
		for _, step := range path.Steps {
			planModels = append(planModels, requestedModels[step.FromModelVersionID], requestedModels[step.ToModelVersionID])
		}
	}

	return NewGraphPlan(normalized, planModels, metricModels, compatibleDimensions, memberOwnerships, joinPaths)
}

type queryRow map[string]any

type queryRunner interface {
	Run(context.Context, compiledQuery) ([]queryRow, error)
}

type sessionQueryRunner struct {
	executor QueryExecutor
}

func (runner sessionQueryRunner) Run(ctx context.Context, query compiledQuery) ([]queryRow, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	result, err := runner.executor.ExecuteWithParameter(query.statement, query.parameters)
	if err != nil {
		return nil, fmt.Errorf("%w: transport", ErrGraphQueryFailed)
	}
	if result == nil {
		return nil, fmt.Errorf("%w: empty result", ErrGraphQueryFailed)
	}
	if !result.IsSucceed() {
		return nil, fmt.Errorf("%w: server code %d", ErrGraphQueryFailed, result.GetErrorCode())
	}
	if result.GetRowSize() > query.maxRows {
		return nil, fmt.Errorf("%w: row limit exceeded", ErrInvalidGraphResult)
	}
	rows := make([]queryRow, 0, result.GetRowSize())
	for index := 0; index < result.GetRowSize(); index++ {
		record, recordErr := result.GetRowValuesByIndex(index)
		if recordErr != nil {
			return nil, fmt.Errorf("%w: row %d", ErrInvalidGraphResult, index)
		}
		row, decodeErr := decodeResultRow(query.kind, record)
		if decodeErr != nil {
			return nil, fmt.Errorf("%w: row %d: %v", ErrInvalidGraphResult, index, decodeErr)
		}
		rows = append(rows, row)
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return rows, nil
}

func decodeResultRow(kind queryKind, record *nebula.Record) (queryRow, error) {
	if kind == queryJoinPaths {
		value, err := record.GetValueByColName("join_path")
		if err != nil {
			return nil, err
		}
		path, err := value.AsPath()
		if err != nil {
			return nil, err
		}
		decoded, err := decodeNebulaPath(path)
		if err != nil {
			return nil, err
		}
		return queryRow{"join_path": decoded}, nil
	}
	columns, ok := scalarColumns[kind]
	if !ok {
		return nil, errors.New("unsupported query result kind")
	}
	row := make(queryRow, len(columns))
	for name, columnType := range columns {
		value, err := record.GetValueByColName(name)
		if err != nil {
			return nil, err
		}
		switch columnType {
		case scalarString:
			row[name], err = value.AsString()
		case scalarInt:
			row[name], err = value.AsInt()
		default:
			err = errors.New("unsupported scalar result type")
		}
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", name, err)
		}
	}
	return row, nil
}

type scalarType uint8

const (
	scalarString scalarType = iota + 1
	scalarInt
)

var scalarColumns = map[queryKind]map[string]scalarType{
	queryMetricModels: {
		"tenant_id": scalarString, "domain_id": scalarString, "release_hash": scalarString,
		"metric_object_id": scalarString, "metric_version_id": scalarString, "metric_version": scalarInt,
		"model_object_id": scalarString, "model_version_id": scalarString, "model_version": scalarInt,
	},
	queryCompatibleDimensions: {
		"tenant_id": scalarString, "domain_id": scalarString, "release_hash": scalarString,
		"model_object_id": scalarString, "model_version_id": scalarString, "model_version": scalarInt,
		"dimension_object_id": scalarString, "dimension_version_id": scalarString, "dimension_version": scalarInt,
	},
	queryMemberOwnerships: {
		"tenant_id": scalarString, "domain_id": scalarString, "release_hash": scalarString,
		"member_object_id": scalarString, "member_version_id": scalarString, "member_version": scalarInt,
		"member_status":       scalarString,
		"dimension_object_id": scalarString, "dimension_version_id": scalarString, "dimension_version": scalarInt,
	},
}

type graphScope struct {
	tenantID    askdata.ID
	domainID    askdata.ID
	releaseHash askdata.ContentHash
}

type scopedJoinPath struct {
	scope graphScope
	path  JoinPath
}

func parseMetricModels(request PlanRequest, rows []queryRow) ([]MetricModelBinding, []ObjectVersionRef, error) {
	metricIndex := refIndex(request.MetricRefs)
	modelIndex := refIndex(request.ModelRefs)
	bindings := make([]MetricModelBinding, 0, len(rows))
	models := make([]ObjectVersionRef, 0, len(rows))
	for index, row := range rows {
		if err := validateRowScope(request, row); err != nil {
			return nil, nil, rowError(index, err)
		}
		metric, err := refFromRow(row, "metric")
		if err != nil || !sameRef(metricIndex[metric.VersionID], metric) {
			return nil, nil, rowError(index, errors.New("metric is outside requested candidates"))
		}
		model, err := refFromRow(row, "model")
		if err != nil || !sameRef(modelIndex[model.VersionID], model) {
			return nil, nil, rowError(index, errors.New("model is outside requested candidates"))
		}
		bindings = append(bindings, MetricModelBinding{MetricVersionID: metric.VersionID, ModelVersionID: model.VersionID})
		models = append(models, model)
	}
	return bindings, normalizedRefs(models), nil
}

func parseCompatibleDimensions(request PlanRequest, models []ObjectVersionRef, rows []queryRow) ([]DimensionCompatibility, error) {
	modelIndex := refIndex(models)
	dimensionIndex := refIndex(request.DimensionRefs)
	compatibilities := make([]DimensionCompatibility, 0, len(rows))
	for index, row := range rows {
		if err := validateRowScope(request, row); err != nil {
			return nil, rowError(index, err)
		}
		model, err := refFromRow(row, "model")
		if err != nil || !sameRef(modelIndex[model.VersionID], model) {
			return nil, rowError(index, errors.New("model is outside resolved models"))
		}
		dimension, err := refFromRow(row, "dimension")
		if err != nil || !sameRef(dimensionIndex[dimension.VersionID], dimension) {
			return nil, rowError(index, errors.New("dimension is outside requested candidates"))
		}
		compatibilities = append(compatibilities, DimensionCompatibility{
			ModelVersionID: model.VersionID, DimensionVersionID: dimension.VersionID,
		})
	}
	return compatibilities, nil
}

func parseMemberOwnerships(request PlanRequest, rows []queryRow) ([]MemberOwnership, error) {
	memberIndex := refIndex(request.MemberRefs)
	dimensionIndex := refIndex(request.DimensionRefs)
	ownerships := make([]MemberOwnership, 0, len(rows))
	for index, row := range rows {
		if err := validateRowScope(request, row); err != nil {
			return nil, rowError(index, err)
		}
		member, err := refFromRow(row, "member")
		if err != nil || !sameRef(memberIndex[member.VersionID], member) {
			return nil, rowError(index, errors.New("member is outside requested candidates"))
		}
		dimension, err := refFromRow(row, "dimension")
		if err != nil || !sameRef(dimensionIndex[dimension.VersionID], dimension) {
			return nil, rowError(index, errors.New("member owner is outside requested dimensions"))
		}
		status, ok := row["member_status"].(string)
		if !ok || (MemberStatus(status) != MemberStatusActive && MemberStatus(status) != MemberStatusExpired) {
			return nil, rowError(index, errors.New("member status is invalid"))
		}
		ownerships = append(ownerships, MemberOwnership{
			MemberVersionID: member.VersionID, DimensionVersionID: dimension.VersionID, Status: MemberStatus(status),
		})
	}
	return ownerships, nil
}

func parseJoinPaths(request PlanRequest, models []ObjectVersionRef, rows []queryRow) ([]JoinPath, error) {
	endpointIndex := refIndex(models)
	allowedIndex := refIndex(request.ModelRefs)
	paths := make([]JoinPath, 0, len(rows))
	for index, row := range rows {
		decoded, ok := row["join_path"].(scopedJoinPath)
		if !ok {
			return nil, rowError(index, errors.New("join path value is invalid"))
		}
		if decoded.scope.tenantID != request.Scope.TenantID || decoded.scope.domainID != request.DomainID ||
			decoded.scope.releaseHash != request.Scope.Release.ContentHash {
			return nil, rowError(index, errors.New("join path scope mismatch"))
		}
		if len(decoded.path.Steps) > request.MaxJoinHops {
			return nil, rowError(index, errors.New("join path exceeds requested hop limit"))
		}
		for _, step := range decoded.path.Steps {
			if _, exists := allowedIndex[step.FromModelVersionID]; !exists {
				return nil, rowError(index, errors.New("join path leaves requested model candidates"))
			}
			if _, exists := allowedIndex[step.ToModelVersionID]; !exists {
				return nil, rowError(index, errors.New("join path leaves requested model candidates"))
			}
		}
		if _, exists := endpointIndex[decoded.path.FromModelVersionID]; !exists {
			return nil, rowError(index, errors.New("join path starts outside metric-bound models"))
		}
		if _, exists := endpointIndex[decoded.path.ToModelVersionID]; !exists {
			return nil, rowError(index, errors.New("join path ends outside metric-bound models"))
		}
		if err := decoded.path.Validate(); err != nil {
			return nil, rowError(index, err)
		}
		paths = append(paths, decoded.path)
	}
	if len(paths) > request.MaxPaths {
		return nil, fmt.Errorf("%w: join path row limit exceeded", ErrInvalidGraphResult)
	}
	return paths, nil
}

func validateRowScope(request PlanRequest, row queryRow) error {
	tenant, tenantOK := row["tenant_id"].(string)
	domain, domainOK := row["domain_id"].(string)
	release, releaseOK := row["release_hash"].(string)
	if !tenantOK || !domainOK || !releaseOK || tenant != string(request.Scope.TenantID) ||
		domain != string(request.DomainID) || release != string(request.Scope.Release.ContentHash) {
		return errors.New("result scope mismatch")
	}
	return nil
}

func refFromRow(row queryRow, prefix string) (ObjectVersionRef, error) {
	objectID, objectOK := row[prefix+"_object_id"].(string)
	versionID, versionIDOK := row[prefix+"_version_id"].(string)
	version, versionNumberOK := row[prefix+"_version"].(int64)
	if !objectOK || !versionIDOK || !versionNumberOK || version > int64(^uint(0)>>1) {
		return ObjectVersionRef{}, errors.New("object reference columns are invalid")
	}
	ref := ObjectVersionRef{ObjectID: askdata.ID(objectID), VersionID: askdata.ID(versionID), Version: int(version)}
	if err := ref.Validate(); err != nil {
		return ObjectVersionRef{}, err
	}
	return ref, nil
}

func decodeNebulaPath(raw *nebula.PathWrapper) (scopedJoinPath, error) {
	if raw == nil || raw.GetPathLength() < 1 || raw.GetPathLength() > MaxJoinHops {
		return scopedJoinPath{}, errors.New("join path length is invalid")
	}
	nodes := raw.GetNodes()
	relationships := raw.GetRelationships()
	if len(nodes) != len(relationships)+1 {
		return scopedJoinPath{}, errors.New("join path node/edge shape is invalid")
	}
	refs := make([]ObjectVersionRef, len(nodes))
	vids := make([]string, len(nodes))
	var scope graphScope
	for index, node := range nodes {
		properties, err := node.Properties(string(ObjectTypeSemanticModel))
		if err != nil {
			return scopedJoinPath{}, err
		}
		ref, nodeScope, err := refAndScopeFromProperties(properties)
		if err != nil {
			return scopedJoinPath{}, err
		}
		vid, err := node.GetID().AsString()
		if err != nil {
			return scopedJoinPath{}, err
		}
		expectedVID, err := BuildVID(nodeScope.tenantID, ObjectTypeSemanticModel, ref)
		if err != nil || vid != expectedVID {
			return scopedJoinPath{}, errors.New("join path vertex VID does not match properties")
		}
		if index == 0 {
			scope = nodeScope
		} else if nodeScope != scope {
			return scopedJoinPath{}, errors.New("join path crosses graph scope")
		}
		refs[index], vids[index] = ref, vid
	}
	steps := make([]JoinStep, 0, len(relationships))
	for index, relationship := range relationships {
		if relationship.GetEdgeName() != "JOINS_TO" {
			return scopedJoinPath{}, errors.New("join path contains an unsupported edge")
		}
		properties := relationship.Properties()
		edgeScope, err := scopeFromProperties(properties)
		if err != nil || edgeScope != scope {
			return scopedJoinPath{}, errors.New("join edge scope mismatch")
		}
		certified, err := requiredBoolProperty(properties, "certified")
		if err != nil || !certified {
			return scopedJoinPath{}, errors.New("join edge is not certified")
		}
		relationshipID, err := requiredIDProperty(properties, "relationship_version_id")
		if err != nil {
			return scopedJoinPath{}, err
		}
		joinType, err := requiredStringProperty(properties, "join_type")
		if err != nil {
			return scopedJoinPath{}, err
		}
		cardinality, err := requiredStringProperty(properties, "cardinality")
		if err != nil {
			return scopedJoinPath{}, err
		}
		fanout, err := requiredStringProperty(properties, "fanout_policy")
		if err != nil {
			return scopedJoinPath{}, err
		}
		sourceVID, sourceErr := relationship.GetSrcVertexID().AsString()
		destinationVID, destinationErr := relationship.GetDstVertexID().AsString()
		if sourceErr != nil || destinationErr != nil {
			return scopedJoinPath{}, errors.New("join edge VID is invalid")
		}
		direction := TraversalForward
		switch {
		case sourceVID == vids[index] && destinationVID == vids[index+1]:
		case sourceVID == vids[index+1] && destinationVID == vids[index]:
			direction = TraversalReverse
		default:
			return scopedJoinPath{}, errors.New("join edge is disconnected from path nodes")
		}
		steps = append(steps, JoinStep{
			Hop: index + 1, RelationshipVersionID: relationshipID,
			FromModelVersionID: refs[index].VersionID, ToModelVersionID: refs[index+1].VersionID,
			Direction: direction, JoinType: registry.JoinType(joinType),
			Cardinality: registry.Cardinality(cardinality), FanoutPolicy: registry.FanoutPolicy(fanout),
		})
	}
	path, err := NewJoinPath(steps)
	if err != nil {
		return scopedJoinPath{}, err
	}
	return scopedJoinPath{scope: scope, path: path}, nil
}

func refAndScopeFromProperties(properties map[string]*nebula.ValueWrapper) (ObjectVersionRef, graphScope, error) {
	objectID, err := requiredIDProperty(properties, "object_id")
	if err != nil {
		return ObjectVersionRef{}, graphScope{}, err
	}
	versionID, err := requiredIDProperty(properties, "version_id")
	if err != nil {
		return ObjectVersionRef{}, graphScope{}, err
	}
	versionValue, err := requiredIntProperty(properties, "version_no")
	if err != nil || versionValue > int64(^uint(0)>>1) {
		return ObjectVersionRef{}, graphScope{}, errors.New("version property is invalid")
	}
	ref := ObjectVersionRef{ObjectID: objectID, VersionID: versionID, Version: int(versionValue)}
	if err := ref.Validate(); err != nil {
		return ObjectVersionRef{}, graphScope{}, err
	}
	scope, err := scopeFromProperties(properties)
	return ref, scope, err
}

func scopeFromProperties(properties map[string]*nebula.ValueWrapper) (graphScope, error) {
	tenantID, err := requiredIDProperty(properties, "tenant_id")
	if err != nil {
		return graphScope{}, err
	}
	domainID, err := requiredIDProperty(properties, "domain_id")
	if err != nil {
		return graphScope{}, err
	}
	release, err := requiredStringProperty(properties, "release_hash")
	if err != nil {
		return graphScope{}, err
	}
	hash := askdata.ContentHash(release)
	if err := hash.Validate(); err != nil {
		return graphScope{}, err
	}
	return graphScope{tenantID: tenantID, domainID: domainID, releaseHash: hash}, nil
}

func requiredStringProperty(properties map[string]*nebula.ValueWrapper, name string) (string, error) {
	value, exists := properties[name]
	if !exists || value == nil {
		return "", fmt.Errorf("property %s is missing", name)
	}
	result, err := value.AsString()
	if err != nil {
		return "", fmt.Errorf("property %s is invalid", name)
	}
	return result, nil
}

func requiredIDProperty(properties map[string]*nebula.ValueWrapper, name string) (askdata.ID, error) {
	value, err := requiredStringProperty(properties, name)
	if err != nil {
		return "", err
	}
	id := askdata.ID(value)
	if err := id.Validate(); err != nil {
		return "", fmt.Errorf("property %s is invalid", name)
	}
	return id, nil
}

func requiredIntProperty(properties map[string]*nebula.ValueWrapper, name string) (int64, error) {
	value, exists := properties[name]
	if !exists || value == nil {
		return 0, fmt.Errorf("property %s is missing", name)
	}
	result, err := value.AsInt()
	if err != nil {
		return 0, fmt.Errorf("property %s is invalid", name)
	}
	return result, nil
}

func requiredBoolProperty(properties map[string]*nebula.ValueWrapper, name string) (bool, error) {
	value, exists := properties[name]
	if !exists || value == nil {
		return false, fmt.Errorf("property %s is missing", name)
	}
	result, err := value.AsBool()
	if err != nil {
		return false, fmt.Errorf("property %s is invalid", name)
	}
	return result, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func rowError(index int, err error) error {
	return fmt.Errorf("%w: row %d: %v", ErrInvalidGraphResult, index, err)
}

func sameRef(left, right ObjectVersionRef) bool {
	return left == right && right.VersionID != ""
}
