// Package testfixture provides synthetic-only semantic assets and question
// cases shared by askdata unit and integration tests. Nothing in this package
// is a production business definition or credential.
package testfixture

import (
	"errors"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
)

const FixtureVersion = "askdata-synthetic-v1"

type Tenant struct {
	TenantID askdata.ID `json:"tenantId"`
	Name     string     `json:"name"`
}

type User struct {
	ActorID   askdata.ID   `json:"actorId"`
	TenantID  askdata.ID   `json:"tenantId"`
	RoleIDs   []askdata.ID `json:"roleIds"`
	DomainIDs []askdata.ID `json:"domainIds"`
}

type SemanticModel struct {
	Version          askdata.VersionRef `json:"version"`
	DomainID         askdata.ID         `json:"domainId"`
	DatasetVersionID askdata.ID         `json:"datasetVersionId"`
	Layer            string             `json:"layer"`
	Name             string             `json:"name"`
}

type Metric struct {
	Version        askdata.VersionRef `json:"version"`
	ModelVersionID askdata.ID         `json:"modelVersionId"`
	Name           string             `json:"name"`
	Aliases        []string           `json:"aliases"`
	Unit           string             `json:"unit"`
	FormulaLabel   string             `json:"formulaLabel"`
}

type Dimension struct {
	Version        askdata.VersionRef `json:"version"`
	ModelVersionID askdata.ID         `json:"modelVersionId"`
	Name           string             `json:"name"`
	Aliases        []string           `json:"aliases"`
	Sensitive      bool               `json:"sensitive"`
}

type MemberStatus string

const (
	MemberActive  MemberStatus = "ACTIVE"
	MemberExpired MemberStatus = "EXPIRED"
)

type Member struct {
	Version            askdata.VersionRef `json:"version"`
	DimensionVersionID askdata.ID         `json:"dimensionVersionId"`
	Key                string             `json:"key"`
	Label              string             `json:"label"`
	Aliases            []string           `json:"aliases"`
	Status             MemberStatus       `json:"status"`
}

type Cardinality string

const (
	CardinalityOneToOne   Cardinality = "ONE_TO_ONE"
	CardinalityManyToOne  Cardinality = "MANY_TO_ONE"
	CardinalityOneToMany  Cardinality = "ONE_TO_MANY"
	CardinalityManyToMany Cardinality = "MANY_TO_MANY"
)

type Relationship struct {
	RelationshipID     askdata.ID  `json:"relationshipId"`
	FromModelVersionID askdata.ID  `json:"fromModelVersionId"`
	ToModelVersionID   askdata.ID  `json:"toModelVersionId"`
	Cardinality        Cardinality `json:"cardinality"`
	Certified          bool        `json:"certified"`
	FanoutRisk         bool        `json:"fanoutRisk"`
}

type Disposition string

const (
	DispositionDirect  Disposition = "DIRECT"
	DispositionClarify Disposition = "CLARIFY"
	DispositionRefuse  Disposition = "REFUSE"
	DispositionNoData  Disposition = "NO_DATA"
)

type ScenarioCode string

const (
	ScenarioSameNameMetric ScenarioCode = "SAME_NAME_METRIC"
	ScenarioSameNameMember ScenarioCode = "SAME_NAME_MEMBER"
	ScenarioUnauthorized   ScenarioCode = "UNAUTHORIZED"
	ScenarioJoinFanout     ScenarioCode = "JOIN_FANOUT"
	ScenarioEmptyResult    ScenarioCode = "EMPTY_RESULT"
	ScenarioExpiredMember  ScenarioCode = "EXPIRED_MEMBER"
)

type Question struct {
	QuestionID          askdata.ID     `json:"questionId"`
	TenantID            askdata.ID     `json:"tenantId"`
	ActorID             askdata.ID     `json:"actorId"`
	Text                string         `json:"text"`
	Scenario            ScenarioCode   `json:"scenario"`
	ExpectedDisposition Disposition    `json:"expectedDisposition"`
	ExpectedIR          *ir.SemanticIR `json:"expectedIr"`
	ExpectedReasonCode  string         `json:"expectedReasonCode"`
}

type Result struct {
	QuestionID askdata.ID `json:"questionId"`
	Columns    []string   `json:"columns"`
	Rows       [][]string `json:"rows"`
}

type Set struct {
	FixtureVersion string             `json:"fixtureVersion"`
	Synthetic      bool               `json:"synthetic"`
	Release        askdata.ReleaseRef `json:"release"`
	Tenants        []Tenant           `json:"tenants"`
	Users          []User             `json:"users"`
	Models         []SemanticModel    `json:"models"`
	Metrics        []Metric           `json:"metrics"`
	Dimensions     []Dimension        `json:"dimensions"`
	Members        []Member           `json:"members"`
	Relationships  []Relationship     `json:"relationships"`
	Questions      []Question         `json:"questions"`
	Results        []Result           `json:"results"`
}

// Standard returns a fresh, fully synthetic fixture graph containing the hard
// cases required by Wave 0.
func Standard() Set {
	release := askdata.ReleaseRef{ReleaseID: "synthetic-release@v1", ContentHash: hash("synthetic release v1")}
	month := ir.TimeGrainMonth
	directIR := ir.SemanticIR{
		IRVersion: ir.Version, SemanticReleaseID: release.ReleaseID, SemanticContentHash: release.ContentHash,
		ModelVersionID: "sales-orders-model@v1",
		Metrics:        []ir.Metric{{MetricVersionID: "sales-net-amount@v1", Alias: "net_sales"}},
		GroupBy:        []ir.GroupBy{{DimensionVersionID: "sales-stat-month@v1", Grain: &month}},
		Filters: []ir.Filter{{
			DimensionVersionID: "sales-region@v1", Operator: ir.FilterIn,
			MemberVersionIDs: []askdata.ID{"sales-region-east@v1"},
		}},
		TimeRange:  &ir.TimeRange{DimensionVersionID: "sales-order-date@v1", Start: "2026-01-01", EndExclusive: "2027-01-01", Timezone: "Asia/Shanghai"},
		Comparison: nil,
		Sort:       []ir.Sort{{TargetType: ir.SortTargetMetric, TargetVersionID: "sales-net-amount@v1", Direction: ir.SortDescending, Nulls: ir.NullsLast}},
		Limit:      500,
	}
	return Set{
		FixtureVersion: FixtureVersion,
		Synthetic:      true,
		Release:        release,
		Tenants: []Tenant{
			{TenantID: "tenant-synthetic-a", Name: "合成租户 A"},
			{TenantID: "tenant-synthetic-b", Name: "合成租户 B"},
		},
		Users: []User{
			{ActorID: "actor-sales", TenantID: "tenant-synthetic-a", RoleIDs: []askdata.ID{"analyst"}, DomainIDs: []askdata.ID{"sales"}},
			{ActorID: "actor-finance", TenantID: "tenant-synthetic-a", RoleIDs: []askdata.ID{"finance-viewer"}, DomainIDs: []askdata.ID{"finance"}},
			{ActorID: "actor-other-tenant", TenantID: "tenant-synthetic-b", RoleIDs: []askdata.ID{"analyst"}, DomainIDs: []askdata.ID{"sales"}},
		},
		Models: []SemanticModel{
			{Version: version("sales-orders-model", "sales-orders-model@v1"), DomainID: "sales", DatasetVersionID: "dws-sales-orders@v1", Layer: "DWS", Name: "销售订单汇总模型"},
			{Version: version("finance-ledger-model", "finance-ledger-model@v1"), DomainID: "finance", DatasetVersionID: "ads-finance-ledger@v1", Layer: "ADS", Name: "财务入账分析模型"},
			{Version: version("sales-line-model", "sales-line-model@v1"), DomainID: "sales", DatasetVersionID: "dws-sales-lines@v1", Layer: "DWS", Name: "销售明细聚合模型"},
		},
		Metrics: []Metric{
			{Version: version("sales-net-amount", "sales-net-amount@v1"), ModelVersionID: "sales-orders-model@v1", Name: "销售额", Aliases: []string{"净销售额", "成交额"}, Unit: "CNY", FormulaLabel: "paid amount minus refund"},
			{Version: version("finance-booked-amount", "finance-booked-amount@v1"), ModelVersionID: "finance-ledger-model@v1", Name: "销售额", Aliases: []string{"入账销售额"}, Unit: "CNY", FormulaLabel: "recognized ledger amount"},
			{Version: version("sales-order-count", "sales-order-count@v1"), ModelVersionID: "sales-orders-model@v1", Name: "订单数", Aliases: []string{"成交订单量"}, Unit: "COUNT", FormulaLabel: "distinct paid order count"},
		},
		Dimensions: []Dimension{
			{Version: version("sales-region", "sales-region@v1"), ModelVersionID: "sales-orders-model@v1", Name: "销售区域", Aliases: []string{"区域", "地区"}},
			{Version: version("sales-organization", "sales-organization@v1"), ModelVersionID: "sales-orders-model@v1", Name: "销售组织", Aliases: []string{"组织"}},
			{Version: version("sales-stat-month", "sales-stat-month@v1"), ModelVersionID: "sales-orders-model@v1", Name: "统计月", Aliases: []string{"月份"}},
			{Version: version("sales-order-date", "sales-order-date@v1"), ModelVersionID: "sales-orders-model@v1", Name: "下单日期", Aliases: []string{"订单日期"}},
			{Version: version("finance-account", "finance-account@v1"), ModelVersionID: "finance-ledger-model@v1", Name: "会计科目", Aliases: []string{"科目"}, Sensitive: true},
		},
		Members: []Member{
			{Version: version("sales-region-east", "sales-region-east@v1"), DimensionVersionID: "sales-region@v1", Key: "EAST", Label: "华东", Aliases: []string{"华东区"}, Status: MemberActive},
			{Version: version("sales-organization-east", "sales-organization-east@v1"), DimensionVersionID: "sales-organization@v1", Key: "ORG_EAST", Label: "华东", Aliases: []string{"华东组织"}, Status: MemberActive},
			{Version: version("sales-region-east-old", "sales-region-east-old@v1"), DimensionVersionID: "sales-region@v1", Key: "EAST_OLD", Label: "华东旧区", Aliases: []string{"原华东区"}, Status: MemberExpired},
		},
		Relationships: []Relationship{
			{RelationshipID: "rel-orders-to-lines", FromModelVersionID: "sales-orders-model@v1", ToModelVersionID: "sales-line-model@v1", Cardinality: CardinalityOneToMany, Certified: true, FanoutRisk: true},
			{RelationshipID: "rel-orders-to-ledger", FromModelVersionID: "sales-orders-model@v1", ToModelVersionID: "finance-ledger-model@v1", Cardinality: CardinalityManyToMany, Certified: false, FanoutRisk: true},
		},
		Questions: []Question{
			{QuestionID: "question-direct", TenantID: "tenant-synthetic-a", ActorID: "actor-sales", Text: "今年华东区按月的销售额", Scenario: ScenarioSameNameMetric, ExpectedDisposition: DispositionDirect, ExpectedIR: &directIR},
			{QuestionID: "question-member-ambiguous", TenantID: "tenant-synthetic-a", ActorID: "actor-sales", Text: "华东的销售额", Scenario: ScenarioSameNameMember, ExpectedDisposition: DispositionClarify, ExpectedReasonCode: "MEMBER_DIMENSION_AMBIGUOUS"},
			{QuestionID: "question-unauthorized", TenantID: "tenant-synthetic-a", ActorID: "actor-sales", Text: "财务入账销售额", Scenario: ScenarioUnauthorized, ExpectedDisposition: DispositionRefuse, ExpectedReasonCode: "SEMANTIC_OBJECT_FORBIDDEN"},
			{QuestionID: "question-fanout", TenantID: "tenant-synthetic-a", ActorID: "actor-sales", Text: "按商品统计订单销售额", Scenario: ScenarioJoinFanout, ExpectedDisposition: DispositionClarify, ExpectedReasonCode: "JOIN_FANOUT_UNSAFE"},
			{QuestionID: "question-empty", TenantID: "tenant-synthetic-a", ActorID: "actor-sales", Text: "2099年华东区销售额", Scenario: ScenarioEmptyResult, ExpectedDisposition: DispositionNoData, ExpectedReasonCode: "TIME_RANGE_NO_DATA"},
			{QuestionID: "question-expired-member", TenantID: "tenant-synthetic-a", ActorID: "actor-sales", Text: "原华东区销售额", Scenario: ScenarioExpiredMember, ExpectedDisposition: DispositionClarify, ExpectedReasonCode: "MEMBER_EXPIRED"},
		},
		Results: []Result{
			{QuestionID: "question-direct", Columns: []string{"stat_month", "net_sales"}, Rows: [][]string{{"2026-01", "1200.00"}, {"2026-02", "980.00"}}},
			{QuestionID: "question-empty", Columns: []string{"net_sales"}, Rows: [][]string{}},
		},
	}
}

func (set Set) Validate() error {
	if set.FixtureVersion != FixtureVersion || !set.Synthetic {
		return errors.New("fixture must be explicitly marked synthetic with the current version")
	}
	if err := set.Release.Validate(); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if len(set.Tenants) == 0 || len(set.Users) == 0 || len(set.Models) == 0 || len(set.Metrics) == 0 || len(set.Dimensions) == 0 || len(set.Questions) == 0 {
		return errors.New("fixture is missing required semantic assets")
	}
	tenantIDs := map[askdata.ID]struct{}{}
	for index, tenant := range set.Tenants {
		if err := tenant.TenantID.Validate(); err != nil {
			return fmt.Errorf("tenants[%d].tenantId: %w", index, err)
		}
		if strings.TrimSpace(tenant.Name) == "" {
			return fmt.Errorf("tenants[%d].name is required", index)
		}
		tenantIDs[tenant.TenantID] = struct{}{}
	}
	for index, user := range set.Users {
		if err := user.ActorID.Validate(); err != nil {
			return fmt.Errorf("users[%d].actorId: %w", index, err)
		}
		if _, exists := tenantIDs[user.TenantID]; !exists {
			return fmt.Errorf("users[%d] references unknown tenant", index)
		}
		if len(user.RoleIDs) == 0 || len(user.DomainIDs) == 0 {
			return fmt.Errorf("users[%d] requires roles and domains", index)
		}
	}
	modelIDs := map[askdata.ID]struct{}{}
	for index, model := range set.Models {
		if err := model.Version.Validate(); err != nil {
			return fmt.Errorf("models[%d].version: %w", index, err)
		}
		if model.Layer != "DWS" && model.Layer != "ADS" {
			return fmt.Errorf("models[%d].layer must be DWS or ADS", index)
		}
		modelIDs[model.Version.VersionID] = struct{}{}
	}
	for index, metric := range set.Metrics {
		if err := metric.Version.Validate(); err != nil {
			return fmt.Errorf("metrics[%d].version: %w", index, err)
		}
		if _, exists := modelIDs[metric.ModelVersionID]; !exists {
			return fmt.Errorf("metrics[%d] references unknown model", index)
		}
	}
	dimensionIDs := map[askdata.ID]struct{}{}
	for index, dimension := range set.Dimensions {
		if err := dimension.Version.Validate(); err != nil {
			return fmt.Errorf("dimensions[%d].version: %w", index, err)
		}
		if _, exists := modelIDs[dimension.ModelVersionID]; !exists {
			return fmt.Errorf("dimensions[%d] references unknown model", index)
		}
		dimensionIDs[dimension.Version.VersionID] = struct{}{}
	}
	for index, member := range set.Members {
		if err := member.Version.Validate(); err != nil {
			return fmt.Errorf("members[%d].version: %w", index, err)
		}
		if _, exists := dimensionIDs[member.DimensionVersionID]; !exists {
			return fmt.Errorf("members[%d] references unknown dimension", index)
		}
		if member.Status != MemberActive && member.Status != MemberExpired {
			return fmt.Errorf("members[%d].status is invalid", index)
		}
	}
	scenarios := map[ScenarioCode]struct{}{}
	questionIDs := map[askdata.ID]struct{}{}
	for index, question := range set.Questions {
		if err := question.QuestionID.Validate(); err != nil {
			return fmt.Errorf("questions[%d].questionId: %w", index, err)
		}
		if strings.TrimSpace(question.Text) == "" {
			return fmt.Errorf("questions[%d].text is required", index)
		}
		if _, exists := tenantIDs[question.TenantID]; !exists {
			return fmt.Errorf("questions[%d] references unknown tenant", index)
		}
		if question.ExpectedDisposition == DispositionDirect {
			if question.ExpectedIR == nil {
				return fmt.Errorf("questions[%d] direct case requires expectedIr", index)
			}
			if err := question.ExpectedIR.Validate(); err != nil {
				return fmt.Errorf("questions[%d].expectedIr: %w", index, err)
			}
			if question.ExpectedIR.SemanticReleaseID != set.Release.ReleaseID || question.ExpectedIR.SemanticContentHash != set.Release.ContentHash {
				return fmt.Errorf("questions[%d].expectedIr release mismatch", index)
			}
		} else if question.ExpectedReasonCode == "" {
			return fmt.Errorf("questions[%d] non-direct case requires expectedReasonCode", index)
		}
		scenarios[question.Scenario] = struct{}{}
		questionIDs[question.QuestionID] = struct{}{}
	}
	for _, required := range []ScenarioCode{ScenarioSameNameMetric, ScenarioSameNameMember, ScenarioUnauthorized, ScenarioJoinFanout, ScenarioEmptyResult, ScenarioExpiredMember} {
		if _, exists := scenarios[required]; !exists {
			return fmt.Errorf("fixture is missing scenario %s", required)
		}
	}
	for index, result := range set.Results {
		if _, exists := questionIDs[result.QuestionID]; !exists {
			return fmt.Errorf("results[%d] references unknown question", index)
		}
		for rowIndex, row := range result.Rows {
			if len(row) != len(result.Columns) {
				return fmt.Errorf("results[%d].rows[%d] column count mismatch", index, rowIndex)
			}
		}
	}
	return nil
}

func version(objectID, versionID askdata.ID) askdata.VersionRef {
	return askdata.VersionRef{ObjectID: objectID, VersionID: versionID, ContentHash: hash(string(versionID))}
}

func hash(value string) askdata.ContentHash {
	return askdata.HashBytes([]byte(FixtureVersion + "\x00" + value))
}
