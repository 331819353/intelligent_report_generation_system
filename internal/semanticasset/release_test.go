package semanticasset

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestNormalizeSemanticReleaseBuildsDeterministicManifestHash(t *testing.T) {
	input := completeSemanticReleaseInput()
	first, err := normalizeSemanticRelease(input)
	if err != nil {
		t.Fatalf("normalize first release: %v", err)
	}
	for left, right := 0, len(input.Objects)-1; left < right; left, right = left+1, right-1 {
		input.Objects[left], input.Objects[right] = input.Objects[right], input.Objects[left]
	}
	second, err := normalizeSemanticRelease(input)
	if err != nil {
		t.Fatalf("normalize reordered release: %v", err)
	}
	if first.ContentHash != second.ContentHash || len(first.Objects) != 7 {
		t.Fatalf("manifest hash is not deterministic: %#v %#v", first, second)
	}
	if first.Objects[0].ObjectType != "DATASET" ||
		first.Objects[len(first.Objects)-1].ObjectType != "TIME" {
		t.Fatalf("objects are not canonically sorted: %#v", first.Objects)
	}
}

func TestNormalizeSemanticReleaseRejectsMissingProductionObjectType(t *testing.T) {
	input := completeSemanticReleaseInput()
	input.Objects = input.Objects[:len(input.Objects)-1]
	_, err := normalizeSemanticRelease(input)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing quality rule error = %v", err)
	}
}

func TestNormalizeSemanticReleaseRejectsUnsafeRelation(t *testing.T) {
	input := completeSemanticReleaseInput()
	for index := range input.Objects {
		if input.Objects[index].ObjectType == "RELATION" {
			input.Objects[index].Contract = json.RawMessage(`{
				"title":"订单到门店","fromId":"dataset:orders",
				"toId":"dataset:stores","cardinality":"unknown",
				"certified":false,"fanoutPolicy":"UNSAFE"
			}`)
		}
	}
	_, err := normalizeSemanticRelease(input)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsafe relation error = %v", err)
	}
}

func TestNormalizeSemanticReleaseRejectsMissingMetricEvidenceFields(t *testing.T) {
	input := completeSemanticReleaseInput()
	for index := range input.Objects {
		if input.Objects[index].ObjectType == "METRIC" {
			input.Objects[index].Contract = json.RawMessage(`{
				"title":"支付GMV","formula":"sum(paid_amount)"
			}`)
		}
	}
	_, err := normalizeSemanticRelease(input)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("incomplete metric error = %v", err)
	}
}

func completeSemanticReleaseInput() CreateSemanticReleaseInput {
	owner := "11111111-1111-4111-8111-111111111111"
	validFrom := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	object := func(
		objectType, objectID string,
		contract string,
	) SemanticReleaseObjectInput {
		return SemanticReleaseObjectInput{
			ObjectType: objectType, ObjectID: objectID, ObjectVersion: "1",
			DomainID: "commerce", OwnerID: owner, Certification: "CERTIFIED",
			Sensitivity: "INTERNAL", ValidFrom: validFrom,
			Contract: json.RawMessage(contract),
		}
	}
	return CreateSemanticReleaseInput{
		SemanticVersion: "commerce-2026-08-03.1",
		Notes:           "首个完整语义发布包",
		Objects: []SemanticReleaseObjectInput{
			object("METRIC", "paid_gmv", `{
				"title":"支付GMV","formula":"sum(paid_amount)",
				"grain":["order_id"],"defaultTimeDimensionId":"paid_at",
				"sourceDatasetIds":["paid_orders"],
				"permissionPolicyIds":["commerce_reader"],
				"qualityRuleIds":["paid_orders_freshness"]
			}`),
			object("DIMENSION", "sales_region", `{
				"title":"销售区域","valueKey":"region_code",
				"usages":["GROUP_BY","FILTER","DRILL"]
			}`),
			object("TIME", "paid_at", `{
				"title":"支付时间","timezone":"Asia/Shanghai",
				"calendar":"NATURAL","completePeriodPolicy":"LAST_COMPLETE_PERIOD"
			}`),
			object("RELATION", "paid_orders_to_store", `{
				"title":"支付订单到门店","fromId":"paid_orders",
				"toId":"store","cardinality":"many_to_one",
				"certified":true,"fanoutPolicy":"SAFE"
			}`),
			object("DATASET", "paid_orders", `{
				"title":"支付订单","grain":["order_id"],
				"source":"commerce.fct_paid_order",
				"freshness":{"warnAfter":"1h","blockAfter":"4h"}
			}`),
			object("POLICY", "commerce_reader", `{
				"title":"交易域只读策略","roles":["analyst"],
				"purpose":["analytics"],"effect":"ALLOW"
			}`),
			object("QUALITY_RULE", "paid_orders_freshness", `{
				"title":"支付订单新鲜度","targetId":"paid_orders",
				"severity":"BLOCK","validator":"freshness_under_4h"
			}`),
		},
	}
}
