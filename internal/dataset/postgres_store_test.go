package dataset

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type recordingDatasetExecutor struct {
	statements []string
	arguments  [][]any
	failAt     int
}

type recordingMetricPublicationSink struct {
	calls   int
	version VersionRecord
	err     error
}

func (sink *recordingMetricPublicationSink) EnqueueDatasetMetricExtractionTx(
	_ context.Context,
	_ pgx.Tx,
	_, _ string,
	version VersionRecord,
) error {
	sink.calls++
	sink.version = version
	return sink.err
}

type recordingGovernedPublicationSink struct {
	calls   int
	version VersionRecord
	err     error
}

func (sink *recordingGovernedPublicationSink) EnqueueGovernedDatasetMaterializationTx(
	_ context.Context,
	_ pgx.Tx,
	_, _ string,
	version VersionRecord,
) error {
	sink.calls++
	sink.version = version
	return sink.err
}

func TestPublicationProcessingStartsOnlyFromPublishedVersion(t *testing.T) {
	for _, layer := range []Layer{LayerDIM, LayerDWD, LayerDWS, LayerADS} {
		t.Run(string(layer), func(t *testing.T) {
			metricSink := &recordingMetricPublicationSink{}
			materializationSink := &recordingGovernedPublicationSink{}
			store := &PostgresStore{
				publicationSink:         metricSink,
				governedPublicationSink: materializationSink,
			}
			version := VersionRecord{
				ID: "published-version", DatasetID: "dataset", Status: "PUBLISHED", Layer: layer,
			}
			if err := store.enqueuePublicationProcessing(
				context.Background(), nil, "tenant", "actor", version,
			); err != nil {
				t.Fatal(err)
			}
			if metricSink.calls != 1 || materializationSink.calls != 1 ||
				metricSink.version.ID != version.ID ||
				materializationSink.version.ID != version.ID {
				t.Fatalf("metric=%+v materialization=%+v", metricSink, materializationSink)
			}
		})
	}

	metricSink := &recordingMetricPublicationSink{}
	store := &PostgresStore{publicationSink: metricSink}
	if err := store.enqueuePublicationProcessing(
		context.Background(), nil, "tenant", "actor",
		VersionRecord{ID: "ods-version", Status: "PUBLISHED", Layer: LayerODS},
	); err != nil || metricSink.calls != 1 {
		t.Fatalf("ODS err=%v metric=%+v", err, metricSink)
	}
}

func TestGovernedPublicationFailsClosedWithoutMaterializationOutbox(t *testing.T) {
	store := &PostgresStore{publicationSink: &recordingMetricPublicationSink{}}
	err := store.enqueuePublicationProcessing(
		context.Background(), nil, "tenant", "actor",
		VersionRecord{ID: "dwd-version", Status: "PUBLISHED", Layer: LayerDWD},
	)
	if err == nil || !strings.Contains(err.Error(), "materialization sink is not configured") {
		t.Fatalf("error=%v", err)
	}
}

func (e *recordingDatasetExecutor) Exec(_ context.Context, statement string, arguments ...any) (pgconn.CommandTag, error) {
	e.statements = append(e.statements, statement)
	e.arguments = append(e.arguments, arguments)
	if e.failAt > 0 && len(e.statements) == e.failAt {
		return pgconn.CommandTag{}, errors.New("update failed")
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func TestDeactivateMappedODSAssetOnlyUpdatesControlPlaneRecords(t *testing.T) {
	executor := &recordingDatasetExecutor{}
	deactivated, err := deactivateMappedODSAssetTx(
		context.Background(), executor, "tenant-1", "table-1", string(LayerODS),
	)
	if err != nil || !deactivated {
		t.Fatalf("deactivated=%v err=%v", deactivated, err)
	}
	if len(executor.statements) != 2 {
		t.Fatalf("statements=%#v", executor.statements)
	}
	combined := strings.ToUpper(strings.Join(executor.statements, "\n"))
	for _, forbidden := range []string{"DROP TABLE", "TRUNCATE", "DELETE FROM"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("ODS deletion contains forbidden source-destructive SQL %q: %s", forbidden, combined)
		}
	}
	if !strings.Contains(combined, "PLATFORM.METADATA_COLUMNS") ||
		!strings.Contains(combined, "PLATFORM.METADATA_TABLES") ||
		!strings.Contains(combined, "ASSET_STATUS='INACTIVE'") {
		t.Fatalf("ODS deletion did not deactivate metadata records: %s", combined)
	}
	for _, arguments := range executor.arguments {
		if len(arguments) != 2 || arguments[0] != "table-1" || arguments[1] != "tenant-1" {
			t.Fatalf("unexpected tenant-scoped arguments: %#v", arguments)
		}
	}
}

func TestDeactivateMappedODSAssetIgnoresDerivedOrUnmappedDatasets(t *testing.T) {
	for _, test := range []struct {
		name          string
		originTableID string
		layer         string
	}{
		{name: "DIM", originTableID: "table-1", layer: string(LayerDIM)},
		{name: "DWD", originTableID: "table-1", layer: string(LayerDWD)},
		{name: "DWS", originTableID: "table-1", layer: string(LayerDWS)},
		{name: "ADS", originTableID: "table-1", layer: string(LayerADS)},
		{name: "unmapped ODS", layer: string(LayerODS)},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingDatasetExecutor{}
			deactivated, err := deactivateMappedODSAssetTx(
				context.Background(), executor, "tenant-1", test.originTableID, test.layer,
			)
			if err != nil || deactivated || len(executor.statements) != 0 {
				t.Fatalf("deactivated=%v statements=%#v err=%v", deactivated, executor.statements, err)
			}
		})
	}
}

func TestDeactivateMappedODSAssetRollsBackOnMetadataUpdateFailure(t *testing.T) {
	executor := &recordingDatasetExecutor{failAt: 2}
	deactivated, err := deactivateMappedODSAssetTx(
		context.Background(), executor, "tenant-1", "table-1", string(LayerODS),
	)
	if err == nil || deactivated {
		t.Fatalf("deactivated=%v err=%v", deactivated, err)
	}
}

func TestResolveUniqueVersionSourceRevisionRequiresExactlyOneMatch(t *testing.T) {
	exact := RevisionRecord{RevisionSummary: RevisionSummary{ID: "revision-exact"}}

	resolved, err := resolveUniqueVersionSourceRevision([]RevisionRecord{exact})
	if err != nil || resolved.ID != exact.ID {
		t.Fatalf("single match resolved=%#v err=%v", resolved, err)
	}
	for _, matches := range [][]RevisionRecord{nil, {exact, {RevisionSummary: RevisionSummary{ID: "revision-duplicate"}}}} {
		if _, err := resolveUniqueVersionSourceRevision(matches); !errors.Is(err, ErrVersionRollbackUnavailable) {
			t.Fatalf("matches=%#v error=%v", matches, err)
		}
	}
}
