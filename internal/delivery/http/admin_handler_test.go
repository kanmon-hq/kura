package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/usecase"
)

// mockAdminUseCase は AdminUseCase のモック
type mockAdminUseCase struct {
	getMonthlyUsageFn   func(ctx context.Context, serviceID, month string) (*entity.ServiceMonthlyReport, error)
	setTenantLimitFn    func(ctx context.Context, req *usecase.SetLimitRequest) error
	listNotificationsFn func(ctx context.Context, limit int) ([]*entity.Notification, error)
}

func (m *mockAdminUseCase) GetMonthlyUsage(ctx context.Context, serviceID, month string) (*entity.ServiceMonthlyReport, error) {
	if m.getMonthlyUsageFn != nil {
		return m.getMonthlyUsageFn(ctx, serviceID, month)
	}
	return nil, nil
}

func (m *mockAdminUseCase) SetTenantLimit(ctx context.Context, req *usecase.SetLimitRequest) error {
	if m.setTenantLimitFn != nil {
		return m.setTenantLimitFn(ctx, req)
	}
	return nil
}

func (m *mockAdminUseCase) ListNotifications(ctx context.Context, limit int) ([]*entity.Notification, error) {
	if m.listNotificationsFn != nil {
		return m.listNotificationsFn(ctx, limit)
	}
	return nil, nil
}

// mockBatchUseCase は BatchUseCase のモック
type mockBatchUseCase struct {
	runMonthlyReportFn  func(ctx context.Context) error
	runQuotaAlertsFn    func(ctx context.Context) error
	runReconciliationFn func(ctx context.Context) error
}

func (m *mockBatchUseCase) RunMonthlyReport(ctx context.Context) error {
	if m.runMonthlyReportFn != nil {
		return m.runMonthlyReportFn(ctx)
	}
	return nil
}

func (m *mockBatchUseCase) RunQuotaAlerts(ctx context.Context) error {
	if m.runQuotaAlertsFn != nil {
		return m.runQuotaAlertsFn(ctx)
	}
	return nil
}

func (m *mockBatchUseCase) RunReconciliation(ctx context.Context) error {
	if m.runReconciliationFn != nil {
		return m.runReconciliationFn(ctx)
	}
	return nil
}

func TestAdminHandler_VerifyKey(t *testing.T) {
	h := NewAdminHandler(nil, nil, "secret123")

	if h.VerifyKey("wrong") {
		t.Errorf("Expected wrong key to be invalid")
	}

	if !h.VerifyKey("secret123") {
		t.Errorf("Expected correct key to be valid")
	}

	if !h.VerifyKey("Bearer secret123") {
		t.Errorf("Expected bearer token to be valid")
	}

	hNoKey := NewAdminHandler(nil, nil, "")
	if hNoKey.VerifyKey("secret123") {
		t.Errorf("Expected to reject all if adminAPIKey is empty")
	}
}

func TestAdminHandler_verifyAdminAuth(t *testing.T) {
	h := NewAdminHandler(nil, nil, "secret123")

	t.Run("valid Authorization header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer secret123")
		if !h.verifyAdminAuth(req) {
			t.Errorf("Expected valid with Authorization header")
		}
	})

	t.Run("missing header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if h.verifyAdminAuth(req) {
			t.Errorf("Expected invalid without headers")
		}
	})

	t.Run("wrong key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer wrong")
		if h.verifyAdminAuth(req) {
			t.Errorf("Expected invalid with wrong key")
		}
	})

	t.Run("no api key set", func(t *testing.T) {
		hEmpty := NewAdminHandler(nil, nil, "")
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer secret123")
		if hEmpty.verifyAdminAuth(req) {
			t.Errorf("Expected invalid when API key is not set")
		}
	})
}

func TestAdminHandler_GetUsage(t *testing.T) {
	mockUC := &mockAdminUseCase{
		getMonthlyUsageFn: func(ctx context.Context, serviceID, month string) (*entity.ServiceMonthlyReport, error) {
			if serviceID == "err-svc" {
				return nil, errors.New("usecase error")
			}
			return &entity.ServiceMonthlyReport{
				ServiceID: serviceID,
				Month:     month,
			}, nil
		},
	}
	h := NewAdminHandler(mockUC, nil, "secret123")

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/usage", nil)
		rec := httptest.NewRecorder()
		h.GetUsage(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected 405 Method Not Allowed, got %d", rec.Code)
		}
	})

	t.Run("unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/usage", nil)
		rec := httptest.NewRecorder()
		h.GetUsage(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Expected 401 Unauthorized, got %d", rec.Code)
		}
	})

	t.Run("success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/usage?service_id=test-svc&month=2023-10", nil)
		req.Header.Set("Authorization", "Bearer secret123")
		rec := httptest.NewRecorder()
		h.GetUsage(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Expected 200 OK, got %d", rec.Code)
		}
		var report entity.ServiceMonthlyReport
		if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
			t.Fatalf("failed to decode json: %v", err)
		}
		if report.ServiceID != "test-svc" || report.Month != "2023-10" {
			t.Errorf("Unexpected response content: %+v", report)
		}
	})

	t.Run("usecase error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/usage?service_id=err-svc", nil)
		req.Header.Set("Authorization", "Bearer secret123")
		rec := httptest.NewRecorder()
		h.GetUsage(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("Expected 400 Bad Request, got %d", rec.Code)
		}
	})
}

func TestAdminHandler_SetLimits(t *testing.T) {
	mockUC := &mockAdminUseCase{
		setTenantLimitFn: func(ctx context.Context, req *usecase.SetLimitRequest) error {
			if req.ServiceID == "err-svc" {
				return errors.New("usecase error")
			}
			return nil
		},
	}
	h := NewAdminHandler(mockUC, nil, "secret123")

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/limits", nil)
		rec := httptest.NewRecorder()
		h.SetLimits(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("Expected 405 Method Not Allowed, got %d", rec.Code)
		}
	})

	t.Run("unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/limits", nil)
		rec := httptest.NewRecorder()
		h.SetLimits(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Expected 401 Unauthorized, got %d", rec.Code)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/limits", bytes.NewBufferString("{invalid"))
		req.Header.Set("Authorization", "Bearer secret123")
		rec := httptest.NewRecorder()
		h.SetLimits(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("Expected 400 Bad Request, got %d", rec.Code)
		}
	})

	t.Run("success", func(t *testing.T) {
		reqBody := `{"service_id": "test-svc", "cost_limit": 100}`
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/limits", bytes.NewBufferString(reqBody))
		req.Header.Set("Authorization", "Bearer secret123")
		rec := httptest.NewRecorder()
		h.SetLimits(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Expected 200 OK, got %d", rec.Code)
		}
		var res map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("failed to decode json: %v", err)
		}
		if res["status"] != "ok" {
			t.Errorf("Expected status ok, got %v", res["status"])
		}
	})

	t.Run("usecase error", func(t *testing.T) {
		reqBody := `{"service_id": "err-svc"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/limits", bytes.NewBufferString(reqBody))
		req.Header.Set("Authorization", "Bearer secret123")
		rec := httptest.NewRecorder()
		h.SetLimits(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("Expected 400 Bad Request, got %d", rec.Code)
		}
	})
}

func TestAdminHandler_ListNotifications(t *testing.T) {
	mockUC := &mockAdminUseCase{
		listNotificationsFn: func(ctx context.Context, limit int) ([]*entity.Notification, error) {
			if limit == 999 {
				return nil, errors.New("usecase error")
			}
			return []*entity.Notification{
				{ID: "n1", Message: "test 1"},
			}, nil
		},
	}
	h := NewAdminHandler(mockUC, nil, "secret123")

	t.Run("unauthorized", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/notifications", nil)
		rec := httptest.NewRecorder()
		h.ListNotifications(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Expected 401 Unauthorized, got %d", rec.Code)
		}
	})

	t.Run("success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/notifications?limit=10", nil)
		req.Header.Set("Authorization", "Bearer secret123")
		rec := httptest.NewRecorder()
		h.ListNotifications(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Expected 200 OK, got %d", rec.Code)
		}

		var res map[string][]*entity.Notification
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("failed to decode json: %v", err)
		}
		if len(res["notifications"]) != 1 || res["notifications"][0].ID != "n1" {
			t.Errorf("Unexpected notifications: %+v", res)
		}
	})

	t.Run("invalid limit format ignored", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/notifications?limit=invalid", nil)
		req.Header.Set("Authorization", "Bearer secret123")
		rec := httptest.NewRecorder()
		h.ListNotifications(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("Expected 200 OK, got %d", rec.Code)
		}
	})

	t.Run("usecase error", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/notifications?limit=999", nil)
		req.Header.Set("Authorization", "Bearer secret123")
		rec := httptest.NewRecorder()
		h.ListNotifications(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("Expected 400 Bad Request, got %d", rec.Code)
		}
	})
}

func TestAdminHandler_Getters(t *testing.T) {
	mockAdmin := &mockAdminUseCase{}
	mockBatch := &mockBatchUseCase{}
	h := NewAdminHandler(mockAdmin, mockBatch, "secret123")

	if h.UseCase() != mockAdmin {
		t.Errorf("UseCase() did not return expected admin usecase")
	}
	if h.BatchUseCase() != mockBatch {
		t.Errorf("BatchUseCase() did not return expected batch usecase")
	}
}
