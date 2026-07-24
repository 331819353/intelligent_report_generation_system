package datasource

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestMaskMetadataSampleRowsRemovesBusinessValues(t *testing.T) {
	rows := []map[string]any{{
		"name":   "张三 Alice",
		"email":  "alice.chen@example.com",
		"phone":  "138-0013-8000",
		"amount": json.Number("16320.55"),
		"active": true,
		"at":     time.Date(2026, 7, 24, 12, 30, 0, 0, time.UTC),
		"bytes":  []byte("secret"),
		"nil":    nil,
	}}
	got := maskMetadataSampleRows(rows)
	want := []map[string]any{{
		"name":   "XX XXXXX",
		"email":  "XXXXX.XXXX@XXXXXXX.XXX",
		"phone":  "000-0000-0000",
		"amount": json.Number("0.0"),
		"active": false,
		"at":     time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		"bytes":  "<bytes>",
		"nil":    nil,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("masked rows=%#v want %#v", got, want)
	}
}
