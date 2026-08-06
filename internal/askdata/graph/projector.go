package graph

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	nebula "github.com/vesoft-inc/nebula-go/v3"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

const (
	ProjectionTargetNebula       = "NEBULA_GRAPH"
	GraphProjectionSchemaVersion = "askdata-nebula-projection-v1"
	DefaultProjectionLease       = 2 * time.Minute
	maxProjectionVertices        = 10_000
	maxProjectionEdges           = 30_000
	projectionMutationBatchSize  = 128
)

var (
	ErrInvalidProjectionWork = errors.New("askdata graph projection work is invalid")
	ErrProjectionLeaseLost   = errors.New("askdata graph projection lease was lost")
	ErrProjectionContract    = errors.New("askdata graph projection contract is invalid")
	ErrProjectionMutation    = errors.New("askdata graph projection mutation failed")
)

type ProjectionClaim struct {
	ProjectionID, TenantID, DomainID, ReleaseID string
	Target, SemanticVersion, ContentHash        string
	LeaseToken                                  string
	Attempt                                     int
}

type ProjectionEdgeType string

const (
	ProjectionEdgeModeledBy    ProjectionEdgeType = "MODELED_BY"
	ProjectionEdgeHasDimension ProjectionEdgeType = "HAS_DIMENSION"
	ProjectionEdgeHasMember    ProjectionEdgeType = "HAS_MEMBER"
	ProjectionEdgeJoinsTo      ProjectionEdgeType = "JOINS_TO"
)

type ProjectionVertex struct {
	Type         ObjectType       `json:"type"`
	Ref          ObjectVersionRef `json:"ref"`
	MemberStatus MemberStatus     `json:"memberStatus,omitempty"`
}

type ProjectionEdge struct {
	Type                  ProjectionEdgeType    `json:"type"`
	FromType              ObjectType            `json:"fromType"`
	From                  ObjectVersionRef      `json:"from"`
	ToType                ObjectType            `json:"toType"`
	To                    ObjectVersionRef      `json:"to"`
	RelationshipVersionID askdata.ID            `json:"relationshipVersionId,omitempty"`
	JoinType              registry.JoinType     `json:"joinType,omitempty"`
	Cardinality           registry.Cardinality  `json:"cardinality,omitempty"`
	FanoutPolicy          registry.FanoutPolicy `json:"fanoutPolicy,omitempty"`
	Certified             bool                  `json:"certified,omitempty"`
}

type ProjectionSnapshot struct {
	TenantID        askdata.ID          `json:"tenantId"`
	DomainID        askdata.ID          `json:"domainId"`
	ReleaseID       askdata.ID          `json:"releaseId"`
	SemanticVersion string              `json:"semanticVersion"`
	ContentHash     askdata.ContentHash `json:"contentHash"`
	ManifestCount   int                 `json:"manifestCount"`
	Vertices        []ProjectionVertex  `json:"vertices"`
	Edges           []ProjectionEdge    `json:"edges"`
}

type ProjectionProof struct {
	SchemaVersion string              `json:"schemaVersion"`
	GraphHash     askdata.ContentHash `json:"graphHash"`
	ObjectCount   int                 `json:"objectCount"`
	VertexCount   int                 `json:"vertexCount"`
	EdgeCount     int                 `json:"edgeCount"`
}

type ProjectionStore interface {
	ListTenantIDs(context.Context) ([]string, error)
	Claim(context.Context, string, string, time.Duration) (*ProjectionClaim, error)
	LoadGraphSnapshot(context.Context, ProjectionClaim, string) (ProjectionSnapshot, error)
	Heartbeat(context.Context, ProjectionClaim, string, time.Duration) error
	Complete(context.Context, ProjectionClaim, string, ProjectionProof) error
	Fail(context.Context, ProjectionClaim, string, string, bool) error
}

type ProjectionWriter interface {
	Apply(context.Context, ProjectionSnapshot, func(context.Context) error) (ProjectionProof, error)
}

type Projector struct {
	store  ProjectionStore
	writer ProjectionWriter
}

func NewProjector(store ProjectionStore, writer ProjectionWriter) (*Projector, error) {
	if store == nil || writer == nil {
		return nil, ErrInvalidProjectionWork
	}
	return &Projector{store: store, writer: writer}, nil
}

func (projector *Projector) TenantIDs(ctx context.Context) ([]string, error) {
	if projector == nil || projector.store == nil {
		return nil, ErrInvalidProjectionWork
	}
	return projector.store.ListTenantIDs(ctx)
}

func (projector *Projector) ProcessNext(
	ctx context.Context, tenantID, workerID string, lease time.Duration,
) (bool, error) {
	if projector == nil || projector.store == nil || projector.writer == nil ||
		uuid.Validate(tenantID) != nil || strings.TrimSpace(workerID) == "" || len(workerID) > 128 ||
		lease < 30*time.Second || lease > 10*time.Minute {
		return false, ErrInvalidProjectionWork
	}
	claim, err := projector.store.Claim(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	if err := validateProjectionClaim(*claim, tenantID); err != nil {
		return true, errors.Join(err, projector.store.Fail(
			ctx, *claim, workerID, "GRAPH_PROJECTION_CLAIM_INVALID", false,
		))
	}
	snapshot, err := projector.store.LoadGraphSnapshot(ctx, *claim, workerID)
	if err != nil {
		return true, errors.Join(err, projector.store.Fail(
			ctx, *claim, workerID, "GRAPH_PROJECTION_SNAPSHOT_FAILED", !errors.Is(err, ErrProjectionContract),
		))
	}
	if err := snapshot.Validate(); err != nil {
		return true, errors.Join(err, projector.store.Fail(
			ctx, *claim, workerID, "GRAPH_PROJECTION_CONTRACT_INVALID", false,
		))
	}
	heartbeat := func(heartbeatCtx context.Context) error {
		return projector.store.Heartbeat(heartbeatCtx, *claim, workerID, lease)
	}
	proof, err := projector.writer.Apply(ctx, snapshot, heartbeat)
	if err != nil {
		if errors.Is(err, ErrProjectionLeaseLost) {
			return true, err
		}
		code, retryable := projectionFailure(err)
		return true, errors.Join(err, projector.store.Fail(ctx, *claim, workerID, code, retryable))
	}
	expected, err := snapshot.Proof()
	if err != nil || proof != expected {
		contractErr := fmt.Errorf("%w: writer proof does not match the canonical snapshot", ErrProjectionContract)
		return true, errors.Join(err, contractErr, projector.store.Fail(
			ctx, *claim, workerID, "GRAPH_PROJECTION_PROOF_MISMATCH", false,
		))
	}
	if err := projector.store.Complete(ctx, *claim, workerID, proof); err != nil {
		return true, err
	}
	return true, nil
}

func validateProjectionClaim(claim ProjectionClaim, tenantID string) error {
	if uuid.Validate(claim.ProjectionID) != nil || uuid.Validate(claim.TenantID) != nil ||
		uuid.Validate(claim.DomainID) != nil || uuid.Validate(claim.ReleaseID) != nil ||
		uuid.Validate(claim.LeaseToken) != nil || claim.TenantID != tenantID ||
		claim.Target != ProjectionTargetNebula || claim.Attempt < 1 || claim.Attempt > 20 ||
		strings.TrimSpace(claim.SemanticVersion) == "" || len(claim.SemanticVersion) > 128 ||
		askdata.ContentHash(claim.ContentHash).Validate() != nil {
		return ErrInvalidProjectionWork
	}
	return nil
}

func projectionFailure(err error) (string, bool) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "GRAPH_PROJECTION_CANCELLED", true
	case errors.Is(err, ErrProjectionContract), errors.Is(err, ErrInvalidProjectionWork):
		return "GRAPH_PROJECTION_CONTRACT_INVALID", false
	default:
		return "NEBULA_PROJECTION_UNAVAILABLE", true
	}
}

func (snapshot ProjectionSnapshot) Validate() error {
	if snapshot.TenantID.Validate() != nil || snapshot.DomainID.Validate() != nil ||
		snapshot.ReleaseID.Validate() != nil || strings.TrimSpace(snapshot.SemanticVersion) == "" ||
		len(snapshot.SemanticVersion) > 128 || snapshot.ContentHash.Validate() != nil ||
		snapshot.ManifestCount < 1 || snapshot.ManifestCount > 10_000 ||
		len(snapshot.Vertices) > maxProjectionVertices || len(snapshot.Edges) > maxProjectionEdges {
		return ErrProjectionContract
	}
	vertices := make(map[string]struct{}, len(snapshot.Vertices))
	for index, vertex := range snapshot.Vertices {
		if err := vertex.Type.Validate(); err != nil || vertex.Ref.Validate() != nil {
			return fmt.Errorf("%w: vertices[%d]", ErrProjectionContract, index)
		}
		if vertex.Type == ObjectTypeMember {
			if vertex.MemberStatus != MemberStatusActive && vertex.MemberStatus != MemberStatusExpired {
				return fmt.Errorf("%w: vertices[%d] has invalid member status", ErrProjectionContract, index)
			}
		} else if vertex.MemberStatus != "" {
			return fmt.Errorf("%w: vertices[%d] has unexpected member status", ErrProjectionContract, index)
		}
		vid, err := BuildVID(snapshot.TenantID, vertex.Type, vertex.Ref)
		if err != nil {
			return fmt.Errorf("%w: vertices[%d]: %v", ErrProjectionContract, index, err)
		}
		key := string(vertex.Type) + "\x00" + vid
		if _, exists := vertices[key]; exists {
			return fmt.Errorf("%w: duplicate vertex", ErrProjectionContract)
		}
		vertices[key] = struct{}{}
	}
	edges := make(map[string]struct{}, len(snapshot.Edges))
	for index, edge := range snapshot.Edges {
		if err := edge.Validate(); err != nil {
			return fmt.Errorf("%w: edges[%d]: %v", ErrProjectionContract, index, err)
		}
		fromVID, err := BuildVID(snapshot.TenantID, edge.FromType, edge.From)
		if err != nil {
			return fmt.Errorf("%w: edges[%d].from", ErrProjectionContract, index)
		}
		toVID, err := BuildVID(snapshot.TenantID, edge.ToType, edge.To)
		if err != nil {
			return fmt.Errorf("%w: edges[%d].to", ErrProjectionContract, index)
		}
		if _, exists := vertices[string(edge.FromType)+"\x00"+fromVID]; !exists {
			return fmt.Errorf("%w: edges[%d] source is not released", ErrProjectionContract, index)
		}
		if _, exists := vertices[string(edge.ToType)+"\x00"+toVID]; !exists {
			return fmt.Errorf("%w: edges[%d] target is not released", ErrProjectionContract, index)
		}
		key := string(edge.Type) + "\x00" + fromVID + "\x00" + toVID + "\x00" + string(edge.RelationshipVersionID)
		if _, exists := edges[key]; exists {
			return fmt.Errorf("%w: duplicate edge", ErrProjectionContract)
		}
		edges[key] = struct{}{}
	}
	return nil
}

func (edge ProjectionEdge) Validate() error {
	if edge.From.Validate() != nil || edge.To.Validate() != nil || edge.FromType.Validate() != nil || edge.ToType.Validate() != nil {
		return errors.New("invalid endpoint")
	}
	switch edge.Type {
	case ProjectionEdgeModeledBy:
		if edge.FromType != ObjectTypeMetric || edge.ToType != ObjectTypeSemanticModel {
			return errors.New("MODELED_BY endpoint types are invalid")
		}
	case ProjectionEdgeHasDimension:
		if edge.FromType != ObjectTypeSemanticModel || edge.ToType != ObjectTypeDimension {
			return errors.New("HAS_DIMENSION endpoint types are invalid")
		}
	case ProjectionEdgeHasMember:
		if edge.FromType != ObjectTypeDimension || edge.ToType != ObjectTypeMember {
			return errors.New("HAS_MEMBER endpoint types are invalid")
		}
	case ProjectionEdgeJoinsTo:
		if edge.FromType != ObjectTypeSemanticModel || edge.ToType != ObjectTypeSemanticModel ||
			edge.RelationshipVersionID.Validate() != nil ||
			!validJoinType(edge.JoinType) || !validCardinality(edge.Cardinality) ||
			!validFanoutPolicy(edge.FanoutPolicy) || !edge.Certified {
			return errors.New("JOINS_TO contract is invalid")
		}
	default:
		return errors.New("unsupported edge type")
	}
	if edge.Type != ProjectionEdgeJoinsTo && (edge.RelationshipVersionID != "" || edge.JoinType != "" ||
		edge.Cardinality != "" || edge.FanoutPolicy != "" || edge.Certified) {
		return errors.New("non-join edge carries join properties")
	}
	return nil
}

func validJoinType(value registry.JoinType) bool {
	return value == registry.JoinInner || value == registry.JoinLeft || value == registry.JoinRight || value == registry.JoinFull
}

func validCardinality(value registry.Cardinality) bool {
	return value == registry.CardinalityOneToOne || value == registry.CardinalityManyToOne ||
		value == registry.CardinalityOneToMany || value == registry.CardinalityManyToMany
}

func validFanoutPolicy(value registry.FanoutPolicy) bool {
	return value == registry.FanoutBlock || value == registry.FanoutCertifiedPre || value == registry.FanoutSafe
}

func (snapshot ProjectionSnapshot) Proof() (ProjectionProof, error) {
	normalized := snapshot.normalized()
	if err := normalized.Validate(); err != nil {
		return ProjectionProof{}, err
	}
	hash, _, err := registry.CanonicalContentHash(struct {
		SchemaVersion string              `json:"schemaVersion"`
		TenantID      askdata.ID          `json:"tenantId"`
		DomainID      askdata.ID          `json:"domainId"`
		ReleaseID     askdata.ID          `json:"releaseId"`
		ContentHash   askdata.ContentHash `json:"contentHash"`
		Vertices      []ProjectionVertex  `json:"vertices"`
		Edges         []ProjectionEdge    `json:"edges"`
	}{
		GraphProjectionSchemaVersion, normalized.TenantID, normalized.DomainID,
		normalized.ReleaseID, normalized.ContentHash, normalized.Vertices, normalized.Edges,
	})
	if err != nil {
		return ProjectionProof{}, err
	}
	return ProjectionProof{
		SchemaVersion: GraphProjectionSchemaVersion, GraphHash: hash,
		ObjectCount: len(normalized.Vertices) + len(normalized.Edges),
		VertexCount: len(normalized.Vertices), EdgeCount: len(normalized.Edges),
	}, nil
}

func (snapshot ProjectionSnapshot) normalized() ProjectionSnapshot {
	normalized := snapshot
	normalized.Vertices = append([]ProjectionVertex(nil), snapshot.Vertices...)
	normalized.Edges = append([]ProjectionEdge(nil), snapshot.Edges...)
	sort.Slice(normalized.Vertices, func(i, j int) bool {
		left, right := normalized.Vertices[i], normalized.Vertices[j]
		return vertexSortKey(left) < vertexSortKey(right)
	})
	sort.Slice(normalized.Edges, func(i, j int) bool {
		return edgeSortKey(normalized.Edges[i]) < edgeSortKey(normalized.Edges[j])
	})
	return normalized
}

func vertexSortKey(vertex ProjectionVertex) string {
	return strings.Join([]string{string(vertex.Type), string(vertex.Ref.ObjectID), string(vertex.Ref.VersionID), strconv.Itoa(vertex.Ref.Version)}, "\x00")
}

func edgeSortKey(edge ProjectionEdge) string {
	return strings.Join([]string{string(edge.Type), string(edge.From.VersionID), string(edge.To.VersionID), string(edge.RelationshipVersionID)}, "\x00")
}

type NebulaProjector struct{ executor QueryExecutor }

type nebulaProjectionLogger struct{}

func (nebulaProjectionLogger) Info(string)  {}
func (nebulaProjectionLogger) Warn(string)  {}
func (nebulaProjectionLogger) Error(string) {}
func (nebulaProjectionLogger) Fatal(string) {}

// OpenSessionPool creates a Space-bound pool for the already role-scoped
// runtime credential. It never accepts a bootstrap/root identity or exposes a
// way to switch Space after construction.
func OpenSessionPool(
	rawAddresses, username, password, space string, tlsEnabled bool,
) (*nebula.SessionPool, error) {
	addresses, err := parseNebulaAddresses(rawAddresses)
	if err != nil || strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" ||
		strings.TrimSpace(space) == "" || strings.ContainsAny(username+space, "\r\n\t") {
		return nil, ErrInvalidProjectionWork
	}
	options := []nebula.SessionPoolConfOption{
		nebula.WithTimeOut(10 * time.Second), nebula.WithIdleTime(time.Minute),
		nebula.WithMinSize(1), nebula.WithMaxSize(8),
	}
	if tlsEnabled {
		options = append(options, nebula.WithSSLConfig(&tls.Config{MinVersion: tls.VersionTLS12}))
	}
	configuration, err := nebula.NewSessionPoolConf(
		username, password, addresses, space, options...,
	)
	if err != nil {
		return nil, fmt.Errorf("configure NebulaGraph session pool: %w", err)
	}
	pool, err := nebula.NewSessionPool(*configuration, nebulaProjectionLogger{})
	if err != nil {
		return nil, fmt.Errorf("open NebulaGraph session pool: %w", err)
	}
	return pool, nil
}

func parseNebulaAddresses(raw string) ([]nebula.HostAddress, error) {
	parts := strings.Split(raw, ",")
	if len(parts) == 0 || len(parts) > 16 {
		return nil, ErrInvalidProjectionWork
	}
	addresses := make([]nebula.HostAddress, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		value := strings.TrimSpace(part)
		host, portText, err := net.SplitHostPort(value)
		if err != nil || host == "" || strings.ContainsAny(host, "\r\n\t") {
			return nil, ErrInvalidProjectionWork
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return nil, ErrInvalidProjectionWork
		}
		key := net.JoinHostPort(host, portText)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		addresses = append(addresses, nebula.HostAddress{Host: host, Port: port})
	}
	if len(addresses) == 0 {
		return nil, ErrInvalidProjectionWork
	}
	return addresses, nil
}

func NewNebulaProjector(executor QueryExecutor) (*NebulaProjector, error) {
	if executor == nil {
		return nil, ErrInvalidProjectionWork
	}
	return &NebulaProjector{executor: executor}, nil
}

func (projector *NebulaProjector) Apply(
	ctx context.Context,
	snapshot ProjectionSnapshot,
	heartbeat func(context.Context) error,
) (ProjectionProof, error) {
	if projector == nil || projector.executor == nil || heartbeat == nil {
		return ProjectionProof{}, ErrInvalidProjectionWork
	}
	normalized := snapshot.normalized()
	proof, err := normalized.Proof()
	if err != nil {
		return ProjectionProof{}, err
	}
	if err := heartbeat(ctx); err != nil {
		return ProjectionProof{}, err
	}
	for start := 0; start < len(normalized.Vertices); {
		end := min(start+projectionMutationBatchSize, len(normalized.Vertices))
		for end > start+1 && normalized.Vertices[end-1].Type != normalized.Vertices[start].Type {
			end--
		}
		statement, parameters, buildErr := buildVertexMutation(normalized, normalized.Vertices[start:end])
		if buildErr != nil {
			return ProjectionProof{}, buildErr
		}
		if err := projector.execute(ctx, statement, parameters); err != nil {
			return ProjectionProof{}, err
		}
		if err := heartbeat(ctx); err != nil {
			return ProjectionProof{}, err
		}
		start = end
	}
	for start := 0; start < len(normalized.Edges); {
		end := min(start+projectionMutationBatchSize, len(normalized.Edges))
		for end > start+1 && normalized.Edges[end-1].Type != normalized.Edges[start].Type {
			end--
		}
		statement, parameters, buildErr := buildEdgeMutation(normalized, normalized.Edges[start:end])
		if buildErr != nil {
			return ProjectionProof{}, buildErr
		}
		if err := projector.execute(ctx, statement, parameters); err != nil {
			return ProjectionProof{}, err
		}
		if err := heartbeat(ctx); err != nil {
			return ProjectionProof{}, err
		}
		start = end
	}
	return proof, nil
}

func (projector *NebulaProjector) execute(
	ctx context.Context, statement string, parameters map[string]interface{},
) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	result, err := projector.executor.ExecuteWithParameter(statement, parameters)
	if err != nil {
		return fmt.Errorf("%w: transport", ErrProjectionMutation)
	}
	if result == nil || !result.IsSucceed() {
		code := int32(-1)
		if result != nil {
			code = int32(result.GetErrorCode())
		}
		return fmt.Errorf("%w: server code %d", ErrProjectionMutation, code)
	}
	return contextError(ctx)
}

func buildVertexMutation(
	snapshot ProjectionSnapshot, vertices []ProjectionVertex,
) (string, map[string]interface{}, error) {
	if len(vertices) == 0 {
		return "", nil, ErrProjectionContract
	}
	tag := vertices[0].Type
	columns := []string{"tenant_id", "domain_id", "release_hash", "object_id", "version_id", "version_no"}
	if tag == ObjectTypeMember {
		columns = append(columns, "member_status")
	}
	parameters := projectionScopeParameters(snapshot)
	values := make([]string, 0, len(vertices))
	for index, vertex := range vertices {
		if vertex.Type != tag {
			return "", nil, fmt.Errorf("%w: mixed vertex batch", ErrProjectionContract)
		}
		vid, err := BuildVID(snapshot.TenantID, vertex.Type, vertex.Ref)
		if err != nil {
			return "", nil, err
		}
		objectKey, versionKey, numberKey := fmt.Sprintf("object_%d", index), fmt.Sprintf("version_%d", index), fmt.Sprintf("number_%d", index)
		parameters[objectKey] = string(vertex.Ref.ObjectID)
		parameters[versionKey] = string(vertex.Ref.VersionID)
		parameters[numberKey] = vertex.Ref.Version
		row := []string{"$tenant_id", "$domain_id", "$release_hash", "$" + objectKey, "$" + versionKey, "$" + numberKey}
		if tag == ObjectTypeMember {
			statusKey := fmt.Sprintf("status_%d", index)
			parameters[statusKey] = string(vertex.MemberStatus)
			row = append(row, "$"+statusKey)
		}
		values = append(values, strconv.Quote(vid)+":("+strings.Join(row, ",")+")")
	}
	return fmt.Sprintf("INSERT VERTEX %s(%s) VALUES %s", tag, strings.Join(columns, ","), strings.Join(values, ",")), parameters, nil
}

func buildEdgeMutation(
	snapshot ProjectionSnapshot, edges []ProjectionEdge,
) (string, map[string]interface{}, error) {
	if len(edges) == 0 {
		return "", nil, ErrProjectionContract
	}
	edgeType := edges[0].Type
	columns := []string{"tenant_id", "domain_id", "release_hash"}
	if edgeType == ProjectionEdgeJoinsTo {
		columns = append(columns, "relationship_version_id", "join_type", "cardinality", "fanout_policy", "certified")
	}
	parameters := projectionScopeParameters(snapshot)
	values := make([]string, 0, len(edges))
	for index, edge := range edges {
		if edge.Type != edgeType {
			return "", nil, fmt.Errorf("%w: mixed edge batch", ErrProjectionContract)
		}
		fromVID, err := BuildVID(snapshot.TenantID, edge.FromType, edge.From)
		if err != nil {
			return "", nil, err
		}
		toVID, err := BuildVID(snapshot.TenantID, edge.ToType, edge.To)
		if err != nil {
			return "", nil, err
		}
		row := []string{"$tenant_id", "$domain_id", "$release_hash"}
		rank := int64(0)
		if edgeType == ProjectionEdgeJoinsTo {
			keys := []string{
				fmt.Sprintf("relationship_%d", index), fmt.Sprintf("join_%d", index),
				fmt.Sprintf("cardinality_%d", index), fmt.Sprintf("fanout_%d", index), fmt.Sprintf("certified_%d", index),
			}
			parameters[keys[0]] = string(edge.RelationshipVersionID)
			parameters[keys[1]] = string(edge.JoinType)
			parameters[keys[2]] = string(edge.Cardinality)
			parameters[keys[3]] = string(edge.FanoutPolicy)
			parameters[keys[4]] = edge.Certified
			for _, key := range keys {
				row = append(row, "$"+key)
			}
			rank, err = BuildRelationshipEdgeRank(edge.RelationshipVersionID)
			if err != nil {
				return "", nil, err
			}
		}
		identity := strconv.Quote(fromVID) + "->" + strconv.Quote(toVID)
		if rank != 0 {
			identity += "@" + strconv.FormatInt(rank, 10)
		}
		values = append(values, identity+":("+strings.Join(row, ",")+")")
	}
	return fmt.Sprintf("INSERT EDGE %s(%s) VALUES %s", edgeType, strings.Join(columns, ","), strings.Join(values, ",")), parameters, nil
}

func projectionScopeParameters(snapshot ProjectionSnapshot) map[string]interface{} {
	return map[string]interface{}{
		"tenant_id": string(snapshot.TenantID), "domain_id": string(snapshot.DomainID),
		"release_hash": string(snapshot.ContentHash),
	}
}

// BuildRelationshipEdgeRank preserves multiple certified relationship
// versions between the same model endpoints without placing release data in
// the VID. The rank is deterministic and derived only from the immutable
// relationship version ID.
func BuildRelationshipEdgeRank(versionID askdata.ID) (int64, error) {
	if err := versionID.Validate(); err != nil {
		return 0, err
	}
	hash := askdata.HashBytes([]byte(versionID))
	decoded := make([]byte, 32)
	for index := 0; index < len(decoded); index++ {
		value, _ := strconv.ParseUint(string(hash[index*2:index*2+2]), 16, 8)
		decoded[index] = byte(value)
	}
	rank := int64(binary.BigEndian.Uint64(decoded[:8]) & uint64(^uint64(0)>>1))
	if rank == 0 {
		return 1, nil
	}
	return rank, nil
}

var _ ProjectionWriter = (*NebulaProjector)(nil)
