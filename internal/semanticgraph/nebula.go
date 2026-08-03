package semanticgraph

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	nebulago "github.com/vesoft-inc/nebula-go/v3"
)

func LoadTLSConfig(caFile, serverName string) (*tls.Config, error) {
	certificate, err := os.ReadFile(strings.TrimSpace(caFile))
	if err != nil {
		return nil, fmt.Errorf("read NebulaGraph CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate) {
		return nil, errors.New("NebulaGraph CA file contains no certificate")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots,
		ServerName: strings.TrimSpace(serverName)}, nil
}

type QueryResult struct{ Rows []map[string]any }

type QueryExecutor interface {
	Execute(context.Context, string, map[string]any) (QueryResult, error)
}

type NebulaConfig struct {
	Addresses        []string
	Username         string
	Password         string
	Space            string
	Timeout          time.Duration
	IdleTimeout      time.Duration
	MinimumPoolSize  int
	MaximumPoolSize  int
	TLSConfig        *tls.Config
	FailureThreshold int
	OpenInterval     time.Duration
}

type NebulaClient struct {
	pool             *nebulago.SessionPool
	failureThreshold int
	openInterval     time.Duration
	breakerMu        sync.Mutex
	failures         int
	openUntil        time.Time
}

type rawGraphPath struct {
	VIDs  []string
	Edges []JoinEdge
}

func NewNebulaClient(config NebulaConfig) (*NebulaClient, error) {
	if len(config.Addresses) == 0 || strings.TrimSpace(config.Username) == "" ||
		strings.TrimSpace(config.Password) == "" || strings.TrimSpace(config.Space) == "" ||
		config.Timeout <= 0 || config.MinimumPoolSize < 1 ||
		config.MaximumPoolSize < config.MinimumPoolSize {
		return nil, ErrInvalidRequest
	}
	hosts := make([]nebulago.HostAddress, 0, len(config.Addresses))
	for _, address := range config.Addresses {
		host, portText, err := net.SplitHostPort(strings.TrimSpace(address))
		if err != nil || strings.TrimSpace(host) == "" {
			return nil, fmt.Errorf("%w: invalid NebulaGraph address", ErrInvalidRequest)
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("%w: invalid NebulaGraph port", ErrInvalidRequest)
		}
		hosts = append(hosts, nebulago.HostAddress{Host: host, Port: port})
	}
	options := []nebulago.SessionPoolConfOption{
		nebulago.WithTimeOut(config.Timeout), nebulago.WithIdleTime(config.IdleTimeout),
		nebulago.WithMinSize(config.MinimumPoolSize), nebulago.WithMaxSize(config.MaximumPoolSize),
	}
	if config.TLSConfig != nil {
		options = append(options, nebulago.WithSSLConfig(config.TLSConfig))
	}
	poolConfig, err := nebulago.NewSessionPoolConf(
		config.Username, config.Password, hosts, config.Space, options...,
	)
	if err != nil {
		return nil, err
	}
	pool, err := nebulago.NewSessionPool(*poolConfig, nebulago.DefaultLogger{})
	if err != nil {
		return nil, fmt.Errorf("initialize NebulaGraph session pool: %w", err)
	}
	if config.FailureThreshold < 1 {
		config.FailureThreshold = 3
	}
	if config.OpenInterval <= 0 {
		config.OpenInterval = 15 * time.Second
	}
	return &NebulaClient{pool: pool, failureThreshold: config.FailureThreshold, openInterval: config.OpenInterval}, nil
}

func (client *NebulaClient) Close() {
	if client != nil && client.pool != nil {
		client.pool.Close()
	}
}

func (client *NebulaClient) Execute(
	ctx context.Context,
	statement string,
	parameters map[string]any,
) (QueryResult, error) {
	if client == nil || client.pool == nil || strings.TrimSpace(statement) == "" {
		return QueryResult{}, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return QueryResult{}, err
	}
	if !client.breakerAllows() {
		return QueryResult{}, ErrGraphUnavailable
	}
	normalizedParameters, err := normalizeNebulaParameters(parameters)
	if err != nil {
		return QueryResult{}, err
	}
	type response struct {
		result *nebulago.ResultSet
		err    error
	}
	responseChannel := make(chan response, 1)
	go func() {
		result, err := client.pool.ExecuteWithParameter(statement, normalizedParameters)
		responseChannel <- response{result: result, err: err}
	}()
	var item response
	select {
	case <-ctx.Done():
		client.recordFailure()
		return QueryResult{}, errors.Join(ErrGraphUnavailable, ctx.Err())
	case item = <-responseChannel:
	}
	if item.err != nil || item.result == nil || !item.result.IsSucceed() {
		client.recordFailure()
		if item.err != nil {
			return QueryResult{}, errors.Join(ErrGraphUnavailable, item.err)
		}
		return QueryResult{}, fmt.Errorf("%w: %s", ErrGraphUnavailable, item.result.GetErrorMsg())
	}
	rows, err := convertNebulaRows(item.result)
	if err != nil {
		client.recordFailure()
		return QueryResult{}, errors.Join(ErrGraphUnavailable, err)
	}
	client.recordSuccess()
	return QueryResult{Rows: rows}, nil
}

// nebula-go intentionally accepts Go int but not the fixed-width integer types
// used by database timestamps and edge ranks. Normalize them at the adapter
// boundary so the semantic runtime remains strongly typed internally.
func normalizeNebulaParameters(parameters map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(parameters))
	for key, value := range parameters {
		normalized, err := normalizeNebulaParameter(value)
		if err != nil {
			return nil, fmt.Errorf("normalize NebulaGraph parameter %s: %w", key, err)
		}
		result[key] = normalized
	}
	return result, nil
}

func normalizeNebulaParameter(value any) (any, error) {
	switch typed := value.(type) {
	case int64:
		if strconv.IntSize == 32 && (typed > int64(^uint(0)>>1) || typed < -int64(^uint(0)>>1)-1) {
			return nil, ErrInvalidRequest
		}
		return int(typed), nil
	case int32:
		return int(typed), nil
	case int16:
		return int(typed), nil
	case int8:
		return int(typed), nil
	case uint:
		if uint64(typed) > uint64(^uint(0)>>1) {
			return nil, ErrInvalidRequest
		}
		return int(typed), nil
	case uint64:
		if typed > uint64(^uint(0)>>1) {
			return nil, ErrInvalidRequest
		}
		return int(typed), nil
	case uint32:
		return normalizeNebulaParameter(uint64(typed))
	case uint16:
		return int(typed), nil
	case uint8:
		return int(typed), nil
	case []any:
		items := make([]any, len(typed))
		for index, item := range typed {
			normalized, err := normalizeNebulaParameter(item)
			if err != nil {
				return nil, err
			}
			items[index] = normalized
		}
		return items, nil
	case map[string]any:
		return normalizeNebulaParameters(typed)
	default:
		return value, nil
	}
}

func (client *NebulaClient) breakerAllows() bool {
	client.breakerMu.Lock()
	defer client.breakerMu.Unlock()
	if client.openUntil.IsZero() || !time.Now().Before(client.openUntil) {
		if !client.openUntil.IsZero() {
			client.failures, client.openUntil = 0, time.Time{}
		}
		return true
	}
	return false
}

func (client *NebulaClient) recordFailure() {
	client.breakerMu.Lock()
	defer client.breakerMu.Unlock()
	client.failures++
	if client.failures >= client.failureThreshold {
		client.openUntil = time.Now().Add(client.openInterval)
	}
}

func (client *NebulaClient) recordSuccess() {
	client.breakerMu.Lock()
	client.failures, client.openUntil = 0, time.Time{}
	client.breakerMu.Unlock()
}

func convertNebulaRows(result *nebulago.ResultSet) ([]map[string]any, error) {
	rows := make([]map[string]any, 0, result.GetRowSize())
	columns := result.GetColNames()
	for rowIndex := 0; rowIndex < result.GetRowSize(); rowIndex++ {
		record, err := result.GetRowValuesByIndex(rowIndex)
		if err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for columnIndex, column := range columns {
			value, err := record.GetValueByIndex(columnIndex)
			if err != nil {
				return nil, err
			}
			converted, err := convertNebulaValue(value)
			if err != nil {
				return nil, fmt.Errorf("convert column %s: %w", column, err)
			}
			row[column] = converted
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func convertNebulaValue(value *nebulago.ValueWrapper) (any, error) {
	switch {
	case value.IsEmpty(), value.IsNull():
		return nil, nil
	case value.IsString():
		return value.AsString()
	case value.IsBool():
		return value.AsBool()
	case value.IsInt():
		return value.AsInt()
	case value.IsFloat():
		return value.AsFloat()
	case value.IsList():
		items, err := value.AsList()
		if err != nil {
			return nil, err
		}
		result := make([]any, 0, len(items))
		for index := range items {
			converted, err := convertNebulaValue(&items[index])
			if err != nil {
				return nil, err
			}
			result = append(result, converted)
		}
		return result, nil
	case value.IsPath():
		path, err := value.AsPath()
		if err != nil {
			return nil, err
		}
		return convertNebulaPath(path)
	default:
		return value.String(), nil
	}
}

func convertNebulaPath(path *nebulago.PathWrapper) (rawGraphPath, error) {
	result := rawGraphPath{VIDs: make([]string, 0, len(path.GetNodes())), Edges: make([]JoinEdge, 0, path.GetPathLength())}
	for _, node := range path.GetNodes() {
		vid, err := node.GetID().AsString()
		if err != nil {
			return rawGraphPath{}, err
		}
		result.VIDs = append(result.VIDs, vid)
	}
	for _, relation := range path.GetRelationships() {
		from, err := relation.GetSrcVertexID().AsString()
		if err != nil {
			return rawGraphPath{}, err
		}
		to, err := relation.GetDstVertexID().AsString()
		if err != nil {
			return rawGraphPath{}, err
		}
		properties := relation.Properties()
		result.Edges = append(result.Edges, JoinEdge{
			RelationID: stringProperty(properties, "relation_id"), FromVID: from, ToVID: to,
			Cardinality: stringProperty(properties, "cardinality"),
			Certified:   boolProperty(properties, "certified"), AllowedForQuery: boolProperty(properties, "allowed_for_query"),
			BaseCost: floatProperty(properties, "base_cost"), FanoutPenalty: floatProperty(properties, "fanout_penalty"),
			StalePenalty: floatProperty(properties, "stale_penalty"), CrossSourcePenalty: floatProperty(properties, "cross_source_penalty"),
			PolicyPenalty: floatProperty(properties, "policy_penalty"),
		})
	}
	return result, nil
}

func stringProperty(properties map[string]*nebulago.ValueWrapper, key string) string {
	if value := properties[key]; value != nil {
		result, _ := value.AsString()
		return result
	}
	return ""
}

func boolProperty(properties map[string]*nebulago.ValueWrapper, key string) bool {
	if value := properties[key]; value != nil {
		result, _ := value.AsBool()
		return result
	}
	return false
}

func floatProperty(properties map[string]*nebulago.ValueWrapper, key string) float64 {
	if value := properties[key]; value != nil {
		if value.IsFloat() {
			result, _ := value.AsFloat()
			return result
		}
		if value.IsInt() {
			result, _ := value.AsInt()
			return float64(result)
		}
	}
	return 0
}

func (client *NebulaClient) UpsertVertex(ctx context.Context, vertex Vertex) error {
	if !knownTag(vertex.Tag) || !stableVIDPattern.MatchString(vertex.VID) {
		return ErrInvalidRequest
	}
	statement := "UPSERT VERTEX ON " + vertex.Tag + " \"" + vertex.VID + "\" SET " +
		"object_id=$object_id,object_version=$object_version,object_type=$object_type," +
		"domain_id=$domain_id,tenant_scope=$tenant_scope,status=$status," +
		"sensitivity=$sensitivity,semantic_version=$semantic_version," +
		"content_hash=$content_hash,valid_from=$valid_from,valid_to=$valid_to," +
		"contract_json=$contract_json"
	parameters := copyParameters(vertex.Props)
	_, err := client.Execute(ctx, statement, parameters)
	return err
}

func (client *NebulaClient) UpsertEdge(ctx context.Context, edge Edge) error {
	if !allowedEdgeTypes[edge.Type] || !stableVIDPattern.MatchString(edge.FromVID) ||
		!stableVIDPattern.MatchString(edge.ToVID) || edge.Rank < 0 {
		return ErrInvalidRequest
	}
	statement := "UPSERT EDGE ON " + edge.Type + " \"" + edge.FromVID + "\"->\"" +
		edge.ToVID + "\"@" + strconv.FormatInt(edge.Rank, 10) + " SET " +
		"relation_id=$relation_id,tenant_scope=$tenant_scope,certified=$certified," +
		"allowed_for_query=$allowed_for_query,cardinality=$cardinality," +
		"base_cost=$base_cost,fanout_penalty=$fanout_penalty,stale_penalty=$stale_penalty," +
		"cross_source_penalty=$cross_source_penalty,policy_penalty=$policy_penalty," +
		"semantic_version=$semantic_version,effective_from=$effective_from," +
		"effective_to=$effective_to,attributes_json=$attributes_json"
	parameters := copyParameters(edge.Props)
	_, err := client.Execute(ctx, statement, parameters)
	return err
}

func (client *NebulaClient) Verify(ctx context.Context, projection Projection) (ProjectionVerification, error) {
	verification := ProjectionVerification{}
	for _, vertex := range projection.Vertices {
		if !knownTag(vertex.Tag) || !stableVIDPattern.MatchString(vertex.VID) {
			return verification, ErrInvalidRequest
		}
		statement := "FETCH PROP ON " + vertex.Tag + " \"" + vertex.VID + "\" YIELD id(vertex) AS vid"
		result, err := client.Execute(ctx, statement, nil)
		if err != nil {
			return verification, err
		}
		verification.VertexCount += len(result.Rows)
	}
	for _, edge := range projection.Edges {
		if !allowedEdgeTypes[edge.Type] || !stableVIDPattern.MatchString(edge.FromVID) ||
			!stableVIDPattern.MatchString(edge.ToVID) || edge.Rank < 0 {
			return verification, ErrInvalidRequest
		}
		statement := "FETCH PROP ON " + edge.Type + " \"" + edge.FromVID + "\"->\"" +
			edge.ToVID + "\"@" + strconv.FormatInt(edge.Rank, 10) + " " +
			"YIELD src(edge) AS from_vid,dst(edge) AS to_vid"
		result, err := client.Execute(ctx, statement, nil)
		if err != nil {
			return verification, err
		}
		verification.EdgeCount += len(result.Rows)
	}
	return verification, nil
}

func copyParameters(source map[string]any) map[string]any {
	result := make(map[string]any, len(source)+3)
	for key, value := range source {
		result[key] = value
	}
	return result
}

func knownTag(tag string) bool {
	for _, candidate := range objectTag {
		if tag == candidate {
			return true
		}
	}
	return false
}
