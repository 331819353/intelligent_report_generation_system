package report

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metric"
	"intelligent-report-generation-system/internal/reportjson"
)

const (
	maxQueryBatchCards  = 50
	maxQueryConcurrency = 6
	queryCacheEntries   = 1024
	queryCacheTTL       = 30 * time.Second
)

type MetricQueryExecutor interface {
	PreviewVersion(context.Context, string, string, string, string, metric.PreviewInput) (dataset.PreviewResult, error)
}

type ReportQueryBatchInput struct {
	CardIDs            []string                            `json:"cardIds"`
	Filters            map[string]any                      `json:"filters"`
	InteractionContext map[string]ReportInteractionContext `json:"interactionContext,omitempty"`
}

// ReportInteractionContext 只接收交互身份和值；维度、目标卡和下钻分组必须从可信 DSL 推导。
type ReportInteractionContext struct {
	SourceCardID  string `json:"sourceCardId"`
	InteractionID string `json:"interactionId"`
	Value         any    `json:"value"`
}

type trustedInteraction struct {
	FilterDimensionID string
	GroupDimensionID  string
	Value             any
}

type ReportQueryBatchResult struct {
	RequestID string            `json:"requestId"`
	Results   []CardQueryResult `json:"results"`
}

type CardQueryResult struct {
	CardID       string                   `json:"cardId"`
	Status       string                   `json:"status"`
	Columns      []ReportQueryColumn      `json:"columns"`
	Rows         [][]any                  `json:"rows"`
	RowCount     int                      `json:"rowCount"`
	DurationMS   int64                    `json:"durationMs"`
	CacheHit     bool                     `json:"cacheHit"`
	ErrorCode    string                   `json:"errorCode,omitempty"`
	ErrorMessage string                   `json:"errorMessage,omitempty"`
	Warnings     []dataset.PreviewWarning `json:"warnings,omitempty"`
}

type ReportQueryColumn struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	FieldID       string `json:"fieldId,omitempty"`
	Role          string `json:"role,omitempty"`
	CanonicalType string `json:"canonicalType,omitempty"`
}

type reportQueryRuntime struct {
	executor MetricQueryExecutor
	cacheMu  sync.Mutex
	cache    map[string]queryCacheEntry
	group    singleflight.Group
}

type queryCacheEntry struct {
	result    dataset.PreviewResult
	expiresAt time.Time
}

func newReportQueryRuntime() *reportQueryRuntime {
	return &reportQueryRuntime{cache: map[string]queryCacheEntry{}}
}

func (s *Service) SetMetricQueryExecutor(executor MetricQueryExecutor) { s.query.executor = executor }

func (s *Service) QueryDraft(ctx context.Context, tenantID, actorID, id string, input ReportQueryBatchInput) (ReportQueryBatchResult, error) {
	if !validPublicationIdentity(tenantID, actorID, id) || s.query.executor == nil {
		return ReportQueryBatchResult{}, ErrQueryUnavailable
	}
	record, err := s.store.Get(ctx, tenantID, actorID, id, "READ")
	if err != nil {
		return ReportQueryBatchResult{}, err
	}
	prepared, err := reportjson.Prepare(record.Definition)
	if err != nil || !prepared.Document.IsCardDSL() {
		return ReportQueryBatchResult{}, ErrQueryUnavailable
	}
	return s.query.executeBatch(ctx, tenantID, actorID, prepared.Document, nil, input)
}

func (s *Service) QueryPublished(ctx context.Context, tenantID, actorID, id string, version int, input ReportQueryBatchInput) (ReportQueryBatchResult, error) {
	if !validPublicationIdentity(tenantID, actorID, id) || version < 1 || s.query.executor == nil {
		return ReportQueryBatchResult{}, ErrQueryUnavailable
	}
	artifact, err := s.GetVersionArtifact(ctx, tenantID, actorID, id, version)
	if err != nil {
		return ReportQueryBatchResult{}, err
	}
	prepared, err := reportjson.Prepare(artifact.Definition)
	if err != nil || !prepared.Document.IsCardDSL() {
		return ReportQueryBatchResult{}, ErrQueryUnavailable
	}
	versions := publishedMetricVersions(prepared.Document)
	return s.query.executeBatch(ctx, tenantID, actorID, prepared.Document, versions, input)
}

func (runtime *reportQueryRuntime) executeBatch(ctx context.Context, tenantID, actorID string, document reportjson.Document, publishedVersions map[string]string, input ReportQueryBatchInput) (ReportQueryBatchResult, error) {
	cards, err := resolveRequestedCards(document, input.CardIDs)
	if err != nil {
		return ReportQueryBatchResult{}, err
	}
	if err := validateRuntimeFilters(document, cards, input); err != nil {
		return ReportQueryBatchResult{}, err
	}
	requestID := uuid.NewString()
	results := make([]CardQueryResult, len(cards))
	semaphore := make(chan struct{}, maxQueryConcurrency)
	var wait sync.WaitGroup
	for index, card := range cards {
		index, card := index, card
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results[index] = cardQueryError(card.ID, "QUERY_CANCELLED", "查询已取消")
				return
			}
			results[index] = runtime.executeCard(ctx, tenantID, actorID, requestID, document, card, publishedVersions, input)
		}()
	}
	wait.Wait()
	return ReportQueryBatchResult{RequestID: requestID, Results: results}, nil
}

func (runtime *reportQueryRuntime) executeCard(ctx context.Context, tenantID, actorID, requestID string, document reportjson.Document, card reportjson.Card, publishedVersions map[string]string, input ReportQueryBatchInput) CardQueryResult {
	started := time.Now()
	if card.Type == "TITLE" {
		return CardQueryResult{CardID: card.ID, Status: "SUCCESS", Columns: []ReportQueryColumn{}, Rows: [][]any{}, Warnings: []dataset.PreviewWarning{}}
	}
	if len(card.Binding.Metrics) == 0 {
		return cardQueryError(card.ID, "BINDING_INCOMPLETE", "卡片尚未绑定指标")
	}
	filters, err := compileMetricFilters(document, card, input)
	if err != nil {
		return cardQueryError(card.ID, "FILTER_INVALID", err.Error())
	}
	previews := make([]dataset.PreviewResult, 0, len(card.Binding.Metrics))
	cacheHit := true
	for metricIndex, binding := range card.Binding.Metrics {
		versionID := binding.VersionID
		if publishedVersions != nil {
			versionID = publishedVersions[binding.ID]
		}
		if versionID == "" {
			return cardQueryError(card.ID, "METRIC_VERSION_REQUIRED", "指标必须绑定精确发布版本")
		}
		queryDimensions := dimensionIDs(card.Binding.Dimensions)
		if interaction, exists := input.InteractionContext[card.ID]; exists {
			trusted, trustedErr := resolveTrustedInteraction(document, card.ID, interaction)
			if trustedErr != nil {
				return cardQueryError(card.ID, "INTERACTION_INVALID", "交互上下文无效")
			}
			if trusted.GroupDimensionID != "" {
				queryDimensions = []string{trusted.GroupDimensionID}
			}
		}
		previewInput := metric.PreviewInput{
			QueryID: fmt.Sprintf("%s-%d", requestID, metricIndex), Parameters: map[string]any{},
			DimensionFieldIDs: queryDimensions, DimensionFilters: filters,
			MetricSortDirection: metricSortDirection(card), MaxRows: boundedCardLimit(card.Binding.Limit),
		}
		preview, hit, queryErr := runtime.executeMetric(ctx, tenantID, actorID, binding.ID, versionID, previewInput)
		if queryErr != nil {
			return cardQueryError(card.ID, metricQueryErrorCode(queryErr), "指标查询失败")
		}
		cacheHit = cacheHit && hit
		previews = append(previews, preview)
	}
	dimensionCount := len(card.Binding.Dimensions)
	if interaction, exists := input.InteractionContext[card.ID]; exists {
		trusted, _ := resolveTrustedInteraction(document, card.ID, interaction)
		if trusted.GroupDimensionID != "" {
			dimensionCount = 1
		}
	}
	merged, err := mergeMetricPreviews(card, previews, dimensionCount)
	if err != nil {
		return cardQueryError(card.ID, "RESULT_SHAPE_INVALID", err.Error())
	}
	merged.CardID, merged.DurationMS, merged.CacheHit = card.ID, time.Since(started).Milliseconds(), cacheHit
	return merged
}

func (runtime *reportQueryRuntime) executeMetric(ctx context.Context, tenantID, actorID, metricID, versionID string, input metric.PreviewInput) (dataset.PreviewResult, bool, error) {
	fingerprint := queryFingerprint(tenantID, actorID, metricID, versionID, input)
	if cached, ok := runtime.cached(fingerprint); ok {
		return cached, true, nil
	}
	value, err, shared := runtime.group.Do(fingerprint, func() (any, error) {
		if cached, ok := runtime.cached(fingerprint); ok {
			return cached, nil
		}
		result, queryErr := runtime.executor.PreviewVersion(ctx, tenantID, actorID, metricID, versionID, input)
		if queryErr == nil {
			runtime.putCache(fingerprint, result)
		}
		return result, queryErr
	})
	if err != nil {
		return dataset.PreviewResult{}, false, err
	}
	return value.(dataset.PreviewResult), shared, nil
}

func (runtime *reportQueryRuntime) cached(key string) (dataset.PreviewResult, bool) {
	runtime.cacheMu.Lock()
	defer runtime.cacheMu.Unlock()
	entry, ok := runtime.cache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(runtime.cache, key)
		return dataset.PreviewResult{}, false
	}
	return clonePreview(entry.result), true
}

func (runtime *reportQueryRuntime) putCache(key string, result dataset.PreviewResult) {
	runtime.cacheMu.Lock()
	defer runtime.cacheMu.Unlock()
	if len(runtime.cache) >= queryCacheEntries {
		now := time.Now()
		for cacheKey, entry := range runtime.cache {
			if now.After(entry.expiresAt) {
				delete(runtime.cache, cacheKey)
			}
		}
	}
	if len(runtime.cache) >= queryCacheEntries {
		for cacheKey := range runtime.cache {
			delete(runtime.cache, cacheKey)
			break
		}
	}
	jitter := time.Duration(key[0]%10) * time.Second
	runtime.cache[key] = queryCacheEntry{result: clonePreview(result), expiresAt: time.Now().Add(queryCacheTTL + jitter)}
}

func resolveRequestedCards(document reportjson.Document, requested []string) ([]reportjson.Card, error) {
	byID := make(map[string]reportjson.Card, len(document.Cards))
	for _, card := range document.Cards {
		byID[card.ID] = card
	}
	if len(requested) == 0 {
		requested = make([]string, 0, len(document.Cards))
		for _, card := range document.Cards {
			if card.Type != "TITLE" {
				requested = append(requested, card.ID)
			}
		}
	}
	if len(requested) > maxQueryBatchCards {
		return nil, ErrQueryInvalid
	}
	seen := map[string]bool{}
	result := make([]reportjson.Card, 0, len(requested))
	for _, id := range requested {
		card, exists := byID[id]
		if !exists || seen[id] || card.Type == "TITLE" {
			return nil, ErrQueryInvalid
		}
		seen[id] = true
		result = append(result, card)
	}
	return result, nil
}

func validateRuntimeFilters(document reportjson.Document, cards []reportjson.Card, input ReportQueryBatchInput) error {
	filters := map[string]reportjson.GlobalFilter{}
	for _, filter := range document.GlobalFilters {
		filters[filter.ID] = filter
		_, present := input.Filters[filter.ID]
		if filter.Required && !present && filter.DefaultValue == nil {
			return ErrQueryInvalid
		}
	}
	for id, value := range input.Filters {
		filter, exists := filters[id]
		if !exists || !validRuntimeFilterValue(filter, value) {
			return ErrQueryInvalid
		}
	}
	cardIDs := map[string]reportjson.Card{}
	for _, card := range cards {
		cardIDs[card.ID] = card
	}
	for cardID, interaction := range input.InteractionContext {
		if _, exists := cardIDs[cardID]; !exists || !validScalarOrList(interaction.Value) {
			return ErrQueryInvalid
		}
		if _, err := resolveTrustedInteraction(document, cardID, interaction); err != nil {
			return ErrQueryInvalid
		}
	}
	return nil
}

func compileMetricFilters(document reportjson.Document, card reportjson.Card, input ReportQueryBatchInput) ([]metric.DimensionFilter, error) {
	compiled := make([]metric.DimensionFilter, 0)
	for _, binding := range card.Binding.GlobalFilterBindings {
		if binding.Enabled != nil && !*binding.Enabled {
			continue
		}
		value, present := input.Filters[binding.FilterID]
		filter, exists := findGlobalFilter(document.GlobalFilters, binding.FilterID)
		if !exists {
			return nil, ErrQueryInvalid
		}
		if !present {
			value = filter.DefaultValue
			if value == nil {
				continue
			}
		}
		normalized, err := normalizeRuntimeFilterValue(filter, value, document.Report.Timezone)
		if err != nil {
			return nil, err
		}
		items, err := translateMetricFilter(binding.TargetDimensionID, filter.Operator, normalized)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, items...)
	}
	for _, filter := range card.Binding.Filters {
		items, err := translateMetricFilter(filter.DimensionID, filter.Operator, filter.Value)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, items...)
	}
	if interaction, exists := input.InteractionContext[card.ID]; exists {
		trusted, err := resolveTrustedInteraction(document, card.ID, interaction)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, metric.DimensionFilter{FieldID: trusted.FilterDimensionID, Operator: "EQUALS", Value: trusted.Value})
	}
	return compiled, nil
}

func translateMetricFilter(fieldID, operator string, value any) ([]metric.DimensionFilter, error) {
	switch operator {
	case "equals":
		return []metric.DimensionFilter{{FieldID: fieldID, Operator: "EQUALS", Value: value}}, nil
	case "notEquals":
		return []metric.DimensionFilter{{FieldID: fieldID, Operator: "NOT_EQUALS", Value: value}}, nil
	case "in":
		return []metric.DimensionFilter{{FieldID: fieldID, Operator: "IN", Value: value}}, nil
	case "notIn":
		return []metric.DimensionFilter{{FieldID: fieldID, Operator: "NOT_IN", Value: value}}, nil
	case "gte":
		return []metric.DimensionFilter{{FieldID: fieldID, Operator: "GTE", Value: value}}, nil
	case "lt":
		return []metric.DimensionFilter{{FieldID: fieldID, Operator: "LT", Value: value}}, nil
	case "between":
		values, ok := value.([]any)
		if !ok || len(values) != 2 {
			return nil, ErrQueryInvalid
		}
		return []metric.DimensionFilter{{FieldID: fieldID, Operator: "GTE", Value: values[0]}, {FieldID: fieldID, Operator: "LT", Value: values[1]}}, nil
	default:
		return nil, ErrQueryInvalid
	}
}

func mergeMetricPreviews(card reportjson.Card, previews []dataset.PreviewResult, dimensionCount int) (CardQueryResult, error) {
	if len(previews) == 0 {
		return cardQueryError(card.ID, "NO_METRIC", "卡片未绑定指标"), nil
	}
	base := clonePreview(previews[0])
	if dimensionCount > len(base.Columns) {
		return CardQueryResult{}, errors.New("指标结果缺少维度列")
	}
	columns := convertColumns(base.ColumnMetadata)
	if len(columns) != len(base.Columns) {
		columns = make([]ReportQueryColumn, len(base.Columns))
		for index, code := range base.Columns {
			columns[index] = ReportQueryColumn{Code: code, Name: code, Role: columnRole(index, dimensionCount)}
		}
	}
	rows := base.Rows
	warnings := append([]dataset.PreviewWarning{}, base.Warnings...)
	for previewIndex := 1; previewIndex < len(previews); previewIndex++ {
		preview := previews[previewIndex]
		if len(preview.Columns) <= dimensionCount {
			return CardQueryResult{}, errors.New("指标结果缺少度量列")
		}
		metricColumn := len(preview.Columns) - 1
		lookup := map[string]any{}
		for _, row := range preview.Rows {
			if len(row) <= metricColumn {
				continue
			}
			lookup[dimensionKey(row, dimensionCount)] = row[metricColumn]
		}
		for rowIndex := range rows {
			rows[rowIndex] = append(rows[rowIndex], lookup[dimensionKey(rows[rowIndex], dimensionCount)])
		}
		metadata := ReportQueryColumn{Code: preview.Columns[metricColumn], Name: preview.Columns[metricColumn], Role: "METRIC"}
		if metricColumn < len(preview.ColumnMetadata) {
			metadata = convertColumn(preview.ColumnMetadata[metricColumn])
		}
		columns = append(columns, metadata)
		warnings = append(warnings, preview.Warnings...)
	}
	return CardQueryResult{Status: "SUCCESS", Columns: columns, Rows: rows, RowCount: len(rows), Warnings: warnings}, nil
}

func publishedMetricVersions(document reportjson.Document) map[string]string {
	publication, ok := document.Extensions["publication"].(map[string]any)
	if !ok {
		return map[string]string{}
	}
	values, ok := publication["metricVersions"].(map[string]any)
	if !ok {
		return map[string]string{}
	}
	result := map[string]string{}
	for id, value := range values {
		if version, ok := value.(string); ok {
			result[id] = version
		}
	}
	return result
}

func queryFingerprint(tenantID, actorID, metricID, versionID string, input metric.PreviewInput) string {
	input.QueryID = ""
	payload, _ := json.Marshal(struct {
		Tenant, Actor, Metric, Version string
		Input                          metric.PreviewInput
	}{tenantID, actorID, metricID, versionID, input})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func clonePreview(value dataset.PreviewResult) dataset.PreviewResult {
	payload, _ := json.Marshal(value)
	var cloned dataset.PreviewResult
	_ = json.Unmarshal(payload, &cloned)
	return cloned
}

func convertColumns(values []dataset.PreviewColumn) []ReportQueryColumn {
	result := make([]ReportQueryColumn, len(values))
	for index, value := range values {
		result[index] = convertColumn(value)
	}
	return result
}
func convertColumn(value dataset.PreviewColumn) ReportQueryColumn {
	return ReportQueryColumn{Code: value.Code, Name: value.Name, FieldID: value.FieldID, Role: strings.ToUpper(value.Role), CanonicalType: value.CanonicalType}
}
func columnRole(index, dimensionCount int) string {
	if index < dimensionCount {
		return "DIMENSION"
	}
	return "METRIC"
}
func dimensionKey(row []any, count int) string {
	payload, _ := json.Marshal(row[:min(count, len(row))])
	return string(payload)
}
func dimensionIDs(values []reportjson.DimensionBinding) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.ID
	}
	return result
}
func metricSortDirection(card reportjson.Card) string {
	if len(card.Binding.Sort) > 0 && strings.EqualFold(card.Binding.Sort[0].Direction, "asc") {
		return "ASC"
	}
	return "DESC"
}
func boundedCardLimit(value int) int {
	if value < 1 {
		return 100
	}
	if value > 1000 {
		return 1000
	}
	return value
}
func findGlobalFilter(filters []reportjson.GlobalFilter, id string) (reportjson.GlobalFilter, bool) {
	for _, filter := range filters {
		if filter.ID == id {
			return filter, true
		}
	}
	return reportjson.GlobalFilter{}, false
}
func resolveTrustedInteraction(document reportjson.Document, targetCardID string, context ReportInteractionContext) (trustedInteraction, error) {
	var source *reportjson.Card
	for index := range document.Cards {
		if document.Cards[index].ID == context.SourceCardID {
			source = &document.Cards[index]
			break
		}
	}
	if source == nil || len(source.Binding.Dimensions) == 0 || context.InteractionID == "" {
		return trustedInteraction{}, ErrQueryInvalid
	}
	var configured *reportjson.CardInteraction
	for index := range source.Interactions {
		if source.Interactions[index].ID == context.InteractionID {
			configured = &source.Interactions[index]
			break
		}
	}
	if configured == nil {
		return trustedInteraction{}, ErrQueryInvalid
	}
	resolved := trustedInteraction{FilterDimensionID: source.Binding.Dimensions[0].ID, Value: context.Value}
	switch configured.Action.Type {
	case "drillDown":
		if targetCardID != source.ID || configured.Action.ToDimension == "" {
			return trustedInteraction{}, ErrQueryInvalid
		}
		resolved.GroupDimensionID = configured.Action.ToDimension
	case "crossFilter":
		if configured.Action.TargetCardID != targetCardID {
			return trustedInteraction{}, ErrQueryInvalid
		}
		if configured.Action.ToDimension != "" {
			resolved.FilterDimensionID = configured.Action.ToDimension
		}
	default:
		return trustedInteraction{}, ErrQueryInvalid
	}
	return resolved, nil
}
func validRuntimeFilterValue(filter reportjson.GlobalFilter, value any) bool {
	if value == nil {
		return !filter.Required
	}
	if filter.MultiValue || filter.Type == "MULTI_SELECT" || filter.Operator == "between" {
		if relativeDateValue(value) != "" && filter.Operator == "between" {
			return true
		}
		items, ok := value.([]any)
		return ok && len(items) > 0 && len(items) <= 100
	}
	return validScalarOrList(value)
}

func normalizeRuntimeFilterValue(filter reportjson.GlobalFilter, value any, timezone string) (any, error) {
	relative := relativeDateValue(value)
	if relative == "" {
		return value, nil
	}
	if filter.Operator != "between" {
		return nil, ErrQueryInvalid
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, ErrQueryInvalid
	}
	now := time.Now().In(location)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	var start, end time.Time
	switch relative {
	case "last_7_days":
		start, end = today.AddDate(0, 0, -6), today.AddDate(0, 0, 1)
	case "last_30_days":
		start, end = today.AddDate(0, 0, -29), today.AddDate(0, 0, 1)
	case "last_90_days":
		start, end = today.AddDate(0, 0, -89), today.AddDate(0, 0, 1)
	case "this_month":
		start = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, location)
		end = start.AddDate(0, 1, 0)
	case "previous_month":
		end = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, location)
		start = end.AddDate(0, -1, 0)
	default:
		return nil, ErrQueryInvalid
	}
	return []any{start.Format("2006-01-02"), end.Format("2006-01-02")}, nil
}

func relativeDateValue(value any) string {
	record, ok := value.(map[string]any)
	if !ok || record["type"] != "relativeDate" {
		return ""
	}
	text, _ := record["value"].(string)
	return text
}
func validScalarOrList(value any) bool {
	switch current := value.(type) {
	case string:
		return len(current) <= 1000
	case bool, json.Number, float64:
		return true
	case []any:
		return len(current) <= 100
	default:
		return false
	}
}
func cardQueryError(cardID, code, message string) CardQueryResult {
	return CardQueryResult{CardID: cardID, Status: "ERROR", Columns: []ReportQueryColumn{}, Rows: [][]any{}, Warnings: []dataset.PreviewWarning{}, ErrorCode: code, ErrorMessage: message}
}
func metricQueryErrorCode(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "QUERY_CANCELLED"
	case errors.Is(err, metric.ErrForbidden):
		return "METRIC_FORBIDDEN"
	case errors.Is(err, metric.ErrVersionUnavailable), errors.Is(err, metric.ErrVersionNotFound):
		return "METRIC_VERSION_UNAVAILABLE"
	default:
		return "METRIC_QUERY_FAILED"
	}
}
