package datasource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type SecretResolver interface {
	Resolve(context.Context, string) (map[string]string, error)
}
type EnvSecretResolver struct{}

var envSecretName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,100}$`)
var connectorHostnameLabel = regexp.MustCompile(
	`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`,
)

// Resolve 将受限的 ENV 引用解析为连接凭据，禁止任意环境变量名注入。
func (EnvSecretResolver) Resolve(_ context.Context, ref string) (map[string]string, error) {
	// 数据源记录只保存 env:// 引用；凭据在调用 Connector 前即时解析，不回写控制库，
	// 错误也不会包含环境变量内容。
	if !strings.HasPrefix(ref, "env://") {
		return nil, errors.New("unsupported secret reference")
	}
	name := strings.TrimPrefix(ref, "env://")
	if !envSecretName.MatchString(name) {
		return nil, errors.New("invalid environment secret reference")
	}
	raw := os.Getenv(name)
	if raw == "" {
		return nil, errors.New("referenced secret is not configured")
	}
	var value map[string]string
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, errors.New("referenced secret is invalid")
	}
	return value, nil
}

type PythonConnector struct {
	kind           Type
	baseURL, token string
	http           *http.Client
	secrets        SecretResolver
	limits         ConnectorLimits
}

var (
	ErrConnectorRequestBytesExceeded  = errors.New("connector request byte limit exceeded")
	ErrConnectorResponseBytesExceeded = errors.New("connector response byte limit exceeded")
	ErrConnectorResourceLimitExceeded = errors.New("connector resource limit exceeded")
)

// ConnectorLimits 在 Go/Connector 两侧使用相同数量级的硬边界。即使远端服务
// 配置漂移或被替换，客户端也会在 JSON 解码和 NDJSON 消费前独立失败关闭。
type ConnectorLimits struct {
	MaxRequestBytes        int64
	MaxJSONResponseBytes   int64
	MaxSampleResponseBytes int64
	MaxSampleCellBytes     int
	MaxSampleRowBytes      int
	MaxStreamBytes         int64
	MaxStreamCellBytes     int
	MaxStreamRowBytes      int
}

func DefaultConnectorLimits() ConnectorLimits {
	return ConnectorLimits{
		MaxRequestBytes:        1 << 20,
		MaxJSONResponseBytes:   64 << 20,
		MaxSampleResponseBytes: 512 << 10,
		MaxSampleCellBytes:     16 << 10,
		MaxSampleRowBytes:      64 << 10,
		MaxStreamBytes:         1 << 30,
		MaxStreamCellBytes:     1 << 20,
		MaxStreamRowBytes:      4 << 20,
	}
}

func (limits ConnectorLimits) normalized() ConnectorLimits {
	defaults := DefaultConnectorLimits()
	if limits.MaxRequestBytes <= 0 {
		limits.MaxRequestBytes = defaults.MaxRequestBytes
	}
	if limits.MaxJSONResponseBytes <= 0 {
		limits.MaxJSONResponseBytes = defaults.MaxJSONResponseBytes
	}
	if limits.MaxSampleResponseBytes <= 0 {
		limits.MaxSampleResponseBytes = defaults.MaxSampleResponseBytes
	}
	if limits.MaxSampleCellBytes <= 0 {
		limits.MaxSampleCellBytes = defaults.MaxSampleCellBytes
	}
	if limits.MaxSampleRowBytes <= 0 {
		limits.MaxSampleRowBytes = defaults.MaxSampleRowBytes
	}
	if limits.MaxStreamBytes <= 0 {
		limits.MaxStreamBytes = defaults.MaxStreamBytes
	}
	if limits.MaxStreamCellBytes <= 0 {
		limits.MaxStreamCellBytes = defaults.MaxStreamCellBytes
	}
	if limits.MaxStreamRowBytes <= 0 {
		limits.MaxStreamRowBytes = defaults.MaxStreamRowBytes
	}
	return limits
}

// NewPythonConnector 创建调用隔离连接器服务的数据库连接器。
func NewPythonConnector(kind Type, baseURL, token string, secrets SecretResolver) *PythonConnector {
	return NewPythonConnectorWithLimits(
		kind, baseURL, token, secrets, DefaultConnectorLimits(),
	)
}

// NewPythonConnectorWithLimits 允许服务启动配置把同一组预算注入控制面和数据面。
func NewPythonConnectorWithLimits(
	kind Type,
	baseURL, token string,
	secrets SecretResolver,
	limits ConnectorLimits,
) *PythonConnector {
	return &PythonConnector{
		kind: kind, baseURL: strings.TrimRight(baseURL, "/"), token: token,
		http: &http.Client{Timeout: 35 * time.Second}, secrets: secrets,
		limits: limits.normalized(),
	}
}

// Type 返回该连接器处理的数据源类型。
func (c *PythonConnector) Type() Type { return c.kind }

// Test 验证远端数据库连接，并返回延迟与服务端信息。
func (c *PythonConnector) Test(ctx context.Context, source Source) (TestResult, error) {
	payload, err := c.connection(ctx, source)
	if err != nil {
		return TestResult{}, err
	}
	var result TestResult
	if err := c.call(ctx, "/v1/connections/test", payload, &result); err != nil {
		return TestResult{}, err
	}
	return result, nil
}

// Sync 请求连接器采集技术元数据，并转换为统一同步结果。
func (c *PythonConnector) Sync(ctx context.Context, source Source) (SyncResult, error) {
	payload, err := c.connection(ctx, source)
	if err != nil {
		return SyncResult{}, err
	}
	var result SyncResult
	if err := c.call(ctx, "/v1/metadata/sync", payload, &result); err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

// Sample 通过专用 Connector 接口读取最多十行，不允许调用方传入任意 SQL。
func (c *PythonConnector) Sample(ctx context.Context, source Source, table MetadataTable, maxRows int) (SampleResult, error) {
	if maxRows < 1 || maxRows > 10 {
		return SampleResult{}, ErrConnectorResourceLimitExceeded
	}
	connection, err := c.connection(ctx, source)
	if err != nil {
		return SampleResult{}, err
	}
	columns := metadataSampleProjection(table)
	if len(columns) == 0 {
		return SampleResult{Columns: []string{}, Rows: [][]any{}}, nil
	}
	if len(columns) > 256 {
		return SampleResult{}, ErrConnectorResourceLimitExceeded
	}
	payload := map[string]any{
		"connection": connection, "catalog_name": table.CatalogName, "schema_name": table.SchemaName,
		"table_name": table.Name, "columns": columns, "max_rows": maxRows,
	}
	var result SampleResult
	if err := c.call(ctx, "/v1/metadata/sample", payload, &result); err != nil {
		return SampleResult{}, err
	}
	if err := validateMetadataSampleResult(
		result, maxRows, c.limits.MaxSampleCellBytes,
		c.limits.MaxSampleRowBytes,
	); err != nil {
		return SampleResult{}, err
	}
	if !sameMetadataSampleColumns(columns, result.Columns) {
		return SampleResult{}, errors.New(
			"connector metadata sample projection is inconsistent",
		)
	}
	return result, nil
}

// Query 执行服务端生成的参数化只读 SQL，并传递统一查询标识和行数上限。
func (c *PythonConnector) Query(ctx context.Context, source Source, queryID, sql string, parameters []any, maxRows int) (QueryResult, error) {
	connection, err := c.connection(ctx, source)
	if err != nil {
		return QueryResult{}, err
	}
	// Connector 的 Pydantic 合同要求 parameters 始终是数组；在边界再次归一化，
	// 防止其他受信调用方把 nil 切片序列化为 null。
	if parameters == nil {
		parameters = []any{}
	}
	payload := map[string]any{"connection": connection, "query_id": queryID, "sql": sql, "parameters": parameters, "max_rows": maxRows}
	var result QueryResult
	if err := c.call(ctx, "/v1/query", payload, &result); err != nil {
		return QueryResult{}, err
	}
	if err := validateConnectorQueryResult(
		result, maxRows, c.limits.MaxStreamCellBytes,
		c.limits.MaxStreamRowBytes,
	); err != nil {
		return QueryResult{}, err
	}
	// 风险告警和节点运行指标只能由可信 Go 查询网关生成；远端 Connector 即使
	// 返回同名字段也必须丢弃，避免源端内容进入用户响应和控制库审计。
	result.Warnings = nil
	result.SourceStats = nil
	return result, nil
}

func validateConnectorQueryResult(
	result QueryResult,
	maxRows, maxCellBytes, maxRowBytes int,
) error {
	if len(result.Columns) == 0 || len(result.Columns) > 1600 ||
		len(result.Rows) > maxRows || result.RowCount != len(result.Rows) ||
		result.DurationMS < 0 {
		return ErrConnectorResourceLimitExceeded
	}
	for _, row := range result.Rows {
		if len(row) != len(result.Columns) {
			return errors.New("connector query row width is invalid")
		}
		if err := validateStreamRowBytes(
			row, maxCellBytes, maxRowBytes,
		); err != nil {
			return err
		}
	}
	return nil
}

// Cancel 中断 Connector 中使用同一查询标识的在途数据库调用。
func (c *PythonConnector) Cancel(ctx context.Context, queryID string) (bool, error) {
	var result struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := c.call(ctx, "/v1/query/cancel", map[string]string{"query_id": queryID}, &result); err != nil {
		return false, err
	}
	return result.Cancelled, nil
}

// Close 通知远端释放指定连接配置对应的连接池。
func (c *PythonConnector) Close(ctx context.Context, source Source) error {
	payload, err := c.connection(ctx, source)
	if err != nil {
		return err
	}
	var result struct {
		Closed bool `json:"closed"`
	}
	return c.call(ctx, "/v1/connections/close", payload, &result)
}

// connection 合并公开连接配置与密钥引用解析出的敏感字段。
func (c *PythonConnector) connection(ctx context.Context, source Source) (map[string]any, error) {
	// 连接负载是唯一会短暂包含明文密码的结构，只发送给带内部令牌的隔离服务。
	// 并发上限来自租户配额而非数据源自定义配置，避免单个源自行放大资源占用。
	secret, err := c.secrets.Resolve(ctx, source.SecretRef)
	if err != nil {
		return nil, err
	}
	required := []string{"host", "port", "database", "username", "password"}
	for _, key := range required {
		if secret[key] == "" {
			return nil, fmt.Errorf("secret is missing %s", key)
		}
	}
	port, err := strconv.Atoi(secret["port"])
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("data source credential port is invalid")
	}
	if err := validateConnectorHost(secret["host"]); err != nil {
		return nil, err
	}
	payload := map[string]any{
		"source_type": source.Type, "host": secret["host"], "port": port,
		"database": secret["database"], "username": secret["username"], "password": secret["password"],
		"tenant_key": source.TenantID, "source_key": source.ID, "max_connections_per_source": source.RuntimeQuota.MaxConnectionsPerSource,
		"max_concurrent_queries": source.RuntimeQuota.MaxConcurrentQueries,
	}
	if source.ID == "" {
		// 连接测试发生在持久化 ID 缺失的场景时，用租户与代码构成稳定池隔离键。
		payload["source_key"] = source.TenantID + ":" + source.Code
	}
	if source.RuntimeQuota.MaxConnectionsPerSource <= 0 {
		payload["max_connections_per_source"] = 5
	}
	if source.RuntimeQuota.MaxConcurrentQueries <= 0 {
		payload["max_concurrent_queries"] = 10
	}
	if source.Type == TypeOracle {
		if mode, ok := source.Config["oracleConnectMode"].(string); ok && mode != "" {
			payload["oracle_connect_mode"] = mode
		}
		if schemas, ok := source.Config["schemas"].([]any); ok {
			values := make([]string, 0, len(schemas))
			for _, schema := range schemas {
				if value, ok := schema.(string); ok {
					values = append(values, value)
				}
			}
			payload["schemas"] = values
		}
	}
	return payload, nil
}

// call 统一执行带内部令牌的 JSON 请求；非 2xx 响应不回传远端正文，避免驱动错误泄露连接信息。
func (c *PythonConnector) call(ctx context.Context, path string, input, output any) error {
	// http.Client 超时是外层兜底；请求上下文仍负责把调用方的超时和主动取消传递到网络层。
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	if int64(len(body)) > c.limits.MaxRequestBytes {
		return ErrConnectorRequestBytesExceeded
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Connector-Token", c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		if response.StatusCode == http.StatusRequestEntityTooLarge {
			return ErrConnectorResourceLimitExceeded
		}
		return fmt.Errorf("connector service returned %s", response.Status)
	}
	responseLimit := c.limits.MaxJSONResponseBytes
	if path == "/v1/metadata/sample" {
		responseLimit = c.limits.MaxSampleResponseBytes
	}
	limited := io.LimitReader(response.Body, responseLimit+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if int64(len(raw)) > responseLimit {
		return ErrConnectorResponseBytesExceeded
	}
	// 查询行使用 interface{} 承载值；保留 JSON 数字文本，避免 64 位业务主键在
	// 进入跨源类型归一化前先被 float64 截断。结构化整数字段仍由解码器正常转换。
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("connector response contains multiple JSON values")
		}
		return err
	}
	return nil
}

func metadataSampleProjection(table MetadataTable) []string {
	columns := make([]string, 0, len(table.Columns))
	for _, column := range table.Columns {
		native := strings.ToUpper(strings.TrimSpace(column.NativeType))
		canonical := strings.ToUpper(strings.TrimSpace(column.CanonicalType))
		unsafe := canonical == "BINARY"
		base := native
		if index := strings.IndexByte(base, '('); index >= 0 {
			base = strings.TrimSpace(base[:index])
		}
		base = strings.Join(strings.Fields(base), " ")
		if strings.HasSuffix(base, "BLOB") {
			unsafe = true
		}
		switch base {
		case "BFILE", "BINARY", "VARBINARY", "RAW", "LONG RAW",
			"CLOB", "NCLOB", "LONG", "XMLTYPE", "JSON",
			"LONGTEXT", "MEDIUMTEXT":
			unsafe = true
		}
		if !unsafe {
			columns = append(columns, column.Name)
		}
	}
	return columns
}

func validateMetadataSampleResult(
	result SampleResult,
	maxRows, maxCellBytes, maxRowBytes int,
) error {
	if len(result.Rows) > maxRows || len(result.Columns) > 256 {
		return ErrConnectorResourceLimitExceeded
	}
	if result.RowCount != len(result.Rows) {
		return errors.New("connector metadata sample row count is invalid")
	}
	for _, row := range result.Rows {
		if len(row) != len(result.Columns) {
			return errors.New("connector metadata sample row width is invalid")
		}
		rowBytes, err := json.Marshal(row)
		if err != nil {
			return errors.New("connector metadata sample value is invalid")
		}
		if len(rowBytes) > maxRowBytes {
			return ErrConnectorResourceLimitExceeded
		}
		for _, value := range row {
			cellBytes, err := json.Marshal(value)
			if err != nil {
				return errors.New("connector metadata sample value is invalid")
			}
			if len(cellBytes) > maxCellBytes {
				return ErrConnectorResourceLimitExceeded
			}
		}
	}
	return nil
}

func sameMetadataSampleColumns(expected, actual []string) bool {
	if len(expected) != len(actual) {
		return false
	}
	for index := range expected {
		if !strings.EqualFold(expected[index], actual[index]) {
			return false
		}
	}
	return true
}

func validateConnectorHost(host string) error {
	value := strings.TrimSpace(host)
	if value == "" || value != host || len(value) > 253 ||
		strings.ContainsAny(value, `/\\@%`) ||
		strings.Contains(value, "://") {
		return errors.New("data source credential host is invalid")
	}
	if ip := net.ParseIP(value); ip != nil {
		if strings.Contains(value, ":") && ip.To4() != nil {
			return errors.New("data source credential host is forbidden")
		}
		if ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
			ip.Equal(net.ParseIP("169.254.169.254")) {
			return errors.New("data source credential host is forbidden")
		}
		return nil
	}
	lower := strings.ToLower(value)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") ||
		lower == "metadata.google.internal" ||
		lower == "metadata.azure.internal" ||
		lower == "instance-data.ec2.internal" {
		return errors.New("data source credential host is forbidden")
	}
	labels := strings.Split(value, ".")
	for _, label := range labels {
		if !connectorHostnameLabel.MatchString(label) {
			return errors.New("data source credential host is invalid")
		}
	}
	return nil
}
