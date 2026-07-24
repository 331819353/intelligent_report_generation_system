package datasource

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	maxStreamColumns    = 1600
	maxStreamEventBytes = 64 << 20
)

// StreamBatch 是 Connector 返回的一批查询行。Columns 在每批中重复提供，
// 让消费方无需把 schema 保存在进程级共享状态中。
type StreamBatch struct {
	Columns []string
	Rows    [][]any
}

// StreamSummary 是流被完整消费并收到 complete 事件后的可信传输摘要。
type StreamSummary struct {
	RowCount    int
	DurationMS  int64
	SourceBytes int64
}

// StreamConsumer 必须在返回前消费当前批次。实现方应把所有批次写入同一事务，
// 因为后续事件仍可能报告源端失败或总行数超限。
type StreamConsumer func(StreamBatch) error

// StreamQuerier 为跨源构建提供有界、常量内存的查询接口。
type StreamQuerier interface {
	StreamQuery(context.Context, Source, string, string, []any, int, int, StreamConsumer) (StreamSummary, error)
}

type connectorStreamEvent struct {
	Type       string   `json:"type"`
	Columns    []string `json:"columns"`
	Rows       [][]any  `json:"rows"`
	RowCount   int      `json:"rowCount"`
	DurationMS int64    `json:"durationMs"`
	Code       string   `json:"code"`
}

// StreamQuery 执行服务端生成的参数化只读 SQL，并逐批交给调用方处理。
// 只有 schema -> batch* -> complete 的完整事件序列才算成功；远端错误、
// 提前 EOF、重复完成事件和行宽/行数不一致都会使调用方事务回滚。
func (c *PythonConnector) StreamQuery(
	ctx context.Context,
	source Source,
	queryID, sql string,
	parameters []any,
	batchSize, maxRows int,
	consume StreamConsumer,
) (StreamSummary, error) {
	if c == nil || c.http == nil || c.secrets == nil {
		return StreamSummary{}, errors.New("stream connector is not configured")
	}
	if consume == nil {
		return StreamSummary{}, errors.New("stream consumer is required")
	}
	if source.Type != c.kind || (source.Type != TypeMySQL && source.Type != TypeOracle) {
		return StreamSummary{}, errors.New("stream source type does not match the connector")
	}
	if strings.TrimSpace(source.ID) == "" || strings.TrimSpace(source.TenantID) == "" ||
		len(source.ID) > 100 || len(source.TenantID) > 100 {
		return StreamSummary{}, errors.New("stream source identity is invalid")
	}
	if queryID != strings.TrimSpace(queryID) || queryID == "" || len(queryID) > 100 {
		return StreamSummary{}, errors.New("stream query identity is invalid")
	}
	if strings.TrimSpace(sql) == "" || len(sql) > 100_000 {
		return StreamSummary{}, errors.New("stream SQL is invalid")
	}
	if len(parameters) > 1000 {
		return StreamSummary{}, errors.New("stream parameter limit exceeded")
	}
	if batchSize < 1 || batchSize > 5000 {
		return StreamSummary{}, errors.New("stream batch size must be between 1 and 5000")
	}
	if maxRows < 1 || maxRows > 5_000_000 {
		return StreamSummary{}, errors.New("stream row limit must be between 1 and 5000000")
	}
	connection, err := c.connection(ctx, source)
	if err != nil {
		return StreamSummary{}, err
	}
	if parameters == nil {
		parameters = []any{}
	}
	body, err := json.Marshal(map[string]any{
		"connection": connection,
		"query_id":   queryID,
		"sql":        sql,
		"parameters": parameters,
		"batch_size": batchSize,
		"max_rows":   maxRows,
	})
	if err != nil {
		return StreamSummary{}, err
	}
	if int64(len(body)) > c.limits.MaxRequestBytes {
		return StreamSummary{}, ErrConnectorRequestBytesExceeded
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/query/stream", bytes.NewReader(body))
	if err != nil {
		return StreamSummary{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/x-ndjson")
	request.Header.Set("X-Connector-Token", c.token)

	// 普通控制面调用保留 35 秒兜底；物化流由构建任务上下文控制生命周期，
	// 避免大表在仍正常传输时被 http.Client 的整请求超时截断。
	streamClient := *c.http
	streamClient.Timeout = 0
	response, err := streamClient.Do(request)
	if err != nil {
		return StreamSummary{}, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		if response.StatusCode == http.StatusRequestEntityTooLarge {
			return StreamSummary{}, ErrConnectorResourceLimitExceeded
		}
		return StreamSummary{}, fmt.Errorf("connector service returned %s", response.Status)
	}

	// LimitReader 在协议解析前最多只允许整流预算外再读取一个探测字节；
	// 远端即使发送无换行的超长事件，也不能迫使 Scanner 缓冲固定 64 MiB。
	scanner := bufio.NewScanner(
		io.LimitReader(response.Body, c.limits.MaxStreamBytes+1),
	)
	// Scanner 必须在分配完整 NDJSON 事件前受本次整流预算约束。额外一个
	// 字节只用于换行 framing；固定 64 MiB 仍是协议的绝对事件上限。
	scannerTokenLimit := int64(maxStreamEventBytes)
	if budgetLimit := c.limits.MaxStreamBytes + 2; budgetLimit < scannerTokenLimit {
		scannerTokenLimit = budgetLimit
	}
	scanner.Buffer(make([]byte, 64<<10), int(scannerTokenLimit))
	var (
		columns     []string
		rowCount    int
		sawSchema   bool
		complete    bool
		summary     StreamSummary
		sourceBytes int64
	)
	for scanner.Scan() {
		// SplitLines removes the newline delimiter. Count it explicitly because
		// Connector emits canonical NDJSON and the whole-response budget covers
		// both payload and framing.
		sourceBytes += int64(len(scanner.Bytes())) + 1
		if sourceBytes > c.limits.MaxStreamBytes {
			return StreamSummary{}, ErrConnectorResponseBytesExceeded
		}
		var event connectorStreamEvent
		if err := decodeStreamEvent(scanner.Bytes(), &event); err != nil {
			return StreamSummary{}, fmt.Errorf("decode connector stream: %w", err)
		}
		if complete {
			return StreamSummary{}, errors.New("connector stream contains events after completion")
		}
		switch event.Type {
		case "schema":
			if sawSchema {
				return StreamSummary{}, errors.New("connector stream contains duplicate schema")
			}
			if err := validateStreamColumns(event.Columns); err != nil {
				return StreamSummary{}, err
			}
			columns = append([]string(nil), event.Columns...)
			sawSchema = true
		case "batch":
			if !sawSchema {
				return StreamSummary{}, errors.New("connector stream batch arrived before schema")
			}
			if len(event.Rows) == 0 {
				return StreamSummary{}, errors.New("connector stream contains an empty batch")
			}
			if len(event.Rows) > batchSize {
				return StreamSummary{}, errors.New("connector stream batch exceeds the requested size")
			}
			for _, row := range event.Rows {
				if len(row) != len(columns) {
					return StreamSummary{}, errors.New("connector stream row width does not match schema")
				}
				if err := validateStreamRowBytes(
					row,
					c.limits.MaxStreamCellBytes,
					c.limits.MaxStreamRowBytes,
				); err != nil {
					return StreamSummary{}, err
				}
			}
			rowCount += len(event.Rows)
			if rowCount > maxRows {
				return StreamSummary{}, errors.New("connector stream exceeded local row limit")
			}
			if err := consume(StreamBatch{Columns: append([]string(nil), columns...), Rows: event.Rows}); err != nil {
				return StreamSummary{}, fmt.Errorf("consume connector stream: %w", err)
			}
		case "complete":
			if !sawSchema {
				return StreamSummary{}, errors.New("connector stream completed before schema")
			}
			if event.RowCount != rowCount {
				return StreamSummary{}, errors.New("connector stream row count does not match received batches")
			}
			if event.DurationMS < 0 {
				return StreamSummary{}, errors.New("connector stream duration is invalid")
			}
			complete = true
			summary = StreamSummary{
				RowCount:    rowCount,
				DurationMS:  event.DurationMS,
				SourceBytes: sourceBytes,
			}
		case "error":
			code := strings.TrimSpace(event.Code)
			if !knownConnectorStreamErrorCode(code) {
				code = "QUERY_FAILED"
			}
			return StreamSummary{}, fmt.Errorf("connector stream failed: %s", code)
		default:
			return StreamSummary{}, errors.New("connector stream contains an unknown event")
		}
	}
	if err := scanner.Err(); err != nil {
		return StreamSummary{}, fmt.Errorf("read connector stream: %w", err)
	}
	if complete {
		return summary, nil
	}
	return StreamSummary{}, errors.New("connector stream ended before completion")
}

func knownConnectorStreamErrorCode(code string) bool {
	switch code {
	case "QUERY_ID_CONFLICT",
		"QUERY_ROW_LIMIT_EXCEEDED",
		"QUERY_SOURCE_BYTES_EXCEEDED",
		"QUERY_STREAM_EVENT_BYTES_EXCEEDED",
		"QUERY_VALUE_UNSUPPORTED",
		"QUERY_CELL_BYTES_EXCEEDED",
		"QUERY_ROW_BYTES_EXCEEDED",
		"QUERY_FAILED":
		return true
	default:
		return false
	}
}

func validateStreamRowBytes(row []any, maxCellBytes, maxRowBytes int) error {
	encodedRow, err := json.Marshal(row)
	if err != nil {
		return errors.New("connector stream row contains an invalid value")
	}
	if len(encodedRow) > maxRowBytes {
		return ErrConnectorResourceLimitExceeded
	}
	for _, value := range row {
		encodedCell, err := json.Marshal(value)
		if err != nil {
			return errors.New("connector stream cell contains an invalid value")
		}
		if len(encodedCell) > maxCellBytes {
			return ErrConnectorResourceLimitExceeded
		}
	}
	return nil
}

func validateStreamColumns(columns []string) error {
	if len(columns) == 0 {
		return errors.New("connector stream schema has no columns")
	}
	if len(columns) > maxStreamColumns {
		return errors.New("connector stream schema has too many columns")
	}
	seen := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		name := strings.TrimSpace(column)
		if name == "" {
			return errors.New("connector stream schema contains an empty column")
		}
		if name != column {
			return errors.New("connector stream schema contains an unnormalized column")
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			return errors.New("connector stream schema contains duplicate columns")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func decodeStreamEvent(raw []byte, event *connectorStreamEvent) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("empty NDJSON event")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(event); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values in one NDJSON event")
		}
		return err
	}
	return nil
}
