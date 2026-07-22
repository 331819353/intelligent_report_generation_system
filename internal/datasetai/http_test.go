package datasetai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

const datasetAIHTTPSessionID = "dataset-ai-http-session"

type datasetAIHTTPAuthStore struct {
	user    auth.LoginUser
	session auth.Session
}

func (s *datasetAIHTTPAuthStore) FindTenantID(context.Context, string) (string, error) {
	return s.user.TenantID, nil
}
func (s *datasetAIHTTPAuthStore) FindUserByEmail(context.Context, string, string) (auth.LoginUser, error) {
	return s.user, nil
}
func (s *datasetAIHTTPAuthStore) FindUserByID(context.Context, string, string) (auth.LoginUser, error) {
	return s.user, nil
}
func (s *datasetAIHTTPAuthStore) CreateSession(_ context.Context, session auth.Session, _, _ string) error {
	s.session = session
	return nil
}
func (s *datasetAIHTTPAuthStore) FindSession(context.Context, string, string) (auth.Session, error) {
	return s.session, nil
}
func (*datasetAIHTTPAuthStore) RotateSession(context.Context, string, string, []byte, []byte, time.Time) error {
	return nil
}
func (*datasetAIHTTPAuthStore) RevokeSession(context.Context, string, string, []byte, string) error {
	return nil
}
func (*datasetAIHTTPAuthStore) RecordLoginFailure(context.Context, string, string, string, string, string, string) {
}

type datasetAIHTTPPermissionStore struct{}

func (*datasetAIHTTPPermissionStore) Allowed(context.Context, access.Check) (bool, error) {
	return true, nil
}

func newDatasetAIHTTPHarness(t *testing.T, planner Planner) (http.Handler, string) {
	t.Helper()
	const tenantID = "tenant-http"
	const actorID = "actor-http"
	tokens := auth.NewTokenManager("dataset-ai-http-test", "01234567890123456789012345678901", time.Hour)
	token, _, err := tokens.Issue(actorID, tenantID, datasetAIHTTPSessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	authStore := &datasetAIHTTPAuthStore{
		user: auth.LoginUser{ID: actorID, TenantID: tenantID, Status: auth.UserStatusActive, TokenVersion: 1},
		session: auth.Session{
			ID: datasetAIHTTPSessionID, TenantID: tenantID, UserID: actorID,
			TokenVersion: 1, UserStatus: auth.UserStatusActive, ExpiresAt: time.Now().UTC().Add(time.Hour),
		},
	}
	return NewHandler(
		auth.NewService(authStore, auth.NewPasswordManager(4), tokens, time.Hour),
		access.NewService(&datasetAIHTTPPermissionStore{}),
		planner,
	), token
}

func TestWritePlanErrorReturnsClarificationQuestion(t *testing.T) {
	recorder := httptest.NewRecorder()
	writePlanError(recorder, &ClarificationRequiredError{Question: "请选择要删除的分组。"})

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "DATASET_AI_CLARIFICATION_REQUIRED" || body["message"] != "请选择要删除的分组。" {
		t.Fatalf("body = %#v", body)
	}
}

func TestWritePlanErrorReturnsCurrentBaselineConflict(t *testing.T) {
	recorder := httptest.NewRecorder()
	writePlanError(recorder, errors.Join(ErrInvalidRequest, ErrCurrentRequired))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "DATASET_AI_CURRENT_REQUIRED" {
		t.Fatalf("body = %#v", body)
	}
}

func TestWritePlanErrorReturnsStableRepairMetadataWithoutValidationDetail(t *testing.T) {
	recorder := httptest.NewRecorder()
	writePlanError(recorder, &InvalidOutputError{
		ReasonCode:      InvalidOutputReasonFieldCaseMismatch,
		Stage:           InvalidOutputStagePlanValidation,
		RepairAttempted: true,
		RequestID:       "request-case-invalid",
		Detail:          "model referenced secret_table.order_id but catalog contains ORDER_ID",
	})

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
	var body planInvalidOutputResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "DATASET_AI_INVALID_OUTPUT" || body.ReasonCode != InvalidOutputReasonFieldCaseMismatch || body.Stage != InvalidOutputStagePlanValidation || !body.RepairAttempted || body.RequestID != "request-case-invalid" {
		t.Fatalf("body = %#v", body)
	}
	if strings.Contains(recorder.Body.String(), "secret_table") || strings.Contains(recorder.Body.String(), "ORDER_ID") {
		t.Fatalf("HTTP response leaked local validation detail: %s", recorder.Body.String())
	}
}

func TestWritePlanErrorNormalizesUnknownInvalidOutputMetadata(t *testing.T) {
	recorder := httptest.NewRecorder()
	writePlanError(recorder, &InvalidOutputError{ReasonCode: "MODEL_TEXT", Stage: "PRIVATE_STAGE", Detail: "raw model output"})

	var body planInvalidOutputResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ReasonCode != InvalidOutputReasonUnknown || body.Stage != InvalidOutputStagePlanValidation || body.RepairAttempted || body.RequestID != "" {
		t.Fatalf("normalized body = %#v", body)
	}
	if strings.Contains(recorder.Body.String(), "MODEL_TEXT") || strings.Contains(recorder.Body.String(), "PRIVATE_STAGE") || strings.Contains(recorder.Body.String(), "raw model output") {
		t.Fatalf("HTTP response leaked untrusted metadata: %s", recorder.Body.String())
	}
}

func TestWritePlanErrorExposesStableTransformReasonWithoutDetail(t *testing.T) {
	recorder := httptest.NewRecorder()
	writePlanError(recorder, &InvalidOutputError{
		ReasonCode: InvalidOutputReasonTransform,
		Stage:      InvalidOutputStagePlanValidation,
		Detail:     "transform_1 references a private field",
	})

	var body planInvalidOutputResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ReasonCode != InvalidOutputReasonTransform {
		t.Fatalf("body = %#v", body)
	}
	if strings.Contains(recorder.Body.String(), "transform_1") || strings.Contains(recorder.Body.String(), "private field") {
		t.Fatalf("HTTP response leaked local validation detail: %s", recorder.Body.String())
	}
}

func TestDatasetAIHTTPReturnsProposalAfterAutomaticRepair(t *testing.T) {
	invalid := monthlyRegionalOrderCountProposal()
	invalid.Plan.Nodes[0].SelectedColumns[0] = "COUNT(*)"
	invalid.Plan.Groups[0].Metrics[0].Column = "COUNT(*)"
	invalid.Plan.End.Outputs[2].Column = "COUNT(*)"
	invoker := &fakeInvoker{configured: true}
	invoker.results = append(invoker.results,
		plannerResult(t, invalid, "request-http-invalid"),
		plannerResult(t, monthlyRegionalOrderCountProposal(), "request-http-repaired"),
	)
	handler, token := newDatasetAIHTTPHarness(t, NewService(monthlyRegionalOrderCountAssetCatalog(), invoker))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/datasets/ai/proposals", strings.NewReader(`{"instruction":"统计月度各区域订单量"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
	var result PlanResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.RequestID != "request-http-repaired" || result.Proposal.Plan.Groups[0].Metrics[0].Column != "ORDER_ID" || len(invoker.inputs) != 2 {
		t.Fatalf("repaired HTTP result/calls = %#v/%d", result, len(invoker.inputs))
	}
}

func TestDatasetAIHTTPReturnsFinalAutomaticRepairFailureMetadata(t *testing.T) {
	first := monthlyRegionalOrderCountProposal()
	first.Plan.Nodes[0].SelectedColumns[0] = "COUNT(*)"
	first.Plan.Groups[0].Metrics[0].Column = "COUNT(*)"
	first.Plan.End.Outputs[2].Column = "COUNT(*)"
	second := monthlyRegionalOrderCountProposal()
	second.Plan.Nodes[0].SelectedColumns[0] = "order_id"
	second.Plan.Groups[0].Metrics[0].Column = "order_id"
	second.Plan.End.Outputs[2].Column = "order_id"
	invoker := &fakeInvoker{configured: true}
	invoker.results = append(invoker.results,
		plannerResult(t, first, "request-http-count-invalid"),
		plannerResult(t, second, "request-http-case-invalid"),
	)
	handler, token := newDatasetAIHTTPHarness(t, NewService(monthlyRegionalOrderCountAssetCatalog(), invoker))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/datasets/ai/proposals", strings.NewReader(`{"instruction":"统计月度各区域订单量"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status/body = %d/%s", recorder.Code, recorder.Body.String())
	}
	var body planInvalidOutputResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ReasonCode != InvalidOutputReasonFieldCaseMismatch || body.Stage != InvalidOutputStagePlanValidation || !body.RepairAttempted || body.RequestID != "request-http-case-invalid" || len(invoker.inputs) != 2 {
		t.Fatalf("repair failure HTTP body/calls = %#v/%d", body, len(invoker.inputs))
	}
	if strings.Contains(recorder.Body.String(), "ORDER_ID") || strings.Contains(recorder.Body.String(), "order_id") {
		t.Fatalf("repair failure HTTP body leaked catalog detail: %s", recorder.Body.String())
	}
}
