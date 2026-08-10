package compiler

import "intelligent-report-generation-system/internal/askdata/ir"

// ResolvedTimeSpec and ResolvedComparison are re-exported from the compiler
// artifact boundary so answer, evidence and report packages do not depend on
// the mutable compiler implementation.
type ResolvedTimeSpec = ir.ResolvedTimeSpec
type ResolvedComparison = ir.ResolvedComparison

// ValidateResolvedTimeSpec validates a serialized/replayed time artifact with
// the same rules used before QueryArtifact hashing.
func ValidateResolvedTimeSpec(value ResolvedTimeSpec) error {
	return validateResolvedTimeSpec(value)
}
