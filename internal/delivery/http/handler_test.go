package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	delivery "github.com/kanmon-hq/kura/internal/delivery/http"
	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/usecase"
)

type mockQuotaRepoForHealth struct {
	pingErr error
}


func (m *mockQuotaRepoForHealth) GetTenantUsage(ctx context.Context, serviceID, tenantID, month string) (*entity.TenantMonthlyUsage, error) {
	return nil, nil
}
func (m *mockQuotaRepoForHealth) IncrementTenantUsage(ctx context.Context, serviceID, tenantID, month string, model string, promptTokens, completionTokens int64, cost float64) error {
	return nil
}
func (m *mockQuotaRepoForHealth) SetServiceLimit(ctx context.Context, serviceID string, costLimit float64, billingType string) error {
	return nil
}
func (m *mockQuotaRepoForHealth) SetTenantLimit(ctx context.Context, serviceID, tenantID string, costLimit float64, billingType string) error {
	return nil
}
func (m *mockQuotaRepoForHealth) GetTenantConfig(ctx context.Context, serviceID, tenantID string) (*entity.TenantConfig, error) {
	return nil, nil
}
func (m *mockQuotaRepoForHealth) SetTenantConfig(ctx context.Context, cfg *entity.TenantConfig) error {
	return nil
}
func (m *mockQuotaRepoForHealth) GetServiceConfigs(ctx context.Context, serviceIDs []string) (map[string]*entity.ServiceConfig, error) {
	return nil, nil
}
func (m *mockQuotaRepoForHealth) GetServiceConfig(ctx context.Context, serviceID string) (*entity.ServiceConfig, error) {
	return nil, nil
}
func (m *mockQuotaRepoForHealth) SetServiceConfig(ctx context.Context, cfg *entity.ServiceConfig) error {
	return nil
}
func (m *mockQuotaRepoForHealth) GetServiceMonthlyUsage(ctx context.Context, serviceID, month string) (*entity.ServiceMonthlyReport, error) {
	return nil, nil
}
func (m *mockQuotaRepoForHealth) GetServiceCost(ctx context.Context, serviceID, month string) (float64, int64, error) {
	return 0, 0, nil
}
func (m *mockQuotaRepoForHealth) GetTenantCost(ctx context.Context, serviceID, tenantID, month string) (float64, int64, error) {
	return 0, 0, nil
}
func (m *mockQuotaRepoForHealth) IncrementCost(ctx context.Context, serviceID, tenantID, month string, promptTokens, completionTokens int64, cost float64) error {
	return nil
}
func (m *mockQuotaRepoForHealth) ResetCost(ctx context.Context, serviceID, tenantID, month string, cost float64, tokens int64) error {
	return nil
}
func (m *mockQuotaRepoForHealth) RecordUsage(ctx context.Context, serviceID, tenantID, month, model string, promptTokens, completionTokens int64, cost float64, pricingVersion string) error {
	return nil
}
func (m *mockQuotaRepoForHealth) AcquireLock(ctx context.Context, lockKey string, ttlSeconds int64) (bool, error) {
	return true, nil
}
func (m *mockQuotaRepoForHealth) ReleaseLock(ctx context.Context, lockKey string) error {
	return nil
}
func (m *mockQuotaRepoForHealth) GetAllTenantsUsageByMonth(ctx context.Context, month string) ([]*entity.TenantMonthlyUsage, error) {
	return nil, nil
}
func (m *mockQuotaRepoForHealth) SaveNotification(ctx context.Context, ntf *entity.Notification) error {
	return nil
}
func (m *mockQuotaRepoForHealth) ListNotifications(ctx context.Context, limit int) ([]*entity.Notification, error) {
	return nil, nil
}
func (m *mockQuotaRepoForHealth) Ping(ctx context.Context) error {
	return m.pingErr
}

func TestLivenessProbe(t *testing.T) {
	h := delivery.NewHandler(nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/livez", nil)
	rec := httptest.NewRecorder()

	h.Liveness(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for liveness, got %d", rec.Code)
	}

	var res map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}
	if res["status"] != "alive" {
		t.Errorf("expected status alive, got %s", res["status"])
	}
}

func TestReadinessProbe_Normal(t *testing.T) {
	mockRepo := &mockQuotaRepoForHealth{pingErr: nil}
	h := delivery.NewHandler(nil, nil, mockRepo, mockRepo)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	h.Readiness(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for readiness, got %d", rec.Code)
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}
	if res["status"] != "ready" {
		t.Errorf("expected status ready, got %v", res["status"])
	}
	if res["database"] != "connected" {
		t.Errorf("expected database connected, got %v", res["database"])
	}
}

func TestReadinessProbe_ShuttingDown(t *testing.T) {
	mockRepo := &mockQuotaRepoForHealth{pingErr: nil}
	h := delivery.NewHandler(nil, nil, mockRepo, mockRepo)

	// シャットダウン状態へ遷移
	h.SetShuttingDown(true)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	h.Readiness(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable when shutting down, got %d", rec.Code)
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}
	if res["status"] != "terminating" {
		t.Errorf("expected status terminating, got %v", res["status"])
	}
}

func TestReadinessProbe_DatabaseError(t *testing.T) {
	mockRepo := &mockQuotaRepoForHealth{pingErr: errors.New("dynamodb connection lost")}
	h := delivery.NewHandler(nil, nil, mockRepo, mockRepo)

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	h.Readiness(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable on database error, got %d", rec.Code)
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}
	if res["status"] != "degraded" {
		t.Errorf("expected status degraded, got %v", res["status"])
	}
}

type mockAuthUseCaseForUsage struct {
	summary *entity.KeyUsageSummary
	err     error
}

func (m *mockAuthUseCaseForUsage) AuthenticateRequest(ctx context.Context, req *http.Request) (*usecase.AuthResult, *entity.StandardErrorResponse) {
	return nil, nil
}
func (m *mockAuthUseCaseForUsage) GetKeyUsageSummary(ctx context.Context, tenantCtx *entity.TenantContext) (*entity.KeyUsageSummary, error) {
	return m.summary, m.err
}

func TestGetKeyUsage(t *testing.T) {
	mockAuth := &mockAuthUseCaseForUsage{
		summary: &entity.KeyUsageSummary{
			ServiceID:           "service-demo",
			TenantID:            "tenant-alpha",
			Month:               "2026-09",
			BillingType:         "capped",
			ServiceCostLimitUSD: 100.0,
			ServiceTotalCostUSD: 20.0,
			ServiceRemainingUSD: 80.0,
		},
	}
	h := delivery.NewHandler(nil, nil, nil, nil, mockAuth)

	// Case 1: Unauthorized (no context)
	reqUnauth := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	recUnauth := httptest.NewRecorder()
	h.GetKeyUsage(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized, got %d", recUnauth.Code)
	}

	// Case 2: Authorized
	reqAuth := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	tenantCtx := &entity.TenantContext{
		ServiceID: "service-demo",
		TenantID:  "tenant-alpha",
	}
	reqAuth = reqAuth.WithContext(context.WithValue(reqAuth.Context(), delivery.TenantContextKey, tenantCtx))
	recAuth := httptest.NewRecorder()

	h.GetKeyUsage(recAuth, reqAuth)
	if recAuth.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", recAuth.Code)
	}

	var res entity.KeyUsageSummary
	if err := json.Unmarshal(recAuth.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode json: %v", err)
	}
	if res.ServiceID != "service-demo" || res.ServiceRemainingUSD != 80.0 {
		t.Errorf("unexpected response content: %+v", res)
	}
}
