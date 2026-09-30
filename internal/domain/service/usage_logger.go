package service

import (
	"context"

	"github.com/kanmon-hq/kura/internal/domain/entity"
)

// UsageLogger はトークン利用量ログを非同期に収集・送出するインターフェース
type UsageLogger interface {
	// Log はトークン使用量イベントを非同期キューへ送出する（ノンブロッキング）
	Log(ctx context.Context, event entity.UsageLogEvent)

	// Close はバッファされたログをフラッシュし、リソースを解放する
	Close() error
}
