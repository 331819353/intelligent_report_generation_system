package registry

import (
	"bytes"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

func TestCanonicalJSONNormalizesOrderWhitespaceAndNumbers(t *testing.T) {
	left, err := CanonicalJSON([]byte(`{"b":1.0,"a":[1e0,{"z":true}]}`))
	if err != nil {
		t.Fatalf("CanonicalJSON(left) error = %v", err)
	}
	right, err := CanonicalJSON([]byte("{\n \"a\" : [1,{\"z\":true}], \"b\": 1 }"))
	if err != nil {
		t.Fatalf("CanonicalJSON(right) error = %v", err)
	}
	if !bytes.Equal(left, right) || string(left) != `{"a":[1,{"z":true}],"b":1}` {
		t.Fatalf("canonical left=%s right=%s", left, right)
	}
	if _, err := CanonicalJSON([]byte(`{"a":1,"a":2}`)); err == nil {
		t.Fatal("CanonicalJSON() accepted duplicate keys")
	}
}

func TestBuildReleaseManifestIsOrderIndependentAndPinsExactVersions(t *testing.T) {
	first := releaseObject(ReleaseObjectMetric, "11111111-1111-4111-8111-111111111111", "21111111-1111-4111-8111-111111111111", `{"type":"METRIC","name":"订单数","version":1,"additivity":"FULLY_ADDITIVE","unit":"COUNT"}`)
	second := releaseObject(ReleaseObjectDimension, "12222222-2222-4222-8222-222222222222", "22222222-2222-4222-8222-222222222222", `{"name":"区域","version":1}`)
	left, err := BuildReleaseManifest([]ReleaseObject{first, second})
	if err != nil {
		t.Fatalf("BuildReleaseManifest(left) error = %v", err)
	}
	right, err := BuildReleaseManifest([]ReleaseObject{second, first})
	if err != nil {
		t.Fatalf("BuildReleaseManifest(right) error = %v", err)
	}
	if left.ContentHash != right.ContentHash {
		t.Fatalf("order changed release hash: %s != %s", left.ContentHash, right.ContentHash)
	}
	changed := first
	changedContract, err := CanonicalJSON([]byte(`{"type":"METRIC","name":"订单数","version":2,"additivity":"FULLY_ADDITIVE","unit":"COUNT"}`))
	if err != nil {
		t.Fatalf("CanonicalJSON(changed) error = %v", err)
	}
	changed.Contract = changedContract
	changed.ContentHash = askdata.HashBytes(changedContract)
	next, err := BuildReleaseManifest([]ReleaseObject{changed, second})
	if err != nil {
		t.Fatalf("BuildReleaseManifest(changed) error = %v", err)
	}
	if next.ContentHash == left.ContentHash {
		t.Fatal("changing one object did not change release hash")
	}
	if next.Objects[0].ContentHash != second.ContentHash || next.Objects[1].ContentHash != changed.ContentHash {
		t.Fatalf("unexpected object hashes after change: %#v", next.Objects)
	}
}

func TestBuildReleaseManifestRejectsContractHashMismatchAndDuplicates(t *testing.T) {
	object := releaseObject(ReleaseObjectMetric, "11111111-1111-4111-8111-111111111111", "21111111-1111-4111-8111-111111111111", `{"type":"METRIC","name":"订单数","additivity":"FULLY_ADDITIVE","unit":"COUNT"}`)
	object.ContentHash = askdata.HashBytes([]byte("wrong"))
	if _, err := BuildReleaseManifest([]ReleaseObject{object}); err == nil {
		t.Fatal("BuildReleaseManifest() accepted mismatched content hash")
	}
	object = releaseObject(ReleaseObjectMetric, "11111111-1111-4111-8111-111111111111", "21111111-1111-4111-8111-111111111111", `{"type":"METRIC","name":"订单数","additivity":"FULLY_ADDITIVE","unit":"COUNT"}`)
	if _, err := BuildReleaseManifest([]ReleaseObject{object, object}); err == nil {
		t.Fatal("BuildReleaseManifest() accepted duplicate exact versions")
	}
}

func releaseObject(objectType ReleaseObjectType, objectID, versionID, contract string) ReleaseObject {
	canonical, err := CanonicalJSON([]byte(contract))
	if err != nil {
		panic(err)
	}
	return ReleaseObject{
		Type: objectType, ObjectID: objectID, ObjectVersionID: versionID,
		ContentHash: askdata.HashBytes(canonical), Sensitivity: SensitivityInternal,
		Contract: canonical,
	}
}
