package repository

import (
	"context"

	"github.com/kanmon-hq/kura/internal/domain/entity"
)

// CostStore はホットパス（残枠確認、アトミック加算、期間リセット）を担うインターフェース
type CostStore interface {
	// GetServiceCost はサービス全体の指定月の累計コストと消費トークン数を取得する
	GetServiceCost(ctx context.Context, serviceID, month string) (cost float64, tokens int64, err error)

	// GetTenantCost はテナント個別の指定月の累計コストと消費トークン数を取得する
	GetTenantCost(ctx context.Context, serviceID, tenantID, month string) (cost float64, tokens int64, err error)

	// IncrementCost はトークン使用量と費用をアトミック加算する (ソフトリミット)
	IncrementCost(ctx context.Context, serviceID, tenantID, month string, promptTokens, completionTokens int64, cost float64) error

	// ResetCost は指定月のコスト・トークン残高を再構築・補正する (Reconciliation 向け)
	ResetCost(ctx context.Context, serviceID, tenantID, month string, cost float64, tokens int64) error

	// SetServiceLimit はサービス全体の月次コスト上限 (USD) や課金タイプを設定・更新する
	SetServiceLimit(ctx context.Context, serviceID string, costLimit float64, billingType string) error

	// SetTenantLimit はサービス配下のテナント個別の月次コスト上限 (USD) や課金タイプを設定・更新する
	SetTenantLimit(ctx context.Context, serviceID, tenantID string, costLimit float64, billingType string) error

	// GetServiceConfig はサービスの永続設定 (Master レコード) を取得する
	GetServiceConfig(ctx context.Context, serviceID string) (*entity.ServiceConfig, error)

	// GetTenantConfig はテナント個別の永続設定 (Master レコード) を取得する
	GetTenantConfig(ctx context.Context, serviceID, tenantID string) (*entity.TenantConfig, error)

	// Ping はストアへの疎通健全性を確認する
	Ping(ctx context.Context) error
}
