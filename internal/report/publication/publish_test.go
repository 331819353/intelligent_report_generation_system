package publication

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/store"
)

type authorizerFunc func(context.Context, store.Identity, askdata.ID) error

func (function authorizerFunc) CheckReportPublish(ctx context.Context, identity store.Identity, reportID askdata.ID) error {
	return function(ctx, identity, reportID)
}

type dependencyValidatorFunc func(context.Context, store.Identity, reportmodel.ReportDefinition) compiler.ValidationIssues

func (function dependencyValidatorFunc) ValidateReportDependencies(ctx context.Context, identity store.Identity, definition reportmodel.ReportDefinition) compiler.ValidationIssues {
	return function(ctx, identity, definition)
}

type insightValidatorFunc func(context.Context, store.Identity, askdata.ID, bool) compiler.ValidationIssues

func (function insightValidatorFunc) ValidateReportInsights(ctx context.Context, identity store.Identity, reportID askdata.ID, acknowledge bool) compiler.ValidationIssues {
	return function(ctx, identity, reportID, acknowledge)
}

type normalizerFunc func(reportmodel.ReportDefinition) (reportmodel.ReportDefinition, []byte, string, error)

func (function normalizerFunc) Normalize(definition reportmodel.ReportDefinition) (reportmodel.ReportDefinition, []byte, string, error) {
	return function(definition)
}

type indexerFunc func(reportmodel.ReportDefinition) (compiler.Indexes, error)

func (function indexerFunc) Build(definition reportmodel.ReportDefinition) (compiler.Indexes, error) {
	return function(definition)
}

type publicationRepositoryFixture struct {
	draft             store.Draft
	versions          map[askdata.ID]store.Version
	inputs            []store.CreateVersionInput
	requestedRevision *int64
	failDraft         error
	failCreate        error
	failComplete      error
	completeCalls     int
	retryCalls        int
	claimReturned     bool
}

func (repository *publicationRepositoryFixture) GetDraftRevision(_ context.Context, _ store.Identity, _ askdata.ID, revision *int64) (store.Draft, error) {
	if revision != nil {
		value := *revision
		repository.requestedRevision = &value
	}
	if repository.failDraft != nil {
		return store.Draft{}, repository.failDraft
	}
	return repository.draft, nil
}

func (repository *publicationRepositoryFixture) CreateVersion(_ context.Context, identity store.Identity, reportID askdata.ID, input store.CreateVersionInput) (store.Version, error) {
	repository.inputs = append(repository.inputs, input)
	if repository.failCreate != nil {
		return store.Version{}, repository.failCreate
	}
	if existing, ok := repository.versions[input.ID]; ok {
		existing.Replayed = true
		return existing, nil
	}
	version := store.Version{
		ID: input.ID, ReportID: reportID, VersionNo: len(repository.versions) + 1,
		SourceRevisionNo: input.SourceRevisionNo, Definition: input.Definition,
		DefinitionHash: input.Prepared.Hash, SchemaVersion: reportmodel.SchemaVersion,
		ObjectURI: input.ObjectURI, PublishedBy: identity.ActorID, PublishedAt: time.Now().UTC(),
		RollbackOfVersionNo: input.RollbackOfVersionNo, RollbackReason: input.RollbackReason,
		StaleInsightsAcknowledged: input.StaleInsightsAcknowledged, ArtifactState: "PENDING",
	}
	repository.versions[input.ID] = version
	return version, nil
}

func (repository *publicationRepositoryFixture) CompletePublication(_ context.Context, _ store.Identity, _ askdata.ID, versionID askdata.ID) error {
	repository.completeCalls++
	if repository.failComplete != nil {
		return repository.failComplete
	}
	version := repository.versions[versionID]
	version.ArtifactState = "READY"
	repository.versions[versionID] = version
	return nil
}

func (repository *publicationRepositoryFixture) GetVersion(_ context.Context, _ store.Identity, _ askdata.ID, versionNo *int) (store.Version, error) {
	for _, version := range repository.versions {
		if versionNo == nil || version.VersionNo == *versionNo {
			return version, nil
		}
	}
	return store.Version{}, store.ErrNotFound
}

func (repository *publicationRepositoryFixture) MarkPublicationRetry(_ context.Context, _ store.Identity, _ askdata.ID, versionID askdata.ID, _ error) error {
	repository.retryCalls++
	version := repository.versions[versionID]
	version.ArtifactState = "RETRY"
	repository.versions[versionID] = version
	return nil
}

func (repository *publicationRepositoryFixture) PublicationTenantIDs(context.Context) ([]string, error) {
	return []string{string(repository.draft.TenantID)}, nil
}

func (repository *publicationRepositoryFixture) ClaimPublication(_ context.Context, _ string, _ time.Duration) (*store.PublicationClaim, error) {
	if repository.claimReturned {
		return nil, nil
	}
	ids := make([]askdata.ID, 0, len(repository.versions))
	for id, version := range repository.versions {
		if version.ArtifactState != "READY" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	repository.claimReturned = true
	version := repository.versions[ids[0]]
	return &store.PublicationClaim{
		Identity: store.Identity{TenantID: repository.draft.TenantID, ActorID: version.PublishedBy},
		ReportID: version.ReportID, Version: version, LeaseToken: "publication-lease",
	}, nil
}

func (repository *publicationRepositoryFixture) CompletePublicationClaim(_ context.Context, claim store.PublicationClaim) error {
	version := repository.versions[claim.Version.ID]
	version.ArtifactState = "READY"
	repository.versions[claim.Version.ID] = version
	repository.completeCalls++
	return nil
}

func (repository *publicationRepositoryFixture) FailPublicationClaim(context.Context, store.PublicationClaim, error) error {
	return nil
}

type artifactStoreFixture struct {
	objects     map[string][]byte
	failPut     error
	failPromote error
	failDelete  error
}

func (artifacts *artifactStoreFixture) PutTemporary(_ context.Context, key string, body []byte) error {
	if artifacts.failPut != nil {
		return artifacts.failPut
	}
	artifacts.objects[artifactFixtureKey(key)] = append([]byte(nil), body...)
	return nil
}

func (artifacts *artifactStoreFixture) Promote(_ context.Context, temporaryKey, finalKey string) error {
	if artifacts.failPromote != nil {
		return artifacts.failPromote
	}
	temporaryKey, finalKey = artifactFixtureKey(temporaryKey), artifactFixtureKey(finalKey)
	if _, exists := artifacts.objects[finalKey]; exists {
		delete(artifacts.objects, temporaryKey)
		return nil
	}
	body, exists := artifacts.objects[temporaryKey]
	if !exists {
		return errors.New("temporary artifact is missing")
	}
	artifacts.objects[finalKey] = append([]byte(nil), body...)
	delete(artifacts.objects, temporaryKey)
	return nil
}

func (artifacts *artifactStoreFixture) Delete(_ context.Context, key string) error {
	if artifacts.failDelete != nil {
		return artifacts.failDelete
	}
	delete(artifacts.objects, artifactFixtureKey(key))
	return nil
}

func (artifacts *artifactStoreFixture) Read(_ context.Context, key string) ([]byte, error) {
	body, exists := artifacts.objects[artifactFixtureKey(key)]
	if !exists {
		return nil, errors.New("artifact is missing")
	}
	return append([]byte(nil), body...), nil
}

func artifactFixtureKey(value string) string {
	parsed, err := url.Parse(value)
	if err == nil && parsed.Scheme != "" {
		return strings.Trim(parsed.Path, "/")
	}
	return strings.Trim(value, "/")
}

type publicationHarness struct {
	publisher  *Publisher
	repository *publicationRepositoryFixture
	artifacts  *artifactStoreFixture
	identity   store.Identity
	request    PublishRequest
}

func newPublicationHarness(t *testing.T) publicationHarness {
	t.Helper()
	definition := publicationDefinition(t, "simple-report.json")
	definition.Provenance.AnalysisMethodVersions = []reportmodel.AnalysisMethodVersionReference{{AnalysisMethod: "PERIOD_COMPARISON", Version: "1.2.0"}}
	definition.Provenance.PromptVersions = []string{"insight-monthly-v2"}
	definition.Provenance.ModelPolicies = []string{"narrative-standard-v1"}
	canonical, hash, err := compiler.Normalize(definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(canonical, &definition); err != nil {
		t.Fatal(err)
	}
	repository := &publicationRepositoryFixture{
		draft: store.Draft{ReportID: definition.Metadata.ID, TenantID: "tenant_report", Definition: definition,
			DefinitionHash: hash, SchemaVersion: reportmodel.SchemaVersion, RevisionNo: 4, UpdatedBy: "publisher"},
		versions: map[askdata.ID]store.Version{},
	}
	artifacts := &artifactStoreFixture{objects: map[string][]byte{}}
	publisher := &Publisher{
		Repository: repository, Artifacts: artifacts,
		Authorizer: authorizerFunc(func(context.Context, store.Identity, askdata.ID) error { return nil }),
		Dependencies: dependencyValidatorFunc(func(context.Context, store.Identity, reportmodel.ReportDefinition) compiler.ValidationIssues {
			return nil
		}),
		Insights:          insightValidatorFunc(func(context.Context, store.Identity, askdata.ID, bool) compiler.ValidationIssues { return nil }),
		ArtifactURIPrefix: "memory://report-artifacts/report-v2",
	}
	return publicationHarness{
		publisher: publisher, repository: repository, artifacts: artifacts,
		identity: store.Identity{TenantID: "tenant_report", ActorID: "publisher", DomainID: "domain_report"},
		request: PublishRequest{ReportID: definition.Metadata.ID, DesktopPreviewHash: askdata.ContentHash(hash),
			MobilePreviewHash: askdata.ContentHash(hash), IdempotencyKey: "publish-key-001"},
	}
}

func TestPublisherSuccessIdempotencyAndFrozenVersionPins(t *testing.T) {
	harness := newPublicationHarness(t)
	beforeHash := harness.repository.draft.DefinitionHash
	first, err := harness.publisher.Publish(context.Background(), harness.identity, harness.request)
	if err != nil || first.ArtifactState != "READY" {
		t.Fatalf("first publication = %#v, %v", first, err)
	}
	second, err := harness.publisher.Publish(context.Background(), harness.identity, harness.request)
	if err != nil || !second.Replayed || second.ID != first.ID || len(harness.repository.versions) != 1 {
		t.Fatalf("idempotent publication = %#v, %v; versions=%d", second, err, len(harness.repository.versions))
	}
	if harness.repository.draft.DefinitionHash != beforeHash || harness.repository.completeCalls != 1 {
		t.Fatal("publication mutated the draft or switched the pointer more than once")
	}
	finalBody, err := harness.artifacts.Read(context.Background(), first.ObjectURI)
	if err != nil {
		t.Fatal(err)
	}
	var artifact reportmodel.ReportDefinition
	if err := json.Unmarshal(finalBody, &artifact); err != nil {
		t.Fatal(err)
	}
	if len(artifact.Provenance.AnalysisMethodVersions) != 1 || len(artifact.Provenance.PromptVersions) != 1 || len(artifact.Provenance.ModelPolicies) != 1 {
		t.Fatalf("published version pins = %#v", artifact.Provenance)
	}
	if len(harness.repository.inputs) != 2 || harness.repository.inputs[0].Prepared == nil {
		t.Fatal("publication did not pass its exact normalized artifact and indexes to storage")
	}
	dependencies := map[string]bool{}
	for _, dependency := range harness.repository.inputs[0].Prepared.Indexes.Dependencies {
		dependencies[dependency.DependencyType] = true
	}
	for _, kind := range []string{"REPORT_TEMPLATE", "THEME", "COMPONENT_TEMPLATE", "DATASET_VERSION", "ANALYSIS_METHOD", "PROMPT_VERSION", "MODEL_POLICY"} {
		if !dependencies[kind] {
			t.Fatalf("published dependency index is missing %s", kind)
		}
	}
	for key := range harness.artifacts.objects {
		if strings.HasSuffix(key, ".tmp") || !strings.HasPrefix(key, "report-v2/") {
			t.Fatalf("unexpected artifact key %q", key)
		}
	}
}

func TestPublisherSelectsHistoricalDraftRevision(t *testing.T) {
	harness := newPublicationHarness(t)
	revision := int64(2)
	harness.repository.draft.RevisionNo = revision
	harness.request.SourceRevisionNo = &revision
	version, err := harness.publisher.Publish(context.Background(), harness.identity, harness.request)
	if err != nil || harness.repository.requestedRevision == nil || *harness.repository.requestedRevision != revision || version.SourceRevisionNo != revision {
		t.Fatalf("historical revision publication = %#v, requested=%v, err=%v", version, harness.repository.requestedRevision, err)
	}
}

func TestPublisherStaleInsightAcknowledgementIsPersisted(t *testing.T) {
	harness := newPublicationHarness(t)
	harness.publisher.Insights = insightValidatorFunc(func(_ context.Context, _ store.Identity, _ askdata.ID, acknowledge bool) compiler.ValidationIssues {
		if acknowledge {
			return nil
		}
		return compiler.ValidationIssues{{Code: "REPORT_INSIGHT_STALE", Path: "components", Message: "stale"}}
	})
	if _, err := harness.publisher.Publish(context.Background(), harness.identity, harness.request); publishStep(err) != 9 || len(harness.repository.versions) != 0 {
		t.Fatalf("unacknowledged stale insight error = %v", err)
	}
	harness.request.AcknowledgeStaleInsights = true
	version, err := harness.publisher.Publish(context.Background(), harness.identity, harness.request)
	if err != nil || !version.StaleInsightsAcknowledged || !harness.repository.inputs[len(harness.repository.inputs)-1].StaleInsightsAcknowledged {
		t.Fatalf("acknowledged stale publication = %#v, %v", version, err)
	}
}

func TestPublisherFailureStepsAreLocatedAndSideEffectsBounded(t *testing.T) {
	steps := []struct {
		step  int
		setup func(*publicationHarness)
	}{
		{1, func(h *publicationHarness) {
			h.publisher.Authorizer = authorizerFunc(func(context.Context, store.Identity, askdata.ID) error { return errors.New("denied") })
		}},
		{2, func(h *publicationHarness) { h.repository.failDraft = errors.New("missing") }},
		{3, func(h *publicationHarness) { h.repository.draft.Definition.SchemaVersion = "2.0" }},
		{4, func(h *publicationHarness) { h.identity.DomainID = "" }},
		{5, func(h *publicationHarness) { h.repository.draft.Definition.Components[0].TemplateRef.Version = "9.9.9" }},
		{6, func(h *publicationHarness) {
			h.publisher.Dependencies = dependencyValidatorFunc(func(context.Context, store.Identity, reportmodel.ReportDefinition) compiler.ValidationIssues {
				return compiler.ValidationIssues{{Code: "REPORT_BINDING_DATASET_NOT_ACTIVE"}}
			})
		}},
		{7, func(h *publicationHarness) { h.request.MobilePreviewHash = "" }},
		{8, func(h *publicationHarness) {
			componentID := h.repository.draft.Definition.Components[0].ID
			h.repository.draft.Definition.Interactions = []reportmodel.Interaction{{
				ID: "interaction_cycle", SourceComponentID: componentID, Event: reportmodel.InteractionClick,
				Action: reportmodel.InteractionFilter, TargetComponentIDs: []askdata.ID{componentID}, FieldMappings: []reportmodel.FieldMapping{},
			}}
		}},
		{9, func(h *publicationHarness) {
			h.publisher.Insights = insightValidatorFunc(func(context.Context, store.Identity, askdata.ID, bool) compiler.ValidationIssues {
				return compiler.ValidationIssues{{Code: "REPORT_INSIGHT_STALE"}}
			})
		}},
		{10, func(h *publicationHarness) {
			h.publisher.Normalizer = normalizerFunc(func(reportmodel.ReportDefinition) (reportmodel.ReportDefinition, []byte, string, error) {
				return reportmodel.ReportDefinition{}, nil, "", errors.New("normalize")
			})
		}},
		{11, func(h *publicationHarness) {
			h.publisher.Indexer = indexerFunc(func(reportmodel.ReportDefinition) (compiler.Indexes, error) {
				return compiler.Indexes{}, errors.New("index")
			})
		}},
		{12, func(h *publicationHarness) { h.artifacts.failPut = errors.New("put") }},
		{13, func(h *publicationHarness) { h.repository.failCreate = errors.New("database") }},
		{14, func(h *publicationHarness) { h.artifacts.failPromote = errors.New("promote") }},
	}
	for _, test := range steps {
		t.Run(strings.TrimSpace(time.Duration(test.step).String()), func(t *testing.T) {
			harness := newPublicationHarness(t)
			test.setup(&harness)
			_, err := harness.publisher.Publish(context.Background(), harness.identity, harness.request)
			if got := publishStep(err); got != test.step {
				t.Fatalf("failure step = %d, error=%v; want %d", got, err, test.step)
			}
			if test.step < 13 && len(harness.repository.versions) != 0 {
				t.Fatalf("step %d created a version", test.step)
			}
			if test.step < 12 && len(harness.artifacts.objects) != 0 {
				t.Fatalf("step %d created an artifact", test.step)
			}
			if test.step == 13 && len(harness.artifacts.objects) != 0 {
				t.Fatal("database failure left a temporary artifact")
			}
			if test.step == 14 && (len(harness.repository.versions) != 1 || harness.repository.retryCalls != 1) {
				t.Fatal("promote failure was not retained for recovery")
			}
		})
	}
}

func TestPublicationRecoveryPromotesExactObjectURI(t *testing.T) {
	harness := newPublicationHarness(t)
	harness.artifacts.failPromote = errors.New("injected promote failure")
	version, err := harness.publisher.Publish(context.Background(), harness.identity, harness.request)
	if publishStep(err) != 14 || version.ID == "" {
		t.Fatalf("initial publication = %#v, %v", version, err)
	}
	harness.artifacts.failPromote = nil
	worker, err := NewRecoveryWorker(harness.repository, harness.artifacts)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.ProcessNext(context.Background(), string(harness.identity.TenantID), time.Minute)
	if err != nil || !processed {
		t.Fatalf("recovery processed=%v error=%v", processed, err)
	}
	if recovered := harness.repository.versions[version.ID]; recovered.ArtifactState != "READY" {
		t.Fatalf("recovered version = %#v", recovered)
	}
	if _, err := harness.artifacts.Read(context.Background(), version.ObjectURI); err != nil {
		t.Fatal("recovery did not promote the URI recorded in the version")
	}
}

func TestRollbackCreatesNewVersionWithoutMutatingHistoryAndIsIdempotent(t *testing.T) {
	harness := newPublicationHarness(t)
	first, err := harness.publisher.Publish(context.Background(), harness.identity, harness.request)
	if err != nil {
		t.Fatal(err)
	}
	second := publishChangedHarnessDraft(t, &harness, "publish-key-002", "New current definition")
	firstBefore := harness.repository.versions[first.ID]
	secondBefore := harness.repository.versions[second.ID]
	ctx := WithIdempotencyKey(context.Background(), "rollback-key-001")
	rolledBack, err := harness.publisher.Rollback(ctx, harness.identity, harness.request.ReportID, 1, " Restore the approved baseline ", false)
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if rolledBack.VersionNo != 3 || rolledBack.RollbackOfVersionNo == nil || *rolledBack.RollbackOfVersionNo != 1 ||
		rolledBack.RollbackReason != "Restore the approved baseline" || rolledBack.DefinitionHash != first.DefinitionHash ||
		rolledBack.DefinitionHash == second.DefinitionHash || rolledBack.ArtifactState != "READY" {
		t.Fatalf("rollback version = %#v", rolledBack)
	}
	if after := harness.repository.versions[first.ID]; after.DefinitionHash != firstBefore.DefinitionHash || after.RollbackOfVersionNo != nil {
		t.Fatalf("target history was mutated: before=%#v after=%#v", firstBefore, after)
	}
	if after := harness.repository.versions[second.ID]; after.DefinitionHash != secondBefore.DefinitionHash {
		t.Fatalf("current history was mutated: before=%#v after=%#v", secondBefore, after)
	}
	replayed, err := harness.publisher.Rollback(ctx, harness.identity, harness.request.ReportID, 1, "Restore the approved baseline", false)
	if err != nil || !replayed.Replayed || replayed.ID != rolledBack.ID || len(harness.repository.versions) != 3 {
		t.Fatalf("idempotent rollback = %#v, %v; versions=%d", replayed, err, len(harness.repository.versions))
	}
}

func TestRollbackRevalidatesDependenciesAndReturnsFailureIssues(t *testing.T) {
	harness := newPublicationHarness(t)
	if _, err := harness.publisher.Publish(context.Background(), harness.identity, harness.request); err != nil {
		t.Fatal(err)
	}
	objectCount := len(harness.artifacts.objects)
	wantIssue := compiler.ValidationIssue{
		Code: "REPORT_BINDING_DATASET_NOT_ACTIVE", Path: "dataContexts.sales_context",
		Message: "historical dataset version is no longer active",
	}
	harness.publisher.Dependencies = dependencyValidatorFunc(func(context.Context, store.Identity, reportmodel.ReportDefinition) compiler.ValidationIssues {
		return compiler.ValidationIssues{wantIssue}
	})
	_, err := harness.publisher.Rollback(
		WithIdempotencyKey(context.Background(), "rollback-key-002"), harness.identity,
		harness.request.ReportID, 1, "Restore after regression", false,
	)
	var stepErr *StepError
	var issues compiler.ValidationIssues
	if !errors.As(err, &stepErr) || stepErr.Step != 6 || !errors.As(stepErr.Err, &issues) ||
		len(issues) != 1 || issues[0] != wantIssue {
		t.Fatalf("rollback dependency failure = %#v / %#v", stepErr, issues)
	}
	if len(harness.repository.versions) != 1 || len(harness.artifacts.objects) != objectCount {
		t.Fatal("rejected rollback changed versions or artifacts")
	}
}

func TestRollbackValidatesReasonAuthorizationTargetAndCompletedState(t *testing.T) {
	tests := []struct {
		name     string
		reason   string
		target   int
		setup    func(*publicationHarness)
		wantStep int
		wantCode string
		withKey  bool
	}{
		{name: "reason required", reason: "  ", target: 1, wantStep: 1, wantCode: "REPORT_ROLLBACK_REASON_REQUIRED"},
		{name: "reason bounded", reason: strings.Repeat("界", 1001), target: 1, wantStep: 1, wantCode: "REPORT_ROLLBACK_REASON_INVALID"},
		{name: "reason controls", reason: "unsafe\nreason", target: 1, wantStep: 1, wantCode: "REPORT_ROLLBACK_REASON_INVALID"},
		{name: "idempotency required", reason: "restore", target: 1, wantStep: 1, wantCode: "IDEMPOTENCY_KEY_REQUIRED"},
		{name: "authorization", reason: "restore", target: 1, withKey: true, wantStep: 1, wantCode: "REPORT_PUBLISH_FORBIDDEN", setup: func(h *publicationHarness) {
			h.publisher.Authorizer = authorizerFunc(func(context.Context, store.Identity, askdata.ID) error { return errors.New("denied") })
		}},
		{name: "positive target", reason: "restore", target: 0, withKey: true, wantStep: 2, wantCode: "REPORT_ROLLBACK_VERSION_INVALID"},
		{name: "target exists", reason: "restore", target: 99, withKey: true, wantStep: 2, wantCode: "REPORT_ROLLBACK_VERSION_NOT_FOUND"},
		{name: "target ready", reason: "restore", target: 1, withKey: true, wantStep: 2, wantCode: "REPORT_ROLLBACK_VERSION_NOT_READY", setup: func(h *publicationHarness) {
			for id, version := range h.repository.versions {
				version.ArtifactState = "RETRY"
				h.repository.versions[id] = version
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newPublicationHarness(t)
			if _, err := harness.publisher.Publish(context.Background(), harness.identity, harness.request); err != nil {
				t.Fatal(err)
			}
			if test.setup != nil {
				test.setup(&harness)
			}
			ctx := context.Background()
			if test.withKey {
				ctx = WithIdempotencyKey(ctx, "rollback-key-validation")
			}
			_, err := harness.publisher.Rollback(ctx, harness.identity, harness.request.ReportID, test.target, test.reason, false)
			var stepErr *StepError
			if !errors.As(err, &stepErr) || stepErr.Step != test.wantStep || stepErr.Code != test.wantCode {
				t.Fatalf("Rollback() error = %#v, want step/code %d/%s", stepErr, test.wantStep, test.wantCode)
			}
			if len(harness.repository.versions) != 1 {
				t.Fatal("invalid rollback created a version")
			}
		})
	}
}

func TestRollbackOfRollbackCreatesAnotherImmutableVersion(t *testing.T) {
	harness := newPublicationHarness(t)
	first, err := harness.publisher.Publish(context.Background(), harness.identity, harness.request)
	if err != nil {
		t.Fatal(err)
	}
	publishChangedHarnessDraft(t, &harness, "publish-key-003", "Changed definition")
	third, err := harness.publisher.Rollback(
		WithIdempotencyKey(context.Background(), "rollback-key-003"), harness.identity,
		harness.request.ReportID, 1, "First rollback", false,
	)
	if err != nil {
		t.Fatal(err)
	}
	fourth, err := harness.publisher.Rollback(
		WithIdempotencyKey(context.Background(), "rollback-key-004"), harness.identity,
		harness.request.ReportID, third.VersionNo, "Rollback the rollback", false,
	)
	if err != nil || fourth.VersionNo != 4 || fourth.RollbackOfVersionNo == nil ||
		*fourth.RollbackOfVersionNo != third.VersionNo || fourth.DefinitionHash != first.DefinitionHash ||
		len(harness.repository.versions) != 4 {
		t.Fatalf("rollback of rollback = %#v, %v", fourth, err)
	}
}

func publishChangedHarnessDraft(t *testing.T, harness *publicationHarness, key, name string) store.Version {
	t.Helper()
	harness.repository.draft.Definition.Metadata.Name = name
	canonical, hash, err := compiler.Normalize(harness.repository.draft.Definition)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(canonical, &harness.repository.draft.Definition); err != nil {
		t.Fatal(err)
	}
	harness.repository.draft.DefinitionHash = hash
	harness.repository.draft.RevisionNo++
	harness.request.SourceRevisionNo = nil
	harness.request.DesktopPreviewHash = askdata.ContentHash(hash)
	harness.request.MobilePreviewHash = askdata.ContentHash(hash)
	harness.request.IdempotencyKey = key
	version, err := harness.publisher.Publish(context.Background(), harness.identity, harness.request)
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func publishStep(err error) int {
	var stepErr *StepError
	if errors.As(err, &stepErr) {
		return stepErr.Step
	}
	return 0
}

func publicationDefinition(t *testing.T, name string) reportmodel.ReportDefinition {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve publication fixture path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "..", "api", "examples", "report-definition", name))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := reportmodel.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}
