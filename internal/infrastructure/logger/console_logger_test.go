package logger_test

import (
	"context"
	"testing"
	"time"

	"github.com/northfieldzz/kura/internal/domain/entity"
	"github.com/northfieldzz/kura/internal/infrastructure/logger"
)

func TestConsoleLogger_LogAndClose(t *testing.T) {
	l := logger.NewConsoleLogger(10)
	ctx := context.Background()

	event := entity.UsageLogEvent{
		TeamID:           "test-team",
		Model:            "gpt-4o",
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		Cost:             0.0025,
		TenantID:         "tenant-1",
		Timestamp:        time.Now(),
	}

	// 正常ログ送出
	l.Log(ctx, event)

	// バッファを溢れさせてドロップログパスを通す
	smallLogger := logger.NewConsoleLogger(1)
	for i := 0; i < 100; i++ {
		smallLogger.Log(ctx, event)
	}

	if err := l.Close(); err != nil {
		t.Fatalf("expected nil error on Close, got: %v", err)
	}
	if err := smallLogger.Close(); err != nil {
		t.Fatalf("expected nil error on Close, got: %v", err)
	}
}
