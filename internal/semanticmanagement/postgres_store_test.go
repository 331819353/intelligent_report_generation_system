package semanticmanagement

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresStoreMapsConstraintFailuresToStableDomainErrors(t *testing.T) {
	tests := []struct {
		code string
		want error
	}{
		{code: "23505", want: ErrConflict},
		{code: "23503", want: ErrInvalidRequest},
		{code: "23514", want: ErrInvalidRequest},
		{code: "22P02", want: ErrInvalidRequest},
	}
	for _, test := range tests {
		err := mapWriteError(&pgconn.PgError{Code: test.code})
		if !errors.Is(err, test.want) {
			t.Fatalf("code=%s error=%v want=%v", test.code, err, test.want)
		}
	}
}

func TestPostgresStoreNullableIdentityArguments(t *testing.T) {
	if nullableUUID("") != nil || nullableText("") != nil {
		t.Fatal("empty optional values must be sent as SQL NULL")
	}
	if nullableUUID(testTagID) != testTagID || nullableText("field_1") != "field_1" {
		t.Fatal("non-empty values must be preserved")
	}
}
