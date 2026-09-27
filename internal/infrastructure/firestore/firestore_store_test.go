package firestore_test

import (
	"context"
	"testing"
	"time"

	"github.com/northfieldzz/kura/internal/infrastructure/firestore"
)

func TestNewFirestoreStore_ContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	// 存在しないプロジェクトでの初期化タイムアウトまたは無効クレデンシャルのハンドリング
	_, _ = firestore.NewFirestoreStore(ctx, "test-project-nonexistent", "(default)")
}
