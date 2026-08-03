package semanticqa

type QuestionRouteCapability struct {
	Route      QuestionRoute `json:"route"`
	Enabled    bool          `json:"enabled"`
	ReasonCode string        `json:"reasonCode,omitempty"`
}

type QuestionRoutingDecision struct {
	Selected     QuestionRoute             `json:"selected"`
	ReasonCode   string                    `json:"reasonCode"`
	Capabilities []QuestionRouteCapability `json:"capabilities"`
}

// routeQuestion implements the three-path policy from the implementation
// plan. The current repository has a secure structured DSL compiler but no
// reliable free-SQL AST adapter for its mixed warehouse dialects, so path B is
// explicitly disabled rather than silently executing model SQL.
func routeQuestion(turn QueryTurnPlan) QuestionRoutingDecision {
	decision := QuestionRoutingDecision{
		Selected:   QuestionRouteClarifyOrRefuse,
		ReasonCode: "SEMANTIC_EVIDENCE_INCOMPLETE",
		Capabilities: []QuestionRouteCapability{
			{Route: QuestionRouteSemantic, Enabled: true},
			{
				Route: QuestionRouteGovernedTextSQL, Enabled: false,
				ReasonCode: "RELIABLE_DIALECT_AST_ADAPTER_NOT_CONFIGURED",
			},
			{Route: QuestionRouteClarifyOrRefuse, Enabled: true},
		},
	}
	if turn.Clarification != nil || turn.State == QuestionStateClarificationRequired {
		decision.ReasonCode = "MINIMUM_CLARIFICATION_REQUIRED"
		return decision
	}
	if len(turn.Plans) == 0 {
		return decision
	}
	for _, plan := range turn.Plans {
		if plan.Status != "READY" {
			decision.ReasonCode = "SEMANTIC_PLAN_NOT_READY"
			return decision
		}
	}
	decision.Selected = QuestionRouteSemantic
	decision.ReasonCode = "CERTIFIED_SEMANTIC_PATH"
	return decision
}
