package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	KPIBundleRoleHeadline  = "HEADLINE"
	KPIBundleRoleTrend     = "TREND"
	KPIBundleRoleBreakdown = "BREAKDOWN"

	MaxKPIBundleItems            = 8
	MaxKPIBundleDimensions       = 64
	MaxKPIBundleQuestionPatterns = 64
)

// KPIBundleItem is one independently compiled plan in a governed KPI answer
// bundle. Different units are allowed across items because each item renders
// independently; query-level unit compatibility remains a compiler concern.
type KPIBundleItem struct {
	MetricVersionID            string   `json:"metricVersionId"`
	Role                       string   `json:"role"`
	GroupByDimensionVersionIDs []string `json:"groupByDimensionVersionIds"`
	ChartType                  string   `json:"chartType"`
	Order                      int      `json:"order"`
}

type KPIBundle struct {
	VersionIdentity
	Code                       string          `json:"code"`
	Name                       string          `json:"name"`
	Items                      []KPIBundleItem `json:"items"`
	DefaultDimensionVersionIDs []string        `json:"defaultDimensionVersionIds"`
	DefaultTimeExpression      string          `json:"defaultTimeExpression"`
	DefaultChartTypes          []string        `json:"defaultChartTypes"`
	RoleMapping                json.RawMessage `json:"roleMapping"`
	ApplicableQuestionPatterns []string        `json:"applicableQuestionPatterns"`
}

// componentManifestTypes is the server-side catalogue corresponding to the
// component-manifest-v1 contract. RPT-CONTRACT-002 may replace its storage,
// but KPI governance always validates through this single catalogue.
var componentManifestTypes = map[string]struct{}{
	"metric-card": {}, "line-trend": {}, "bar-comparison": {}, "bar-horizontal": {},
	"area-stacked": {}, "pie-donut": {}, "scatter": {}, "funnel": {},
	"data-table": {}, "rich-text": {}, "image": {}, "filter-control": {},
	"insight-text": {},
}

func IsRegisteredComponentType(componentType string) bool {
	_, exists := componentManifestTypes[componentType]
	return exists
}

func (bundle KPIBundle) Validate() error {
	validation := validator{}
	validateVersionIdentity(&validation, bundle.VersionIdentity, "")
	validateCodeName(&validation, bundle.Code, bundle.Name)
	if len(bundle.Items) < 1 || len(bundle.Items) > MaxKPIBundleItems {
		validation.add(validationCodeInvalidDependency, "items", "must contain one to eight items")
	}
	headlines := 0
	orders := make(map[int]struct{}, len(bundle.Items))
	groupByDimensions := map[string]struct{}{}
	for index, item := range bundle.Items {
		prefix := fmt.Sprintf("items[%d].", index)
		validateUUID(&validation, prefix+"metricVersionId", item.MetricVersionID, true)
		if !oneOf(item.Role, KPIBundleRoleHeadline, KPIBundleRoleTrend, KPIBundleRoleBreakdown) {
			validation.add(validationCodeInvalidEnum, prefix+"role", "must be HEADLINE, TREND or BREAKDOWN")
		}
		if item.Role == KPIBundleRoleHeadline {
			headlines++
		}
		if !IsRegisteredComponentType(item.ChartType) {
			validation.add(validationCodeInvalidDependency, prefix+"chartType", "KPI_BUNDLE_CHART_UNREGISTERED")
		}
		if item.Order < 1 || item.Order > len(bundle.Items) {
			validation.add(validationCodeInvalidDependency, prefix+"order", "must be contiguous from one")
		} else if _, duplicate := orders[item.Order]; duplicate {
			validation.add(validationCodeDuplicate, prefix+"order", "order is duplicated")
		}
		orders[item.Order] = struct{}{}
		if len(item.GroupByDimensionVersionIDs) > MaxKPIBundleDimensions {
			validation.add(validationCodeInvalidDependency, prefix+"groupByDimensionVersionIds", "cannot contain more than 64 dimensions")
		}
		seenDimensions := map[string]struct{}{}
		for dimensionIndex, dimensionID := range item.GroupByDimensionVersionIDs {
			path := fmt.Sprintf("%sgroupByDimensionVersionIds[%d]", prefix, dimensionIndex)
			validateUUID(&validation, path, dimensionID, true)
			if _, duplicate := seenDimensions[dimensionID]; duplicate {
				validation.add(validationCodeDuplicate, path, "dimension version is duplicated")
			}
			seenDimensions[dimensionID] = struct{}{}
			groupByDimensions[dimensionID] = struct{}{}
		}
	}
	if headlines == 0 {
		validation.add(validationCodeInvalidDependency, "items", "KPI_BUNDLE_HEADLINE_MISSING")
	}
	if len(bundle.DefaultDimensionVersionIDs) > MaxKPIBundleDimensions {
		validation.add(validationCodeInvalidDependency, "defaultDimensionVersionIds", "cannot contain more than 64 dimensions")
	}
	seenDefaults := map[string]struct{}{}
	for index, dimensionID := range bundle.DefaultDimensionVersionIDs {
		path := fmt.Sprintf("defaultDimensionVersionIds[%d]", index)
		validateUUID(&validation, path, dimensionID, true)
		if _, duplicate := seenDefaults[dimensionID]; duplicate {
			validation.add(validationCodeDuplicate, path, "dimension version is duplicated")
		}
		if _, referenced := groupByDimensions[dimensionID]; !referenced {
			validation.add(validationCodeInvalidDependency, path, "must be used by at least one item group-by")
		}
		seenDefaults[dimensionID] = struct{}{}
	}
	if strings.TrimSpace(bundle.DefaultTimeExpression) != bundle.DefaultTimeExpression ||
		len(bundle.DefaultTimeExpression) < 1 || len(bundle.DefaultTimeExpression) > 512 {
		validation.add(validationCodeRequired, "defaultTimeExpression", "must contain 1 to 512 trimmed characters")
	}
	if len(bundle.DefaultChartTypes) > 16 {
		validation.add(validationCodeInvalidDependency, "defaultChartTypes", "cannot contain more than 16 component types")
	}
	for index, componentType := range bundle.DefaultChartTypes {
		if !IsRegisteredComponentType(componentType) {
			validation.add(validationCodeInvalidDependency, fmt.Sprintf("defaultChartTypes[%d]", index), "KPI_BUNDLE_CHART_UNREGISTERED")
		}
	}
	validateJSONObject(&validation, "roleMapping", bundle.RoleMapping, 65536)
	if len(bundle.ApplicableQuestionPatterns) > MaxKPIBundleQuestionPatterns {
		validation.add(validationCodeInvalidDependency, "applicableQuestionPatterns", "cannot contain more than 64 patterns")
	}
	seenPatterns := map[string]struct{}{}
	for index, pattern := range bundle.ApplicableQuestionPatterns {
		normalized := strings.ToLower(strings.TrimSpace(pattern))
		path := fmt.Sprintf("applicableQuestionPatterns[%d]", index)
		if normalized == "" || pattern != strings.TrimSpace(pattern) || len(pattern) > 512 {
			validation.add(validationCodeRequired, path, "must contain 1 to 512 trimmed characters")
		}
		if _, duplicate := seenPatterns[normalized]; duplicate {
			validation.add(validationCodeDuplicate, path, "question pattern is duplicated after normalization")
		}
		seenPatterns[normalized] = struct{}{}
	}
	return validation.result()
}

func normalizeKPIBundle(bundle *KPIBundle) {
	if bundle.RoleMapping == nil {
		bundle.RoleMapping = json.RawMessage(`{}`)
	}
	for index := range bundle.Items {
		bundle.Items[index].GroupByDimensionVersionIDs = sortedAdminIDs(bundle.Items[index].GroupByDimensionVersionIDs)
	}
	sort.SliceStable(bundle.Items, func(left, right int) bool {
		if bundle.Items[left].Order != bundle.Items[right].Order {
			return bundle.Items[left].Order < bundle.Items[right].Order
		}
		return bundle.Items[left].MetricVersionID < bundle.Items[right].MetricVersionID
	})
	bundle.DefaultDimensionVersionIDs = sortedAdminIDs(bundle.DefaultDimensionVersionIDs)
	bundle.ApplicableQuestionPatterns = sortedAdminAliases(bundle.ApplicableQuestionPatterns)
}

type kpiBundleContractDocument struct {
	Type                       string          `json:"type"`
	KPIBundleID                string          `json:"kpiBundleId"`
	VersionNo                  int             `json:"versionNo"`
	Code                       string          `json:"code"`
	Name                       string          `json:"name"`
	Items                      []KPIBundleItem `json:"items"`
	DefaultDimensionVersionIDs []string        `json:"defaultDimensionVersionIds"`
	DefaultTimeExpression      string          `json:"defaultTimeExpression"`
	DefaultChartTypes          []string        `json:"defaultChartTypes"`
	RoleMapping                json.RawMessage `json:"roleMapping"`
	ApplicableQuestionPatterns []string        `json:"applicableQuestionPatterns"`
}

func kpiBundleContract(bundle KPIBundle) kpiBundleContractDocument {
	return kpiBundleContractDocument{
		Type: "KPI_BUNDLE", KPIBundleID: bundle.ObjectID, VersionNo: bundle.VersionNo,
		Code: bundle.Code, Name: bundle.Name, Items: append([]KPIBundleItem(nil), bundle.Items...),
		DefaultDimensionVersionIDs: append([]string(nil), bundle.DefaultDimensionVersionIDs...),
		DefaultTimeExpression:      bundle.DefaultTimeExpression,
		DefaultChartTypes:          append([]string(nil), bundle.DefaultChartTypes...),
		RoleMapping:                append(json.RawMessage(nil), bundle.RoleMapping...),
		ApplicableQuestionPatterns: append([]string(nil), bundle.ApplicableQuestionPatterns...),
	}
}

func KPIBundleContentHash(bundle KPIBundle) askdata.ContentHash {
	normalizeKPIBundle(&bundle)
	return contentHashForContract(kpiBundleContract(bundle))
}

func KPIBundleReleaseObject(bundle KPIBundle) (ReleaseObject, error) {
	if bundle.Status != VersionStatusCertified {
		return ReleaseObject{}, fmt.Errorf("KPI bundle must be CERTIFIED before release")
	}
	normalizeKPIBundle(&bundle)
	if err := bundle.Validate(); err != nil {
		return ReleaseObject{}, err
	}
	return NewReleaseObject(ReleaseObjectKPIBundle, bundle.ObjectID, bundle.ID,
		SensitivityInternal, kpiBundleContract(bundle), bundle.ContentHash)
}

func validateKPIBundleReferencesTx(ctx context.Context, tx pgx.Tx, bundle KPIBundle) error {
	for itemIndex, item := range bundle.Items {
		var metricValid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM askdata.metric_versions
			WHERE id=$1 AND tenant_id=$2 AND domain_id=$3 AND status='CERTIFIED'
		)`, item.MetricVersionID, bundle.TenantID, bundle.DomainID).Scan(&metricValid); err != nil {
			return err
		}
		if !metricValid {
			return fmt.Errorf("%w: KPI_BUNDLE_METRIC_INVALID: items[%d] metric must be certified in the same domain",
				ErrRegistryInvalidRequest, itemIndex)
		}
		for dimensionIndex, dimensionID := range item.GroupByDimensionVersionIDs {
			var compatible bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(
				SELECT 1 FROM askdata.metric_dimension_versions
				WHERE tenant_id=$1 AND domain_id=$2 AND metric_version_id=$3
				  AND dimension_version_id=$4 AND status='CERTIFIED' AND compatible
			)`, bundle.TenantID, bundle.DomainID, item.MetricVersionID, dimensionID).Scan(&compatible); err != nil {
				return err
			}
			if !compatible {
				return fmt.Errorf("%w: KPI_BUNDLE_DIMENSION_INCOMPATIBLE: items[%d].groupByDimensionVersionIds[%d]",
					ErrRegistryInvalidRequest, itemIndex, dimensionIndex)
			}
		}
	}
	return nil
}
