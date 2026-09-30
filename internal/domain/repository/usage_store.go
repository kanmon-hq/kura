package repository

import (
	"context"

	"github.com/kanmon-hq/kura/internal/domain/entity"
)

// UsageStore は利用実績の記録・集計・レポート取得および分散ロックを担うインターフェース
type UsageStore interface {
	// RecordUsage はモデル別のトークン消費および費用実績を記録する
	RecordUsage(ctx context.Context, serviceID, tenantID, month, model string, promptTokens, completionTokens int64, cost float64, pricingVersion string) error

	// GetTenantUsage は指定サービス・テナントの指定月の累計利用量およびモデル別内訳を取得する
	GetTenantUsage(ctx context.Context, serviceID, tenantID, month string) (*entity.TenantMonthlyUsage, error)

	// GetServiceMonthlyUsage は指定サービス全体の月次利用実績およびモデル別内訳を取得する (Admin API 向け)
	GetServiceMonthlyUsage(ctx context.Context, serviceID, month string) (*entity.ServiceMonthlyReport, error)

	// GetAllTenantsUsageByMonth は指定月の全テナントの利用実績を取得する (月次締めレポート・補正向け)
	GetAllTenantsUsageByMonth(ctx context.Context, month string) ([]*entity.TenantMonthlyUsage, error)

	// AcquireLock は分散ロックを獲得する (月次締め・補正の二重実行防止)
	AcquireLock(ctx context.Context, lockKey string, ttlSeconds int64) (bool, error)

	// ReleaseLock は分散ロックを解放する
	ReleaseLock(ctx context.Context, lockKey string) error

	// SaveNotification はアプリ内通知（アラート・月次レポート）を保存する
	SaveNotification(ctx context.Context, ntf *entity.Notification) error

	// ListNotifications は最新のアプリ内通知一覧を取得する (新しい順)
	ListNotifications(ctx context.Context, limit int) ([]*entity.Notification, error)

	// Ping はストアへの疎通健全性を確認する
	Ping(ctx context.Context) error
}
