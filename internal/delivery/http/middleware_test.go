package http_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	delivery "github.com/kanmon-hq/kura/internal/delivery/http"
	"github.com/kanmon-hq/kura/internal/infrastructure/memory"
	"github.com/kanmon-hq/kura/internal/infrastructure/metrics"
	"github.com/kanmon-hq/kura/internal/usecase"
)

func TestAuthMiddleware_GatewaySharedSecret(t *testing.T) {
	memStore := memory.NewMemoryStore()
	auc := usecase.NewAuthUseCase(memStore, memStore)
	m := metrics.NewMetrics()

	currentSecret := "supersecretkey123456789012345678"
	prevSecret := "oldsecretkey12345678901234567890"

	gwCfg := delivery.GatewayAuthConfig{
		SharedSecret:         currentSecret,
		SharedSecretPrevious: prevSecret,
		HeaderName:           "X-Gateway-Secret",
		InsecureNoAuth:       false,
	}
	middleware := delivery.NewAuthMiddleware(auc, gwCfg, m)

	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	wrapped := middleware.Wrap(dummyHandler)

	// 1. Missing Secret Header -> 401
	{
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("X-Service-ID", "test-svc")
		req.Header.Set("X-Tenant-ID", "test-tenant")
		rec := httptest.NewRecorder()

		wrapped(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 on missing secret header, got %d", rec.Code)
		}
	}

	// 2. Invalid Secret Header -> 401
	{
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("X-Service-ID", "test-svc")
		req.Header.Set("X-Tenant-ID", "test-tenant")
		req.Header.Set("X-Gateway-Secret", "wrong-secret-value-12345678901234")
		rec := httptest.NewRecorder()

		wrapped(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401 on wrong secret, got %d", rec.Code)
		}
	}

	// 3. Valid Current Secret -> 200
	{
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("X-Service-ID", "test-svc")
		req.Header.Set("X-Tenant-ID", "test-tenant")
		req.Header.Set("X-Gateway-Secret", currentSecret)
		rec := httptest.NewRecorder()

		wrapped(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 with current secret, got %d (body: %s)", rec.Code, rec.Body.String())
		}
	}

	// 4. Valid Previous Secret (Rotation) -> 200
	{
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("X-Service-ID", "test-svc")
		req.Header.Set("X-Tenant-ID", "test-tenant")
		req.Header.Set("X-Gateway-Secret", prevSecret)
		rec := httptest.NewRecorder()

		wrapped(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 with previous secret, got %d (body: %s)", rec.Code, rec.Body.String())
		}
	}

	// 5. Insecure Mode -> Passes even without secret
	{
		insecureGwCfg := delivery.GatewayAuthConfig{
			InsecureNoAuth: true,
		}
		insecureMiddleware := delivery.NewAuthMiddleware(auc, insecureGwCfg, m)
		insecureWrapped := insecureMiddleware.Wrap(dummyHandler)

		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		req.Header.Set("X-Service-ID", "test-svc")
		req.Header.Set("X-Tenant-ID", "test-tenant")
		rec := httptest.NewRecorder()

		insecureWrapped(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 when InsecureNoAuth is true, got %d", rec.Code)
		}
	}
}
