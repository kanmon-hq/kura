package metrics_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kanmon-hq/kura/internal/infrastructure/metrics"
)

func TestMetrics_Handler(t *testing.T) {
	m := metrics.NewMetrics()

	// メトリクスをいくつか記録
	m.RecordRequest("gpt-4o", false, http.StatusOK, 250*time.Millisecond, "team-alpha")
	m.RecordRequest("claude-3-5-sonnet", true, http.StatusOK, 1500*time.Millisecond, "team-beta")
	m.RecordTokens("gpt-4o", 100, 50, 150, 0.0015, "team-alpha")
	m.RecordTTFT("claude-3-5-sonnet", 300*time.Millisecond)
	m.IncActive()
	m.DecActive()

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()

	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from /metrics, got %d", rec.Code)
	}

	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	bodyStr := string(body)

	expectedKeywords := []string{
		"kura_requests_total",
		"kura_tokens_total",
		"kura_request_duration_seconds",
		"kura_time_to_first_token_seconds",
		"kura_estimated_cost_usd_total",
		"kura_active_requests",
		"go_goroutines", // Go コレクター
	}

	for _, kw := range expectedKeywords {
		if !strings.Contains(bodyStr, kw) {
			t.Errorf("expected metrics output to contain %q, but was missing", kw)
		}
	}
}

func TestMetrics_NilSafe(t *testing.T) {
	var m *metrics.Metrics

	// nil レシーバー呼び出しでもパニックにならないこと
	m.IncActive()
	m.DecActive()
	m.RecordRequest("gpt-4o", false, 200, time.Second, "svc")
	m.RecordTTFT("gpt-4o", time.Second)
	m.RecordTokens("gpt-4o", 1, 1, 2, 0.01, "svc")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 from nil metrics handler, got %d", rec.Code)
	}
}
