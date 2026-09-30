package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/repository"
)

type memoryCostCounter struct {
	cost   float64
	tokens int64
}

// MemoryStore は開発・テスト用の完全インメモリな CostStore / UsageStore 実装
type MemoryStore struct {
	mu             sync.RWMutex
	serviceConfigs map[string]*entity.ServiceConfig
	tenantConfigs  map[string]*entity.TenantConfig
	serviceCosts   map[string]memoryCostCounter // key: serviceID#month
	tenantCosts    map[string]memoryCostCounter // key: serviceID#tenantID#month
	usages         map[string]*entity.TenantMonthlyUsage
	notifications  []*entity.Notification
	locks          map[string]time.Time
}

// NewMemoryStore は新規 MemoryStore を生成する
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		serviceConfigs: make(map[string]*entity.ServiceConfig),
		tenantConfigs:  make(map[string]*entity.TenantConfig),
		serviceCosts:   make(map[string]memoryCostCounter),
		tenantCosts:    make(map[string]memoryCostCounter),
		usages:         make(map[string]*entity.TenantMonthlyUsage),
		notifications:  make([]*entity.Notification, 0),
		locks:          make(map[string]time.Time),
	}
}

var _ repository.CostStore = (*MemoryStore)(nil)
var _ repository.UsageStore = (*MemoryStore)(nil)

// ================= CostStore 実装 =================

func (m *MemoryStore) GetServiceCost(ctx context.Context, serviceID, month string) (float64, int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := serviceID + "#" + month
	c := m.serviceCosts[key]
	return c.cost, c.tokens, nil
}

func (m *MemoryStore) GetTenantCost(ctx context.Context, serviceID, tenantID, month string) (float64, int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := serviceID + "#" + tenantID + "#" + month
	c := m.tenantCosts[key]
	return c.cost, c.tokens, nil
}

func (m *MemoryStore) IncrementCost(ctx context.Context, serviceID, tenantID, month string, promptTokens, completionTokens int64, cost float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	totalTokens := promptTokens + completionTokens

	// サービス合算加算
	svcKey := serviceID + "#" + month
	svcVal := m.serviceCosts[svcKey]
	svcVal.cost += cost
	svcVal.tokens += totalTokens
	m.serviceCosts[svcKey] = svcVal

	// テナント別加算
	if tenantID != "" {
		tKey := serviceID + "#" + tenantID + "#" + month
		tVal := m.tenantCosts[tKey]
		tVal.cost += cost
		tVal.tokens += totalTokens
		m.tenantCosts[tKey] = tVal
	}

	return nil
}

func (m *MemoryStore) ResetCost(ctx context.Context, serviceID, tenantID, month string, cost float64, tokens int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if tenantID == "" {
		svcKey := serviceID + "#" + month
		m.serviceCosts[svcKey] = memoryCostCounter{cost: cost, tokens: tokens}
	} else {
		tKey := serviceID + "#" + tenantID + "#" + month
		m.tenantCosts[tKey] = memoryCostCounter{cost: cost, tokens: tokens}
	}
	return nil
}

func (m *MemoryStore) SetServiceLimit(ctx context.Context, serviceID string, costLimit float64, billingType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg, ok := m.serviceConfigs[serviceID]
	if !ok {
		cfg = &entity.ServiceConfig{
			ServiceID: serviceID,
		}
	}
	cfg.CostLimit = costLimit
	cfg.BillingType = billingType
	cfg.UpdatedAt = time.Now()
	m.serviceConfigs[serviceID] = cfg
	return nil
}

func (m *MemoryStore) SetTenantLimit(ctx context.Context, serviceID, tenantID string, costLimit float64, billingType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := serviceID + "#" + tenantID
	cfg, ok := m.tenantConfigs[key]
	if !ok {
		cfg = &entity.TenantConfig{
			ServiceID: serviceID,
			TenantID:  tenantID,
		}
	}
	cfg.CostLimit = costLimit
	cfg.BillingType = billingType
	cfg.UpdatedAt = time.Now()
	m.tenantConfigs[key] = cfg
	return nil
}

func (m *MemoryStore) GetServiceConfig(ctx context.Context, serviceID string) (*entity.ServiceConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, ok := m.serviceConfigs[serviceID]
	if !ok {
		return nil, nil
	}
	copy := *cfg
	return &copy, nil
}

func (m *MemoryStore) GetTenantConfig(ctx context.Context, serviceID, tenantID string) (*entity.TenantConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := serviceID + "#" + tenantID
	cfg, ok := m.tenantConfigs[key]
	if !ok {
		return nil, nil
	}
	copy := *cfg
	return &copy, nil
}

// ================= UsageStore 実装 =================

func (m *MemoryStore) RecordUsage(ctx context.Context, serviceID, tenantID, month, model string, promptTokens, completionTokens int64, cost float64, pricingVersion string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := serviceID + "#" + tenantID + "#" + month
	usage, ok := m.usages[key]
	if !ok {
		usage = &entity.TenantMonthlyUsage{
			PK:        entity.BuildPK(serviceID, tenantID),
			SK:        entity.BuildSK(month),
			ServiceID: serviceID,
			TenantID:  tenantID,
			Month:     month,
			Models:    make(map[string]*entity.ModelUsage),
		}
		m.usages[key] = usage
	}

	usage.TotalTokens += promptTokens + completionTokens
	usage.TotalCost += cost
	usage.UpdatedAt = time.Now()

	mu, ok := usage.Models[model]
	if !ok {
		mu = &entity.ModelUsage{}
		usage.Models[model] = mu
	}
	mu.PromptTokens += promptTokens
	mu.CompletionTokens += completionTokens
	mu.TotalTokens += promptTokens + completionTokens
	mu.Cost += cost

	return nil
}

func (m *MemoryStore) GetTenantUsage(ctx context.Context, serviceID, tenantID, month string) (*entity.TenantMonthlyUsage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := serviceID + "#" + tenantID + "#" + month
	usage, ok := m.usages[key]
	if !ok {
		return nil, nil
	}
	return usage, nil
}

func (m *MemoryStore) GetServiceMonthlyUsage(ctx context.Context, serviceID, month string) (*entity.ServiceMonthlyReport, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	report := &entity.ServiceMonthlyReport{
		ServiceID:   serviceID,
		Month:       month,
		BillingType: string(entity.BillingTypePAYG),
		Models:      make(map[string]*entity.ServiceReportModel),
		Tenants:     make(map[string]*entity.TenantReportItem),
	}

	if cfg, ok := m.serviceConfigs[serviceID]; ok {
		report.CostLimit = cfg.CostLimit
		report.BillingType = string(cfg.EffectiveBillingType())
	}

	prefix := serviceID + "#"
	for k, usage := range m.usages {
		if strings.HasPrefix(k, prefix) && usage.Month == month {
			report.TotalTokens += usage.TotalTokens
			report.TotalCostUSD += usage.TotalCost

			tItem, ok := report.Tenants[usage.TenantID]
			if !ok {
				tItem = &entity.TenantReportItem{TenantID: usage.TenantID}
				report.Tenants[usage.TenantID] = tItem
			}
			tItem.TotalTokens += usage.TotalTokens
			tItem.TotalCostUSD += usage.TotalCost

			for modelName, mu := range usage.Models {
				mItem, ok := report.Models[modelName]
				if !ok {
					mItem = &entity.ServiceReportModel{}
					report.Models[modelName] = mItem
				}
				mItem.Tokens += mu.TotalTokens
				mItem.CostUSD += mu.Cost
			}
		}
	}

	return report, nil
}

func (m *MemoryStore) GetAllTenantsUsageByMonth(ctx context.Context, month string) ([]*entity.TenantMonthlyUsage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*entity.TenantMonthlyUsage
	for _, usage := range m.usages {
		if usage.Month == month {
			result = append(result, usage)
		}
	}
	return result, nil
}

func (m *MemoryStore) AcquireLock(ctx context.Context, lockKey string, ttlSeconds int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	if exp, ok := m.locks[lockKey]; ok {
		if now.Before(exp) {
			return false, nil // ロック保持中
		}
	}

	m.locks[lockKey] = now.Add(time.Duration(ttlSeconds) * time.Second)
	return true, nil
}

func (m *MemoryStore) ReleaseLock(ctx context.Context, lockKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.locks, lockKey)
	return nil
}

func (m *MemoryStore) SaveNotification(ctx context.Context, ntf *entity.Notification) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notifications = append(m.notifications, ntf)
	return nil
}

func (m *MemoryStore) ListNotifications(ctx context.Context, limit int) ([]*entity.Notification, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	n := len(m.notifications)
	if n == 0 {
		return []*entity.Notification{}, nil
	}

	// 新しい順にソートして返却
	copied := make([]*entity.Notification, n)
	copy(copied, m.notifications)
	sort.Slice(copied, func(i, j int) bool {
		return copied[i].CreatedAt.After(copied[j].CreatedAt)
	})

	if limit > 0 && limit < n {
		copied = copied[:limit]
	}
	return copied, nil
}

func (m *MemoryStore) Ping(ctx context.Context) error {
	return nil
}
