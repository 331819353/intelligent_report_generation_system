package publication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/store"
)

type Authorizer interface {
	CheckReportPublish(context.Context, store.Identity, askdata.ID) error
}

type DependencyValidator interface {
	ValidateReportDependencies(context.Context, store.Identity, reportmodel.ReportDefinition) compiler.ValidationIssues
}

type InsightValidator interface {
	ValidateReportInsights(context.Context, store.Identity, askdata.ID, bool) compiler.ValidationIssues
}

type Repository interface {
	GetDraftRevision(context.Context, store.Identity, askdata.ID, *int64) (store.Draft, error)
	CreateVersion(context.Context, store.Identity, askdata.ID, store.CreateVersionInput) (store.Version, error)
	CompletePublication(context.Context, store.Identity, askdata.ID, askdata.ID) error
	GetVersion(context.Context, store.Identity, askdata.ID, *int) (store.Version, error)
}

type DefinitionNormalizer interface {
	Normalize(reportmodel.ReportDefinition) (reportmodel.ReportDefinition, []byte, string, error)
}

type DependencyIndexer interface {
	Build(reportmodel.ReportDefinition) (compiler.Indexes, error)
}

type Publisher struct {
	Repository        Repository
	Artifacts         ArtifactStore
	Authorizer        Authorizer
	Dependencies      DependencyValidator
	Insights          InsightValidator
	Normalizer        DefinitionNormalizer
	Indexer           DependencyIndexer
	ArtifactURIPrefix string
}

type PublishRequest struct {
	ReportID                 askdata.ID
	SourceRevisionNo         *int64
	AcknowledgeStaleInsights bool
	// PreviewedDesktop and PreviewedMobile are the publisher's attestation that
	// they looked at both layouts before publishing.
	//
	// These were previously two content hashes the caller had to echo back, and
	// the check was that they equalled the draft hash the caller already held —
	// so the gate could not express what its name claimed. A client-side render
	// hash cannot be a trustworthy gate either, since the client produces it. An
	// honest human attestation is recorded as one, and the responsive layout
	// itself is validated server-side by stage 7.
	PreviewedDesktop    bool
	PreviewedMobile     bool
	RollbackOfVersionNo *int
	RollbackReason      string
	IdempotencyKey      string
}

type StepError struct {
	Step int
	Code string
	Err  error
}

func (err *StepError) Error() string {
	return fmt.Sprintf("publish step %d %s: %v", err.Step, err.Code, err.Err)
}
func (err *StepError) Unwrap() error { return err.Err }

func (publisher *Publisher) Publish(ctx context.Context, identity store.Identity, request PublishRequest) (store.Version, error) {
	if publisher == nil || publisher.Repository == nil || publisher.Artifacts == nil {
		return store.Version{}, errors.New("publisher is not configured")
	}
	if len(request.IdempotencyKey) < 8 || len(request.IdempotencyKey) > 128 {
		return store.Version{}, &StepError{Step: 1, Code: "IDEMPOTENCY_KEY_REQUIRED", Err: errors.New("Idempotency-Key must contain 8..128 characters")}
	}
	if publisher.Authorizer == nil {
		return store.Version{}, &StepError{Step: 1, Code: "REPORT_PUBLISH_FORBIDDEN", Err: errors.New("publication authorizer is unavailable")}
	}
	if err := publisher.Authorizer.CheckReportPublish(ctx, identity, request.ReportID); err != nil {
		return store.Version{}, &StepError{Step: 1, Code: "REPORT_PUBLISH_FORBIDDEN", Err: err}
	}
	draft, err := publisher.Repository.GetDraftRevision(ctx, identity, request.ReportID, request.SourceRevisionNo)
	if err != nil {
		return store.Version{}, &StepError{Step: 2, Code: "REPORT_DRAFT_NOT_FOUND", Err: err}
	}
	return publisher.publishDefinition(ctx, identity, request, draft.Definition, draft.DefinitionHash, draft.RevisionNo)
}

func (publisher *Publisher) publishDefinition(ctx context.Context, identity store.Identity, request PublishRequest, definition reportmodel.ReportDefinition, sourceHash string, sourceRevision int64) (store.Version, error) {
	staticIssues := publicationDefinitionIssues(definition)
	if issues := staticIssues[3]; len(issues) != 0 {
		return store.Version{}, &StepError{Step: 3, Code: publicationStepCode(3), Err: issues}
	}
	if err := validatePublicationDomain(identity, definition); err != nil {
		return store.Version{}, &StepError{Step: 4, Code: "REPORT_DOMAIN_INVALID", Err: err}
	}
	if issues := staticIssues[5]; len(issues) != 0 {
		return store.Version{}, &StepError{Step: 5, Code: publicationStepCode(5), Err: issues}
	}
	if publisher.Dependencies == nil {
		return store.Version{}, &StepError{Step: 6, Code: "REPORT_DEPENDENCY_INVALID", Err: errors.New("dependency validator is unavailable")}
	}
	if issues := publisher.Dependencies.ValidateReportDependencies(ctx, identity, definition); len(issues) != 0 {
		return store.Version{}, &StepError{Step: 6, Code: "REPORT_DEPENDENCY_INVALID", Err: issues}
	}
	if issues := staticIssues[7]; len(issues) != 0 {
		return store.Version{}, &StepError{Step: 7, Code: publicationStepCode(7), Err: issues}
	}
	if !request.PreviewedDesktop || !request.PreviewedMobile {
		return store.Version{}, &StepError{Step: 7, Code: "REPORT_PREVIEW_REQUIRED", Err: errors.New("the publisher must confirm both the desktop and mobile preview")}
	}
	if issues := staticIssues[8]; len(issues) != 0 {
		return store.Version{}, &StepError{Step: 8, Code: publicationStepCode(8), Err: issues}
	}
	if publisher.Insights == nil {
		return store.Version{}, &StepError{Step: 9, Code: "REPORT_INSIGHT_INVALID", Err: errors.New("insight validator is unavailable")}
	}
	if issues := publisher.Insights.ValidateReportInsights(ctx, identity, request.ReportID, request.AcknowledgeStaleInsights); len(issues) != 0 {
		return store.Version{}, &StepError{Step: 9, Code: "REPORT_INSIGHT_STALE", Err: issues}
	}
	normalizer := publisher.Normalizer
	if normalizer == nil {
		normalizer = defaultDefinitionNormalizer{}
	}
	normalized, canonical, definitionHash, err := normalizer.Normalize(definition)
	if err != nil {
		return store.Version{}, &StepError{Step: 10, Code: "REPORT_NORMALIZATION_FAILED", Err: err}
	}
	if definitionHash != sourceHash {
		return store.Version{}, &StepError{Step: 10, Code: "REPORT_SOURCE_HASH_MISMATCH", Err: errors.New("selected revision hash does not match its normalized definition")}
	}
	indexer := publisher.Indexer
	if indexer == nil {
		indexer = defaultDependencyIndexer{}
	}
	indexes, err := indexer.Build(normalized)
	if err != nil {
		return store.Version{}, &StepError{Step: 11, Code: "REPORT_INDEX_BUILD_FAILED", Err: err}
	}
	operation := "PUBLISH"
	if request.RollbackOfVersionNo != nil {
		operation = "ROLLBACK"
	}
	requestBytes, _ := json.Marshal(struct {
		ReportID         askdata.ID `json:"reportId"`
		SourceRevision   int64      `json:"sourceRevision"`
		DefinitionHash   string     `json:"definitionHash"`
		PreviewedDesktop bool       `json:"previewedDesktop"`
		PreviewedMobile  bool       `json:"previewedMobile"`
		Acknowledge      bool       `json:"acknowledgeStaleInsights"`
		RollbackVersion  *int       `json:"rollbackOfVersionNo,omitempty"`
		RollbackReason   string     `json:"rollbackReason,omitempty"`
	}{request.ReportID, sourceRevision, definitionHash, request.PreviewedDesktop, request.PreviewedMobile, request.AcknowledgeStaleInsights,
		request.RollbackOfVersionNo, request.RollbackReason})
	requestHash := askdata.HashBytes(requestBytes)
	versionID := askdata.ID(uuid.NewSHA1(uuid.NameSpaceOID, []byte("report-publication-v1\x00"+
		string(identity.TenantID)+"\x00"+string(request.ReportID)+"\x00"+string(identity.ActorID)+
		"\x00"+operation+"\x00"+request.IdempotencyKey)).String())
	baseKey := path.Join(string(identity.TenantID), string(request.ReportID), string(versionID)+".json")
	objectURI := strings.TrimSuffix(publisher.ArtifactURIPrefix, "/") + "/" + baseKey
	storageKey, err := publicationArtifactStorageKey(objectURI)
	if err != nil {
		return store.Version{}, &StepError{Step: 12, Code: "REPORT_ARTIFACT_URI_INVALID", Err: err}
	}
	temporaryKey := storageKey + ".tmp"
	if err := publisher.Artifacts.PutTemporary(ctx, temporaryKey, canonical); err != nil {
		return store.Version{}, &StepError{Step: 12, Code: "REPORT_ARTIFACT_TEMP_WRITE_FAILED", Err: err}
	}
	version, err := publisher.Repository.CreateVersion(ctx, identity, request.ReportID, store.CreateVersionInput{
		ID: versionID, SourceRevisionNo: sourceRevision, Definition: normalized, ObjectURI: objectURI,
		RollbackOfVersionNo: request.RollbackOfVersionNo, RollbackReason: request.RollbackReason,
		StaleInsightsAcknowledged: request.AcknowledgeStaleInsights,
		Operation:                 operation, IdempotencyKey: request.IdempotencyKey, RequestHash: requestHash,
		Prepared: &store.PreparedDefinition{Definition: normalized, Canonical: canonical, Hash: definitionHash, Indexes: indexes},
	})
	if err != nil {
		cleanupErr := publisher.Artifacts.Delete(context.WithoutCancel(ctx), temporaryKey)
		return store.Version{}, &StepError{Step: 13, Code: "REPORT_VERSION_COMMIT_FAILED", Err: errors.Join(err, cleanupErr)}
	}
	if version.ArtifactState == "READY" {
		_ = publisher.Artifacts.Delete(context.WithoutCancel(ctx), temporaryKey)
		return version, nil
	}
	if err := publisher.Artifacts.Promote(ctx, objectURI+".tmp", objectURI); err != nil {
		if retry, ok := publisher.Repository.(interface {
			MarkPublicationRetry(context.Context, store.Identity, askdata.ID, askdata.ID, error) error
		}); ok {
			_ = retry.MarkPublicationRetry(context.WithoutCancel(ctx), identity, request.ReportID, version.ID, err)
		}
		return version, &StepError{Step: 14, Code: "REPORT_ARTIFACT_PROMOTE_RETRY", Err: err}
	}
	if err := publisher.Repository.CompletePublication(ctx, identity, request.ReportID, version.ID); err != nil {
		if retry, ok := publisher.Repository.(interface {
			MarkPublicationRetry(context.Context, store.Identity, askdata.ID, askdata.ID, error) error
		}); ok {
			_ = retry.MarkPublicationRetry(context.WithoutCancel(ctx), identity, request.ReportID, version.ID, nil)
		}
		return version, &StepError{Step: 14, Code: "REPORT_PUBLISHED_POINTER_FAILED", Err: err}
	}
	version.ArtifactState = "READY"
	return version, nil
}

func publicationArtifactStorageKey(objectURI string) (string, error) {
	parsed, err := url.Parse(objectURI)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || strings.Trim(parsed.Path, "/") == "" {
		return "", errors.New("report artifact URI must contain a scheme, bucket and object path")
	}
	return strings.Trim(parsed.Path, "/"), nil
}

type defaultDefinitionNormalizer struct{}

func (defaultDefinitionNormalizer) Normalize(definition reportmodel.ReportDefinition) (reportmodel.ReportDefinition, []byte, string, error) {
	canonical, hash, err := compiler.Normalize(definition)
	if err != nil {
		return reportmodel.ReportDefinition{}, nil, "", err
	}
	var normalized reportmodel.ReportDefinition
	if err := json.Unmarshal(canonical, &normalized); err != nil {
		return reportmodel.ReportDefinition{}, nil, "", err
	}
	return normalized, canonical, hash, nil
}

type defaultDependencyIndexer struct{}

func (defaultDependencyIndexer) Build(definition reportmodel.ReportDefinition) (compiler.Indexes, error) {
	return compiler.BuildIndexes(definition), nil
}

func publicationDefinitionIssues(definition reportmodel.ReportDefinition) map[int]compiler.ValidationIssues {
	result := map[int]compiler.ValidationIssues{}
	for _, issue := range compiler.ValidateDefinition(definition, nil) {
		step := 3
		switch {
		case strings.HasPrefix(issue.Code, "REPORT_COMPONENT_"), strings.HasPrefix(issue.Code, "REPORT_MANIFEST_"):
			step = 5
		case strings.HasPrefix(issue.Code, "REPORT_LAYOUT_"), strings.HasPrefix(issue.Code, "REPORT_ZONE_"),
			strings.HasPrefix(issue.Code, "REPORT_SLOT_"), strings.HasPrefix(issue.Code, "REPORT_MOBILE_"):
			step = 7
		case strings.HasPrefix(issue.Code, "REPORT_INTERACTION_"):
			step = 8
		}
		result[step] = append(result[step], issue)
	}
	return result
}

func validatePublicationDomain(identity store.Identity, definition reportmodel.ReportDefinition) error {
	if identity.DomainID.Validate() != nil {
		return errors.New("publication requires the selected business domain")
	}
	for _, component := range definition.Components {
		if component.DataBinding == nil || component.DataBinding.SemanticQueryRef == nil {
			continue
		}
		if component.DataBinding.SemanticQueryRef.SemanticIR.DomainID != identity.DomainID {
			return fmt.Errorf("component %q belongs to another business domain", component.ID)
		}
	}
	return nil
}

func publicationStepCode(step int) string {
	switch step {
	case 5:
		return "REPORT_COMPONENT_INVALID"
	case 7:
		return "REPORT_LAYOUT_INVALID"
	case 8:
		return "REPORT_INTERACTION_INVALID"
	default:
		return "REPORT_SCHEMA_INVALID"
	}
}

func (publisher *Publisher) Rollback(ctx context.Context, identity store.Identity, reportID askdata.ID, targetVersionNo int, reason string, acknowledgeStale bool) (store.Version, error) {
	trimmedReason := strings.TrimSpace(reason)
	if trimmedReason == "" {
		return store.Version{}, &StepError{Step: 1, Code: "REPORT_ROLLBACK_REASON_REQUIRED", Err: errors.New("rollback reason is required")}
	}
	if utf8.RuneCountInString(trimmedReason) > 1000 || strings.IndexFunc(trimmedReason, unicode.IsControl) >= 0 {
		return store.Version{}, &StepError{Step: 1, Code: "REPORT_ROLLBACK_REASON_INVALID", Err: errors.New("rollback reason must contain at most 1000 characters and no control characters")}
	}
	if publisher == nil || publisher.Repository == nil || publisher.Artifacts == nil {
		return store.Version{}, errors.New("publisher is not configured")
	}
	idempotencyKey := requestIdempotencyKey(ctx)
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return store.Version{}, &StepError{Step: 1, Code: "IDEMPOTENCY_KEY_REQUIRED", Err: errors.New("Idempotency-Key must contain 8..128 characters")}
	}
	if publisher.Authorizer == nil {
		return store.Version{}, &StepError{Step: 1, Code: "REPORT_PUBLISH_FORBIDDEN", Err: errors.New("publication authorizer is unavailable")}
	}
	if err := publisher.Authorizer.CheckReportPublish(ctx, identity, reportID); err != nil {
		return store.Version{}, &StepError{Step: 1, Code: "REPORT_PUBLISH_FORBIDDEN", Err: err}
	}
	if targetVersionNo < 1 {
		return store.Version{}, &StepError{Step: 2, Code: "REPORT_ROLLBACK_VERSION_INVALID", Err: errors.New("targetVersionNo must be positive")}
	}
	target, err := publisher.Repository.GetVersion(ctx, identity, reportID, &targetVersionNo)
	if err != nil {
		return store.Version{}, &StepError{Step: 2, Code: "REPORT_ROLLBACK_VERSION_NOT_FOUND", Err: err}
	}
	if target.ArtifactState != "READY" {
		return store.Version{}, &StepError{Step: 2, Code: "REPORT_ROLLBACK_VERSION_NOT_READY", Err: errors.New("rollback target is not a completed published version")}
	}
	return publisher.publishDefinition(ctx, identity, PublishRequest{
		ReportID: reportID, AcknowledgeStaleInsights: acknowledgeStale,
		// A rollback republishes a version that was already previewed and
		// attested when it was first published.
		PreviewedDesktop: true, PreviewedMobile: true,
		RollbackOfVersionNo: &targetVersionNo, RollbackReason: trimmedReason,
		IdempotencyKey: idempotencyKey,
	}, target.Definition, target.DefinitionHash, target.SourceRevisionNo)
}

type idempotencyContextKey struct{}

func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, idempotencyContextKey{}, strings.TrimSpace(key))
}

func requestIdempotencyKey(ctx context.Context) string {
	value, _ := ctx.Value(idempotencyContextKey{}).(string)
	return value
}

// Compensate retries only the recoverable object promotion and pointer switch.
func (publisher *Publisher) Compensate(ctx context.Context, identity store.Identity, reportID askdata.ID, version store.Version) error {
	if version.ReportID != reportID || version.ObjectURI == "" {
		return errors.New("report publication compensation target is invalid")
	}
	if err := publisher.Artifacts.Promote(ctx, version.ObjectURI+".tmp", version.ObjectURI); err != nil {
		return err
	}
	return publisher.Repository.CompletePublication(ctx, identity, reportID, version.ID)
}
