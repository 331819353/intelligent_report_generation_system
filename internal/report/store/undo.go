package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/operation"
)

var (
	ErrNothingToUndo = errors.New("no report revision is available to undo")
	ErrNothingToRedo = errors.New("no report undo revision is available to redo")
)

func (store *PostgresStore) Undo(ctx context.Context, identity Identity, reportID askdata.ID) (Draft, Revision, error) {
	var err error
	ctx, err = store.requestContext(ctx, identity, reportID)
	if err != nil {
		return Draft{}, Revision{}, err
	}
	var draft Draft
	var revision Revision
	err = store.withTenantTx(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		current, err := loadDraftTx(ctx, tx, reportID, true)
		if err != nil {
			return err
		}
		revisions, err := loadRevisionChainTx(ctx, tx, reportID)
		if err != nil {
			return err
		}
		undoStack, _, err := revisionStacks(revisions)
		if err != nil {
			return err
		}
		if len(undoStack) == 0 {
			return ErrNothingToUndo
		}
		target := undoStack[len(undoStack)-1]
		inverse, err := inverseRevision(target)
		if err != nil {
			return err
		}
		draft, revision, err = saveDraftWithRevisionTx(ctx, tx, identity, reportID, SaveInput{
			ExpectedRevision: current.RevisionNo, Operations: inverse, Source: "UNDO",
			InverseOfRevisionNo: &target.RevisionNo,
		})
		return err
	})
	return draft, revision, err
}

func (store *PostgresStore) Redo(ctx context.Context, identity Identity, reportID askdata.ID) (Draft, Revision, error) {
	var err error
	ctx, err = store.requestContext(ctx, identity, reportID)
	if err != nil {
		return Draft{}, Revision{}, err
	}
	var draft Draft
	var revision Revision
	err = store.withTenantTx(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		current, err := loadDraftTx(ctx, tx, reportID, true)
		if err != nil {
			return err
		}
		revisions, err := loadRevisionChainTx(ctx, tx, reportID)
		if err != nil {
			return err
		}
		_, redoStack, err := revisionStacks(revisions)
		if err != nil {
			return err
		}
		if len(redoStack) == 0 {
			return ErrNothingToRedo
		}
		target := redoStack[len(redoStack)-1]
		inverse, err := inverseRevision(target)
		if err != nil {
			return err
		}
		draft, revision, err = saveDraftWithRevisionTx(ctx, tx, identity, reportID, SaveInput{
			ExpectedRevision: current.RevisionNo, Operations: inverse, Source: "REDO",
			InverseOfRevisionNo: &target.RevisionNo,
		})
		return err
	})
	return draft, revision, err
}

func revisionStacks(revisions []Revision) ([]Revision, []Revision, error) {
	undoStack := make([]Revision, 0, len(revisions))
	redoStack := make([]Revision, 0, len(revisions))
	for _, item := range revisions {
		switch item.Source {
		case "UNDO":
			if len(undoStack) == 0 || item.InverseOfRevisionNo == nil ||
				undoStack[len(undoStack)-1].RevisionNo != *item.InverseOfRevisionNo {
				return nil, nil, errors.New("report undo chain is inconsistent")
			}
			undoStack = undoStack[:len(undoStack)-1]
			redoStack = append(redoStack, item)
		case "REDO":
			if len(redoStack) == 0 || item.InverseOfRevisionNo == nil ||
				redoStack[len(redoStack)-1].RevisionNo != *item.InverseOfRevisionNo {
				return nil, nil, errors.New("report redo chain is inconsistent")
			}
			redoStack = redoStack[:len(redoStack)-1]
			undoStack = append(undoStack, item)
		default:
			if item.InverseOfRevisionNo != nil {
				return nil, nil, errors.New("report revision chain has invalid inverse metadata")
			}
			undoStack = append(undoStack, item)
			redoStack = redoStack[:0]
		}
	}
	return undoStack, redoStack, nil
}

func loadRevisionChainTx(ctx context.Context, tx pgx.Tx, reportID askdata.ID) ([]Revision, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,report_id::text,revision_no,base_revision_no,source,
		operation_json,before_hash,after_hash,COALESCE(before_snapshot,'null'::jsonb),inverse_of_revision_no,
		actor_user_id::text,COALESCE(ai_run_id::text,''),created_at
		FROM platform.report_revisions WHERE report_id=$1 ORDER BY revision_no`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Revision{}
	for rows.Next() {
		var item Revision
		if err := scanRevision(rows, &item); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func inverseRevision(revision Revision) ([]operation.Operation, error) {
	if len(revision.BeforeSnapshot) == 0 {
		return nil, errors.New("report revision predates durable undo snapshots")
	}
	var before reportmodel.ReportDefinition
	if err := json.Unmarshal(revision.BeforeSnapshot, &before); err != nil {
		return nil, err
	}
	var operations []operation.Operation
	if err := json.Unmarshal(revision.OperationJSON, &operations); err != nil {
		return nil, err
	}
	return operation.InvertBundle(operations, before)
}
