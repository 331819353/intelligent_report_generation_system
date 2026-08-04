package datasource

import (
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestSourceConnectionIdentityUsesConnectionAndUsername(t *testing.T) {
	t.Parallel()
	base := Source{Type: TypeOracle, Config: map[string]any{
		"host": "DB.Internal", "port": 11521, "database": "FREEPDB1", "username": "TAKEOUT_USER",
	}}
	same := Source{Type: TypeOracle, Config: map[string]any{
		"host": " db.internal ", "port": float64(11521), "database": "freepdb1", "username": "takeout_user",
	}}
	if sourceConnectionIdentity(base) == "" || sourceConnectionIdentity(base) != sourceConnectionIdentity(same) {
		t.Fatal("case and JSON number representation must not change connection identity")
	}
	differentUser := same
	differentUser.Config = map[string]any{
		"host": "db.internal", "port": 11521, "database": "freepdb1", "username": "another_user",
	}
	if sourceConnectionIdentity(base) == sourceConnectionIdentity(differentUser) {
		t.Fatal("a different username must produce a different connection identity")
	}
	if sourceConnectionIdentity(Source{Type: TypeExcel}) != "" {
		t.Fatal("file data sources must not have a database connection identity")
	}
}

func TestDataSourceConnectionConflictRecognizesUniqueIndex(t *testing.T) {
	t.Parallel()
	err := &pgconn.PgError{Code: "23505", ConstraintName: "data_sources_domain_connection_identity_active_key"}
	if !dataSourceConnectionConflict(err) || dataSourceCodeConflict(err) {
		t.Fatal("connection identity conflict must be classified separately from code conflicts")
	}
}
