package usecase_test

import (
	"context"
	"testing"
	"time"

	"github.com/northfieldzz/kura/internal/domain/entity"
	"github.com/northfieldzz/kura/internal/infrastructure/memory"
	"github.com/northfieldzz/kura/internal/usecase"
)

func TestAdminUseCase_AllMethods(t *testing.T) {
	ctx := context.Background()
	store := memory.NewMemoryStore()
	adminUC := usecase.NewAdminUseCase(store, store)

	// 1. GetMonthlyUsage - バリデーションエラー & 正常取得
	if _, err := adminUC.GetMonthlyUsage(ctx, "", "2026-09"); err == nil {
		t.Fatalf("expected error when service_id is empty")
	}

	report, err := adminUC.GetMonthlyUsage(ctx, "svc-1", "")
	if err != nil || report == nil {
		t.Fatalf("failed to get monthly usage: %v", err)
	}

	// 2. SetTenantLimit - サービス全体 & テナント個別
	if err := adminUC.SetTenantLimit(ctx, &usecase.SetLimitRequest{}); err == nil {
		t.Fatalf("expected error when service_id is empty")
	}

	if err := adminUC.SetTenantLimit(ctx, &usecase.SetLimitRequest{
		ServiceID: "svc-1",
		CostLimit: 100.0,
	}); err != nil {
		t.Fatalf("failed to set service limit: %v", err)
	}

	if err := adminUC.SetTenantLimit(ctx, &usecase.SetLimitRequest{
		ServiceID: "svc-1",
		TenantID:  "tenant-1",
		CostLimit: 50.0,
	}); err != nil {
		t.Fatalf("failed to set tenant limit: %v", err)
	}

	// 3. ListNotifications
	ntf := &entity.Notification{
		ID:        "ntf-1",
		Title:     "Title",
		Message:   "Body",
		CreatedAt: time.Now(),
	}
	_ = store.SaveNotification(ctx, ntf)

	list, err := adminUC.ListNotifications(ctx, 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("failed to list notifications: %v", err)
	}
}
