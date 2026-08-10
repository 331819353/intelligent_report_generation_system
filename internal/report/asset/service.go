package asset

import (
	"bytes"
	"context"
	"errors"
	"strconv"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/publication"
	"intelligent-report-generation-system/internal/report/store"
)

type ArtifactReader interface {
	Read(context.Context, string) ([]byte, error)
}
type ManifestRegistry interface{ Has(string, string) bool }

type Service struct {
	Repository   *PostgresRepository
	Artifacts    ArtifactReader
	Manifests    ManifestRegistry
	Dependencies publication.DependencyValidator
}

func (service Service) Archive(ctx context.Context, identity store.Identity, reportID askdata.ID, reason string) error {
	if service.Repository == nil {
		return errors.New("report asset service is unavailable")
	}
	return service.Repository.Transition(ctx, identity, reportID, "ACTIVE", "ARCHIVED", reason, "ARCHIVED", "")
}

func (service Service) Restore(ctx context.Context, identity store.Identity, reportID askdata.ID, reason string) error {
	if service.Repository == nil {
		return errors.New("report asset service is unavailable")
	}
	if err := validateReason(reason); err != nil {
		return &Error{StableCode: "REPORT_ASSET_REASON_INVALID", Message: "上架原因无效", Err: err}
	}
	version, err := service.Repository.VersionForRestore(ctx, identity, reportID)
	if errors.Is(err, ErrNotFound) {
		// A never-published report has no immutable artifact to validate.
		return service.Repository.Transition(ctx, identity, reportID, "ARCHIVED", "ACTIVE", reason, "RESTORED", "")
	}
	if err != nil {
		return err
	}
	issues := compiler.ValidationIssues{}
	if version.ArtifactState != "READY" {
		issues = append(issues, compiler.ValidationIssue{Code: "REPORT_ARTIFACT_NOT_READY", Path: "artifactState", Message: "published artifact is not ready"})
	}
	raw := []byte(nil)
	if service.Artifacts != nil {
		raw, err = service.Artifacts.Read(ctx, version.ObjectURI)
		if err != nil {
			issues = append(issues, compiler.ValidationIssue{Code: "REPORT_ARTIFACT_UNAVAILABLE", Path: "objectUri", Message: "published artifact cannot be read"})
		}
	} else {
		issues = append(issues, compiler.ValidationIssue{Code: "REPORT_ARTIFACT_UNAVAILABLE", Path: "objectUri", Message: "artifact reader is unavailable"})
	}
	if len(raw) > 0 {
		if len(raw) > report.MaxDefinitionBytes || string(askdata.HashBytes(raw)) != version.DefinitionHash {
			issues = append(issues, compiler.ValidationIssue{Code: "REPORT_ARTIFACT_HASH_MISMATCH", Path: "definitionHash", Message: "published artifact hash verification failed"})
		} else if canonical, hash, normalizeErr := compiler.Normalize(version.Definition); normalizeErr != nil || hash != version.DefinitionHash || !bytes.Equal(canonical, raw) {
			issues = append(issues, compiler.ValidationIssue{Code: "REPORT_ARTIFACT_NON_CANONICAL", Path: "definition", Message: "published artifact is not canonical"})
		}
	}
	if service.Manifests == nil {
		issues = append(issues, compiler.ValidationIssue{Code: "REPORT_COMPONENT_REGISTRY_UNAVAILABLE", Path: "components", Message: "component registry is unavailable"})
	} else {
		for index, component := range version.Definition.Components {
			if !service.Manifests.Has(component.TemplateRef.Type, component.TemplateRef.Version) {
				issues = append(issues, compiler.ValidationIssue{Code: "REPORT_COMPONENT_VERSION_UNAVAILABLE", Path: "components[" + strconv.Itoa(index) + "].templateRef", Message: "pinned component version is unavailable"})
			}
		}
	}
	if service.Dependencies == nil {
		issues = append(issues, compiler.ValidationIssue{Code: "REPORT_DEPENDENCY_LOOKUP_FAILED", Path: "dataContexts", Message: "dependency validator is unavailable"})
	} else {
		issues = append(issues, service.Dependencies.ValidateReportDependencies(ctx, identity, version.Definition)...)
	}
	if len(issues) != 0 {
		return &Error{StableCode: "REPORT_RESTORE_VALIDATION_FAILED", Message: "报告重新上架校验失败", Issues: issues}
	}
	return service.Repository.Transition(ctx, identity, reportID, "ARCHIVED", "ACTIVE", reason, "RESTORED", version.ID)
}
