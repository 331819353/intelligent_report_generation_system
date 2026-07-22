package datasource

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestDataSourceCodeConflictClassification(t *testing.T) {
	for _, constraint := range []string{"data_sources_tenant_code_active_key", "data_sources_tenant_id_code_key"} {
		err := &pgconn.PgError{Code: "23505", ConstraintName: constraint}
		if !dataSourceCodeConflict(err) {
			t.Fatalf("constraint %s was not classified", constraint)
		}
	}
	if dataSourceCodeConflict(&pgconn.PgError{Code: "23503", ConstraintName: "data_sources_file_asset_id_fkey"}) {
		t.Fatal("foreign-key failure was classified as a code conflict")
	}
	if dataSourceCodeConflict(errors.New("create failed")) {
		t.Fatal("generic failure was classified as a code conflict")
	}
}
