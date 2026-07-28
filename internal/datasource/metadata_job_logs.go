package datasource

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const metadataJobLogLimit = 300

type metadataJobLogEvent struct {
	log MetadataJobLog
	at  time.Time
}

func loadMetadataJobLogs(
	ctx context.Context,
	tx pgx.Tx,
	job MetadataJob,
) ([]MetadataJobLog, error) {
	events := make([]metadataJobLogEvent, 0, 32)
	var createdAt time.Time
	var startedAt, completedAt pgtype.Timestamptz
	if err := tx.QueryRow(ctx, `SELECT created_at,started_at,completed_at
		FROM platform.data_source_metadata_jobs WHERE id=$1`, job.ID).
		Scan(&createdAt, &startedAt, &completedAt); err != nil {
		return nil, err
	}
	events = appendMetadataJobLog(events, createdAt, "INFO", "QUEUED", "任务已提交，等待后台执行", "", "", 0)
	if startedAt.Valid {
		events = appendMetadataJobLog(events, startedAt.Time, "INFO", "DISCOVERY", "后台处理器已领取任务", "", "", 0)
	}

	itemRows, err := tx.Query(ctx, `SELECT table_name,status,stage,
			COALESCE(completed_at,started_at,created_at)
		FROM platform.data_source_metadata_job_items
		WHERE job_id=$1 AND status<>'QUEUED'
		ORDER BY COALESCE(completed_at,started_at,created_at),id`, job.ID)
	if err != nil {
		return nil, err
	}
	for itemRows.Next() {
		var tableName, status, stage string
		var eventAt time.Time
		if err := itemRows.Scan(&tableName, &status, &stage, &eventAt); err != nil {
			itemRows.Close()
			return nil, err
		}
		level, message := metadataJobItemLogMessage(status, stage)
		events = appendMetadataJobLog(events, eventAt, level, stage, message, tableName, "", 0)
	}
	if err := itemRows.Err(); err != nil {
		itemRows.Close()
		return nil, err
	}
	itemRows.Close()

	roundRows, err := tx.Query(ctx, `SELECT round_number,table_name,status,error_code,
			model_name,latency_ms,event_at
		FROM (
			SELECT row_number() OVER (ORDER BY m.created_at,m.id)::integer AS round_number,
				i.table_name,m.status,m.error_code,m.model_name,m.latency_ms,
				COALESCE(m.completed_at,m.created_at) AS event_at
			FROM platform.ai_metadata_jobs m
			JOIN platform.data_source_metadata_job_items i
				ON i.id=m.data_source_metadata_job_item_id
				AND i.tenant_id=m.tenant_id
			WHERE i.job_id=$1
		) rounds
		ORDER BY event_at,round_number`, job.ID)
	if err != nil {
		return nil, err
	}
	for roundRows.Next() {
		var number int
		var tableName, status, errorCode, model string
		var durationMS int64
		var eventAt time.Time
		if err := roundRows.Scan(
			&number,
			&tableName,
			&status,
			&errorCode,
			&model,
			&durationMS,
			&eventAt,
		); err != nil {
			roundRows.Close()
			return nil, err
		}
		level, message := metadataAIRoundLogMessage(number, status, errorCode)
		events = appendMetadataJobLog(events, eventAt, level, "LLM", message, tableName, model, durationMS)
	}
	if err := roundRows.Err(); err != nil {
		roundRows.Close()
		return nil, err
	}
	roundRows.Close()

	requestRows, err := tx.Query(ctx, `WITH calls AS (
			SELECT row_number() OVER (ORDER BY r.created_at,r.id)::integer AS call_number,
				i.table_name,r.status,r.error_code,r.model_name,r.latency_ms,
				COALESCE(r.completed_at,r.created_at) AS event_at
			FROM platform.ai_requests r
			JOIN platform.data_source_metadata_job_items i
				ON i.job_id=$1
				AND i.table_id::text=r.resource_id
				AND r.created_at>=COALESCE(i.started_at,i.created_at)
				AND r.created_at<=COALESCE(i.completed_at,now())+interval '1 second'
			WHERE r.purpose='METADATA_COMPLETION'
				AND r.resource_type='METADATA_TABLE'
		)
		SELECT call_number,table_name,status,error_code,model_name,latency_ms,event_at
		FROM calls
		ORDER BY event_at DESC,call_number DESC
		LIMIT $2`, job.ID, metadataJobLogLimit)
	if err != nil {
		return nil, err
	}
	for requestRows.Next() {
		var number int
		var tableName, status, errorCode, model string
		var durationMS int64
		var eventAt time.Time
		if err := requestRows.Scan(
			&number,
			&tableName,
			&status,
			&errorCode,
			&model,
			&durationMS,
			&eventAt,
		); err != nil {
			requestRows.Close()
			return nil, err
		}
		level, message := metadataAIRequestLogMessage(number, status, errorCode)
		events = appendMetadataJobLog(events, eventAt, level, "LLM", message, tableName, model, durationMS)
	}
	if err := requestRows.Err(); err != nil {
		requestRows.Close()
		return nil, err
	}
	requestRows.Close()

	if completedAt.Valid {
		level, message := "SUCCESS", "任务处理完成"
		if job.Status == "PARTIAL" {
			level, message = "WARN", "任务部分完成，请查看失败明细"
		} else if job.Status == "FAILED" {
			level, message = "ERROR", "任务处理失败，请查看失败明细"
		}
		events = appendMetadataJobLog(events, completedAt.Time, level, job.Stage, message, "", "", 0)
	}

	sort.SliceStable(events, func(left, right int) bool {
		return events[left].at.Before(events[right].at)
	})
	if len(events) > metadataJobLogLimit {
		events = append(events[:1], events[len(events)-(metadataJobLogLimit-1):]...)
	}
	logs := make([]MetadataJobLog, len(events))
	for index, event := range events {
		event.log.Timestamp = event.at.UTC().Format(time.RFC3339Nano)
		logs[index] = event.log
	}
	return logs, nil
}

func appendMetadataJobLog(
	events []metadataJobLogEvent,
	at time.Time,
	level, stage, message, tableName, model string,
	durationMS int64,
) []metadataJobLogEvent {
	return append(events, metadataJobLogEvent{
		at: at,
		log: MetadataJobLog{
			Level:      level,
			Stage:      stage,
			Message:    message,
			TableName:  strings.TrimSpace(tableName),
			Model:      strings.TrimSpace(model),
			DurationMS: durationMS,
		},
	})
}

func metadataJobItemLogMessage(status, stage string) (string, string) {
	if status == "SUCCEEDED" {
		return "SUCCESS", "数据表处理完成"
	}
	if status == "SKIPPED" {
		return "INFO", "结构未变化，已跳过 LLM"
	}
	if status == "FAILED" {
		return "ERROR", "数据表处理失败"
	}
	switch stage {
	case "DISCOVERY":
		return "INFO", "正在读取表结构"
	case "DIFF":
		return "INFO", "正在比对结构变化"
	case "SAMPLE":
		return "INFO", "正在读取授权范围内的样本"
	case "PERSISTENCE":
		return "INFO", "正在保存物理元数据"
	case "LLM":
		return "INFO", "已进入 LLM 元数据完善"
	default:
		return "INFO", "数据表正在处理"
	}
}

func metadataAIRoundLogMessage(number int, status, errorCode string) (string, string) {
	prefix := fmt.Sprintf("第 %d 轮 LLM 处理", number)
	if status == "RUNNING" {
		return "INFO", prefix + "已开始"
	}
	if status == "SUCCEEDED" {
		return "SUCCESS", prefix + "已完成"
	}
	switch errorCode {
	case "PARTIAL_OUTPUT":
		return "WARN", prefix + "部分完成；有效字段已保存，仅重试剩余字段"
	case "TIMEOUT":
		return "WARN", prefix + "达到处理时限，准备重试"
	case "INVALID_OUTPUT":
		return "WARN", prefix + "输出未通过业务校验，准备重试"
	default:
		return "ERROR", prefix + "未完成"
	}
}

func metadataAIRequestLogMessage(number int, status, errorCode string) (string, string) {
	prefix := fmt.Sprintf("模型调用 #%d", number)
	if status == "RUNNING" {
		return "INFO", prefix + " 正在处理"
	}
	if status == "SUCCEEDED" {
		return "SUCCESS", prefix + " 已完成"
	}
	switch errorCode {
	case "AI_INVALID_OUTPUT", "AI_INVALID_RESPONSE":
		return "WARN", prefix + " 输出未通过结构校验，将自动纠错或重试"
	case "AI_PROVIDER_TIMEOUT":
		return "WARN", prefix + " 调用超时"
	case "AI_RATE_LIMITED":
		return "WARN", prefix + " 触发上游限流，将自动重试"
	case "AI_REQUEST_CANCELED":
		return "WARN", prefix + " 已取消"
	default:
		return "ERROR", prefix + " 调用失败"
	}
}
