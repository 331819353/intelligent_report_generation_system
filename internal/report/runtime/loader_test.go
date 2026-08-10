package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	stdruntime "runtime"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report/store"
	"intelligent-report-generation-system/internal/report/template"
)

type versionRepositoryFunc func(context.Context, store.Identity, askdata.ID, *int) (store.Version, error)

func (function versionRepositoryFunc) GetVersion(ctx context.Context, identity store.Identity, reportID askdata.ID, versionNo *int) (store.Version, error) {
	return function(ctx, identity, reportID, versionNo)
}

type artifactReaderFunc func(context.Context, string) ([]byte, error)

func (function artifactReaderFunc) Read(ctx context.Context, uri string) ([]byte, error) {
	return function(ctx, uri)
}

type manifestRegistryFunc func(string, string) bool

func (function manifestRegistryFunc) Has(componentType, version string) bool {
	return function(componentType, version)
}

func TestLoaderReadsOnlyReadyImmutableVersions(t *testing.T) {
	raw := canonicalRuntimeFixture(t, "simple-report.json")
	version := store.Version{
		ID: "version_sales_v1", ReportID: "report_sales_overview", VersionNo: 1,
		DefinitionRaw: raw, DefinitionHash: string(askdata.HashBytes(raw)), ObjectURI: "reports/sales/v1.json",
		ArtifactState: "READY",
	}
	registry, err := template.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	requestedVersion := 0
	loader := Loader{
		Versions: versionRepositoryFunc(func(_ context.Context, _ store.Identity, reportID askdata.ID, versionNo *int) (store.Version, error) {
			if reportID != version.ReportID || versionNo == nil || *versionNo != 1 {
				t.Fatalf("version request report=%s version=%v", reportID, versionNo)
			}
			requestedVersion = *versionNo
			return version, nil
		}),
		Artifacts: artifactReaderFunc(func(_ context.Context, uri string) ([]byte, error) {
			if uri != version.ObjectURI {
				t.Fatalf("artifact URI = %s", uri)
			}
			return raw, nil
		}),
		Manifests: registry,
	}
	one := 1
	loaded, err := loader.Load(context.Background(), store.Identity{}, version.ReportID, &one)
	if err != nil || requestedVersion != 1 || loaded.VersionNo != 1 || loaded.Definition.Metadata.ID != version.ReportID {
		t.Fatalf("Load() = %#v, %v", loaded, err)
	}
	if _, exists := reflect.TypeOf(loader).FieldByName("Drafts"); exists {
		t.Fatal("runtime loader exposes a draft repository")
	}
}

func TestLoaderRejectsTamperedPendingAndUnavailableComponents(t *testing.T) {
	raw := canonicalRuntimeFixture(t, "simple-report.json")
	base := store.Version{
		ID: "version_sales_v1", ReportID: "report_sales_overview", VersionNo: 1,
		DefinitionRaw: raw, DefinitionHash: string(askdata.HashBytes(raw)), ObjectURI: "reports/sales/v1.json",
		ArtifactState: "READY",
	}
	tests := []struct {
		name      string
		version   store.Version
		artifact  []byte
		manifests ManifestRegistry
		wantCode  string
	}{
		{name: "pending artifact", version: func() store.Version { value := base; value.ArtifactState = "RETRY"; return value }(), artifact: raw, wantCode: "REPORT_ARTIFACT_NOT_READY"},
		{name: "tampered artifact", version: base, artifact: append(append([]byte(nil), raw...), ' '), wantCode: "REPORT_ARTIFACT_HASH_MISMATCH"},
		{name: "missing component version", version: base, artifact: raw, manifests: manifestRegistryFunc(func(string, string) bool { return false }), wantCode: "REPORT_COMPONENT_VERSION_UNAVAILABLE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loader := Loader{
				Versions: versionRepositoryFunc(func(context.Context, store.Identity, askdata.ID, *int) (store.Version, error) {
					return test.version, nil
				}),
				Artifacts: artifactReaderFunc(func(context.Context, string) ([]byte, error) { return test.artifact, nil }),
				Manifests: test.manifests,
			}
			_, err := loader.Load(context.Background(), store.Identity{}, base.ReportID, nil)
			var runtimeErr *Error
			if !errors.As(err, &runtimeErr) || runtimeErr.Code() != test.wantCode {
				t.Fatalf("Load() error = %#v; want %s", err, test.wantCode)
			}
		})
	}
}

func TestLoaderFallsBackToVersionBytesWhenObjectReadFails(t *testing.T) {
	raw := canonicalRuntimeFixture(t, "simple-report.json")
	version := store.Version{ReportID: "report_sales_overview", VersionNo: 2, DefinitionRaw: raw, DefinitionHash: string(askdata.HashBytes(raw)), ObjectURI: "missing", ArtifactState: "READY"}
	loader := Loader{
		Versions:  versionRepositoryFunc(func(context.Context, store.Identity, askdata.ID, *int) (store.Version, error) { return version, nil }),
		Artifacts: artifactReaderFunc(func(context.Context, string) ([]byte, error) { return nil, errors.New("object store unavailable") }),
	}
	loaded, err := loader.Load(context.Background(), store.Identity{}, version.ReportID, nil)
	if err != nil || loaded.VersionNo != 2 {
		t.Fatalf("fallback Load() = %#v, %v", loaded, err)
	}
}

func canonicalRuntimeFixture(t *testing.T, name string) []byte {
	t.Helper()
	_, filename, _, ok := stdruntime.Caller(0)
	if !ok {
		t.Fatal("resolve runtime fixture path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "..", "api", "examples", "report-definition", name))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := decodeRuntimeFixture(raw)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.Prepare(definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Canonical) == 0 || len(prepared.Hash) != 64 {
		t.Fatal("fixture did not normalize")
	}
	return prepared.Canonical
}
