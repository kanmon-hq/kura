package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/repository"
	"github.com/kanmon-hq/kura/internal/infrastructure/websocket"
	"github.com/kanmon-hq/kura/internal/usecase"
)

// Handler は Gateway の HTTP リクエストハンドラ群
type Handler struct {
	chatUseCase    usecase.ChatUseCase
	realtimeProxy  *websocket.RealtimeProxy
	costStore      repository.CostStore
	usageStore     repository.UsageStore
	authUseCase    usecase.AuthUseCase
	isShuttingDown atomic.Bool
}

// NewHandler は Handler インスタンスを生成する
func NewHandler(chatUseCase usecase.ChatUseCase, realtimeProxy *websocket.RealtimeProxy, costStore repository.CostStore, usageStore repository.UsageStore, authUseCase ...usecase.AuthUseCase) *Handler {
	var auc usecase.AuthUseCase
	if len(authUseCase) > 0 {
		auc = authUseCase[0]
	}
	return &Handler{
		chatUseCase:   chatUseCase,
		realtimeProxy: realtimeProxy,
		costStore:     costStore,
		usageStore:    usageStore,
		authUseCase:   auc,
	}
}

// SetShuttingDown はシャットダウン状態を更新する (Readiness を 503 に切り替える)
func (h *Handler) SetShuttingDown(val bool) {
	h.isShuttingDown.Store(val)
}

// Liveness はプロセスの死活監視用エンドポイント (GET /livez)
// 外部依存関係を見ず、プロセスが稼働中であれば常に 200 OK を返す
func (h *Handler) Liveness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{
		"status": "alive",
	})
}

// Readiness はトラフィック受付準備完了の監視用エンドポイント (GET /readyz)
// 1. シャットダウン移行中の場合は即座に 503 を返し、ロードバランサに新規流入を止めさせる
// 2. DynamoDB / メモリストアの疎通を確認する
func (h *Handler) Readiness(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if h.isShuttingDown.Load() {
		WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":  "terminating",
			"message": "server is gracefully shutting down",
		})
		return
	}

	if h.costStore != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := h.costStore.Ping(ctx); err != nil {
			WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status":  "degraded",
				"message": "cost store health check failed",
				"error":   err.Error(),
			})
			return
		}
	}

	if h.usageStore != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := h.usageStore.Ping(ctx); err != nil {
			WriteJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status":  "degraded",
				"message": "usage store health check failed",
				"error":   err.Error(),
			})
			return
		}
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"status":   "ready",
		"database": "connected",
	})
}

// HealthCheck は総合ヘルスチェック用エンドポイント (GET /healthz)
func (h *Handler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	h.Readiness(w, r)
}

// ChatCompletions は OpenAI 互換のチャット補完エンドポイント (POST /v1/chat/completions)
func (h *Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, entity.NewStandardError(
			http.StatusMethodNotAllowed,
			entity.ErrorTypeInvalidRequest,
			"Method not allowed",
			"",
		))
		return
	}

	tenantCtx := GetTenantContextFromContext(r.Context())
	if tenantCtx == nil {
		WriteError(w, entity.NewStandardError(
			http.StatusUnauthorized,
			entity.ErrorTypeUnauthorized,
			"Unauthorized",
			"",
		))
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		WriteError(w, entity.NewStandardError(
			http.StatusBadRequest,
			entity.ErrorTypeInvalidRequest,
			"Failed to read request body",
			"",
		))
		return
	}
	defer r.Body.Close()

	var req entity.ChatCompletionRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		WriteError(w, entity.NewStandardError(
			http.StatusBadRequest,
			entity.ErrorTypeInvalidRequest,
			"Invalid JSON request: "+err.Error(),
			"",
		))
		return
	}

	if req.Model == "" {
		WriteError(w, entity.NewStandardError(
			http.StatusBadRequest,
			entity.ErrorTypeInvalidRequest,
			"model field is required",
			"",
		))
		return
	}

	h.chatUseCase.HandleChatCompletion(w, r, tenantCtx, &req)
}

// Realtime は WebSocket パススルーエンドポイント (GET /v1/realtime)
func (h *Handler) Realtime(w http.ResponseWriter, r *http.Request) {
	tenantCtx := GetTenantContextFromContext(r.Context())
	if tenantCtx == nil {
		WriteError(w, entity.NewStandardError(
			http.StatusUnauthorized,
			entity.ErrorTypeUnauthorized,
			"Unauthorized",
			"",
		))
		return
	}

	h.realtimeProxy.ServeWebSocket(w, r, tenantCtx)
}

// GetKeyUsage は認証されたキー/サービスの当月利用量とリアルタイム残枠サマリを返却する (GET /v1/usage)
func (h *Handler) GetKeyUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, entity.NewStandardError(http.StatusMethodNotAllowed, entity.ErrorTypeInvalidRequest, "Method not allowed", ""))
		return
	}

	tenantCtx := GetTenantContextFromContext(r.Context())
	if tenantCtx == nil {
		WriteError(w, entity.NewStandardError(
			http.StatusUnauthorized,
			entity.ErrorTypeUnauthorized,
			"Unauthorized",
			"missing_auth",
		))
		return
	}

	if h.authUseCase == nil {
		WriteError(w, entity.NewStandardError(
			http.StatusInternalServerError,
			entity.ErrorTypeInternalError,
			"Auth usecase not initialized",
			"",
		))
		return
	}

	summary, err := h.authUseCase.GetKeyUsageSummary(r.Context(), tenantCtx)
	if err != nil {
		WriteError(w, entity.NewStandardError(
			http.StatusInternalServerError,
			entity.ErrorTypeInternalError,
			err.Error(),
			"",
		))
		return
	}

	WriteJSON(w, http.StatusOK, summary)
}
