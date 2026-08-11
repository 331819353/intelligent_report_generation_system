package tools

import (
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

// Compile-time proof that this package supplies both dependencies the Agent
// Loop requires. If either contract drifts, the build breaks here rather than
// at the point where the question run worker tries to assemble a Loop.
var (
	_ orchestrator.CognitionRunner      = (*CognitionRunner)(nil)
	_ orchestrator.GovernedToolRegistry = (*toolhost.Registry)(nil)
)
