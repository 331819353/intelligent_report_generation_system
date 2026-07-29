package dataset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/platform/database"

	"github.com/jackc/pgx/v5"
)

const (
	dwdDIMNameValidationPromptVersion      = "warehouse-dim-name-validation-v1"
	dwdDIMDuplicateValidationPromptVersion = "warehouse-dim-duplicate-validation-v1"
)

// dwdDIMValidationCandidate represents one generated, unpublished DIM draft.
// Document is intentionally excluded from the first-pass request. The complete
// field contract is sent only for groups whose generated names collide.
type dwdDIMValidationCandidate struct {
	SourceDatasetID           string             `json:"sourceDatasetId"`
	SourceDatasetVersionID    string             `json:"sourceDatasetVersionId"`
	SourceRole                string             `json:"sourceRole"`
	DimensionDatasetID        string             `json:"dimensionDatasetId"`
	DimensionDatasetVersionID string             `json:"dimensionDatasetVersionId"`
	Name                      string             `json:"name"`
	Description               string             `json:"description"`
	OutputGrain               OutputGrain        `json:"outputGrain"`
	Fields                    []dwdPlanningField `json:"fields,omitempty"`
	Document                  Document           `json:"-"`
}

type dwdDIMDuplicateNameGroup struct {
	SourceDatasetVersionIDs []string `json:"sourceDatasetVersionIds"`
	Rationale               string   `json:"rationale"`
}

type dwdDIMNameValidationPlan struct {
	Groups []dwdDIMDuplicateNameGroup `json:"groups"`
}

type dwdDIMNameValidationCompletion struct {
	AIRequestID string
	Plan        dwdDIMNameValidationPlan
}

type dwdDIMDuplicateValidationInput struct {
	Candidates []dwdDIMValidationCandidate `json:"candidates"`
}

type dwdDIMDuplicateValidationPlan struct {
	Decision                        string               `json:"decision"`
	CanonicalSourceDatasetVersionID string               `json:"canonicalSourceDatasetVersionId"`
	FinalName                       string               `json:"finalName"`
	FinalDescription                string               `json:"finalDescription"`
	SeparateNames                   []dwdDIMSeparateName `json:"separateNames"`
	Rationale                       string               `json:"rationale"`
}

type dwdDIMSeparateName struct {
	SourceDatasetVersionID string `json:"sourceDatasetVersionId"`
	FinalName              string `json:"finalName"`
}

type dwdDIMDuplicateValidationCompletion struct {
	AIRequestID string
	Plan        dwdDIMDuplicateValidationPlan
}

type dwdDIMStageValidationResult struct {
	Stage                 dwdDimensionStageResult
	AIRequestID           string
	CheckpointCount       int
	ReusedCheckpointCount int
}

type dwdDIMDuplicateGroupResult struct {
	input      dwdDIMDuplicateValidationInput
	completion dwdDIMDuplicateValidationCompletion
	reused     bool
}

const dwdDIMNameValidationSystemPrompt = `你是企业数据仓库 DIM 草稿表名唯一性校验器。输入是同一领域内本轮已经生成并落库、但尚未发表的全部候选 DIM 草稿摘要。本步骤只检查表名，不读取字段，也不决定删除或合并。

要求：
1. 必须检查全部候选 DIM。把名称忽略大小写、首尾空白、连续空白和“表”等无语义格式差异后仍表示同一个 DIM 名称的候选放入同一组。
2. 每个重复组至少包含两个 sourceDatasetVersionId；一个候选最多只能出现在一个组中。
3. 不同实体即使名称相近也不得放在同一组；没有重名时 groups 返回空数组。
4. 只能复制输入中的精确 sourceDatasetVersionId，不能返回字段、SQL、DDL、Markdown 或额外解释。

输出只能是 JSON Schema 指定的对象。`

const dwdDIMDuplicateValidationSystemPrompt = `你是企业数据仓库 DIM 重名字段审校器。输入只包含上一步识别出的一个重名组，以及数据库刚读取的每张未发布 DIM 草稿的完整字段合同和输出粒度。

先比较实体含义、完整业务键、字段类型、属性函数依赖、生命周期和治理边界，再作决定：
1. 只有所有候选表示同一业务实体、完整粒度键相同且类型兼容时，decision 才能是 KEEP_ONE。
2. 实体、复合键、生命周期、更新节奏或治理边界不同，或仅有通用 id/code 无法证明同一实体时，decision 必须是 KEEP_SEPARATE。
3. KEEP_ONE 时选择字段合同更完整、来源更权威且粒度更稳定的候选作为 canonicalSourceDatasetVersionId，并给出唯一、明确的 finalName 和 finalDescription。DIM 必须保持一对一 ODS，平台只保留所选候选自身的字段，不会关联其他 ODS；rationale 要概括关键字段差异和选择依据。
4. KEEP_SEPARATE 时 canonicalSourceDatasetVersionId、finalName、finalDescription 返回空字符串；separateNames 必须逐一覆盖组内候选，为每张表给出互不重复且能明确区分实体/粒度的名称，并在 rationale 中简短说明不能合并的关键差异。
5. KEEP_ONE 时 separateNames 返回空数组。
6. 只能复制输入中的精确 sourceDatasetVersionId。不要返回字段映射、SQL、DDL、Markdown 或额外解释。

输出只能是 JSON Schema 指定的对象。`

func (planner *OrchestratedDWDModelingPlanner) ValidateDimensionNames(
	ctx context.Context,
	input dwdPlanningInput,
	candidates []dwdDIMValidationCandidate,
) (dwdDIMNameValidationCompletion, error) {
	if !planner.Configured() || !validDWDPlanningInput(input) ||
		len(candidates) == 0 {
		return dwdDIMNameValidationCompletion{}, errDWDModelingInvalid
	}
	type summary struct {
		SourceDatasetVersionID string `json:"sourceDatasetVersionId"`
		DimensionDatasetID     string `json:"dimensionDatasetId"`
		Name                   string `json:"name"`
		Description            string `json:"description"`
	}
	summaries := make([]summary, 0, len(candidates))
	versionIDs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.SourceDatasetVersionID == "" ||
			candidate.DimensionDatasetID == "" ||
			strings.TrimSpace(candidate.Name) == "" {
			return dwdDIMNameValidationCompletion{}, errDWDModelingInvalid
		}
		summaries = append(summaries, summary{
			SourceDatasetVersionID: candidate.SourceDatasetVersionID,
			DimensionDatasetID:     candidate.DimensionDatasetID,
			Name:                   strings.TrimSpace(candidate.Name),
			Description:            strings.TrimSpace(candidate.Description),
		})
		versionIDs = append(versionIDs, candidate.SourceDatasetVersionID)
	}
	raw, err := json.Marshal(struct {
		Domain     string    `json:"domain"`
		Candidates []summary `json:"candidates"`
	}{Domain: input.Domain, Candidates: summaries})
	if err != nil {
		return dwdDIMNameValidationCompletion{}, err
	}
	schema, err := dwdDIMNameValidationResponseSchema(versionIDs)
	if err != nil {
		return dwdDIMNameValidationCompletion{}, err
	}
	var plan dwdDIMNameValidationPlan
	_, requestID, err := planner.invokeDIMValidation(
		ctx, input, dwdDIMNameValidationPromptVersion,
		dwdDIMNameValidationSystemPrompt, raw, schema,
		`请只修复 DIM 表名重复组：检查全部候选；每组至少两个且候选不能跨组重复；只能复制输入的 sourceDatasetVersionId；没有重名时返回 {"groups":[]}。`,
		func(content []byte) error {
			var candidatePlan dwdDIMNameValidationPlan
			if err := decodeStrictDIMValidationJSON(
				content, &candidatePlan,
			); err != nil {
				return err
			}
			candidatePlan, err = canonicalizeDIMNameValidation(
				candidates, candidatePlan,
			)
			if err == nil {
				plan = candidatePlan
			}
			return err
		},
	)
	if err != nil {
		return dwdDIMNameValidationCompletion{}, err
	}
	return dwdDIMNameValidationCompletion{
		AIRequestID: requestID, Plan: plan,
	}, nil
}

func (planner *OrchestratedDWDModelingPlanner) ValidateDimensionDuplicates(
	ctx context.Context,
	input dwdPlanningInput,
	group dwdDIMDuplicateValidationInput,
) (dwdDIMDuplicateValidationCompletion, error) {
	if !planner.Configured() || !validDWDPlanningInput(input) ||
		len(group.Candidates) < 2 {
		return dwdDIMDuplicateValidationCompletion{}, errDWDModelingInvalid
	}
	versionIDs := make([]string, 0, len(group.Candidates))
	for index := range group.Candidates {
		candidate := group.Candidates[index]
		if candidate.SourceDatasetVersionID == "" ||
			strings.TrimSpace(candidate.Name) == "" ||
			len(candidate.Fields) == 0 ||
			len(candidate.OutputGrain.KeyFields) == 0 {
			return dwdDIMDuplicateValidationCompletion{}, errDWDModelingInvalid
		}
		versionIDs = append(versionIDs, candidate.SourceDatasetVersionID)
	}
	raw, err := json.Marshal(struct {
		Domain string                         `json:"domain"`
		Group  dwdDIMDuplicateValidationInput `json:"duplicateGroup"`
	}{Domain: input.Domain, Group: group})
	if err != nil {
		return dwdDIMDuplicateValidationCompletion{}, err
	}
	if len(raw) > 256<<10 {
		return dwdDIMDuplicateValidationCompletion{}, fmt.Errorf(
			"%w: DIM duplicate field context exceeds 256 KiB",
			errDWDModelingInvalid,
		)
	}
	schema, err := dwdDIMDuplicateValidationResponseSchema(versionIDs)
	if err != nil {
		return dwdDIMDuplicateValidationCompletion{}, err
	}
	var plan dwdDIMDuplicateValidationPlan
	_, requestID, err := planner.invokeDIMValidation(
		ctx, input, dwdDIMDuplicateValidationPromptVersion,
		dwdDIMDuplicateValidationSystemPrompt, raw, schema,
		`请只修复当前 DIM 重名组裁决：同实体且完整键与类型兼容才可 KEEP_ONE，否则 KEEP_SEPARATE。KEEP_ONE 必须选择字段更完整、来源更权威的一张输入候选，提供唯一名称和说明且 separateNames 为空；DIM 一对一 ODS，不能建议跨 ODS 关联或合并字段。KEEP_SEPARATE 的三个保留字段必须为空字符串，separateNames 必须逐表覆盖并给出互不重复的明确名称。`,
		func(content []byte) error {
			var candidatePlan dwdDIMDuplicateValidationPlan
			if err := decodeStrictDIMValidationJSON(
				content, &candidatePlan,
			); err != nil {
				return err
			}
			if err := validateDIMDuplicateValidation(
				group.Candidates, candidatePlan,
			); err != nil {
				return err
			}
			if candidatePlan.Decision == "KEEP_ONE" {
				if _, _, err := buildCanonicalDIMDocument(
					group.Candidates, candidatePlan,
				); err != nil {
					return fmt.Errorf(
						"%w: candidate DIM keys or field types cannot safely select one authoritative table",
						errDWDModelingInvalid,
					)
				}
			}
			plan = candidatePlan
			return nil
		},
	)
	if err != nil {
		return dwdDIMDuplicateValidationCompletion{}, err
	}
	return dwdDIMDuplicateValidationCompletion{
		AIRequestID: requestID, Plan: plan,
	}, nil
}

func (planner *OrchestratedDWDModelingPlanner) invokeDIMValidation(
	ctx context.Context,
	input dwdPlanningInput,
	promptVersion, systemPrompt string,
	raw []byte,
	schema aiplatform.JSONSchema,
	repairPrompt string,
	validate func([]byte) error,
) ([]byte, string, error) {
	temperature := 0.0
	request := aiplatform.ProviderRequest{
		Messages: []aiplatform.Message{
			{
				Role: aiplatform.MessageRoleSystem,
				Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText, Text: systemPrompt,
				}},
			},
			{
				Role: aiplatform.MessageRoleUser,
				Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText, Text: string(raw),
				}},
			},
		},
		ResponseSchema: schema, Temperature: &temperature, MaxOutputTokens: 3000,
	}
	callCtx, cancel := context.WithTimeout(
		ctx, planner.timeout*dwdStageInvocationAttempts,
	)
	defer cancel()
	invocation := aiplatform.Invocation{
		TenantID: input.TenantID, ActorID: input.ActorID,
		Purpose:       aiplatform.PurposeDatasetDAGGeneration,
		PromptVersion: promptVersion,
		ResourceType:  "DATASET_MODELING_SCOPE", ResourceID: input.ResourceID,
		Request: request,
	}
	baseMessages := append([]aiplatform.Message(nil), request.Messages...)
	var lastErr error
	for attempt := 0; attempt < dwdStageInvocationAttempts; attempt++ {
		result, invokeErr := planner.invoker.Invoke(callCtx, invocation)
		if invokeErr == nil {
			invokeErr = validate(result.ProviderResult.Content)
			if invokeErr == nil {
				return result.ProviderResult.Content, result.RequestID, nil
			}
		}
		lastErr = invokeErr
		if !repairableDWDModelingError(invokeErr) ||
			attempt == dwdStageInvocationAttempts-1 {
			return nil, "", invokeErr
		}
		if err := callCtx.Err(); err != nil {
			return nil, "", err
		}
		invocation.Request.Messages = dwdStageRepairMessages(
			baseMessages, result, invokeErr, repairPrompt,
			attempt == dwdStageInvocationAttempts-2,
		)
	}
	return nil, "", lastErr
}

func dwdDIMNameValidationResponseSchema(
	versionIDs []string,
) (aiplatform.JSONSchema, error) {
	versionIDs = uniqueSortedNonEmptyDWDStrings(versionIDs)
	if len(versionIDs) == 0 {
		return aiplatform.JSONSchema{}, errDWDModelingInvalid
	}
	raw, err := json.Marshal(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"groups"},
		"properties": map[string]any{
			"groups": map[string]any{
				"type": "array", "minItems": 0, "maxItems": len(versionIDs) / 2,
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{"sourceDatasetVersionIds", "rationale"},
					"properties": map[string]any{
						"sourceDatasetVersionIds": map[string]any{
							"type": "array", "minItems": 2,
							"maxItems": len(versionIDs), "uniqueItems": true,
							"items": map[string]any{
								"type": "string", "enum": versionIDs,
							},
						},
						"rationale": map[string]any{
							"type": "string", "minLength": 1, "maxLength": 1024,
						},
					},
				},
			},
		},
	})
	if err != nil {
		return aiplatform.JSONSchema{}, err
	}
	return aiplatform.JSONSchema{
		Name:        "warehouse_dim_name_validation",
		Description: "全部未发布 DIM 草稿的表名重复组",
		Schema:      raw,
	}, nil
}

func dwdDIMDuplicateValidationResponseSchema(
	versionIDs []string,
) (aiplatform.JSONSchema, error) {
	versionIDs = uniqueSortedNonEmptyDWDStrings(versionIDs)
	if len(versionIDs) < 2 {
		return aiplatform.JSONSchema{}, errDWDModelingInvalid
	}
	raw, err := json.Marshal(map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{
			"decision", "canonicalSourceDatasetVersionId",
			"finalName", "finalDescription", "separateNames", "rationale",
		},
		"properties": map[string]any{
			"decision": map[string]any{
				"type": "string", "enum": []string{"KEEP_ONE", "KEEP_SEPARATE"},
			},
			"canonicalSourceDatasetVersionId": map[string]any{
				"type": "string", "enum": append([]string{""}, versionIDs...),
			},
			"finalName": map[string]any{
				"type": "string", "maxLength": 256,
			},
			"finalDescription": map[string]any{
				"type": "string", "maxLength": 2048,
			},
			"separateNames": map[string]any{
				"type": "array", "minItems": 0, "maxItems": len(versionIDs),
				"items": map[string]any{
					"type": "object", "additionalProperties": false,
					"required": []string{
						"sourceDatasetVersionId", "finalName",
					},
					"properties": map[string]any{
						"sourceDatasetVersionId": map[string]any{
							"type": "string", "enum": versionIDs,
						},
						"finalName": map[string]any{
							"type": "string", "minLength": 1, "maxLength": 256,
						},
					},
				},
			},
			"rationale": map[string]any{
				"type": "string", "minLength": 1, "maxLength": 2048,
			},
		},
	})
	if err != nil {
		return aiplatform.JSONSchema{}, err
	}
	return aiplatform.JSONSchema{
		Name:        "warehouse_dim_duplicate_validation",
		Description: "重名 DIM 草稿的字段级保留一张或分别保留裁决",
		Schema:      raw,
	}, nil
}

func decodeStrictDIMValidationJSON(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > maxDWDModelingRepairContent {
		return errDWDModelingInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", errDWDModelingInvalid, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errDWDModelingInvalid
	}
	return nil
}

func canonicalizeDIMNameValidation(
	candidates []dwdDIMValidationCandidate,
	plan dwdDIMNameValidationPlan,
) (dwdDIMNameValidationPlan, error) {
	byVersion := make(map[string]dwdDIMValidationCandidate, len(candidates))
	for _, candidate := range candidates {
		if candidate.SourceDatasetVersionID == "" {
			return dwdDIMNameValidationPlan{}, errDWDModelingInvalid
		}
		byVersion[candidate.SourceDatasetVersionID] = candidate
	}
	seen := map[string]bool{}
	rationaleByName := map[string]string{}
	for _, group := range plan.Groups {
		ids := uniqueSortedNonEmptyDWDStrings(
			group.SourceDatasetVersionIDs,
		)
		if len(ids) < 2 || strings.TrimSpace(group.Rationale) == "" {
			return dwdDIMNameValidationPlan{}, errDWDModelingInvalid
		}
		baseName := ""
		for _, id := range ids {
			candidate, exists := byVersion[id]
			if !exists || seen[id] {
				return dwdDIMNameValidationPlan{}, errDWDModelingInvalid
			}
			name := normalizedDIMDuplicateName(candidate.Name)
			if name == "" || (baseName != "" && name != baseName) {
				return dwdDIMNameValidationPlan{}, errDWDModelingInvalid
			}
			baseName = name
			seen[id] = true
		}
		rationaleByName[baseName] = strings.TrimSpace(group.Rationale)
	}
	// The local contract cannot allow the LLM to omit an exact duplicate name.
	// Deterministic complete groups replace partial provider groups, so three
	// equal names can never be split into an accepted pair plus an orphan.
	exact := map[string][]string{}
	for _, candidate := range candidates {
		name := normalizedDIMDuplicateName(candidate.Name)
		if name != "" {
			exact[name] = append(exact[name], candidate.SourceDatasetVersionID)
		}
	}
	groups := make([]dwdDIMDuplicateNameGroup, 0, len(exact))
	for name, ids := range exact {
		ids = uniqueSortedNonEmptyDWDStrings(ids)
		if len(ids) < 2 {
			continue
		}
		rationale := rationaleByName[name]
		if rationale == "" {
			rationale = "生成后的 DIM 表名重复：" + name
		}
		groups = append(groups, dwdDIMDuplicateNameGroup{
			SourceDatasetVersionIDs: ids,
			Rationale:               rationale,
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		return strings.Join(groups[i].SourceDatasetVersionIDs, "\x00") <
			strings.Join(groups[j].SourceDatasetVersionIDs, "\x00")
	})
	return dwdDIMNameValidationPlan{Groups: groups}, nil
}

func validateDIMDuplicateValidation(
	candidates []dwdDIMValidationCandidate,
	plan dwdDIMDuplicateValidationPlan,
) error {
	allowed := map[string]bool{}
	for _, candidate := range candidates {
		allowed[candidate.SourceDatasetVersionID] = true
	}
	if strings.TrimSpace(plan.Rationale) == "" {
		return errDWDModelingInvalid
	}
	switch plan.Decision {
	case "KEEP_ONE":
		if !allowed[plan.CanonicalSourceDatasetVersionID] ||
			strings.TrimSpace(plan.FinalName) == "" ||
			strings.TrimSpace(plan.FinalDescription) == "" ||
			len(plan.SeparateNames) != 0 {
			return errDWDModelingInvalid
		}
	case "KEEP_SEPARATE":
		if plan.CanonicalSourceDatasetVersionID != "" ||
			plan.FinalName != "" || plan.FinalDescription != "" ||
			len(plan.SeparateNames) != len(candidates) {
			return errDWDModelingInvalid
		}
		seenVersions, seenNames := map[string]bool{}, map[string]bool{}
		for _, item := range plan.SeparateNames {
			name := normalizedDIMDuplicateName(item.FinalName)
			if !allowed[item.SourceDatasetVersionID] ||
				seenVersions[item.SourceDatasetVersionID] ||
				name == "" || seenNames[name] {
				return errDWDModelingInvalid
			}
			seenVersions[item.SourceDatasetVersionID] = true
			seenNames[name] = true
		}
	default:
		return errDWDModelingInvalid
	}
	return nil
}

func normalizedDIMDuplicateName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(
		" ", "", "\t", "", "\n", "", "\r", "",
		"（", "(", "）", ")", "_", "", "-", "",
	).Replace(value)
	value = strings.TrimSuffix(value, "数据表")
	value = strings.TrimSuffix(value, "维度表")
	value = strings.TrimSuffix(value, "dim表")
	value = strings.TrimSuffix(value, "表")
	value = strings.TrimSuffix(value, "维度")
	return strings.TrimSpace(value)
}

func uniqueSortedNonEmptyDWDStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func dwdDIMValidationGroupHash(ids []string) string {
	raw := strings.Join(uniqueSortedNonEmptyDWDStrings(ids), "\x00")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// validateGeneratedDIMStage runs only after prepareDIMStage committed every
// generated DIM as an unpublished draft. It first sends the complete name list
// to the validator, then loads/sends fields only for duplicate-name groups.
func (worker *DWDModelingWorker) validateGeneratedDIMStage(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	snapshotHash string,
	classifications []dwdLLMClassification,
	designs map[string]dwdLLMDimensionDesign,
	stage dwdDimensionStageResult,
	planner resumableDWDModelingPlanner,
) (dwdDIMStageValidationResult, error) {
	result := dwdDIMStageValidationResult{Stage: stage}
	candidates, err := worker.loadGeneratedDIMValidationCandidates(
		ctx, claim, workerID, input, snapshotHash, classifications, designs,
	)
	if err != nil {
		return dwdDIMStageValidationResult{}, err
	}
	if len(candidates) < 2 {
		return result, nil
	}
	nameCompletion, reused, err := worker.loadDIMNameValidationCheckpoint(
		ctx, claim, workerID, input, snapshotHash, candidates,
	)
	if err != nil {
		return dwdDIMStageValidationResult{}, err
	}
	if !reused {
		nameCompletion, err = planner.ValidateDimensionNames(
			ctx, input, candidates,
		)
		if err != nil {
			return dwdDIMStageValidationResult{}, err
		}
		if err := worker.saveDWDModelingCheckpoint(
			ctx, claim, workerID, input, snapshotHash,
			"DIM_NAME_VALIDATION", claim.TriggerDatasetVersionID,
			dwdDIMNameValidationPromptVersion, nameCompletion.AIRequestID,
			nameCompletion.Plan,
		); err != nil {
			return dwdDIMStageValidationResult{}, err
		}
	}
	result.CheckpointCount++
	if reused {
		result.ReusedCheckpointCount++
	}
	result.AIRequestID = nameCompletion.AIRequestID
	if len(nameCompletion.Plan.Groups) == 0 {
		return result, nil
	}

	candidateByVersion := make(
		map[string]dwdDIMValidationCandidate, len(candidates),
	)
	for _, candidate := range candidates {
		candidateByVersion[candidate.SourceDatasetVersionID] = candidate
	}
	tasks := make(
		[]func(context.Context) (dwdDIMDuplicateGroupResult, error),
		0, len(nameCompletion.Plan.Groups),
	)
	for _, duplicateGroup := range nameCompletion.Plan.Groups {
		group := duplicateGroup
		tasks = append(tasks, func(taskCtx context.Context) (dwdDIMDuplicateGroupResult, error) {
			groupInput := dwdDIMDuplicateValidationInput{
				Candidates: make(
					[]dwdDIMValidationCandidate, 0,
					len(group.SourceDatasetVersionIDs),
				),
			}
			for _, versionID := range group.SourceDatasetVersionIDs {
				candidate, exists := candidateByVersion[versionID]
				if !exists {
					return dwdDIMDuplicateGroupResult{}, errDWDModelingInvalid
				}
				groupInput.Candidates = append(
					groupInput.Candidates, candidate,
				)
			}
			completion, checkpointReused, err :=
				worker.loadDIMDuplicateValidationCheckpoint(
					taskCtx, claim, workerID, input, snapshotHash, groupInput,
				)
			if err != nil {
				return dwdDIMDuplicateGroupResult{}, err
			}
			if !checkpointReused {
				completion, err = planner.ValidateDimensionDuplicates(
					taskCtx, input, groupInput,
				)
				if err != nil {
					return dwdDIMDuplicateGroupResult{}, err
				}
				subjectID := group.SourceDatasetVersionIDs[0]
				if err := worker.saveDWDModelingCheckpoint(
					taskCtx, claim, workerID, input, snapshotHash,
					"DIM_DUPLICATE_VALIDATION", subjectID,
					dwdDIMDuplicateValidationPromptVersion,
					completion.AIRequestID, completion.Plan,
				); err != nil {
					return dwdDIMDuplicateGroupResult{}, err
				}
			}
			return dwdDIMDuplicateGroupResult{
				input: groupInput, completion: completion,
				reused: checkpointReused,
			}, nil
		})
	}
	groupResults, err := runBoundedDWDTasks(
		ctx, dwdFactDesignConcurrency, tasks,
	)
	if err != nil {
		return dwdDIMStageValidationResult{}, err
	}
	for _, group := range groupResults {
		result.CheckpointCount++
		if group.reused {
			result.ReusedCheckpointCount++
		}
		result.AIRequestID = group.completion.AIRequestID
	}
	reconciled, err := worker.applyDIMDuplicateValidations(
		ctx, claim, workerID, input, snapshotHash,
		classifications, stage, groupResults,
	)
	if err != nil {
		return dwdDIMStageValidationResult{}, err
	}
	result.Stage = reconciled
	return result, nil
}

func (worker *DWDModelingWorker) loadGeneratedDIMValidationCandidates(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	snapshotHash string,
	classifications []dwdLLMClassification,
	designs map[string]dwdLLMDimensionDesign,
) ([]dwdDIMValidationCandidate, error) {
	result := []dwdDIMValidationCandidate{}
	err := database.WithTenantTx(
		ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
			if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
				return err
			}
			if err := validateDWDPlanningSnapshotTx(
				ctx, tx, input.DomainID, snapshotHash,
			); err != nil {
				return err
			}
			assets, err := loadPublishedODSAssetsTx(ctx, tx)
			if err != nil {
				return err
			}
			sourceByVersion := map[string]dwdODSAsset{}
			for _, asset := range assets {
				if asset.DomainID == input.DomainID {
					sourceByVersion[asset.VersionID] = asset
				}
			}
			for _, classification := range classifications {
				if !classificationProducesDimension(classification) {
					continue
				}
				source, exists :=
					sourceByVersion[classification.DatasetVersionID]
				_, designed := designs[classification.DatasetVersionID]
				if !exists || !designed {
					continue
				}
				var dimDatasetID, dimVersionID string
				var raw json.RawMessage
				err = tx.QueryRow(ctx, `SELECT
						output.dim_dataset_id::text,
						draft.id::text,
						draft.dsl_json
					FROM platform.dim_modeling_outputs AS output
					JOIN platform.datasets AS dataset
					  ON dataset.id=output.dim_dataset_id
					 AND dataset.tenant_id=output.tenant_id
					 AND dataset.deleted_at IS NULL
					JOIN platform.dataset_versions AS draft
					  ON draft.id=dataset.current_draft_version_id
					 AND draft.dataset_id=dataset.id
					 AND draft.tenant_id=dataset.tenant_id
					 AND draft.status='DRAFT'
					 AND draft.layer='DIM'
					WHERE output.source_dataset_id=$1::uuid`,
					source.DatasetID,
				).Scan(&dimDatasetID, &dimVersionID, &raw)
				if errors.Is(err, pgx.ErrNoRows) {
					continue
				}
				if err != nil {
					return err
				}
				document, err := DecodeAndNormalize(raw)
				if err != nil {
					return err
				}
				if len(document.Nodes) != 1 ||
					document.Nodes[0].DatasetVersionID != source.VersionID ||
					len(document.Joins) != 0 {
					return errDWDModelingInvalid
				}
				fields := make(
					[]dwdPlanningField, 0, len(document.Fields),
				)
				for _, field := range document.Fields {
					fields = append(fields, dwdPlanningField{
						Code: field.Code, Name: field.Name,
						Description: field.Description, Role: field.Role,
						CanonicalType: field.CanonicalType,
						SemanticType:  field.SemanticType,
						Nullable:      field.Nullable,
					})
				}
				result = append(result, dwdDIMValidationCandidate{
					SourceDatasetID:           source.DatasetID,
					SourceDatasetVersionID:    source.VersionID,
					SourceRole:                classification.Role,
					DimensionDatasetID:        dimDatasetID,
					DimensionDatasetVersionID: dimVersionID,
					Name:                      document.Dataset.Name,
					Description:               document.Dataset.Description,
					OutputGrain:               document.OutputGrain,
					Fields:                    fields, Document: document,
				})
			}
			return nil
		},
	)
	sort.Slice(result, func(i, j int) bool {
		return result[i].SourceDatasetVersionID <
			result[j].SourceDatasetVersionID
	})
	return result, err
}

func (worker *DWDModelingWorker) loadDIMNameValidationCheckpoint(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	snapshotHash string,
	candidates []dwdDIMValidationCandidate,
) (dwdDIMNameValidationCompletion, bool, error) {
	var result dwdDIMNameValidationCompletion
	found := false
	err := database.WithTenantTx(
		ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
			if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
				return err
			}
			var raw json.RawMessage
			var payloadHash string
			err := tx.QueryRow(ctx, `SELECT payload_json,payload_hash,
					ai_request_id::text
				FROM platform.dwd_modeling_checkpoints
				WHERE job_id=$1::uuid
				  AND checkpoint_kind='DIM_NAME_VALIDATION'
				  AND subject_dataset_version_id=$2::uuid
				  AND snapshot_hash=$3 AND prompt_version=$4`,
				claim.ID, claim.TriggerDatasetVersionID, snapshotHash,
				dwdDIMNameValidationPromptVersion,
			).Scan(&raw, &payloadHash, &result.AIRequestID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := validateDWDCheckpointHash(raw, payloadHash); err != nil {
				return err
			}
			if err := decodeStrictDIMValidationJSON(
				raw, &result.Plan,
			); err != nil {
				return err
			}
			result.Plan, err = canonicalizeDIMNameValidation(
				candidates, result.Plan,
			)
			if err != nil {
				return err
			}
			found = true
			return nil
		},
	)
	return result, found, err
}

func (worker *DWDModelingWorker) loadDIMDuplicateValidationCheckpoint(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	snapshotHash string,
	group dwdDIMDuplicateValidationInput,
) (dwdDIMDuplicateValidationCompletion, bool, error) {
	var result dwdDIMDuplicateValidationCompletion
	found := false
	ids := make([]string, 0, len(group.Candidates))
	for _, candidate := range group.Candidates {
		ids = append(ids, candidate.SourceDatasetVersionID)
	}
	ids = uniqueSortedNonEmptyDWDStrings(ids)
	if len(ids) < 2 {
		return result, false, errDWDModelingInvalid
	}
	err := database.WithTenantTx(
		ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
			if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
				return err
			}
			var raw json.RawMessage
			var payloadHash string
			err := tx.QueryRow(ctx, `SELECT payload_json,payload_hash,
					ai_request_id::text
				FROM platform.dwd_modeling_checkpoints
				WHERE job_id=$1::uuid
				  AND checkpoint_kind='DIM_DUPLICATE_VALIDATION'
				  AND subject_dataset_version_id=$2::uuid
				  AND snapshot_hash=$3 AND prompt_version=$4`,
				claim.ID, ids[0], snapshotHash,
				dwdDIMDuplicateValidationPromptVersion,
			).Scan(&raw, &payloadHash, &result.AIRequestID)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := validateDWDCheckpointHash(raw, payloadHash); err != nil {
				return err
			}
			if err := decodeStrictDIMValidationJSON(
				raw, &result.Plan,
			); err != nil {
				return err
			}
			if err := validateDIMDuplicateValidation(
				group.Candidates, result.Plan,
			); err != nil {
				return err
			}
			if result.Plan.Decision == "KEEP_ONE" {
				if _, _, err := buildCanonicalDIMDocument(
					group.Candidates, result.Plan,
				); err != nil {
					return err
				}
			}
			found = true
			return nil
		},
	)
	return result, found, err
}

func (worker *DWDModelingWorker) applyDIMDuplicateValidations(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	snapshotHash string,
	classifications []dwdLLMClassification,
	stage dwdDimensionStageResult,
	groups []dwdDIMDuplicateGroupResult,
) (dwdDimensionStageResult, error) {
	for _, group := range groups {
		switch group.completion.Plan.Decision {
		case "KEEP_ONE":
			if err := worker.applyDIMKeepOneValidation(
				ctx, claim, workerID, input, snapshotHash,
				group.input, group.completion.Plan,
			); err != nil {
				return dwdDimensionStageResult{}, err
			}
			canonical := group.completion.Plan.CanonicalSourceDatasetVersionID
			for _, candidate := range group.input.Candidates {
				action := "RETIRED"
				if candidate.SourceDatasetVersionID == canonical {
					action = "KEPT"
					stage.Updated++
				} else {
					stage.Retired++
				}
				stage.Items = append(stage.Items, dwdModelingResultItem{
					Layer:           string(LayerDIM),
					SourceDatasetID: candidate.SourceDatasetID,
					SourceVersionID: candidate.SourceDatasetVersionID,
					DatasetID:       candidate.DimensionDatasetID,
					Action:          action, Reason: "DIM_FIELD_VALIDATION_KEEP_ONE",
					ErrorMessage: group.completion.Plan.Rationale,
				})
			}
		case "KEEP_SEPARATE":
			if err := worker.applyDIMSeparateNames(
				ctx, claim, workerID, input, snapshotHash,
				group.input, group.completion.Plan,
			); err != nil {
				return dwdDimensionStageResult{}, err
			}
			for _, candidate := range group.input.Candidates {
				stage.Updated++
				stage.Items = append(stage.Items, dwdModelingResultItem{
					Layer:           string(LayerDIM),
					SourceDatasetID: candidate.SourceDatasetID,
					SourceVersionID: candidate.SourceDatasetVersionID,
					DatasetID:       candidate.DimensionDatasetID,
					Action:          "RENAMED",
					Reason:          "SAME_NAME_DIFFERENT_ENTITY",
				})
			}
		default:
			return dwdDimensionStageResult{}, errDWDModelingInvalid
		}
	}
	resolved, err := worker.resolveModeledDIMStage(
		ctx, claim, workerID, input, snapshotHash, classifications,
	)
	if err != nil {
		return dwdDimensionStageResult{}, err
	}
	resolved.Items = stage.Items
	resolved.Created = stage.Created
	resolved.Updated = stage.Updated
	resolved.Retired = stage.Retired
	resolved.Skipped = stage.Skipped
	resolved.FailedDesignCount = stage.FailedDesignCount
	resolved.FailedSourceDatasets = stage.FailedSourceDatasets
	return resolved, nil
}

func (worker *DWDModelingWorker) applyDIMKeepOneValidation(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	snapshotHash string,
	group dwdDIMDuplicateValidationInput,
	plan dwdDIMDuplicateValidationPlan,
) error {
	document, inputHash, err := buildCanonicalDIMDocument(
		group.Candidates, plan,
	)
	if err != nil {
		return err
	}
	prepared, err := Prepare(mustMarshalDWDDocument(document))
	if err != nil {
		return fmt.Errorf("%w: canonical DIM DAG is invalid: %v",
			errDWDModelingInvalid, err)
	}
	canonical, exists := dimValidationCandidateBySourceVersion(
		group.Candidates, plan.CanonicalSourceDatasetVersionID,
	)
	if !exists {
		return errDWDModelingInvalid
	}
	return database.WithTenantTx(
		ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
			if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
				return err
			}
			if err := validateDWDPlanningSnapshotTx(
				ctx, tx, input.DomainID, snapshotHash,
			); err != nil {
				return err
			}
			// Retire only untouched, unpublished generated duplicates. Any
			// governance or downstream use aborts the whole transaction.
			for _, candidate := range group.Candidates {
				if candidate.SourceDatasetVersionID ==
					canonical.SourceDatasetVersionID {
					continue
				}
				signature, strong := dwdDIMDocumentEntitySignature(
					candidate.Document,
				)
				if !strong {
					return ErrConflict
				}
				retiredID, mapped, retired, err :=
					worker.retireSuppressedGeneratedDIMDraftTx(
						ctx, tx, claim,
						dwdODSAsset{
							DatasetID: candidate.SourceDatasetID,
							VersionID: candidate.SourceDatasetVersionID,
						},
						map[string]bool{signature: true},
					)
				if err != nil {
					return err
				}
				if !mapped || !retired ||
					retiredID != candidate.DimensionDatasetID {
					return ErrConflict
				}
			}
			if err := worker.replaceValidatedDIMDraftTx(
				ctx, tx, claim, canonical.SourceDatasetID,
				canonical.DimensionDatasetID, input.Domain, inputHash,
				prepared,
			); err != nil {
				return err
			}
			for _, candidate := range group.Candidates {
				if candidate.SourceDatasetVersionID ==
					canonical.SourceDatasetVersionID {
					continue
				}
				if _, err := tx.Exec(ctx, `INSERT INTO platform.dim_modeling_suppressions(
						tenant_id,suppressed_source_dataset_id,
						canonical_source_dataset_id,canonical_dim_dataset_id,
						last_job_id,last_input_hash,rationale
					) VALUES($1,$2,$3,$4,$5,$6,$7)
					ON CONFLICT(tenant_id,suppressed_source_dataset_id)
					DO UPDATE SET
						canonical_source_dataset_id=
							excluded.canonical_source_dataset_id,
						canonical_dim_dataset_id=
							excluded.canonical_dim_dataset_id,
						last_job_id=excluded.last_job_id,
						last_input_hash=excluded.last_input_hash,
						rationale=excluded.rationale`,
					claim.TenantID, candidate.SourceDatasetID,
					canonical.SourceDatasetID, canonical.DimensionDatasetID,
					claim.ID, inputHash, plan.Rationale,
				); err != nil {
					return err
				}
			}
			return nil
		},
	)
}

func (worker *DWDModelingWorker) applyDIMSeparateNames(
	ctx context.Context,
	claim dwdModelingClaim,
	workerID string,
	input dwdPlanningInput,
	snapshotHash string,
	group dwdDIMDuplicateValidationInput,
	plan dwdDIMDuplicateValidationPlan,
) error {
	nameByVersion := map[string]string{}
	for _, item := range plan.SeparateNames {
		nameByVersion[item.SourceDatasetVersionID] =
			strings.TrimSpace(item.FinalName)
	}
	return database.WithTenantTx(
		ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
			if err := validateDWDClaimTx(ctx, tx, claim, workerID); err != nil {
				return err
			}
			if err := validateDWDPlanningSnapshotTx(
				ctx, tx, input.DomainID, snapshotHash,
			); err != nil {
				return err
			}
			seenDatasets := map[string]bool{}
			for _, candidate := range group.Candidates {
				if seenDatasets[candidate.DimensionDatasetID] {
					// One DIM can only own one ODS source.
					return ErrConflict
				}
				seenDatasets[candidate.DimensionDatasetID] = true
				if _, err := tx.Exec(ctx,
					`DELETE FROM platform.dim_modeling_suppressions
					 WHERE suppressed_source_dataset_id=$1::uuid`,
					candidate.SourceDatasetID,
				); err != nil {
					return err
				}
				document := candidate.Document
				document.Dataset.Name =
					nameByVersion[candidate.SourceDatasetVersionID]
				prepared, err := Prepare(mustMarshalDWDDocument(document))
				if err != nil {
					return err
				}
				hashRaw, _ := json.Marshal(struct {
					Source string `json:"source"`
					Name   string `json:"name"`
				}{
					Source: candidate.SourceDatasetVersionID,
					Name:   document.Dataset.Name,
				})
				sum := sha256.Sum256(hashRaw)
				if err := worker.replaceValidatedDIMDraftTx(
					ctx, tx, claim, candidate.SourceDatasetID,
					candidate.DimensionDatasetID, input.Domain,
					hex.EncodeToString(sum[:]), prepared,
				); err != nil {
					return err
				}
			}
			return nil
		},
	)
}

func buildCanonicalDIMDocument(
	candidates []dwdDIMValidationCandidate,
	plan dwdDIMDuplicateValidationPlan,
) (Document, string, error) {
	if err := validateDIMDuplicateValidation(candidates, plan); err != nil ||
		plan.Decision != "KEEP_ONE" {
		return Document{}, "", errDWDModelingInvalid
	}
	canonical, exists := dimValidationCandidateBySourceVersion(
		candidates, plan.CanonicalSourceDatasetVersionID,
	)
	if !exists || len(canonical.Document.Nodes) != 1 ||
		len(canonical.Document.OutputGrain.KeyFields) == 0 {
		return Document{}, "", errDWDModelingInvalid
	}
	canonicalSignature, strong := dwdDIMDocumentEntitySignature(
		canonical.Document,
	)
	if !strong {
		return Document{}, "", errDWDModelingInvalid
	}
	baseName := normalizedDIMDuplicateName(canonical.Name)
	canonicalFields := dwdDIMFieldsByFoldedCode(canonical.Document.Fields)
	maxAuthority := -1
	maxFieldsAtAuthority := -1
	for _, candidate := range candidates {
		authority := dwdDimensionAuthorityPriority(candidate.SourceRole)
		if authority > maxAuthority {
			maxAuthority = authority
			maxFieldsAtAuthority = len(candidate.Document.Fields)
			continue
		}
		if authority == maxAuthority &&
			len(candidate.Document.Fields) > maxFieldsAtAuthority {
			maxFieldsAtAuthority = len(candidate.Document.Fields)
		}
	}
	if dwdDimensionAuthorityPriority(canonical.SourceRole) != maxAuthority ||
		len(canonical.Document.Fields) < maxFieldsAtAuthority {
		return Document{}, "", errDWDModelingInvalid
	}
	for _, candidate := range candidates {
		if candidate.SourceDatasetVersionID ==
			canonical.SourceDatasetVersionID {
			continue
		}
		if normalizedDIMDuplicateName(candidate.Name) != baseName ||
			len(candidate.Document.Nodes) != 1 ||
			len(candidate.Document.Joins) != 0 {
			return Document{}, "", errDWDModelingInvalid
		}
		signature, strong := dwdDIMDocumentEntitySignature(
			candidate.Document,
		)
		if !strong || signature != canonicalSignature {
			return Document{}, "", errDWDModelingInvalid
		}
		fields := dwdDIMFieldsByFoldedCode(candidate.Document.Fields)
		for code, field := range fields {
			if existing, duplicate := canonicalFields[code]; duplicate &&
				!dwdCanonicalTypesCompatible(
					existing.CanonicalType, field.CanonicalType,
				) {
				return Document{}, "", errDWDModelingInvalid
			}
		}
	}
	document := canonical.Document
	document.Dataset.Name = strings.TrimSpace(plan.FinalName)
	document.Dataset.Description = strings.TrimSpace(plan.FinalDescription)
	// The selected DIM remains exactly one DATASET node backed by one ODS.
	if len(document.Nodes) != 1 || len(document.Joins) != 0 {
		return Document{}, "", errDWDModelingInvalid
	}
	raw, err := json.Marshal(struct {
		Sources []string                      `json:"comparedSources"`
		Plan    dwdDIMDuplicateValidationPlan `json:"plan"`
	}{
		Sources: func() []string {
			ids := make([]string, 0, len(candidates))
			for _, candidate := range candidates {
				ids = append(ids, candidate.SourceDatasetVersionID)
			}
			return uniqueSortedNonEmptyDWDStrings(ids)
		}(),
		Plan: plan,
	})
	if err != nil {
		return Document{}, "", err
	}
	sum := sha256.Sum256(raw)
	return document, hex.EncodeToString(sum[:]), nil
}

func (worker *DWDModelingWorker) replaceValidatedDIMDraftTx(
	ctx context.Context,
	tx pgx.Tx,
	claim dwdModelingClaim,
	sourceDatasetID, dimDatasetID, domain, inputHash string,
	prepared Prepared,
) error {
	var lastSchemaHash string
	if err := tx.QueryRow(ctx, `SELECT last_generated_schema_hash
		FROM platform.dim_modeling_outputs
		WHERE source_dataset_id=$1::uuid
		  AND dim_dataset_id=$2::uuid
		FOR UPDATE`, sourceDatasetID, dimDatasetID).
		Scan(&lastSchemaHash); err != nil {
		return err
	}
	var aggregateVersion, draftRecordVersion int64
	var draftVersionID, currentSchemaHash, layer string
	err := tx.QueryRow(ctx, `SELECT
			dataset.version,dataset.current_draft_version_id::text,
			draft.record_version,draft.schema_hash,draft.layer
		FROM platform.datasets AS dataset
		JOIN platform.dataset_versions AS draft
		  ON draft.id=dataset.current_draft_version_id
		 AND draft.dataset_id=dataset.id
		 AND draft.tenant_id=dataset.tenant_id
		 AND draft.status='DRAFT'
		WHERE dataset.id=$1::uuid AND dataset.deleted_at IS NULL
		FOR UPDATE OF dataset,draft`, dimDatasetID).
		Scan(
			&aggregateVersion, &draftVersionID, &draftRecordVersion,
			&currentSchemaHash, &layer,
		)
	if err != nil {
		return err
	}
	if layer != string(LayerDIM) || currentSchemaHash != lastSchemaHash {
		return ErrConflict
	}
	if currentSchemaHash != prepared.DSLHash {
		if tag, err := tx.Exec(ctx, `UPDATE platform.dataset_versions SET
				layer='DIM',dsl_json=$1,schema_hash=$2,logical_plan_json=$3,
				plan_hash=$4,record_version=record_version+1,updated_by=$5
			WHERE id=$6::uuid AND status='DRAFT' AND record_version=$7`,
			prepared.DSLJSON, prepared.DSLHash, prepared.LogicalPlanJSON,
			prepared.PlanHash, claim.ActorID, draftVersionID,
			draftRecordVersion,
		); err != nil {
			return err
		} else if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		if tag, err := tx.Exec(ctx, `UPDATE platform.datasets SET
				code=$1,name=$2,description=$3,dataset_type=$4,layer='DIM',
				version=version+1,updated_by=$5
			WHERE id=$6::uuid AND version=$7`,
			prepared.Document.Dataset.Code, prepared.Document.Dataset.Name,
			prepared.Document.Dataset.Description,
			prepared.Document.Dataset.Type, claim.ActorID,
			dimDatasetID, aggregateVersion,
		); err != nil {
			return err
		} else if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		if err := replaceDerived(
			ctx, tx, claim.TenantID, dimDatasetID, draftVersionID,
			prepared.Document, true,
		); err != nil {
			return err
		}
		if err := insertDraftRevisionTx(
			ctx, tx, claim.TenantID, dimDatasetID, claim.ActorID,
			draftVersionID, aggregateVersion+1, draftRecordVersion+1,
			"SAVE", "", prepared,
		); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.dim_modeling_outputs SET
			domain_key=$1,last_job_id=$2,last_input_hash=$3,
			last_generated_schema_hash=$4,last_action=$5
		WHERE dim_dataset_id=$6::uuid`,
		domain, claim.ID, inputHash, prepared.DSLHash,
		func() string {
			if currentSchemaHash == prepared.DSLHash {
				return "UNCHANGED"
			}
			return "UPDATED"
		}(),
		dimDatasetID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'AUTO_DIM_VALIDATE','DATASET',$3,
			jsonb_build_object(
			  'sourceDatasetId',$4::text,
			  'dwdModelingJobId',$5::text,
			  'dslHash',$6::text
			))`,
		claim.TenantID, claim.ActorID, dimDatasetID,
		sourceDatasetID, claim.ID, prepared.DSLHash,
	); err != nil {
		return err
	}
	return nil
}

func dwdDIMDocumentEntitySignature(
	document Document,
) (string, bool) {
	table := dwdPlanningTable{
		OutputGrain: document.OutputGrain,
		Fields:      make([]dwdPlanningField, 0, len(document.Fields)),
	}
	for _, field := range document.Fields {
		table.Fields = append(table.Fields, dwdPlanningField{
			Code: field.Code, Role: field.Role,
			CanonicalType: field.CanonicalType,
			SemanticType:  field.SemanticType, Nullable: field.Nullable,
		})
	}
	return dwdDimensionEntitySignature(
		table, document.OutputGrain.KeyFields,
	)
}

func dimValidationCandidateBySourceVersion(
	candidates []dwdDIMValidationCandidate,
	versionID string,
) (dwdDIMValidationCandidate, bool) {
	for _, candidate := range candidates {
		if candidate.SourceDatasetVersionID == versionID {
			return candidate, true
		}
	}
	return dwdDIMValidationCandidate{}, false
}

func dwdDIMFieldsByFoldedCode(fields []Field) map[string]Field {
	result := make(map[string]Field, len(fields))
	for _, field := range fields {
		result[strings.ToLower(strings.TrimSpace(field.Code))] = field
	}
	return result
}
