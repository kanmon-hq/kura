package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kanmon-hq/kura/internal/domain/entity"
)

func createTestSQLiteStore(t *testing.T) (*SQLiteStore, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "kura_sqlite_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	dbPath := filepath.Join(dir, "test.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("failed to create SQLiteStore: %v", err)
	}

	cleanup := func() {
		store.Close()
		os.RemoveAll(dir)
	}
	return store, cleanup
}

func TestSQLiteStore_CostOperations(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	serviceID := "svc-1"
	tenantID := "tenant-1"
	month := "2026-09"

	// Initial check
	cost, tokens, err := store.GetTenantCost(ctx, serviceID, tenantID, month)
	if err != nil {
		t.Fatalf("unexpected error on initial check: %v", err)
	}
	if cost != 0 || tokens != 0 {
		t.Errorf("expected 0, 0; got cost=%v, tokens=%v", cost, tokens)
	}

	// Set tenant limit
	if err := store.SetTenantLimit(ctx, serviceID, tenantID, 50.0, string(entity.BillingTypePAYG)); err != nil {
		t.Fatalf("failed to set tenant limit: %v", err)
	}

	tc, err := store.GetTenantConfig(ctx, serviceID, tenantID)
	if err != nil {
		t.Fatalf("unexpected error after set limit: %v", err)
	}
	if tc == nil || tc.CostLimit != 50.0 || string(tc.BillingType) != string(entity.BillingTypePAYG) {
		t.Errorf("expected 50, PAYG; got %+v", tc)
	}

	// Increment cost
	if err := store.IncrementCost(ctx, serviceID, tenantID, month, 100, 200, 20.0); err != nil {
		t.Fatalf("failed to increment cost: %v", err)
	}

	cost, tokens, err = store.GetTenantCost(ctx, serviceID, tenantID, month)
	if err != nil {
		t.Fatalf("failed to get tenant cost: %v", err)
	}
	if cost != 20.0 || tokens != 300 {
		t.Errorf("expected 20.0, 300; got cost=%v, tokens=%v", cost, tokens)
	}

	// Increment again
	if err := store.IncrementCost(ctx, serviceID, tenantID, month, 50, 50, 35.0); err != nil {
		t.Fatalf("failed to increment cost: %v", err)
	}

	cost, tokens, err = store.GetTenantCost(ctx, serviceID, tenantID, month)
	if err != nil {
		t.Fatalf("failed to get tenant cost: %v", err)
	}
	if cost != 55.0 || tokens != 400 {
		t.Errorf("expected 55.0, 400; got cost=%v, tokens=%v", cost, tokens)
	}

	// Reset cost
	if err := store.ResetCost(ctx, serviceID, tenantID, month, 0.0, 0); err != nil {
		t.Fatalf("failed to reset cost: %v", err)
	}

	cost, tokens, err = store.GetTenantCost(ctx, serviceID, tenantID, month)
	if err != nil {
		t.Fatalf("unexpected error after reset: %v", err)
	}
	if cost != 0 || tokens != 0 {
		t.Errorf("expected 0, 0; got cost=%v, tokens=%v", cost, tokens)
	}
}

func TestSQLiteStore_ConfigOperations(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	serviceID := "svc-cfg"
	tenantID := "tenant-cfg"

	// Initial configs should be nil
	sc, err := store.GetServiceConfig(ctx, serviceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc != nil {
		t.Errorf("expected nil service config, got %v", sc)
	}

	// Set service limit
	if err := store.SetServiceLimit(ctx, serviceID, 100.0, string(entity.BillingTypePAYG)); err != nil {
		t.Fatalf("failed to set service limit: %v", err)
	}

	sc, err = store.GetServiceConfig(ctx, serviceID)
	if err != nil {
		t.Fatalf("failed to get service config: %v", err)
	}
	if sc == nil || sc.CostLimit != 100.0 || string(sc.BillingType) != string(entity.BillingTypePAYG) {
		t.Errorf("unexpected service config returned: %+v", sc)
	}

	// Set tenant limit
	if err := store.SetTenantLimit(ctx, serviceID, tenantID, 200.0, string(entity.BillingTypeCapped)); err != nil {
		t.Fatalf("failed to set tenant limit: %v", err)
	}

	tc, err := store.GetTenantConfig(ctx, serviceID, tenantID)
	if err != nil {
		t.Fatalf("failed to get tenant config: %v", err)
	}
	if tc == nil || tc.CostLimit != 200.0 || string(tc.BillingType) != string(entity.BillingTypeCapped) {
		t.Errorf("unexpected tenant config returned: %+v", tc)
	}
}

func TestSQLiteStore_UsageOperations(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	serviceID := "svc-1"
	tenantID := "tenant-1"
	month := "2026-09"
	model := "claude-3-5-sonnet"

	if err := store.RecordUsage(ctx, serviceID, tenantID, month, model, 100, 200, 0.05, "v1"); err != nil {
		t.Fatalf("failed to record usage: %v", err)
	}

	usage, err := store.GetTenantUsage(ctx, serviceID, tenantID, month)
	if err != nil {
		t.Fatalf("failed to get tenant usage: %v", err)
	}
	if usage == nil || usage.TotalTokens != 300 || usage.TotalCost != 0.05 {
		t.Fatalf("unexpected tenant usage: %+v", usage)
	}
	if mu, ok := usage.Models[model]; !ok || mu.TotalTokens != 300 || mu.Cost != 0.05 {
		t.Errorf("unexpected model usage: %+v", mu)
	}

	// Report
	report, err := store.GetServiceMonthlyUsage(ctx, serviceID, month)
	if err != nil {
		t.Fatalf("failed to get service monthly usage: %v", err)
	}
	if report.TotalTokens != 300 || report.TotalCostUSD != 0.05 {
		t.Errorf("unexpected service report: %+v", report)
	}
	if tItem, ok := report.Tenants[tenantID]; !ok || tItem.TotalTokens != 300 {
		t.Errorf("unexpected tenant in report: %+v", tItem)
	}

	// All tenants usage
	all, err := store.GetAllTenantsUsageByMonth(ctx, month)
	if err != nil {
		t.Fatalf("failed to get all tenants usage: %v", err)
	}
	if len(all) != 1 || all[0].TenantID != tenantID {
		t.Errorf("unexpected all tenants usage: %+v", all)
	}
}

func TestSQLiteStore_Lock(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	lockKey := "monthly_aggregation"

	// Acquire lock 1
	acquired, err := store.AcquireLock(ctx, lockKey, 2)
	if err != nil {
		t.Fatalf("unexpected error acquiring lock: %v", err)
	}
	if !acquired {
		t.Fatalf("expected lock to be acquired")
	}

	// Try acquiring again immediately (should fail)
	acquired2, err := store.AcquireLock(ctx, lockKey, 2)
	if err != nil {
		t.Fatalf("unexpected error on second lock attempt: %v", err)
	}
	if acquired2 {
		t.Fatalf("expected second lock attempt to fail")
	}

	// Release lock
	if err := store.ReleaseLock(ctx, lockKey); err != nil {
		t.Fatalf("failed to release lock: %v", err)
	}

	// Acquire after release
	acquired3, err := store.AcquireLock(ctx, lockKey, 2)
	if err != nil {
		t.Fatalf("unexpected error on re-acquiring lock: %v", err)
	}
	if !acquired3 {
		t.Fatalf("expected lock to be acquired after release")
	}
}

func TestSQLiteStore_Notifications(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	ntf := &entity.Notification{
		ID:        "ntf-1",
		Type:      entity.NotificationTypeAlert,
		Title:     "Budget Alert",
		Message:   "Tenant exceeded budget",
		IsAlert:   true,
		CreatedAt: time.Now().UTC(),
	}

	if err := store.SaveNotification(ctx, ntf); err != nil {
		t.Fatalf("failed to save notification: %v", err)
	}

	list, err := store.ListNotifications(ctx, 10)
	if err != nil {
		t.Fatalf("failed to list notifications: %v", err)
	}
	if len(list) != 1 || list[0].ID != "ntf-1" || !list[0].IsAlert {
		t.Errorf("unexpected notifications: %+v", list)
	}
}
