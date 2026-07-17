package asset

import "errors"

type Table struct {
	ID                  string   `json:"id"`
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
	ManualLocked        bool     `json:"manualLocked"`
	AssetStatus         string   `json:"assetStatus"`
	ManagementStatus    string   `json:"managementStatus"`
	EnrichmentStatus    string   `json:"enrichmentStatus"`
	StructureHash       string   `json:"structureHash"`
	MetadataVersion     int64    `json:"metadataVersion"`
	BusinessVersion     int64    `json:"businessVersion"`
	ColumnCount         int      `json:"columnCount"`
	LastSyncAt          string   `json:"lastSyncAt"`
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
type Diff struct {
	ID           string `json:"id"`
	DataSourceID string `json:"dataSourceId"`
	ObjectType   string `json:"objectType"`
	ObjectKey    string `json:"objectKey"`
	ChangeType   string `json:"changeType"`
	Before       any    `json:"before"`
	After        any    `json:"after"`
	CreatedAt    string `json:"createdAt"`
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
