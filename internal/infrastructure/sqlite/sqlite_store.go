package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/repository"
)

// SQLiteStore は SQLite をバックエンドとする CostStore / UsageStore 実装 (単一ノード向け)
type SQLiteStore struct {
	db *sql.DB
	mu sync.Mutex // 単一ライターの排他制御
}

var _ repository.CostStore = (*SQLiteStore)(nil)
var _ repository.UsageStore = (*SQLiteStore)(nil)

// NewSQLiteStore は指定ファイルパスで SQLiteStore を初期化し、テーブルスキーマを自動作成する
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	if dbPath == "" {
		dbPath = "./data/kura.db"
	}

	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory for sqlite db '%s': %w", dir, err)
		}
	}

	// PRAGMA 設定付き DSN
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database '%s': %w", dbPath, err)
	}

	// 単一ライター・コネクションプール制御
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	store := &SQLiteStore{db: db}
	if err := store.initSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to initialize sqlite schema: %w", err)
	}

	return store, nil
}

// NewSQLiteStoreWithDB は既存の *sql.DB をラップして SQLiteStore を生成する (テスト用)
func NewSQLiteStoreWithDB(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db}
}

// Close はデータベース接続をクローズする
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) initSchema(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	schema := `
	CREATE TABLE IF NOT EXISTS kura_service_configs (
		service_id TEXT PRIMARY KEY,
		billing_type TEXT NOT NULL,
		cost_limit REAL NOT NULL,
		updated_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS kura_tenant_configs (
		service_id TEXT NOT NULL,
		tenant_id TEXT NOT NULL,
		billing_type TEXT NOT NULL,
		cost_limit REAL NOT NULL,
		updated_at DATETIME NOT NULL,
		PRIMARY KEY(service_id, tenant_id)
	);

	CREATE TABLE IF NOT EXISTS kura_cost_counters (
		counter_key TEXT PRIMARY KEY,
		cost REAL NOT NULL DEFAULT 0,
		tokens INTEGER NOT NULL DEFAULT 0,
		updated_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS kura_tenant_usage (
		service_id TEXT NOT NULL,
		tenant_id TEXT NOT NULL,
		month TEXT NOT NULL,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		total_cost REAL NOT NULL DEFAULT 0,
		models TEXT NOT NULL DEFAULT '{}',
		updated_at DATETIME NOT NULL,
		PRIMARY KEY(service_id, tenant_id, month)
	);

	CREATE TABLE IF NOT EXISTS kura_locks (
		lock_key TEXT PRIMARY KEY,
		expires_at DATETIME NOT NULL
	);

	CREATE TABLE IF NOT EXISTS kura_notifications (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL,
		title TEXT NOT NULL,
		message TEXT NOT NULL,
		is_alert INTEGER NOT NULL,
		created_at DATETIME NOT NULL
	);
	`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// ================= CostStore 実装 =================

func (s *SQLiteStore) GetServiceCost(ctx context.Context, serviceID, month string) (float64, int64, error) {
	key := fmt.Sprintf("svc:%s:month:%s", serviceID, month)
	var cost float64
	var tokens int64
	err := s.db.QueryRowContext(ctx, "SELECT cost, tokens FROM kura_cost_counters WHERE counter_key = ?", key).Scan(&cost, &tokens)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	return cost, tokens, nil
}

func (s *SQLiteStore) GetTenantCost(ctx context.Context, serviceID, tenantID, month string) (float64, int64, error) {
	key := fmt.Sprintf("svc:%s:tenant:%s:month:%s", serviceID, tenantID, month)
	var cost float64
	var tokens int64
	err := s.db.QueryRowContext(ctx, "SELECT cost, tokens FROM kura_cost_counters WHERE counter_key = ?", key).Scan(&cost, &tokens)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	return cost, tokens, nil
}

func (s *SQLiteStore) IncrementCost(ctx context.Context, serviceID, tenantID, month string, promptTokens, completionTokens int64, cost float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	totalTokens := promptTokens + completionTokens
	svcKey := fmt.Sprintf("svc:%s:month:%s", serviceID, month)
	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	query := `
	INSERT INTO kura_cost_counters (counter_key, cost, tokens, updated_at)
	VALUES (?, ?, ?, ?)
	ON CONFLICT (counter_key) DO UPDATE SET
		cost = kura_cost_counters.cost + excluded.cost,
		tokens = kura_cost_counters.tokens + excluded.tokens,
		updated_at = excluded.updated_at;
	`
	if _, err := tx.ExecContext(ctx, query, svcKey, cost, totalTokens, now); err != nil {
		return err
	}

	if tenantID != "" {
		tenantKey := fmt.Sprintf("svc:%s:tenant:%s:month:%s", serviceID, tenantID, month)
		if _, err := tx.ExecContext(ctx, query, tenantKey, cost, totalTokens, now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (s *SQLiteStore) ResetCost(ctx context.Context, serviceID, tenantID, month string, cost float64, tokens int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var key string
	if tenantID == "" {
		key = fmt.Sprintf("svc:%s:month:%s", serviceID, month)
	} else {
		key = fmt.Sprintf("svc:%s:tenant:%s:month:%s", serviceID, tenantID, month)
	}
	now := time.Now().UTC()

	query := `
	INSERT INTO kura_cost_counters (counter_key, cost, tokens, updated_at)
	VALUES (?, ?, ?, ?)
	ON CONFLICT (counter_key) DO UPDATE SET
		cost = excluded.cost,
		tokens = excluded.tokens,
		updated_at = excluded.updated_at;
	`
	_, err := s.db.ExecContext(ctx, query, key, cost, tokens, now)
	return err
}

func (s *SQLiteStore) SetServiceLimit(ctx context.Context, serviceID string, costLimit float64, billingType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	query := `
	INSERT INTO kura_service_configs (service_id, cost_limit, billing_type, updated_at)
	VALUES (?, ?, ?, ?)
	ON CONFLICT (service_id) DO UPDATE SET
		cost_limit = excluded.cost_limit,
		billing_type = excluded.billing_type,
		updated_at = excluded.updated_at;
	`
	_, err := s.db.ExecContext(ctx, query, serviceID, costLimit, billingType, now)
	return err
}

func (s *SQLiteStore) SetTenantLimit(ctx context.Context, serviceID, tenantID string, costLimit float64, billingType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	query := `
	INSERT INTO kura_tenant_configs (service_id, tenant_id, cost_limit, billing_type, updated_at)
	VALUES (?, ?, ?, ?, ?)
	ON CONFLICT (service_id, tenant_id) DO UPDATE SET
		cost_limit = excluded.cost_limit,
		billing_type = excluded.billing_type,
		updated_at = excluded.updated_at;
	`
	_, err := s.db.ExecContext(ctx, query, serviceID, tenantID, costLimit, billingType, now)
	return err
}

func (s *SQLiteStore) GetServiceConfig(ctx context.Context, serviceID string) (*entity.ServiceConfig, error) {
	var cfg entity.ServiceConfig
	err := s.db.QueryRowContext(ctx, "SELECT service_id, cost_limit, billing_type, updated_at FROM kura_service_configs WHERE service_id = ?", serviceID).
		Scan(&cfg.ServiceID, &cfg.CostLimit, &cfg.BillingType, &cfg.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &cfg, nil
}

func (s *SQLiteStore) GetTenantConfig(ctx context.Context, serviceID, tenantID string) (*entity.TenantConfig, error) {
	var cfg entity.TenantConfig
	err := s.db.QueryRowContext(ctx, "SELECT service_id, tenant_id, cost_limit, billing_type, updated_at FROM kura_tenant_configs WHERE service_id = ? AND tenant_id = ?", serviceID, tenantID).
		Scan(&cfg.ServiceID, &cfg.TenantID, &cfg.CostLimit, &cfg.BillingType, &cfg.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &cfg, nil
}

// ================= UsageStore 実装 =================

func (s *SQLiteStore) RecordUsage(ctx context.Context, serviceID, tenantID, month, model string, promptTokens, completionTokens int64, cost float64, pricingVersion string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	totalTokens := promptTokens + completionTokens
	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var existingTokens int64
	var existingCost float64
	var modelsJSON string

	err = tx.QueryRowContext(ctx, `
		SELECT total_tokens, total_cost, models 
		FROM kura_tenant_usage 
		WHERE service_id = ? AND tenant_id = ? AND month = ?`,
		serviceID, tenantID, month).Scan(&existingTokens, &existingCost, &modelsJSON)

	modelsMap := make(map[string]*entity.ModelUsage)
	if err == nil {
		_ = json.Unmarshal([]byte(modelsJSON), &modelsMap)
	} else if err != sql.ErrNoRows {
		return err
	}

	mu, ok := modelsMap[model]
	if !ok {
		mu = &entity.ModelUsage{}
		modelsMap[model] = mu
	}
	mu.PromptTokens += promptTokens
	mu.CompletionTokens += completionTokens
	mu.TotalTokens += totalTokens
	mu.Cost += cost

	newModelsBytes, _ := json.Marshal(modelsMap)
	newTotalTokens := existingTokens + totalTokens
	newTotalCost := existingCost + cost

	upsertQuery := `
	INSERT INTO kura_tenant_usage (service_id, tenant_id, month, total_tokens, total_cost, models, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT (service_id, tenant_id, month) DO UPDATE SET
		total_tokens = excluded.total_tokens,
		total_cost = excluded.total_cost,
		models = excluded.models,
		updated_at = excluded.updated_at;
	`
	if _, err := tx.ExecContext(ctx, upsertQuery, serviceID, tenantID, month, newTotalTokens, newTotalCost, string(newModelsBytes), now); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *SQLiteStore) GetTenantUsage(ctx context.Context, serviceID, tenantID, month string) (*entity.TenantMonthlyUsage, error) {
	var usage entity.TenantMonthlyUsage
	var modelsJSON string

	err := s.db.QueryRowContext(ctx, `
		SELECT service_id, tenant_id, month, total_tokens, total_cost, models, updated_at 
		FROM kura_tenant_usage 
		WHERE service_id = ? AND tenant_id = ? AND month = ?`,
		serviceID, tenantID, month).Scan(&usage.ServiceID, &usage.TenantID, &usage.Month, &usage.TotalTokens, &usage.TotalCost, &modelsJSON, &usage.UpdatedAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	usage.Models = make(map[string]*entity.ModelUsage)
	_ = json.Unmarshal([]byte(modelsJSON), &usage.Models)
	usage.PK = entity.BuildPK(serviceID, tenantID)
	usage.SK = entity.BuildSK(month)
	return &usage, nil
}

func (s *SQLiteStore) GetServiceMonthlyUsage(ctx context.Context, serviceID, month string) (*entity.ServiceMonthlyReport, error) {
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

	rows, err := s.db.QueryContext(ctx, `
		SELECT tenant_id, total_tokens, total_cost, models 
		FROM kura_tenant_usage 
		WHERE service_id = ? AND month = ?`, serviceID, month)
	if err != nil {
		return report, err
	}
	defer rows.Close()

	for rows.Next() {
		var tenantID string
		var totalTokens int64
		var totalCost float64
		var modelsJSON string

		if err := rows.Scan(&tenantID, &totalTokens, &totalCost, &modelsJSON); err != nil {
			continue
		}

		report.TotalTokens += totalTokens
		report.TotalCostUSD += totalCost

		report.Tenants[tenantID] = &entity.TenantReportItem{
			TenantID:     tenantID,
			TotalTokens:  totalTokens,
			TotalCostUSD: totalCost,
		}

		var modelsMap map[string]*entity.ModelUsage
		if err := json.Unmarshal([]byte(modelsJSON), &modelsMap); err == nil {
			for mName, mu := range modelsMap {
				mItem, ok := report.Models[mName]
				if !ok {
					mItem = &entity.ServiceReportModel{}
					report.Models[mName] = mItem
				}
				mItem.Tokens += mu.TotalTokens
				mItem.CostUSD += mu.Cost
			}
		}
	}

	return report, nil
}

func (s *SQLiteStore) GetAllTenantsUsageByMonth(ctx context.Context, month string) ([]*entity.TenantMonthlyUsage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT service_id, tenant_id, month, total_tokens, total_cost, models, updated_at 
		FROM kura_tenant_usage 
		WHERE month = ?`, month)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*entity.TenantMonthlyUsage
	for rows.Next() {
		var usage entity.TenantMonthlyUsage
		var modelsJSON string
		if err := rows.Scan(&usage.ServiceID, &usage.TenantID, &usage.Month, &usage.TotalTokens, &usage.TotalCost, &modelsJSON, &usage.UpdatedAt); err != nil {
			continue
		}
		usage.Models = make(map[string]*entity.ModelUsage)
		_ = json.Unmarshal([]byte(modelsJSON), &usage.Models)
		usage.PK = entity.BuildPK(usage.ServiceID, usage.TenantID)
		usage.SK = entity.BuildSK(usage.Month)
		list = append(list, &usage)
	}
	return list, nil
}

func (s *SQLiteStore) AcquireLock(ctx context.Context, lockKey string, ttlSeconds int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	expiresAt := now.Add(time.Duration(ttlSeconds) * time.Second)

	query := `
	INSERT INTO kura_locks (lock_key, expires_at)
	VALUES (?, ?)
	ON CONFLICT (lock_key) DO UPDATE
	SET expires_at = excluded.expires_at
	WHERE kura_locks.expires_at < ?;
	`
	res, err := s.db.ExecContext(ctx, query, lockKey, expiresAt, now)
	if err != nil {
		return false, err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (s *SQLiteStore) ReleaseLock(ctx context.Context, lockKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.ExecContext(ctx, "DELETE FROM kura_locks WHERE lock_key = ?", lockKey)
	return err
}

func (s *SQLiteStore) SaveNotification(ctx context.Context, ntf *entity.Notification) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	isAlertInt := 0
	if ntf.IsAlert {
		isAlertInt = 1
	}

	query := `
	INSERT INTO kura_notifications (id, type, title, message, is_alert, created_at)
	VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT (id) DO NOTHING;
	`
	_, err := s.db.ExecContext(ctx, query, ntf.ID, string(ntf.Type), ntf.Title, ntf.Message, isAlertInt, ntf.CreatedAt.UTC())
	return err
}

func (s *SQLiteStore) ListNotifications(ctx context.Context, limit int) ([]*entity.Notification, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id, type, title, message, is_alert, created_at FROM kura_notifications ORDER BY created_at DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*entity.Notification
	for rows.Next() {
		var ntf entity.Notification
		var typeStr string
		var isAlertInt int
		if err := rows.Scan(&ntf.ID, &typeStr, &ntf.Title, &ntf.Message, &isAlertInt, &ntf.CreatedAt); err != nil {
			continue
		}
		ntf.Type = entity.NotificationType(typeStr)
		ntf.IsAlert = isAlertInt == 1
		list = append(list, &ntf)
	}
	return list, nil
}

func (s *SQLiteStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}
