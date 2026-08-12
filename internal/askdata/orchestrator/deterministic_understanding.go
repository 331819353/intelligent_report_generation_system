package orchestrator

import (
	"encoding/json"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
)

func deterministicUnderstanding(
	request LoopRequest,
) (cognition.UnderstandingProposal, askdata.EvidenceRef, bool) {
	rules := rulesFromConversationFacts(request)
	if rules == nil || len(rules.UnresolvedSpans) != 0 {
		return cognition.UnderstandingProposal{}, askdata.EvidenceRef{}, false
	}
	for _, governed := range request.Facts {
		if governed.Fact.Kind != cognition.FactConversation || governed.Evidence.Validate() != nil {
			continue
		}
		var conversation struct {
			Question string `json:"question"`
		}
		if json.Unmarshal(governed.Fact.Payload, &conversation) != nil || strings.TrimSpace(conversation.Question) == "" {
			continue
		}
		return cognition.UnderstandingProposal{
			IntentSummary:   "在当前业务领域内按已发布语义口径执行受控指标分析。",
			UnresolvedSpans: []string{},
		}, governed.Evidence, true
	}
	return cognition.UnderstandingProposal{}, askdata.EvidenceRef{}, false
}
