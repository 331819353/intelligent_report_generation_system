package datasource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// managedMetadataRepository 为刷新任务提供原子身份校验；导入仍使用 Repository 的可重新导入语义。
type managedMetadataRepository interface {
	ApplyManagedMetadata(context.Context, Source, string, string, SyncResult) (ManagedMetadataApplyResult, error)
	DeactivateManagedMetadata(context.Context, Source, TableSelection, time.Time) (bool, error)
}

// QueueImportTables 只校验并持久化用户选择，采样和 LLM 完善由 worker 执行。
func (s *Service) QueueImportTables(ctx context.Context, tenantID, actorID, sourceID string, selections []TableSelection) (MetadataJob, error) {
	if len(selections) == 0 || len(selections) > 100 {
		return MetadataJob{}, errors.New("between 1 and 100 tables must be selected")
	}
	seen := make(map[string]struct{}, len(selections))
	for _, selection := range selections {
		key := selection.CatalogName + "\x1f" + selection.SchemaName + "\x1f" + selection.TableName
		if selection.TableName == "" {
			return MetadataJob{}, errors.New("selected table name is required")
		}
		if _, exists := seen[key]; exists {
			return MetadataJob{}, errors.New("selected tables must be unique")
		}
		seen[key] = struct{}{}
	}
	source, err := s.metadataJobSource(ctx, tenantID, sourceID)
	if err != nil {
		return MetadataJob{}, err
	}
	hash, err := metadataJobSourceHash(source)
	if err != nil {
		return MetadataJob{}, err
	}
	return s.jobs.EnqueueMetadataJob(ctx, metadataJobRequest{
		TenantID: tenantID, DataSourceID: sourceID, RequestedBy: actorID, Kind: MetadataJobImport,
		Mode: MetadataRefreshFull, SourceConfigHash: hash, Tables: selections,
	})
}

// QueueRefreshTables 对已纳管表创建后台刷新任务；可选 tableIDs 用于安全限定单表或小批量刷新。
func (s *Service) QueueRefreshTables(ctx context.Context, tenantID, actorID, sourceID string, mode MetadataRefreshMode, tableIDs ...string) (MetadataJob, error) {
	if mode == "" {
		mode = MetadataRefreshIncremental
	}
	if mode != MetadataRefreshIncremental && mode != MetadataRefreshFull {
		return MetadataJob{}, errors.New("invalid metadata refresh mode")
	}
	source, err := s.metadataJobSource(ctx, tenantID, sourceID)
	if err != nil {
		return MetadataJob{}, err
	}
	selections, err := s.repo.ListActiveTableSelections(ctx, tenantID, sourceID)
	if err != nil {
		return MetadataJob{}, err
	}
	if len(tableIDs) > 0 {
		if len(tableIDs) > 100 {
			return MetadataJob{}, errors.New("at most 100 managed tables may be refreshed")
		}
		requested := make(map[string]struct{}, len(tableIDs))
		for _, tableID := range tableIDs {
			if tableID == "" {
				return MetadataJob{}, errors.New("managed table id is required")
			}
			if _, exists := requested[tableID]; exists {
				return MetadataJob{}, errors.New("managed table ids must be unique")
			}
			requested[tableID] = struct{}{}
		}
		selected := make([]TableSelection, 0, len(requested))
		for _, selection := range selections {
			if _, exists := requested[selection.TableID]; exists {
				selected = append(selected, selection)
				delete(requested, selection.TableID)
			}
		}
		if len(requested) != 0 {
			return MetadataJob{}, errors.New("managed table was not found in this data source")
		}
		selections = selected
	}
	hash, err := metadataJobSourceHash(source)
	if err != nil {
		return MetadataJob{}, err
	}
	return s.jobs.EnqueueMetadataJob(ctx, metadataJobRequest{
		TenantID: tenantID, DataSourceID: sourceID, RequestedBy: actorID, Kind: MetadataJobRefresh,
		Mode: mode, SourceConfigHash: hash, Tables: selections,
	})
}

func (s *Service) GetMetadataJob(ctx context.Context, tenantID, sourceID, jobID string) (MetadataJob, error) {
	if s.jobs == nil {
		return MetadataJob{}, errors.New("metadata job repository is not configured")
	}
	return s.jobs.GetMetadataJob(ctx, tenantID, sourceID, jobID)
}

func (s *Service) LatestActiveMetadataJob(ctx context.Context, tenantID, sourceID string) (*MetadataJob, error) {
	if s.jobs == nil {
		return nil, errors.New("metadata job repository is not configured")
	}
	return s.jobs.LatestActiveMetadataJob(ctx, tenantID, sourceID)
}

func (s *Service) MetadataJobTenantIDs(ctx context.Context) ([]string, error) {
	if s.jobs == nil {
		return nil, errors.New("metadata job repository is not configured")
	}
	return s.jobs.ListMetadataJobTenantIDs(ctx)
}

func (s *Service) metadataJobSource(ctx context.Context, tenantID, sourceID string) (Source, error) {
	if s.jobs == nil {
		return Source{}, errors.New("metadata job repository is not configured")
	}
	source, connector, err := s.load(ctx, tenantID, sourceID)
	if err != nil {
		return Source{}, err
	}
	if source.Status != StatusActive {
		return Source{}, fmt.Errorf("cannot process metadata from source in %s status", source.Status)
	}
	if s.completer == nil {
		return Source{}, errors.New("metadata AI completer is not configured")
	}
	if _, ok := connector.(MetadataSampler); !ok {
		return Source{}, errors.New("connector does not support metadata sampling")
	}
	return source, nil
}

// ProcessNextMetadataJob 领取并处理一个租户任务。表级错误被记录后继续，基础依赖错误才终止整批。
func (s *Service) ProcessNextMetadataJob(ctx context.Context, tenantID, workerID string, lease time.Duration) (bool, error) {
	if s.jobs == nil {
		return false, errors.New("metadata job repository is not configured")
	}
	claim, err := s.jobs.ClaimMetadataJob(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	jobCtx, cancel := context.WithCancel(ctx)
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan error, 1)
	go s.keepMetadataJobLease(jobCtx, stopHeartbeat, claim, workerID, lease, cancel, heartbeatDone)

	runErr := s.executeMetadataJob(jobCtx, claim, workerID, lease)
	// 正常收尾使用独立停止信号，避免取消正在执行的心跳 SQL 后把任务遗留为 RUNNING。
	close(stopHeartbeat)
	heartbeatErr := <-heartbeatDone
	cancel()
	if heartbeatErr != nil {
		return true, heartbeatErr
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return true, runErr
		}
		code, message := metadataJobSafeError(runErr)
		failCtx, failCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer failCancel()
		if failErr := s.jobs.FailMetadataJob(failCtx, claim.TenantID, claim.ID, workerID, code, message); failErr != nil {
			return true, fmt.Errorf("run metadata job: %v; persist failure: %w", runErr, failErr)
		}
		return true, runErr
	}
	_, err = s.jobs.FinishMetadataJob(ctx, claim.TenantID, claim.ID, workerID)
	return true, err
}

func (s *Service) keepMetadataJobLease(ctx context.Context, stop <-chan struct{}, claim *metadataJobClaim, workerID string, lease time.Duration, cancel context.CancelFunc, done chan<- error) {
	interval := lease / 3
	if interval < time.Second {
		interval = time.Second
	}
	heartbeatTimeout := lease / 6
	if heartbeatTimeout < time.Second {
		heartbeatTimeout = time.Second
	}
	if heartbeatTimeout > 10*time.Second {
		heartbeatTimeout = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			done <- nil
			return
		case <-ctx.Done():
			done <- nil
			return
		case <-ticker.C:
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			heartbeatCtx, heartbeatCancel := context.WithTimeout(ctx, heartbeatTimeout)
			err := s.jobs.HeartbeatMetadataJob(heartbeatCtx, claim.TenantID, claim.ID, workerID, lease)
			heartbeatCancel()
			if err != nil {
				cancel()
				done <- err
				return
			}
		}
	}
}

func (s *Service) executeMetadataJob(ctx context.Context, claim *metadataJobClaim, workerID string, lease time.Duration) error {
	// 崩溃可能发生在全部表已形成终态、批任务尚未收口之间；此时无需再次访问数据源。
	if claim.Total > 0 && claim.Completed == claim.Total {
		return nil
	}
	if claim.RequestedBy == "" {
		return metadataJobExecutionError{code: "REQUESTER_UNAVAILABLE", message: "任务提交人已不可用，请重新提交任务"}
	}
	source, connector, err := s.load(ctx, claim.TenantID, claim.DataSourceID)
	if err != nil {
		return metadataJobExecutionError{code: "DATA_SOURCE_UNAVAILABLE", message: "数据源不可用，请检查配置后重试", cause: err}
	}
	if source.Status != StatusActive {
		return metadataJobExecutionError{code: "DATA_SOURCE_NOT_ACTIVE", message: "数据源当前不是运行状态", cause: fmt.Errorf("status %s", source.Status)}
	}
	currentHash, err := metadataJobSourceHash(source)
	if err != nil {
		return err
	}
	if currentHash != claim.SourceConfigHash {
		return metadataJobExecutionError{code: "SOURCE_CONFIGURATION_CHANGED", message: "数据源配置已变更，请重新提交任务"}
	}
	sampler, ok := connector.(MetadataSampler)
	if !ok || s.completer == nil {
		return metadataJobExecutionError{code: "PROCESSOR_UNAVAILABLE", message: "元数据处理器暂不可用"}
	}
	var refreshRepository managedMetadataRepository
	if claim.Kind == MetadataJobRefresh {
		refreshRepository, ok = s.repo.(managedMetadataRepository)
		if !ok {
			return metadataJobExecutionError{code: "PROCESSOR_UNAVAILABLE", message: "元数据刷新仓储暂不可用"}
		}
	}
	if err := s.jobs.UpdateMetadataJobStage(ctx, claim.TenantID, claim.ID, workerID, "DISCOVERY", lease); err != nil {
		return err
	}
	discovered, err := connector.Sync(ctx, source)
	if err != nil {
		return metadataJobExecutionError{code: "DISCOVERY_FAILED", message: "读取源库表结构失败，请稍后重试", cause: err}
	}
	available := make(map[string]MetadataTable, len(discovered.Tables))
	for _, table := range discovered.Tables {
		available[metadataTableKey(table)] = table
	}
	if err := s.jobs.UpdateMetadataJobStage(ctx, claim.TenantID, claim.ID, workerID, "DIFF", lease); err != nil {
		return err
	}
	items, err := s.jobs.ListMetadataJobItems(ctx, claim.TenantID, claim.ID)
	if err != nil {
		return err
	}
	var deletionObservedAt time.Time
	if claim.Kind == MetadataJobRefresh {
		for _, item := range items {
			if item.Status == "SUCCEEDED" || item.Status == "SKIPPED" || item.Status == "FAILED" {
				continue
			}
			key := item.CatalogName + "\x1f" + item.SchemaName + "\x1f" + item.TableName
			if _, exists := available[key]; exists {
				continue
			}
			deletionObservedAt, err = authoritativeMetadataSnapshot(discovered)
			if err != nil {
				return metadataJobExecutionError{code: "DISCOVERY_FAILED", message: "读取源库表结构失败，请稍后重试", cause: err}
			}
			break
		}
	}
	for _, item := range items {
		if item.Status == "SUCCEEDED" || item.Status == "SKIPPED" || item.Status == "FAILED" {
			continue
		}
		if err := s.jobs.UpdateMetadataJobItem(ctx, claim.TenantID, claim.ID, item.ID, workerID, metadataJobItemUpdate{Status: "RUNNING", Stage: "DISCOVERY"}, lease); err != nil {
			return err
		}
		key := item.CatalogName + "\x1f" + item.SchemaName + "\x1f" + item.TableName
		table, exists := available[key]
		if !exists {
			if claim.Kind == MetadataJobRefresh {
				if err := s.ensureMetadataJobSourceCurrent(ctx, claim); err != nil {
					return err
				}
				managed, deactivateErr := refreshRepository.DeactivateManagedMetadata(ctx, source, TableSelection{
					CatalogName: item.CatalogName, SchemaName: item.SchemaName, TableName: item.TableName,
					TableID: item.TableID, StructureHash: item.PreviousStructureHash,
				}, deletionObservedAt)
				if errors.Is(deactivateErr, ErrMetadataSourceChanged) {
					return metadataJobExecutionError{code: "SOURCE_CONFIGURATION_CHANGED", message: "数据源状态或配置已变更，请重新提交任务", cause: deactivateErr}
				}
				if errors.Is(deactivateErr, ErrMetadataRefreshSuperseded) {
					if err := s.jobs.UpdateMetadataJobItem(ctx, claim.TenantID, claim.ID, item.ID, workerID, metadataJobItemUpdate{
						Status: "SKIPPED", Stage: "COMPLETE", TableID: item.TableID,
						ErrorCode: "STRUCTURE_SUPERSEDED", ErrorMessage: "表结构已被更新版本取代，请重新提交刷新",
					}, lease); err != nil {
						return err
					}
					continue
				}
				if deactivateErr != nil {
					return deactivateErr
				}
				status, code := "SUCCEEDED", "SOURCE_TABLE_REMOVED"
				if !managed {
					status, code = "SKIPPED", "ASSET_NOT_MANAGED"
				}
				if err := s.jobs.UpdateMetadataJobItem(ctx, claim.TenantID, claim.ID, item.ID, workerID, metadataJobItemUpdate{
					Status: status, Stage: "COMPLETE", TableID: item.TableID, ErrorCode: code,
				}, lease); err != nil {
					return err
				}
				continue
			}
			if err := s.failMetadataJobItem(ctx, claim, item, workerID, lease, "DISCOVERY", "SOURCE_TABLE_NOT_FOUND", "源库中未找到该表"); err != nil {
				return err
			}
			continue
		}
		structureHash, _, err := metadataTableHash(table)
		if err != nil {
			if err := s.failMetadataJobItem(ctx, claim, item, workerID, lease, "DIFF", "STRUCTURE_HASH_FAILED", "无法比较表结构"); err != nil {
				return err
			}
			continue
		}
		itemCompleted, err := s.jobs.IsMetadataJobItemCompleted(ctx, claim.TenantID, item.ID, item.TableID, structureHash)
		if err != nil {
			return err
		}
		if itemCompleted {
			if err := s.jobs.UpdateMetadataJobItem(ctx, claim.TenantID, claim.ID, item.ID, workerID, metadataJobItemUpdate{
				Status: "SUCCEEDED", Stage: "COMPLETE", TableID: item.TableID,
			}, lease); err != nil {
				return err
			}
			continue
		}
		enriched, err := s.jobs.IsMetadataTableEnriched(ctx, claim.TenantID, item.TableID, structureHash)
		if err != nil {
			return err
		}
		if enriched && claim.Kind == MetadataJobRefresh && claim.Mode == MetadataRefreshIncremental &&
			item.PreviousStructureHash == structureHash {
			if err := s.jobs.UpdateMetadataJobItem(ctx, claim.TenantID, claim.ID, item.ID, workerID, metadataJobItemUpdate{
				Status: "SKIPPED", Stage: "COMPLETE", TableID: item.TableID, ErrorCode: "UNCHANGED",
			}, lease); err != nil {
				return err
			}
			continue
		}
		if err := s.jobs.UpdateMetadataJobItem(ctx, claim.TenantID, claim.ID, item.ID, workerID, metadataJobItemUpdate{Status: "RUNNING", Stage: "PERSISTENCE", TableID: item.TableID}, lease); err != nil {
			return err
		}
		if err := s.ensureMetadataJobSourceCurrent(ctx, claim); err != nil {
			return err
		}
		metadata, err := selectedMetadataResult([]MetadataTable{table})
		if err != nil {
			return err
		}
		managed := true
		targetTable := true
		var targetColumnIDs []string
		var targetColumnNames []string
		var tableID string
		if claim.Kind == MetadataJobRefresh {
			var applied ManagedMetadataApplyResult
			applied, err = refreshRepository.ApplyManagedMetadata(ctx, source, item.TableID, item.PreviousStructureHash, metadata)
			tableID, managed = applied.TableID, applied.Managed
			if claim.Mode == MetadataRefreshIncremental {
				targetTable = applied.TablePending
				targetColumnIDs = make([]string, 0, len(applied.PendingColumns))
				targetColumnNames = make([]string, 0, len(applied.PendingColumns))
				for _, column := range applied.PendingColumns {
					targetColumnIDs = append(targetColumnIDs, column.ID)
					targetColumnNames = append(targetColumnNames, column.Name)
				}
			}
		} else {
			var ids map[string]string
			ids, err = s.repo.ApplySelectedMetadata(ctx, source, metadata)
			tableID = ids[metadataTableKey(table)]
		}
		if err != nil {
			if errors.Is(err, ErrMetadataSourceChanged) {
				return metadataJobExecutionError{code: "SOURCE_CONFIGURATION_CHANGED", message: "数据源状态或配置已变更，请重新提交任务", cause: err}
			}
			if errors.Is(err, ErrMetadataRefreshSuperseded) {
				if err := s.jobs.UpdateMetadataJobItem(ctx, claim.TenantID, claim.ID, item.ID, workerID, metadataJobItemUpdate{
					Status: "SKIPPED", Stage: "COMPLETE", TableID: item.TableID,
					ErrorCode: "STRUCTURE_SUPERSEDED", ErrorMessage: "表结构已被更新版本取代，请重新提交刷新",
				}, lease); err != nil {
					return err
				}
				continue
			}
			slog.ErrorContext(ctx, "metadata job technical persistence failed", "job_id", claim.ID, "table", item.TableName, "error", err)
			if err := s.failMetadataJobItem(ctx, claim, item, workerID, lease, "PERSISTENCE", "METADATA_UPDATE_FAILED", "保存表结构失败"); err != nil {
				return err
			}
			continue
		}
		if !managed {
			if err := s.jobs.UpdateMetadataJobItem(ctx, claim.TenantID, claim.ID, item.ID, workerID, metadataJobItemUpdate{
				Status: "SKIPPED", Stage: "COMPLETE", TableID: item.TableID,
				ErrorCode: "ASSET_NOT_MANAGED", ErrorMessage: "表资产已删除或不再受管",
			}, lease); err != nil {
				return err
			}
			continue
		}
		if claim.Kind == MetadataJobRefresh && claim.Mode == MetadataRefreshIncremental && !targetTable && len(targetColumnIDs) == 0 {
			if err := s.jobs.UpdateMetadataJobItem(ctx, claim.TenantID, claim.ID, item.ID, workerID, metadataJobItemUpdate{
				Status: "SUCCEEDED", Stage: "COMPLETE", TableID: tableID,
			}, lease); err != nil {
				return err
			}
			continue
		}
		var rows []map[string]any
		shouldSample := claim.Kind == MetadataJobImport || claim.Mode == MetadataRefreshFull || len(targetColumnNames) > 0
		if shouldSample {
			if err := s.jobs.UpdateMetadataJobItem(ctx, claim.TenantID, claim.ID, item.ID, workerID, metadataJobItemUpdate{Status: "RUNNING", Stage: "SAMPLE", TableID: tableID}, lease); err != nil {
				return err
			}
			if err := s.ensureMetadataJobSourceCurrent(ctx, claim); err != nil {
				return err
			}
			sample, sampleErr := sampler.Sample(ctx, source, table, 3)
			if sampleErr != nil {
				slog.ErrorContext(ctx, "metadata job table sampling failed", "job_id", claim.ID, "table", item.TableName, "error", sampleErr)
				if err := s.failMetadataJobItem(ctx, claim, item, workerID, lease, "SAMPLE", "SAMPLE_FAILED", "读取表样本失败"); err != nil {
					return err
				}
				continue
			}
			rows = sampleRowsForColumns(sample, targetColumnNames)
		}
		if err := s.jobs.UpdateMetadataJobItem(ctx, claim.TenantID, claim.ID, item.ID, workerID, metadataJobItemUpdate{Status: "RUNNING", Stage: "LLM", TableID: tableID}, lease); err != nil {
			return err
		}
		if err := s.ensureMetadataJobSourceCurrent(ctx, claim); err != nil {
			return err
		}
		if err := s.completer.CompleteTable(ctx, claim.TenantID, claim.RequestedBy, tableID, rows, targetTable, targetColumnIDs, structureHash, item.ID, workerID, source.Version); err != nil {
			slog.ErrorContext(ctx, "metadata job LLM completion failed", "job_id", claim.ID, "table", item.TableName, "error", err)
			if err := s.failMetadataJobItem(ctx, claim, item, workerID, lease, "LLM", "LLM_COMPLETION_FAILED", "LLM 表结构完善失败"); err != nil {
				return err
			}
			continue
		}
		if err := s.jobs.UpdateMetadataJobItem(ctx, claim.TenantID, claim.ID, item.ID, workerID, metadataJobItemUpdate{Status: "SUCCEEDED", Stage: "COMPLETE", TableID: tableID}, lease); err != nil {
			return err
		}
	}
	return nil
}

// authoritativeMetadataSnapshot 只允许完整且可审计的发现快照驱动“源表已删除”判断。
// 普通表刷新仍可使用目标表结果，但缺失表绝不能由计数不一致、重复键或无效水位的部分响应推断。
func authoritativeMetadataSnapshot(result SyncResult) (time.Time, error) {
	if result.Assets != len(result.Tables) {
		return time.Time{}, errors.New("connector metadata snapshot asset count is inconsistent")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, result.Watermark)
	if err != nil {
		return time.Time{}, errors.New("connector metadata snapshot watermark is invalid")
	}
	digest, err := hex.DecodeString(result.SnapshotHash)
	if err != nil || len(digest) != sha256.Size {
		return time.Time{}, errors.New("connector metadata snapshot hash is invalid")
	}
	seen := make(map[string]struct{}, len(result.Tables))
	for _, table := range result.Tables {
		if table.Name == "" {
			return time.Time{}, errors.New("connector metadata snapshot contains an unnamed table")
		}
		key := metadataTableKey(table)
		if _, exists := seen[key]; exists {
			return time.Time{}, errors.New("connector metadata snapshot contains duplicate table keys")
		}
		seen[key] = struct{}{}
	}
	return observedAt, nil
}

func (s *Service) ensureMetadataJobSourceCurrent(ctx context.Context, claim *metadataJobClaim) error {
	current, err := s.repo.Get(ctx, claim.TenantID, claim.DataSourceID)
	if err != nil {
		return metadataJobExecutionError{code: "DATA_SOURCE_UNAVAILABLE", message: "数据源不可用，请重新提交任务", cause: err}
	}
	if current.Status != StatusActive {
		return metadataJobExecutionError{code: "DATA_SOURCE_NOT_ACTIVE", message: "数据源状态已变化，请重新提交任务"}
	}
	hash, err := metadataJobSourceHash(current)
	if err != nil {
		return err
	}
	if hash != claim.SourceConfigHash {
		return metadataJobExecutionError{code: "SOURCE_CONFIGURATION_CHANGED", message: "数据源配置已变更，请重新提交任务"}
	}
	return nil
}

func (s *Service) failMetadataJobItem(ctx context.Context, claim *metadataJobClaim, item metadataJobItem, workerID string, lease time.Duration, stage, code, message string) error {
	return s.jobs.UpdateMetadataJobItem(ctx, claim.TenantID, claim.ID, item.ID, workerID, metadataJobItemUpdate{
		Status: "FAILED", Stage: "FAILED", TableID: item.TableID, ErrorCode: code, ErrorMessage: message,
	}, lease)
}

func metadataJobSourceHash(source Source) (string, error) {
	payload, err := json.Marshal(struct {
		Type        Type           `json:"type"`
		Config      map[string]any `json:"config"`
		SecretRef   string         `json:"secretRef"`
		FileAssetID string         `json:"fileAssetId"`
	}{source.Type, source.Config, source.SecretRef, source.FileAssetID})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

type metadataJobExecutionError struct {
	code, message string
	cause         error
}

func (e metadataJobExecutionError) Error() string {
	if e.cause != nil {
		return e.code + ": " + e.cause.Error()
	}
	return e.code
}

func metadataJobSafeError(err error) (string, string) {
	var executionErr metadataJobExecutionError
	if errors.As(err, &executionErr) {
		return executionErr.code, executionErr.message
	}
	return "JOB_EXECUTION_FAILED", "后台处理失败，请稍后重试"
}
