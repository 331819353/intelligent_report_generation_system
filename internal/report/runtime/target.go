package runtime

import (
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/store"
)

// ExecutionTarget is what the runtime executes.
//
// A report is authored as a draft and consumed as an immutable published
// version. Both are the same Report Definition and must therefore run through
// the same filter resolution, planning, batching and policy enforcement — a
// second execution path for previews would drift from the published one and
// defeat the point of previewing. What legitimately differs is provenance
// (which revision or version produced the definition) and which bindings can be
// executed at all, so those differences are modelled here explicitly rather
// than being implied by a nil field somewhere downstream.
type ExecutionTarget struct {
	ReportID       askdata.ID
	Definition     report.ReportDefinition
	DefinitionHash string

	// VersionID and VersionNo identify an immutable published artifact. They are
	// empty for a draft.
	VersionID askdata.ID
	VersionNo int

	// RevisionNo identifies the editable draft. It is zero for a version.
	RevisionNo int64

	// Draft distinguishes the two rather than leaving callers to infer it from
	// an empty VersionID, so a missing pin can never be read as "this is a
	// draft" by accident.
	Draft bool
}

// PublishedTarget executes the hash-verified artifact the loader returned.
func PublishedTarget(loaded LoadedReport) ExecutionTarget {
	return ExecutionTarget{
		ReportID: loaded.ReportID, Definition: loaded.Definition,
		DefinitionHash: loaded.DefinitionHash,
		VersionID:      loaded.VersionID, VersionNo: loaded.VersionNo,
	}
}

// DraftTarget executes the current editable definition so an author can see
// real, permission-scoped data while binding fields instead of having to
// publish a version to find out whether the binding works.
func DraftTarget(
	reportID askdata.ID,
	definition report.ReportDefinition,
	definitionHash string,
	revisionNo int64,
) ExecutionTarget {
	return ExecutionTarget{
		ReportID: reportID, Definition: definition,
		DefinitionHash: definitionHash, RevisionNo: revisionNo, Draft: true,
	}
}

func (target ExecutionTarget) Validate() error {
	if target.ReportID.Validate() != nil {
		return errors.New("report execution target is invalid")
	}
	if askdata.ContentHash(target.DefinitionHash).Validate() != nil {
		return errors.New("report execution target definition hash is invalid")
	}
	if target.Draft {
		if target.VersionID != "" || target.VersionNo != 0 || target.RevisionNo < 0 {
			return errors.New("draft execution target must not carry a published version")
		}
		return nil
	}
	if target.VersionID.Validate() != nil || target.VersionNo < 1 || target.RevisionNo != 0 {
		return errors.New("published execution target requires an immutable version")
	}
	return nil
}

// PolicyScopeHash scopes batch deduplication and any downstream cache to the
// authenticated viewer and the exact definition being executed.
//
// Draft and published scopes use different domain-separation prefixes, so a
// preview result can never be served for a published run or vice versa. A draft
// additionally keys on the definition hash rather than the revision number:
// editing the report changes the scope immediately, so a result computed before
// a binding change can never be reused after it.
func (target ExecutionTarget) PolicyScopeHash(identity store.Identity) (string, error) {
	if identity.Validate() != nil {
		return "", errors.New("report runtime viewer identity is invalid")
	}
	if err := target.Validate(); err != nil {
		return "", err
	}
	prefix := "report-runtime-policy-v1"
	discriminator := string(target.VersionID)
	if target.Draft {
		prefix = "report-draft-policy-v1"
		discriminator = target.DefinitionHash
	}
	return string(askdata.HashBytes([]byte(
		prefix + "\x00" + string(identity.TenantID) + "\x00" +
			string(identity.DomainID) + "\x00" + string(identity.ActorID) + "\x00" +
			string(target.ReportID) + "\x00" + discriminator,
	))), nil
}

// CodeDraftPreviewRequiresPublish marks a component that cannot run against a
// draft. A SEMANTIC_IR binding is pinned to a governed query artifact that the
// database only releases for an immutable report version (see the SECURITY
// DEFINER function behind LoadQueryArtifact), so there is nothing to execute
// until the report is published. Reporting that explicitly is the honest
// outcome; silently returning no rows would read as "this metric is empty".
const CodeDraftPreviewRequiresPublish = "REPORT_DRAFT_PREVIEW_REQUIRES_PUBLISH"

// executableAgainst reports whether a component's binding can run against this
// target, and if not, why.
func (target ExecutionTarget) executableAgainst(component report.Component) string {
	if !target.Draft || component.DataBinding == nil {
		return ""
	}
	if component.DataBinding.BindingMode == report.BindingSemanticIR {
		return CodeDraftPreviewRequiresPublish
	}
	return ""
}

// pin binds every planned query to the definition that produced it. Published
// queries carry their immutable version; draft queries deliberately carry no
// version, because inventing one would let a preview masquerade as a published
// run in audit and artifact lookups.
func (target ExecutionTarget) pin(plan *ExecutionPlan) error {
	if plan == nil {
		return fmt.Errorf("report execution plan is required")
	}
	if err := target.Validate(); err != nil {
		return err
	}
	for index := range plan.Components {
		if plan.Components[index].Query == nil {
			continue
		}
		plan.Components[index].Query.ReportID = target.ReportID
		plan.Components[index].Query.ReportVersionID = target.VersionID
		plan.Components[index].Query.Draft = target.Draft
	}
	return nil
}
