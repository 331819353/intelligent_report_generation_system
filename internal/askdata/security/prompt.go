package security

import "intelligent-report-generation-system/internal/askdata/security/promptguard"

const (
	PromptAssessmentVersion  = promptguard.PromptAssessmentVersion
	MaxUntrustedPromptBytes  = promptguard.MaxUntrustedPromptBytes
	PromptTrustUntrustedData = promptguard.PromptTrustUntrustedData
	PromptAllow              = promptguard.PromptAllow
	PromptBlock              = promptguard.PromptBlock
	PromptRefuse             = promptguard.PromptRefuse
)

var (
	ErrInvalidUntrustedPromptData = promptguard.ErrInvalidUntrustedPromptData
	ErrPromptInjection            = promptguard.ErrPromptInjection
	AssessUntrustedPromptData     = promptguard.AssessUntrustedPromptData
)

type PromptTrustLabel = promptguard.PromptTrustLabel
type PromptDisposition = promptguard.PromptDisposition
type PromptAssessment = promptguard.PromptAssessment
type PromptViolation = promptguard.PromptViolation
