package reportai

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type PublishGateSummary struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Status     string   `json:"status"`
	IssueCodes []string `json:"issueCodes"`
	Summary    string   `json:"summary"`
}

type PublishImpactSummary struct {
	VisibleCount      int `json:"visibleCount"`
	EditableCount     int `json:"editableCount"`
	SubscriptionCount int `json:"subscriptionCount"`
	ActiveShareCount  int `json:"activeShareCount"`
}

type PublishReviewRequest struct {
	ReportTitle      string               `json:"reportTitle"`
	SourceRevisionNo int64                `json:"sourceRevisionNo"`
	TargetVersionNo  int                  `json:"targetVersionNo"`
	DefinitionHash   string               `json:"definitionHash"`
	Gates            []PublishGateSummary `json:"gates"`
	Impact           PublishImpactSummary `json:"impact"`
	DependencyRefs   []string             `json:"dependencyRefs"`
	BlockerCodes     []string             `json:"blockerCodes"`
	WarningCodes     []string             `json:"warningCodes"`
}

type PublishRisk struct {
	Code            string `json:"code"`
	Title           string `json:"title"`
	Explanation     string `json:"explanation"`
	Evidence        string `json:"evidence"`
	SuggestedAction string `json:"suggestedAction"`
}

type PublishReview struct {
	Recommendation string        `json:"recommendation"`
	Headline       string        `json:"headline"`
	Summary        string        `json:"summary"`
	Risks          []PublishRisk `json:"risks"`
	// Source 标明评审文本的来源：AI 表示由模型叙述，DETERMINISTIC 表示未配置
	// 模型提供方（或模型评审失败）时，直接由确定性发布门禁生成。发布决策在两
	// 种来源下完全一致，模型永远不能放宽门禁。
	Source string `json:"source,omitempty"`
}

const (
	PublishReviewSourceAI            = "AI"
	PublishReviewSourceDeterministic = "DETERMINISTIC"
)

// PublishRecommendation 由确定性门禁唯一决定：有阻断项则 BLOCK，只有告警项
// 则 CONDITIONAL，否则 ALLOW。模型评审必须与该结论一致，不得放宽。
func PublishRecommendation(request PublishReviewRequest) string {
	switch {
	case len(request.BlockerCodes) > 0:
		return "BLOCK"
	case len(request.WarningCodes) > 0:
		return "CONDITIONAL"
	default:
		return "ALLOW"
	}
}

// DeterministicPublishReview 直接由发布前检查结果生成评审结论，用于未配置 LLM
// 提供方或模型评审失败的场景。它保证平台在没有模型的情况下依然可以完成发布，
// 且返回文本中不含任何模型生成内容——每条风险都由门禁问题码逐条派生。
func DeterministicPublishReview(request PublishReviewRequest) PublishReview {
	gateByCode := map[string]PublishGateSummary{}
	for _, gate := range request.Gates {
		for _, code := range gate.IssueCodes {
			if _, exists := gateByCode[code]; !exists {
				gateByCode[code] = gate
			}
		}
	}
	hash := request.DefinitionHash
	if len(hash) > 12 {
		hash = hash[:12]
	}
	seen := map[string]struct{}{}
	risks := make([]PublishRisk, 0, len(request.BlockerCodes)+len(request.WarningCodes))
	collect := func(codes []string, severity, action string) {
		for _, code := range codes {
			code = strings.TrimSpace(code)
			if code == "" {
				continue
			}
			if _, exists := seen[code]; exists {
				continue
			}
			seen[code] = struct{}{}
			gate := gateByCode[code]
			label := strings.TrimSpace(gate.Label)
			if label == "" {
				label = "发布检查"
			}
			explanation := strings.TrimSpace(gate.Summary)
			if explanation == "" {
				explanation = "发布前检查记录了该问题码，请在编辑器中核对对应组件。"
			}
			risks = append(risks, PublishRisk{
				Code:        code,
				Title:       label + "·" + severity,
				Explanation: explanation,
				Evidence: fmt.Sprintf("门禁 %s 状态 %s，问题码 %s；来源修订 r%d，定义哈希 %s。",
					gate.ID, gate.Status, code, request.SourceRevisionNo, hash),
				SuggestedAction: action,
			})
		}
	}
	collect(request.BlockerCodes, "阻断", "返回编辑器修复后重新发起发布评审。")
	collect(request.WarningCodes, "需确认", "确认该问题在业务上可接受后再勾选并发布。")

	recommendation := PublishRecommendation(request)
	headline := fmt.Sprintf("确定性门禁全部通过，可发布 v%d", request.TargetVersionNo)
	switch recommendation {
	case "BLOCK":
		headline = fmt.Sprintf("发布被 %d 项确定性门禁阻断", len(request.BlockerCodes))
	case "CONDITIONAL":
		headline = fmt.Sprintf("%d 项门禁需要人工确认后方可发布", len(request.WarningCodes))
	}
	return PublishReview{
		Recommendation: recommendation,
		Headline:       headline,
		Summary: fmt.Sprintf(
			"当前未启用模型评审，以下结论完全来自确定性发布前检查：共 %d 项门禁，阻断 %d 项、告警 %d 项；"+
				"来源修订 r%d，目标版本 v%d，固定依赖 %d 项；影响范围为可见 %d 人、可编辑 %d 人、订阅 %d 个、有效分享 %d 个。",
			len(request.Gates), len(request.BlockerCodes), len(request.WarningCodes),
			request.SourceRevisionNo, request.TargetVersionNo, len(request.DependencyRefs),
			request.Impact.VisibleCount, request.Impact.EditableCount,
			request.Impact.SubscriptionCount, request.Impact.ActiveShareCount,
		),
		Risks:  risks,
		Source: PublishReviewSourceDeterministic,
	}
}

type PublishReviewGenerator interface {
	ReviewPublication(context.Context, PublishReviewRequest) (PublishReview, error)
}

func ReviewPublication(ctx context.Context, generator PublishReviewGenerator, request PublishReviewRequest) (PublishReview, error) {
	if generator == nil || strings.TrimSpace(request.ReportTitle) == "" || request.SourceRevisionNo < 0 ||
		request.TargetVersionNo < 1 || len(request.DefinitionHash) != 64 || len(request.Gates) != 6 ||
		len(request.DependencyRefs) > 100 || len(request.BlockerCodes) > 100 || len(request.WarningCodes) > 100 {
		return PublishReview{}, errors.New("report AI publication review request is invalid")
	}
	review, err := generator.ReviewPublication(ctx, request)
	if err != nil {
		return PublishReview{}, err
	}
	if err := validatePublishReview(request, review); err != nil {
		return PublishReview{}, err
	}
	review.Source = PublishReviewSourceAI
	return review, nil
}

func validatePublishReview(request PublishReviewRequest, review PublishReview) error {
	review.Headline = strings.TrimSpace(review.Headline)
	review.Summary = strings.TrimSpace(review.Summary)
	if review.Headline == "" || review.Summary == "" || len(review.Headline) > 200 || len(review.Summary) > 1000 || len(review.Risks) > 20 {
		return errors.New("report AI publication review text is invalid")
	}
	want := "ALLOW"
	if len(request.BlockerCodes) != 0 {
		want = "BLOCK"
	} else if len(request.WarningCodes) != 0 {
		want = "CONDITIONAL"
	}
	if review.Recommendation != want {
		return errors.New("report AI publication recommendation conflicts with deterministic gates")
	}
	allowedCodes := append(append([]string{}, request.BlockerCodes...), request.WarningCodes...)
	seen := map[string]struct{}{}
	for _, risk := range review.Risks {
		risk.Code = strings.TrimSpace(risk.Code)
		if !slices.Contains(allowedCodes, risk.Code) || strings.TrimSpace(risk.Title) == "" ||
			strings.TrimSpace(risk.Explanation) == "" || strings.TrimSpace(risk.Evidence) == "" ||
			strings.TrimSpace(risk.SuggestedAction) == "" {
			return errors.New("report AI publication risk is invalid")
		}
		if _, exists := seen[risk.Code]; exists {
			return errors.New("report AI publication risk is duplicated")
		}
		seen[risk.Code] = struct{}{}
	}
	for _, code := range allowedCodes {
		if _, exists := seen[code]; !exists {
			return errors.New("report AI publication review omitted a deterministic issue")
		}
	}
	return nil
}
