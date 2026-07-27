package dataset

import (
	"fmt"
	"sort"
	"strings"
)

const defaultModeledSemanticCode = "general"

var modeledSemanticCodes = map[string]string{
	"运营":   "operations",
	"企业":   "enterprise",
	"交易":   "transaction",
	"履约":   "fulfillment",
	"商品":   "product",
	"商户":   "merchant",
	"客户":   "customer",
	"订单":   "order",
	"订单商品": "order_item",
	"配送事件": "delivery_event",
	"配送区域": "delivery_zone",
	"骑手":   "courier",
	"财务":   "finance",
	"供应链":  "supply_chain",
	"经营分析": "business_analysis",
	"企业画像": "enterprise_profile",
	"风险监控": "risk_monitoring",
	"客户分析": "customer_analysis",
	"商品分析": "product_analysis",
	"履约分析": "fulfillment_analysis",
	"运营分析": "operations_analysis",
	"通用领域": defaultModeledSemanticCode,
	"通用主题": defaultModeledSemanticCode,
}

// ModeledDatasetBusinessName returns the ordinary reader-facing business name.
// Legacy generated layer prefixes are removed so the card title is not coupled
// to the physical table naming convention.
func ModeledDatasetBusinessName(name string) string {
	name = strings.TrimSpace(name)
	parts := strings.Split(name, "_")
	if len(parts) < 2 || !modeledLayerToken(parts[0]) {
		return name
	}
	if len(parts) >= 4 {
		return strings.TrimSpace(strings.Join(parts[3:], "_"))
	}
	return strings.TrimSpace(strings.Join(parts[1:], "_"))
}

// modeledDatasetPhysicalCode applies the governed physical-name convention:
//
//	layer_domain_topic_business_name
//
// All parts are stable ASCII identifiers so the value can safely serve as both
// the dataset technical code and the downstream physical table name.
func modeledDatasetPhysicalCode(
	layer Layer,
	domain string,
	tags []string,
	businessName string,
) (string, error) {
	domainCode := modeledSemanticCode(modeledSemanticValue(domain, "领域"))
	topicCode := modeledSemanticCode(modeledTopic(tags))
	businessCode := trimWarehouseCodePrefix(
		normalizeBusinessIdentifier(businessName),
	)
	businessCode = strings.TrimSuffix(businessCode, "_id")
	businessCode = strings.TrimSuffix(businessCode, "_key")
	businessCode = strings.Trim(businessCode, "_")
	if businessCode == "" || businessCode == "entity" || businessCode == "table" {
		return "", fmt.Errorf(
			"%w: business model code cannot be derived from source table or entity key",
			errDWDModelingInvalid,
		)
	}
	code := strings.Join([]string{
		strings.ToLower(strings.TrimSpace(string(layer))),
		domainCode,
		topicCode,
		businessCode,
	}, "_")
	if !identifierPattern.MatchString(code) || len(code) > 63 {
		return "", fmt.Errorf(
			"%w: governed physical table name is invalid or exceeds 63 characters",
			errDWDModelingInvalid,
		)
	}
	return code, nil
}

func modeledTopic(tags []string) string {
	topics := make([]string, 0, len(tags))
	for _, tag := range tags {
		if !modeledSemanticCategory(tag, "主题") {
			continue
		}
		if value := modeledSemanticValue(tag, "主题"); value != "" {
			topics = append(topics, value)
		}
	}
	if len(topics) == 0 {
		return ""
	}
	sort.Strings(topics)
	return topics[0]
}

func modeledSemanticCode(value string) string {
	value = strings.TrimSpace(value)
	if code := modeledSemanticCodes[value]; code != "" {
		return code
	}
	if code := normalizeBusinessIdentifier(value); code != "" {
		return code
	}
	return defaultModeledSemanticCode
}

func modeledSemanticCategory(value, category string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, category+":") ||
		strings.HasPrefix(value, category+"：")
}

func modeledSemanticValue(value, category string) string {
	value = strings.TrimSpace(value)
	for _, separator := range []string{":", "："} {
		prefix := category + separator
		if strings.HasPrefix(value, prefix) {
			value = strings.TrimSpace(strings.TrimPrefix(value, prefix))
			break
		}
	}
	return strings.Trim(value, "_-:： ·")
}

func modeledDomainName(value string) string {
	if value = modeledSemanticValue(value, "领域"); value != "" {
		return value
	}
	return defaultModeledSemanticCode
}

func modeledLayerToken(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case string(LayerDIM), string(LayerDWD), string(LayerDWS), string(LayerADS):
		return true
	default:
		return false
	}
}
