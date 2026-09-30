package cache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/repository"
	"github.com/kanmon-hq/kura/internal/infrastructure/metrics"
)

// CacheOptions はキャッシュ層の設定
type CacheOptions struct {
	Enabled              bool
	NegativeTTL          time.Duration
	ConfigTTL            time.Duration
	BalanceTTL           time.Duration
	BatchFlushInterval   time.Duration
}

type negativeEntry struct {
	expiresAt time.Time
}

type serviceConfigEntry struct {
	cfg       *entity.ServiceConfig
	expiresAt time.Time
}

type tenantConfigEntry struct {
	cfg       *entity.TenantConfig
	expiresAt time.Time
}

type balanceEntry struct {
	cost      float64
	tokens    int64
	expiresAt time.Time
}

type batchItemKey struct {
	serviceID string
	tenantID  string
	month     string
}

type batchItemValue struct {
	promptTokens     int64
	completionTokens int64
	cost             float64
}

// CachedCostStore は CostStore をラップするキャッシュデコレーター
type CachedCostStore struct {
	underlying repository.CostStore
	metrics    *metrics.Metrics
	opts       CacheOptions

	negativeCache      sync.Map // key: serviceID:tenantID -> negativeEntry
	serviceConfigCache sync.Map // key: serviceID -> serviceConfigEntry
	tenantConfigCache  sync.Map // key: serviceID:tenantID -> tenantConfigEntry
	balanceCache       sync.Map // key: svc:month / svc:tenant:month -> balanceEntry

	// バッチ書き込みバッファ
	bufferMu sync.Mutex
	buffer   map[batchItemKey]*batchItemValue
	stopChan chan struct{}
	flushWg  sync.WaitGroup
}

var _ repository.CostStore = (*CachedCostStore)(nil)

// NewCachedCostStore は CostStore をキャッシュ層でラップする
func NewCachedCostStore(underlying repository.CostStore, m *metrics.Metrics, opts CacheOptions) *CachedCostStore {
	store := &CachedCostStore{
		underlying: underlying,
		metrics:    m,
		opts:       opts,
		buffer:     make(map[batchItemKey]*batchItemValue),
		stopChan:   make(chan struct{}),
	}

	if opts.BatchFlushInterval > 0 {
		store.flushWg.Add(1)
		go store.startFlushWorker(opts.BatchFlushInterval)
	}

	return store
}

// Underlying は内部の CostStore を返す
func (c *CachedCostStore) Underlying() repository.CostStore {
	return c.underlying
}

// ================= ネガティブキャッシュ関連 =================

// IsNegativeCached は該当テナントが予算超過でネガティブキャッシュされているかを判定する
func (c *CachedCostStore) IsNegativeCached(serviceID, tenantID string) bool {
	if !c.opts.Enabled || c.opts.NegativeTTL <= 0 || tenantID == "" {
		return false
	}
	key := fmt.Sprintf("%s:%s", serviceID, tenantID)
	val, ok := c.negativeCache.Load(key)
	if !ok {
		return false
	}
	entry := val.(negativeEntry)
	if time.Now().UTC().After(entry.expiresAt) {
		c.negativeCache.Delete(key)
		return false
	}
	if c.metrics != nil {
		c.metrics.RecordCacheHit("negative")
		c.metrics.RecordNegativeRejection(serviceID)
	}
	return true
}

// MarkNegativeCached はテナントをネガティブキャッシュ（予算超過）に登録する
func (c *CachedCostStore) MarkNegativeCached(serviceID, tenantID string, now time.Time) {
	if !c.opts.Enabled || c.opts.NegativeTTL <= 0 || tenantID == "" {
		return
	}
	nowUTC := now.UTC()
	// 当月末の時刻
	nextMonth := time.Date(nowUTC.Year(), nowUTC.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	ttl := c.opts.NegativeTTL
	if nowUTC.Add(ttl).After(nextMonth) {
		ttl = nextMonth.Sub(nowUTC)
	}
	entry := negativeEntry{
		expiresAt: nowUTC.Add(ttl),
	}
	key := fmt.Sprintf("%s:%s", serviceID, tenantID)
	c.negativeCache.Store(key, entry)
}

// InvalidateTenant は指定テナントの全キャッシュを無効化する
func (c *CachedCostStore) InvalidateTenant(serviceID, tenantID string) {
	key := fmt.Sprintf("%s:%s", serviceID, tenantID)
	c.negativeCache.Delete(key)
	c.tenantConfigCache.Delete(key)

	// バランスキャッシュも削除 (serviceID:tenantID を含むプレフィックス)
	prefix := fmt.Sprintf("svc:%s:tenant:%s:", serviceID, tenantID)
	c.balanceCache.Range(func(k, v any) bool {
		if strKey, ok := k.(string); ok && len(strKey) >= len(prefix) && strKey[:len(prefix)] == prefix {
			c.balanceCache.Delete(k)
		}
		return true
	})
}

// InvalidateService は指定サービスの全キャッシュを無効化する
func (c *CachedCostStore) InvalidateService(serviceID string) {
	c.serviceConfigCache.Delete(serviceID)
	prefix := fmt.Sprintf("svc:%s:", serviceID)
	c.balanceCache.Range(func(k, v any) bool {
		if strKey, ok := k.(string); ok && len(strKey) >= len(prefix) && strKey[:len(prefix)] == prefix {
			c.balanceCache.Delete(k)
		}
		return true
	})
}

// InvalidateAll は全キャッシュを無効化する (Reconciliation 完了時など)
func (c *CachedCostStore) InvalidateAll() {
	c.negativeCache.Range(func(k, v any) bool {
		c.negativeCache.Delete(k)
		return true
	})
	c.serviceConfigCache.Range(func(k, v any) bool {
		c.serviceConfigCache.Delete(k)
		return true
	})
	c.tenantConfigCache.Range(func(k, v any) bool {
		c.tenantConfigCache.Delete(k)
		return true
	})
	c.balanceCache.Range(func(k, v any) bool {
		c.balanceCache.Delete(k)
		return true
	})
}

// ================= CostStore インターフェース実装 =================

func (c *CachedCostStore) GetServiceCost(ctx context.Context, serviceID, month string) (float64, int64, error) {
	if !c.opts.Enabled || c.opts.BalanceTTL <= 0 {
		return c.underlying.GetServiceCost(ctx, serviceID, month)
	}

	key := fmt.Sprintf("svc:%s:month:%s", serviceID, month)
	if val, ok := c.balanceCache.Load(key); ok {
		entry := val.(balanceEntry)
		if time.Now().UTC().Before(entry.expiresAt) {
			if c.metrics != nil {
				c.metrics.RecordCacheHit("balance")
			}
			return entry.cost, entry.tokens, nil
		}
		c.balanceCache.Delete(key)
	}

	if c.metrics != nil {
		c.metrics.RecordCacheMiss("balance")
	}

	cost, tokens, err := c.underlying.GetServiceCost(ctx, serviceID, month)
	if err == nil {
		c.balanceCache.Store(key, balanceEntry{
			cost:      cost,
			tokens:    tokens,
			expiresAt: time.Now().UTC().Add(c.opts.BalanceTTL),
		})
	}
	return cost, tokens, err
}

func (c *CachedCostStore) GetTenantCost(ctx context.Context, serviceID, tenantID, month string) (float64, int64, error) {
	if !c.opts.Enabled || c.opts.BalanceTTL <= 0 {
		return c.underlying.GetTenantCost(ctx, serviceID, tenantID, month)
	}

	key := fmt.Sprintf("svc:%s:tenant:%s:month:%s", serviceID, tenantID, month)
	if val, ok := c.balanceCache.Load(key); ok {
		entry := val.(balanceEntry)
		if time.Now().UTC().Before(entry.expiresAt) {
			if c.metrics != nil {
				c.metrics.RecordCacheHit("balance")
			}
			return entry.cost, entry.tokens, nil
		}
		c.balanceCache.Delete(key)
	}

	if c.metrics != nil {
		c.metrics.RecordCacheMiss("balance")
	}

	cost, tokens, err := c.underlying.GetTenantCost(ctx, serviceID, tenantID, month)
	if err == nil {
		c.balanceCache.Store(key, balanceEntry{
			cost:      cost,
			tokens:    tokens,
			expiresAt: time.Now().UTC().Add(c.opts.BalanceTTL),
		})
	}
	return cost, tokens, err
}

func (c *CachedCostStore) IncrementCost(ctx context.Context, serviceID, tenantID, month string, promptTokens, completionTokens int64, cost float64) error {
	// バッチ書き込みが有効な場合
	if c.opts.Enabled && c.opts.BatchFlushInterval > 0 {
		c.bufferMu.Lock()
		key := batchItemKey{serviceID: serviceID, tenantID: tenantID, month: month}
		item, ok := c.buffer[key]
		if !ok {
			item = &batchItemValue{}
			c.buffer[key] = item
		}
		item.promptTokens += promptTokens
		item.completionTokens += completionTokens
		item.cost += cost

		// バッファ中の未反映額をメトリクスに更新
		if c.metrics != nil {
			var totalBuffered float64
			for _, v := range c.buffer {
				totalBuffered += v.cost
			}
			c.metrics.SetBufferedCost(totalBuffered)
		}
		c.bufferMu.Unlock()

		// 残枠キャッシュを無効化
		c.InvalidateTenant(serviceID, tenantID)
		return nil
	}

	// 通常の即時反映
	err := c.underlying.IncrementCost(ctx, serviceID, tenantID, month, promptTokens, completionTokens, cost)
	if err == nil {
		// 残枠キャッシュを無効化
		c.InvalidateTenant(serviceID, tenantID)
	}
	return err
}

func (c *CachedCostStore) ResetCost(ctx context.Context, serviceID, tenantID, month string, cost float64, tokens int64) error {
	err := c.underlying.ResetCost(ctx, serviceID, tenantID, month, cost, tokens)
	if err == nil {
		c.InvalidateTenant(serviceID, tenantID)
		if tenantID == "" {
			c.InvalidateService(serviceID)
		}
	}
	return err
}

func (c *CachedCostStore) SetServiceLimit(ctx context.Context, serviceID string, costLimit float64, billingType string) error {
	err := c.underlying.SetServiceLimit(ctx, serviceID, costLimit, billingType)
	if err == nil {
		c.InvalidateService(serviceID)
	}
	return err
}

func (c *CachedCostStore) SetTenantLimit(ctx context.Context, serviceID, tenantID string, costLimit float64, billingType string) error {
	err := c.underlying.SetTenantLimit(ctx, serviceID, tenantID, costLimit, billingType)
	if err == nil {
		c.InvalidateTenant(serviceID, tenantID)
	}
	return err
}

func (c *CachedCostStore) GetServiceConfig(ctx context.Context, serviceID string) (*entity.ServiceConfig, error) {
	if !c.opts.Enabled || c.opts.ConfigTTL <= 0 {
		return c.underlying.GetServiceConfig(ctx, serviceID)
	}

	if val, ok := c.serviceConfigCache.Load(serviceID); ok {
		entry := val.(serviceConfigEntry)
		if time.Now().UTC().Before(entry.expiresAt) {
			if c.metrics != nil {
				c.metrics.RecordCacheHit("config")
			}
			return entry.cfg, nil
		}
		c.serviceConfigCache.Delete(serviceID)
	}

	if c.metrics != nil {
		c.metrics.RecordCacheMiss("config")
	}

	cfg, err := c.underlying.GetServiceConfig(ctx, serviceID)
	if err == nil && cfg != nil {
		c.serviceConfigCache.Store(serviceID, serviceConfigEntry{
			cfg:       cfg,
			expiresAt: time.Now().UTC().Add(c.opts.ConfigTTL),
		})
	}
	return cfg, err
}

func (c *CachedCostStore) GetTenantConfig(ctx context.Context, serviceID, tenantID string) (*entity.TenantConfig, error) {
	if !c.opts.Enabled || c.opts.ConfigTTL <= 0 {
		return c.underlying.GetTenantConfig(ctx, serviceID, tenantID)
	}

	key := fmt.Sprintf("%s:%s", serviceID, tenantID)
	if val, ok := c.tenantConfigCache.Load(key); ok {
		entry := val.(tenantConfigEntry)
		if time.Now().UTC().Before(entry.expiresAt) {
			if c.metrics != nil {
				c.metrics.RecordCacheHit("config")
			}
			return entry.cfg, nil
		}
		c.tenantConfigCache.Delete(key)
	}

	if c.metrics != nil {
		c.metrics.RecordCacheMiss("config")
	}

	cfg, err := c.underlying.GetTenantConfig(ctx, serviceID, tenantID)
	if err == nil && cfg != nil {
		c.tenantConfigCache.Store(key, tenantConfigEntry{
			cfg:       cfg,
			expiresAt: time.Now().UTC().Add(c.opts.ConfigTTL),
		})
	}
	return cfg, err
}

func (c *CachedCostStore) Ping(ctx context.Context) error {
	return c.underlying.Ping(ctx)
}

// ================= バッチ書き込みフラッシュ =================

func (c *CachedCostStore) startFlushWorker(interval time.Duration) {
	defer c.flushWg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_ = c.Flush(context.Background())
		case <-c.stopChan:
			_ = c.Flush(context.Background())
			return
		}
	}
}

// Flush はバッファにたまっているすべての加算リクエストを underlying ストアに書き込む
func (c *CachedCostStore) Flush(ctx context.Context) error {
	c.bufferMu.Lock()
	if len(c.buffer) == 0 {
		c.bufferMu.Unlock()
		return nil
	}
	items := c.buffer
	c.buffer = make(map[batchItemKey]*batchItemValue)
	if c.metrics != nil {
		c.metrics.SetBufferedCost(0)
		c.metrics.RecordCacheFlush()
	}
	c.bufferMu.Unlock()

	var firstErr error
	for k, v := range items {
		if err := c.underlying.IncrementCost(ctx, k.serviceID, k.tenantID, k.month, v.promptTokens, v.completionTokens, v.cost); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// Close はワーカーを停止し、バッファをフラッシュする (グレースフルシャットダウン用)
func (c *CachedCostStore) Close() error {
	if c.opts.BatchFlushInterval > 0 {
		close(c.stopChan)
		c.flushWg.Wait()
	}
	return c.Flush(context.Background())
}
