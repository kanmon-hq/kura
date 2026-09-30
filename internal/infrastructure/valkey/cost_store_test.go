package valkey_test

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/kanmon-hq/kura/internal/infrastructure/valkey"
	"github.com/redis/go-redis/v9"
)

func TestValkeyCostStore_AllOperations(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	defer mr.Close()

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	store := valkey.NewValkeyCostStoreWithClient(client)
	ctx := context.Background()

	// 1. URL による初期化
	urlStore, err := valkey.NewValkeyCostStore("redis://" + mr.Addr())
	if err != nil {
		t.Fatalf("failed to init via URL: %v", err)
	}
	if err := urlStore.Ping(ctx); err != nil {
		t.Fatalf("failed to ping urlStore: %v", err)
	}

	// 2. 設定操作 (Service & Tenant Limits)
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

	// 3. インクリメント操作 (IncrementCost)
	if err := store.IncrementCost(ctx, "svc-1", "tenant-1", "2026-09", 100, 50, 0.05); err != nil {
		t.Fatalf("failed to increment cost: %v", err)
	}
	sCost, sTokens, err := store.GetServiceCost(ctx, "svc-1", "2026-09")
	if err != nil || sTokens != 150 {
		t.Fatalf("unexpected service cost: %f, tokens: %d", sCost, sTokens)
	}
	tCost, tTokens, err := store.GetTenantCost(ctx, "svc-1", "tenant-1", "2026-09")
	if err != nil || tTokens != 150 {
		t.Fatalf("unexpected tenant cost: %f, tokens: %d", tCost, tTokens)
	}

	// 4. リセット操作 (ResetCost)
	if err := store.ResetCost(ctx, "svc-1", "tenant-1", "2026-09", 1.0, 500); err != nil {
		t.Fatalf("failed to reset cost: %v", err)
	}
	tCost, tTokens, _ = store.GetTenantCost(ctx, "svc-1", "tenant-1", "2026-09")
	if tTokens != 500 {
		t.Fatalf("expected tokens 500, got %d", tTokens)
	}
}
