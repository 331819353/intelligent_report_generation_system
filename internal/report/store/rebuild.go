package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
)

type RebuildIndexResult struct {
	DraftRevisionNo    int64 `json:"draftRevisionNo"`
	DraftComponents    int   `json:"draftComponents"`
	DraftDependencies  int   `json:"draftDependencies"`
	VerifiedVersions   int   `json:"verifiedVersions"`
	BackfilledVersions int   `json:"backfilledVersions"`
}

// RebuildAllIndexes reconstructs the mutable draft indexes and verifies every
// immutable version index. A completely absent version index may be restored;
// a partial or divergent immutable index fails closed for forensic repair.
func (store *PostgresStore) RebuildAllIndexes(ctx context.Context, identity Identity, reportID askdata.ID) (RebuildIndexResult, error) {
	var err error
	ctx, err = store.requestContext(ctx, identity, reportID)
	if err != nil {
		return RebuildIndexResult{}, err
	}
	var result RebuildIndexResult
	err = store.withTenantTx(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		// CreateVersion takes the same report-row lock before allocating a
		// version number. Holding it makes the repair snapshot closed: no new
		// immutable version can appear between enumeration and commit.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM platform.reports WHERE id=$1 FOR UPDATE`, reportID); err != nil {
			return err
		}
		draft, err := loadDraftTx(ctx, tx, reportID, true)
		if err != nil {
			return err
		}
		draftIndexes := compiler.BuildIndexes(draft.Definition)
		if err := rebuildDraftIndexes(ctx, tx, identity.TenantID, reportID, draft.RevisionNo, draftIndexes); err != nil {
			return err
		}
		result.DraftRevisionNo = draft.RevisionNo
		result.DraftComponents = len(draftIndexes.Components)
		result.DraftDependencies = len(draftIndexes.Dependencies)
		if err := verifyDraftIndexes(ctx, tx, reportID, draft.RevisionNo, draftIndexes); err != nil {
			return fmt.Errorf("draft: %w", err)
		}

		rows, err := tx.Query(ctx, `SELECT id::text,definition_json,definition_hash
			FROM platform.report_versions WHERE report_id=$1 ORDER BY version_no`, reportID)
		if err != nil {
			return err
		}
		type versionDocument struct {
			id   askdata.ID
			raw  []byte
			hash string
		}
		versions := []versionDocument{}
		for rows.Next() {
			var item versionDocument
			if err := rows.Scan(&item.id, &item.raw, &item.hash); err != nil {
				rows.Close()
				return err
			}
			versions = append(versions, item)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, version := range versions {
			var definition report.ReportDefinition
			var canonical json.RawMessage
			if err := hydrateStoredDefinition(version.raw, version.hash, &definition, &canonical); err != nil {
				return fmt.Errorf("version %s definition: %w", version.id, err)
			}
			expected := compiler.BuildIndexes(definition)
			componentCount, dependencyCount, err := versionIndexCounts(ctx, tx, version.id)
			if err != nil {
				return err
			}
			if componentCount == 0 && dependencyCount == 0 {
				if err := insertVersionIndexes(ctx, tx, identity.TenantID, reportID, version.id, expected); err != nil {
					return err
				}
				result.BackfilledVersions++
			} else if componentCount != len(expected.Components) || dependencyCount != len(expected.Dependencies) {
				return fmt.Errorf("version %s immutable index cardinality mismatch", version.id)
			}
			if err := verifyVersionIndexes(ctx, tx, version.id, expected); err != nil {
				return fmt.Errorf("version %s: %w", version.id, err)
			}
			result.VerifiedVersions++
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return RebuildIndexResult{}, ErrNotFound
	}
	return result, err
}

func verifyDraftIndexes(
	ctx context.Context, tx pgx.Tx, reportID askdata.ID, revisionNo int64, expected compiler.Indexes,
) error {
	var componentCount, dependencyCount int
	if err := tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM platform.report_draft_component_indexes WHERE report_id=$1),
		(SELECT count(*) FROM platform.report_draft_dependencies WHERE report_id=$1)`, reportID,
	).Scan(&componentCount, &dependencyCount); err != nil {
		return err
	}
	if componentCount != len(expected.Components) || dependencyCount != len(expected.Dependencies) {
		return errors.New("draft index cardinality mismatch")
	}
	for _, item := range expected.Components {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.report_draft_component_indexes
			WHERE report_id=$1 AND revision_no=$2 AND component_id=$3
			  AND component_type=$4 AND component_version=$5 AND page_id=$6
			  AND section_id=$7 AND block_id=$8 AND slot_id=$9
			  AND COALESCE(binding_mode,'')=$10
		)`, reportID, revisionNo, item.ComponentID, item.ComponentType, item.ComponentVersion,
			item.PageID, item.SectionID, item.BlockID, item.SlotID, item.BindingMode,
		).Scan(&exists); err != nil || !exists {
			return errors.Join(err, errors.New("component index mismatch"))
		}
	}
	for _, item := range expected.Dependencies {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.report_draft_dependencies
			WHERE report_id=$1 AND dependency_type=$2 AND dependency_id=$3
			  AND component_ids=$4
		)`, reportID, item.DependencyType, item.DependencyID,
			idsToStrings(item.ComponentIDs),
		).Scan(&exists); err != nil || !exists {
			return errors.Join(err, errors.New("dependency index mismatch"))
		}
	}
	return nil
}

func versionIndexCounts(ctx context.Context, tx pgx.Tx, versionID askdata.ID) (int, int, error) {
	var components, dependencies int
	if err := tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM platform.report_version_component_indexes WHERE report_version_id=$1),
		(SELECT count(*) FROM platform.report_version_dependencies WHERE report_version_id=$1)`, versionID).Scan(&components, &dependencies); err != nil {
		return 0, 0, err
	}
	return components, dependencies, nil
}

func verifyVersionIndexes(ctx context.Context, tx pgx.Tx, versionID askdata.ID, expected compiler.Indexes) error {
	for _, item := range expected.Components {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.report_version_component_indexes
			WHERE report_version_id=$1 AND component_id=$2 AND component_type=$3 AND component_version=$4
			AND page_id=$5 AND section_id=$6 AND block_id=$7 AND slot_id=$8
			AND COALESCE(binding_mode,'')=$9)`, versionID, item.ComponentID, item.ComponentType,
			item.ComponentVersion, item.PageID, item.SectionID, item.BlockID, item.SlotID, item.BindingMode).Scan(&exists); err != nil || !exists {
			return errors.Join(err, errors.New("component index mismatch"))
		}
	}
	for _, item := range expected.Dependencies {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.report_version_dependencies
			WHERE report_version_id=$1 AND dependency_type=$2 AND dependency_id=$3
			AND component_ids=$4)`, versionID, item.DependencyType, item.DependencyID,
			idsToStrings(item.ComponentIDs)).Scan(&exists); err != nil || !exists {
			return errors.Join(err, errors.New("dependency index mismatch"))
		}
	}
	return nil
}
