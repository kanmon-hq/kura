package logger

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/service"
)

// ConsoleLogger はコンソール (stdout) に非同期でトークン利用量 JSON を出力するロガー (CloudWatch Logs 向け)
type ConsoleLogger struct {
	logChan chan entity.UsageLogEvent
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewConsoleLogger は ConsoleLogger を初期化し、バックグラウンドワーカーを起動する
func NewConsoleLogger(bufferSize int) service.UsageLogger {
	ctx, cancel := context.WithCancel(context.Background())
	l := &ConsoleLogger{
		logChan: make(chan entity.UsageLogEvent, bufferSize),
		ctx:     ctx,
		cancel:  cancel,
	}

	l.wg.Add(1)
	go l.worker()

	return l
}

func (l *ConsoleLogger) worker() {
	defer l.wg.Done()
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)

	for {
		select {
		case event, ok := <-l.logChan:
			if !ok {
				return
			}
			// 構造化JSONでコンソールに出力
			_ = enc.Encode(event)
		case <-l.ctx.Done():
			// 残りのチャネルデータをフラッシュ
			for {
				select {
				case event, ok := <-l.logChan:
					if !ok {
						return
					}
					_ = enc.Encode(event)
				default:
					return
				}
			}
		}
	}
}

// Log はメイン処理をブロックせずにチャネルへイベントを送出する
func (l *ConsoleLogger) Log(ctx context.Context, event entity.UsageLogEvent) {
	select {
	case l.logChan <- event:
	default:
		// バッファフル時はメイン処理の遅延を防ぐため標準エラーに警告出力してドロップ（オーバーヘッド極小化）
		fmt.Fprintf(os.Stderr, "[WARN] usage logger buffer full, dropped log for team: %s, model: %s\n", event.TeamID, event.Model)
	}
}

// Close はワーカーを安全に停止し、未送信のログを処理する
func (l *ConsoleLogger) Close() error {
	l.cancel()
	close(l.logChan)
	l.wg.Wait()
	return nil
}
