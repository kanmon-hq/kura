package usecase

import (
	"context"
	"fmt"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/repository"
)

// SetLimitRequest はサービス上限設定リクエスト
type SetLimitRequest struct {
	ServiceID   string  `json:"service_id" doc:"サービス識別子" required:"true" example:"payment-service"`
	TenantID    string  `json:"tenant_id,omitempty" doc:"テナント識別子 (指定時はテナント個別上限、未指定時はサービス全体上限を設定)" example:"team-alpha"`
	CostLimit   float64 `json:"cost_limit,omitempty" doc:"月次コスト上限 (USD)"`
	BillingType string  `json:"billing_type,omitempty" doc:"課金プラン (pay_as_you_go または capped)"`
}

// AdminUseCase は社内管理・請求レポート・クォータ設定用ユースケース
type AdminUseCase interface {
	GetMonthlyUsage(ctx context.Context, serviceID, month string) (*entity.ServiceMonthlyReport, error)
	SetTenantLimit(ctx context.Context, req *SetLimitRequest) error
	ListNotifications(ctx context.Context, limit int) ([]*entity.Notification, error)
}

type adminUseCase struct {
	costStore  repository.CostStore
	usageStore repository.UsageStore
}

// NewAdminUseCase は AdminUseCase を生成する
func NewAdminUseCase(costStore repository.CostStore, usageStore repository.UsageStore) AdminUseCase {
	return &adminUseCase{
		costStore:  costStore,
		usageStore: usageStore,
	}
}

func (u *adminUseCase) GetMonthlyUsage(ctx context.Context, serviceID, month string) (*entity.ServiceMonthlyReport, error) {
	if serviceID == "" {
		return nil, fmt.Errorf("service_id query parameter is required")
	}
	if month == "" {
		month = entity.CurrentMonthJST()
	}

	report, err := u.usageStore.GetServiceMonthlyUsage(ctx, serviceID, month)
	if err != nil {
		return nil, fmt.Errorf("failed to get service monthly usage: %w", err)
	}

	return report, nil
}

func (u *adminUseCase) SetTenantLimit(ctx context.Context, req *SetLimitRequest) error {
	if req.ServiceID == "" {
		return fmt.Errorf("service_id is required")
	}
	if req.BillingType == "" {
		if req.CostLimit > 0 {
			req.BillingType = string(entity.BillingTypeCapped)
		} else {
			req.BillingType = string(entity.BillingTypePAYG)
		}
	}

	if req.TenantID != "" {
		return u.costStore.SetTenantLimit(ctx, req.ServiceID, req.TenantID, req.CostLimit, req.BillingType)
	}

	return u.costStore.SetServiceLimit(ctx, req.ServiceID, req.CostLimit, req.BillingType)
}

func (u *adminUseCase) ListNotifications(ctx context.Context, limit int) ([]*entity.Notification, error) {
	return u.usageStore.ListNotifications(ctx, limit)
}
