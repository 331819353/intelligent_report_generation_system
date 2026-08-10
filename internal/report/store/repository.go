package store

import (
	"context"

	"intelligent-report-generation-system/internal/askdata"
)

// Repository is the Report V2 persistence boundary. Draft mutations and their
// immutable revision records are deliberately exposed as one operation so a
// caller cannot persist only half of a revision.
type Repository interface {
	CreateReport(context.Context, Identity, CreateInput) (Report, Draft, error)
	ListReports(context.Context, Identity, int) ([]Report, error)
	GetReport(context.Context, Identity, askdata.ID) (Report, error)
	GetDraft(context.Context, Identity, askdata.ID) (Draft, error)
	GetDraftRevision(context.Context, Identity, askdata.ID, *int64) (Draft, error)
	SaveDraftWithRevision(context.Context, Identity, askdata.ID, SaveInput) (Draft, Revision, error)
	ListRevisions(context.Context, Identity, askdata.ID, int) ([]Revision, error)
	Undo(context.Context, Identity, askdata.ID) (Draft, Revision, error)
	Redo(context.Context, Identity, askdata.ID) (Draft, Revision, error)
	CreateVersion(context.Context, Identity, askdata.ID, CreateVersionInput) (Version, error)
	ListVersions(context.Context, Identity, askdata.ID, int) ([]Version, error)
	GetVersion(context.Context, Identity, askdata.ID, *int) (Version, error)
}

var _ Repository = (*PostgresStore)(nil)
