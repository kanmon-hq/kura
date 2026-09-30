package usecase

import (
	"context"
	"net/http"
	"testing"

	"github.com/kanmon-hq/kura/internal/domain/entity"
)

func BenchmarkParseTagsHeader(b *testing.B) {
	header := "env=production,team=analytics,feature=billing-v2,priority=high"

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = parseTagsHeader(header)
	}
}

func BenchmarkParseTagsHeader_Empty(b *testing.B) {
	header := ""

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = parseTagsHeader(header)
	}
}

func BenchmarkAuthenticateRequest(b *testing.B) {
	repo := &mockQuotaRepo{
		getServiceUsageFn: func(ctx context.Context, serviceID, month string) (*entity.ServiceMonthlyReport, error) {
			return &entity.ServiceMonthlyReport{
				ServiceID:    serviceID,
				Month:        month,
				CostLimit:    100.0,
				TotalCostUSD: 10.0,
				BillingType:  "capped",
			}, nil
		},
	}
	uc := NewAuthUseCase(repo, repo)

	req, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("X-Service-ID", "payment-service")
	req.Header.Set("X-Tenant-ID", "tenant-corp-a")
	req.Header.Set("X-User-ID", "user-bench-01")
	req.Header.Set("X-Data-Residency", "japan")
	req.Header.Set("X-Environment", "production")
	req.Header.Set("X-Tags", "env=prod,team=backend")

	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, _ = uc.AuthenticateRequest(ctx, req)
	}
}
