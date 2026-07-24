// Package materializationworker executes immutable, server-registered
// materialization plans. It resolves every physical input from trusted
// PostgreSQL metadata and never accepts SQL or relation names from a caller.
package materializationworker

import (
	"context"
	"fmt"
	"time"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/querycompiler"
	"intelligent-report-generation-system/internal/warehouse"
)

const (
	CodeTargetVersionUnavailable        = "TARGET_VERSION_UNAVAILABLE"
	CodeTargetContractChanged           = "TARGET_CONTRACT_CHANGED"
	CodePostgresExecutionRequired       = "POSTGRES_EXECUTION_REQUIRED"
	CodeRefreshModeUnsupported          = "REFRESH_MODE_UNSUPPORTED"
	CodePartitionedTableUnsupported     = "PARTITIONED_TABLE_UNSUPPORTED"
	CodeODSExcelUnsupported             = "ODS_EXCEL_MATERIALIZATION_UNSUPPORTED"
	CodeODSSourceStagingNotConfigured   = "ODS_SOURCE_STAGING_NOT_CONFIGURED"
	CodeODSUnsupported                  = "ODS_MATERIALIZATION_UNSUPPORTED"
	CodeODSSourceContractInvalid        = "ODS_SOURCE_CONTRACT_INVALID"
	CodeODSStagingFailed                = "ODS_STAGING_FAILED"
	CodeODSStagingTimeout               = "ODS_STAGING_TIMEOUT"
	CodeUpstreamUnavailable             = "UPSTREAM_MATERIALIZATION_UNAVAILABLE"
	CodeUpstreamSnapshotChanged         = "UPSTREAM_SNAPSHOT_CHANGED"
	CodeUpstreamContractInvalid         = "UPSTREAM_CONTRACT_INVALID"
	CodeTrustedPlanInvalid              = "TRUSTED_PLAN_INVALID"
	CodeWarehouseBuildTimeout           = "WAREHOUSE_BUILD_TIMEOUT"
	CodeWarehouseBuildFailed            = "WAREHOUSE_BUILD_FAILED"
	CodeQualityGateFailed               = "QUALITY_GATE_FAILED"
	CodeMaterializationActivationFailed = "MATERIALIZATION_ACTIVATION_FAILED"
)

// ExecutionError is safe to persist on a build run. Cause is available to
// worker logs, while Message deliberately contains no SQL or physical name.
type ExecutionError struct {
	Code    string
	Message string
	Cause   error
}

func (err *ExecutionError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

func (err *ExecutionError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

func executionError(code, message string, cause error) error {
	return &ExecutionError{Code: code, Message: message, Cause: cause}
}

// ResolvedBuild contains only server-loaded logical and physical contracts.
// Tables are populated from ACTIVE materialization metadata under tenant RLS.
type ResolvedBuild struct {
	Document      dataset.Document
	Tables        map[string]querycompiler.TableRef
	SchemaHash    string
	VersionNo     int
	InputRowCount map[int]int64
}

type Store interface {
	ListTenantIDs(context.Context) ([]string, error)
	Claim(context.Context, string, string, time.Duration) (*materialization.Claim, error)
	Heartbeat(context.Context, materialization.Claim, time.Duration) (materialization.Claim, error)
	StartNode(context.Context, materialization.Claim, string) error
	FinishNode(context.Context, materialization.Claim, string, materialization.NodeResult) error
	Fail(context.Context, materialization.Claim, string, string, []materialization.QualityResult) error
	Activate(context.Context, materialization.Claim, materialization.Activation) (materialization.Materialization, error)
}

type Resolver interface {
	Resolve(context.Context, materialization.Claim) (ResolvedBuild, error)
}

type Builder interface {
	Build(context.Context, warehouse.BuildInput) (warehouse.BuildResult, error)
}

func validateDependencies(store Store, resolver Resolver, builder Builder) error {
	if store == nil || resolver == nil || builder == nil {
		return fmt.Errorf("materialization worker dependencies are not configured")
	}
	return nil
}
