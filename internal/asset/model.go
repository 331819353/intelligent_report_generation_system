package asset

import (
	"errors"
	"fmt"
	"strings"
)

type Table struct {
	ID string `json:"id"`
	// OriginTableID links a virtual ODS dataset version back to the physical
	// metadata asset it represents. Candidate de-duplication uses this provenance;
	// a table name is never an asset identity because different sources may expose
	// same-named tables with different fields or row scopes.
	OriginTableID       string   `json:"originTableId,omitempty"`
	DataSourceID        string   `json:"dataSourceId"`
	DataSourceName      string   `json:"dataSourceName"`
	DataSourceType      string   `json:"dataSourceType"`
	FileVersionID       string   `json:"fileVersionId,omitempty"`
	CatalogName         string   `json:"catalogName"`
	SchemaName          string   `json:"schemaName"`
	TableName           string   `json:"tableName"`
	TableType           string   `json:"tableType"`
	SourceComment       string   `json:"sourceComment"`
	BusinessName        string   `json:"businessName"`
	BusinessDescription string   `json:"businessDescription"`
	Tags                []string `json:"tags"`
	SensitivityLevel    string   `json:"sensitivityLevel"`
	Visibility          string   `json:"visibility"`
	// ManualLocked 仅用于兼容历史表记录的数据库扫描，不再作为表级业务能力暴露。
	ManualLocked     bool   `json:"-"`
	AssetStatus      string `json:"assetStatus"`
	ManagementStatus string `json:"managementStatus"`
	EnrichmentStatus string `json:"enrichmentStatus"`
	StructureHash    string `json:"structureHash"`
	MetadataVersion  int64  `json:"metadataVersion"`
	BusinessVersion  int64  `json:"businessVersion"`
	ColumnCount      int    `json:"columnCount"`
	// LockedColumnCount 是已锁定人工定义的字段数。锁定是字段级保护：这些字段在
	// 刷新时保留人工定义并跳过 AI 覆盖，同表其余字段照常刷新。
	LockedColumnCount int    `json:"lockedColumnCount"`
	LastSyncAt        string `json:"lastSyncAt"`
	// RefreshState / RefreshStage / RefreshNote 只在该表参与一个尚未结束的元数据
	// 批任务时非空。它们让页面在整批完成之前就能逐表展示排队、处理中、成功、
	// 失败和跳过（例如已锁定人工定义）的真实进度，而 EnrichmentStatus 仍然只表达
	// 已落库的完善结果，下游建模判断不受运行中任务影响。
	RefreshState string `json:"refreshState,omitempty"`
	RefreshStage string `json:"refreshStage,omitempty"`
	RefreshNote  string `json:"refreshNote,omitempty"`
	// PrimaryKeyColumns / ForeignKeys are the declared key constraints captured at
	// metadata sync. They are the strongest deterministic join evidence a raw
	// table offers and feed the modeling blueprint's join candidates.
	PrimaryKeyColumns []string     `json:"primaryKeyColumns,omitempty"`
	ForeignKeys       []ForeignKey `json:"foreignKeys,omitempty"`
}

// ForeignKey is one declared FOREIGN KEY constraint. ReferencedTable is the
// bare table name reported by the source (schema qualification is not
// guaranteed), so consumers match it against table names, not ids.
type ForeignKey struct {
	Name              string   `json:"name,omitempty"`
	Columns           []string `json:"columns"`
	ReferencedTable   string   `json:"referencedTable"`
	ReferencedColumns []string `json:"referencedColumns"`
}
type Column struct {
	ID                  string   `json:"id"`
	TableID             string   `json:"tableId"`
	ColumnName          string   `json:"columnName"`
	OrdinalPosition     int      `json:"ordinalPosition"`
	SourceComment       string   `json:"sourceComment"`
	NativeType          string   `json:"nativeType"`
	CanonicalType       string   `json:"canonicalType"`
	Nullable            bool     `json:"nullable"`
	BusinessName        string   `json:"businessName"`
	BusinessDescription string   `json:"businessDescription"`
	Tags                []string `json:"tags"`
	SensitivityLevel    string   `json:"sensitivityLevel"`
	SemanticType        string   `json:"semanticType"`
	ManualLocked        bool     `json:"manualLocked"`
	AssetStatus         string   `json:"assetStatus"`
	BusinessVersion     int64    `json:"businessVersion"`
	// Declared key flags from the source schema (metadata_columns.is_*).
	PrimaryKey bool `json:"primaryKey"`
	ForeignKey bool `json:"foreignKey"`
	Unique     bool `json:"unique"`
}
type Search struct {
	Query, DataSourceID, SourceType, Status, Sensitivity, Tag, Visibility, ManagementStatus string
	EnrichedOnly                                                                            bool
	Limit, Offset                                                                           int
}
type BusinessMetadata struct {
	BusinessName        string   `json:"businessName"`
	BusinessDescription string   `json:"businessDescription"`
	Tags                []string `json:"tags"`
	SensitivityLevel    string   `json:"sensitivityLevel"`
	SemanticType        string   `json:"semanticType,omitempty"`
	Visibility          string   `json:"visibility,omitempty"`
	ManualLocked        bool     `json:"manualLocked"`
	ExpectedVersion     int64    `json:"expectedVersion"`
}
type ManualCompletionInput struct {
	ExpectedVersion       int64  `json:"expectedVersion"`
	ExpectedStructureHash string `json:"expectedStructureHash"`
}
type ManualCompletionIncompleteError struct {
	Missing []string
}

func (e *ManualCompletionIncompleteError) Error() string {
	return fmt.Sprintf("手工完善信息不完整：%s", strings.Join(e.Missing, "、"))
}

type Diff struct {
	ID           string       `json:"id"`
	DataSourceID string       `json:"dataSourceId"`
	ObjectType   string       `json:"objectType"`
	ObjectKey    string       `json:"objectKey"`
	ChangeType   string       `json:"changeType"`
	Before       any          `json:"before"`
	After        any          `json:"after"`
	CreatedAt    string       `json:"createdAt"`
	Breaking     bool         `json:"breaking"`
	ImpactCount  int          `json:"impactCount"`
	StaleCount   int          `json:"staleCount"`
	Impact       []Dependency `json:"impact,omitempty"`
}
type Dependency struct {
	ID             string `json:"id"`
	DownstreamType string `json:"downstreamType"`
	DownstreamID   string `json:"downstreamId"`
	DownstreamName string `json:"downstreamName"`
	Kind           string `json:"kind"`
	CreatedAt      string `json:"createdAt"`
}

// Validate 校验人工维护的业务元数据、标签和敏感级别。
func (m BusinessMetadata) Validate(column bool) error {
	if m.ExpectedVersion <= 0 {
		return errors.New("expectedVersion must be greater than zero")
	}
	switch m.SensitivityLevel {
	case "PUBLIC", "INTERNAL", "CONFIDENTIAL", "RESTRICTED":
	default:
		return errors.New("invalid sensitivityLevel")
	}
	if !column {
		switch m.Visibility {
		case "PRIVATE", "TENANT_PUBLIC":
		default:
			return errors.New("invalid visibility")
		}
	} else {
		switch m.SemanticType {
		case "", "DATE", "TIME", "DATETIME", "REGION", "COMPANY_NAME", "AMOUNT", "PERCENTAGE", "IDENTIFIER", "CATEGORY", "QUANTITY", "BOOLEAN", "TEXT":
		default:
			return errors.New("invalid semanticType")
		}
	}
	if len(m.Tags) > 30 {
		return errors.New("too many tags")
	}
	for _, tag := range m.Tags {
		if len(tag) == 0 || len(tag) > 50 {
			return errors.New("invalid tag")
		}
	}
	return nil
}

func (m ManualCompletionInput) Validate() error {
	if m.ExpectedVersion <= 0 {
		return errors.New("expectedVersion must be greater than zero")
	}
	if len(m.ExpectedStructureHash) != 64 {
		return errors.New("expectedStructureHash must be a 64-character hash")
	}
	return nil
}
