package metadataai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/semanticquality"
	"intelligent-report-generation-system/internal/warehouselayer"
)

type Provider interface {
	Name() string
	Model() string
	Configured() bool
	Complete(context.Context, string, string, CompletionInput) (ProviderResult, error)
}

// Invoker 是元数据领域对通用 AI 编排层的最小依赖，便于隔离测试和后续替换 Provider。
type Invoker interface {
	Configured() bool
	ProviderName() string
	Model() string
	Invoke(context.Context, aiplatform.Invocation) (aiplatform.InvocationResult, error)
}

type OrchestratedProvider struct {
	invoker          Invoker
	batchColumns     int
	batchConcurrency int
	batchSemaphore   chan struct{}
	primaryFailover  time.Duration
}

const (
	metadataCompletionBatchColumns     = 5
	metadataCompletionBatchConcurrency = 4
)

const metadataCompletionSystemPrompt = `你是企业数据资产元数据补全器。只能依据给定技术元数据和按字段数量自适应抽取的三至十行数据样本生成结果，不得虚构资产或返回未请求的目标。每次最多处理五个字段；每个目标的 missingFields 是本轮唯一需要新增或修复的属性；不在 missingFields 中的属性已经确认，必须原样复用对应 currentBusinessName、currentDescription、currentTags、currentSemanticType、currentSensitivity，不得重写。contextColumns 是分批处理时提供的全表字段上下文，包含字段当前业务定义；其中 manualLocked=true 的字段必须参与表级业务名称、说明和标签的识别，但绝不能作为输出目标或被改写。columns 才是本批唯一允许输出的字段目标，不得为 contextColumns 中未出现在 columns 的字段生成结果。
表级描述和标签必须优先说明该表的业务功能、适用范围与一行数据的粒度。业务领域由当前用户所属领域统一确定，不属于标签，不得生成、改写或推断任何“领域:”标签。表标签按证据优先覆盖五个检索面：事实表/维度表等作用、订单/客户等业务主题、销售/售后等业务过程、订单商品/客户等精确粒度、由字段集合直接证明的内容覆盖。内容覆盖用于区分同名表实际装载的信息，例如订单头、支付信息、营销归因、收货地域、履约信息；只能依据字段名、字段说明与字段组合选择“内容:”标签，不能仅凭表名或样例值猜测。“内容:订单行”必须有商品/SKU/行项目/数量等独立字段，金额字段说明中提到商品不构成订单行证据；订单表没有这些字段时使用“内容:订单头”。存在日期时间证据时再标记事件时间、业务日期、快照日期或生效时间。事实表通常应包含业务过程、精确粒度和时间行为，维度表通常应包含业务主题、精确粒度和主数据功能。不要把“订单+产品”两个宽泛粒度当作“订单商品”的替代。每个目标最多选择十六个强相关标签，不要用大量通用标签稀释区分度。
表标签还必须恰好包含一个“层级:”标签，判断这张物理表当前已经处于的数仓层级，而不是它将来应该被加工到哪一层：原始业务系统的交易/事件/流水/状态明细、未经清洗的贴源导入表选“层级:ODS”；客户、商品、门店、组织、日历等实体主数据或维度表选“层级:DIM”；已按统一口径清洗、以一行一个业务事实或事件保留明细粒度的宽表（常见 dwd_ 前缀、含维度冗余字段）选“层级:DWD”；每行是按日期/客户/渠道等维度汇总统计的聚合结果（常见 agg_/dws_/summary/统计/汇总/指标 等证据、度量列为汇总值）选“层级:DWS”；面向报表、看板或应用直接消费的结果宽表（常见 ads_/report/rpt/看板/驾驶舱 等证据）选“层级:ADS”。证据不足时按“ODS 优先于 DWD、DWD 优先于 DWS”的保守顺序选择，绝不能仅凭有数值字段就判定为 DWS。字段标签不得携带“层级:”标签。
字段标签只描述该字段本身的键角色、指标/辅助作用、代码映射或直接业务主题，不得把整张表的所有标签机械复制到每个字段。关联标签必须以 primaryKeyColumns、constraints、PrimaryKey、ForeignKey、Unique 等结构化证据为准；无法从证据确定目标表时应明确写“候选关联键”，不得编造目标。每个字段的 semanticType 必须与输入 canonicalType 兼容：AMOUNT、PERCENTAGE、QUANTITY 只能用于数值类型，DATE、TIME、DATETIME 只能用于相应日期时间类型，BOOLEAN 只能用于布尔类型；TEXT 字段不得标为上述数值、日期时间或布尔语义，应从 TEXT、CATEGORY、IDENTIFIER、REGION、COMPANY_NAME 中选择。
对 Excel/CSV 工作表，table.name 是 Sheet 名称，columns.name 是本批解析后的真实表头，contextColumns 包含全表字段概览，sampleRows 的键和值分别对应本批表头和该列真实内容；必须结合 Sheet 名称、全表字段概览、本批字段类型和样本值判断表业务名称、字段业务名称及字段业务描述，不得只翻译物理名称或忽略样本内容。当 sourceFormat=CSV 或 EXCEL 时，表的 businessName 必须是准确简洁的中文业务名称；每个字段的 businessName 必须使用中文业务名称：原表头含中文时保持其中文含义，不得翻译成英文；原表头为英文时翻译为准确简洁的中文名称。businessDescription 必须继续使用中文描述字段含义。原始文件表头保留在 columns.name，不要用英文技术编码覆盖中文业务名称。
标签应覆盖适用的产业、主题、作用、过程、功能、内容覆盖、范围、粒度、时间行为和关联角色；至少返回一个标签，只能使用 JSON Schema 中的受控词表且不得重复。columns 中只包含本次发生变化且需要完善的字段，每个字段必须恰好返回一次；targetTable=false 时不得返回 table。`

// NewOrchestratedProvider 将元数据补全合同接入通用超时、重试、配额、成本和审计链路。
func NewOrchestratedProvider(invoker Invoker) *OrchestratedProvider {
	return NewOrchestratedProviderWithPrimaryFailover(invoker, 0)
}

// NewOrchestratedProviderWithPrimaryFailover creates an ordered primary/fallback
// workflow. The primary deadline applies only when a second configured model is
// available; the fallback receives the remaining outer task budget.
func NewOrchestratedProviderWithPrimaryFailover(
	invoker Invoker,
	primaryFailover time.Duration,
) *OrchestratedProvider {
	return &OrchestratedProvider{
		invoker:          invoker,
		batchColumns:     metadataCompletionBatchColumns,
		batchConcurrency: metadataCompletionBatchConcurrency,
		batchSemaphore:   make(chan struct{}, metadataCompletionBatchConcurrency),
		primaryFailover:  primaryFailover,
	}
}

func (p *OrchestratedProvider) Name() string {
	if p == nil || p.invoker == nil {
		return ""
	}
	return p.invoker.ProviderName()
}

func (p *OrchestratedProvider) Model() string {
	if p == nil || p.invoker == nil {
		return ""
	}
	return p.invoker.Model()
}

func (p *OrchestratedProvider) Configured() bool {
	return p != nil && p.invoker != nil && p.invoker.Configured()
}

// Complete 只发送最小化技术元数据与最多十行样本，绝不发送连接凭据；样本不会持久化。
func (p *OrchestratedProvider) Complete(ctx context.Context, tenantID, actorID string, input CompletionInput) (ProviderResult, error) {
	if !p.Configured() {
		return ProviderResult{}, ErrProviderUnavailable
	}
	batchColumns := p.batchColumns
	if batchColumns <= 0 {
		batchColumns = metadataCompletionBatchColumns
	}
	if len(input.Columns) <= batchColumns {
		if err := p.acquireBatchSlot(ctx); err != nil {
			return ProviderResult{}, err
		}
		defer p.releaseBatchSlot()
		return p.completeBatch(ctx, tenantID, actorID, input)
	}
	batches := splitCompletionInput(input, batchColumns)
	slog.InfoContext(ctx, "metadata AI completion split into batches",
		"table_id", input.Table.ID,
		"column_count", len(input.Columns),
		"batch_count", len(batches),
		"batch_columns", batchColumns,
		"batch_concurrency", p.batchConcurrency,
	)
	results := p.completeBatches(ctx, tenantID, actorID, batches)
	return mergeCompletionBatchResults(input, results)
}

type completionBatchResult struct {
	result ProviderResult
	err    error
}

func splitCompletionInput(input CompletionInput, batchColumns int) []CompletionInput {
	batches := make([]CompletionInput, 0, (len(input.Columns)+batchColumns-1)/batchColumns)
	contextColumns := input.ContextColumns
	if len(contextColumns) == 0 {
		contextColumns = completionColumnContexts(input.Columns)
	}
	for start := 0; start < len(input.Columns); start += batchColumns {
		end := start + batchColumns
		if end > len(input.Columns) {
			end = len(input.Columns)
		}
		batch := input
		batch.TargetTable = input.TargetTable && start == 0
		batch.ContextColumns = contextColumns
		batch.Columns = append([]Target(nil), input.Columns[start:end]...)
		batch.SampleRows = projectCompletionSamples(input.SampleRows, batch.Columns)
		batches = append(batches, batch)
	}
	return batches
}

func projectCompletionSamples(
	rows []map[string]any,
	columns []Target,
) []map[string]any {
	if len(rows) == 0 {
		return nil
	}
	projected := make([]map[string]any, len(rows))
	for rowIndex, row := range rows {
		values := make(map[string]any, len(columns))
		for _, column := range columns {
			if value, exists := row[column.Name]; exists {
				values[column.Name] = value
			}
		}
		projected[rowIndex] = values
	}
	return projected
}

func (p *OrchestratedProvider) completeBatches(
	ctx context.Context,
	tenantID, actorID string,
	batches []CompletionInput,
) []completionBatchResult {
	results := make([]completionBatchResult, len(batches))
	indexes := make(chan int)
	workerCount := p.batchConcurrency
	if workerCount <= 0 {
		workerCount = metadataCompletionBatchConcurrency
	}
	if workerCount > len(batches) {
		workerCount = len(batches)
	}
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer workers.Done()
			for index := range indexes {
				if err := p.acquireBatchSlot(ctx); err != nil {
					results[index].err = err
					continue
				}
				results[index].result, results[index].err = p.completeBatch(
					ctx, tenantID, actorID, batches[index],
				)
				p.releaseBatchSlot()
			}
		}()
	}
	for index := range batches {
		select {
		case indexes <- index:
		case <-ctx.Done():
			close(indexes)
			workers.Wait()
			for remaining := index; remaining < len(results); remaining++ {
				if results[remaining].err == nil {
					results[remaining].err = ctx.Err()
				}
			}
			return results
		}
	}
	close(indexes)
	workers.Wait()
	return results
}

func (p *OrchestratedProvider) acquireBatchSlot(ctx context.Context) error {
	if p.batchSemaphore == nil {
		p.batchSemaphore = make(chan struct{}, metadataCompletionBatchConcurrency)
	}
	select {
	case p.batchSemaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *OrchestratedProvider) releaseBatchSlot() {
	<-p.batchSemaphore
}

func mergeCompletionBatchResults(
	input CompletionInput,
	batches []completionBatchResult,
) (ProviderResult, error) {
	merged := ProviderResult{
		Output: CompletionOutput{
			SchemaVersion: SchemaVersion,
			Columns:       []SuggestionValue{},
		},
	}
	columns := make(map[string]SuggestionValue, len(input.Columns))
	var firstErr, fatalErr error
	for _, batch := range batches {
		result := batch.result
		merged.Model = mergeProviderModels(merged.Model, result.Model)
		if merged.ModelVersion == "" {
			merged.ModelVersion = result.ModelVersion
		}
		merged.Usage.PromptTokens += result.Usage.PromptTokens
		merged.Usage.CompletionTokens += result.Usage.CompletionTokens
		merged.Usage.TotalTokens += result.Usage.TotalTokens
		if result.Output.Table != nil {
			if merged.Output.Table != nil {
				return ProviderResult{}, fmt.Errorf(
					"%w: batched output contains duplicate table target",
					ErrInvalidOutput,
				)
			}
			table := *result.Output.Table
			merged.Output.Table = &table
		}
		for _, column := range result.Output.Columns {
			if _, duplicate := columns[column.TargetID]; duplicate {
				return ProviderResult{}, fmt.Errorf(
					"%w: batched output contains duplicate column target",
					ErrInvalidOutput,
				)
			}
			columns[column.TargetID] = column
		}
		if batch.err != nil && firstErr == nil {
			firstErr = batch.err
		}
		if batch.err != nil && batchCompletionFatal(batch.err) && fatalErr == nil {
			fatalErr = batch.err
		}
	}
	if fatalErr != nil {
		return merged, fatalErr
	}
	missing := 0
	if input.TargetTable && merged.Output.Table == nil {
		missing++
	}
	for _, target := range input.Columns {
		column, exists := columns[target.ID]
		if !exists {
			missing++
			continue
		}
		merged.Output.Columns = append(merged.Output.Columns, column)
		delete(columns, target.ID)
	}
	if len(columns) != 0 {
		return ProviderResult{}, fmt.Errorf(
			"%w: batched output contains an unknown column target",
			ErrInvalidOutput,
		)
	}
	if missing == 0 {
		if err := ValidateOutput(input, merged.Output); err != nil {
			return ProviderResult{}, err
		}
		return merged, nil
	}
	completed := len(merged.Output.Columns) + boolCount(merged.Output.Table != nil)
	if completed > 0 {
		partial, err := markBatchedPartialOutput(input, merged.Output)
		if err != nil {
			return ProviderResult{}, err
		}
		merged.Output = partial
		return merged, &PartialOutputError{MissingTargets: missing}
	}
	if firstErr != nil {
		return merged, firstErr
	}
	return merged, ErrInvalidOutput
}

func mergeProviderModels(current, next string) string {
	models := make([]string, 0, 2)
	seen := make(map[string]struct{})
	for _, value := range []string{current, next} {
		for _, candidate := range strings.Split(value, ",") {
			model := strings.TrimSpace(candidate)
			if model == "" {
				continue
			}
			key := strings.ToLower(model)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			models = append(models, model)
		}
	}
	return strings.Join(models, ",")
}

func markBatchedPartialOutput(
	input CompletionInput,
	output CompletionOutput,
) (CompletionOutput, error) {
	partial := CompletionOutput{
		SchemaVersion: SchemaVersion,
		Columns:       []SuggestionValue{},
	}
	if output.Table != nil {
		value, _, accepted := mergePartialSuggestion(
			input, input.Table, *output.Table, false,
		)
		if !accepted {
			return CompletionOutput{}, ErrInvalidOutput
		}
		partial.Table = &value
	}
	targets := make(map[string]Target, len(input.Columns))
	for _, target := range input.Columns {
		targets[target.ID] = target
	}
	for _, candidate := range output.Columns {
		target, exists := targets[candidate.TargetID]
		if !exists {
			return CompletionOutput{}, ErrInvalidOutput
		}
		value, _, accepted := mergePartialSuggestion(
			input, target, candidate, true,
		)
		if !accepted {
			return CompletionOutput{}, ErrInvalidOutput
		}
		partial.Columns = append(partial.Columns, value)
	}
	return partial, nil
}

func batchCompletionFatal(err error) bool {
	return errors.Is(err, aiplatform.ErrTenantAIForbidden) ||
		errors.Is(err, aiplatform.ErrQuotaExceeded) ||
		errors.Is(err, ErrProviderUnavailable)
}

func (p *OrchestratedProvider) completeBatch(
	ctx context.Context,
	tenantID, actorID string,
	input CompletionInput,
) (ProviderResult, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return ProviderResult{}, err
	}
	schemaJSON, err := json.Marshal(outputSchema(input))
	if err != nil {
		return ProviderResult{}, err
	}
	temperature := 0.0
	invocation := aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID, Purpose: aiplatform.PurposeMetadataCompletion,
		PromptVersion: PromptVersion, ResourceType: "METADATA_TABLE", ResourceID: input.Table.ID,
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{Role: aiplatform.MessageRoleSystem, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: metadataCompletionSystemPrompt}}},
				{Role: aiplatform.MessageRoleUser, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: string(inputJSON)}}},
			},
			ResponseSchema: aiplatform.JSONSchema{Name: "metadata_completion", Description: "企业数据资产元数据结构化补全", Schema: schemaJSON},
			Temperature:    &temperature, MaxOutputTokens: 4096,
		},
	}
	configuredModels := p.invoker.Model()
	hasFallback := configuredFallbackModel(configuredModels, "") != ""
	primaryCtx := ctx
	primaryCancel := func() {}
	if hasFallback && p.primaryFailover > 0 {
		primaryCtx, primaryCancel = context.WithTimeout(ctx, p.primaryFailover)
	}
	result, err := p.invoker.Invoke(primaryCtx, invocation)
	primaryCancel()
	fallbackModel := configuredFallbackModel(
		configuredModels,
		result.ProviderResult.Model,
	)
	var validationErr error
	previousUsage := Usage{}
	var invalidContent json.RawMessage
	if err != nil {
		validationErr = translateOrchestrationError(err)
		if !errors.Is(validationErr, ErrInvalidOutput) &&
			(fallbackModel == "" || !metadataFallbackEligible(ctx, err)) {
			return ProviderResult{}, validationErr
		}
		if content, diagnostic, ok := aiplatform.InvalidOutputDetails(err); ok {
			invalidContent = content
			if diagnostic != "" {
				validationErr = fmt.Errorf("%w: %s", ErrInvalidOutput, diagnostic)
			}
			slog.WarnContext(ctx, "metadata AI structured output requires repair",
				"table_id", input.Table.ID, "output_diagnostic", diagnostic)
		}
	} else {
		output, outputErr := decodeAndValidateCompletion(input, result.ProviderResult.Content)
		if outputErr == nil {
			return completionProviderResult(result, output, Usage{}), nil
		}
		validationErr = outputErr
		invalidContent = result.ProviderResult.Content
		previousUsage = invocationUsage(result)
		slog.WarnContext(ctx, "metadata AI domain output requires repair",
			"table_id", input.Table.ID, "output_diagnostic", outputErr)
	}
	if err := ctx.Err(); err != nil {
		return ProviderResult{}, err
	}

	// JSON Schema 无法表达 targetId “各出现且只出现一次”等跨数组约束。
	// 首次非法输出时，将可用的模型原输出和安全校验原因交给后备模型；
	// 未配置后备模型时仍由原模型仅纠错重试一次。
	repairInvocation := invocation
	if fallbackModel != "" {
		repairInvocation.PreferredModel = fallbackModel
	}
	if errors.Is(validationErr, ErrInvalidOutput) {
		repairMessages := append(
			[]aiplatform.Message(nil),
			invocation.Request.Messages...,
		)
		if len(invalidContent) > 0 {
			repairMessages = append(repairMessages, aiplatform.Message{Role: aiplatform.MessageRoleAssistant, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: string(invalidContent)}}})
		}
		repairMessages = append(repairMessages, aiplatform.Message{Role: aiplatform.MessageRoleUser, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: repairInstruction(input, validationErr)}}})
		repairInvocation.Request.Messages = repairMessages
	}
	repairResult, err := p.invoker.Invoke(ctx, repairInvocation)
	if err != nil {
		translated := translateOrchestrationError(err)
		repairContent := json.RawMessage(nil)
		if content, _, ok := aiplatform.InvalidOutputDetails(err); ok {
			repairContent = content
		}
		for _, candidate := range []struct {
			result  aiplatform.InvocationResult
			content json.RawMessage
		}{
			{result: repairResult, content: repairContent},
			{result: result, content: invalidContent},
		} {
			if partial, missing, ok := decodePartialCompletion(input, candidate.content); ok {
				partialResult := completionProviderResult(
					candidate.result, partial, previousUsage,
					result.ProviderResult.Model,
				)
				if missing == 0 {
					return partialResult, nil
				}
				return partialResult, &PartialOutputError{MissingTargets: missing}
			}
		}
		return ProviderResult{}, translated
	}
	repairedOutput, repairErr := decodeAndValidateCompletion(input, repairResult.ProviderResult.Content)
	if repairErr != nil {
		if partial, missing, ok := decodePartialCompletion(
			input, repairResult.ProviderResult.Content,
		); ok {
			partialResult := completionProviderResult(
				repairResult, partial, previousUsage,
				result.ProviderResult.Model,
			)
			if missing == 0 {
				return partialResult, nil
			}
			return partialResult, &PartialOutputError{MissingTargets: missing}
		}
		if partial, missing, ok := decodePartialCompletion(input, invalidContent); ok {
			partialResult := completionProviderResult(
				result, partial, previousUsage,
			)
			if missing == 0 {
				return partialResult, nil
			}
			return partialResult, &PartialOutputError{MissingTargets: missing}
		}
		return ProviderResult{}, fmt.Errorf("%w: 纠错重试仍未通过: %v", ErrInvalidOutput, repairErr)
	}
	return completionProviderResult(
		repairResult,
		repairedOutput,
		previousUsage,
		result.ProviderResult.Model,
	), nil
}

func configuredFallbackModel(models, current string) string {
	parts := strings.Split(models, ",")
	if len(parts) < 2 {
		return ""
	}
	current = strings.TrimSpace(current)
	currentIndex := 0
	if current != "" {
		currentIndex = -1
		for index, model := range parts {
			if strings.EqualFold(strings.TrimSpace(model), current) {
				currentIndex = index
				break
			}
		}
		if currentIndex < 0 {
			return ""
		}
	}
	currentModel := strings.TrimSpace(parts[currentIndex])
	for offset := 1; offset < len(parts); offset++ {
		candidate := strings.TrimSpace(parts[(currentIndex+offset)%len(parts)])
		if candidate != "" && !strings.EqualFold(candidate, currentModel) {
			return candidate
		}
	}
	return ""
}

func metadataFallbackEligible(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if errors.Is(err, aiplatform.ErrTenantAIForbidden) ||
		errors.Is(err, aiplatform.ErrQuotaExceeded) {
		return false
	}
	var providerErr *aiplatform.ProviderError
	if !errors.As(err, &providerErr) {
		return errors.Is(err, context.DeadlineExceeded)
	}
	switch providerErr.Code {
	case aiplatform.ErrorCodeCanceled,
		aiplatform.ErrorCodeInvalidRequest,
		aiplatform.ErrorCodeAuthentication:
		return false
	default:
		return true
	}
}

// decodeAndValidateCompletion 在 Provider JSON Schema 校验后继续执行领域级一一映射校验。
func decodeAndValidateCompletion(input CompletionInput, content json.RawMessage) (CompletionOutput, error) {
	var output CompletionOutput
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return CompletionOutput{}, fmt.Errorf("%w: 解码元数据结构化输出失败", ErrInvalidOutput)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return CompletionOutput{}, fmt.Errorf("%w: %v", ErrInvalidOutput, err)
	}
	output = normalizeOutputForInput(input, output)
	if err := ValidateOutput(input, output); err != nil {
		return CompletionOutput{}, err
	}
	return output, nil
}

// decodePartialCompletion 只保留能够独立通过可信边界校验的表/字段建议。重复
// targetId 会整体丢弃该目标，避免在冲突候选中擅自选择；未知目标永不落库。
func decodePartialCompletion(
	input CompletionInput,
	content json.RawMessage,
) (CompletionOutput, int, bool) {
	if len(content) == 0 {
		return CompletionOutput{}, 0, false
	}
	var candidate CompletionOutput
	if err := json.Unmarshal(content, &candidate); err != nil ||
		candidate.SchemaVersion != SchemaVersion {
		return CompletionOutput{}, 0, false
	}
	candidate = normalizeOutputForInput(input, candidate)
	output := CompletionOutput{
		SchemaVersion: SchemaVersion,
		Columns:       []SuggestionValue{},
	}
	missing := len(input.Columns)
	if input.TargetTable {
		missing++
		if candidate.Table != nil && candidate.Table.TargetID == input.Table.ID {
			value, complete, accepted := mergePartialSuggestion(
				input, input.Table, *candidate.Table, false,
			)
			if accepted {
				output.Table = &value
				if complete {
					missing--
				}
			}
		}
	}
	expected := make(map[string]Target, len(input.Columns))
	for _, target := range input.Columns {
		expected[target.ID] = target
	}
	candidates := make(map[string]SuggestionValue, len(candidate.Columns))
	duplicates := make(map[string]bool)
	for _, value := range candidate.Columns {
		if _, exists := expected[value.TargetID]; !exists {
			continue
		}
		if _, seen := candidates[value.TargetID]; seen {
			delete(candidates, value.TargetID)
			duplicates[value.TargetID] = true
			continue
		}
		if duplicates[value.TargetID] {
			continue
		}
		candidates[value.TargetID] = value
	}
	for _, target := range input.Columns {
		candidateValue, exists := candidates[target.ID]
		if !exists {
			continue
		}
		value, complete, accepted := mergePartialSuggestion(
			input, target, candidateValue, true,
		)
		if !accepted {
			continue
		}
		output.Columns = append(output.Columns, value)
		if complete {
			missing--
		}
	}
	if missing == 0 {
		return output, 0, true
	}
	applied := len(output.Columns)
	if output.Table != nil {
		applied++
	}
	return output, missing, applied > 0
}

// mergePartialSuggestion 逐属性校验模型候选并与 PostgreSQL 中的当前值合并。
// 不可信属性会被忽略，已经确认的属性不会被空值或非法枚举覆盖。
func mergePartialSuggestion(
	input CompletionInput,
	target Target,
	candidate SuggestionValue,
	column bool,
) (SuggestionValue, bool, bool) {
	if candidate.TargetID != target.ID ||
		candidate.Confidence <= 0 || candidate.Confidence > 1 {
		return SuggestionValue{}, false, false
	}
	value := suggestionFromTarget(target)
	value.TargetID = target.ID
	value.Confidence = candidate.Confidence
	provided := make([]string, 0, 5)
	if err := validateText("businessName", candidate.BusinessName, 120); err == nil {
		valid := true
		if column && isFileSourceFormat(input.SourceFormat) {
			valid = containsChinese(candidate.BusinessName)
		}
		if !column && isFileSourceFormat(input.SourceFormat) {
			valid = containsChinese(candidate.BusinessName)
		}
		if valid {
			value.BusinessName = candidate.BusinessName
			provided = append(provided, "businessName")
		}
	}
	if err := validateText(
		"businessDescription", candidate.BusinessDescription, 1000,
	); err == nil && (!column || !isFileSourceFormat(input.SourceFormat) ||
		containsChinese(candidate.BusinessDescription)) {
		value.BusinessDescription = candidate.BusinessDescription
		provided = append(provided, "businessDescription")
	}
	if validControlledTags(candidate.Tags, column) {
		value.Tags = append([]string(nil), candidate.Tags...)
		provided = append(provided, "tags")
	}
	if allowedSensitivity[candidate.SensitivityLevel] {
		value.SensitivityLevel = candidate.SensitivityLevel
		provided = append(provided, "sensitivityLevel")
	}
	if column && allowedSemanticTypes[candidate.SemanticType] &&
		(strings.TrimSpace(target.CanonicalType) == "" ||
			semanticquality.Compatible(target.CanonicalType, candidate.SemanticType)) {
		value.SemanticType = candidate.SemanticType
		provided = append(provided, "semanticType")
	}
	if len(provided) == 0 {
		return SuggestionValue{}, false, false
	}
	value.ProvidedFields = provided
	value.Complete = len(completionMissingFields(input, target, value, column)) == 0
	return value, value.Complete, true
}

func suggestionFromTarget(target Target) SuggestionValue {
	return SuggestionValue{
		TargetID:            target.ID,
		BusinessName:        strings.TrimSpace(target.CurrentBusinessName),
		BusinessDescription: strings.TrimSpace(target.CurrentDescription),
		Tags:                append([]string{}, target.CurrentTags...),
		SensitivityLevel:    strings.TrimSpace(target.CurrentSensitivity),
		SemanticType:        strings.TrimSpace(target.CurrentSemanticType),
	}
}

func completionMissingFields(
	input CompletionInput,
	target Target,
	value SuggestionValue,
	column bool,
) []string {
	missing := make([]string, 0, 5)
	if err := validateText("businessName", value.BusinessName, 120); err != nil ||
		(column && isFileSourceFormat(input.SourceFormat) &&
			!containsChinese(value.BusinessName)) ||
		(!column && isFileSourceFormat(input.SourceFormat) &&
			!containsChinese(value.BusinessName)) {
		missing = append(missing, "businessName")
	}
	if err := validateText(
		"businessDescription", value.BusinessDescription, 1000,
	); err != nil || (column && isFileSourceFormat(input.SourceFormat) &&
		!containsChinese(value.BusinessDescription)) {
		missing = append(missing, "businessDescription")
	}
	if !validControlledTags(value.Tags, column) {
		missing = append(missing, "tags")
	}
	if !allowedSensitivity[value.SensitivityLevel] {
		missing = append(missing, "sensitivityLevel")
	}
	if column && (!allowedSemanticTypes[value.SemanticType] ||
		(strings.TrimSpace(target.CanonicalType) != "" &&
			!semanticquality.Compatible(target.CanonicalType, value.SemanticType))) {
		missing = append(missing, "semanticType")
	}
	return missing
}

// validControlledTags 判断当前标签是否已经完整：受控词表、无重复，且表资产恰好
// 携带一个层级标签、字段资产不携带。缺少层级标签的历史表会因此把 tags 重新列为
// 待补全属性，让下一次清洗补齐层级判断。
func validControlledTags(tags []string, column bool) bool {
	if len(tags) == 0 || len(tags) > maxControlledTagsPerTarget {
		return false
	}
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		if !allowedTags[tag] || seen[tag] {
			return false
		}
		seen[tag] = true
	}
	return warehouselayer.Validate(tags, column, true) == nil
}

func validateSuggestionForTarget(
	input CompletionInput,
	target Target,
	value SuggestionValue,
	column bool,
) error {
	if err := validateValue(value, column, column && isFileSourceFormat(input.SourceFormat)); err != nil {
		return err
	}
	if !column {
		if isFileSourceFormat(input.SourceFormat) &&
			!containsChinese(value.BusinessName) {
			return errors.New("file table business name must contain Chinese text")
		}
		return nil
	}
	if strings.TrimSpace(target.CanonicalType) != "" &&
		!semanticquality.Compatible(target.CanonicalType, value.SemanticType) {
		return errors.New("semantic type is incompatible with canonical type")
	}
	return nil
}

// repairInstruction 明确列出本次允许的稳定 ID，避免模型再次遗漏或复用目标。
func repairInstruction(input CompletionInput, validationErr error) string {
	columnIDs := make([]string, 0, len(input.Columns))
	missingByTarget := make(map[string][]string, len(input.Columns)+1)
	for _, column := range input.Columns {
		columnIDs = append(columnIDs, column.ID)
		missingByTarget[column.ID] = column.MissingFields
	}
	encodedColumnIDs, _ := json.Marshal(columnIDs)
	if input.TargetTable {
		missingByTarget[input.Table.ID] = input.Table.MissingFields
	}
	encodedMissing, _ := json.Marshal(missingByTarget)
	tableRule := "不得返回 table"
	if input.TargetTable {
		tableRule = fmt.Sprintf("table.targetId 必须等于 %q", input.Table.ID)
	}
	return fmt.Sprintf("上一次输出未通过本地可信边界校验：%v。请先按目标检查仍缺属性，只修复 missingFields，并原样保留其他 current 值，然后重新生成完整 JSON，不要解释。各目标仍缺属性：%s。columns.targetId 必须与以下数组一一对应，保持数量一致并且每个 ID 恰好出现一次：%s；%s。", validationErr, encodedMissing, encodedColumnIDs, tableRule)
}

// completionProviderResult 合并纠错前后的令牌用量，确保元数据任务审计覆盖真实消耗。
func completionProviderResult(
	result aiplatform.InvocationResult,
	output CompletionOutput,
	previous Usage,
	previousModels ...string,
) ProviderResult {
	usage := invocationUsage(result)
	usage.PromptTokens += previous.PromptTokens
	usage.CompletionTokens += previous.CompletionTokens
	usage.TotalTokens += previous.TotalTokens
	model := result.ProviderResult.Model
	for _, previousModel := range previousModels {
		model = mergeProviderModels(previousModel, model)
	}
	return ProviderResult{Output: output, Model: model, Usage: usage}
}

func invocationUsage(result aiplatform.InvocationResult) Usage {
	return Usage{
		PromptTokens:     result.ProviderResult.Usage.PromptTokens,
		CompletionTokens: result.ProviderResult.Usage.CompletionTokens,
		TotalTokens:      result.ProviderResult.Usage.TotalTokens,
	}
}

// translateOrchestrationError 保持元数据 API 已发布的超时和非法输出错误合同。
func translateOrchestrationError(err error) error {
	var providerErr *aiplatform.ProviderError
	if !errors.As(err, &providerErr) {
		return err
	}
	switch providerErr.Code {
	case aiplatform.ErrorCodeTimeout:
		return errors.Join(context.DeadlineExceeded, err)
	case aiplatform.ErrorCodeInvalidOutput:
		return errors.Join(ErrInvalidOutput, err)
	default:
		return err
	}
}

// ensureJSONEOF 确保结构化输出后不存在第二个 JSON 值。
func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("结构化输出包含尾随 JSON")
		}
		return fmt.Errorf("读取结构化输出尾部失败: %w", err)
	}
	return nil
}

// firstNonBlank 返回第一个非空白字符串。
func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
