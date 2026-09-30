package usecase

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/repository"
	"github.com/kanmon-hq/kura/internal/infrastructure/notifier"
)

// BatchUseCase は定期バッチ処理および補正処理のビジネスロジックを担うインターフェース
type BatchUseCase interface {
	// RunMonthlyReport は前月の月次締めレポートを集計し、通知する (毎月1日実行)
	RunMonthlyReport(ctx context.Context) error

	// RunQuotaAlerts は当月のクォータ使用率が 80% / 90% を超えたテナントを検知して警告通知する (毎時実行)
	RunQuotaAlerts(ctx context.Context) error

	// RunReconciliation は集計結果ストアの実績からコスト管理ストアのカウンタを再構築・補正する
	RunReconciliation(ctx context.Context) error
}

type batchUseCase struct {
	costStore  repository.CostStore
	usageStore repository.UsageStore
	notifier   notifier.Notifier
}

// NewBatchUseCase は CostStore と UsageStore を受け取って BatchUseCase を生成する
func NewBatchUseCase(costStore repository.CostStore, usageStore repository.UsageStore, n notifier.Notifier) BatchUseCase {
	return &batchUseCase{
		costStore:  costStore,
		usageStore: usageStore,
		notifier:   n,
	}
}

// RunMonthlyReport は前月の月次締めレポートを集計・通知する
func (u *batchUseCase) RunMonthlyReport(ctx context.Context) error {
	now := time.Now().In(entity.JST)
	// 前月キー (例: 9月1日実行なら 2026-08)
	lastMonth := entity.FormatMonthJST(now.AddDate(0, -1, 0))
	lockKey := "monthly_report#" + lastMonth

	// 1. 分散ロック取得 (30日間有効)
	acquired, err := u.usageStore.AcquireLock(ctx, lockKey, 30*86400)
	if err != nil {
		return fmt.Errorf("failed to acquire lock for monthly report: %w", err)
	}
	if !acquired {
		log.Printf("[INFO] [CRON] Monthly report for %s was already processed by another instance. Skipping.", lastMonth)
		return nil
	}

	log.Printf("[INFO] [CRON] Acquired lock for monthly report (%s). Generating report...", lastMonth)

	// 2. 前月分の全テナント利用実績を取得
	tenants, err := u.usageStore.GetAllTenantsUsageByMonth(ctx, lastMonth)
	if err != nil {
		return fmt.Errorf("failed to fetch monthly usage for %s: %w", lastMonth, err)
	}

	if len(tenants) == 0 {
		msg := fmt.Sprintf("対象月: %s\n前月の利用実績レコードは存在しませんでした（利用量 0）。", lastMonth)
		_ = u.notifier.Send(ctx, fmt.Sprintf("📊 Kura 月次利用実績レポート (%s)", lastMonth), msg, false)
		return nil
	}

	// サービス別に合算集計
	type serviceAgg struct {
		serviceID   string
		totalTokens int64
		totalCost   float64
		tenantCount int
		models      map[string]int64
	}
	services := make(map[string]*serviceAgg)
	var grandTotalTokens int64
	var grandTotalCost float64

	for _, t := range tenants {
		agg, ok := services[t.ServiceID]
		if !ok {
			agg = &serviceAgg{
				serviceID: t.ServiceID,
				models:    make(map[string]int64),
			}
			services[t.ServiceID] = agg
		}
		agg.totalTokens += t.TotalTokens
		agg.totalCost += t.TotalCost
		agg.tenantCount++
		grandTotalTokens += t.TotalTokens
		grandTotalCost += t.TotalCost

		for mName, m := range t.Models {
			agg.models[mName] += m.TotalTokens
		}
	}

	// 3. レポートメッセージの構築
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("対象月: %s\n", lastMonth))
	sb.WriteString(fmt.Sprintf("全サービス合計消費トークン: %s トークン\n", formatTokens(grandTotalTokens)))
	sb.WriteString(fmt.Sprintf("全サービス合計概算コスト: $%.4f USD\n", grandTotalCost))
	sb.WriteString(fmt.Sprintf("アクティブサービス数: %d / アクティブテナント総数: %d\n\n", len(services), len(tenants)))
	sb.WriteString("━━━━━━━━ サービス別内訳 ━━━━━━━━\n")

	// サービス名順でソート
	var svcList []*serviceAgg
	for _, s := range services {
		svcList = append(svcList, s)
	}
	sort.Slice(svcList, func(i, j int) bool {
		return svcList[i].serviceID < svcList[j].serviceID
	})

	for _, s := range svcList {
		sb.WriteString(fmt.Sprintf("🔹 %s:\n", s.serviceID))
		sb.WriteString(fmt.Sprintf("   - テナント数: %d\n", s.tenantCount))
		sb.WriteString(fmt.Sprintf("   - トークン数: %s\n", formatTokens(s.totalTokens)))
		sb.WriteString(fmt.Sprintf("   - 概算コスト: $%.4f USD\n", s.totalCost))

		// モデル別
		if len(s.models) > 0 {
			var mNames []string
			for m := range s.models {
				mNames = append(mNames, m)
			}
			sort.Strings(mNames)
			var mSummary []string
			for _, m := range mNames {
				mSummary = append(mSummary, fmt.Sprintf("%s (%s)", m, formatTokens(s.models[m])))
			}
			sb.WriteString(fmt.Sprintf("   - モデル: %s\n", strings.Join(mSummary, ", ")))
		}
	}

	// 4. 通知送信 (DynamoDB / アプリ内通知 & ログ)
	title := fmt.Sprintf("📊 Kura 月次締め利用実績レポート (%s)", lastMonth)
	if err := u.notifier.Send(ctx, title, sb.String(), false); err != nil {
		log.Printf("[WARN] Failed to send monthly report notification: %v", err)
	}

	log.Printf("[INFO] [CRON] Monthly report for %s successfully completed.", lastMonth)
	return nil
}

// RunQuotaAlerts は当月のクォータ使用率が 80% / 90% を超えたテナントを検知して警告通知する
func (u *batchUseCase) RunQuotaAlerts(ctx context.Context) error {
	now := time.Now().In(entity.JST)
	currentMonth := entity.CurrentMonthJST()
	hourKey := now.Format("2006-01-02-15")
	lockKey := "quota_alert#" + hourKey

	// 1. 分散ロック取得 (1時間有効)
	acquired, err := u.usageStore.AcquireLock(ctx, lockKey, 3600)
	if err != nil {
		return fmt.Errorf("failed to acquire lock for quota alert: %w", err)
	}
	if !acquired {
		return nil
	}

	log.Printf("[INFO] [CRON] Acquired lock for quota alerts (%s). Scanning usages...", hourKey)

	// 2. 当月分の全利用実績を取得
	tenants, err := u.usageStore.GetAllTenantsUsageByMonth(ctx, currentMonth)
	if err != nil {
		return fmt.Errorf("failed to fetch current month usage: %w", err)
	}

	// サービス単位で合算
	serviceUsageMap := make(map[string]float64)
	for _, t := range tenants {
		serviceUsageMap[t.ServiceID] += t.TotalCost
	}

	for serviceID, totalCost := range serviceUsageMap {
		cfg, err := u.costStore.GetServiceConfig(ctx, serviceID)
		if err != nil || cfg == nil {
			continue
		}
		if cfg.CostLimit <= 0 || cfg.EffectiveBillingType() != entity.BillingTypeCapped {
			continue
		}

		rate := (totalCost / cfg.CostLimit) * 100.0
		if rate >= 80.0 {
			urgency := "⚠️ 注意"
			if rate >= 100.0 {
				urgency = "🚨 制限到達"
			} else if rate >= 90.0 {
				urgency = "🚨 警告"
			}
			msg := fmt.Sprintf("[%s] サービス '%s' の月次予算消化率が %.1f%% に達しました。\n累計コスト: $%.4f / 上限: $%.2f",
				urgency, serviceID, rate, totalCost, cfg.CostLimit)
			_ = u.notifier.Send(ctx, fmt.Sprintf("Kura 予算クォータアラート (%s)", serviceID), msg, true)
		}
	}

	return nil
}

// RunReconciliation は集計結果ストアの実績からコスト管理ストアのカウンタを再構築・補正する
func (u *batchUseCase) RunReconciliation(ctx context.Context) error {
	currentMonth := entity.CurrentMonthJST()
	lockKey := "reconciliation#" + currentMonth

	// 分散ロック取得 (120秒有効)
	acquired, err := u.usageStore.AcquireLock(ctx, lockKey, 120)
	if err != nil {
		return fmt.Errorf("failed to acquire lock for reconciliation: %w", err)
	}
	if !acquired {
		log.Printf("[INFO] [RECONCILE] Reconciliation for %s is already running on another node. Skipping.", currentMonth)
		return nil
	}
	defer func() {
		_ = u.usageStore.ReleaseLock(ctx, lockKey)
	}()

	log.Printf("[INFO] [RECONCILE] Starting reconciliation for month %s...", currentMonth)

	tenants, err := u.usageStore.GetAllTenantsUsageByMonth(ctx, currentMonth)
	if err != nil {
		return fmt.Errorf("failed to fetch monthly usage for reconciliation: %w", err)
	}

	serviceCosts := make(map[string]float64)
	serviceTokens := make(map[string]int64)

	reconciledTenants := 0
	for _, t := range tenants {
		// テナント個別のコストカウンタをリセット
		if err := u.costStore.ResetCost(ctx, t.ServiceID, t.TenantID, currentMonth, t.TotalCost, t.TotalTokens); err != nil {
			log.Printf("[WARN] [RECONCILE] Failed to reset tenant cost for %s/%s: %v", t.ServiceID, t.TenantID, err)
		} else {
			reconciledTenants++
		}
		serviceCosts[t.ServiceID] += t.TotalCost
		serviceTokens[t.ServiceID] += t.TotalTokens
	}

	// サービス全体のコストカウンタをリセット
	for svcID, cost := range serviceCosts {
		tokens := serviceTokens[svcID]
		if err := u.costStore.ResetCost(ctx, svcID, "", currentMonth, cost, tokens); err != nil {
			log.Printf("[WARN] [RECONCILE] Failed to reset service cost for %s: %v", svcID, err)
		}
	}

	log.Printf("[INFO] [RECONCILE] Successfully reconciled %d tenants across %d services for month %s.",
		reconciledTenants, len(serviceCosts), currentMonth)
	return nil
}

func formatTokens(n int64) string {
	in := fmt.Sprintf("%d", n)
	out := make([]byte, len(in)+(len(in)-1)/3)
	for i, j, k := len(in)-1, len(out)-1, 0; i >= 0; i, j, k = i-1, j-1, k+1 {
		if k > 0 && k%3 == 0 {
			out[j] = ','
			j--
		}
		out[j] = in[i]
	}
	return string(out)
}
