package dataset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

const (
	dwdModelingPromptVersion       = "warehouse-modeling-v9"
	dwdClassificationPromptVersion = "warehouse-classification-v1"
	dwdFactDesignPromptVersion     = "warehouse-fact-design-v1"
	maxDWDModelingRepairContent    = 64 << 10
	dwdModelingInvocationAttempts  = 3
	dwdStageInvocationAttempts     = 2
	maxDWDValidationIssues         = 64
)

type DWDModelingPlanner interface {
	Configured() bool
	Plan(context.Context, dwdPlanningInput) (dwdPlanningCompletion, error)
}

// resumableDWDModelingPlanner is implemented by planners that can persist the
// classification and each FACT design independently. The public Plan method is
// retained for compatibility and direct validation tests; the durable worker
// prefers this staged contract so an interruption never discards prior stages.
type resumableDWDModelingPlanner interface {
	DWDModelingPlanner
	Classify(context.Context, dwdPlanningInput) (dwdClassificationCompletion, error)
	DesignFact(
		context.Context,
		dwdPlanningInput,
		[]dwdLLMClassification,
		string,
	) (dwdFactDesignCompletion, error)
}

type dwdPlanningInput struct {
	TenantID   string             `json:"-"`
	ActorID    string             `json:"-"`
	ResourceID string             `json:"-"`
	Domain     string             `json:"domain"`
	Trigger    dwdPlanningTrigger `json:"trigger"`
	Tables     []dwdPlanningTable `json:"tables"`
}

type dwdPlanningTrigger struct {
	DatasetID string `json:"datasetId"`
	VersionID string `json:"versionId"`
}

type dwdPlanningTable struct {
	DatasetID   string             `json:"datasetId"`
	VersionID   string             `json:"versionId"`
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Tags        []string           `json:"tags"`
	OutputGrain OutputGrain        `json:"outputGrain"`
	Fields      []dwdPlanningField `json:"fields"`
}

type dwdPlanningField struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Role          string `json:"role"`
	CanonicalType string `json:"canonicalType"`
	SemanticType  string `json:"semanticType"`
	Nullable      bool   `json:"nullable"`
}

type dwdPlanningCompletion struct {
	AIRequestID           string
	Plan                  dwdLLMPlan
	CheckpointCount       int
	ReusedCheckpointCount int
}

type dwdClassificationCompletion struct {
	AIRequestID     string
	Domain          string
	Classifications []dwdLLMClassification
}

type dwdFactDesignCompletion struct {
	AIRequestID string
	Output      dwdLLMOutput
}

type dwdLLMPlan struct {
	Domain          string                 `json:"domain"`
	Classifications []dwdLLMClassification `json:"classifications"`
	Outputs         []dwdLLMOutput         `json:"outputs"`
}

type dwdLLMClassification struct {
	DatasetVersionID string `json:"datasetVersionId"`
	Role             string `json:"role"`
	Rationale        string `json:"rationale"`
}

type dwdLLMOutput struct {
	FactDatasetVersionID string        `json:"factDatasetVersionId"`
	Name                 string        `json:"name"`
	Description          string        `json:"description"`
	Joins                []dwdLLMJoin  `json:"joins"`
	Fields               []dwdLLMField `json:"fields"`
	GrainKeyOutputCodes  []string      `json:"grainKeyOutputCodes"`
	TimeOutputCode       string        `json:"timeOutputCode"`
	Rationale            string        `json:"rationale"`
}

type dwdLLMJoin struct {
	DimensionDatasetVersionID string                `json:"dimensionDatasetVersionId"`
	Conditions                []dwdLLMJoinCondition `json:"conditions"`
	JoinType                  string                `json:"joinType"`
	Rationale                 string                `json:"rationale"`
}

type dwdLLMJoinCondition struct {
	FactFieldCode      string `json:"factFieldCode"`
	DimensionFieldCode string `json:"dimensionFieldCode"`
}

type dwdLLMField struct {
	SourceDatasetVersionID string                 `json:"sourceDatasetVersionId"`
	SourceFieldCode        string                 `json:"sourceFieldCode"`
	OutputCode             string                 `json:"outputCode"`
	OutputName             string                 `json:"outputName"`
	OutputDescription      string                 `json:"outputDescription"`
	Role                   string                 `json:"role"`
	Cleaning               []string               `json:"cleaning"`
	Processing             []dwdLLMProcessingStep `json:"processing"`
}

type dwdLLMProcessingStep struct {
	Operation                       string   `json:"operation"`
	Arguments                       []string `json:"arguments"`
	SecondarySourceDatasetVersionID string   `json:"secondarySourceDatasetVersionId"`
	SecondarySourceFieldCode        string   `json:"secondarySourceFieldCode"`
	Unit                            string   `json:"-"`
	TargetType                      string   `json:"-"`
	FallbackValue                   string   `json:"-"`
	Precision                       int      `json:"-"`
	Start                           int      `json:"-"`
	Length                          int      `json:"-"`
	SearchValue                     string   `json:"-"`
	ReplacementValue                string   `json:"-"`
	MatchValue                      string   `json:"-"`
	ThenValue                       string   `json:"-"`
	ElseValue                       string   `json:"-"`
	ConditionOperator               string   `json:"-"`
	Separator                       string   `json:"-"`
}

type dwdAIInvoker interface {
	Configured() bool
	Invoke(context.Context, aiplatform.Invocation) (aiplatform.InvocationResult, error)
}

type OrchestratedDWDModelingPlanner struct {
	invoker dwdAIInvoker
	timeout time.Duration
}

var _ resumableDWDModelingPlanner = (*OrchestratedDWDModelingPlanner)(nil)

func NewOrchestratedDWDModelingPlanner(
	invoker dwdAIInvoker,
	timeout time.Duration,
) *OrchestratedDWDModelingPlanner {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &OrchestratedDWDModelingPlanner{invoker: invoker, timeout: timeout}
}

func (planner *OrchestratedDWDModelingPlanner) Configured() bool {
	return planner != nil && planner.invoker != nil && planner.invoker.Configured()
}

const dwdClassificationSystemPrompt = `你是企业数据仓库 ODS 角色分类器。输入只包含同一业务领域内已发布 ODS 数据集的元数据，不包含业务数据行。

逐表判断为 FACT、DIMENSION、MASTER 或 OTHER：
- FACT 是“谁在何时对什么做了什么”的事件或交易明细；
- DIMENSION 是人物、商品、组织、区域等用于解释事实的分析实体；
- MASTER 是稳定的核心实体主数据；
- 证据不足时使用 OTHER。

classifications 必须逐一覆盖输入表且不得重复，只能复制精确 datasetVersionId。理由应简短、基于表名、说明、标签、字段角色和粒度。不要设计字段、关联、SQL 或物理表。输出只能是 JSON Schema 指定的对象。`

const dwdFactDesignSystemPrompt = `你是企业数据仓库 DWD 明细结构与 DAG 设计师。ODS 角色分类已经通过校验且不可更改；本次只设计指定的一张 FACT，输入只包含该事实表和同领域 DIMENSION/MASTER 元数据，不包含业务数据行。

1. 生成且只生成指定 FACT 的一张 DWD，保持业务事实粒度，不得分组或聚合。
2. 保留事实表全部字段。逐一检查标识/维度字段和以 _id/_key 结尾的字段；在 DIMENSION/MASTER 中存在唯一同名、类型兼容的键时必须 LEFT JOIN。复合业务键必须完整放入同一 join.conditions。
3. 每个已关联维度至少扩充一个关联键之外的名称、分类、区域、状态等描述字段（只要存在）；维度侧关联键只用于 Join，不重复输出。
4. 基础 cleaning：字符串维度/标识/属性去首尾空格；可空字符串用 UNKNOWN、可空数值用 -1；时间显式转换，字符串时间先 TRIM 再 CAST；度量和时间不得擅自补默认值。
5. 真实需要时可使用 DATE_FORMAT、DATE_TRUNC、CAST、TRIM、UPPER、LOWER、REPLACE、SUBSTRING、CONCAT、COALESCE、ADD、SUBTRACT、MULTIPLY、DIVIDE、ROUND、ABS、FLOOR、CEIL、CASE。arguments 的每项都是字符串，二元处理只能引用已经加入输出的事实或维度字段。
6. 只能复制输入中的精确 dataset version id 和字段 code，不得返回 SQL、DDL、表达式文本、物理表或调度命令。输出是交给受控 DAG 开发引擎的待审阅结构化设计。

输出只能是 JSON Schema 指定的对象。`

type dwdLLMClassificationPlan struct {
	Domain          string                 `json:"domain"`
	Classifications []dwdLLMClassification `json:"classifications"`
}

type dwdLLMFactDesign struct {
	Output dwdLLMOutput `json:"output"`
}

func (planner *OrchestratedDWDModelingPlanner) Classify(
	ctx context.Context,
	input dwdPlanningInput,
) (dwdClassificationCompletion, error) {
	if !planner.Configured() || !validDWDPlanningInput(input) {
		return dwdClassificationCompletion{}, errDWDModelingInvalid
	}
	raw, err := json.Marshal(struct {
		Domain  string             `json:"domain"`
		Trigger dwdPlanningTrigger `json:"trigger"`
		Tables  []dwdPlanningTable `json:"tables"`
	}{Domain: input.Domain, Trigger: input.Trigger, Tables: input.Tables})
	if err != nil {
		return dwdClassificationCompletion{}, err
	}
	if len(raw) > 256<<10 {
		return dwdClassificationCompletion{}, fmt.Errorf(
			"%w: classification context exceeds 256 KiB", errDWDModelingInvalid,
		)
	}
	schema, err := dwdClassificationResponseSchema(input)
	if err != nil {
		return dwdClassificationCompletion{}, err
	}
	temperature := 0.0
	request := aiplatform.ProviderRequest{
		Messages: []aiplatform.Message{
			{
				Role: aiplatform.MessageRoleSystem,
				Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText, Text: dwdClassificationSystemPrompt,
				}},
			},
			{
				Role: aiplatform.MessageRoleUser,
				Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText, Text: string(raw),
				}},
			},
		},
		ResponseSchema:  schema,
		Temperature:     &temperature,
		MaxOutputTokens: 3000,
	}
	callCtx, cancel := context.WithTimeout(
		ctx, planner.timeout*dwdStageInvocationAttempts,
	)
	defer cancel()
	invocation := aiplatform.Invocation{
		TenantID: input.TenantID, ActorID: input.ActorID,
		Purpose:       aiplatform.PurposeDatasetDAGGeneration,
		PromptVersion: dwdClassificationPromptVersion,
		ResourceType:  "DATASET_VERSION", ResourceID: input.ResourceID,
		Request: request,
	}
	baseMessages := append([]aiplatform.Message(nil), request.Messages...)
	for attempt := 0; attempt < dwdStageInvocationAttempts; attempt++ {
		result, invokeErr := planner.invoker.Invoke(callCtx, invocation)
		if invokeErr == nil {
			candidate, decodeErr := decodeDWDClassificationPlan(
				result.ProviderResult.Content,
			)
			if decodeErr == nil {
				candidate.Classifications = normalizeDWDClassifications(
					input, candidate.Classifications,
				)
				decodeErr = validateDWDLLMClassifications(
					input, candidate.Domain, candidate.Classifications,
				)
			}
			if decodeErr == nil {
				return dwdClassificationCompletion{
					AIRequestID:     result.RequestID,
					Domain:          candidate.Domain,
					Classifications: candidate.Classifications,
				}, nil
			}
			invokeErr = decodeErr
		}
		if !repairableDWDModelingError(invokeErr) ||
			attempt == dwdStageInvocationAttempts-1 {
			return dwdClassificationCompletion{}, invokeErr
		}
		if err := callCtx.Err(); err != nil {
			return dwdClassificationCompletion{}, err
		}
		invocation.Request.Messages = dwdStageRepairMessages(
			baseMessages, result, invokeErr,
			`请只修复 ODS 角色分类：domain 必须等于输入；classifications 必须逐表覆盖且不重复，只能使用输入的精确 datasetVersionId；role 只能是 FACT、DIMENSION、MASTER、OTHER；rationale 保持简短。不要返回 outputs、SQL、Markdown 或解释。`,
		)
	}
	return dwdClassificationCompletion{}, errDWDModelingInvalid
}

func (planner *OrchestratedDWDModelingPlanner) DesignFact(
	ctx context.Context,
	input dwdPlanningInput,
	classifications []dwdLLMClassification,
	factVersionID string,
) (dwdFactDesignCompletion, error) {
	if !planner.Configured() || !validDWDPlanningInput(input) {
		return dwdFactDesignCompletion{}, errDWDModelingInvalid
	}
	scoped, scopedClassifications, err := dwdFactPlanningScope(
		input, classifications, factVersionID,
	)
	if err != nil {
		return dwdFactDesignCompletion{}, err
	}
	raw, err := json.Marshal(struct {
		Domain               string                 `json:"domain"`
		FactDatasetVersionID string                 `json:"factDatasetVersionId"`
		Classifications      []dwdLLMClassification `json:"classifications"`
		Tables               []dwdPlanningTable     `json:"tables"`
	}{
		Domain: scoped.Domain, FactDatasetVersionID: factVersionID,
		Classifications: scopedClassifications, Tables: scoped.Tables,
	})
	if err != nil {
		return dwdFactDesignCompletion{}, err
	}
	if len(raw) > 256<<10 {
		return dwdFactDesignCompletion{}, fmt.Errorf(
			"%w: fact design context exceeds 256 KiB", errDWDModelingInvalid,
		)
	}
	schema, err := dwdFactDesignResponseSchema(scoped, factVersionID)
	if err != nil {
		return dwdFactDesignCompletion{}, err
	}
	temperature := 0.0
	request := aiplatform.ProviderRequest{
		Messages: []aiplatform.Message{
			{
				Role: aiplatform.MessageRoleSystem,
				Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText, Text: dwdFactDesignSystemPrompt,
				}},
			},
			{
				Role: aiplatform.MessageRoleUser,
				Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText, Text: string(raw),
				}},
			},
		},
		ResponseSchema:  schema,
		Temperature:     &temperature,
		MaxOutputTokens: 8000,
	}
	callCtx, cancel := context.WithTimeout(
		ctx, planner.timeout*dwdStageInvocationAttempts,
	)
	defer cancel()
	invocation := aiplatform.Invocation{
		TenantID: input.TenantID, ActorID: input.ActorID,
		Purpose:       aiplatform.PurposeDatasetDAGGeneration,
		PromptVersion: dwdFactDesignPromptVersion,
		ResourceType:  "DATASET_VERSION", ResourceID: factVersionID,
		Request: request,
	}
	baseMessages := append([]aiplatform.Message(nil), request.Messages...)
	for attempt := 0; attempt < dwdStageInvocationAttempts; attempt++ {
		result, invokeErr := planner.invoker.Invoke(callCtx, invocation)
		if invokeErr == nil {
			candidate, decodeErr := decodeDWDFactDesign(
				result.ProviderResult.Content,
			)
			if decodeErr == nil {
				plan := dwdLLMPlan{
					Domain:          scoped.Domain,
					Classifications: scopedClassifications,
					Outputs:         []dwdLLMOutput{candidate.Output},
				}
				plan = normalizeDWDJoinOutputProjection(plan)
				plan = completeMandatoryDWDPolicyCleaning(scoped, plan)
				decodeErr = validateDWDLLMPlan(scoped, plan)
				if decodeErr == nil {
					candidate.Output = plan.Outputs[0]
				}
			}
			if decodeErr == nil {
				return dwdFactDesignCompletion{
					AIRequestID: result.RequestID,
					Output:      candidate.Output,
				}, nil
			}
			invokeErr = decodeErr
		}
		if !repairableDWDModelingError(invokeErr) ||
			attempt == dwdStageInvocationAttempts-1 {
			return dwdFactDesignCompletion{}, invokeErr
		}
		if err := callCtx.Err(); err != nil {
			return dwdFactDesignCompletion{}, err
		}
		invocation.Request.Messages = dwdStageRepairMessages(
			baseMessages, result, invokeErr,
			dwdFactDesignRepairInstruction(invokeErr),
		)
	}
	return dwdFactDesignCompletion{}, errDWDModelingInvalid
}

func dwdFactDesignRepairInstruction(validationErr error) string {
	return fmt.Sprintf(`上一份单 FACT DWD 方案未通过结构或本地安全校验。原因：%s

请只返回修复后的 {"output": ...}：
1. factDatasetVersionId 必须是指定 FACT；保留全部事实字段且不聚合。
2. 只能引用输入中的精确版本和字段；唯一可靠维度键必须 LEFT JOIN，复合键保持在同一个 conditions。
3. 已关联维度存在说明字段时必须扩充至少一个，维度侧关联键不得重复输出。
4. cleaning、processing、grainKeyOutputCodes 和 timeOutputCode 必须满足原始合同。
5. 名称、说明和 rationale 保持简短，不返回 classifications、SQL、Markdown 或额外解释。`,
		validationErr.Error(),
	)
}

func validDWDPlanningInput(input dwdPlanningInput) bool {
	return input.TenantID != "" && input.ActorID != "" &&
		input.Domain != "" && len(input.Tables) > 0 && len(input.Tables) <= 48
}

func dwdStageRepairMessages(
	base []aiplatform.Message,
	result aiplatform.InvocationResult,
	invokeErr error,
	instruction string,
) []aiplatform.Message {
	repairContent := result.ProviderResult.Content
	repairDiagnostic := ""
	if candidate, diagnostic, ok := aiplatform.InvalidOutputDetails(invokeErr); ok {
		repairContent = candidate
		repairDiagnostic = diagnostic
	}
	messages := append([]aiplatform.Message(nil), base...)
	if len(repairContent) > 0 && len(repairContent) <= maxDWDModelingRepairContent {
		messages = append(messages, aiplatform.Message{
			Role: aiplatform.MessageRoleAssistant,
			Parts: []aiplatform.ContentPart{{
				Type: aiplatform.ContentTypeText, Text: string(repairContent),
			}},
		})
	}
	if strings.TrimSpace(repairDiagnostic) != "" {
		instruction += "\n结构诊断：" + strings.TrimSpace(repairDiagnostic)
	}
	return append(messages, aiplatform.Message{
		Role: aiplatform.MessageRoleUser,
		Parts: []aiplatform.ContentPart{{
			Type: aiplatform.ContentTypeText, Text: instruction,
		}},
	})
}

const dwdModelingSystemPrompt = `你是企业数据仓库 DIM/DWD 建模设计师。输入只包含同一业务领域内已发布 ODS 数据集的完整元数据，不包含业务数据行。ODS 是源系统物理表的治理映射。

你的职责是完成可交给开发引擎执行的分层结构和 DAG 设计，而不是复述输入或编写 SQL：
1. 对每张 ODS 判断为 FACT、DIMENSION、MASTER 或 OTHER，并给出简短元数据依据。事实表描述“谁在何时对什么做了什么”的业务事件/交易明细；维度表描述人物、商品、组织、区域等分析维度；主数据描述稳定核心实体；证据不足使用 OTHER。平台会把每个 DIMENSION/MASTER 分类并行转换为一张保留实体粒度、关键字段和说明字段的 DIM 草稿，因此分类本身就是 DIM 设计决策。
2. 每张 FACT 必须设计且只设计一张以事实明细为中心的 DWD 输出；不得分组、聚合或改变事实粒度。
3. 必须逐一审视 FACT 中所有标识/维度字段以及 code 以 _id/_key 结尾的字段，并在全部 DIMENSION/MASTER 中按字段 code、业务名称、说明、标签和类型寻找关联键。事实字段与某个维度键 code 精确同名（忽略大小写）、类型兼容且候选唯一时，该 LEFT JOIN 是必选项，不得退化为 ODS 单表直出。关联若依赖两个或更多业务键，必须在同一个 join.conditions 中完整返回全部条件，不能拆成多个 Join，也不能只取其中一个条件。只有确实没有唯一可靠候选时才不关联，禁止仅凭字段位置猜测。
4. DWD 必须保留事实表全部字段。对事实表的标识、维度、属性和时间字段设计基础清洗：字符串去首尾空格；非时间的可空维度字符串使用 UNKNOWN 补位；非时间的可空维度数值使用 -1 补位；日期/时间显式转换为 DATE 或 DATETIME。字符串时间先 TRIM 再 CAST，不得先补 UNKNOWN；度量空值和时间空值不得擅自补默认值。基础卫生操作进入 cleaning。
5. 每个已关联的维度/主数据必须至少选择一个关联键之外的名称、分类、区域、状态等描述字段扩充到输出（只要输入存在此类字段），不能只关联而不扩维。所有维度侧关联键只用于 Join，不得再次放入 output.fields；最终结果只保留事实侧关联键，避免同一业务键输出两份。维度字符串同样去首尾空格；字段编码必须是稳定英文标识符且唯一。
6. 除基础 cleaning 外，按真实元数据需要为每个字段设计 processing，可使用现有全部字段处理能力：DATE_FORMAT、DATE_TRUNC、CAST、TRIM、UPPER、LOWER、REPLACE、SUBSTRING、CONCAT、COALESCE、ADD、SUBTRACT、MULTIPLY、DIVIDE、ROUND、ABS、FLOOR、CEIL、CASE。不需要额外处理时返回空数组。不得为了展示而滥加操作；不得聚合或改变事实粒度。
7. processing 每步使用紧凑参数，arguments 的每个元素都必须是字符串：DATE_FORMAT/DATE_TRUNC arguments=["MONTH"]；CAST=["DATE"]；REPLACE=["旧值","新值"]；SUBSTRING=["1","8"]；CONCAT=["-"]；COALESCE=["UNKNOWN"]；ROUND=["2"]；CASE=["EQUALS","A","有效","其他"]；其余操作 arguments=[]。同一处理功能、同一依赖阶段应用于多个字段时，平台会把多条规则合并进一个 DAG 组件。二元计算或拼接通过 secondarySourceDatasetVersionId/secondarySourceFieldCode 引用已加入当前输出的事实或维度字段；不用次字段时两个值均为空字符串。不得返回 SQL、自由表达式或虚构表字段。
8. classifications 必须逐一覆盖输入中的每张表；outputs 必须逐一覆盖所有被分类为 FACT 的表，不得为其他角色生成 output。DIM 与 DWD 是当前可恢复流程的第一阶段；DWD 必须保留维度键和分析属性，后续 DWS 只允许使用一个或多个已发布 DWD。ADS 仅为明确消费场景预留，不自动组合。
9. 只能使用输入给出的精确 dataset version id 和字段 code。结果是待审阅的结构化设计方案，不代表自动发布；物理表、SQL、调度和重试由底层 DAG 开发引擎负责。

输出只能是 JSON Schema 指定的对象。`

func (planner *OrchestratedDWDModelingPlanner) Plan(
	ctx context.Context,
	input dwdPlanningInput,
) (dwdPlanningCompletion, error) {
	if !planner.Configured() || input.TenantID == "" || input.ActorID == "" ||
		input.Domain == "" || len(input.Tables) == 0 || len(input.Tables) > 48 {
		return dwdPlanningCompletion{}, errDWDModelingInvalid
	}
	raw, err := json.Marshal(struct {
		Domain  string             `json:"domain"`
		Trigger dwdPlanningTrigger `json:"trigger"`
		Tables  []dwdPlanningTable `json:"tables"`
	}{Domain: input.Domain, Trigger: input.Trigger, Tables: input.Tables})
	if err != nil {
		return dwdPlanningCompletion{}, err
	}
	if len(raw) > 256<<10 {
		return dwdPlanningCompletion{}, fmt.Errorf("%w: planning context exceeds 256 KiB", errDWDModelingInvalid)
	}
	schema, err := dwdModelingResponseSchema(input)
	if err != nil {
		return dwdPlanningCompletion{}, err
	}
	temperature := 0.0
	request := aiplatform.ProviderRequest{
		Messages: []aiplatform.Message{
			{
				Role: aiplatform.MessageRoleSystem,
				Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText, Text: dwdModelingSystemPrompt,
				}},
			},
			{
				Role: aiplatform.MessageRoleUser,
				Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText, Text: string(raw),
				}},
			},
		},
		ResponseSchema: schema,
		Temperature:    &temperature,
		// Match the proven interactive DAG planner ceiling. A 32K completion
		// reservation plus the same-domain metadata context exceeds the deployed
		// model gateway budget before inference starts.
		MaxOutputTokens: 8000,
	}
	// 结构错误和领域错误可能依次暴露：第一轮补齐 JSON，第二轮才能发现 Join
	// 或字段合同问题。最多三轮且每轮只携带最新候选，既允许增量修复，也避免
	// 把历史错误响应反复堆进上下文。
	callCtx, cancel := context.WithTimeout(
		ctx, planner.timeout*dwdModelingInvocationAttempts,
	)
	defer cancel()
	invocation := aiplatform.Invocation{
		TenantID: input.TenantID, ActorID: input.ActorID,
		Purpose:       aiplatform.PurposeDatasetDAGGeneration,
		PromptVersion: dwdModelingPromptVersion,
		ResourceType:  "DATASET_VERSION", ResourceID: input.ResourceID,
		Request: request,
	}
	baseMessages := append([]aiplatform.Message(nil), invocation.Request.Messages...)
	for invocationAttempt := 0; invocationAttempt < dwdModelingInvocationAttempts; invocationAttempt++ {
		result, invokeErr := planner.invoker.Invoke(callCtx, invocation)
		completion, validationErr := decodeAndValidateDWDCompletion(
			input, result, invokeErr,
		)
		if validationErr == nil {
			return completion, nil
		}
		if !repairableDWDModelingError(validationErr) ||
			invocationAttempt == dwdModelingInvocationAttempts-1 {
			return dwdPlanningCompletion{}, validationErr
		}
		if err := callCtx.Err(); err != nil {
			return dwdPlanningCompletion{}, err
		}

		repairContent := result.ProviderResult.Content
		repairDiagnostic := ""
		if candidate, diagnostic, ok := aiplatform.InvalidOutputDetails(invokeErr); ok {
			repairContent = candidate
			repairDiagnostic = diagnostic
		}
		repairMessages := append([]aiplatform.Message(nil), baseMessages...)
		if len(repairContent) > 0 &&
			len(repairContent) <= maxDWDModelingRepairContent {
			repairMessages = append(repairMessages, aiplatform.Message{
				Role: aiplatform.MessageRoleAssistant,
				Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText,
					Text: string(repairContent),
				}},
			})
		}
		repairMessages = append(repairMessages, aiplatform.Message{
			Role: aiplatform.MessageRoleUser,
			Parts: []aiplatform.ContentPart{{
				Type: aiplatform.ContentTypeText,
				Text: dwdModelingRepairInstruction(
					validationErr, repairDiagnostic,
				),
			}},
		})
		invocation.Request.Messages = repairMessages
	}
	return dwdPlanningCompletion{}, errDWDModelingInvalid
}

func decodeAndValidateDWDCompletion(
	input dwdPlanningInput,
	result aiplatform.InvocationResult,
	invokeErr error,
) (dwdPlanningCompletion, error) {
	if invokeErr != nil {
		return dwdPlanningCompletion{}, invokeErr
	}
	plan, err := decodeDWDModelingPlan(result.ProviderResult.Content)
	if err != nil {
		return dwdPlanningCompletion{}, err
	}
	plan = normalizeDWDJoinOutputProjection(plan)
	plan = completeMandatoryDWDPolicyCleaning(input, plan)
	if err := validateDWDLLMPlan(input, plan); err != nil {
		return dwdPlanningCompletion{}, err
	}
	return dwdPlanningCompletion{AIRequestID: result.RequestID, Plan: plan}, nil
}

// completeMandatoryDWDPolicyCleaning does not classify tables, select joins, add
// fields or choose field roles. It only materializes hygiene operations that are
// unambiguously required by the platform contract after the LLM has made those
// design decisions.
func completeMandatoryDWDPolicyCleaning(
	input dwdPlanningInput,
	plan dwdLLMPlan,
) dwdLLMPlan {
	fieldsByVersion := make(map[string]map[string]dwdPlanningField, len(input.Tables))
	for _, table := range input.Tables {
		fieldsByVersion[table.VersionID] = planningFieldsByCode(table)
	}
	for outputIndex := range plan.Outputs {
		for fieldIndex := range plan.Outputs[outputIndex].Fields {
			field := &plan.Outputs[outputIndex].Fields[fieldIndex]
			source, exists := fieldsByVersion[field.SourceDatasetVersionID][field.SourceFieldCode]
			if !exists {
				continue
			}
			canonical := strings.ToUpper(source.CanonicalType)
			role := strings.ToUpper(field.Role)
			dimensionRelated := role == "IDENTIFIER" || role == "DIMENSION" ||
				role == "ATTRIBUTE" || role == "TIME"
			nullFillEligible := role == "IDENTIFIER" || role == "DIMENSION" ||
				role == "ATTRIBUTE"
			// 基础卫生策略完全由可信元数据决定。模型可能把字符串清洗误配给
			// 数值/日期字段；若在此保留这些建议，一条错误规则会使整份领域方案
			// 失败。LLM 的业务判断保留在 processing，cleaning 只保留平台合同。
			operations := make(map[string]bool, 3)
			if canonical == "STRING" && dimensionRelated {
				operations["TRIM"] = true
			}
			if source.Nullable && nullFillEligible {
				switch canonical {
				case "STRING":
					operations["COALESCE_UNKNOWN"] = true
				case "INTEGER", "DECIMAL":
					operations["COALESCE_NEGATIVE_ONE"] = true
				}
			}
			switch canonical {
			case "DATE":
				operations["CAST_DATE"] = true
			case "DATETIME":
				operations["CAST_DATETIME"] = true
			case "STRING":
				if role == "TIME" {
					switch {
					case containsString(field.Cleaning, "CAST_DATE"):
						operations["CAST_DATE"] = true
					case containsString(field.Cleaning, "CAST_DATETIME"):
						operations["CAST_DATETIME"] = true
					case strings.EqualFold(source.SemanticType, "DATE") ||
						strings.Contains(strings.ToLower(source.Code), "date"):
						operations["CAST_DATE"] = true
					default:
						operations["CAST_DATETIME"] = true
					}
				}
			}
			field.Cleaning = field.Cleaning[:0]
			for _, operation := range []string{
				"TRIM", "COALESCE_UNKNOWN", "COALESCE_NEGATIVE_ONE",
				"CAST_DATE", "CAST_DATETIME",
			} {
				if operations[operation] {
					field.Cleaning = append(field.Cleaning, operation)
				}
			}
		}
	}
	return plan
}

// normalizeDWDJoinOutputProjection enforces one visible copy of every join key.
// Fact fields are always retained by the DWD contract; every matching dimension
// key, including every condition of a composite relationship, remains available
// to the Join but is removed from the final output projection.
func normalizeDWDJoinOutputProjection(plan dwdLLMPlan) dwdLLMPlan {
	for outputIndex := range plan.Outputs {
		dimensionJoinKeys := map[string]map[string]bool{}
		for _, join := range plan.Outputs[outputIndex].Joins {
			keys := dimensionJoinKeys[join.DimensionDatasetVersionID]
			if keys == nil {
				keys = map[string]bool{}
				dimensionJoinKeys[join.DimensionDatasetVersionID] = keys
			}
			for _, condition := range join.Conditions {
				keys[strings.ToLower(condition.DimensionFieldCode)] = true
			}
		}
		filtered := plan.Outputs[outputIndex].Fields[:0]
		for _, field := range plan.Outputs[outputIndex].Fields {
			if dimensionJoinKeys[field.SourceDatasetVersionID][strings.ToLower(field.SourceFieldCode)] {
				continue
			}
			filtered = append(filtered, field)
		}
		plan.Outputs[outputIndex].Fields = filtered
	}
	return plan
}

func repairableDWDModelingError(err error) bool {
	if errors.Is(err, errDWDModelingInvalid) {
		return true
	}
	var providerError *aiplatform.ProviderError
	return errors.As(err, &providerError) &&
		providerError.Code == aiplatform.ErrorCodeInvalidOutput
}

func dwdModelingRepairInstruction(validationErr error, diagnostic string) string {
	reason := validationErr.Error()
	if strings.TrimSpace(diagnostic) != "" {
		reason += "；结构诊断：" + strings.TrimSpace(diagnostic)
	}
	return fmt.Sprintf(`上一份 DWD 方案未通过结构或本地安全校验。原因：%s

请重新检查原始同领域 ODS 元数据，并只输出一份修复后的完整 JSON：
1. classifications 必须逐表覆盖且不重复；每个 FACT 必须恰好有一个 output。
2. 只能复制输入中的精确 datasetVersionId 和字段 code；维度关联只允许 LEFT JOIN，且关联键类型兼容。
3. 每个 FACT 的全部字段都必须出现在其 output.fields 中；逐一检查标识/维度及 _id/_key 字段，存在唯一同名且类型兼容的 DIMENSION/MASTER 键时必须 LEFT JOIN；复合业务键必须放入同一个 join.conditions 并完整覆盖。
4. 每个已关联维度必须至少输出一个关联键之外的描述字段（只要该表存在），维度扩充字段必须来自该 output 已关联的 DIMENSION/MASTER；维度侧所有关联键不得出现在 output.fields。
5. 非时间的字符串维度相关字段依次使用 TRIM 和必要的 COALESCE_UNKNOWN；可空数值维度相关字段使用 COALESCE_NEGATIVE_ONE；字符串时间先 TRIM 再 CAST_DATE/CAST_DATETIME 且不得补 UNKNOWN；DATE/DATETIME 使用对应 CAST；度量和时间不得补默认值。
6. processing 可按实际需要使用全部已声明处理操作；每步只按原始提示规定填写紧凑 arguments，同类多字段分别声明规则即可，平台会合并组件。二元操作的次字段必须来自已关联输入。
7. grainKeyOutputCodes 和 timeOutputCode 只能引用 output.fields 的 outputCode。
8. 为控制响应长度，名称、说明和 rationale 保持简短，不复述输入，不返回 SQL、Markdown 或解释。`, reason)
}

func dwdClassificationResponseSchema(
	input dwdPlanningInput,
) (aiplatform.JSONSchema, error) {
	versionIDs := make([]string, 0, len(input.Tables))
	for _, table := range input.Tables {
		versionIDs = append(versionIDs, table.VersionID)
	}
	sort.Strings(versionIDs)
	if len(versionIDs) == 0 {
		return aiplatform.JSONSchema{}, errDWDModelingInvalid
	}
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"domain", "classifications"},
		"properties": map[string]any{
			"domain": map[string]any{
				"type": "string", "enum": []string{input.Domain},
			},
			"classifications": map[string]any{
				"type":     "array",
				"minItems": len(input.Tables), "maxItems": len(input.Tables),
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"datasetVersionId", "role", "rationale"},
					"properties": map[string]any{
						"datasetVersionId": map[string]any{
							"type": "string", "enum": versionIDs,
						},
						"role": map[string]any{
							"type": "string",
							"enum": []string{"FACT", "DIMENSION", "MASTER", "OTHER"},
						},
						"rationale": map[string]any{
							"type": "string", "maxLength": 1024,
						},
					},
				},
			},
		},
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return aiplatform.JSONSchema{}, err
	}
	return aiplatform.JSONSchema{
		Name:        "warehouse_ods_classification",
		Description: "同领域 ODS 的事实、维度、主数据和其他角色分类",
		Schema:      raw,
	}, nil
}

func dwdFactDesignResponseSchema(
	input dwdPlanningInput,
	factVersionID string,
) (aiplatform.JSONSchema, error) {
	full, err := dwdModelingResponseSchema(input)
	if err != nil {
		return aiplatform.JSONSchema{}, err
	}
	var root map[string]any
	if err := json.Unmarshal(full.Schema, &root); err != nil {
		return aiplatform.JSONSchema{}, err
	}
	properties, ok := root["properties"].(map[string]any)
	if !ok {
		return aiplatform.JSONSchema{}, errDWDModelingInvalid
	}
	outputs, ok := properties["outputs"].(map[string]any)
	if !ok {
		return aiplatform.JSONSchema{}, errDWDModelingInvalid
	}
	output, ok := outputs["items"].(map[string]any)
	if !ok {
		return aiplatform.JSONSchema{}, errDWDModelingInvalid
	}
	outputProperties, ok := output["properties"].(map[string]any)
	if !ok {
		return aiplatform.JSONSchema{}, errDWDModelingInvalid
	}
	factProperty, ok := outputProperties["factDatasetVersionId"].(map[string]any)
	if !ok {
		return aiplatform.JSONSchema{}, errDWDModelingInvalid
	}
	factProperty["enum"] = []string{factVersionID}
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"required":   []string{"output"},
		"properties": map[string]any{"output": output},
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return aiplatform.JSONSchema{}, err
	}
	return aiplatform.JSONSchema{
		Name:        "warehouse_fact_dwd_design",
		Description: "单个事实 ODS 的 DWD 明细结构与 DAG 设计",
		Schema:      raw,
	}, nil
}

func dwdModelingResponseSchema(input dwdPlanningInput) (aiplatform.JSONSchema, error) {
	versionIDs := make([]string, 0, len(input.Tables))
	maxFields := 0
	for _, table := range input.Tables {
		versionIDs = append(versionIDs, table.VersionID)
		maxFields += len(table.Fields)
	}
	sort.Strings(versionIDs)
	if len(versionIDs) == 0 || maxFields == 0 {
		return aiplatform.JSONSchema{}, errDWDModelingInvalid
	}
	optionalVersionIDs := append([]string{""}, versionIDs...)
	maxOutputFields := min(maxFields, 2048)
	stringProperty := func(maxLength int) map[string]any {
		return map[string]any{"type": "string", "maxLength": maxLength}
	}
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"domain", "classifications", "outputs"},
		"properties": map[string]any{
			"domain": map[string]any{"type": "string", "enum": []string{input.Domain}},
			"classifications": map[string]any{
				"type": "array", "minItems": len(input.Tables), "maxItems": len(input.Tables),
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"datasetVersionId", "role", "rationale"},
					"properties": map[string]any{
						"datasetVersionId": map[string]any{"type": "string", "enum": versionIDs},
						"role":             map[string]any{"type": "string", "enum": []string{"FACT", "DIMENSION", "MASTER", "OTHER"}},
						"rationale":        stringProperty(1024),
					},
				},
			},
			"outputs": map[string]any{
				"type": "array", "minItems": 0, "maxItems": len(input.Tables),
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{
						"factDatasetVersionId", "name", "description", "joins",
						"fields", "grainKeyOutputCodes", "timeOutputCode", "rationale",
					},
					"properties": map[string]any{
						"factDatasetVersionId": map[string]any{"type": "string", "enum": versionIDs},
						"name":                 stringProperty(256),
						"description":          stringProperty(4096),
						"joins": map[string]any{
							"type": "array", "minItems": 0, "maxItems": MaxNodes - 1,
							"items": map[string]any{
								"type": "object", "additionalProperties": false,
								"required": []string{
									"dimensionDatasetVersionId", "conditions",
									"joinType", "rationale",
								},
								"properties": map[string]any{
									"dimensionDatasetVersionId": map[string]any{"type": "string", "enum": versionIDs},
									"conditions": map[string]any{
										"type": "array", "minItems": 1, "maxItems": 8,
										"items": map[string]any{
											"type": "object", "additionalProperties": false,
											"required": []string{"factFieldCode", "dimensionFieldCode"},
											"properties": map[string]any{
												"factFieldCode":      stringProperty(128),
												"dimensionFieldCode": stringProperty(128),
											},
										},
									},
									"joinType":  map[string]any{"type": "string", "enum": []string{"LEFT"}},
									"rationale": stringProperty(1024),
								},
							},
						},
						"fields": map[string]any{
							"type": "array", "minItems": 1, "maxItems": maxOutputFields,
							"items": map[string]any{
								"type": "object", "additionalProperties": false,
								"required": []string{
									"sourceDatasetVersionId", "sourceFieldCode", "outputCode",
									"outputName", "outputDescription", "role", "cleaning",
									"processing",
								},
								"properties": map[string]any{
									"sourceDatasetVersionId": map[string]any{"type": "string", "enum": versionIDs},
									"sourceFieldCode":        stringProperty(128),
									"outputCode":             stringProperty(128),
									"outputName":             stringProperty(256),
									"outputDescription":      stringProperty(4096),
									"role": map[string]any{
										"type": "string",
										"enum": []string{"DIMENSION", "MEASURE", "ATTRIBUTE", "TIME", "IDENTIFIER"},
									},
									"cleaning": map[string]any{
										"type": "array", "minItems": 0, "maxItems": 3,
										"items": map[string]any{
											"type": "string",
											"enum": []string{
												"TRIM", "COALESCE_UNKNOWN", "COALESCE_NEGATIVE_ONE",
												"CAST_DATE", "CAST_DATETIME",
											},
										},
									},
									"processing": map[string]any{
										"type": "array", "minItems": 0, "maxItems": 32,
										"items": map[string]any{
											"type": "object", "additionalProperties": false,
											"required": []string{
												"operation", "arguments",
												"secondarySourceDatasetVersionId",
												"secondarySourceFieldCode",
											},
											"properties": map[string]any{
												"operation": map[string]any{
													"type": "string",
													"enum": []string{
														"DATE_FORMAT", "DATE_TRUNC", "CAST", "TRIM",
														"UPPER", "LOWER", "REPLACE", "SUBSTRING",
														"CONCAT", "COALESCE", "ADD", "SUBTRACT",
														"MULTIPLY", "DIVIDE", "ROUND", "ABS",
														"FLOOR", "CEIL", "CASE",
													},
												},
												"arguments": map[string]any{
													"type": "array", "minItems": 0, "maxItems": 4,
													"items": stringProperty(512),
												},
												"secondarySourceDatasetVersionId": map[string]any{
													"type": "string", "enum": optionalVersionIDs,
												},
												"secondarySourceFieldCode": stringProperty(128),
											},
										},
									},
								},
							},
						},
						"grainKeyOutputCodes": map[string]any{
							"type": "array", "minItems": 1, "maxItems": 32,
							"items": stringProperty(128),
						},
						"timeOutputCode": stringProperty(128),
						"rationale":      stringProperty(2048),
					},
				},
			},
		},
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return aiplatform.JSONSchema{}, err
	}
	return aiplatform.JSONSchema{
		Name:        "warehouse_modeling_plan",
		Description: "同领域 ODS 的 DIM 分类决策与事实中心 DWD 明细 DAG 设计",
		Schema:      raw,
	}, nil
}

func decodeDWDClassificationPlan(raw []byte) (dwdLLMClassificationPlan, error) {
	var plan dwdLLMClassificationPlan
	if err := decodeStrictDWDJSON(raw, &plan); err != nil {
		return dwdLLMClassificationPlan{}, err
	}
	return plan, nil
}

func decodeDWDFactDesign(raw []byte) (dwdLLMFactDesign, error) {
	var design dwdLLMFactDesign
	if err := decodeStrictDWDJSON(raw, &design); err != nil {
		return dwdLLMFactDesign{}, err
	}
	return design, nil
}

func decodeDWDModelingPlan(raw []byte) (dwdLLMPlan, error) {
	var plan dwdLLMPlan
	if err := decodeStrictDWDJSON(raw, &plan); err != nil {
		return dwdLLMPlan{}, err
	}
	return plan, nil
}

func decodeStrictDWDJSON(raw []byte, destination any) error {
	if len(raw) == 0 || len(raw) > 2<<20 {
		return errDWDModelingInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: decode LLM warehouse plan", errDWDModelingInvalid)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing LLM warehouse content", errDWDModelingInvalid)
	}
	return nil
}

func normalizeDWDClassifications(
	input dwdPlanningInput,
	classifications []dwdLLMClassification,
) []dwdLLMClassification {
	byVersion := make(map[string]dwdLLMClassification, len(classifications))
	for _, classification := range classifications {
		byVersion[classification.DatasetVersionID] = classification
	}
	normalized := make([]dwdLLMClassification, 0, len(input.Tables))
	for _, table := range input.Tables {
		if classification, exists := byVersion[table.VersionID]; exists {
			normalized = append(normalized, classification)
		}
	}
	return normalized
}

func dwdFactPlanningScope(
	input dwdPlanningInput,
	classifications []dwdLLMClassification,
	factVersionID string,
) (dwdPlanningInput, []dwdLLMClassification, error) {
	if err := validateDWDLLMClassifications(
		input, input.Domain, classifications,
	); err != nil {
		return dwdPlanningInput{}, nil, err
	}
	roleByVersion := make(map[string]string, len(classifications))
	classificationByVersion := make(
		map[string]dwdLLMClassification, len(classifications),
	)
	for _, classification := range classifications {
		roleByVersion[classification.DatasetVersionID] = classification.Role
		classificationByVersion[classification.DatasetVersionID] = classification
	}
	if roleByVersion[factVersionID] != "FACT" {
		return dwdPlanningInput{}, nil, fmt.Errorf(
			"%w: requested fact is not classified as FACT", errDWDModelingInvalid,
		)
	}
	scoped := input
	scoped.ResourceID = factVersionID
	scoped.Tables = make([]dwdPlanningTable, 0, len(input.Tables))
	scopedClassifications := make(
		[]dwdLLMClassification, 0, len(input.Tables),
	)
	for _, table := range input.Tables {
		role := roleByVersion[table.VersionID]
		if table.VersionID != factVersionID &&
			role != "DIMENSION" && role != "MASTER" {
			continue
		}
		scoped.Tables = append(scoped.Tables, table)
		scopedClassifications = append(
			scopedClassifications, classificationByVersion[table.VersionID],
		)
	}
	if err := validateDWDLLMClassifications(
		scoped, scoped.Domain, scopedClassifications,
	); err != nil {
		return dwdPlanningInput{}, nil, err
	}
	return scoped, scopedClassifications, nil
}

func validateDWDLLMPlan(input dwdPlanningInput, plan dwdLLMPlan) error {
	if err := validateDWDLLMClassifications(
		input, plan.Domain, plan.Classifications,
	); err != nil {
		return err
	}
	tableByVersion := map[string]dwdPlanningTable{}
	for _, table := range input.Tables {
		tableByVersion[table.VersionID] = table
	}
	roleByVersion := map[string]string{}
	for _, classification := range plan.Classifications {
		roleByVersion[classification.DatasetVersionID] = classification.Role
	}
	outputByFact := map[string]bool{}
	issues := []string{}
	for outputIndex, output := range plan.Outputs {
		if roleByVersion[output.FactDatasetVersionID] != "FACT" ||
			outputByFact[output.FactDatasetVersionID] {
			appendDWDValidationIssue(
				&issues, "outputs[%d] is not bound to one unique FACT", outputIndex,
			)
			continue
		}
		outputByFact[output.FactDatasetVersionID] = true
		fact := tableByVersion[output.FactDatasetVersionID]
		if err := validateDWDLLMOutput(fact, tableByVersion, roleByVersion, output); err != nil {
			appendDWDValidationIssue(
				&issues, "output %s: %s",
				output.FactDatasetVersionID, dwdValidationDetail(err),
			)
		}
	}
	for versionID, role := range roleByVersion {
		if role == "FACT" && !outputByFact[versionID] {
			appendDWDValidationIssue(
				&issues, "FACT %s has no DWD output", versionID,
			)
		}
	}
	return dwdValidationError(issues)
}

func validateDWDLLMClassifications(
	input dwdPlanningInput,
	domain string,
	classifications []dwdLLMClassification,
) error {
	if domain != input.Domain || len(classifications) != len(input.Tables) {
		return fmt.Errorf(
			"%w: LLM classification coverage is incomplete",
			errDWDModelingInvalid,
		)
	}
	tableByVersion := make(map[string]bool, len(input.Tables))
	for _, table := range input.Tables {
		if table.VersionID == "" || tableByVersion[table.VersionID] {
			return errDWDModelingInvalid
		}
		tableByVersion[table.VersionID] = true
	}
	roleByVersion := make(map[string]string, len(classifications))
	for _, classification := range classifications {
		if !tableByVersion[classification.DatasetVersionID] ||
			roleByVersion[classification.DatasetVersionID] != "" ||
			!containsString(
				[]string{"FACT", "DIMENSION", "MASTER", "OTHER"},
				classification.Role,
			) ||
			strings.TrimSpace(classification.Rationale) == "" {
			return fmt.Errorf(
				"%w: LLM classification is invalid", errDWDModelingInvalid,
			)
		}
		roleByVersion[classification.DatasetVersionID] = classification.Role
	}
	if len(roleByVersion) != len(tableByVersion) {
		return fmt.Errorf(
			"%w: LLM did not classify every ODS", errDWDModelingInvalid,
		)
	}
	return nil
}

func appendDWDValidationIssue(issues *[]string, format string, arguments ...any) {
	if len(*issues) >= maxDWDValidationIssues {
		return
	}
	*issues = append(*issues, fmt.Sprintf(format, arguments...))
}

func dwdValidationError(issues []string) error {
	if len(issues) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", errDWDModelingInvalid, strings.Join(issues, "; "))
}

func dwdValidationDetail(err error) string {
	detail := strings.TrimSpace(err.Error())
	return strings.TrimPrefix(detail, errDWDModelingInvalid.Error()+": ")
}

func validateDWDLLMOutput(
	fact dwdPlanningTable,
	tables map[string]dwdPlanningTable,
	roles map[string]string,
	output dwdLLMOutput,
) error {
	issues := []string{}
	if strings.TrimSpace(output.Name) == "" {
		appendDWDValidationIssue(&issues, "output name is empty")
	}
	if strings.TrimSpace(output.Description) == "" {
		appendDWDValidationIssue(&issues, "output description is empty")
	}
	if len(output.Fields) == 0 {
		appendDWDValidationIssue(&issues, "output fields are empty")
	}
	if len(output.GrainKeyOutputCodes) == 0 {
		appendDWDValidationIssue(&issues, "output grain keys are empty")
	}
	factFields := planningFieldsByCode(fact)
	joined := map[string]bool{fact.VersionID: true}
	type joinBinding struct {
		join      dwdLLMJoin
		condition dwdLLMJoinCondition
	}
	joinByFactField := map[string]joinBinding{}
	dimensionJoinKeys := map[string]map[string]bool{}
	for _, join := range output.Joins {
		dimension, exists := tables[join.DimensionDatasetVersionID]
		switch {
		case !exists:
			return fmt.Errorf(
				"%w: DWD join references unknown dimension %s",
				errDWDModelingInvalid, join.DimensionDatasetVersionID,
			)
		case joined[join.DimensionDatasetVersionID]:
			return fmt.Errorf(
				"%w: DWD dimension %s is joined more than once; merge all keys into one conditions array",
				errDWDModelingInvalid, join.DimensionDatasetVersionID,
			)
		case roles[join.DimensionDatasetVersionID] != "DIMENSION" &&
			roles[join.DimensionDatasetVersionID] != "MASTER":
			return fmt.Errorf(
				"%w: DWD join target %s is classified as %s instead of DIMENSION/MASTER",
				errDWDModelingInvalid, join.DimensionDatasetVersionID,
				roles[join.DimensionDatasetVersionID],
			)
		case join.JoinType != "LEFT":
			return fmt.Errorf("%w: DWD join type must be LEFT", errDWDModelingInvalid)
		case len(join.Conditions) == 0 || len(join.Conditions) > 8:
			return fmt.Errorf(
				"%w: DWD join conditions count must be 1..8",
				errDWDModelingInvalid,
			)
		}
		conditionSeen := map[string]bool{}
		dimensionFields := planningFieldsByCode(dimension)
		dimensionKeys := map[string]bool{}
		for _, condition := range join.Conditions {
			factField, factOK := factFields[condition.FactFieldCode]
			dimensionField, dimensionOK := dimensionFields[condition.DimensionFieldCode]
			conditionKey := strings.ToLower(condition.FactFieldCode) + "\x00" +
				strings.ToLower(condition.DimensionFieldCode)
			if !factOK || !dimensionOK || conditionSeen[conditionKey] ||
				!dwdCanonicalTypesCompatible(
					factField.CanonicalType, dimensionField.CanonicalType,
				) {
				return fmt.Errorf("%w: DWD join field is missing, duplicated or type-incompatible", errDWDModelingInvalid)
			}
			conditionSeen[conditionKey] = true
			dimensionKeys[strings.ToLower(condition.DimensionFieldCode)] = true
			joinByFactField[strings.ToLower(condition.FactFieldCode)] = joinBinding{
				join: join, condition: condition,
			}
		}
		joined[join.DimensionDatasetVersionID] = true
		dimensionJoinKeys[join.DimensionDatasetVersionID] = dimensionKeys
	}
	for factFieldCode, candidate := range requiredDWDJoinCandidates(fact, tables, roles) {
		binding, exists := joinByFactField[strings.ToLower(factFieldCode)]
		if !exists ||
			binding.join.DimensionDatasetVersionID != candidate.DimensionDatasetVersionID ||
			!strings.EqualFold(
				binding.condition.DimensionFieldCode,
				candidate.DimensionFieldCode,
			) {
			appendDWDValidationIssue(
				&issues,
				"fact reference %s has unique compatible dimension key %s.%s but is not joined",
				factFieldCode, candidate.DimensionDatasetVersionID,
				candidate.DimensionFieldCode,
			)
		}
	}

	outputCodes := map[string]bool{}
	timeOutputCodes := map[string]bool{}
	includedFactFields := map[string]bool{}
	dimensionOutputFields := map[string]map[string]bool{}
	selectedSourceFields := map[string]map[string]bool{}
	for _, field := range output.Fields {
		if selectedSourceFields[field.SourceDatasetVersionID] == nil {
			selectedSourceFields[field.SourceDatasetVersionID] = map[string]bool{}
		}
		selectedSourceFields[field.SourceDatasetVersionID][strings.ToLower(field.SourceFieldCode)] = true
	}
	for fieldIndex, field := range output.Fields {
		sourceTable, exists := tables[field.SourceDatasetVersionID]
		if !exists {
			appendDWDValidationIssue(
				&issues, "field[%d] references unknown source dataset version",
				fieldIndex,
			)
		}
		sourceField, fieldExists := dwdPlanningField{}, false
		if exists {
			sourceField, fieldExists = planningFieldsByCode(sourceTable)[field.SourceFieldCode]
		}
		if exists && !fieldExists {
			appendDWDValidationIssue(
				&issues, "field[%d] references unknown source field %s",
				fieldIndex, field.SourceFieldCode,
			)
		}
		if exists && !joined[field.SourceDatasetVersionID] {
			appendDWDValidationIssue(
				&issues, "field[%d] uses a dimension that is not joined", fieldIndex,
			)
		}
		if dimensionJoinKeys[field.SourceDatasetVersionID][strings.ToLower(field.SourceFieldCode)] {
			appendDWDValidationIssue(
				&issues,
				"field[%d] repeats a dimension-side join key in output",
				fieldIndex,
			)
		}
		outputCodeValid := identifierPattern.MatchString(field.OutputCode)
		if !outputCodeValid {
			appendDWDValidationIssue(
				&issues, "field[%d] outputCode is not a valid identifier", fieldIndex,
			)
		}
		outputCodeKey := strings.ToLower(field.OutputCode)
		outputCodeDuplicate := outputCodeValid && outputCodes[outputCodeKey]
		if outputCodeDuplicate {
			appendDWDValidationIssue(
				&issues, "field[%d] duplicates outputCode %s",
				fieldIndex, field.OutputCode,
			)
		}
		if strings.TrimSpace(field.OutputName) == "" ||
			strings.TrimSpace(field.OutputDescription) == "" {
			appendDWDValidationIssue(
				&issues, "field[%d] name or description is empty", fieldIndex,
			)
		}
		roleValid := containsString(
			[]string{"DIMENSION", "MEASURE", "ATTRIBUTE", "TIME", "IDENTIFIER"},
			field.Role,
		)
		if !roleValid {
			appendDWDValidationIssue(
				&issues, "field[%d] has invalid role %s", fieldIndex, field.Role,
			)
		}
		if exists && fieldExists && roleValid {
			sourceMeasure := strings.EqualFold(sourceField.Role, "MEASURE")
			sourceField.Role = field.Role
			if err := validateDWDCleaning(sourceField, field.Cleaning); err != nil {
				appendDWDValidationIssue(
					&issues, "field %s cleaning is invalid: %v",
					field.OutputCode, err,
				)
			}
			if err := validateDWDProcessing(
				sourceField, field.Cleaning, field.Processing,
				tables, joined, selectedSourceFields,
			); err != nil {
				appendDWDValidationIssue(
					&issues, "field %s processing is invalid: %v",
					field.OutputCode, err,
				)
			}
			if sourceMeasure && (containsString(field.Cleaning, "COALESCE_UNKNOWN") ||
				containsString(field.Cleaning, "COALESCE_NEGATIVE_ONE")) {
				appendDWDValidationIssue(
					&issues, "measure field %s must not fill nulls", field.OutputCode,
				)
			}
		}
		if outputCodeValid && !outputCodeDuplicate {
			outputCodes[outputCodeKey] = true
		}
		if exists && fieldExists {
			if field.SourceDatasetVersionID != fact.VersionID {
				if dimensionOutputFields[field.SourceDatasetVersionID] == nil {
					dimensionOutputFields[field.SourceDatasetVersionID] = map[string]bool{}
				}
				dimensionOutputFields[field.SourceDatasetVersionID][strings.ToLower(field.SourceFieldCode)] = true
			}
			if field.Role == "TIME" &&
				containsString(
					[]string{"DATE", "DATETIME", "STRING"},
					strings.ToUpper(sourceField.CanonicalType),
				) && outputCodeValid && !outputCodeDuplicate {
				timeOutputCodes[outputCodeKey] = true
			}
			if field.SourceDatasetVersionID == fact.VersionID {
				includedFactFields[strings.ToLower(field.SourceFieldCode)] = true
			}
		}
	}
	for _, join := range output.Joins {
		dimension := tables[join.DimensionDatasetVersionID]
		hasDescriptiveField := false
		for _, candidate := range dimension.Fields {
			if dimensionJoinKeys[join.DimensionDatasetVersionID][strings.ToLower(candidate.Code)] ||
				strings.EqualFold(candidate.Role, "MEASURE") ||
				strings.EqualFold(candidate.Role, "TIME") {
				continue
			}
			hasDescriptiveField = true
			break
		}
		if !hasDescriptiveField {
			continue
		}
		expanded := false
		for code := range dimensionOutputFields[join.DimensionDatasetVersionID] {
			if !dimensionJoinKeys[join.DimensionDatasetVersionID][strings.ToLower(code)] {
				expanded = true
				break
			}
		}
		if !expanded {
			appendDWDValidationIssue(
				&issues,
				"joined dimension %s has descriptive fields but none are added to output",
				join.DimensionDatasetVersionID,
			)
		}
	}
	for code := range factFields {
		if !includedFactFields[strings.ToLower(code)] {
			appendDWDValidationIssue(&issues, "fact field %s is missing from output", code)
		}
	}
	for _, code := range output.GrainKeyOutputCodes {
		if !outputCodes[strings.ToLower(code)] {
			appendDWDValidationIssue(
				&issues, "grain key %s is not a valid output field", code,
			)
		}
	}
	if output.TimeOutputCode != "" && !timeOutputCodes[strings.ToLower(output.TimeOutputCode)] {
		appendDWDValidationIssue(
			&issues, "time field %s is not a valid TIME output", output.TimeOutputCode,
		)
	}
	return dwdValidationError(issues)
}

type requiredDWDJoin struct {
	DimensionDatasetVersionID string
	DimensionFieldCode        string
}

// requiredDWDJoinCandidates is intentionally conservative: it only turns a
// relationship into a hard contract when an identifier-like fact field has one
// and only one exact-name, type-compatible DIMENSION/MASTER key. Broader fuzzy
// relationship discovery remains the LLM's responsibility.
func requiredDWDJoinCandidates(
	fact dwdPlanningTable,
	tables map[string]dwdPlanningTable,
	roles map[string]string,
) map[string]requiredDWDJoin {
	result := map[string]requiredDWDJoin{}
	for _, factField := range fact.Fields {
		code := strings.ToLower(strings.TrimSpace(factField.Code))
		identifierLike := strings.EqualFold(factField.Role, "IDENTIFIER") ||
			strings.HasSuffix(code, "_id") || strings.HasSuffix(code, "_key")
		if !identifierLike || strings.EqualFold(factField.Role, "MEASURE") ||
			strings.EqualFold(factField.Role, "TIME") {
			continue
		}
		candidates := []requiredDWDJoin{}
		for versionID, table := range tables {
			if versionID == fact.VersionID ||
				(roles[versionID] != "DIMENSION" && roles[versionID] != "MASTER") {
				continue
			}
			for _, dimensionField := range table.Fields {
				if strings.EqualFold(dimensionField.Code, factField.Code) &&
					dwdCanonicalTypesCompatible(
						factField.CanonicalType, dimensionField.CanonicalType,
					) {
					candidates = append(candidates, requiredDWDJoin{
						DimensionDatasetVersionID: versionID,
						DimensionFieldCode:        dimensionField.Code,
					})
				}
			}
		}
		if len(candidates) == 1 {
			result[factField.Code] = candidates[0]
		}
	}
	return result
}

func planningFieldsByCode(table dwdPlanningTable) map[string]dwdPlanningField {
	fields := make(map[string]dwdPlanningField, len(table.Fields))
	for _, field := range table.Fields {
		fields[field.Code] = field
	}
	return fields
}

func validateDWDCleaning(field dwdPlanningField, cleaning []string) error {
	seen := map[string]bool{}
	for _, operation := range cleaning {
		if !containsString([]string{
			"TRIM", "COALESCE_UNKNOWN", "COALESCE_NEGATIVE_ONE",
			"CAST_DATE", "CAST_DATETIME",
		}, operation) {
			return fmt.Errorf("unsupported operation %s", operation)
		}
		if seen[operation] {
			return fmt.Errorf("duplicate operation %s", operation)
		}
		seen[operation] = true
	}
	canonical := strings.ToUpper(field.CanonicalType)
	role := strings.ToUpper(field.Role)
	dimensionRelated := role == "IDENTIFIER" || role == "DIMENSION" ||
		role == "ATTRIBUTE" || role == "TIME"
	nullFillEligible := role == "IDENTIFIER" || role == "DIMENSION" ||
		role == "ATTRIBUTE"
	if canonical == "STRING" && dimensionRelated && !seen["TRIM"] {
		return errors.New("STRING dimension/time field requires TRIM")
	}
	if canonical == "STRING" && nullFillEligible && field.Nullable &&
		!seen["COALESCE_UNKNOWN"] {
		return errors.New("nullable STRING dimension field requires COALESCE_UNKNOWN")
	}
	if (canonical == "INTEGER" || canonical == "DECIMAL") &&
		nullFillEligible && field.Nullable && !seen["COALESCE_NEGATIVE_ONE"] {
		return errors.New("nullable numeric dimension field requires COALESCE_NEGATIVE_ONE")
	}
	if canonical == "DATE" && !seen["CAST_DATE"] {
		return errors.New("DATE field requires CAST_DATE")
	}
	if canonical == "DATETIME" && !seen["CAST_DATETIME"] {
		return errors.New("DATETIME field requires CAST_DATETIME")
	}
	if role == "TIME" && canonical == "STRING" &&
		!seen["CAST_DATE"] && !seen["CAST_DATETIME"] {
		return errors.New("STRING time field requires CAST_DATE or CAST_DATETIME")
	}
	if !dimensionRelated &&
		(seen["COALESCE_UNKNOWN"] || seen["COALESCE_NEGATIVE_ONE"]) {
		return errors.New("non-dimension field must not fill nulls")
	}
	if role == "TIME" &&
		(seen["COALESCE_UNKNOWN"] || seen["COALESCE_NEGATIVE_ONE"]) {
		return errors.New("time field must not fill nulls with a sentinel")
	}
	if seen["TRIM"] || seen["COALESCE_UNKNOWN"] {
		if canonical != "STRING" {
			return errors.New("TRIM/COALESCE_UNKNOWN require STRING input")
		}
	}
	if seen["COALESCE_NEGATIVE_ONE"] &&
		canonical != "INTEGER" && canonical != "DECIMAL" {
		return errors.New("COALESCE_NEGATIVE_ONE requires numeric input")
	}
	if seen["CAST_DATE"] && canonical != "DATE" &&
		!(canonical == "STRING" && role == "TIME") {
		return errors.New("CAST_DATE requires DATE or STRING time input")
	}
	if seen["CAST_DATETIME"] && canonical != "DATETIME" &&
		!(canonical == "STRING" && role == "TIME") {
		return errors.New("CAST_DATETIME requires DATETIME or STRING time input")
	}
	if seen["CAST_DATE"] && seen["CAST_DATETIME"] {
		return errors.New("CAST_DATE and CAST_DATETIME are mutually exclusive")
	}
	return nil
}

func validateDWDProcessing(
	field dwdPlanningField,
	cleaning []string,
	processing []dwdLLMProcessingStep,
	tables map[string]dwdPlanningTable,
	joined map[string]bool,
	selectedSourceFields map[string]map[string]bool,
) error {
	if len(processing) > 32 {
		return errors.New("processing steps exceed 32")
	}
	canonical := strings.ToUpper(strings.TrimSpace(field.CanonicalType))
	for _, operation := range cleaning {
		switch operation {
		case "CAST_DATE":
			canonical = "DATE"
		case "CAST_DATETIME":
			canonical = "DATETIME"
		}
	}
	numeric := func(value string) bool {
		return value == "INTEGER" || value == "DECIMAL"
	}
	for index, rawStep := range processing {
		step, normalizeErr := normalizeDWDProcessingStep(rawStep)
		if normalizeErr != nil {
			return fmt.Errorf("processing[%d] has invalid arguments", index)
		}
		operation := strings.ToUpper(strings.TrimSpace(step.Operation))
		stepPath := fmt.Sprintf("processing[%d] %s", index, operation)
		if !containsString([]string{
			"DATE_FORMAT", "DATE_TRUNC", "CAST", "TRIM", "UPPER", "LOWER",
			"REPLACE", "SUBSTRING", "CONCAT", "COALESCE", "ADD", "SUBTRACT",
			"MULTIPLY", "DIVIDE", "ROUND", "ABS", "FLOOR", "CEIL", "CASE",
		}, operation) {
			return fmt.Errorf("%s is unsupported", stepPath)
		}
		switch operation {
		case "DATE_FORMAT":
			if !containsString([]string{"DAY", "MONTH", "QUARTER", "YEAR"}, step.Unit) ||
				!containsString([]string{"DATE", "DATETIME"}, canonical) {
				return fmt.Errorf("%s requires DATE/DATETIME and a supported unit", stepPath)
			}
			canonical = "STRING"
		case "DATE_TRUNC":
			if !containsString([]string{"DAY", "WEEK", "MONTH", "QUARTER", "YEAR"}, step.Unit) ||
				!containsString([]string{"DATE", "DATETIME"}, canonical) {
				return fmt.Errorf("%s requires DATE/DATETIME and a supported unit", stepPath)
			}
		case "CAST":
			target := strings.ToUpper(strings.TrimSpace(step.TargetType))
			if !containsString(
				[]string{"STRING", "INTEGER", "DECIMAL", "BOOLEAN", "DATE", "DATETIME"},
				target,
			) {
				return fmt.Errorf("%s has invalid target type", stepPath)
			}
			canonical = target
		case "TRIM", "UPPER", "LOWER", "REPLACE", "SUBSTRING":
			if operation == "REPLACE" && step.SearchValue == "" {
				return fmt.Errorf("%s requires non-empty searchValue", stepPath)
			}
			if operation == "SUBSTRING" && (step.Start < 1 || step.Length < 0) {
				return fmt.Errorf("%s requires start >= 1 and length >= 0", stepPath)
			}
			canonical = "STRING"
		case "CONCAT", "ADD", "SUBTRACT", "MULTIPLY", "DIVIDE":
			secondaryTable, tableExists := tables[step.SecondarySourceDatasetVersionID]
			secondaryField, fieldExists := dwdPlanningField{}, false
			if tableExists {
				secondaryField, fieldExists = planningFieldsByCode(secondaryTable)[step.SecondarySourceFieldCode]
			}
			if !tableExists || !fieldExists ||
				!joined[step.SecondarySourceDatasetVersionID] ||
				!selectedSourceFields[step.SecondarySourceDatasetVersionID][strings.ToLower(step.SecondarySourceFieldCode)] {
				return fmt.Errorf("%s references a missing or unavailable secondary field", stepPath)
			}
			if operation == "CONCAT" {
				canonical = "STRING"
				break
			}
			if !numeric(canonical) ||
				!numeric(strings.ToUpper(secondaryField.CanonicalType)) {
				return fmt.Errorf("%s requires two numeric fields", stepPath)
			}
			canonical = "DECIMAL"
		case "COALESCE":
			// 空字符串本身是合法的 STRING 回填值；其余类型在编译阶段执行
			// 严格字面量解析，避免把自由文本隐式转换成 SQL。
		case "ROUND":
			if !numeric(canonical) || step.Precision < -10 || step.Precision > 10 {
				return fmt.Errorf("%s requires numeric input and precision -10..10", stepPath)
			}
		case "ABS", "FLOOR", "CEIL":
			if !numeric(canonical) {
				return fmt.Errorf("%s requires numeric input", stepPath)
			}
		case "CASE":
			if !containsString([]string{
				"EQUALS", "NOT_EQUALS", "GT", "GTE", "LT", "LTE",
				"CONTAINS", "NOT_CONTAINS", "IS_NULL", "IS_NOT_NULL",
			}, step.ConditionOperator) {
				return fmt.Errorf("%s has invalid condition operator", stepPath)
			}
			canonical = "STRING"
		}
	}
	return nil
}
