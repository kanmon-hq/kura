package repository

import (
	"context"

	"github.com/kanmon-hq/kura/internal/domain/entity"
)

// QuotaRepository は CostStore と UsageStore の双方を満たす統合リポジトリインターフェース
type QuotaRepository interface {
	CostStore
	UsageStore

	// IncrementTenantUsage は互換性のためのエイリアスメソッド
	IncrementTenantUsage(ctx context.Context, serviceID, tenantID, month string, model string, promptTokens, completionTokens int64, cost float64) error

	// SetTenantConfig はテナント個別の永続設定 (Master レコード) を保存・更新する
	SetTenantConfig(ctx context.Context, cfg *entity.TenantConfig) error

	// SetServiceConfig はサービスの永続設定 (Master レコード) を保存・更新する
	SetServiceConfig(ctx context.Context, cfg *entity.ServiceConfig) error
}
