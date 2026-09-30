package firestore

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/repository"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FirestoreStore は Google Cloud Firestore をバックエンドとする QuotaRepository (CostStore / UsageStore) 実装
type FirestoreStore struct {
	client     *firestore.Client
	projectID  string
	databaseID string
}

var _ repository.CostStore = (*FirestoreStore)(nil)
var _ repository.UsageStore = (*FirestoreStore)(nil)
var _ repository.QuotaRepository = (*FirestoreStore)(nil)

const (
	colServiceConfigs = "kura_service_configs"
	colTenantConfigs  = "kura_tenant_configs"
	colCostCounters   = "kura_cost_counters"
	colTenantUsage    = "kura_tenant_usage"
	colLocks          = "kura_locks"
	colNotifications  = "kura_notifications"
)

// NewFirestoreStore は GCP プロジェクトIDおよびデータベースIDから FirestoreStore を初期化する
func NewFirestoreStore(ctx context.Context, projectID, databaseID string) (*FirestoreStore, error) {
	if projectID == "" {
		projectID = firestore.DetectProjectID
	}
	if databaseID == "" {
		databaseID = "(default)"
	}

	var client *firestore.Client
	var err error

	if databaseID == "(default)" {
		client, err = firestore.NewClient(ctx, projectID)
	} else {
		client, err = firestore.NewClientWithDatabase(ctx, projectID, databaseID)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create Firestore client (project=%s, db=%s): %w", projectID, databaseID, err)
	}

	store := &FirestoreStore{
		client:     client,
		projectID:  projectID,
		databaseID: databaseID,
	}

	log.Printf("[INFO] [STORAGE] Initialized Google Cloud Firestore Store (Project: %s, Database: %s)", projectID, databaseID)
	return store, nil
}

// NewFirestoreStoreWithClient は既存の *firestore.Client をラップして FirestoreStore を生成する (テスト用)
func NewFirestoreStoreWithClient(client *firestore.Client, projectID, databaseID string) *FirestoreStore {
	return &FirestoreStore{
		client:     client,
		projectID:  projectID,
		databaseID: databaseID,
	}
}

// Close は Firestore クライアントの接続を閉じる
func (s *FirestoreStore) Close() error {
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}

// Ping は Firestore への疎通健全性を確認する
func (s *FirestoreStore) Ping(ctx context.Context) error {
	_, err := s.client.Collection(colCostCounters).Doc("PING").Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil
		}
		return err
	}
	return nil
}

// --- CostStore Methods ---

func (s *FirestoreStore) GetServiceCost(ctx context.Context, serviceID, month string) (float64, int64, error) {
	docID := fmt.Sprintf("%s_SVC_%s", serviceID, month)
	doc, err := s.client.Collection(colCostCounters).Doc(docID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("failed to get service cost from firestore: %w", err)
	}

	data := doc.Data()
	var totalCost float64
	var totalTokens int64

	if v, ok := data["total_cost_micro_usd"].(int64); ok && v != 0 {
		totalCost = float64(v) / 1000000.0
	} else if v, ok := data["total_cost"].(float64); ok {
		totalCost = v
	}

	if v, ok := data["total_tokens"].(int64); ok {
		totalTokens = v
	}

	return totalCost, totalTokens, nil
}

func (s *FirestoreStore) GetTenantCost(ctx context.Context, serviceID, tenantID, month string) (float64, int64, error) {
	docID := fmt.Sprintf("%s_%s_%s", serviceID, tenantID, month)
	doc, err := s.client.Collection(colCostCounters).Doc(docID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("failed to get tenant cost from firestore: %w", err)
	}

	data := doc.Data()
	var totalCost float64
	var totalTokens int64

	if v, ok := data["total_cost_micro_usd"].(int64); ok && v != 0 {
		totalCost = float64(v) / 1000000.0
	} else if v, ok := data["total_cost"].(float64); ok {
		totalCost = v
	}

	if v, ok := data["total_tokens"].(int64); ok {
		totalTokens = v
	}

	return totalCost, totalTokens, nil
}

func (s *FirestoreStore) IncrementCost(ctx context.Context, serviceID, tenantID, month string, promptTokens, completionTokens int64, cost float64) error {
	totalTokens := promptTokens + completionTokens
	costMicro := int64(cost*1000000.0 + 0.5)
	now := time.Now().Unix()

	// 1. テナント別カウンターのアトミック加算 (firestore.Increment)
	tenantDocID := fmt.Sprintf("%s_%s_%s", serviceID, tenantID, month)
	tenantDocRef := s.client.Collection(colCostCounters).Doc(tenantDocID)
	_, err := tenantDocRef.Set(ctx, map[string]interface{}{
		"service_id":           serviceID,
		"tenant_id":            tenantID,
		"month":                month,
		"total_tokens":         firestore.Increment(totalTokens),
		"total_cost_micro_usd": firestore.Increment(costMicro),
		"updated_at":           now,
	}, firestore.MergeAll)
	if err != nil {
		return fmt.Errorf("failed to increment tenant cost in firestore: %w", err)
	}

	// 2. サービス全体カウンターのアトミック加算
	svcDocID := fmt.Sprintf("%s_SVC_%s", serviceID, month)
	svcDocRef := s.client.Collection(colCostCounters).Doc(svcDocID)
	_, err = svcDocRef.Set(ctx, map[string]interface{}{
		"service_id":           serviceID,
		"month":                month,
		"total_tokens":         firestore.Increment(totalTokens),
		"total_cost_micro_usd": firestore.Increment(costMicro),
		"updated_at":           now,
	}, firestore.MergeAll)
	if err != nil {
		return fmt.Errorf("failed to increment service cost in firestore: %w", err)
	}

	return nil
}

func (s *FirestoreStore) ResetCost(ctx context.Context, serviceID, tenantID, month string, cost float64, tokens int64) error {
	docID := fmt.Sprintf("%s_%s_%s", serviceID, tenantID, month)
	docRef := s.client.Collection(colCostCounters).Doc(docID)
	now := time.Now().Unix()

	_, err := docRef.Set(ctx, map[string]interface{}{
		"service_id":           serviceID,
		"tenant_id":            tenantID,
		"month":                month,
		"total_tokens":         tokens,
		"total_cost":           cost,
		"total_cost_micro_usd": int64(cost*1000000.0 + 0.5),
		"updated_at":           now,
	})
	return err
}

func (s *FirestoreStore) SetServiceLimit(ctx context.Context, serviceID string, costLimit float64, billingType string) error {
	cfg := &entity.ServiceConfig{
		ServiceID:   serviceID,
		BillingType: billingType,
		CostLimit:   costLimit,
		UpdatedAt:   time.Now(),
	}
	return s.SetServiceConfig(ctx, cfg)
}

func (s *FirestoreStore) SetTenantLimit(ctx context.Context, serviceID, tenantID string, costLimit float64, billingType string) error {
	cfg := &entity.TenantConfig{
		ServiceID:   serviceID,
		TenantID:    tenantID,
		BillingType: billingType,
		CostLimit:   costLimit,
		UpdatedAt:   time.Now(),
	}
	return s.SetTenantConfig(ctx, cfg)
}

func (s *FirestoreStore) GetServiceConfig(ctx context.Context, serviceID string) (*entity.ServiceConfig, error) {
	doc, err := s.client.Collection(colServiceConfigs).Doc(serviceID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get service config from firestore: %w", err)
	}

	data := doc.Data()
	cfg := &entity.ServiceConfig{
		ServiceID: serviceID,
	}
	if v, ok := data["billing_type"].(string); ok {
		cfg.BillingType = v
	}
	if v, ok := data["cost_limit"].(float64); ok {
		cfg.CostLimit = v
	}
	if v, ok := data["updated_at"].(int64); ok {
		cfg.UpdatedAt = time.Unix(v, 0)
	}
	return cfg, nil
}

func (s *FirestoreStore) GetTenantConfig(ctx context.Context, serviceID, tenantID string) (*entity.TenantConfig, error) {
	docID := fmt.Sprintf("%s_%s", serviceID, tenantID)
	doc, err := s.client.Collection(colTenantConfigs).Doc(docID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get tenant config from firestore: %w", err)
	}

	data := doc.Data()
	cfg := &entity.TenantConfig{
		ServiceID: serviceID,
		TenantID:  tenantID,
	}
	if v, ok := data["billing_type"].(string); ok {
		cfg.BillingType = v
	}
	if v, ok := data["cost_limit"].(float64); ok {
		cfg.CostLimit = v
	}
	if v, ok := data["updated_at"].(int64); ok {
		cfg.UpdatedAt = time.Unix(v, 0)
	}
	return cfg, nil
}

func (s *FirestoreStore) SetServiceConfig(ctx context.Context, cfg *entity.ServiceConfig) error {
	docRef := s.client.Collection(colServiceConfigs).Doc(cfg.ServiceID)
	_, err := docRef.Set(ctx, map[string]interface{}{
		"service_id":   cfg.ServiceID,
		"billing_type": cfg.BillingType,
		"cost_limit":   cfg.CostLimit,
		"updated_at":   cfg.UpdatedAt.Unix(),
	})
	return err
}

func (s *FirestoreStore) SetTenantConfig(ctx context.Context, cfg *entity.TenantConfig) error {
	docID := fmt.Sprintf("%s_%s", cfg.ServiceID, cfg.TenantID)
	docRef := s.client.Collection(colTenantConfigs).Doc(docID)
	_, err := docRef.Set(ctx, map[string]interface{}{
		"service_id":   cfg.ServiceID,
		"tenant_id":    cfg.TenantID,
		"billing_type": cfg.BillingType,
		"cost_limit":   cfg.CostLimit,
		"updated_at":   cfg.UpdatedAt.Unix(),
	})
	return err
}

// --- UsageStore Methods ---

func (s *FirestoreStore) RecordUsage(ctx context.Context, serviceID, tenantID, month, model string, promptTokens, completionTokens int64, cost float64, pricingVersion string) error {
	docID := fmt.Sprintf("%s_%s_%s", serviceID, tenantID, month)
	docRef := s.client.Collection(colTenantUsage).Doc(docID)
	totalTokens := promptTokens + completionTokens
	now := time.Now().Unix()

	return s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		doc, err := tx.Get(docRef)
		var existingTotalTokens int64
		var existingTotalCost float64
		modelsMap := make(map[string]map[string]interface{})

		if err == nil {
			data := doc.Data()
			if v, ok := data["total_tokens"].(int64); ok {
				existingTotalTokens = v
			}
			if v, ok := data["total_cost"].(float64); ok {
				existingTotalCost = v
			}
			if rawModels, ok := data["models"].(map[string]interface{}); ok {
				for mName, mVal := range rawModels {
					if mMap, ok := mVal.(map[string]interface{}); ok {
						modelsMap[mName] = mMap
					}
				}
			}
		} else if status.Code(err) != codes.NotFound {
			return err
		}

		// モデル内訳の更新
		mMap, ok := modelsMap[model]
		if !ok {
			mMap = map[string]interface{}{
				"prompt_tokens":     int64(0),
				"completion_tokens": int64(0),
				"total_tokens":      int64(0),
				"cost":              float64(0),
			}
		}

		pTok, _ := mMap["prompt_tokens"].(int64)
		cTok, _ := mMap["completion_tokens"].(int64)
		tTok, _ := mMap["total_tokens"].(int64)
		cVal, _ := mMap["cost"].(float64)

		mMap["prompt_tokens"] = pTok + promptTokens
		mMap["completion_tokens"] = cTok + completionTokens
		mMap["total_tokens"] = tTok + totalTokens
		mMap["cost"] = cVal + cost
		modelsMap[model] = mMap

		record := map[string]interface{}{
			"service_id":   serviceID,
			"tenant_id":    tenantID,
			"month":        month,
			"total_tokens": existingTotalTokens + totalTokens,
			"total_cost":   existingTotalCost + cost,
			"models":       modelsMap,
			"updated_at":   now,
		}

		return tx.Set(docRef, record)
	})
}

func (s *FirestoreStore) IncrementTenantUsage(ctx context.Context, serviceID, tenantID, month, model string, promptTokens, completionTokens int64, cost float64) error {
	return s.RecordUsage(ctx, serviceID, tenantID, month, model, promptTokens, completionTokens, cost, "")
}

func (s *FirestoreStore) GetTenantUsage(ctx context.Context, serviceID, tenantID, month string) (*entity.TenantMonthlyUsage, error) {
	docID := fmt.Sprintf("%s_%s_%s", serviceID, tenantID, month)
	doc, err := s.client.Collection(colTenantUsage).Doc(docID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return &entity.TenantMonthlyUsage{
				ServiceID:   serviceID,
				TenantID:    tenantID,
				Month:       month,
				TotalTokens: 0,
				TotalCost:   0,
				Models:      make(map[string]*entity.ModelUsage),
			}, nil
		}
		return nil, fmt.Errorf("failed to get tenant usage from firestore: %w", err)
	}

	data := doc.Data()
	usage := &entity.TenantMonthlyUsage{
		ServiceID: serviceID,
		TenantID:  tenantID,
		Month:     month,
		Models:    make(map[string]*entity.ModelUsage),
	}

	if v, ok := data["total_tokens"].(int64); ok {
		usage.TotalTokens = v
	}
	if v, ok := data["total_cost"].(float64); ok {
		usage.TotalCost = v
	}

	if rawModels, ok := data["models"].(map[string]interface{}); ok {
		for mName, mVal := range rawModels {
			if mMap, ok := mVal.(map[string]interface{}); ok {
				mu := &entity.ModelUsage{}
				if v, ok := mMap["prompt_tokens"].(int64); ok {
					mu.PromptTokens = v
				}
				if v, ok := mMap["completion_tokens"].(int64); ok {
					mu.CompletionTokens = v
				}
				if v, ok := mMap["total_tokens"].(int64); ok {
					mu.TotalTokens = v
				}
				if v, ok := mMap["cost"].(float64); ok {
					mu.Cost = v
				}
				usage.Models[mName] = mu
			}
		}
	}

	return usage, nil
}

func (s *FirestoreStore) GetServiceMonthlyUsage(ctx context.Context, serviceID, month string) (*entity.ServiceMonthlyReport, error) {
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

	iter := s.client.Collection(colTenantUsage).
		Where("service_id", "==", serviceID).
		Where("month", "==", month).
		Documents(ctx)
	defer iter.Stop()

	for {
		doc, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate service usage in firestore: %w", err)
		}

		data := doc.Data()
		var tenantID string
		var totalTokens int64
		var totalCost float64

		if v, ok := data["tenant_id"].(string); ok {
			tenantID = v
		}
		if v, ok := data["total_tokens"].(int64); ok {
			totalTokens = v
		}
		if v, ok := data["total_cost"].(float64); ok {
			totalCost = v
		}

		report.TotalTokens += totalTokens
		report.TotalCostUSD += totalCost

		if tenantID != "" {
			report.Tenants[tenantID] = &entity.TenantReportItem{
				TenantID:     tenantID,
				TotalTokens:  totalTokens,
				TotalCostUSD: totalCost,
			}
		}

		if rawModels, ok := data["models"].(map[string]interface{}); ok {
			for mName, mVal := range rawModels {
				if mMap, ok := mVal.(map[string]interface{}); ok {
					mItem, ok := report.Models[mName]
					if !ok {
						mItem = &entity.ServiceReportModel{}
						report.Models[mName] = mItem
					}
					if v, ok := mMap["total_tokens"].(int64); ok {
						mItem.Tokens += v
					}
					if v, ok := mMap["cost"].(float64); ok {
						mItem.CostUSD += v
					}
				}
			}
		}
	}

	return report, nil
}

func (s *FirestoreStore) GetAllTenantsUsageByMonth(ctx context.Context, month string) ([]*entity.TenantMonthlyUsage, error) {
	iter := s.client.Collection(colTenantUsage).
		Where("month", "==", month).
		Documents(ctx)
	defer iter.Stop()

	var list []*entity.TenantMonthlyUsage
	for {
		doc, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate all tenants usage in firestore: %w", err)
		}

		data := doc.Data()
		usage := &entity.TenantMonthlyUsage{
			Month:  month,
			Models: make(map[string]*entity.ModelUsage),
		}

		if v, ok := data["service_id"].(string); ok {
			usage.ServiceID = v
		}
		if v, ok := data["tenant_id"].(string); ok {
			usage.TenantID = v
		}
		if v, ok := data["total_tokens"].(int64); ok {
			usage.TotalTokens = v
		}
		if v, ok := data["total_cost"].(float64); ok {
			usage.TotalCost = v
		}

		if rawModels, ok := data["models"].(map[string]interface{}); ok {
			for mName, mVal := range rawModels {
				if mMap, ok := mVal.(map[string]interface{}); ok {
					mu := &entity.ModelUsage{}
					if v, ok := mMap["prompt_tokens"].(int64); ok {
						mu.PromptTokens = v
					}
					if v, ok := mMap["completion_tokens"].(int64); ok {
						mu.CompletionTokens = v
					}
					if v, ok := mMap["total_tokens"].(int64); ok {
						mu.TotalTokens = v
					}
					if v, ok := mMap["cost"].(float64); ok {
						mu.Cost = v
					}
					usage.Models[mName] = mu
				}
			}
		}
		list = append(list, usage)
	}
	return list, nil
}

// --- Distributed Lock Methods ---

func (s *FirestoreStore) AcquireLock(ctx context.Context, lockKey string, ttlSeconds int64) (bool, error) {
	docRef := s.client.Collection(colLocks).Doc(lockKey)
	now := time.Now().Unix()
	expiresAt := now + ttlSeconds

	var acquired bool
	err := s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		doc, err := tx.Get(docRef)
		if err == nil {
			data := doc.Data()
			if exp, ok := data["expires_at"].(int64); ok && exp > now {
				acquired = false
				return nil // 有効なロックが存在
			}
		} else if status.Code(err) != codes.NotFound {
			return err
		}

		acquired = true
		return tx.Set(docRef, map[string]interface{}{
			"lock_key":   lockKey,
			"expires_at": expiresAt,
			"updated_at": now,
		})
	})

	if err != nil {
		return false, fmt.Errorf("failed to acquire firestore lock: %w", err)
	}
	return acquired, nil
}

func (s *FirestoreStore) ReleaseLock(ctx context.Context, lockKey string) error {
	docRef := s.client.Collection(colLocks).Doc(lockKey)
	_, err := docRef.Delete(ctx)
	if err != nil && status.Code(err) != codes.NotFound {
		return fmt.Errorf("failed to release firestore lock: %w", err)
	}
	return nil
}

// --- Notification Methods ---

func (s *FirestoreStore) SaveNotification(ctx context.Context, ntf *entity.Notification) error {
	docRef := s.client.Collection(colNotifications).Doc(ntf.ID)
	_, err := docRef.Set(ctx, map[string]interface{}{
		"id":         ntf.ID,
		"type":       string(ntf.Type),
		"title":      ntf.Title,
		"message":    ntf.Message,
		"is_alert":   ntf.IsAlert,
		"created_at": ntf.CreatedAt.Unix(),
	})
	return err
}

func (s *FirestoreStore) ListNotifications(ctx context.Context, limit int) ([]*entity.Notification, error) {
	iter := s.client.Collection(colNotifications).
		OrderBy("created_at", firestore.Desc).
		Limit(limit).
		Documents(ctx)
	defer iter.Stop()

	var list []*entity.Notification
	for {
		doc, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to list notifications from firestore: %w", err)
		}

		data := doc.Data()
		ntf := &entity.Notification{}
		if v, ok := data["id"].(string); ok {
			ntf.ID = v
		}
		if v, ok := data["type"].(string); ok {
			ntf.Type = entity.NotificationType(v)
		}
		if v, ok := data["title"].(string); ok {
			ntf.Title = v
		}
		if v, ok := data["message"].(string); ok {
			ntf.Message = v
		}
		if v, ok := data["is_alert"].(bool); ok {
			ntf.IsAlert = v
		}
		if v, ok := data["created_at"].(int64); ok {
			ntf.CreatedAt = time.Unix(v, 0)
		}

		list = append(list, ntf)
	}
	return list, nil
}
