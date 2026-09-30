package notifier

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/repository"
)

// Notifier はアラート・レポート通知用インターフェース
type Notifier interface {
	Send(ctx context.Context, title, message string, isAlert bool) error
}

type internalNotifier struct {
	repo repository.UsageStore
}

// NewInternalNotifier はアプリ内部（UsageStore）に通知を保存し、標準ログにも出力する Notifier を生成する
func NewInternalNotifier(repo repository.UsageStore) Notifier {
	return &internalNotifier{
		repo: repo,
	}
}

func (n *internalNotifier) Send(ctx context.Context, title, message string, isAlert bool) error {
	level := "INFO"
	ntfType := entity.NotificationTypeInfo
	if isAlert {
		level = "WARN"
		ntfType = entity.NotificationTypeAlert
	} else if strings.Contains(title, "月次") || strings.Contains(strings.ToLower(title), "report") {
		ntfType = entity.NotificationTypeReport
	}

	log.Printf("[%s] [NOTIFICATION] %s\n%s", level, title, message)

	if n.repo != nil {
		id := fmt.Sprintf("ntf_%s", uuid.New().String()[:8])
		ntf := &entity.Notification{
			ID:        id,
			Type:      ntfType,
			Title:     title,
			Message:   message,
			IsAlert:   isAlert,
			CreatedAt: time.Now().UTC(),
		}
		if err := n.repo.SaveNotification(ctx, ntf); err != nil {
			log.Printf("[WARN] Failed to save internal notification: %v", err)
			return err
		}
	}
	return nil
}

type logNotifier struct{}

// NewLogNotifier は標準出力へ構造化ログとして通知を出力する汎用 Notifier を生成する
func NewLogNotifier() Notifier {
	return &logNotifier{}
}

func (l *logNotifier) Send(ctx context.Context, title, message string, isAlert bool) error {
	level := "INFO"
	if isAlert {
		level = "WARN"
	}
	log.Printf("[%s] [NOTIFICATION] %s\n%s", level, title, message)
	return nil
}
