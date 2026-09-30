package scheduler

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/usecase"
	"github.com/robfig/cron/v3"
)

// CronScheduler は Go サーバー内蔵の定期バッチスケジューラー
type CronScheduler struct {
	cron     *cron.Cron
	batchUC  usecase.BatchUseCase
	stopChan chan struct{}
}

// NewCronScheduler は JST ロケーションでスケジューラーを初期化する
func NewCronScheduler(batchUC usecase.BatchUseCase) *CronScheduler {
	return NewCronSchedulerWithReconcile(batchUC, 0)
}

// NewCronSchedulerWithReconcile は定期補正間隔（秒）を指定してスケジューラーを初期化する
func NewCronSchedulerWithReconcile(batchUC usecase.BatchUseCase, reconcileIntervalSeconds int) *CronScheduler {
	// 日本標準時 (JST) で cron を設定
	c := cron.New(cron.WithLocation(entity.JST))

	s := &CronScheduler{
		cron:     c,
		batchUC:  batchUC,
		stopChan: make(chan struct{}),
	}

	// 1. 月次締めレポート通知: 毎月1日 00:05 JST
	_, err := c.AddFunc("5 0 1 * *", func() {
		log.Println("[CRON] Triggered monthly usage report batch...")
		ctx := context.Background()
		if err := batchUC.RunMonthlyReport(ctx); err != nil {
			log.Printf("[ERROR] [CRON] Monthly report failed: %v", err)
		}
	})
	if err != nil {
		log.Printf("[ERROR] Failed to register monthly report cron: %v", err)
	}

	// 2. クォータ警告検知: 毎時 00 分 JST
	_, err = c.AddFunc("0 * * * *", func() {
		log.Println("[CRON] Triggered quota threshold alert batch...")
		ctx := context.Background()
		if err := batchUC.RunQuotaAlerts(ctx); err != nil {
			log.Printf("[ERROR] [CRON] Quota alerts check failed: %v", err)
		}
	})
	if err != nil {
		log.Printf("[ERROR] Failed to register quota alert cron: %v", err)
	}

	// 3. 定期補正 (Reconciliation): 指定間隔 (0なら無効)
	if reconcileIntervalSeconds > 0 {
		spec := fmt.Sprintf("@every %ds", reconcileIntervalSeconds)
		_, err = c.AddFunc(spec, func() {
			log.Println("[CRON] Triggered scheduled counter reconciliation...")
			ctx := context.Background()
			if err := batchUC.RunReconciliation(ctx); err != nil {
				log.Printf("[ERROR] [CRON] Scheduled reconciliation failed: %v", err)
			}
		})
		if err != nil {
			log.Printf("[ERROR] Failed to register reconciliation cron: %v", err)
		}

		// 起動時の初期補正を非同期実行
		go func() {
			time.Sleep(2 * time.Second) // 起動直後のトラフィック安定待ち
			log.Println("[INFO] Running initial startup counter reconciliation...")
			ctx := context.Background()
			if err := batchUC.RunReconciliation(ctx); err != nil {
				log.Printf("[WARN] Initial startup reconciliation warning: %v", err)
			}
		}()
	}

	return s
}

// Start はスケジューラーをバックグラウンドで開始する
func (s *CronScheduler) Start() {
	log.Println("[INFO] Starting internal cron scheduler (JST timezone)...")
	s.cron.Start()
}

// Stop はスケジューラーを安全に停止する
func (s *CronScheduler) Stop() {
	log.Println("[INFO] Stopping internal cron scheduler...")
	ctx := s.cron.Stop()
	<-ctx.Done()
	log.Println("[INFO] Internal cron scheduler stopped.")
}
