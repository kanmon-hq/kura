package cache

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/northfieldzz/kura/internal/domain/entity"
	"github.com/northfieldzz/kura/internal/infrastructure/memory"
	"github.com/northfieldzz/kura/internal/infrastructure/metrics"
)

func TestCachedCostStore_NegativeCache(t *testing.T) {
	memStore := memory.NewMemoryStore()
	m := metrics.NewMetrics()
	opts := CacheOptions{
		Enabled:     true,
		NegativeTTL: 200 * time.Millisecond,
	}
	cached := NewCachedCostStore(memStore, m, opts)
	defer cached.Close()

	serviceID := "svc-test"
	tenantID := "tenant-exceeded"

	// Initial check: not negative cached
	if cached.IsNegativeCached(serviceID, tenantID) {
		t.Fatalf("expected not negative cached initially")
	}

	// Mark negative cached
	now := time.Now().UTC()
	cached.MarkNegativeCached(serviceID, tenantID, now)

	// Check: should be negative cached
	if !cached.IsNegativeCached(serviceID, tenantID) {
		t.Fatalf("expected negative cached after marking")
	}

	// Invalidate tenant
	cached.InvalidateTenant(serviceID, tenantID)
	if cached.IsNegativeCached(serviceID, tenantID) {
		t.Fatalf("expected negative cache cleared after InvalidateTenant")
	}

	// Mark again and wait for TTL expiration
	cached.MarkNegativeCached(serviceID, tenantID, time.Now().UTC())
	time.Sleep(250 * time.Millisecond)
	if cached.IsNegativeCached(serviceID, tenantID) {
		t.Fatalf("expected negative cache expired after TTL")
	}
}

func TestCachedCostStore_ConfigCache(t *testing.T) {
	memStore := memory.NewMemoryStore()
	ctx := context.Background()
	serviceID := "svc-cfg"
	tenantID := "tenant-cfg"

	// Set initial config in underlying store
	_ = memStore.SetTenantLimit(ctx, serviceID, tenantID, 100.0, string(entity.BillingTypePAYG))

	m := metrics.NewMetrics()
	opts := CacheOptions{
		Enabled:   true,
		ConfigTTL: 500 * time.Millisecond,
	}
	cached := NewCachedCostStore(memStore, m, opts)
	defer cached.Close()

	// 1st get (miss -> fetch from underlying)
	tc, err := cached.GetTenantConfig(ctx, serviceID, tenantID)
	if err != nil || tc == nil || tc.CostLimit != 100.0 {
		t.Fatalf("unexpected tenant config: %+v, err: %v", tc, err)
	}

	// Direct update underlying without cache knowing
	_ = memStore.SetTenantLimit(ctx, serviceID, tenantID, 200.0, string(entity.BillingTypePAYG))

	// 2nd get (hit cache -> still returns 100.0)
	tcCached, err := cached.GetTenantConfig(ctx, serviceID, tenantID)
	if err != nil || tcCached.CostLimit != 100.0 {
		t.Fatalf("expected cached limit 100.0, got %v", tcCached)
	}

	// Update via CachedCostStore (invalidates cache)
	_ = cached.SetTenantLimit(ctx, serviceID, tenantID, 300.0, string(entity.BillingTypePAYG))

	// 3rd get (fetched fresh 300.0)
	tcFresh, err := cached.GetTenantConfig(ctx, serviceID, tenantID)
	if err != nil || tcFresh.CostLimit != 300.0 {
		t.Fatalf("expected refreshed limit 300.0, got %v", tcFresh)
	}
}

func TestCachedCostStore_BalanceCache(t *testing.T) {
	memStore := memory.NewMemoryStore()
	ctx := context.Background()
	serviceID := "svc-bal"
	tenantID := "tenant-bal"
	month := "2026-09"

	_ = memStore.IncrementCost(ctx, serviceID, tenantID, month, 100, 100, 10.0)

	m := metrics.NewMetrics()
	opts := CacheOptions{
		Enabled:    true,
		BalanceTTL: 500 * time.Millisecond,
	}
	cached := NewCachedCostStore(memStore, m, opts)
	defer cached.Close()

	// 1st get
	cost, tokens, err := cached.GetTenantCost(ctx, serviceID, tenantID, month)
	if err != nil || cost != 10.0 || tokens != 200 {
		t.Fatalf("unexpected cost: %v, tokens: %v", cost, tokens)
	}

	// Directly mutate underlying
	_ = memStore.IncrementCost(ctx, serviceID, tenantID, month, 50, 50, 5.0)

	// Cached get returns old
	costCached, _, _ := cached.GetTenantCost(ctx, serviceID, tenantID, month)
	if costCached != 10.0 {
		t.Fatalf("expected cached cost 10.0, got %v", costCached)
	}

	// Increment via CachedCostStore invalidates balance cache
	_ = cached.IncrementCost(ctx, serviceID, tenantID, month, 10, 10, 1.0)

	// Fresh get (10 + 5 + 1 = 16)
	costFresh, _, _ := cached.GetTenantCost(ctx, serviceID, tenantID, month)
	if costFresh != 16.0 {
		t.Fatalf("expected fresh cost 16.0, got %v", costFresh)
	}
}

func TestCachedCostStore_BatchBuffering(t *testing.T) {
	memStore := memory.NewMemoryStore()
	ctx := context.Background()
	serviceID := "svc-batch"
	tenantID := "tenant-batch"
	month := "2026-09"

	m := metrics.NewMetrics()
	opts := CacheOptions{
		Enabled:            true,
		BatchFlushInterval: 100 * time.Millisecond,
	}
	cached := NewCachedCostStore(memStore, m, opts)

	// Increment (buffered in memory, not yet in underlying)
	if err := cached.IncrementCost(ctx, serviceID, tenantID, month, 100, 100, 5.0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Underlying should still be 0 before flush
	costUnderlying, _, _ := memStore.GetTenantCost(ctx, serviceID, tenantID, month)
	if costUnderlying != 0.0 {
		t.Fatalf("expected underlying cost 0 before flush, got %v", costUnderlying)
	}

	// Wait for automatic ticker flush
	time.Sleep(150 * time.Millisecond)

	costFlushed, _, _ := memStore.GetTenantCost(ctx, serviceID, tenantID, month)
	if costFlushed != 5.0 {
		t.Fatalf("expected underlying cost 5.0 after ticker flush, got %v", costFlushed)
	}

	// Add more and test Close() graceful flush
	_ = cached.IncrementCost(ctx, serviceID, tenantID, month, 200, 200, 10.0)
	_ = cached.Close()

	costFinal, tokensFinal, _ := memStore.GetTenantCost(ctx, serviceID, tenantID, month)
	if costFinal != 15.0 || tokensFinal != 600 {
		t.Fatalf("expected final cost 15.0 and tokens 600, got cost=%v, tokens=%v", costFinal, tokensFinal)
	}
}

func TestCachedCostStore_Concurrency(t *testing.T) {
	memStore := memory.NewMemoryStore()
	ctx := context.Background()
	m := metrics.NewMetrics()
	opts := CacheOptions{
		Enabled:            true,
		NegativeTTL:        100 * time.Millisecond,
		ConfigTTL:          100 * time.Millisecond,
		BalanceTTL:         100 * time.Millisecond,
		BatchFlushInterval: 50 * time.Millisecond,
	}
	cached := NewCachedCostStore(memStore, m, opts)
	defer cached.Close()

	var wg sync.WaitGroup
	serviceID := "svc-conc"
	month := "2026-09"

	for i := 0; i < 20; i++ {
		wg.Add(1)
		tenantID := "tenant-conc"
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				_ = cached.IncrementCost(ctx, serviceID, tenantID, month, 10, 10, 0.5)
			} else {
				_, _, _ = cached.GetTenantCost(ctx, serviceID, tenantID, month)
				cached.MarkNegativeCached(serviceID, tenantID, time.Now().UTC())
				_ = cached.IsNegativeCached(serviceID, tenantID)
				cached.InvalidateTenant(serviceID, tenantID)
			}
		}(i)
	}

	wg.Wait()
}

func TestCachedCostStore_ServiceOperationsAndPing(t *testing.T) {
	memStore := memory.NewMemoryStore()
	ctx := context.Background()
	m := metrics.NewMetrics()
	opts := CacheOptions{
		Enabled:   true,
		ConfigTTL: 500 * time.Millisecond,
	}
	cached := NewCachedCostStore(memStore, m, opts)
	defer cached.Close()

	// SetServiceLimit & GetServiceConfig
	if err := cached.SetServiceLimit(ctx, "svc-all", 500.0, "payg"); err != nil {
		t.Fatalf("failed to set service limit: %v", err)
	}
	cfg, err := cached.GetServiceConfig(ctx, "svc-all")
	if err != nil || cfg == nil || cfg.CostLimit != 500.0 {
		t.Fatalf("unexpected service config: %v", cfg)
	}

	// ServiceCost & ResetCost
	if err := cached.ResetCost(ctx, "svc-all", "tenant-1", "2026-09", 10.0, 1000); err != nil {
		t.Fatalf("failed to reset cost: %v", err)
	}
	cost, tokens, err := cached.GetServiceCost(ctx, "svc-all", "2026-09")
	if err != nil {
		t.Fatalf("failed to get service cost: %v", err)
	}
	_ = cost
	_ = tokens

	if err := cached.Ping(ctx); err != nil {
		t.Fatalf("failed to ping cached cost store: %v", err)
	}
}
