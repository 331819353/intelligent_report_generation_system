package querycompiler

import (
	"errors"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/dataset"
)

// MaterializationInput describes a server-owned PostgreSQL warehouse build.
// Tables must come from the trusted materialization registry; clients cannot
// provide physical relation names or SQL text.
type MaterializationInput struct {
	Document   dataset.Document
	Tables     map[string]TableRef
	Parameters map[string]any
}

// CompileMaterialization compiles a complete, unbounded PostgreSQL SELECT for
// CTAS/COPY execution in the warehouse. Unlike preview compilation it does not
// apply viewer-specific row/column policies or a row limit: authorization and
// data-quality gates belong to the build plan and publication transaction.
func CompileMaterialization(input MaterializationInput) (CompiledQuery, error) {
	if err := dataset.Validate(input.Document); err != nil {
		return CompiledQuery{}, fmt.Errorf("invalid dataset document: %w", err)
	}
	if len(input.Document.Nodes) == 0 {
		return CompiledQuery{}, errors.New("materialization requires at least one input node")
	}
	if len(input.Tables) != len(input.Document.Nodes) {
		return CompiledQuery{}, errors.New("materialization table whitelist must exactly match the dataset nodes")
	}
	if !input.Document.ExecutionPolicy.Materialization.Enabled {
		return CompiledQuery{}, errors.New("dataset materialization is not enabled")
	}
	if err := validatePostgreSQLDocumentIdentifiers(input.Document); err != nil {
		return CompiledQuery{}, err
	}
	parameters, err := NormalizeParameters(input.Document.Parameters, input.Parameters)
	if err != nil {
		return CompiledQuery{}, err
	}
	compiler := &compiler{input: Input{
		Document:   input.Document,
		Dialect:    PostgreSQL,
		Tables:     input.Tables,
		Parameters: parameters,
	}, args: []any{}}
	query, err := compiler.compileInner()
	if err != nil {
		return CompiledQuery{}, err
	}
	if strings.ContainsAny(query, ";\x00") || strings.Contains(query, "--") || strings.Contains(query, "/*") {
		return CompiledQuery{}, errors.New("compiled materialization query contains a forbidden token")
	}
	return CompiledQuery{SQL: query, Args: compiler.args}, nil
}
