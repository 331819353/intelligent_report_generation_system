package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/store"
)

type VersionRepository interface {
	GetVersion(context.Context, store.Identity, askdata.ID, *int) (store.Version, error)
}

type ArtifactReader interface {
	Read(context.Context, string) ([]byte, error)
}

type ManifestRegistry interface {
	Has(componentType, version string) bool
}

type Loader struct {
	Versions  VersionRepository
	Artifacts ArtifactReader
	Manifests ManifestRegistry
}

func (loader Loader) Load(ctx context.Context, identity store.Identity, reportID askdata.ID, versionNo *int) (LoadedReport, error) {
	if loader.Versions == nil {
		return LoadedReport{}, errors.New("version repository is required")
	}
	version, err := loader.Versions.GetVersion(ctx, identity, reportID, versionNo)
	if err != nil {
		return LoadedReport{}, err
	}
	if version.ArtifactState != "READY" {
		return LoadedReport{}, NewError("REPORT_ARTIFACT_NOT_READY", "报告发布制品尚未就绪", nil)
	}
	raw := []byte(version.DefinitionRaw)
	if loader.Artifacts != nil {
		if artifact, artifactErr := loader.Artifacts.Read(ctx, version.ObjectURI); artifactErr == nil {
			raw = artifact
		}
	}
	if len(raw) == 0 || len(raw) > report.MaxDefinitionBytes {
		return LoadedReport{}, NewError("REPORT_ARTIFACT_SIZE_INVALID", "报告发布制品大小无效", nil)
	}
	if string(askdata.HashBytes(raw)) != version.DefinitionHash {
		return LoadedReport{}, NewError("REPORT_ARTIFACT_HASH_MISMATCH", "报告发布制品校验失败", nil)
	}
	definition, err := report.Decode(raw)
	if err != nil {
		return LoadedReport{}, NewError("REPORT_ARTIFACT_SCHEMA_INVALID", "报告发布制品结构无效", err)
	}
	// Ensure the JSON consumed by the runtime really is the bytes whose hash
	// was verified; never trust a separately decoded draft object.
	canonical, canonicalHash, canonicalErr := compiler.Normalize(definition)
	if canonicalErr != nil || canonicalHash != version.DefinitionHash || !bytes.Equal(canonical, raw) {
		return LoadedReport{}, NewError("REPORT_ARTIFACT_NON_CANONICAL", "报告发布制品不是规范版本", canonicalErr)
	}
	if loader.Manifests != nil {
		for _, component := range definition.Components {
			if !loader.Manifests.Has(component.TemplateRef.Type, component.TemplateRef.Version) {
				return LoadedReport{}, NewError(
					"REPORT_COMPONENT_VERSION_UNAVAILABLE",
					fmt.Sprintf("组件版本 %s@%s 不可用，请联系管理员", component.TemplateRef.Type, component.TemplateRef.Version),
					nil,
				)
			}
		}
	}
	return LoadedReport{ReportID: reportID, VersionID: version.ID, VersionNo: version.VersionNo, DefinitionHash: version.DefinitionHash, Definition: definition}, nil
}
