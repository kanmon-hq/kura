package valkey

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/repository"
	"github.com/redis/go-redis/v9"
)

// ValkeyCostStore は Valkey / Redis をバックエンドとする高パフォーマンスな CostStore 実装
type ValkeyCostStore struct {
	client *redis.Client
}

var _ repository.CostStore = (*ValkeyCostStore)(nil)

// NewValkeyCostStore は Redis/Valkey URL から CostStore を生成する
func NewValkeyCostStore(rawURL string) (*ValkeyCostStore, error) {
	// valkey:// スキームを redis:// に正規化
	if strings.HasPrefix(rawURL, "valkey://") {
		rawURL = "redis://" + strings.TrimPrefix(rawURL, "valkey://")
	}

	opt, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid valkey/redis url: %w", err)
	}

	client := redis.NewClient(opt)
	return &ValkeyCostStore{client: client}, nil
}

// NewValkeyCostStoreWithClient は既存の redis.Client を使用して CostStore を生成する
func NewValkeyCostStoreWithClient(client *redis.Client) *ValkeyCostStore {
	return &ValkeyCostStore{client: client}
}

func (s *ValkeyCostStore) serviceCostKey(serviceID, month string) string {
	return fmt.Sprintf("{kura:svc:%s}:cost:%s", serviceID, month)
}

func (s *ValkeyCostStore) tenantCostKey(serviceID, tenantID, month string) string {
	return fmt.Sprintf("{kura:svc:%s}:tenant:%s:cost:%s", serviceID, tenantID, month)
}

func (s *ValkeyCostStore) serviceConfigKey(serviceID string) string {
	return fmt.Sprintf("{kura:svc:%s}:config", serviceID)
}

func (s *ValkeyCostStore) tenantConfigKey(serviceID, tenantID string) string {
	return fmt.Sprintf("{kura:svc:%s}:tenant:%s:config", serviceID, tenantID)
}

func (s *ValkeyCostStore) GetServiceCost(ctx context.Context, serviceID, month string) (float64, int64, error) {
	key := s.serviceCostKey(serviceID, month)
	res, err := s.client.HMGet(ctx, key, "cost", "tokens").Result()
	if err != nil {
		if err == redis.Nil {
			return 0, 0, nil
		}
		return 0, 0, err
	}

	var cost float64
	var tokens int64

	if res[0] != nil {
		if str, ok := res[0].(string); ok {
			cost, _ = strconv.ParseFloat(str, 64)
		}
	}
	if res[1] != nil {
		if str, ok := res[1].(string); ok {
			tokens, _ = strconv.ParseInt(str, 10, 64)
		}
	}

	return cost, tokens, nil
}

func (s *ValkeyCostStore) GetTenantCost(ctx context.Context, serviceID, tenantID, month string) (float64, int64, error) {
	key := s.tenantCostKey(serviceID, tenantID, month)
	res, err := s.client.HMGet(ctx, key, "cost", "tokens").Result()
	if err != nil {
		if err == redis.Nil {
			return 0, 0, nil
		}
		return 0, 0, err
	}

	var cost float64
	var tokens int64

	if res[0] != nil {
		if str, ok := res[0].(string); ok {
			cost, _ = strconv.ParseFloat(str, 64)
		}
	}
	if res[1] != nil {
		if str, ok := res[1].(string); ok {
			tokens, _ = strconv.ParseInt(str, 10, 64)
		}
	}

	return cost, tokens, nil
}

func (s *ValkeyCostStore) IncrementCost(ctx context.Context, serviceID, tenantID, month string, promptTokens, completionTokens int64, cost float64) error {
	svcKey := s.serviceCostKey(serviceID, month)
	totalTokens := promptTokens + completionTokens

	pipe := s.client.TxPipeline()
	pipe.HIncrByFloat(ctx, svcKey, "cost", cost)
	pipe.HIncrBy(ctx, svcKey, "tokens", totalTokens)
	// 月次キーの有効期限を60日に設定 (TTL自動クリーンアップ)
	pipe.Expire(ctx, svcKey, 60*24*time.Hour)

	if tenantID != "" {
		tenantKey := s.tenantCostKey(serviceID, tenantID, month)
		pipe.HIncrByFloat(ctx, tenantKey, "cost", cost)
		pipe.HIncrBy(ctx, tenantKey, "tokens", totalTokens)
		pipe.Expire(ctx, tenantKey, 60*24*time.Hour)
	}

	_, err := pipe.Exec(ctx)
	return err
}

func (s *ValkeyCostStore) ResetCost(ctx context.Context, serviceID, tenantID, month string, cost float64, tokens int64) error {
	var key string
	if tenantID == "" {
		key = s.serviceCostKey(serviceID, month)
	} else {
		key = s.tenantCostKey(serviceID, tenantID, month)
	}

	pipe := s.client.TxPipeline()
	pipe.HSet(ctx, key, map[string]interface{}{
		"cost":   fmt.Sprintf("%.6f", cost),
		"tokens": strconv.FormatInt(tokens, 10),
	})
	pipe.Expire(ctx, key, 60*24*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *ValkeyCostStore) SetServiceLimit(ctx context.Context, serviceID string, costLimit float64, billingType string) error {
	key := s.serviceConfigKey(serviceID)
	data := map[string]interface{}{
		"service_id":   serviceID,
		"cost_limit":   fmt.Sprintf("%.2f", costLimit),
		"billing_type": billingType,
		"updated_at":   time.Now().Format(time.RFC3339),
	}
	return s.client.HSet(ctx, key, data).Err()
}

func (s *ValkeyCostStore) SetTenantLimit(ctx context.Context, serviceID, tenantID string, costLimit float64, billingType string) error {
	key := s.tenantConfigKey(serviceID, tenantID)
	data := map[string]interface{}{
		"service_id":   serviceID,
		"tenant_id":    tenantID,
		"cost_limit":   fmt.Sprintf("%.2f", costLimit),
		"billing_type": billingType,
		"updated_at":   time.Now().Format(time.RFC3339),
	}
	return s.client.HSet(ctx, key, data).Err()
}

func (s *ValkeyCostStore) GetServiceConfig(ctx context.Context, serviceID string) (*entity.ServiceConfig, error) {
	key := s.serviceConfigKey(serviceID)
	vals, err := s.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if len(vals) == 0 {
		return nil, nil
	}

	costLimit, _ := strconv.ParseFloat(vals["cost_limit"], 64)
	billingType := vals["billing_type"]
	updatedAt, _ := time.Parse(time.RFC3339, vals["updated_at"])

	return &entity.ServiceConfig{
		ServiceID:   serviceID,
		CostLimit:   costLimit,
		BillingType: billingType,
		UpdatedAt:   updatedAt,
	}, nil
}

func (s *ValkeyCostStore) GetTenantConfig(ctx context.Context, serviceID, tenantID string) (*entity.TenantConfig, error) {
	key := s.tenantConfigKey(serviceID, tenantID)
	vals, err := s.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if len(vals) == 0 {
		return nil, nil
	}

	costLimit, _ := strconv.ParseFloat(vals["cost_limit"], 64)
	billingType := vals["billing_type"]
	updatedAt, _ := time.Parse(time.RFC3339, vals["updated_at"])

	return &entity.TenantConfig{
		ServiceID:   serviceID,
		TenantID:    tenantID,
		CostLimit:   costLimit,
		BillingType: billingType,
		UpdatedAt:   updatedAt,
	}, nil
}

func (s *ValkeyCostStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}
