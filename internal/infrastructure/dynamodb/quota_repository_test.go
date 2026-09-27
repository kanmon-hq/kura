package dynamodb

import (
	"context"
	"testing"
	"github.com/northfieldzz/kura/internal/domain/entity"
)

func TestMemoryQuotaRepository_GetAndIncrementTenantUsage(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryQuotaRepository(1000)

	serviceID := "svc-1"
	tenantID := "tenant-1"
	month := "2023-10"
	model := "gpt-4"

	// Initial Get
	usage, err := repo.GetTenantUsage(ctx, serviceID, tenantID, month)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage == nil {
		t.Fatalf("expected usage to be not nil")
	}
	if usage.TotalTokens != 0 || usage.TotalCost != 0 {
		t.Fatalf("expected initial usage to be 0, got tokens %d, cost %f", usage.TotalTokens, usage.TotalCost)
	}

	// Increment
	err = repo.IncrementTenantUsage(ctx, serviceID, tenantID, month, model, 100, 50, 0.5)
	if err != nil {
		t.Fatalf("unexpected error during increment: %v", err)
	}

	// Get after increment
	usage, err = repo.GetTenantUsage(ctx, serviceID, tenantID, month)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage.TotalTokens != 150 {
		t.Fatalf("expected 150 total tokens, got %d", usage.TotalTokens)
	}
	if usage.TotalCost != 0.5 {
		t.Fatalf("expected 0.5 total cost, got %f", usage.TotalCost)
	}

	if len(usage.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(usage.Models))
	}

	modelUsage, ok := usage.Models["gpt-4"]
	if !ok {
		t.Fatalf("expected gpt-4 model usage to exist")
	}
	if modelUsage.TotalTokens != 150 {
		t.Fatalf("expected 150 tokens for gpt-4, got %d", modelUsage.TotalTokens)
	}

	// Increment again
	err = repo.IncrementTenantUsage(ctx, serviceID, tenantID, month, model, 200, 100, 1.0)
	if err != nil {
		t.Fatalf("unexpected error during increment: %v", err)
	}

	// Get after second increment
	usage, err = repo.GetTenantUsage(ctx, serviceID, tenantID, month)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if usage.TotalTokens != 450 {
		t.Fatalf("expected 450 total tokens, got %d", usage.TotalTokens)
	}
	if usage.TotalCost != 1.5 {
		t.Fatalf("expected 1.5 total cost, got %f", usage.TotalCost)
	}
}

func TestMemoryQuotaRepository_ServiceConfig(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryQuotaRepository(1000)

	serviceID := "svc-1"

	// Initial Get should return nil
	cfg, err := repo.GetServiceConfig(ctx, serviceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected initial config to be nil")
	}

	// Set config

	newCfg := &entity.ServiceConfig{
		ServiceID: serviceID,
		CostLimit: 500.0,
		BillingType: "prepaid",
	}
	err = repo.SetServiceConfig(ctx, newCfg)
	if err != nil {
		t.Fatalf("unexpected error setting config: %v", err)
	}

	// Get config
	cfg, err = repo.GetServiceConfig(ctx, serviceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatalf("expected config to be not nil")
	}
	if cfg.CostLimit != 500.0 {
		t.Fatalf("expected cost limit 500.0, got %f", cfg.CostLimit)
	}
	if cfg.BillingType != "prepaid" {
		t.Fatalf("expected billing type prepaid, got %s", cfg.BillingType)
	}
}

func TestMemoryQuotaRepository_TenantConfig(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryQuotaRepository(1000)

	serviceID := "svc-1"
	tenantID := "tenant-1"

	// Initial Get should return nil
	cfg, err := repo.GetTenantConfig(ctx, serviceID, tenantID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected initial config to be nil")
	}

	// Set config
	newCfg := &entity.TenantConfig{
		ServiceID: serviceID,
		TenantID: tenantID,
		CostLimit: 100.0,
		BillingType: "postpaid",
	}
	err = repo.SetTenantConfig(ctx, newCfg)
	if err != nil {
		t.Fatalf("unexpected error setting config: %v", err)
	}

	// Get config
	cfg, err = repo.GetTenantConfig(ctx, serviceID, tenantID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatalf("expected config to be not nil")
	}
	if cfg.CostLimit != 100.0 {
		t.Fatalf("expected cost limit 100.0, got %f", cfg.CostLimit)
	}
	if cfg.BillingType != "postpaid" {
		t.Fatalf("expected billing type postpaid, got %s", cfg.BillingType)
	}
}

func TestMemoryQuotaRepository_LimitsAndUsage(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryQuotaRepository(1000)

	serviceID := "svc-limits"
	tenantID1 := "tenant-l1"
	tenantID2 := "tenant-l2"
	month := "2023-11"

	// Set Service Limit
	err := repo.SetServiceLimit(ctx, serviceID, 1000.0, "postpaid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Set Tenant Limit
	err = repo.SetTenantLimit(ctx, serviceID, tenantID1, 200.0, "prepaid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Increment Usage (with 2 named tenants and 1 empty tenant)
	_ = repo.IncrementTenantUsage(ctx, serviceID, tenantID1, month, "gpt-4", 10, 5, 0.1)
	_ = repo.IncrementTenantUsage(ctx, serviceID, tenantID2, month, "gpt-3.5", 20, 10, 0.05)
	_ = repo.IncrementTenantUsage(ctx, serviceID, "", month, "gpt-4", 10, 5, 0.05) // テナント未指定

	// GetServiceMonthlyUsage
	report, err := repo.GetServiceMonthlyUsage(ctx, serviceID, month)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if report == nil {
		t.Fatalf("expected report to not be nil")
	}
	if report.TotalTokens != 60 {
		t.Fatalf("expected 60 total tokens, got %d", report.TotalTokens)
	}
	if report.TotalCostUSD < 0.199 || report.TotalCostUSD > 0.201 {
		t.Fatalf("expected 0.20 total cost, got %f", report.TotalCostUSD)
	}
	if report.CostLimit != 1000.0 {
		t.Fatalf("expected service cost limit 1000.0, got %f", report.CostLimit)
	}
	// テナント未指定の利用はサービス合計に計上され、report.Tenants には "default" 等のフォールバックで混入しない
	if len(report.Tenants) != 2 {
		t.Fatalf("expected 2 named tenants in report, got %d", len(report.Tenants))
	}
	if _, hasDefault := report.Tenants["default"]; hasDefault {
		t.Fatalf("report.Tenants should not have 'default' entry for omitted tenantID")
	}
	if len(report.Models) != 2 {
		t.Fatalf("expected 2 models in report, got %d", len(report.Models))
	}

	// GetAllTenantsUsageByMonth
	usages, err := repo.GetAllTenantsUsageByMonth(ctx, month)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(usages) != 3 {
		t.Fatalf("expected 3 tenant usages (including empty tenant), got %d", len(usages))
	}
}

func TestMemoryQuotaRepository_Lock(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryQuotaRepository(1000)

	lockKey := "test-lock"

	// Acquire lock successfully
	acquired, err := repo.AcquireLock(ctx, lockKey, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !acquired {
		t.Fatalf("expected to acquire lock")
	}

	// Try to acquire again, should fail
	acquired, err = repo.AcquireLock(ctx, lockKey, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if acquired {
		t.Fatalf("expected to not acquire lock while it is active")
	}
}

func TestMemoryQuotaRepository_Notifications(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryQuotaRepository(1000)

	// List empty notifications
	ntfs, err := repo.ListNotifications(ctx, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ntfs) != 0 {
		t.Fatalf("expected 0 notifications, got %d", len(ntfs))
	}

	// Save notification
	ntf1 := &entity.Notification{Title: "Title1", Message: "Message1"}
	err = repo.SaveNotification(ctx, ntf1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ntf2 := &entity.Notification{Title: "Title2", Message: "Message2"}
	err = repo.SaveNotification(ctx, ntf2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// List notifications (should be prepended, so ntf2 is first)
	ntfs, err = repo.ListNotifications(ctx, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ntfs) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(ntfs))
	}
	if ntfs[0].Title != "Title2" {
		t.Fatalf("expected Title2 to be first, got %s", ntfs[0].Title)
	}

	ntfs, err = repo.ListNotifications(ctx, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ntfs) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(ntfs))
	}
}

func TestMemoryQuotaRepository_Ping(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryQuotaRepository(1000)

	err := repo.Ping(ctx)
	if err != nil {
		t.Fatalf("expected ping to succeed, got error: %v", err)
	}
}

func TestDynamoDBQuotaRepository_AllMethods(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryQuotaRepository(1000000)

	// Config
	_ = repo.SetServiceLimit(ctx, "svc-wrap", 200.0, "payg")
	sCfg, _ := repo.GetServiceConfig(ctx, "svc-wrap")
	if sCfg == nil || sCfg.CostLimit != 200.0 {
		t.Errorf("unexpected service config")
	}

	_ = repo.SetTenantLimit(ctx, "svc-wrap", "t-wrap", 100.0, "capped")
	tCfg, _ := repo.GetTenantConfig(ctx, "svc-wrap", "t-wrap")
	if tCfg == nil || tCfg.CostLimit != 100.0 {
		t.Errorf("unexpected tenant config")
	}

	// Cost & Usage
	_ = repo.IncrementCost(ctx, "svc-wrap", "t-wrap", "2026-09", 100, 100, 0.02)
	cost, tokens, _ := repo.GetServiceCost(ctx, "svc-wrap", "2026-09")
	if tokens != 200 || cost != 0.02 {
		t.Errorf("unexpected service cost/tokens: %f, %d", cost, tokens)
	}
	tCost, tTokens, _ := repo.GetTenantCost(ctx, "svc-wrap", "t-wrap", "2026-09")
	if tTokens != 200 || tCost != 0.02 {
		t.Errorf("unexpected tenant cost/tokens: %f, %d", tCost, tTokens)
	}

	_ = repo.ResetCost(ctx, "svc-wrap", "t-wrap", "2026-09", 0.5, 500)
	_ = repo.RecordUsage(ctx, "svc-wrap", "t-wrap", "2026-09", "fast", 100, 100, 0.02, "v1")
	u, _ := repo.GetTenantUsage(ctx, "svc-wrap", "t-wrap", "2026-09")
	if u == nil || u.TotalTokens != 700 {
		t.Errorf("unexpected tenant usage, got tokens: %d", u.TotalTokens)
	}

	r, _ := repo.GetServiceMonthlyUsage(ctx, "svc-wrap", "2026-09")
	if r == nil || r.TotalTokens != 700 {
		t.Errorf("unexpected service monthly report, got tokens: %d", r.TotalTokens)
	}

	allU, _ := repo.GetAllTenantsUsageByMonth(ctx, "2026-09")
	if len(allU) != 1 {
		t.Errorf("unexpected all tenants usage")
	}

	// Lock & Notification & Ping
	ok, _ := repo.AcquireLock(ctx, "wrap-lock", 60)
	if !ok {
		t.Errorf("expected lock acquired")
	}
	_ = repo.ReleaseLock(ctx, "wrap-lock")

	ntf := &entity.Notification{Title: "WrapTitle", Message: "WrapMsg"}
	_ = repo.SaveNotification(ctx, ntf)
	ntfs, _ := repo.ListNotifications(ctx, 10)
	if len(ntfs) != 1 {
		t.Errorf("unexpected notifications length")
	}

	_ = repo.Ping(ctx)
}
