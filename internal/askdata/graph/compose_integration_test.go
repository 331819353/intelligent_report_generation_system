package graph_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	nebula "github.com/vesoft-inc/nebula-go/v3"

	"intelligent-report-generation-system/internal/askdata"
	askgraph "intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/registry"
)

const composeIntegrationTimeout = 60 * time.Second

var composeVerificationProjectPattern = regexp.MustCompile(
	`^askdata-graph002-verify-([0-9]{10})-([1-9][0-9]{0,6})$`,
)

func TestNebulaComposeRolesPersistenceAndGraphPlan(t *testing.T) {
	if os.Getenv("ASKDATA_NEBULA_COMPOSE_INTEGRATION") != "1" {
		t.Skip("run scripts/verify-nebula-compose.sh")
	}
	if os.Getenv("ASKDATA_NEBULA_COMPOSE_ISOLATED") != "1" {
		t.Fatal("formal Graph integration may only run in an isolated Compose project")
	}
	project := requiredEnv(t, "ASKDATA_NEBULA_COMPOSE_PROJECT")
	addresses := mustAddresses(t, requiredEnv(t, "ASKDATA_NEBULA_COMPOSE_ADDRESSES"))
	space := requiredEnv(t, "ASKDATA_NEBULA_COMPOSE_SPACE")
	assertVerificationComposeTarget(t, project, space, addresses)
	readerUser := requiredEnv(t, "ASKDATA_NEBULA_COMPOSE_READER_USER")
	readerPassword := requiredEnv(t, "ASKDATA_NEBULA_COMPOSE_READER_PASSWORD")
	writerUser := requiredEnv(t, "ASKDATA_NEBULA_COMPOSE_WRITER_USER")
	writerPassword := requiredEnv(t, "ASKDATA_NEBULA_COMPOSE_WRITER_PASSWORD")
	if readerUser == writerUser || readerPassword == writerPassword {
		t.Fatal("reader and writer identities must be distinct")
	}

	request := pocGraphPlanRequest(t)
	cleanup := func() error {
		pool, err := composeSessionPool(writerUser, writerPassword, addresses, space)
		if err != nil {
			return fmt.Errorf("open writer cleanup session: %w", err)
		}
		defer pool.Close()
		return cleanupComposeFixture(pool, request)
	}
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup isolated graph fixture: %v", err)
		}
	})

	readerPool := waitForComposeSessionPool(t, readerUser, readerPassword, addresses, space)
	writerPool := waitForComposeSessionPool(t, writerUser, writerPassword, addresses, space)

	projector, err := askgraph.NewNebulaProjector(writerPool)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := composeProjectionSnapshot(request)
	proof, err := projector.Apply(context.Background(), snapshot, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("release projector could not rebuild the frozen GraphPlan schema: %v", err)
	}
	expectedProof, err := snapshot.Proof()
	if err != nil || proof != expectedProof {
		t.Fatalf("release projector proof = %#v, expected %#v, err=%v", proof, expectedProof, err)
	}
	assertComposeGraphPlan(t, readerPool, request)
	assertComposeRealScopeIsolation(t, readerPool, writerPool, request)
	assertComposeGraphPlan(t, readerPool, request)
	assertParameterEscaping(t, readerPool)
	assertConcurrentSessionPool(t, readerPool)
	assertMissingSpaceFails(t, readerUser, readerPassword, addresses)
	assertMissingSpaceFails(t, writerUser, writerPassword, addresses)
	assertComposeWrongPasswordFails(t, readerUser, readerPassword, addresses, space)
	assertComposeWrongPasswordFails(t, writerUser, writerPassword, addresses, space)

	readerMutation := fmt.Sprintf(
		`INSERT VERTEX metric(tenant_id,domain_id,release_hash,object_id,version_id,version_no) VALUES %s:("tenant-poc","sales","%s","forbidden","forbidden@v1",1)`,
		strconv.Quote("__graph002_reader_forbidden__"), request.Scope.Release.ContentHash,
	)
	assertComposeQueryDenied(t, readerPool, readerMutation, "GUEST data mutation")
	assertComposeQueryDenied(t, readerPool,
		`CREATE TAG IF NOT EXISTS semantic_model(tenant_id string, domain_id string, release_hash string, object_id string, version_id string, version_no int)`,
		"GUEST schema mutation",
	)
	assertComposeQueryDenied(t, writerPool,
		`CREATE TAG IF NOT EXISTS semantic_model(tenant_id string, domain_id string, release_hash string, object_id string, version_id string, version_no int)`,
		"USER schema mutation",
	)
	assertComposeQueryDenied(t, writerPool,
		fmt.Sprintf("GRANT ROLE USER ON %s TO %s", space, writerUser),
		"USER role administration",
	)

	readerPool.Close()
	writerPool.Close()

	if os.Getenv("ASKDATA_NEBULA_COMPOSE_RECREATE") == "1" {
		recreateFormalComposeGraph(t)
		readerPool = waitForComposeSessionPool(t, readerUser, readerPassword, addresses, space)
		writerPool = waitForComposeSessionPool(t, writerUser, writerPassword, addresses, space)
		assertComposeGraphPlan(t, readerPool, request)
		if err := cleanupComposeFixture(writerPool, request); err != nil {
			t.Fatalf("writer could not clean the persisted graph fixture: %v", err)
		}
		readerPool.Close()
		writerPool.Close()
	}
}

func composeProjectionSnapshot(request askgraph.PlanRequest) askgraph.ProjectionSnapshot {
	metricOrders := request.MetricRefs[0]
	metricRevenue := request.MetricRefs[1]
	modelLines := request.ModelRefs[0]
	modelOrders := request.ModelRefs[1]
	dimensionRegion := request.DimensionRefs[0]
	memberEast := request.MemberRefs[0]
	return askgraph.ProjectionSnapshot{
		TenantID: request.Scope.TenantID, DomainID: request.DomainID,
		ReleaseID: request.Scope.Release.ReleaseID, SemanticVersion: "compose-v1",
		ContentHash: request.Scope.Release.ContentHash, ManifestCount: 7,
		Vertices: []askgraph.ProjectionVertex{
			{Type: askgraph.ObjectTypeMetric, Ref: metricOrders},
			{Type: askgraph.ObjectTypeMetric, Ref: metricRevenue},
			{Type: askgraph.ObjectTypeSemanticModel, Ref: modelLines},
			{Type: askgraph.ObjectTypeSemanticModel, Ref: modelOrders},
			{Type: askgraph.ObjectTypeDimension, Ref: dimensionRegion},
			{Type: askgraph.ObjectTypeMember, Ref: memberEast, MemberStatus: askgraph.MemberStatusActive},
		},
		Edges: []askgraph.ProjectionEdge{
			{
				Type: askgraph.ProjectionEdgeModeledBy, FromType: askgraph.ObjectTypeMetric, From: metricOrders,
				ToType: askgraph.ObjectTypeSemanticModel, To: modelOrders,
			},
			{
				Type: askgraph.ProjectionEdgeModeledBy, FromType: askgraph.ObjectTypeMetric, From: metricRevenue,
				ToType: askgraph.ObjectTypeSemanticModel, To: modelLines,
			},
			{
				Type: askgraph.ProjectionEdgeHasDimension, FromType: askgraph.ObjectTypeSemanticModel, From: modelOrders,
				ToType: askgraph.ObjectTypeDimension, To: dimensionRegion,
			},
			{
				Type: askgraph.ProjectionEdgeHasMember, FromType: askgraph.ObjectTypeDimension, From: dimensionRegion,
				ToType: askgraph.ObjectTypeMember, To: memberEast,
			},
			{
				Type: askgraph.ProjectionEdgeJoinsTo, FromType: askgraph.ObjectTypeSemanticModel, From: modelOrders,
				ToType: askgraph.ObjectTypeSemanticModel, To: modelLines,
				RelationshipVersionID: "relationship-orders-lines@v1",
				JoinType:              registry.JoinInner, Cardinality: registry.CardinalityOneToMany,
				FanoutPolicy: registry.FanoutCertifiedPre, Certified: true,
			},
		},
	}
}

func composeSessionPool(
	username, password string,
	addresses []nebula.HostAddress,
	space string,
) (*nebula.SessionPool, error) {
	configuration, err := nebula.NewSessionPoolConf(
		username,
		password,
		addresses,
		space,
		nebula.WithTimeOut(10*time.Second),
		nebula.WithMinSize(1),
		nebula.WithMaxSize(8),
	)
	if err != nil {
		return nil, err
	}
	return nebula.NewSessionPool(*configuration, pocLogger{})
}

func waitForComposeSessionPool(
	t *testing.T,
	username, password string,
	addresses []nebula.HostAddress,
	space string,
) *nebula.SessionPool {
	t.Helper()
	deadline := time.Now().Add(composeIntegrationTimeout)
	var lastErr error
	for {
		pool, err := composeSessionPool(username, password, addresses, space)
		if err == nil {
			return pool
		}
		lastErr = err
		if time.Now().After(deadline) {
			t.Fatalf("NebulaGraph session did not become ready: %v", lastErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func assertComposeGraphPlan(t *testing.T, pool *nebula.SessionPool, request askgraph.PlanRequest) {
	t.Helper()
	client, err := askgraph.NewClient(pool)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		plan, resolveErr := client.Resolve(context.Background(), request)
		if resolveErr == nil && len(plan.Models) == 2 && len(plan.MetricModels) == 2 &&
			len(plan.CompatibleDimensions) == 1 && len(plan.MemberOwnerships) == 1 &&
			len(plan.JoinPaths) == 1 {
			if validateErr := plan.Validate(); validateErr != nil {
				t.Fatal(validateErr)
			}
			if !plan.JoinPaths[0].Allowed || plan.MemberOwnerships[0].Status != askgraph.MemberStatusActive {
				t.Fatalf("unexpected Compose GraphPlan: %#v", plan)
			}
			assertComposeEvidence(t, plan)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("reader GraphPlan did not converge: err=%v", resolveErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func assertComposeWrongPasswordFails(
	t *testing.T,
	username, password string,
	addresses []nebula.HostAddress,
	space string,
) {
	t.Helper()
	pool, err := composeSessionPool(username, password+"x", addresses, space)
	if err == nil {
		pool.Close()
		t.Fatalf("NebulaGraph accepted a wrong password for %s", username)
	}
}

func assertComposeQueryDenied(t *testing.T, pool *nebula.SessionPool, statement, boundary string) {
	t.Helper()
	if _, err := pool.ExecuteAndCheck(statement); err == nil {
		t.Fatalf("NebulaGraph allowed forbidden %s", boundary)
	}
}

func cleanupComposeFixture(pool *nebula.SessionPool, request askgraph.PlanRequest) error {
	vids := []string{strconv.Quote("__graph002_reader_forbidden__")}
	typedRefs := []struct {
		objectType askgraph.ObjectType
		refs       []askgraph.ObjectVersionRef
	}{
		{askgraph.ObjectTypeMetric, request.MetricRefs},
		{askgraph.ObjectTypeSemanticModel, request.ModelRefs},
		{askgraph.ObjectTypeDimension, request.DimensionRefs},
		{askgraph.ObjectTypeMember, request.MemberRefs},
	}
	for _, group := range typedRefs {
		for _, ref := range group.refs {
			vid, err := askgraph.BuildVID(request.Scope.TenantID, group.objectType, ref)
			if err != nil {
				return err
			}
			vids = append(vids, strconv.Quote(vid))
		}
	}
	_, err := pool.ExecuteAndCheck("DELETE VERTEX " + strings.Join(vids, ",") + " WITH EDGE")
	return err
}

func recreateFormalComposeGraph(t *testing.T) {
	t.Helper()
	project := requiredEnv(t, "ASKDATA_NEBULA_COMPOSE_PROJECT")
	if !composeVerificationProjectPattern.MatchString(project) {
		t.Fatal("refusing to recreate a non-verification Compose project")
	}
	arguments := verificationComposeArguments(t, project)
	arguments = append(arguments,
		"up", "--detach", "--wait", "--force-recreate",
		"nebula-metad", "nebula-storaged", "nebula-graphd",
	)
	command := exec.Command("docker", arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("recreate formal NebulaGraph services: %v: %s", err, strings.TrimSpace(string(output)))
	}
}

func assertVerificationComposeTarget(
	t *testing.T,
	project string,
	space string,
	addresses []nebula.HostAddress,
) {
	t.Helper()
	matches := composeVerificationProjectPattern.FindStringSubmatch(project)
	if matches == nil {
		t.Fatalf("refusing to mutate project with unsafe verification name %q", project)
	}
	expectedSpace := fmt.Sprintf("g002_verify_%s_%s", matches[1], matches[2])
	if space != expectedSpace {
		t.Fatalf("verification Space %q is not bound to project %q", space, project)
	}
	if len(addresses) != 1 || addresses[0].Host != "127.0.0.1" {
		t.Fatalf("verification endpoint must be exactly one IPv4 loopback address: %#v", addresses)
	}

	arguments := verificationComposeArguments(t, project)
	portOutput := runComposeInspection(t, arguments, "port", "nebula-loopback-proxy", "9669")
	published := mustAddresses(t, strings.TrimSpace(portOutput))
	if len(published) != 1 || published[0] != addresses[0] {
		t.Fatalf("verification endpoint %#v does not belong to project proxy %#v", addresses, published)
	}

	containerID := strings.TrimSpace(runComposeInspection(
		t, arguments, "ps", "--quiet", "nebula-loopback-proxy",
	))
	if containerID == "" || strings.ContainsAny(containerID, "\r\n") {
		t.Fatal("isolated NebulaGraph loopback proxy container is missing or ambiguous")
	}
	inspect := exec.Command(
		"docker", "inspect", "--format",
		`{{ index .Config.Labels "com.docker.compose.project" }}|{{ index .Config.Labels "com.docker.compose.service" }}`,
		containerID,
	)
	output, err := inspect.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect isolated NebulaGraph proxy: %v: %s", err, strings.TrimSpace(string(output)))
	}
	if label := strings.TrimSpace(string(output)); label != project+"|nebula-loopback-proxy" {
		t.Fatalf("loopback proxy has unexpected Compose ownership %q", label)
	}
}

func verificationComposeArguments(t *testing.T, project string) []string {
	t.Helper()
	repository := repositoryRoot(t)
	composeFile := filepath.Join(repository, "compose.yaml")
	overrideFile := filepath.Join(repository, "deployments", "nebula", "verification.override.yaml")
	if info, err := os.Stat(overrideFile); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("verification Compose override is unavailable: %v", err)
	}
	return []string{
		"compose",
		"--project-name", project,
		"--file", composeFile,
		"--file", overrideFile,
		"--env-file", filepath.Join(repository, ".env.example"),
		"--profile", "verification",
		"--profile", "graph-access",
	}
}

func runComposeInspection(t *testing.T, base []string, arguments ...string) string {
	t.Helper()
	commandArguments := append(append([]string(nil), base...), arguments...)
	command := exec.Command("docker", commandArguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("inspect verification Compose target: %v: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output)
}

func assertComposeRealScopeIsolation(
	t *testing.T,
	readerPool, writerPool *nebula.SessionPool,
	request askgraph.PlanRequest,
) {
	t.Helper()
	client, err := askgraph.NewClient(readerPool)
	if err != nil {
		t.Fatal(err)
	}

	metricOrders := request.MetricRefs[0]
	metricRevenue := request.MetricRefs[1]
	modelLines := request.ModelRefs[0]
	modelOrders := request.ModelRefs[1]
	dimensionRegion := request.DimensionRefs[0]
	memberEast := request.MemberRefs[0]
	mustVID := func(objectType askgraph.ObjectType, ref askgraph.ObjectVersionRef) string {
		t.Helper()
		vid, buildErr := askgraph.BuildVID(request.Scope.TenantID, objectType, ref)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		return vid
	}
	metricOrdersVID := mustVID(askgraph.ObjectTypeMetric, metricOrders)
	modelLinesVID := mustVID(askgraph.ObjectTypeSemanticModel, modelLines)
	modelOrdersVID := mustVID(askgraph.ObjectTypeSemanticModel, modelOrders)
	dimensionRegionVID := mustVID(askgraph.ObjectTypeDimension, dimensionRegion)
	memberEastVID := mustVID(askgraph.ObjectTypeMember, memberEast)
	joinProperties := map[string]interface{}{
		"relationship_version_id": "relationship-orders-lines@v1",
		"join_type":               string(registry.JoinInner),
		"cardinality":             string(registry.CardinalityOneToMany),
		"fanout_policy":           string(registry.FanoutCertifiedPre),
		"certified":               true,
	}

	hasMetricBinding := func(plan askgraph.GraphPlan, metric, model askdata.ID) bool {
		for _, binding := range plan.MetricModels {
			if binding.MetricVersionID == metric && binding.ModelVersionID == model {
				return true
			}
		}
		return false
	}
	hasBothMetricBindings := func(plan askgraph.GraphPlan) bool {
		return hasMetricBinding(plan, metricOrders.VersionID, modelOrders.VersionID) &&
			hasMetricBinding(plan, metricRevenue.VersionID, modelLines.VersionID)
	}
	metricOrModelExcluded := func(plan askgraph.GraphPlan) bool {
		return !hasMetricBinding(plan, metricOrders.VersionID, modelOrders.VersionID) &&
			hasMetricBinding(plan, metricRevenue.VersionID, modelLines.VersionID)
	}
	dimensionExcluded := func(plan askgraph.GraphPlan) bool {
		for _, compatible := range plan.CompatibleDimensions {
			if compatible.ModelVersionID == modelOrders.VersionID &&
				compatible.DimensionVersionID == dimensionRegion.VersionID {
				return false
			}
		}
		return hasBothMetricBindings(plan)
	}
	memberExcluded := func(plan askgraph.GraphPlan) bool {
		for _, ownership := range plan.MemberOwnerships {
			if ownership.MemberVersionID == memberEast.VersionID &&
				ownership.DimensionVersionID == dimensionRegion.VersionID {
				return false
			}
		}
		return hasBothMetricBindings(plan)
	}
	joinExcluded := func(plan askgraph.GraphPlan) bool {
		return len(plan.JoinPaths) == 0 && hasBothMetricBindings(plan)
	}

	cases := []struct {
		name     string
		write    func(askgraph.PlanRequest) error
		excluded func(askgraph.GraphPlan) bool
	}{
		{
			name: "metric-tag",
			write: func(scoped askgraph.PlanRequest) error {
				return insertPOCVertex(writerPool, scoped, "metric", metricOrdersVID, metricOrders, "")
			},
			excluded: metricOrModelExcluded,
		},
		{
			name: "semantic-model-tag",
			write: func(scoped askgraph.PlanRequest) error {
				return insertPOCVertex(writerPool, scoped, "semantic_model", modelOrdersVID, modelOrders, "")
			},
			excluded: metricOrModelExcluded,
		},
		{
			name: "dimension-tag",
			write: func(scoped askgraph.PlanRequest) error {
				return insertPOCVertex(writerPool, scoped, "dimension", dimensionRegionVID, dimensionRegion, "")
			},
			excluded: dimensionExcluded,
		},
		{
			name: "member-tag",
			write: func(scoped askgraph.PlanRequest) error {
				return insertPOCVertex(
					writerPool, scoped, "member", memberEastVID, memberEast, string(askgraph.MemberStatusActive),
				)
			},
			excluded: memberExcluded,
		},
		{
			name: "modeled-by-edge",
			write: func(scoped askgraph.PlanRequest) error {
				return insertPOCEdge(writerPool, scoped, "MODELED_BY", metricOrdersVID, modelOrdersVID, nil)
			},
			excluded: metricOrModelExcluded,
		},
		{
			name: "has-dimension-edge",
			write: func(scoped askgraph.PlanRequest) error {
				return insertPOCEdge(
					writerPool, scoped, "HAS_DIMENSION", modelOrdersVID, dimensionRegionVID, nil,
				)
			},
			excluded: dimensionExcluded,
		},
		{
			name: "has-member-edge",
			write: func(scoped askgraph.PlanRequest) error {
				return insertPOCEdge(writerPool, scoped, "HAS_MEMBER", dimensionRegionVID, memberEastVID, nil)
			},
			excluded: memberExcluded,
		},
		{
			name: "joins-to-edge",
			write: func(scoped askgraph.PlanRequest) error {
				return insertPOCEdge(
					writerPool, scoped, "JOINS_TO", modelOrdersVID, modelLinesVID, joinProperties,
				)
			},
			excluded: joinExcluded,
		},
	}
	boundaries := []struct {
		name   string
		mutate func(*askgraph.PlanRequest)
	}{
		{
			name: "tenant",
			mutate: func(scoped *askgraph.PlanRequest) {
				scoped.Scope.TenantID = "tenant-graph002-decoy"
			},
		},
		{
			name: "release",
			mutate: func(scoped *askgraph.PlanRequest) {
				scoped.Scope.Release.ContentHash = askdata.HashBytes([]byte("graph002-wrong-release"))
			},
		},
	}

	for _, testCase := range cases {
		testCase := testCase
		for _, boundary := range boundaries {
			boundary := boundary
			t.Run(testCase.name+"-"+boundary.name, func(t *testing.T) {
				mutated := request
				boundary.mutate(&mutated)
				if writeErr := testCase.write(mutated); writeErr != nil {
					t.Fatalf("write %s %s decoy: %v", boundary.name, testCase.name, writeErr)
				}

				exclusionErr := waitForComposePlanCondition(client, request, testCase.excluded)
				restoreErr := testCase.write(request)
				if restoreErr == nil {
					restoreErr = waitForComposePlanCondition(client, request, composeGraphPlanComplete)
				}
				if exclusionErr != nil {
					if restoreErr != nil {
						t.Fatalf("%v; restore also failed: %v", exclusionErr, restoreErr)
					}
					t.Fatal(exclusionErr)
				}
				if restoreErr != nil {
					t.Fatalf("restore %s after %s decoy: %v", testCase.name, boundary.name, restoreErr)
				}
			})
		}
	}
}

func waitForComposePlanCondition(
	client *askgraph.Client,
	request askgraph.PlanRequest,
	condition func(askgraph.GraphPlan) bool,
) error {
	deadline := time.Now().Add(30 * time.Second)
	var lastPlan askgraph.GraphPlan
	var lastErr error
	for {
		plan, err := client.Resolve(context.Background(), request)
		lastPlan = plan
		lastErr = err
		if err == nil {
			if validateErr := plan.Validate(); validateErr != nil {
				lastErr = validateErr
			} else if condition(plan) {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"GraphPlan condition did not converge: err=%v models=%d metricModels=%d dimensions=%d members=%d paths=%d",
				lastErr,
				len(lastPlan.Models),
				len(lastPlan.MetricModels),
				len(lastPlan.CompatibleDimensions),
				len(lastPlan.MemberOwnerships),
				len(lastPlan.JoinPaths),
			)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func composeGraphPlanComplete(plan askgraph.GraphPlan) bool {
	return len(plan.Models) == 2 && len(plan.MetricModels) == 2 &&
		len(plan.CompatibleDimensions) == 1 && len(plan.MemberOwnerships) == 1 &&
		len(plan.JoinPaths) == 1
}

func assertComposeEvidence(t *testing.T, plan askgraph.GraphPlan) {
	t.Helper()
	evidence, err := plan.EvidenceRef()
	if err != nil || evidence.Kind != askdata.EvidenceKindGraphPath || evidence.ContentHash != plan.PlanHash {
		t.Fatalf("unexpected Compose GraphPlan evidence: %#v err=%v", evidence, err)
	}
}
