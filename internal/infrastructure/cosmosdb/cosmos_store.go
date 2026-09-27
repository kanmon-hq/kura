package cosmosdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/northfieldzz/kura/internal/domain/entity"
	"github.com/northfieldzz/kura/internal/domain/repository"
)

// CosmosStore は Azure Cosmos DB (NoSQL API) をバックエンドとする QuotaRepository (CostStore / UsageStore) 実装
type CosmosStore struct {
	client        *azcosmos.Client
	container     *azcosmos.ContainerClient
	databaseName  string
	containerName string
	mu            sync.RWMutex
}

var _ repository.CostStore = (*CosmosStore)(nil)
var _ repository.UsageStore = (*CosmosStore)(nil)
var _ repository.QuotaRepository = (*CosmosStore)(nil)

// ItemDoc は Cosmos DB コンテナ内の統一ドキュメント構造
type ItemDoc struct {
	ID                string                        `json:"id"`
	PK                string                        `json:"pk"`
	Type              string                        `json:"type"` // "service_config", "tenant_config", "cost_counter", "tenant_usage", "lock", "notification"
	ServiceID         string                        `json:"service_id,omitempty"`
	TenantID          string                        `json:"tenant_id,omitempty"`
	Month             string                        `json:"month,omitempty"`
	BillingType       string                        `json:"billing_type,omitempty"`
	CostLimit         float64                       `json:"cost_limit,omitempty"`
	TotalTokens       int64                         `json:"total_tokens,omitempty"`
	TotalCost         float64                       `json:"total_cost,omitempty"`
	TotalCostMicroUSD int64                         `json:"total_cost_micro_usd,omitempty"`
	Models            map[string]*entity.ModelUsage `json:"models,omitempty"`
	ExpiresAt         int64                         `json:"expires_at,omitempty"`
	Title             string                        `json:"title,omitempty"`
	Message           string                        `json:"message,omitempty"`
	IsAlert           bool                          `json:"is_alert,omitempty"`
	CreatedAt         int64                         `json:"created_at,omitempty"`
	UpdatedAt         int64                         `json:"updated_at,omitempty"`
	TTL               int32                         `json:"ttl,omitempty"` // Cosmos DB TTL
}

// CostToMicroUSD は float64 の USD 費用をマイクロ USD (int64) に変換する
func CostToMicroUSD(cost float64) int64 {
	return int64(cost*1000000.0 + 0.5)
}

// MicroUSDToCost はマイクロ USD (int64) を float64 の USD 費用に変換する
func MicroUSDToCost(micro int64) float64 {
	return float64(micro) / 1000000.0
}

// NewCosmosStore は接続情報から CosmosStore を生成する
func NewCosmosStore(endpoint, key, connectionString, databaseName, containerName string) (*CosmosStore, error) {
	if databaseName == "" {
		databaseName = "kura"
	}
	if containerName == "" {
		containerName = "usage"
	}

	var client *azcosmos.Client
	var err error

	if connectionString != "" {
		client, err = azcosmos.NewClientFromConnectionString(connectionString, nil)
	} else if endpoint != "" && key != "" {
		cred, credErr := azcosmos.NewKeyCredential(key)
		if credErr != nil {
			return nil, fmt.Errorf("failed to create Cosmos DB key credential: %w", credErr)
		}
		client, err = azcosmos.NewClientWithKey(endpoint, cred, nil)
	} else {
		return nil, fmt.Errorf("either COSMOSDB_CONNECTION_STRING or (COSMOSDB_ENDPOINT and COSMOSDB_KEY) must be provided")
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create Cosmos DB client: %w", err)
	}

	container, err := client.NewContainer(databaseName, containerName)
	if err != nil {
		return nil, fmt.Errorf("failed to get Cosmos DB container client: %w", err)
	}

	store := &CosmosStore{
		client:        client,
		container:     container,
		databaseName:  databaseName,
		containerName: containerName,
	}

	log.Printf("[INFO] [STORAGE] Initialized Azure Cosmos DB Store (Database: %s, Container: %s)", databaseName, containerName)
	return store, nil
}

// NewCosmosStoreWithClient は既存の azcosmos クライアントから CosmosStore を生成する (テスト用)
func NewCosmosStoreWithClient(client *azcosmos.Client, databaseName, containerName string) (*CosmosStore, error) {
	container, err := client.NewContainer(databaseName, containerName)
	if err != nil {
		return nil, err
	}
	return &CosmosStore{
		client:        client,
		container:     container,
		databaseName:  databaseName,
		containerName: containerName,
	}, nil
}

// Ping はストアへの疎通健全性を確認する
func (s *CosmosStore) Ping(ctx context.Context) error {
	pk := azcosmos.NewPartitionKeyString("PING")
	_, err := s.container.ReadItem(ctx, pk, "PING", nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == 404 {
			return nil // 疎通成功 (アイテム不在)
		}
		return err
	}
	return nil
}

// --- CostStore Methods ---

func (s *CosmosStore) GetServiceCost(ctx context.Context, serviceID, month string) (float64, int64, error) {
	pkStr := fmt.Sprintf("COUNTER#%s", serviceID)
	pk := azcosmos.NewPartitionKeyString(pkStr)
	id := fmt.Sprintf("SVC#%s", month)

	resp, err := s.container.ReadItem(ctx, pk, id, nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == 404 {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("failed to read service cost: %w", err)
	}

	var item ItemDoc
	if err := json.Unmarshal(resp.Value, &item); err != nil {
		return 0, 0, fmt.Errorf("failed to unmarshal service cost: %w", err)
	}
	cost := item.TotalCost
	if item.TotalCostMicroUSD > 0 || (cost == 0 && item.TotalCostMicroUSD != 0) {
		cost = MicroUSDToCost(item.TotalCostMicroUSD)
	}
	return cost, item.TotalTokens, nil
}

func (s *CosmosStore) GetTenantCost(ctx context.Context, serviceID, tenantID, month string) (float64, int64, error) {
	pkStr := fmt.Sprintf("COUNTER#%s", serviceID)
	pk := azcosmos.NewPartitionKeyString(pkStr)
	id := fmt.Sprintf("TENANT#%s#%s", tenantID, month)

	resp, err := s.container.ReadItem(ctx, pk, id, nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == 404 {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("failed to read tenant cost: %w", err)
	}

	var item ItemDoc
	if err := json.Unmarshal(resp.Value, &item); err != nil {
		return 0, 0, fmt.Errorf("failed to unmarshal tenant cost: %w", err)
	}
	cost := item.TotalCost
	if item.TotalCostMicroUSD > 0 || (cost == 0 && item.TotalCostMicroUSD != 0) {
		cost = MicroUSDToCost(item.TotalCostMicroUSD)
	}
	return cost, item.TotalTokens, nil
}

func (s *CosmosStore) IncrementCost(ctx context.Context, serviceID, tenantID, month string, promptTokens, completionTokens int64, cost float64) error {
	totalTokens := promptTokens + completionTokens
	costMicro := CostToMicroUSD(cost)
	pkStr := fmt.Sprintf("COUNTER#%s", serviceID)
	pk := azcosmos.NewPartitionKeyString(pkStr)

	// 1. テナント別カウンターのアトミック更新 (PatchOperations)
	tenantDocID := fmt.Sprintf("TENANT#%s#%s", tenantID, month)
	if err := s.patchOrUpsertCounter(ctx, pk, pkStr, tenantDocID, "tenant_counter", serviceID, tenantID, month, totalTokens, cost, costMicro); err != nil {
		return fmt.Errorf("failed to increment tenant cost: %w", err)
	}

	// 2. サービス全体カウンターのアトミック更新
	serviceDocID := fmt.Sprintf("SVC#%s", month)
	if err := s.patchOrUpsertCounter(ctx, pk, pkStr, serviceDocID, "service_counter", serviceID, "", month, totalTokens, cost, costMicro); err != nil {
		return fmt.Errorf("failed to increment service cost: %w", err)
	}

	return nil
}

func (s *CosmosStore) patchOrUpsertCounter(ctx context.Context, pk azcosmos.PartitionKey, pkStr, id, itemType, serviceID, tenantID, month string, tokens int64, cost float64, costMicro int64) error {
	now := time.Now().Unix()
	patchOps := azcosmos.PatchOperations{}
	patchOps.AppendIncrement("/total_tokens", tokens)
	patchOps.AppendIncrement("/total_cost_micro_usd", costMicro)
	patchOps.AppendSet("/updated_at", now)

	_, err := s.container.PatchItem(ctx, pk, id, patchOps, nil)
	if err == nil {
		return nil
	}

	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) && respErr.StatusCode == 404 {
		// 初回レコード作成 (Upsert)
		item := ItemDoc{
			ID:                id,
			PK:                pkStr,
			Type:              itemType,
			ServiceID:         serviceID,
			TenantID:          tenantID,
			Month:             month,
			TotalTokens:       tokens,
			TotalCost:         cost,
			TotalCostMicroUSD: costMicro,
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		data, marshalErr := json.Marshal(item)
		if marshalErr != nil {
			return marshalErr
		}
		_, createErr := s.container.UpsertItem(ctx, pk, data, nil)
		return createErr
	}
	return err
}

func (s *CosmosStore) ResetCost(ctx context.Context, serviceID, tenantID, month string, cost float64, tokens int64) error {
	pkStr := fmt.Sprintf("COUNTER#%s", serviceID)
	pk := azcosmos.NewPartitionKeyString(pkStr)
	id := fmt.Sprintf("TENANT#%s#%s", tenantID, month)
	now := time.Now().Unix()

	item := ItemDoc{
		ID:                id,
		PK:                pkStr,
		Type:              "tenant_counter",
		ServiceID:         serviceID,
		TenantID:          tenantID,
		Month:             month,
		TotalTokens:       tokens,
		TotalCost:         cost,
		TotalCostMicroUSD: CostToMicroUSD(cost),
		UpdatedAt:         now,
	}
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = s.container.UpsertItem(ctx, pk, data, nil)
	return err
}

func (s *CosmosStore) SetServiceLimit(ctx context.Context, serviceID string, costLimit float64, billingType string) error {
	cfg, err := s.GetServiceConfig(ctx, serviceID)
	if err != nil || cfg == nil {
		cfg = &entity.ServiceConfig{
			ServiceID:   serviceID,
			BillingType: billingType,
			CostLimit:   costLimit,
			UpdatedAt:   time.Now(),
		}
	} else {
		cfg.CostLimit = costLimit
		cfg.BillingType = billingType
		cfg.UpdatedAt = time.Now()
	}
	return s.SetServiceConfig(ctx, cfg)
}

func (s *CosmosStore) SetTenantLimit(ctx context.Context, serviceID, tenantID string, costLimit float64, billingType string) error {
	cfg, err := s.GetTenantConfig(ctx, serviceID, tenantID)
	if err != nil || cfg == nil {
		cfg = &entity.TenantConfig{
			ServiceID:   serviceID,
			TenantID:    tenantID,
			BillingType: billingType,
			CostLimit:   costLimit,
			UpdatedAt:   time.Now(),
		}
	} else {
		cfg.CostLimit = costLimit
		cfg.BillingType = billingType
		cfg.UpdatedAt = time.Now()
	}
	return s.SetTenantConfig(ctx, cfg)
}

func (s *CosmosStore) GetServiceConfig(ctx context.Context, serviceID string) (*entity.ServiceConfig, error) {
	pkStr := fmt.Sprintf("CONFIG#%s", serviceID)
	pk := azcosmos.NewPartitionKeyString(pkStr)
	id := "SVC"

	resp, err := s.container.ReadItem(ctx, pk, id, nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == 404 {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get service config: %w", err)
	}

	var item ItemDoc
	if err := json.Unmarshal(resp.Value, &item); err != nil {
		return nil, err
	}
	return &entity.ServiceConfig{
		ServiceID:   item.ServiceID,
		BillingType: item.BillingType,
		CostLimit:   item.CostLimit,
		UpdatedAt:   time.Unix(item.UpdatedAt, 0),
	}, nil
}

func (s *CosmosStore) GetTenantConfig(ctx context.Context, serviceID, tenantID string) (*entity.TenantConfig, error) {
	pkStr := fmt.Sprintf("CONFIG#%s", serviceID)
	pk := azcosmos.NewPartitionKeyString(pkStr)
	id := tenantID

	resp, err := s.container.ReadItem(ctx, pk, id, nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == 404 {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get tenant config: %w", err)
	}

	var item ItemDoc
	if err := json.Unmarshal(resp.Value, &item); err != nil {
		return nil, err
	}
	return &entity.TenantConfig{
		ServiceID:   item.ServiceID,
		TenantID:    item.TenantID,
		BillingType: item.BillingType,
		CostLimit:   item.CostLimit,
		UpdatedAt:   time.Unix(item.UpdatedAt, 0),
	}, nil
}

func (s *CosmosStore) SetServiceConfig(ctx context.Context, cfg *entity.ServiceConfig) error {
	pkStr := fmt.Sprintf("CONFIG#%s", cfg.ServiceID)
	pk := azcosmos.NewPartitionKeyString(pkStr)
	id := "SVC"

	item := ItemDoc{
		ID:          id,
		PK:          pkStr,
		Type:        "service_config",
		ServiceID:   cfg.ServiceID,
		BillingType: cfg.BillingType,
		CostLimit:   cfg.CostLimit,
		UpdatedAt:   cfg.UpdatedAt.Unix(),
	}
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = s.container.UpsertItem(ctx, pk, data, nil)
	return err
}

func (s *CosmosStore) SetTenantConfig(ctx context.Context, cfg *entity.TenantConfig) error {
	pkStr := fmt.Sprintf("CONFIG#%s", cfg.ServiceID)
	pk := azcosmos.NewPartitionKeyString(pkStr)
	id := cfg.TenantID

	item := ItemDoc{
		ID:          id,
		PK:          pkStr,
		Type:        "tenant_config",
		ServiceID:   cfg.ServiceID,
		TenantID:    cfg.TenantID,
		BillingType: cfg.BillingType,
		CostLimit:   cfg.CostLimit,
		UpdatedAt:   cfg.UpdatedAt.Unix(),
	}
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = s.container.UpsertItem(ctx, pk, data, nil)
	return err
}

// --- UsageStore Methods ---

func (s *CosmosStore) RecordUsage(ctx context.Context, serviceID, tenantID, month, model string, promptTokens, completionTokens int64, cost float64, pricingVersion string) error {
	pkStr := fmt.Sprintf("USAGE#%s", serviceID)
	pk := azcosmos.NewPartitionKeyString(pkStr)
	id := fmt.Sprintf("%s#%s", tenantID, month)
	totalTokens := promptTokens + completionTokens
	now := time.Now().Unix()

	s.mu.Lock()
	defer s.mu.Unlock()

	// 既存レコードの読み出し (存在しなければ初期化)
	var item ItemDoc
	resp, err := s.container.ReadItem(ctx, pk, id, nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == 404 {
			item = ItemDoc{
				ID:          id,
				PK:          pkStr,
				Type:        "tenant_usage",
				ServiceID:   serviceID,
				TenantID:    tenantID,
				Month:       month,
				TotalTokens: 0,
				TotalCost:   0,
				Models:      make(map[string]*entity.ModelUsage),
				CreatedAt:   now,
			}
		} else {
			return fmt.Errorf("failed to read tenant usage record: %w", err)
		}
	} else {
		if err := json.Unmarshal(resp.Value, &item); err != nil {
			return fmt.Errorf("failed to unmarshal tenant usage record: %w", err)
		}
	}

	if item.Models == nil {
		item.Models = make(map[string]*entity.ModelUsage)
	}

	// モデル別集計
	mu, ok := item.Models[model]
	if !ok || mu == nil {
		mu = &entity.ModelUsage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
			Cost:             0,
		}
		item.Models[model] = mu
	}

	mu.PromptTokens += promptTokens
	mu.CompletionTokens += completionTokens
	mu.TotalTokens += totalTokens
	mu.Cost += cost

	item.TotalTokens += totalTokens
	item.TotalCost += cost
	item.UpdatedAt = now

	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = s.container.UpsertItem(ctx, pk, data, nil)
	return err
}

func (s *CosmosStore) IncrementTenantUsage(ctx context.Context, serviceID, tenantID, month, model string, promptTokens, completionTokens int64, cost float64) error {
	return s.RecordUsage(ctx, serviceID, tenantID, month, model, promptTokens, completionTokens, cost, "")
}

func (s *CosmosStore) GetTenantUsage(ctx context.Context, serviceID, tenantID, month string) (*entity.TenantMonthlyUsage, error) {
	pkStr := fmt.Sprintf("USAGE#%s", serviceID)
	pk := azcosmos.NewPartitionKeyString(pkStr)
	id := fmt.Sprintf("%s#%s", tenantID, month)

	resp, err := s.container.ReadItem(ctx, pk, id, nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == 404 {
			return &entity.TenantMonthlyUsage{
				ServiceID:   serviceID,
				TenantID:    tenantID,
				Month:       month,
				TotalTokens: 0,
				TotalCost:   0,
				Models:      make(map[string]*entity.ModelUsage),
			}, nil
		}
		return nil, fmt.Errorf("failed to get tenant usage: %w", err)
	}

	var item ItemDoc
	if err := json.Unmarshal(resp.Value, &item); err != nil {
		return nil, err
	}

	if item.Models == nil {
		item.Models = make(map[string]*entity.ModelUsage)
	}

	return &entity.TenantMonthlyUsage{
		ServiceID:   item.ServiceID,
		TenantID:    item.TenantID,
		Month:       item.Month,
		TotalTokens: item.TotalTokens,
		TotalCost:   item.TotalCost,
		Models:      item.Models,
	}, nil
}

func (s *CosmosStore) GetServiceMonthlyUsage(ctx context.Context, serviceID, month string) (*entity.ServiceMonthlyReport, error) {
	pkStr := fmt.Sprintf("USAGE#%s", serviceID)
	pk := azcosmos.NewPartitionKeyString(pkStr)

	query := "SELECT * FROM c WHERE c.type = 'tenant_usage' AND c.month = @month"
	opt := &azcosmos.QueryOptions{
		QueryParameters: []azcosmos.QueryParameter{
			{Name: "@month", Value: month},
		},
	}

	report := &entity.ServiceMonthlyReport{
		ServiceID:   serviceID,
		Month:       month,
		BillingType: string(entity.BillingTypePAYG),
		Models:      make(map[string]*entity.ServiceReportModel),
		Tenants:     make(map[string]*entity.TenantReportItem),
	}

	if cfg, _ := s.GetServiceConfig(ctx, serviceID); cfg != nil {
		report.CostLimit = cfg.CostLimit
		report.BillingType = string(cfg.EffectiveBillingType())
	}

	pager := s.container.NewQueryItemsPager(query, pk, opt)
	actualModels := make(map[string]*entity.ServiceReportModel)

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to query service usage page: %w", err)
		}
		for _, bytes := range page.Items {
			var item ItemDoc
			if err := json.Unmarshal(bytes, &item); err != nil {
				continue
			}
			report.TotalTokens += item.TotalTokens
			report.TotalCostUSD += item.TotalCost

			if item.TenantID != "" {
				report.Tenants[item.TenantID] = &entity.TenantReportItem{
					TenantID:     item.TenantID,
					TotalTokens:  item.TotalTokens,
					TotalCostUSD: item.TotalCost,
				}
			}

			for mName, mu := range item.Models {
				rMu, ok := actualModels[mName]
				if !ok || rMu == nil {
					rMu = &entity.ServiceReportModel{}
					actualModels[mName] = rMu
				}
				rMu.Tokens += mu.TotalTokens
				rMu.CostUSD += mu.Cost
			}
		}
	}

	report.Models = actualModels
	return report, nil
}

func (s *CosmosStore) GetAllTenantsUsageByMonth(ctx context.Context, month string) ([]*entity.TenantMonthlyUsage, error) {
	query := "SELECT * FROM c WHERE c.type = 'tenant_usage' AND c.month = @month"
	opt := &azcosmos.QueryOptions{
		QueryParameters: []azcosmos.QueryParameter{
			{Name: "@month", Value: month},
		},
	}

	// クロスパーティションクエリ
	pager := s.container.NewQueryItemsPager(query, azcosmos.PartitionKey{}, opt)
	var list []*entity.TenantMonthlyUsage

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to query all tenants usage: %w", err)
		}
		for _, bytes := range page.Items {
			var item ItemDoc
			if err := json.Unmarshal(bytes, &item); err != nil {
				continue
			}
			list = append(list, &entity.TenantMonthlyUsage{
				ServiceID:   item.ServiceID,
				TenantID:    item.TenantID,
				Month:       item.Month,
				TotalTokens: item.TotalTokens,
				TotalCost:   item.TotalCost,
				Models:      item.Models,
			})
		}
	}
	return list, nil
}

// --- Distributed Lock Methods ---

func (s *CosmosStore) AcquireLock(ctx context.Context, lockKey string, ttlSeconds int64) (bool, error) {
	pkStr := "LOCK"
	pk := azcosmos.NewPartitionKeyString(pkStr)
	now := time.Now().Unix()
	expiresAt := now + ttlSeconds

	// 1. 既存ロックの確認
	resp, err := s.container.ReadItem(ctx, pk, lockKey, nil)
	if err == nil {
		var item ItemDoc
		if jsonErr := json.Unmarshal(resp.Value, &item); jsonErr == nil {
			if item.ExpiresAt > now {
				return false, nil // 有効なロックが存在
			}
		}
	} else {
		var respErr *azcore.ResponseError
		if !errors.As(err, &respErr) || respErr.StatusCode != 404 {
			return false, fmt.Errorf("failed to check lock status: %w", err)
		}
	}

	// 2. ロックの獲得 / 更新 (CreateItem または ETag付き更新)
	lockDoc := ItemDoc{
		ID:        lockKey,
		PK:        pkStr,
		Type:      "lock",
		ExpiresAt: expiresAt,
		UpdatedAt: now,
		TTL:       int32(ttlSeconds + 60),
	}
	data, err := json.Marshal(lockDoc)
	if err != nil {
		return false, err
	}

	_, err = s.container.UpsertItem(ctx, pk, data, nil)
	if err != nil {
		return false, fmt.Errorf("failed to acquire lock: %w", err)
	}
	return true, nil
}

func (s *CosmosStore) ReleaseLock(ctx context.Context, lockKey string) error {
	pkStr := "LOCK"
	pk := azcosmos.NewPartitionKeyString(pkStr)
	_, err := s.container.DeleteItem(ctx, pk, lockKey, nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == 404 {
			return nil
		}
		return fmt.Errorf("failed to release lock: %w", err)
	}
	return nil
}

// --- Notification Methods ---

func (s *CosmosStore) SaveNotification(ctx context.Context, ntf *entity.Notification) error {
	pkStr := "NOTIF"
	pk := azcosmos.NewPartitionKeyString(pkStr)

	item := ItemDoc{
		ID:        ntf.ID,
		PK:        pkStr,
		Type:      "notification",
		Title:     ntf.Title,
		Message:   ntf.Message,
		IsAlert:   ntf.IsAlert,
		CreatedAt: ntf.CreatedAt.Unix(),
	}
	data, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = s.container.UpsertItem(ctx, pk, data, nil)
	return err
}

func (s *CosmosStore) ListNotifications(ctx context.Context, limit int) ([]*entity.Notification, error) {
	pkStr := "NOTIF"
	pk := azcosmos.NewPartitionKeyString(pkStr)

	query := "SELECT * FROM c WHERE c.type = 'notification' ORDER BY c.created_at DESC"
	pager := s.container.NewQueryItemsPager(query, pk, nil)

	var list []*entity.Notification
	for pager.More() && len(list) < limit {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list notifications: %w", err)
		}
		for _, bytes := range page.Items {
			var item ItemDoc
			if err := json.Unmarshal(bytes, &item); err != nil {
				continue
			}
			ntfType := entity.NotificationTypeAlert
			if !item.IsAlert {
				ntfType = entity.NotificationTypeReport
			}
			list = append(list, &entity.Notification{
				ID:        item.ID,
				Type:      ntfType,
				Title:     item.Title,
				Message:   item.Message,
				IsAlert:   item.IsAlert,
				CreatedAt: time.Unix(item.CreatedAt, 0),
			})
			if len(list) >= limit {
				break
			}
		}
	}

	// メモリソート (Cosmos DB 上のインデックス順序補正用)
	sort.Slice(list, func(i, j int) bool {
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})

	return list, nil
}
