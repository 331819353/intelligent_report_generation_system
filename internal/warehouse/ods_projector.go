package warehouse

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/querycompiler"
)

type ODSProjectionInput struct {
	TenantID     string
	SourceRunID  string
	TargetRunID  string
	TargetNodeID string
	Document     dataset.Document
	Source       querycompiler.TableRef
}

// ODSProjector applies a published ODS projection to a fully staged immutable
// source and writes a run-scoped transient table so DIM/DWD can consume the
// complete frozen source without trusting client-provided physical relations.
type ODSProjector struct {
	transactions stagingTransactionFactory
}

func NewODSProjector(pool *pgxpool.Pool) *ODSProjector {
	return &ODSProjector{transactions: pgxStagingFactory{pool: pool}}
}

func newODSProjector(transactions stagingTransactionFactory) *ODSProjector {
	return &ODSProjector{transactions: transactions}
}

func (projector *ODSProjector) Project(
	ctx context.Context,
	input ODSProjectionInput,
) (StageResult, error) {
	if projector == nil || projector.transactions == nil {
		return StageResult{}, fmt.Errorf(
			"%w: ODS projector is not configured", ErrInvalidBuild,
		)
	}
	if input.Document.Dataset.Layer != dataset.LayerODS ||
		len(input.Document.Nodes) != 1 ||
		input.Document.Nodes[0].Type != "TABLE" ||
		strings.TrimSpace(input.TargetNodeID) == "" {
		return StageResult{}, fmt.Errorf(
			"%w: ODS projection contract is invalid", ErrInvalidBuild,
		)
	}
	sourceNode := input.Document.Nodes[0]
	expectedSourceSchema, expectedSourceTable, err := stagingTarget(
		input.TenantID, input.SourceRunID, sourceNode.ID,
	)
	if err != nil {
		return StageResult{}, err
	}
	if input.Source.NodeID != sourceNode.ID ||
		input.Source.Schema != expectedSourceSchema ||
		input.Source.Name != expectedSourceTable {
		return StageResult{}, fmt.Errorf(
			"%w: ODS projection source is outside its frozen staging target",
			ErrInvalidBuild,
		)
	}
	targetSchema, targetTable, err := stagingTarget(
		input.TenantID, input.TargetRunID, input.TargetNodeID,
	)
	if err != nil {
		return StageResult{}, err
	}
	// Legacy ODS versions may predate source-backed materialization. This
	// server-owned clone enables the compiler for the transient projection that
	// feeds a DIM/DWD build without mutating the immutable published DSL.
	projectionDocument := input.Document
	projectionDocument.ExecutionPolicy.Materialization.Enabled = true
	projectionDocument.ExecutionPolicy.Materialization.RefreshMode = "ON_DEMAND"
	compiled, err := querycompiler.CompileMaterialization(
		querycompiler.MaterializationInput{
			Document: projectionDocument,
			Tables: map[string]querycompiler.TableRef{
				sourceNode.ID: input.Source,
			},
			Parameters: nil,
		},
	)
	if err != nil {
		return StageResult{}, fmt.Errorf(
			"%w: compile frozen ODS projection: %v", ErrInvalidBuild, err,
		)
	}

	tx, err := projector.transactions.Begin(ctx)
	if err != nil {
		return StageResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qualified := quoteIdentifier(targetSchema) + "." + quoteIdentifier(targetTable)
	if _, err := tx.Exec(ctx, "DROP TABLE IF EXISTS "+qualified); err != nil {
		return StageResult{}, fmt.Errorf("replace projected ODS staging table: %w", err)
	}
	if _, err := tx.Exec(
		ctx,
		"CREATE UNLOGGED TABLE "+qualified+" AS "+compiled.SQL,
		compiled.Args...,
	); err != nil {
		return StageResult{}, fmt.Errorf("project full ODS staging table: %w", err)
	}
	var rowCount int64
	if err := tx.QueryRow(
		ctx, "SELECT COUNT(*)::bigint FROM "+qualified,
	).Scan(&rowCount); err != nil {
		return StageResult{}, fmt.Errorf("count projected ODS staging rows: %w", err)
	}
	if _, err := tx.Exec(ctx, "ANALYZE "+qualified); err != nil {
		return StageResult{}, fmt.Errorf("analyze projected ODS staging table: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return StageResult{}, err
	}
	return StageResult{
		Schema: targetSchema, Table: targetTable,
		QualifiedName: targetSchema + "." + targetTable,
		RowCount:      rowCount,
	}, nil
}
