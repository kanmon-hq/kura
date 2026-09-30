package usecase

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/infrastructure/dynamodb"
)

type mockNotifier struct {
	mu       sync.Mutex
	messages []string
	isAlert  []bool
}

func (m *mockNotifier) Send(ctx context.Context, title, message string, isAlert bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, title+"\n"+message)
	m.isAlert = append(m.isAlert, isAlert)
	return nil
}

func TestBatchUseCase_MonthlyReport_Locking(t *testing.T) {
	repo := dynamodb.NewMemoryQuotaRepository(1000000)
	notifier := &mockNotifier{}
	batchUC := NewBatchUseCase(repo, repo, notifier)

	now := time.Now().In(entity.JST)
	lastMonth := entity.FormatMonthJST(now.AddDate(0, -1, 0))

	// 前月分の利用実績を登録
	_ = repo.IncrementTenantUsage(context.Background(), "payment-service", "team-a", lastMonth, "fast", 1000, 2000, 0.05)

	// 1回目の実行: ロック獲得成功 -> 通知される
	err := batchUC.RunMonthlyReport(context.Background())
	if err != nil {
		t.Fatalf("unexpected error on first run: %v", err)
	}

	notifier.mu.Lock()
	if len(notifier.messages) != 1 {
		t.Fatalf("expected 1 notification sent, got %d", len(notifier.messages))
	}
	if !strings.Contains(notifier.messages[0], "payment-service") {
		t.Errorf("expected notification to contain payment-service, got %s", notifier.messages[0])
	}
	notifier.mu.Unlock()

	// 2回目の実行 (他コンテナまたは重複呼び出しを想定): ロックが既に存在するためスキップ
	err = batchUC.RunMonthlyReport(context.Background())
	if err != nil {
		t.Fatalf("unexpected error on second run: %v", err)
	}

	notifier.mu.Lock()
	if len(notifier.messages) != 1 {
		t.Errorf("expected still 1 notification (second run skipped), got %d", len(notifier.messages))
	}
	notifier.mu.Unlock()
}

func TestBatchUseCase_QuotaAlerts_LockingAndThreshold(t *testing.T) {
	repo := dynamodb.NewMemoryQuotaRepository(1000000)
	notifier := &mockNotifier{}
	batchUC := NewBatchUseCase(repo, repo, notifier)

	currentMonth := entity.CurrentMonthJST()

	// サービス1: 85% 利用 (コスト上限$100.00、消費$85.00) -> 警告対象
	_ = repo.SetServiceLimit(context.Background(), "payment-service", 100.0, string(entity.BillingTypeCapped))
	_ = repo.IncrementTenantUsage(context.Background(), "payment-service", "team-alert", currentMonth, "fast", 850000, 0, 85.0)

	// サービス2: 50% 利用 (コスト上限$100.00、消費$50.00) -> 対象外
	_ = repo.SetServiceLimit(context.Background(), "auth-service", 100.0, string(entity.BillingTypeCapped))
	_ = repo.IncrementTenantUsage(context.Background(), "auth-service", "team-safe", currentMonth, "fast", 500000, 0, 50.0)

	// 1回目の実行: アラート通知が送られる
	err := batchUC.RunQuotaAlerts(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	notifier.mu.Lock()
	if len(notifier.messages) != 1 {
		t.Fatalf("expected 1 alert notification, got %d", len(notifier.messages))
	}
	alertMsg := notifier.messages[0]
	if !strings.Contains(alertMsg, "payment-service") {
		t.Errorf("expected alert message to contain payment-service, got: %s", alertMsg)
	}
	if strings.Contains(alertMsg, "auth-service") {
		t.Errorf("alert message should not contain safe service: %s", alertMsg)
	}
	notifier.mu.Unlock()

	// 2回目の実行 (同一時間スロット): スキップされる
	err = batchUC.RunQuotaAlerts(context.Background())
	if err != nil {
		t.Fatalf("unexpected error on second run: %v", err)
	}

	notifier.mu.Lock()
	if len(notifier.messages) != 1 {
		t.Errorf("expected still 1 alert notification (second run skipped), got %d", len(notifier.messages))
	}
	notifier.mu.Unlock()
}

func TestBatchUseCase_RunReconciliation(t *testing.T) {
	repo := dynamodb.NewMemoryQuotaRepository(1000000)
	notifier := &mockNotifier{}
	batchUC := NewBatchUseCase(repo, repo, notifier)
	ctx := context.Background()

	currentMonth := entity.CurrentMonthJST()

	// テナント利用実績を記録
	_ = repo.IncrementTenantUsage(ctx, "payment-service", "team-1", currentMonth, "fast", 1000, 2000, 0.05)
	_ = repo.IncrementTenantUsage(ctx, "payment-service", "team-2", currentMonth, "fast", 3000, 4000, 0.15)

	// 1回目の実行: 正常に補正
	err := batchUC.RunReconciliation(ctx)
	if err != nil {
		t.Fatalf("expected nil error on reconciliation, got %v", err)
	}

	// 補正後のコスト確認
	tCost, tTokens, _ := repo.GetTenantCost(ctx, "payment-service", "team-1", currentMonth)
	if tTokens != 3000 || tCost != 0.05 {
		t.Errorf("unexpected reconciled tenant cost/tokens: %f, %d", tCost, tTokens)
	}
}
