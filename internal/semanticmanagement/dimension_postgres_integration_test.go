package semanticmanagement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/platform/database"
)

const (
	semanticDimensionTestSchemaHash   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	semanticDimensionTestSnapshotHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// Opt-in: point only at a disposable database with migrations through v68.
// The connected role must own the warehouse physical/published schemas because
// the refresh worker deliberately refuses views it cannot prove it owns.
func TestPostgresDimensionRefreshCompatibilityAnd690Search(t *testing.T) {
	databaseURL := os.Getenv("SEMANTIC_MANAGEMENT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SEMANTIC_MANAGEMENT_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	tenantID, actorID := uuid.NewString(), uuid.NewString()
	dwdDatasetID, dwdDraftID, dwdVersionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	dwsDatasetID, dwsDraftID, dwsVersionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
		VALUES($1,$2,'Semantic dimension integration')`,
		tenantID, "semantic_dimension_"+tenantID[:8]); err != nil {
		t.Fatal(err)
	}
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
			id,tenant_id,email,display_name,password_hash
		) VALUES($1,$2,$3,'Semantic dimension manager','test-hash')`,
			actorID, tenantID, actorID+"@example.test"); err != nil {
			return err
		}
		if err := createSemanticPublishedDatasetFixture(
			ctx, tx, tenantID, actorID, dwdDatasetID, dwdDraftID,
			dwdVersionID, materialization.LayerDWD, "dwd_"+dwdDatasetID[:8], false,
		); err != nil {
			return err
		}
		if err := createSemanticPublishedDatasetFixture(
			ctx, tx, tenantID, actorID, dwsDatasetID, dwsDraftID,
			dwsVersionID, materialization.LayerDWS, "dws_"+dwsDatasetID[:8], true,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.object_permissions(
				tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
			) VALUES($1,'USER',$2,'DATASET',$3,'READ',$2)`,
			tenantID, actorID, dwsDatasetID); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	materializationStore := materialization.NewPostgresStore(pool)
	buildRequest := materialization.RegisterRequest{
		Plan: materialization.BuildPlan{
			Version:   materialization.PlanVersion,
			DatasetID: dwsDatasetID, DatasetVersionID: dwsVersionID,
			Layer: materialization.LayerDWS, Mode: materialization.RunModeFull,
			Nodes: []materialization.PlanNode{
				{ID: "extract", Kind: materialization.NodeExtract, Engine: materialization.EnginePostgres, InputOrdinals: []int{1}},
				{ID: "aggregate", Kind: materialization.NodeAggregate, Engine: materialization.EnginePostgres, DependsOn: []string{"extract"}},
				{ID: "materialize", Kind: materialization.NodeMaterialize, Engine: materialization.EnginePostgres, DependsOn: []string{"aggregate"}},
			},
			Target: materialization.TargetPlan{
				Storage: "POSTGRES", AtomicPublish: true, RelationKind: "TABLE",
				RefreshMode: string(materialization.RunModeFull), StableViewName: true,
			},
		},
		Inputs: []materialization.InputSnapshot{{
			Ordinal: 1, Type: materialization.InputDatasetVersion,
			Layer:     string(materialization.LayerDWD),
			DatasetID: dwdDatasetID, DatasetVersionID: dwdVersionID,
			SourceVersion: "published:1", SchemaHash: semanticDimensionTestSchemaHash,
			SnapshotHash: semanticDimensionTestSnapshotHash,
			SnapshotJSON: json.RawMessage(`{"watermark":"full"}`),
		}},
		MaxAttempts: 3,
	}
	run, created, err := materializationStore.Register(
		ctx, tenantID, actorID, buildRequest,
	)
	if err != nil || !created {
		t.Fatalf("register DWS build: created=%v err=%v", created, err)
	}
	buildClaim, err := materializationStore.Claim(
		ctx, tenantID, "semantic-dimension-build", time.Minute,
	)
	if err != nil || buildClaim == nil || buildClaim.ID != run.ID {
		t.Fatalf("claim DWS build: claim=%+v err=%v", buildClaim, err)
	}
	for _, nodeID := range []string{"extract", "aggregate", "materialize"} {
		if err := materializationStore.StartNode(ctx, *buildClaim, nodeID); err != nil {
			t.Fatalf("start node %s: %v", nodeID, err)
		}
		rows, bytes := int64(4), int64(1024)
		if err := materializationStore.FinishNode(ctx, *buildClaim, nodeID, materialization.NodeResult{
			Status: materialization.NodeSucceeded, InputRowCount: &rows,
			OutputRowCount: &rows, OutputSizeBytes: &bytes,
		}); err != nil {
			t.Fatalf("finish node %s: %v", nodeID, err)
		}
	}
	physical, err := materialization.GeneratePhysicalIdentifier(
		tenantID, dwsDatasetID, run.ID, materialization.LayerDWS,
	)
	if err != nil {
		t.Fatal(err)
	}
	physicalTable := quoteTrustedIdentifier(physical.Schema) + "." +
		quoteTrustedIdentifier(physical.Name)
	if _, err := pool.Exec(ctx, "CREATE TABLE "+physicalTable+
		" (circle_code text NOT NULL,region_code text,channel_code text,customer_id text NOT NULL,total_amount numeric NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO "+physicalTable+
		"(circle_code,region_code,channel_code,customer_id,total_amount) VALUES($1,'华东','直营','customer-1',10),($2,'华北','门店','customer-2',20),($3,'华北','门店','customer-3',30),($4,'华南','直营','customer-4',40)",
		"智家生态圈", "ABC", "abc", "ＡＢＣ"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO "+physicalTable+
		"(circle_code,region_code,channel_code,customer_id,total_amount) "+
		"SELECT '智家生态圈',NULL,'channel-'||value::text,"+
		"'customer-'||(value+4)::text,1 "+
		"FROM generate_series(1,10000) AS value"); err != nil {
		t.Fatal(err)
	}
	active, err := materializationStore.Activate(ctx, *buildClaim, materialization.Activation{
		Physical: physical, RelationKind: "TABLE",
		SchemaHash:   semanticDimensionTestSchemaHash,
		SnapshotHash: semanticDimensionTestSnapshotHash,
		RowCount:     10004, SizeBytes: 1024,
		Watermark: json.RawMessage(`{"watermark":"full"}`),
	})
	if err != nil || active.Status != "ACTIVE" {
		t.Fatalf("activate DWS materialization: item=%+v err=%v", active, err)
	}

	store := NewPostgresStore(pool)
	service := NewDimensionService(store)
	if _, err := service.CreateDimension(ctx, tenantID, actorID, CreateDimensionInput{
		DatasetID: dwsDatasetID, DatasetVersionID: dwsVersionID,
		FieldID: "field_amount", Code: "invalid_measure", Name: "非法度量维度",
		DimensionType: "STANDARD", MemberIndexPolicy: "FULL", Status: "PUBLISHED",
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("measure-backed dimension error=%v", err)
	}
	if _, err := service.CreateDimension(ctx, tenantID, actorID, CreateDimensionInput{
		DatasetID: dwsDatasetID, DatasetVersionID: dwsVersionID,
		FieldID: "field_circle", Code: "profile_bypass", Name: "画像绕过",
		DimensionType: "STANDARD", MemberIndexPolicy: "FULL", Status: "PUBLISHED",
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("direct FULL dimension bypassed pending profile: %v", err)
	}
	pendingDraft, err := service.CreateDimension(
		ctx, tenantID, actorID,
		CreateDimensionInput{
			DatasetID: dwsDatasetID, DatasetVersionID: dwsVersionID,
			FieldID: "field_identifier", Code: "profile_update_bypass",
			Name: "画像更新绕过", DimensionType: "STANDARD",
			MemberIndexPolicy: "FULL", Status: "DRAFT",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateDimension(
		ctx, tenantID, actorID, pendingDraft.ID,
		UpdateDimensionInput{
			ExpectedVersion:   pendingDraft.Version,
			Code:              pendingDraft.Code,
			Name:              pendingDraft.Name,
			Description:       pendingDraft.Description,
			DimensionType:     pendingDraft.DimensionType,
			MemberIndexPolicy: "FULL",
			Status:            "PUBLISHED",
		},
	); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("dimension update bypassed pending profile: %v", err)
	}
	pendingSafe, err := service.UpdateDimension(
		ctx, tenantID, actorID, pendingDraft.ID,
		UpdateDimensionInput{
			ExpectedVersion:   pendingDraft.Version,
			Code:              pendingDraft.Code,
			Name:              pendingDraft.Name,
			Description:       pendingDraft.Description,
			DimensionType:     pendingDraft.DimensionType,
			MemberIndexPolicy: "NONE",
			Status:            "PUBLISHED",
		},
	)
	if err != nil {
		t.Fatalf("NONE update should remain safe before profile: %v", err)
	}
	if _, err := service.DeprecateDimension(
		ctx, tenantID, actorID, pendingSafe.ID, pendingSafe.Version,
	); err != nil {
		t.Fatal(err)
	}
	expiredClaim, err := store.ClaimDimensionProfile(
		ctx, tenantID, "expired-profile-writer", time.Second,
	)
	if err != nil || expiredClaim == nil {
		t.Fatalf("claim expiring profile: claim=%+v err=%v", expiredClaim, err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := store.FailDimensionProfile(
		ctx, *expiredClaim, "PROFILE_FAILED",
	); !errors.Is(err, ErrProfileLeaseLost) {
		t.Fatalf("expired profile writer was not fenced: %v", err)
	}
	profileWorker := NewDimensionProfileWorker(store)
	for index := 0; index < 4; index++ {
		processed, profileErr := profileWorker.ProcessNext(
			ctx, tenantID, "semantic-dimension-profile", 2*time.Minute,
		)
		if profileErr != nil || !processed {
			t.Fatalf(
				"process dimension profile %d: processed=%v err=%v",
				index, processed, profileErr,
			)
		}
	}
	if err := assertOldPublishedVersionCannotReuseProfile(
		ctx, pool, tenantID, actorID, dwsDatasetID, dwsDraftID,
		dwsVersionID,
	); err != nil {
		t.Fatal(err)
	}
	candidates, candidateTotal, err := service.ListDimensionSurveyCandidates(
		ctx, tenantID, DimensionSurveyFilter{
			Page: Page{Limit: 50}, DatasetVersionID: dwsVersionID,
			Status: "SUGGESTED",
		},
	)
	if err != nil || candidateTotal != 4 || len(candidates) != 4 {
		t.Fatalf("survey candidates=%+v total=%d err=%v",
			candidates, candidateTotal, err)
	}
	var circleCandidate, regionCandidate, channelCandidate, identifierCandidate DimensionSurveyCandidate
	for _, candidate := range candidates {
		switch candidate.FieldID {
		case "field_circle":
			circleCandidate = candidate
		case "field_region":
			regionCandidate = candidate
		case "field_channel":
			channelCandidate = candidate
		case "field_identifier":
			identifierCandidate = candidate
		}
		if candidate.MaterializationID != active.ID ||
			candidate.MaterializationSnapshotHash != semanticDimensionTestSnapshotHash ||
			candidate.SchemaHash != semanticDimensionTestSchemaHash ||
			candidate.Status != "SUGGESTED" ||
			!strings.Contains(string(candidate.Evidence), `"containsBusinessSamples": false`) {
			t.Fatalf("unsafe or unfenced survey candidate=%+v", candidate)
		}
		if candidate.FieldID == "field_identifier" {
			if candidate.Profile.Status != "SKIPPED_POLICY" ||
				candidate.Profile.ResultCode != "IDENTIFIER_FIELD_PROFILE_SKIPPED" ||
				candidate.Profile.RowCount != nil ||
				candidate.Profile.NonNullCount != nil ||
				candidate.Profile.NullCount != nil ||
				candidate.Profile.DistinctCount != nil ||
				candidate.Profile.RecommendedMemberIndexPolicy != "EXACT_ONLY" {
				t.Fatalf("identifier values were scanned: %+v", candidate)
			}
		} else if candidate.Profile.Status != "SUCCEEDED" ||
			candidate.Profile.RowCount == nil ||
			*candidate.Profile.RowCount != 10004 ||
			candidate.Profile.NonNullCount == nil ||
			candidate.Profile.NullCount == nil ||
			candidate.Profile.DistinctCount == nil {
			t.Fatalf("measured profile evidence missing: %+v", candidate)
		}
	}
	if circleCandidate.ID == "" || regionCandidate.ID == "" ||
		channelCandidate.ID == "" || identifierCandidate.ID == "" {
		t.Fatalf("missing survey fields: %+v", candidates)
	}
	if !channelCandidate.RiskHighCardinality ||
		!channelCandidate.ProposedHighCardinality ||
		channelCandidate.ProposedMemberIndexPolicy != "EXACT_ONLY" ||
		channelCandidate.Profile.RecommendedMemberIndexPolicy != "EXACT_ONLY" {
		t.Fatalf("measured high-cardinality policy was not tightened: %+v", channelCandidate)
	}
	if !identifierCandidate.RiskHighCardinality ||
		!identifierCandidate.ProposedHighCardinality ||
		identifierCandidate.ProposedMemberIndexPolicy != "EXACT_ONLY" {
		t.Fatalf("identifier policy shortcut was not tightened: %+v", identifierCandidate)
	}

	// The field was not sensitive when surveyed. Approving a sensitivity tag
	// afterwards must be re-read during accept; frozen false evidence cannot
	// authorize a FULL member scan.
	semanticService := NewService(store)
	sensitivityTag, err := semanticService.CreateTag(
		ctx, tenantID, actorID, CreateTagInput{
			Code: "restricted_" + tenantID[:8], Name: "受限字段",
			Category: "SENSITIVITY", Governance: "CONTROLLED", Status: "ACTIVE",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := semanticService.CreateAssetTagBinding(
		ctx, tenantID, actorID, CreateAssetTagBindingInput{
			TagID: sensitivityTag.ID, AssetType: "DATASET_FIELD",
			DatasetID: dwsDatasetID, DatasetVersionID: dwsVersionID,
			DatasetFieldID: "field_region", Origin: "USER", Status: "APPROVED",
			Evidence: json.RawMessage(`{"source":"integration-governance"}`),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AcceptDimensionSurveyCandidate(
		ctx, tenantID, actorID, regionCandidate.ID, regionCandidate.Version,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("late sensitivity approval did not fence accept: %v", err)
	}
	previousRegionVersion := regionCandidate.Version
	regionCandidate, err = service.GetDimensionSurveyCandidate(
		ctx, tenantID, regionCandidate.ID,
	)
	if err != nil ||
		regionCandidate.Version <= previousRegionVersion ||
		!regionCandidate.ProposedSensitive ||
		regionCandidate.ProposedMemberIndexPolicy != "NONE" ||
		regionCandidate.Profile.Status != "STALE" ||
		regionCandidate.Profile.ResultCode != "SENSITIVITY_POLICY_CHANGED" {
		t.Fatalf(
			"late sensitivity did not advance/stale candidate: item=%+v err=%v",
			regionCandidate, err,
		)
	}
	regionCandidate, err = service.UpdateDimensionSurveyCandidate(
		ctx, tenantID, actorID, regionCandidate.ID,
		UpdateDimensionSurveyCandidateInput{
			ExpectedVersion: regionCandidate.Version,
			Code:            "region", Name: "区域", Description: "受限区域维度",
			DimensionType: "GEOGRAPHY", MemberIndexPolicy: "NONE",
			Sensitive: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	regionAcceptance, err := service.AcceptDimensionSurveyCandidate(
		ctx, tenantID, actorID, regionCandidate.ID, regionCandidate.Version,
	)
	if err != nil || !regionAcceptance.Dimension.Sensitive ||
		regionAcceptance.Dimension.MemberIndexPolicy != "NONE" ||
		regionAcceptance.Dimension.Status != "PUBLISHED" ||
		regionAcceptance.MemberRefreshJob != nil ||
		regionAcceptance.NextAction != "MEMBER_INDEX_DISABLED" {
		t.Fatalf("sensitive acceptance=%+v err=%v", regionAcceptance, err)
	}

	// Approval and acceptance serialize on the exact field risk lock. Either
	// approval wins and acceptance fails, or acceptance wins and the approval
	// transaction immediately tightens the published dimension and skips its
	// queued FULL refresh. No committed unsafe intermediate state may remain.
	channelBinding, err := semanticService.CreateAssetTagBinding(
		ctx, tenantID, actorID, CreateAssetTagBindingInput{
			TagID: sensitivityTag.ID, AssetType: "DATASET_FIELD",
			DatasetID: dwsDatasetID, DatasetVersionID: dwsVersionID,
			DatasetFieldID: "field_channel", Origin: "USER", Status: "SUGGESTED",
			Evidence: json.RawMessage(`{"source":"integration-race"}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	startRiskRace := make(chan struct{})
	acceptRiskResult := make(chan error, 1)
	approveRiskResult := make(chan error, 1)
	go func() {
		<-startRiskRace
		_, raceErr := service.AcceptDimensionSurveyCandidate(
			ctx, tenantID, actorID, channelCandidate.ID, channelCandidate.Version,
		)
		acceptRiskResult <- raceErr
	}()
	go func() {
		<-startRiskRace
		_, raceErr := semanticService.UpdateAssetTagBinding(
			ctx, tenantID, actorID, channelBinding.ID,
			UpdateAssetTagBindingInput{
				ExpectedRecordVersion: channelBinding.RecordVersion,
				Origin:                "USER", Status: "APPROVED",
				Evidence: json.RawMessage(`{"source":"integration-race-approved"}`),
			},
		)
		approveRiskResult <- raceErr
	}()
	close(startRiskRace)
	acceptRiskErr, approveRiskErr := <-acceptRiskResult, <-approveRiskResult
	if postgresErrorCode(acceptRiskErr) == "40P01" ||
		postgresErrorCode(approveRiskErr) == "40P01" {
		t.Fatalf(
			"governance lock barrier deadlocked: accept=%v approve=%v",
			acceptRiskErr, approveRiskErr,
		)
	}
	if approveRiskErr != nil ||
		(acceptRiskErr != nil && !errors.Is(acceptRiskErr, ErrConflict)) {
		t.Fatalf("risk race accept=%v approve=%v", acceptRiskErr, approveRiskErr)
	}
	var channelDimensionCount int
	var channelSensitive bool
	var channelPolicy string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*)::int,
				COALESCE(bool_and(sensitive),false),
				COALESCE(min(member_index_policy),'')
			FROM platform.semantic_dimensions
			WHERE dataset_version_id=$1::uuid
			  AND field_id='field_channel'
			  AND status='PUBLISHED'`, dwsVersionID).Scan(
			&channelDimensionCount, &channelSensitive, &channelPolicy,
		)
	})
	if err != nil {
		t.Fatal(err)
	}
	if channelDimensionCount == 0 {
		if !errors.Is(acceptRiskErr, ErrConflict) {
			t.Fatalf("risk race lost dimension without conflict: %v", acceptRiskErr)
		}
	} else if channelDimensionCount != 1 || !channelSensitive ||
		channelPolicy != "NONE" {
		t.Fatalf("unsafe channel dimension count=%d sensitive=%v policy=%q accept=%v",
			channelDimensionCount, channelSensitive, channelPolicy, acceptRiskErr)
	}

	circleCandidate, err = service.UpdateDimensionSurveyCandidate(
		ctx, tenantID, actorID, circleCandidate.ID,
		UpdateDimensionSurveyCandidateInput{
			ExpectedVersion: circleCandidate.Version,
			Code:            "home_ecosystem", Name: "生态圈",
			Description: "业务生态圈维度", DimensionType: "ORGANIZATION",
			MemberIndexPolicy: "FULL",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	acceptance, err := service.AcceptDimensionSurveyCandidate(
		ctx, tenantID, actorID, circleCandidate.ID, circleCandidate.Version,
	)
	if err != nil || acceptance.Candidate.Status != "ACCEPTED" ||
		acceptance.Dimension.Status != "PUBLISHED" ||
		acceptance.MemberRefreshJob == nil ||
		acceptance.MemberRefreshJob.Status != "QUEUED" ||
		acceptance.MemberRefreshJob.MaterializationID != active.ID ||
		acceptance.MemberRefreshJob.MaxMembers != defaultRefreshMaxMembers ||
		acceptance.MemberRefreshJob.TimeoutSeconds != defaultRefreshTimeout ||
		acceptance.MemberSearchReady ||
		acceptance.NextAction != "WAIT_FOR_MEMBER_REFRESH" {
		t.Fatalf("dimension survey acceptance=%+v err=%v", acceptance, err)
	}
	dimension := acceptance.Dimension
	refresh := *acceptance.MemberRefreshJob
	processed, err := NewDimensionRefreshWorker(store).ProcessNext(
		ctx, tenantID, "semantic-dimension-refresh", time.Minute,
	)
	if err != nil || !processed {
		t.Fatalf("process refresh: processed=%v err=%v", processed, err)
	}
	expiringRefresh, created, err := service.CreateRefreshJob(
		ctx, tenantID, actorID, dimension.ID, "expired-failure-fence",
		CreateRefreshJobInput{
			ExpectedDimensionVersion: dimension.Version,
			MaxMembers:               10,
			TimeoutSeconds:           30,
		},
	)
	if err != nil || !created {
		t.Fatalf(
			"create expiring refresh: item=%+v created=%v err=%v",
			expiringRefresh, created, err,
		)
	}
	expiringClaim, err := store.ClaimDimensionRefresh(
		ctx, tenantID, "expired-refresh-writer", time.Second,
	)
	if err != nil || expiringClaim == nil ||
		expiringClaim.ID != expiringRefresh.ID {
		t.Fatalf("claim expiring refresh: claim=%+v err=%v", expiringClaim, err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := store.FailDimensionRefresh(
		ctx, *expiringClaim, "REFRESH_FAILED",
		"dimension member refresh failed",
	); !errors.Is(err, ErrRefreshLeaseLost) {
		t.Fatalf("expired refresh failure writer was not fenced: %v", err)
	}
	processed, err = NewDimensionRefreshWorker(store).ProcessNext(
		ctx, tenantID, "semantic-dimension-refresh", time.Minute,
	)
	if err != nil || !processed {
		t.Fatalf(
			"reclaim expiring refresh: processed=%v err=%v", processed, err,
		)
	}
	members, total, err := service.ListDimensionMembers(ctx, tenantID, actorID, DimensionMemberFilter{
		Page: Page{Limit: 50}, DimensionID: dimension.ID, Status: "ACTIVE",
	})
	if err != nil || total != 2 || len(members) != 2 {
		t.Fatalf("normalized members=%+v total=%d err=%v", members, total, err)
	}
	var homeMember DimensionMember
	for _, member := range members {
		if member.CanonicalLabel == "智家生态圈" {
			homeMember = member
		}
	}
	if homeMember.ID == "" {
		t.Fatalf("home ecosystem member missing: %+v", members)
	}
	alias, err := service.CreateDimensionMemberAlias(ctx, tenantID, actorID, CreateDimensionMemberAliasInput{
		DimensionID: dimension.ID, DimensionMemberID: homeMember.ID,
		Alias: "690", AliasType: "LEGACY",
	})
	if err != nil || alias.NormalizedAlias != "690" {
		t.Fatalf("create 690 alias: item=%+v err=%v", alias, err)
	}

	metricID, metricDraftID, metricVersionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := createPublishedMetricFixture(
		ctx, pool, tenantID, actorID, dwsDatasetID, dwsVersionID,
		metricID, metricDraftID, metricVersionID,
	); err != nil {
		t.Fatal(err)
	}
	proposed, err := service.ProposeCompatibility(ctx, tenantID, actorID, ProposeCompatibilityInput{
		DimensionID: dimension.ID, MetricID: metricID,
		MetricVersionID: metricVersionID, MetricDatasetVersionID: dwsVersionID,
		CompatibilityType: "DIRECT", FanoutPolicy: "UNSAFE",
		JoinPath: json.RawMessage(`[]`), EvidenceSource: "HUMAN",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.DecideCompatibility(
		ctx, tenantID, actorID, proposed.ID, proposed.Version, "VERIFIED",
	); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unsafe verification error=%v", err)
	}
	proposed, err = service.UpdateCompatibility(
		ctx, tenantID, actorID, proposed.ID,
		UpdateCompatibilityInput{
			ExpectedVersion: proposed.Version, CompatibilityType: "DIRECT",
			FanoutPolicy: "SAFE", JoinPath: json.RawMessage(`[]`),
			EvidenceSource: "HUMAN",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := service.DecideCompatibility(
		ctx, tenantID, actorID, proposed.ID, proposed.Version, "VERIFIED",
	)
	if err != nil || verified.Status != "VERIFIED" || verified.VerifiedBy != actorID {
		t.Fatalf("verify compatibility: item=%+v err=%v", verified, err)
	}
	results, err := service.SearchMemberMetrics(ctx, tenantID, actorID, "690", 20)
	if err != nil || len(results) != 1 ||
		results[0].CanonicalLabel != "智家生态圈" ||
		results[0].MetricVersionID != metricVersionID ||
		results[0].PublishedSchema != physical.PublishedSchema ||
		results[0].PublishedName != physical.PublishedName {
		t.Fatalf("690 search results=%+v err=%v", results, err)
	}

	unauthorizedID := uuid.NewString()
	rowRestrictedID := uuid.NewString()
	columnRestrictedID := uuid.NewString()
	otherTenantID := uuid.NewString()
	otherTenantActorID := uuid.NewString()
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
				id,tenant_id,email,display_name,password_hash
			) VALUES
			  ($1,$4,$5,'No member access','test-hash'),
			  ($2,$4,$6,'Row restricted member reader','test-hash'),
			  ($3,$4,$7,'Column restricted member reader','test-hash')`,
			unauthorizedID, rowRestrictedID, columnRestrictedID, tenantID,
			unauthorizedID+"@example.test", rowRestrictedID+"@example.test",
			columnRestrictedID+"@example.test"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.object_permissions(
				tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
			) VALUES
			  ($1,'USER',$2,'DATASET',$4,'READ',$5),
			  ($1,'USER',$3,'DATASET',$4,'READ',$5)`,
			tenantID, rowRestrictedID, columnRestrictedID, dwsDatasetID,
			actorID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.data_row_policies(
				tenant_id,object_type,object_id,name,expression_dsl,
				applicable_user_ids
			) VALUES(
				$1,'DATASET',$2,'member index must not bypass row scope',
				'{"type":"EQUALS","left":{"type":"FIELD_REF","fieldCode":"region_code"},"right":{"type":"LITERAL","value":"华东"}}',
				ARRAY[$3::uuid]
			)`, tenantID, dwsDatasetID, rowRestrictedID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.data_column_policies(
				tenant_id,object_type,object_id,field_code,policy_type,
				mask_rule,applicable_user_ids
			) VALUES(
				$1,'DATASET',$2,'circle_code','MASK',
				'{"type":"PARTIAL","prefixLength":0,"suffixLength":0,"maskChar":"*"}',
				ARRAY[$3::uuid]
			)`, tenantID, dwsDatasetID, columnRestrictedID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
		VALUES($1,$2,'Other semantic tenant')`,
		otherTenantID, "semantic_other_"+otherTenantID[:8]); err != nil {
		t.Fatal(err)
	}
	if err := database.WithTenantTx(
		ctx, pool, otherTenantID,
		func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO platform.users(
					id,tenant_id,email,display_name,password_hash
				) VALUES($1,$2,$3,'Other tenant reader','test-hash')`,
				otherTenantActorID, otherTenantID,
				otherTenantActorID+"@example.test")
			return err
		},
	); err != nil {
		t.Fatal(err)
	}

	for name, restrictedActorID := range map[string]string{
		"no dataset permission":  unauthorizedID,
		"applicable row policy":  rowRestrictedID,
		"masked dimension field": columnRestrictedID,
	} {
		t.Run("member access "+name, func(t *testing.T) {
			if _, _, err := service.ListDimensionMembers(
				ctx, tenantID, restrictedActorID,
				DimensionMemberFilter{
					Page: Page{Limit: 20}, DimensionID: dimension.ID,
				},
			); !errors.Is(err, ErrMemberAccessDenied) {
				t.Fatalf("member enumeration was not denied: %v", err)
			}
			if _, _, err := service.ListDimensionMemberAliases(
				ctx, tenantID, restrictedActorID,
				DimensionMemberAliasFilter{
					Page: Page{Limit: 20}, DimensionID: dimension.ID,
				},
			); !errors.Is(err, ErrMemberAccessDenied) {
				t.Fatalf("alias enumeration was not denied: %v", err)
			}
			filteredAliases, filteredTotal, err :=
				service.ListDimensionMemberAliases(
					ctx, tenantID, restrictedActorID,
					DimensionMemberAliasFilter{Page: Page{Limit: 20}},
				)
			if err != nil || filteredTotal != 0 || len(filteredAliases) != 0 {
				t.Fatalf(
					"broad alias scope leaked: aliases=%+v total=%d err=%v",
					filteredAliases, filteredTotal, err,
				)
			}
			filteredSearch, err := service.SearchMemberMetrics(
				ctx, tenantID, restrictedActorID, "690", 20,
			)
			if err != nil || len(filteredSearch) != 0 {
				t.Fatalf(
					"member search leaked: results=%+v err=%v",
					filteredSearch, err,
				)
			}
		})
	}
	if _, _, err := service.ListDimensionMembers(
		ctx, otherTenantID, otherTenantActorID,
		DimensionMemberFilter{
			Page: Page{Limit: 20}, DimensionID: dimension.ID,
		},
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant dimension did not remain nonexistent: %v", err)
	}

	replayed, created, err := service.CreateRefreshJob(
		ctx, tenantID, actorID, dimension.ID, "full-refresh-1",
		CreateRefreshJobInput{ExpectedDimensionVersion: dimension.Version, MaxMembers: 10, TimeoutSeconds: 30},
	)
	if err != nil || !created || replayed.ID == refresh.ID || replayed.Status != "QUEUED" {
		t.Fatalf("create explicit refresh after automatic refresh: item=%+v created=%v err=%v",
			replayed, created, err)
	}
	refresh = replayed
	replayed, created, err = service.CreateRefreshJob(
		ctx, tenantID, actorID, dimension.ID, "full-refresh-1",
		CreateRefreshJobInput{ExpectedDimensionVersion: dimension.Version, MaxMembers: 10, TimeoutSeconds: 30},
	)
	if err != nil || created || replayed.ID != refresh.ID || replayed.Status != "QUEUED" {
		t.Fatalf("refresh replay: item=%+v created=%v err=%v", replayed, created, err)
	}
	if _, _, err := service.CreateRefreshJob(
		ctx, tenantID, actorID, dimension.ID, "full-refresh-1",
		CreateRefreshJobInput{ExpectedDimensionVersion: dimension.Version, MaxMembers: 9, TimeoutSeconds: 30},
	); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("refresh idempotency conflict error=%v", err)
	}
	processed, err = NewDimensionRefreshWorker(store).ProcessNext(
		ctx, tenantID, "semantic-dimension-refresh", time.Minute,
	)
	if err != nil || !processed {
		t.Fatalf("process explicit refresh: processed=%v err=%v", processed, err)
	}

	failedRefresh, created, err := service.CreateRefreshJob(
		ctx, tenantID, actorID, dimension.ID, "bounded-refresh",
		CreateRefreshJobInput{
			ExpectedDimensionVersion: dimension.Version,
			MaxMembers:               1, TimeoutSeconds: 30,
		},
	)
	if err != nil || !created {
		t.Fatalf("create bounded refresh: item=%+v created=%v err=%v", failedRefresh, created, err)
	}
	processed, err = NewDimensionRefreshWorker(store).ProcessNext(
		ctx, tenantID, "semantic-dimension-refresh", time.Minute,
	)
	if !processed || !errors.Is(err, ErrRefreshCardinality) {
		t.Fatalf("bounded refresh: processed=%v err=%v", processed, err)
	}
	jobs, _, err := service.ListRefreshJobs(ctx, tenantID, RefreshJobFilter{
		Page: Page{Limit: 50}, DimensionID: dimension.ID, Status: "FAILED",
	})
	if err != nil || len(jobs) != 1 ||
		jobs[0].ResultCode != "CARDINALITY_LIMIT_EXCEEDED" {
		t.Fatalf("failed refresh jobs=%+v err=%v", jobs, err)
	}
	members, total, err = service.ListDimensionMembers(ctx, tenantID, actorID, DimensionMemberFilter{
		Page: Page{Limit: 50}, DimensionID: dimension.ID, Status: "ACTIVE",
	})
	if err != nil || total != 2 || len(members) != 2 {
		t.Fatalf("failed refresh changed atomic snapshot: members=%+v total=%d err=%v",
			members, total, err)
	}

	// The second cursor page contains only case-normalized duplicates of the
	// first page. A page with zero newly normalized values is not EOF: the
	// unique value on the third page must still be indexed.
	if _, err := pool.Exec(ctx, "TRUNCATE "+physicalTable); err != nil {
		t.Fatal(err)
	}
	sourceRows := make([][]any, 0, 2001)
	for index := 0; index < 1000; index++ {
		sourceRows = append(sourceRows, []any{
			fmt.Sprintf("A%04d", index), fmt.Sprintf("cursor-a-%04d", index), 10,
		})
	}
	for index := 0; index < 1000; index++ {
		sourceRows = append(sourceRows, []any{
			fmt.Sprintf("a%04d", index), fmt.Sprintf("cursor-b-%04d", index), 20,
		})
	}
	sourceRows = append(sourceRows, []any{"z-final", "cursor-final", 30})
	if _, err := pool.CopyFrom(
		ctx, pgx.Identifier{physical.Schema, physical.Name},
		[]string{"circle_code", "customer_id", "total_amount"},
		pgx.CopyFromRows(sourceRows),
	); err != nil {
		t.Fatal(err)
	}
	cursorRefresh, created, err := service.CreateRefreshJob(
		ctx, tenantID, actorID, dimension.ID, "normalized-empty-cursor-page",
		CreateRefreshJobInput{
			ExpectedDimensionVersion: dimension.Version,
			MaxMembers:               2000, TimeoutSeconds: 30,
		},
	)
	if err != nil || !created {
		t.Fatalf("create cursor refresh: item=%+v created=%v err=%v",
			cursorRefresh, created, err)
	}
	processed, err = NewDimensionRefreshWorker(store).ProcessNext(
		ctx, tenantID, "semantic-dimension-refresh", time.Minute,
	)
	if err != nil || !processed {
		t.Fatalf("process cursor refresh: processed=%v err=%v", processed, err)
	}
	members, total, err = service.ListDimensionMembers(ctx, tenantID, actorID, DimensionMemberFilter{
		Page: Page{Limit: 50}, DimensionID: dimension.ID, Query: "z-final", Status: "ACTIVE",
	})
	if err != nil || total != 1 || len(members) != 1 ||
		members[0].CanonicalLabel != "z-final" {
		t.Fatalf("third cursor page was not indexed: members=%+v total=%d err=%v",
			members, total, err)
	}

	dmlFenceRefresh, created, err := service.CreateRefreshJob(
		ctx, tenantID, actorID, dimension.ID, "physical-dml-fence",
		CreateRefreshJobInput{
			ExpectedDimensionVersion: dimension.Version,
			MaxMembers:               2000,
			TimeoutSeconds:           30,
		},
	)
	if err != nil || !created {
		t.Fatalf(
			"create physical DML fence refresh: item=%+v created=%v err=%v",
			dmlFenceRefresh, created, err,
		)
	}
	dmlFenceClaim, err := store.ClaimDimensionRefresh(
		ctx, tenantID, "physical-dml-fence-refresh", time.Minute,
	)
	if err != nil || dmlFenceClaim == nil ||
		dmlFenceClaim.ID != dmlFenceRefresh.ID {
		t.Fatalf(
			"claim physical DML fence refresh: claim=%+v err=%v",
			dmlFenceClaim, err,
		)
	}
	dmlScanLocked := make(chan struct{})
	releaseDMLScan := make(chan struct{})
	dmlCommitLocked := make(chan struct{})
	releaseDMLCommit := make(chan struct{})
	dmlScanReleased, dmlCommitReleased := false, false
	defer func() {
		store.dimensionRefreshScanHook = nil
		store.dimensionRefreshCommitHook = nil
		if !dmlScanReleased {
			close(releaseDMLScan)
		}
		if !dmlCommitReleased {
			close(releaseDMLCommit)
		}
	}()
	store.dimensionRefreshScanHook = func(hookCtx context.Context) error {
		close(dmlScanLocked)
		select {
		case <-releaseDMLScan:
			return nil
		case <-hookCtx.Done():
			return hookCtx.Err()
		}
	}
	store.dimensionRefreshCommitHook = func(hookCtx context.Context) error {
		close(dmlCommitLocked)
		select {
		case <-releaseDMLCommit:
			return nil
		case <-hookCtx.Done():
			return hookCtx.Err()
		}
	}
	dmlFenceRefreshErr := make(chan error, 1)
	go func() {
		dmlFenceRefreshErr <- store.RefreshDimensionMembers(
			ctx, *dmlFenceClaim, "physical-dml-fence-refresh",
		)
	}()
	select {
	case <-dmlScanLocked:
	case <-time.After(5 * time.Second):
		t.Fatal("DML fence refresh did not enter the physical-table scan phase")
	}

	dmlConnection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dmlConnectionReleased := false
	defer func() {
		if !dmlConnectionReleased {
			dmlConnection.Release()
		}
	}()
	var dmlPID int
	if err := dmlConnection.QueryRow(
		ctx, `SELECT pg_backend_pid()`,
	).Scan(&dmlPID); err != nil {
		t.Fatal(err)
	}
	dmlErr := make(chan error, 1)
	go func() {
		_, updateErr := dmlConnection.Exec(
			ctx, "UPDATE "+physicalTable+
				" SET total_amount=total_amount+1 WHERE customer_id='cursor-final'",
		)
		dmlErr <- updateErr
	}()
	if err := waitForSemanticLockWait(ctx, pool, dmlPID, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case updateErr := <-dmlErr:
		t.Fatalf("physical DML crossed the scan lock: %v", updateErr)
	default:
	}

	close(releaseDMLScan)
	dmlScanReleased = true
	select {
	case <-dmlCommitLocked:
	case updateErr := <-dmlErr:
		t.Fatalf("physical DML crossed the scan/merge boundary: %v", updateErr)
	case refreshRunErr := <-dmlFenceRefreshErr:
		t.Fatalf("DML fence refresh ended before late-gate merge: %v", refreshRunErr)
	case <-time.After(10 * time.Second):
		t.Fatal("DML fence refresh did not reach the generation commit")
	}
	select {
	case updateErr := <-dmlErr:
		t.Fatalf("physical DML crossed the late-gate merge lock: %v", updateErr)
	default:
	}

	close(releaseDMLCommit)
	dmlCommitReleased = true
	if refreshRunErr := <-dmlFenceRefreshErr; refreshRunErr != nil {
		t.Fatalf("DML fence refresh failed: %v", refreshRunErr)
	}
	if updateErr := <-dmlErr; updateErr != nil {
		t.Fatalf("physical DML did not resume after generation commit: %v", updateErr)
	}
	dmlConnection.Release()
	dmlConnectionReleased = true
	store.dimensionRefreshScanHook = nil
	store.dimensionRefreshCommitHook = nil

	sensitivityTag, err = semanticService.DeprecateTag(
		ctx, tenantID, actorID, sensitivityTag.ID, sensitivityTag.Version,
	)
	if err != nil || sensitivityTag.Status != "DEPRECATED" {
		t.Fatalf("deprecate sensitivity tag: item=%+v err=%v", sensitivityTag, err)
	}
	switchRefresh, created, err := service.CreateRefreshJob(
		ctx, tenantID, actorID, dimension.ID, "source-switch-during-scan",
		CreateRefreshJobInput{
			ExpectedDimensionVersion: dimension.Version,
			MaxMembers:               2000,
			TimeoutSeconds:           30,
		},
	)
	if err != nil || !created {
		t.Fatalf(
			"create source-switch refresh: item=%+v created=%v err=%v",
			switchRefresh, created, err,
		)
	}
	switchClaim, err := store.ClaimDimensionRefresh(
		ctx, tenantID, "source-switch-refresh", time.Minute,
	)
	if err != nil || switchClaim == nil || switchClaim.ID != switchRefresh.ID {
		t.Fatalf("claim source-switch refresh: claim=%+v err=%v", switchClaim, err)
	}
	scanLocked := make(chan struct{})
	releaseScan := make(chan struct{})
	scanReleased := false
	defer func() {
		if !scanReleased {
			close(releaseScan)
		}
	}()
	store.dimensionRefreshScanHook = func(hookCtx context.Context) error {
		close(scanLocked)
		select {
		case <-releaseScan:
			return nil
		case <-hookCtx.Done():
			return hookCtx.Err()
		}
	}
	refreshErr := make(chan error, 1)
	go func() {
		refreshErr <- store.RefreshDimensionMembers(
			ctx, *switchClaim, "source-switch-refresh",
		)
	}()
	select {
	case <-scanLocked:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not enter the physical-table scan phase")
	}

	governanceWriteCtx, cancelGovernanceWrite :=
		context.WithTimeout(ctx, 2*time.Second)
	_, err = semanticService.CreateTag(
		governanceWriteCtx, tenantID, actorID,
		CreateTagInput{
			Code: "scan_concurrency_" + tenantID[:8],
			Name: "扫描并发治理写", Category: "FREEFORM",
			Governance: "FREEFORM", Status: "DRAFT",
		},
	)
	cancelGovernanceWrite()
	if err != nil {
		t.Fatalf(
			"unrelated governance write was blocked by long scan: %v", err,
		)
	}

	activationCtx, cancelActivation := context.WithTimeout(ctx, 15*time.Second)
	replacement, err := activateReplacementDWSMaterialization(
		activationCtx, pool, materializationStore, tenantID, actorID,
		dwdDatasetID, dwdVersionID, dwsDatasetID, dwsVersionID,
	)
	cancelActivation()
	if err != nil {
		t.Fatalf("replacement activation was convoyed behind scan: %v", err)
	}
	close(releaseScan)
	scanReleased = true
	err = <-refreshErr
	store.dimensionRefreshScanHook = nil
	if !errors.Is(err, ErrRefreshSourceChanged) &&
		!errors.Is(err, ErrRefreshLeaseLost) {
		t.Fatalf("source switch did not fail fenced refresh commit: %v", err)
	}
	skippedSourceSwitchJobs, _, err := service.ListRefreshJobs(
		ctx, tenantID,
		RefreshJobFilter{
			Page: Page{Limit: 50}, DimensionID: dimension.ID, Status: "SKIPPED",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	foundSkippedSourceSwitch := false
	for _, job := range skippedSourceSwitchJobs {
		if job.ID == switchRefresh.ID &&
			job.ResultCode == "MEMBER_INDEX_POLICY_CHANGED" {
			foundSkippedSourceSwitch = true
		}
	}
	if !foundSkippedSourceSwitch {
		t.Fatalf(
			"source-switch job was not atomically fenced: jobs=%+v",
			skippedSourceSwitchJobs,
		)
	}
	dimension, err = service.GetDimension(ctx, tenantID, dimension.ID)
	if err != nil || dimension.MemberIndexPolicy != "NONE" ||
		dimension.MemberRefreshGeneration != "" ||
		dimension.MemberCount != nil ||
		dimension.LastMemberRefreshJobID != "" {
		t.Fatalf(
			"active materialization did not immediately invalidate FULL dimension: item=%+v err=%v",
			dimension, err,
		)
	}
	members, total, err = service.ListDimensionMembers(
		ctx, tenantID, actorID,
		DimensionMemberFilter{
			Page: Page{Limit: 50}, DimensionID: dimension.ID,
			Status: "DEPRECATED",
		},
	)
	if err != nil || total != 0 || len(members) != 0 {
		t.Fatalf(
			"superseded members were publicly enumerable: members=%+v total=%d err=%v",
			members, total, err,
		)
	}
	aliases, aliasTotal, err := service.ListDimensionMemberAliases(
		ctx, tenantID, actorID,
		DimensionMemberAliasFilter{
			Page: Page{Limit: 50}, DimensionID: dimension.ID,
		},
	)
	if err != nil || aliasTotal != 0 || len(aliases) != 0 {
		t.Fatalf(
			"superseded aliases were publicly enumerable: aliases=%+v total=%d err=%v",
			aliases, aliasTotal, err,
		)
	}
	results, err = service.SearchMemberMetrics(ctx, tenantID, actorID, "690", 20)
	if err != nil || len(results) != 0 {
		t.Fatalf("superseded member remained searchable: results=%+v err=%v", results, err)
	}
	for index := 0; index < 4; index++ {
		processed, profileErr := profileWorker.ProcessNext(
			ctx, tenantID, "semantic-dimension-profile", 2*time.Minute,
		)
		if profileErr != nil || !processed {
			t.Fatalf(
				"process replacement profile %d: processed=%v err=%v",
				index, processed, profileErr,
			)
		}
	}
	var sensitiveProfileStatus, sensitiveProfileCode, sensitiveProfilePolicy string
	var sensitiveProfileRows, sensitiveProfileDistinct *int64
	if err := database.WithTenantTx(
		ctx, pool, tenantID,
		func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT status,result_code,
					recommended_member_index_policy,row_count,distinct_count
				FROM platform.dimension_profile_jobs
				WHERE materialization_id=$1::uuid
				  AND field_id='field_region'`,
				replacement.ID,
			).Scan(
				&sensitiveProfileStatus, &sensitiveProfileCode,
				&sensitiveProfilePolicy, &sensitiveProfileRows,
				&sensitiveProfileDistinct,
			)
		},
	); err != nil {
		t.Fatal(err)
	}
	if sensitiveProfileStatus != "SKIPPED_POLICY" ||
		sensitiveProfileCode != "SENSITIVE_FIELD_PROFILE_SKIPPED" ||
		sensitiveProfilePolicy != "NONE" ||
		sensitiveProfileRows != nil || sensitiveProfileDistinct != nil {
		t.Fatalf(
			"historical sensitivity floor scanned values or relaxed: status=%q code=%q policy=%q rows=%v ndv=%v",
			sensitiveProfileStatus, sensitiveProfileCode,
			sensitiveProfilePolicy, sensitiveProfileRows,
			sensitiveProfileDistinct,
		)
	}
	dimension, err = service.UpdateDimension(
		ctx, tenantID, actorID, dimension.ID,
		UpdateDimensionInput{
			ExpectedVersion:   dimension.Version,
			Code:              dimension.Code,
			Name:              dimension.Name,
			Description:       dimension.Description,
			DimensionType:     dimension.DimensionType,
			MemberIndexPolicy: "FULL",
			HighCardinality:   false,
			Sensitive:         false,
			Status:            "PUBLISHED",
		},
	)
	if err != nil {
		t.Fatalf("restore FULL after replacement profile: %v", err)
	}
	members, total, err = service.ListDimensionMembers(
		ctx, tenantID, actorID,
		DimensionMemberFilter{
			Page: Page{Limit: 50}, DimensionID: dimension.ID,
			Status: "DEPRECATED",
		},
	)
	if err != nil || total != 0 || len(members) != 0 {
		t.Fatalf(
			"old members leaked after FULL but before refresh: members=%+v total=%d err=%v",
			members, total, err,
		)
	}
	aliases, aliasTotal, err = service.ListDimensionMemberAliases(
		ctx, tenantID, actorID,
		DimensionMemberAliasFilter{
			Page: Page{Limit: 50}, DimensionID: dimension.ID,
		},
	)
	if err != nil || aliasTotal != 0 || len(aliases) != 0 {
		t.Fatalf(
			"old aliases leaked after FULL but before refresh: aliases=%+v total=%d err=%v",
			aliases, aliasTotal, err,
		)
	}
	replacementRefresh, created, err := service.CreateRefreshJob(
		ctx, tenantID, actorID, dimension.ID, "replacement-full-refresh",
		CreateRefreshJobInput{
			ExpectedDimensionVersion: dimension.Version,
			MaxMembers:               100,
			TimeoutSeconds:           30,
		},
	)
	if err != nil || !created ||
		replacementRefresh.MaterializationID != replacement.ID {
		t.Fatalf(
			"replacement refresh: item=%+v created=%v err=%v",
			replacementRefresh, created, err,
		)
	}
	processed, err = NewDimensionRefreshWorker(store).ProcessNext(
		ctx, tenantID, "semantic-dimension-refresh", time.Minute,
	)
	if err != nil || !processed {
		t.Fatalf("process replacement refresh: processed=%v err=%v", processed, err)
	}
	dimension, err = service.GetDimension(ctx, tenantID, dimension.ID)
	if err != nil {
		t.Fatal(err)
	}

	dimension, err = updateIntegrationDimensionPolicy(
		ctx, service, tenantID, actorID, dimension, "EXACT_ONLY",
	)
	if err != nil {
		t.Fatal(err)
	}
	skipped, created, err := service.CreateRefreshJob(
		ctx, tenantID, actorID, dimension.ID, "exact-only-refresh",
		CreateRefreshJobInput{ExpectedDimensionVersion: dimension.Version},
	)
	if err != nil || !created || skipped.Status != "SKIPPED" ||
		skipped.ResultCode != "EXACT_ONLY_AUTOMATIC_DISCOVERY_SKIPPED" {
		t.Fatalf("EXACT_ONLY refresh: item=%+v created=%v err=%v", skipped, created, err)
	}
	dimension, err = service.UpdateDimension(ctx, tenantID, actorID, dimension.ID, UpdateDimensionInput{
		ExpectedVersion: dimension.Version,
		Code:            dimension.Code, Name: dimension.Name, Description: dimension.Description,
		DimensionType: dimension.DimensionType, MemberIndexPolicy: "NONE",
		HighCardinality: dimension.HighCardinality, Sensitive: true, Status: "PUBLISHED",
	})
	if err != nil {
		t.Fatal(err)
	}
	members, total, err = service.ListDimensionMembers(ctx, tenantID, actorID, DimensionMemberFilter{
		Page: Page{Limit: 50}, DimensionID: dimension.ID, Status: "DEPRECATED",
	})
	if err != nil || total != 0 || len(members) != 0 {
		t.Fatalf("sensitive member history was enumerable: members=%+v total=%d err=%v",
			members, total, err)
	}
	aliases, aliasTotal, err = service.ListDimensionMemberAliases(
		ctx, tenantID, actorID, DimensionMemberAliasFilter{
			Page: Page{Limit: 50}, DimensionID: dimension.ID,
		},
	)
	if err != nil || aliasTotal != 0 || len(aliases) != 0 {
		t.Fatalf("sensitive member aliases were enumerable: aliases=%+v total=%d err=%v",
			aliases, aliasTotal, err)
	}
	results, err = service.SearchMemberMetrics(ctx, tenantID, actorID, "690", 20)
	if err != nil || len(results) != 0 {
		t.Fatalf("sensitive member remained searchable: results=%+v err=%v", results, err)
	}
	dimension, err = updateIntegrationDimensionPolicy(
		ctx, service, tenantID, actorID, dimension, "NONE",
	)
	if err != nil {
		t.Fatal(err)
	}
	skipped, created, err = service.CreateRefreshJob(
		ctx, tenantID, actorID, dimension.ID, "none-refresh",
		CreateRefreshJobInput{ExpectedDimensionVersion: dimension.Version},
	)
	if err != nil || !created || skipped.Status != "SKIPPED" ||
		skipped.ResultCode != "MEMBER_INDEX_DISABLED" {
		t.Fatalf("NONE refresh: item=%+v created=%v err=%v", skipped, created, err)
	}
	members, total, err = service.ListDimensionMembers(ctx, tenantID, actorID, DimensionMemberFilter{
		Page: Page{Limit: 50}, DimensionID: dimension.ID, Status: "ACTIVE",
	})
	if err != nil || total != 0 || len(members) != 0 {
		t.Fatalf("restricted policy retained FULL index: members=%+v total=%d err=%v",
			members, total, err)
	}
}

func TestPostgresDimensionProfilePermissions(t *testing.T) {
	workerURL := os.Getenv("SEMANTIC_MANAGEMENT_TEST_DATABASE_URL")
	appURL := os.Getenv("SEMANTIC_MANAGEMENT_APP_TEST_DATABASE_URL")
	if workerURL == "" || appURL == "" {
		t.Skip("semantic worker/app integration database URLs are not set")
	}
	ctx := context.Background()
	workerPool, err := pgxpool.New(ctx, workerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer workerPool.Close()

	var publicExecute, appExecute, workerExecute bool
	var appProfileWrite, appMemberWrite bool
	if err := workerPool.QueryRow(ctx, `SELECT
			has_function_privilege(
			  'public',
			  'platform.apply_dimension_profile_resource_limits(uuid,integer,text,uuid)',
			  'EXECUTE'
			),
			has_function_privilege(
			  'report_app',
			  'platform.apply_dimension_profile_resource_limits(uuid,integer,text,uuid)',
			  'EXECUTE'
			),
			has_function_privilege(
			  'report_worker',
			  'platform.apply_dimension_profile_resource_limits(uuid,integer,text,uuid)',
			  'EXECUTE'
			),
			has_table_privilege(
			  'report_app','platform.dimension_profile_jobs','INSERT,UPDATE,DELETE'
			),
			has_table_privilege(
			  'report_app','platform.dimension_members','INSERT,UPDATE,DELETE'
			)`).Scan(
		&publicExecute, &appExecute, &workerExecute,
		&appProfileWrite, &appMemberWrite,
	); err != nil {
		t.Fatal(err)
	}
	if publicExecute || appExecute || !workerExecute ||
		appProfileWrite || appMemberWrite {
		t.Fatalf(
			"unsafe profile privileges public=%v app=%v worker=%v profileDML=%v memberDML=%v",
			publicExecute, appExecute, workerExecute,
			appProfileWrite, appMemberWrite,
		)
	}

	appPool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer appPool.Close()
	assertPermissionDenied := func(label, sql string) {
		t.Helper()
		_, execErr := appPool.Exec(ctx, sql)
		var databaseError *pgconn.PgError
		if !errors.As(execErr, &databaseError) ||
			databaseError.Code != "42501" {
			t.Fatalf("%s was not denied: %v", label, execErr)
		}
	}
	assertPermissionDenied(
		"profile resource helper",
		`SELECT platform.apply_dimension_profile_resource_limits(
			gen_random_uuid(),1,'unauthorized',gen_random_uuid()
		)`,
	)
	assertPermissionDenied(
		"profile evidence mutation",
		`UPDATE platform.dimension_profile_jobs
		 SET updated_at=updated_at WHERE false`,
	)
	assertPermissionDenied(
		"dimension member mutation",
		`UPDATE platform.dimension_members
		 SET updated_at=updated_at WHERE false`,
	)
}

func waitForSemanticLockWait(
	ctx context.Context,
	pool *pgxpool.Pool,
	pid int,
	timeout time.Duration,
) error {
	deadline := time.Now().Add(timeout)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT COALESCE((
				SELECT wait_event_type='Lock'
				FROM pg_stat_activity
				WHERE pid=$1
			),false)`, pid).Scan(&waiting); err != nil {
			return err
		}
		if waiting {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New(
				"physical DML did not wait on the refresh relation lock",
			)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func createSemanticPublishedDatasetFixture(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID, datasetID, draftID, publishedID string,
	layer materialization.Layer,
	code string,
	includeSemanticFields bool,
) error {
	datasetType := "SINGLE_SOURCE"
	if layer != materialization.LayerODS {
		datasetType = "CROSS_SOURCE"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.datasets(
		id,tenant_id,code,name,dataset_type,status,created_by,updated_by,layer
	) VALUES($1,$2,$3,$4,$5,'PUBLISHED',$6,$6,$7)`,
		datasetID, tenantID, code, code, datasetType, actorID, layer); err != nil {
		return err
	}
	dsl := json.RawMessage(`{"dataset":{"code":"fixture"},"nodes":[]}`)
	logicalPlan := json.RawMessage(`{"nodes":[]}`)
	if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_versions(
		id,tenant_id,dataset_id,version_no,status,dsl_version,dsl_json,
		schema_hash,logical_plan_json,plan_hash,created_by,updated_by,layer
	) VALUES($1,$2,$3,1,'DRAFT','1.0',$4,$5,$6,$5,$7,$7,$8)`,
		draftID, tenantID, datasetID, dsl, semanticDimensionTestSchemaHash,
		logicalPlan, actorID, layer); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_versions(
		id,tenant_id,dataset_id,version_no,status,dsl_version,dsl_json,
		schema_hash,logical_plan_json,plan_hash,created_by,updated_by,
		published_at,published_by,source_draft_version_id,
		source_draft_record_version,layer
	) VALUES($1,$2,$3,2,'PUBLISHING','1.0',$4,$5,$6,$5,$7,$7,
		now(),$7,$8,1,$9)`,
		publishedID, tenantID, datasetID, dsl, semanticDimensionTestSchemaHash,
		logicalPlan, actorID, draftID, layer); err != nil {
		return err
	}
	if includeSemanticFields {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_fields(
				tenant_id,dataset_version_id,field_id,field_code,field_name,
				description,expression_json,canonical_type,semantic_type,field_role,
				aggregation,nullable,visible,ordinal_position
			) VALUES
				($1,$2,'field_circle','circle_code','生态圈','',
				 '{"type":"FIELD_REF","nodeId":"aggregate","field":"circle_code"}',
				 'STRING','','DIMENSION','',false,true,1),
				($1,$2,'field_region','region_code','区域','',
				 '{"type":"FIELD_REF","nodeId":"aggregate","field":"region_code"}',
				 'STRING','','DIMENSION','',true,true,2),
				($1,$2,'field_channel','channel_code','渠道','',
				 '{"type":"FIELD_REF","nodeId":"aggregate","field":"channel_code"}',
				 'STRING','','DIMENSION','',true,true,3),
				($1,$2,'field_identifier','customer_id','客户标识','',
				 '{"type":"FIELD_REF","nodeId":"aggregate","field":"customer_id"}',
				 'STRING','IDENTIFIER','IDENTIFIER','',false,true,4),
				($1,$2,'field_amount','total_amount','总金额','',
				 '{"type":"FIELD_REF","nodeId":"aggregate","field":"total_amount"}',
				 'DECIMAL','','MEASURE','SUM',false,true,5)`,
			tenantID, publishedID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.dataset_versions
		SET status='PUBLISHED' WHERE id=$1`, publishedID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE platform.datasets SET
		current_draft_version_id=$1,current_published_version_id=$2
		WHERE id=$3`, draftID, publishedID, datasetID)
	return err
}

func createPublishedMetricFixture(
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, actorID, datasetID, datasetVersionID,
	metricID, metricDraftID, metricVersionID string,
) error {
	return database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.metrics(
			id,tenant_id,dataset_id,code,name,metric_type,status,created_by,updated_by
		) VALUES($1,$2,$3,$4,'生态圈销售额','ATOMIC','DRAFT',$5,$5)`,
			metricID, tenantID, datasetID, "ecosystem_revenue_"+metricID[:8], actorID); err != nil {
			return err
		}
		definition := json.RawMessage(`{"version":"1.0"}`)
		if _, err := tx.Exec(ctx, `INSERT INTO platform.metric_versions(
			id,tenant_id,metric_id,dataset_id,dataset_version_id,version_no,status,
			definition_version,definition_json,definition_hash,created_by,updated_by
		) VALUES($1,$2,$3,$4,$5,1,'DRAFT','1.0',$6,$7,$8,$8)`,
			metricDraftID, tenantID, metricID, datasetID, datasetVersionID,
			definition, semanticDimensionTestSchemaHash, actorID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.metric_versions(
			id,tenant_id,metric_id,dataset_id,dataset_version_id,version_no,status,
			definition_version,definition_json,definition_hash,created_by,updated_by,
			published_at,published_by,source_draft_version_id,source_draft_record_version
		) VALUES($1,$2,$3,$4,$5,2,'PUBLISHING','1.0',$6,$7,$8,$8,
			now(),$8,$9,1)`,
			metricVersionID, tenantID, metricID, datasetID, datasetVersionID,
			definition, semanticDimensionTestSchemaHash, actorID, metricDraftID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.metric_versions
			SET status='PUBLISHED' WHERE id=$1`, metricVersionID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE platform.metrics SET
			status='PUBLISHED',current_draft_version_id=$1,
			current_published_version_id=$2 WHERE id=$3`,
			metricDraftID, metricVersionID, metricID)
		return err
	})
}

func updateIntegrationDimensionPolicy(
	ctx context.Context,
	service *DimensionService,
	tenantID, actorID string,
	dimension Dimension,
	policy string,
) (Dimension, error) {
	return service.UpdateDimension(ctx, tenantID, actorID, dimension.ID, UpdateDimensionInput{
		ExpectedVersion: dimension.Version,
		Code:            dimension.Code, Name: dimension.Name, Description: dimension.Description,
		DimensionType: dimension.DimensionType, MemberIndexPolicy: policy,
		HighCardinality: dimension.HighCardinality, Sensitive: dimension.Sensitive,
		Status: "PUBLISHED",
	})
}

func activateReplacementDWSMaterialization(
	ctx context.Context,
	pool *pgxpool.Pool,
	store *materialization.PostgresStore,
	tenantID, actorID, inputDatasetID, inputDatasetVersionID,
	datasetID, datasetVersionID string,
) (materialization.Materialization, error) {
	replacementSnapshotHash := strings.Repeat("c", 64)
	request := materialization.RegisterRequest{
		Plan: materialization.BuildPlan{
			Version:   materialization.PlanVersion,
			DatasetID: datasetID, DatasetVersionID: datasetVersionID,
			Layer: materialization.LayerDWS, Mode: materialization.RunModeFull,
			Nodes: []materialization.PlanNode{
				{
					ID: "extract", Kind: materialization.NodeExtract,
					Engine:        materialization.EnginePostgres,
					InputOrdinals: []int{1},
				},
				{
					ID: "aggregate", Kind: materialization.NodeAggregate,
					Engine:    materialization.EnginePostgres,
					DependsOn: []string{"extract"},
				},
				{
					ID: "materialize", Kind: materialization.NodeMaterialize,
					Engine:    materialization.EnginePostgres,
					DependsOn: []string{"aggregate"},
				},
			},
			Target: materialization.TargetPlan{
				Storage: "POSTGRES", AtomicPublish: true,
				RelationKind:   "TABLE",
				RefreshMode:    string(materialization.RunModeFull),
				StableViewName: true,
			},
		},
		Inputs: []materialization.InputSnapshot{{
			Ordinal: 1, Type: materialization.InputDatasetVersion,
			Layer:     string(materialization.LayerDWD),
			DatasetID: inputDatasetID, DatasetVersionID: inputDatasetVersionID,
			SourceVersion: "published:replacement",
			SchemaHash:    semanticDimensionTestSchemaHash,
			SnapshotHash:  replacementSnapshotHash,
			SnapshotJSON:  json.RawMessage(`{"watermark":"replacement"}`),
		}},
		MaxAttempts: 3,
	}
	run, created, err := store.Register(ctx, tenantID, actorID, request)
	if err != nil || !created {
		return materialization.Materialization{}, fmt.Errorf(
			"register replacement build: created=%v err=%w", created, err,
		)
	}
	claim, err := store.Claim(
		ctx, tenantID, "semantic-dimension-replacement", time.Minute,
	)
	if err != nil || claim == nil || claim.ID != run.ID {
		return materialization.Materialization{}, fmt.Errorf(
			"claim replacement build: claim=%+v err=%w", claim, err,
		)
	}
	for _, nodeID := range []string{"extract", "aggregate", "materialize"} {
		if err := store.StartNode(ctx, *claim, nodeID); err != nil {
			return materialization.Materialization{}, err
		}
		rows, bytes := int64(2), int64(512)
		if err := store.FinishNode(
			ctx, *claim, nodeID,
			materialization.NodeResult{
				Status:        materialization.NodeSucceeded,
				InputRowCount: &rows, OutputRowCount: &rows,
				OutputSizeBytes: &bytes,
			},
		); err != nil {
			return materialization.Materialization{}, err
		}
	}
	physical, err := materialization.GeneratePhysicalIdentifier(
		tenantID, datasetID, run.ID, materialization.LayerDWS,
	)
	if err != nil {
		return materialization.Materialization{}, err
	}
	qualified := quoteTrustedIdentifier(physical.Schema) + "." +
		quoteTrustedIdentifier(physical.Name)
	if _, err := pool.Exec(ctx, "CREATE TABLE "+qualified+
		" (circle_code text NOT NULL,region_code text,channel_code text,customer_id text NOT NULL,total_amount numeric NOT NULL)"); err != nil {
		return materialization.Materialization{}, err
	}
	if _, err := pool.Exec(ctx, "INSERT INTO "+qualified+
		"(circle_code,region_code,channel_code,customer_id,total_amount) VALUES"+
		"('新生态圈','华中','线上','replacement-1',50),"+
		"('新零售圈','西南','门店','replacement-2',60)"); err != nil {
		return materialization.Materialization{}, err
	}
	active, err := store.Activate(
		ctx, *claim,
		materialization.Activation{
			Physical: physical, RelationKind: "TABLE",
			SchemaHash:   semanticDimensionTestSchemaHash,
			SnapshotHash: replacementSnapshotHash,
			RowCount:     2, SizeBytes: 512,
			Watermark: json.RawMessage(`{"watermark":"replacement"}`),
		},
	)
	if err != nil {
		return materialization.Materialization{}, err
	}
	return active, nil
}

func assertOldPublishedVersionCannotReuseProfile(
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, actorID, datasetID, draftID, oldPublishedVersionID string,
) error {
	newPublishedVersionID := uuid.NewString()
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		dsl := json.RawMessage(`{"dataset":{"code":"profile-fence"},"nodes":[]}`)
		logicalPlan := json.RawMessage(`{"nodes":[]}`)
		if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_versions(
				id,tenant_id,dataset_id,version_no,status,dsl_version,dsl_json,
				schema_hash,logical_plan_json,plan_hash,created_by,updated_by,
				published_at,published_by,source_draft_version_id,
				source_draft_record_version,layer
			) VALUES($1,$2,$3,99,'PUBLISHING','1.0',$4,$5,$6,$5,$7,$7,
				now(),$7,$8,1,'DWS')`,
			newPublishedVersionID, tenantID, datasetID, dsl,
			semanticDimensionTestSchemaHash, logicalPlan, actorID, draftID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_fields(
				tenant_id,dataset_version_id,field_id,field_code,field_name,
				description,expression_json,canonical_type,semantic_type,
				field_role,aggregation,nullable,visible,ordinal_position
			)
			SELECT tenant_id,$1::uuid,field_id,field_code,field_name,
				description,expression_json,canonical_type,semantic_type,
				field_role,aggregation,nullable,visible,ordinal_position
			FROM platform.dataset_fields
			WHERE dataset_version_id=$2::uuid`,
			newPublishedVersionID, oldPublishedVersionID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.dataset_versions
			SET status='PUBLISHED' WHERE id=$1::uuid`,
			newPublishedVersionID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.datasets
			SET current_published_version_id=$1::uuid
			WHERE id=$2::uuid`, newPublishedVersionID, datasetID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.semantic_dimensions(
				tenant_id,dataset_id,dataset_version_id,field_id,code,name,
				description,dimension_type,member_index_policy,
				high_cardinality,sensitive,status,definition_hash,
				created_by,updated_by
			) VALUES(
				platform.current_tenant_id(),$1::uuid,$2::uuid,'field_circle',
				'old_version_profile_bypass','旧版本画像绕过','',
				'STANDARD','FULL',false,false,'PUBLISHED',$3,$4::uuid,$4::uuid
			)`, datasetID, oldPublishedVersionID, strings.Repeat("d", 64), actorID)
		return err
	})
	if err == nil {
		return errors.New("old published version reused current profile evidence")
	}
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != "23514" {
		return fmt.Errorf("old version fence returned unexpected error: %w", err)
	}
	return nil
}

func postgresErrorCode(err error) string {
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		return databaseError.Code
	}
	return ""
}
