package graph_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	nebula "github.com/vesoft-inc/nebula-go/v3"

	"intelligent-report-generation-system/internal/askdata"
	askgraph "intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/registry"
)

const (
	lockedNebulaServerVersion = "3.8.0"
	lockedNebulaClientModule  = "github.com/vesoft-inc/nebula-go/v3"
	lockedNebulaClientVersion = "v3.8.0"
	pocSpaceName              = "askdata_graph_poc"
	pocSocketTimeout          = 10 * time.Second
	pocStartupTimeout         = 45 * time.Second
)

type pocLogger struct{}

func (pocLogger) Info(string)  {}
func (pocLogger) Warn(string)  {}
func (pocLogger) Error(string) {}
func (pocLogger) Fatal(string) {}

func TestNebulaVersionLock(t *testing.T) {
	goModule, err := os.ReadFile(filepath.Join(repositoryRoot(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	want := lockedNebulaClientModule + " " + lockedNebulaClientVersion
	if !strings.Contains(string(goModule), want) {
		t.Fatalf("go.mod does not pin %s", want)
	}
}

func TestNebulaGraphCompatibilityPOC(t *testing.T) {
	if os.Getenv("ASKDATA_NEBULA_POC_INTEGRATION") != "1" {
		t.Skip("set ASKDATA_NEBULA_POC_INTEGRATION=1 or run scripts/verify-nebula-poc.sh")
	}
	plainAddresses := mustAddresses(t, requiredEnv(t, "ASKDATA_NEBULA_POC_ADDRESSES"))
	if len(plainAddresses) != 2 {
		t.Fatal("POC requires exactly two plain graphd addresses")
	}
	username := envOrDefault("ASKDATA_NEBULA_POC_USERNAME", "root")
	password := envOrDefault("ASKDATA_NEBULA_POC_PASSWORD", "nebula")

	adminPool := mustConnectionPool(t, plainAddresses, nil)
	defer adminPool.Close()
	for _, address := range plainAddresses {
		if err := adminPool.Ping(address, pocSocketTimeout); err != nil {
			t.Fatalf("ping graphd %s:%d: %v", address.Host, address.Port, err)
		}
	}
	adminSession, err := adminPool.GetSession(username, password)
	if err != nil {
		t.Fatal(err)
	}
	defer adminSession.Release()

	preparePOCSpace(t, adminSession)
	assertServerVersions(t, adminSession)

	sessionPool := mustSessionPool(t, username, password, plainAddresses, pocSpaceName, nil, 2, 8)
	defer sessionPool.Close()
	assertSpaceBinding(t, sessionPool)
	assertGraphPlanAdapter(t, sessionPool)
	assertParameterEscaping(t, sessionPool)
	assertConcurrentSessionPool(t, sessionPool)
	assertMissingSpaceFails(t, username, password, plainAddresses)
	assertSocketTimeout(t, username, password)
	assertTLS(t, username, password)
	assertFailureRecovery(t, username, password, plainAddresses)
}

func assertGraphPlanAdapter(t *testing.T, pool *nebula.SessionPool) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TAG IF NOT EXISTS semantic_model(tenant_id string, domain_id string, release_hash string, object_id string, version_id string, version_no int)`,
		`CREATE TAG IF NOT EXISTS metric(tenant_id string, domain_id string, release_hash string, object_id string, version_id string, version_no int)`,
		`CREATE TAG IF NOT EXISTS dimension(tenant_id string, domain_id string, release_hash string, object_id string, version_id string, version_no int)`,
		`CREATE TAG IF NOT EXISTS member(tenant_id string, domain_id string, release_hash string, object_id string, version_id string, version_no int, member_status string)`,
		`CREATE EDGE IF NOT EXISTS MODELED_BY(tenant_id string, domain_id string, release_hash string)`,
		`CREATE EDGE IF NOT EXISTS HAS_DIMENSION(tenant_id string, domain_id string, release_hash string)`,
		`CREATE EDGE IF NOT EXISTS HAS_MEMBER(tenant_id string, domain_id string, release_hash string)`,
		`CREATE EDGE IF NOT EXISTS JOINS_TO(tenant_id string, domain_id string, release_hash string, relationship_version_id string, join_type string, cardinality string, fanout_policy string, certified bool)`,
	} {
		if _, err := pool.ExecuteAndCheck(statement); err != nil {
			t.Fatal(err)
		}
	}

	request := pocGraphPlanRequest(t)
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := seedPOCGraph(pool, request)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("POC graph schema did not become writable: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	client, err := askgraph.NewClient(pool)
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(30 * time.Second)
	var plan askgraph.GraphPlan
	for {
		plan, err = client.Resolve(context.Background(), request)
		if err == nil && len(plan.Models) == 2 && len(plan.MetricModels) == 2 &&
			len(plan.CompatibleDimensions) == 1 && len(plan.MemberOwnerships) == 1 &&
			len(plan.JoinPaths) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("GraphPlan adapter did not converge: plan=%#v err=%v", plan, err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if !plan.JoinPaths[0].Allowed ||
		!containsRisk(plan.JoinPaths[0].RiskCodes, askgraph.JoinRiskPreaggregation) ||
		!containsRisk(plan.JoinPaths[0].RiskCodes, askgraph.JoinRiskOneToMany) {
		t.Fatalf("unexpected POC join risk: %#v", plan.JoinPaths[0])
	}
	if plan.MemberOwnerships[0].Status != askgraph.MemberStatusActive ||
		plan.MemberOwnerships[0].DimensionVersionID != "dimension-region@v1" {
		t.Fatalf("unexpected POC member ownership: %#v", plan.MemberOwnerships)
	}
	evidence, err := plan.EvidenceRef()
	if err != nil || evidence.Kind != askdata.EvidenceKindGraphPath || evidence.ContentHash != plan.PlanHash {
		t.Fatalf("unexpected POC GraphPlan evidence: %#v err=%v", evidence, err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "ngql") || strings.Contains(strings.ToLower(string(raw)), "match ") {
		t.Fatalf("GraphPlan exposed generated nGQL: %s", raw)
	}
}

func pocGraphPlanRequest(t *testing.T) askgraph.PlanRequest {
	t.Helper()
	release := askdata.ReleaseRef{
		ReleaseID: "release-poc@v1", ContentHash: askdata.HashBytes([]byte("nebula-graph-plan-poc")),
	}
	scope, err := askdata.NewPolicyScope(
		"tenant-poc", "actor-poc", []askdata.ID{"sales"}, []askdata.ID{"analyst"}, release,
	)
	if err != nil {
		t.Fatal(err)
	}
	return askgraph.PlanRequest{
		Scope: scope, DomainID: "sales",
		MetricRefs: []askgraph.ObjectVersionRef{
			{ObjectID: "metric-orders", VersionID: "metric-orders@v1", Version: 1},
			{ObjectID: "metric-revenue", VersionID: "metric-revenue@v1", Version: 1},
		},
		ModelRefs: []askgraph.ObjectVersionRef{
			{ObjectID: "model-lines", VersionID: "model-lines@v1", Version: 1},
			{ObjectID: "model-orders", VersionID: "model-orders@v1", Version: 1},
		},
		DimensionRefs: []askgraph.ObjectVersionRef{
			{ObjectID: "dimension-region", VersionID: "dimension-region@v1", Version: 1},
		},
		MemberRefs: []askgraph.ObjectVersionRef{
			{ObjectID: "member-east", VersionID: "member-east@v1", Version: 1},
		},
		MaxJoinHops: 2, MaxPaths: 4,
	}
}

func seedPOCGraph(pool *nebula.SessionPool, request askgraph.PlanRequest) error {
	metricOrders := request.MetricRefs[0]
	metricRevenue := request.MetricRefs[1]
	modelLines := request.ModelRefs[0]
	modelOrders := request.ModelRefs[1]
	dimensionRegion := request.DimensionRefs[0]
	memberEast := request.MemberRefs[0]
	typedRefs := []struct {
		objectType askgraph.ObjectType
		tag        string
		ref        askgraph.ObjectVersionRef
		status     string
	}{
		{askgraph.ObjectTypeMetric, "metric", metricOrders, ""},
		{askgraph.ObjectTypeMetric, "metric", metricRevenue, ""},
		{askgraph.ObjectTypeSemanticModel, "semantic_model", modelLines, ""},
		{askgraph.ObjectTypeSemanticModel, "semantic_model", modelOrders, ""},
		{askgraph.ObjectTypeDimension, "dimension", dimensionRegion, ""},
		{askgraph.ObjectTypeMember, "member", memberEast, string(askgraph.MemberStatusActive)},
	}
	vids := make(map[askdata.ID]string, len(typedRefs))
	for _, item := range typedRefs {
		vid, err := askgraph.BuildVID(request.Scope.TenantID, item.objectType, item.ref)
		if err != nil {
			return err
		}
		vids[item.ref.VersionID] = vid
		if err := insertPOCVertex(pool, request, item.tag, vid, item.ref, item.status); err != nil {
			return err
		}
	}
	edges := []struct {
		name     string
		from, to string
		extra    map[string]interface{}
	}{
		{"MODELED_BY", vids[metricOrders.VersionID], vids[modelOrders.VersionID], nil},
		{"MODELED_BY", vids[metricRevenue.VersionID], vids[modelLines.VersionID], nil},
		{"HAS_DIMENSION", vids[modelOrders.VersionID], vids[dimensionRegion.VersionID], nil},
		{"HAS_MEMBER", vids[dimensionRegion.VersionID], vids[memberEast.VersionID], nil},
		{"JOINS_TO", vids[modelOrders.VersionID], vids[modelLines.VersionID], map[string]interface{}{
			"relationship_version_id": "relationship-orders-lines@v1",
			"join_type":               string(registry.JoinInner), "cardinality": string(registry.CardinalityOneToMany),
			"fanout_policy": string(registry.FanoutCertifiedPre), "certified": true,
		}},
	}
	for _, edge := range edges {
		if err := insertPOCEdge(pool, request, edge.name, edge.from, edge.to, edge.extra); err != nil {
			return err
		}
	}
	return nil
}

func insertPOCVertex(
	pool *nebula.SessionPool,
	request askgraph.PlanRequest,
	tag, vid string,
	ref askgraph.ObjectVersionRef,
	memberStatus string,
) error {
	columns := []string{"tenant_id", "domain_id", "release_hash", "object_id", "version_id", "version_no"}
	values := []string{"$tenant_id", "$domain_id", "$release_hash", "$object_id", "$version_id", "$version_no"}
	parameters := pocScopeParameters(request)
	parameters["object_id"] = string(ref.ObjectID)
	parameters["version_id"] = string(ref.VersionID)
	parameters["version_no"] = ref.Version
	if memberStatus != "" {
		columns = append(columns, "member_status")
		values = append(values, "$member_status")
		parameters["member_status"] = memberStatus
	}
	statement := fmt.Sprintf(
		"INSERT VERTEX %s(%s) VALUES %s:(%s)",
		tag, strings.Join(columns, ","), strconv.Quote(vid), strings.Join(values, ","),
	)
	return executePOCMutation(pool, statement, parameters)
}

func insertPOCEdge(
	pool *nebula.SessionPool,
	request askgraph.PlanRequest,
	edgeName, fromVID, toVID string,
	extra map[string]interface{},
) error {
	columns := []string{"tenant_id", "domain_id", "release_hash"}
	values := []string{"$tenant_id", "$domain_id", "$release_hash"}
	parameters := pocScopeParameters(request)
	for _, key := range []string{"relationship_version_id", "join_type", "cardinality", "fanout_policy", "certified"} {
		if value, exists := extra[key]; exists {
			columns = append(columns, key)
			values = append(values, "$"+key)
			parameters[key] = value
		}
	}
	statement := fmt.Sprintf(
		"INSERT EDGE %s(%s) VALUES %s:(%s)",
		edgeName, strings.Join(columns, ","), pocEdgeIdentity(edgeName, fromVID, toVID, extra), strings.Join(values, ","),
	)
	return executePOCMutation(pool, statement, parameters)
}

func pocEdgeIdentity(edgeName, fromVID, toVID string, extra map[string]interface{}) string {
	identity := strconv.Quote(fromVID) + "->" + strconv.Quote(toVID)
	if edgeName != "JOINS_TO" {
		return identity
	}
	versionID, ok := extra["relationship_version_id"].(string)
	if !ok {
		return identity
	}
	rank, err := askgraph.BuildRelationshipEdgeRank(askdata.ID(versionID))
	if err != nil {
		return identity
	}
	return identity + "@" + strconv.FormatInt(rank, 10)
}

func executePOCMutation(pool *nebula.SessionPool, statement string, parameters map[string]interface{}) error {
	result, err := pool.ExecuteWithParameter(statement, parameters)
	if err != nil {
		return err
	}
	if !result.IsSucceed() {
		return errors.New(result.GetErrorMsg())
	}
	return nil
}

func pocScopeParameters(request askgraph.PlanRequest) map[string]interface{} {
	return map[string]interface{}{
		"tenant_id": string(request.Scope.TenantID), "domain_id": string(request.DomainID),
		"release_hash": string(request.Scope.Release.ContentHash),
	}
}

func containsRisk(values []askgraph.JoinRiskCode, target askgraph.JoinRiskCode) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func assertServerVersions(t *testing.T, session *nebula.Session) {
	t.Helper()
	for _, service := range []string{"META", "STORAGE", "GRAPH"} {
		deadline := time.Now().Add(30 * time.Second)
		var lastErr error
		for {
			result, err := session.ExecuteAndCheck("SHOW HOSTS " + service)
			if err == nil {
				versions, valuesErr := result.GetValuesByColName("Version")
				ready := valuesErr == nil && len(versions) > 0
				lastErr = valuesErr
				for _, version := range versions {
					value, valueErr := version.AsString()
					if valueErr != nil {
						ready = false
						lastErr = valueErr
						break
					}
					if value != lockedNebulaServerVersion {
						t.Fatalf("%s service version = %q, want %q", service, value, lockedNebulaServerVersion)
					}
				}
				if ready {
					break
				}
			} else {
				lastErr = err
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s service versions did not become available: %v", service, lastErr)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
}

func preparePOCSpace(t *testing.T, session *nebula.Session) {
	t.Helper()
	result, err := session.Execute(`ADD HOSTS "nebula-storaged":9779`)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsSucceed() && !strings.Contains(strings.ToLower(result.GetErrorMsg()), "exist") {
		t.Fatalf("register POC storage: %s", result.GetErrorMsg())
	}
	query := fmt.Sprintf(
		"CREATE SPACE IF NOT EXISTS %s(partition_num=1, replica_factor=1, vid_type=FIXED_STRING(256))",
		pocSpaceName,
	)
	if _, err := session.ExecuteAndCheck(query); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		result, err := session.ExecuteAndCheck("USE " + pocSpaceName + "; RETURN 1 AS ready")
		if err == nil && resultInt(t, result, "ready") == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("space %s did not become visible: %v", pocSpaceName, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func assertSpaceBinding(t *testing.T, pool *nebula.SessionPool) {
	t.Helper()
	result, err := pool.ExecuteAndCheck("RETURN 1 AS bound")
	if err != nil {
		t.Fatal(err)
	}
	if result.GetSpaceName() != pocSpaceName || resultInt(t, result, "bound") != 1 {
		t.Fatalf("session pool was not bound to %q: space=%q", pocSpaceName, result.GetSpaceName())
	}
	spaces, err := pool.ShowSpaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, space := range spaces {
		if space.Name == pocSpaceName {
			return
		}
	}
	t.Fatalf("POC space %q is missing", pocSpaceName)
}

func assertParameterEscaping(t *testing.T, pool *nebula.SessionPool) {
	t.Helper()
	payload := "x' ; DROP SPACE askdata_graph_poc; -- \\\" <system>中文</system>"
	result, err := pool.ExecuteWithParameter(
		"RETURN $payload AS payload, $sequence AS sequence",
		map[string]interface{}{"payload": payload, "sequence": 17},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsSucceed() {
		t.Fatal(result.GetErrorMsg())
	}
	if got := resultString(t, result, "payload"); got != payload {
		t.Fatalf("parameter round trip = %q, want %q", got, payload)
	}
	if got := resultInt(t, result, "sequence"); got != 17 {
		t.Fatalf("integer parameter = %d, want 17", got)
	}
	if _, err := pool.ExecuteAndCheck("RETURN 1 AS space_still_exists"); err != nil {
		t.Fatalf("bound query after injection-shaped parameter: %v", err)
	}
}

func assertConcurrentSessionPool(t *testing.T, pool *nebula.SessionPool) {
	t.Helper()
	const concurrency = 8
	errorsByWorker := make(chan error, concurrency)
	var waitGroup sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		worker := worker
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			result, err := pool.ExecuteWithParameter("RETURN $worker AS worker", map[string]interface{}{"worker": worker})
			if err != nil {
				errorsByWorker <- err
				return
			}
			if !result.IsSucceed() {
				errorsByWorker <- errors.New(result.GetErrorMsg())
				return
			}
			value, err := recordInt(result, "worker")
			if err != nil || value != int64(worker) {
				errorsByWorker <- fmt.Errorf("worker %d result=%d err=%v", worker, value, err)
			}
		}()
	}
	waitGroup.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		t.Error(err)
	}
}

func assertMissingSpaceFails(t *testing.T, username, password string, addresses []nebula.HostAddress) {
	t.Helper()
	configuration, err := nebula.NewSessionPoolConf(
		username,
		password,
		addresses,
		"askdata_graph_poc_missing",
		nebula.WithTimeOut(pocSocketTimeout),
		nebula.WithMinSize(1),
	)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := nebula.NewSessionPool(*configuration, pocLogger{})
	if err == nil {
		pool.Close()
		t.Fatal("Session Pool accepted a missing Space")
	}
}

func assertSocketTimeout(t *testing.T, username, password string) {
	t.Helper()
	address := mustAddresses(t, requiredEnv(t, "ASKDATA_NEBULA_POC_BLACKHOLE_ADDRESS"))
	const timeout = 600 * time.Millisecond
	configuration, err := nebula.NewSessionPoolConf(
		username,
		password,
		address,
		pocSpaceName,
		nebula.WithTimeOut(timeout),
		nebula.WithMinSize(1),
	)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	pool, err := nebula.NewSessionPool(*configuration, nebula.DefaultLogger{})
	elapsed := time.Since(started)
	if err == nil {
		pool.Close()
		t.Fatal("blackhole endpoint did not trigger a socket timeout")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("configured socket timeout took %s", elapsed)
	}
	if elapsed < timeout/2 {
		t.Fatalf("blackhole failed before configured timeout: %s", elapsed)
	}
}

func assertTLS(t *testing.T, username, password string) {
	t.Helper()
	addresses := mustAddresses(t, requiredEnv(t, "ASKDATA_NEBULA_POC_TLS_ADDRESS"))
	tlsConfiguration, err := nebula.GetDefaultSSLConfig(
		requiredEnv(t, "ASKDATA_NEBULA_POC_CA_FILE"),
		requiredEnv(t, "ASKDATA_NEBULA_POC_CLIENT_CERT_FILE"),
		requiredEnv(t, "ASKDATA_NEBULA_POC_CLIENT_KEY_FILE"),
	)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfiguration.MinVersion = tls.VersionTLS12
	tlsConfiguration.ServerName = "localhost"
	pool := mustSessionPool(t, username, password, addresses, pocSpaceName, tlsConfiguration, 1, 2)
	defer pool.Close()
	result, err := pool.ExecuteAndCheck("SHOW HOSTS META")
	if err != nil {
		t.Fatal(err)
	}
	if version := resultString(t, result, "Version"); version != lockedNebulaServerVersion {
		t.Fatalf("TLS graphd version = %q", version)
	}
}

func assertFailureRecovery(
	t *testing.T,
	username, password string,
	addresses []nebula.HostAddress,
) {
	t.Helper()
	if os.Getenv("ASKDATA_NEBULA_POC_FAILURE_RECOVERY") != "1" {
		t.Skip("failure recovery requires the isolated POC Compose project")
	}
	composeFile, project := validatePOCComposeTarget(t)
	pool := mustSessionPool(t, username, password, addresses, pocSpaceName, nil, 1, 1)
	defer pool.Close()
	if _, err := pool.ExecuteAndCheck("RETURN 1 AS before_stop"); err != nil {
		t.Fatal(err)
	}
	composeCommand(t, composeFile, project, "stop", "--timeout", "1", "nebula-graphd-a")
	t.Cleanup(func() {
		composeCommand(t, composeFile, project, "start", "nebula-graphd-a")
		waitForTCP(t, addresses[0], true, 30*time.Second)
	})
	waitForTCP(t, addresses[0], false, 10*time.Second)
	result, err := pool.ExecuteAndCheck("RETURN 2 AS after_stop")
	if err != nil {
		t.Fatalf("Session Pool did not recover through second graphd: %v", err)
	}
	if value := resultInt(t, result, "after_stop"); value != 2 {
		t.Fatalf("recovered query value = %d", value)
	}
}

func validatePOCComposeTarget(t *testing.T) (string, string) {
	t.Helper()
	repositoryRoot := repositoryRoot(t)
	expected := filepath.Join(repositoryRoot, "deployments", "nebula-poc", "compose.yaml")
	composeFile := filepath.Clean(requiredEnv(t, "ASKDATA_NEBULA_POC_COMPOSE_FILE"))
	project := requiredEnv(t, "ASKDATA_NEBULA_POC_COMPOSE_PROJECT")
	if composeFile != expected || project != "askdata-nebula-poc" {
		t.Fatalf("refusing to control non-POC Compose target: file=%q project=%q", composeFile, project)
	}
	return composeFile, project
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve POC source location")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
}

func composeCommand(t *testing.T, composeFile, project string, arguments ...string) {
	t.Helper()
	commandArguments := []string{"compose", "--project-name", project, "--file", composeFile}
	commandArguments = append(commandArguments, arguments...)
	command := exec.Command("docker", commandArguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose %s: %v: %s", strings.Join(arguments, " "), err, strings.TrimSpace(string(output)))
	}
}

func waitForTCP(t *testing.T, address nebula.HostAddress, wantOpen bool, timeout time.Duration) {
	t.Helper()
	endpoint := net.JoinHostPort(address.Host, strconv.Itoa(address.Port))
	deadline := time.Now().Add(timeout)
	for {
		connection, err := net.DialTimeout("tcp", endpoint, 200*time.Millisecond)
		if err == nil {
			_ = connection.Close()
		}
		if (err == nil) == wantOpen {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("TCP endpoint %s open=%t, want %t", endpoint, err == nil, wantOpen)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func mustConnectionPool(
	t *testing.T,
	addresses []nebula.HostAddress,
	tlsConfiguration *tls.Config,
) *nebula.ConnectionPool {
	t.Helper()
	configuration := nebula.GetDefaultConf()
	configuration.TimeOut = pocSocketTimeout
	configuration.MinConnPoolSize = 1
	configuration.MaxConnPoolSize = 8
	var pool *nebula.ConnectionPool
	var err error
	deadline := time.Now().Add(pocStartupTimeout)
	for {
		if tlsConfiguration == nil {
			pool, err = nebula.NewConnectionPool(addresses, configuration, pocLogger{})
		} else {
			pool, err = nebula.NewSslConnectionPool(addresses, configuration, tlsConfiguration, pocLogger{})
		}
		if err == nil {
			return pool
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func mustSessionPool(
	t *testing.T,
	username, password string,
	addresses []nebula.HostAddress,
	space string,
	tlsConfiguration *tls.Config,
	minimum, maximum int,
) *nebula.SessionPool {
	t.Helper()
	options := []nebula.SessionPoolConfOption{
		nebula.WithTimeOut(pocSocketTimeout),
		nebula.WithIdleTime(time.Minute),
		nebula.WithMinSize(minimum),
		nebula.WithMaxSize(maximum),
	}
	if tlsConfiguration != nil {
		options = append(options, nebula.WithSSLConfig(tlsConfiguration))
	}
	configuration, err := nebula.NewSessionPoolConf(username, password, addresses, space, options...)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(pocStartupTimeout)
	for {
		pool, err := nebula.NewSessionPool(*configuration, pocLogger{})
		if err == nil {
			return pool
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func resultString(t *testing.T, result *nebula.ResultSet, column string) string {
	t.Helper()
	value, err := recordString(result, column)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func recordString(result *nebula.ResultSet, column string) (string, error) {
	record, err := result.GetRowValuesByIndex(0)
	if err != nil {
		return "", err
	}
	value, err := record.GetValueByColName(column)
	if err != nil {
		return "", err
	}
	return value.AsString()
}

func resultInt(t *testing.T, result *nebula.ResultSet, column string) int64 {
	t.Helper()
	value, err := recordInt(result, column)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func recordInt(result *nebula.ResultSet, column string) (int64, error) {
	record, err := result.GetRowValuesByIndex(0)
	if err != nil {
		return 0, err
	}
	value, err := record.GetValueByColName(column)
	if err != nil {
		return 0, err
	}
	return value.AsInt()
}

func mustAddresses(t *testing.T, raw string) []nebula.HostAddress {
	t.Helper()
	parts := strings.Split(raw, ",")
	addresses := make([]nebula.HostAddress, 0, len(parts))
	for _, part := range parts {
		host, portText, err := net.SplitHostPort(strings.TrimSpace(part))
		if err != nil || host == "" {
			t.Fatalf("invalid POC graph address %q", part)
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			t.Fatalf("invalid POC graph port %q", portText)
		}
		addresses = append(addresses, nebula.HostAddress{Host: host, Port: port})
	}
	return addresses
}

func requiredEnv(t *testing.T, key string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		t.Fatalf("%s is required for the NebulaGraph POC", key)
	}
	return value
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
