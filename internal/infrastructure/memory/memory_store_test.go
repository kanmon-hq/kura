package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/infrastructure/memory"
)

func TestMemoryStore_AllOperations(t *testing.T) {
	ctx := context.Background()
	store := memory.NewMemoryStore()

	// 1. CostStore - Cost Limits & Configs
	if err := store.SetServiceLimit(ctx, "svc-1", 100.0, "payg"); err != nil {
		t.Fatalf("failed to set service limit: %v", err)
	}
	sCfg, err := store.GetServiceConfig(ctx, "svc-1")
	if err != nil || sCfg == nil || sCfg.CostLimit != 100.0 {
		t.Fatalf("unexpected service config: %v", sCfg)
	}

	if err := store.SetTenantLimit(ctx, "svc-1", "tenant-1", 50.0, "capped"); err != nil {
		t.Fatalf("failed to set tenant limit: %v", err)
	}
	tCfg, err := store.GetTenantConfig(ctx, "svc-1", "tenant-1")
	if err != nil || tCfg == nil || tCfg.CostLimit != 50.0 {
		t.Fatalf("unexpected tenant config: %v", tCfg)
	}

	// 2. CostStore - Cost Increment & Reset
	if err := store.IncrementCost(ctx, "svc-1", "tenant-1", "2026-09", 100, 50, 0.05); err != nil {
		t.Fatalf("failed to increment cost: %v", err)
	}
	sCost, sTokens, err := store.GetServiceCost(ctx, "svc-1", "2026-09")
	if err != nil || sCost != 0.05 || sTokens != 150 {
		t.Fatalf("unexpected service cost: %f, tokens: %d", sCost, sTokens)
	}
	tCost, tTokens, err := store.GetTenantCost(ctx, "svc-1", "tenant-1", "2026-09")
	if err != nil || tCost != 0.05 || tTokens != 150 {
		t.Fatalf("unexpected tenant cost: %f, tokens: %d", tCost, tTokens)
	}

	if err := store.ResetCost(ctx, "svc-1", "tenant-1", "2026-09", 1.0, 500); err != nil {
		t.Fatalf("failed to reset cost: %v", err)
	}
	tCost, tTokens, _ = store.GetTenantCost(ctx, "svc-1", "tenant-1", "2026-09")
	if tCost != 1.0 || tTokens != 500 {
		t.Fatalf("expected reset cost 1.0, got %f", tCost)
	}

	// 3. UsageStore - Record & Reports
	if err := store.RecordUsage(ctx, "svc-1", "tenant-1", "2026-09", "gpt-4o", 100, 50, 0.05, "v1"); err != nil {
		t.Fatalf("failed to record usage: %v", err)
	}
	usage, err := store.GetTenantUsage(ctx, "svc-1", "tenant-1", "2026-09")
	if err != nil || usage == nil || usage.TotalTokens != 150 {
		t.Fatalf("unexpected tenant usage: %v", usage)
	}

	report, err := store.GetServiceMonthlyUsage(ctx, "svc-1", "2026-09")
	if err != nil || report == nil || report.TotalTokens != 150 {
		t.Fatalf("unexpected monthly report: %v", report)
	}

	allUsage, err := store.GetAllTenantsUsageByMonth(ctx, "2026-09")
	if err != nil || len(allUsage) != 1 {
		t.Fatalf("unexpected all tenants usage: %v", allUsage)
	}

	// 4. Distributed Lock
	ok, err := store.AcquireLock(ctx, "test-lock", 10)
	if err != nil || !ok {
		t.Fatalf("failed to acquire lock")
	}
	ok2, _ := store.AcquireLock(ctx, "test-lock", 10)
	if ok2 {
		t.Fatalf("expected lock conflict, but got acquired")
	}
	if err := store.ReleaseLock(ctx, "test-lock"); err != nil {
		t.Fatalf("failed to release lock: %v", err)
	}

	// 5. Notifications
	ntf := &entity.Notification{
		ID:        "ntf-1",
		Type:      entity.NotificationTypeAlert,
		Title:     "Alert",
		Message:   "Quota warning",
		IsAlert:   true,
		CreatedAt: time.Now(),
	}
	if err := store.SaveNotification(ctx, ntf); err != nil {
		t.Fatalf("failed to save notification: %v", err)
	}
	list, err := store.ListNotifications(ctx, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("unexpected notification list: %v", list)
	}

	if err := store.Ping(ctx); err != nil {
		t.Fatalf("ping failed: %v", err)
	}
}
