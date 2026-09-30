package http

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/usecase"
)


// AdminHandler は管理者向け API ハンドラ群
type AdminHandler struct {
	adminUseCase usecase.AdminUseCase
	batchUseCase usecase.BatchUseCase
	adminAPIKey  string
}

// NewAdminHandler は AdminHandler インスタンスを生成する
func NewAdminHandler(adminUseCase usecase.AdminUseCase, batchUseCase usecase.BatchUseCase, adminAPIKey string) *AdminHandler {
	return &AdminHandler{
		adminUseCase: adminUseCase,
		batchUseCase: batchUseCase,
		adminAPIKey:  adminAPIKey,
	}
}

// BatchUseCase は BatchUseCase を返す
func (h *AdminHandler) BatchUseCase() usecase.BatchUseCase {
	return h.batchUseCase
}

// UseCase は AdminUseCase を返す
func (h *AdminHandler) UseCase() usecase.AdminUseCase {
	return h.adminUseCase
}

// VerifyKey は渡されたキー文字列が管理者キーと一致するか検証する
func (h *AdminHandler) VerifyKey(key string) bool {
	if h.adminAPIKey == "" {
		return false // 未設定時はアクセス拒否
	}
	if strings.HasPrefix(key, "Bearer ") {
		key = strings.TrimPrefix(key, "Bearer ")
	}
	return subtle.ConstantTimeCompare([]byte(key), []byte(h.adminAPIKey)) == 1
}

// verifyAdminAuth は管理者キーを検証する (Authorization: Bearer <ADMIN_KEY>)
func (h *AdminHandler) verifyAdminAuth(r *http.Request) bool {
	if h.adminAPIKey == "" {
		return false // 未設定時はアクセス拒否
	}

	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		return h.VerifyKey(authHeader)
	}

	return false
}

// GetUsage は指定サービスの月次利用実績およびモデル別内訳を取得する (GET /v1/admin/usage)
func (h *AdminHandler) GetUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, entity.NewStandardError(http.StatusMethodNotAllowed, entity.ErrorTypeInvalidRequest, "Method not allowed", ""))
		return
	}

	if !h.verifyAdminAuth(r) {
		WriteError(w, entity.NewStandardError(http.StatusUnauthorized, entity.ErrorTypeUnauthorized, "Invalid or missing admin API key", "admin_unauthorized"))
		return
	}

	serviceID := r.URL.Query().Get("service_id")
	month := r.URL.Query().Get("month")

	report, err := h.adminUseCase.GetMonthlyUsage(r.Context(), serviceID, month)
	if err != nil {
		WriteError(w, entity.NewStandardError(http.StatusBadRequest, entity.ErrorTypeInvalidRequest, err.Error(), ""))
		return
	}

	WriteJSON(w, http.StatusOK, report)
}

// SetLimits はテナントのリミット設定を登録・更新する (POST /v1/admin/limits)
func (h *AdminHandler) SetLimits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, entity.NewStandardError(http.StatusMethodNotAllowed, entity.ErrorTypeInvalidRequest, "Method not allowed", ""))
		return
	}

	if !h.verifyAdminAuth(r) {
		WriteError(w, entity.NewStandardError(http.StatusUnauthorized, entity.ErrorTypeUnauthorized, "Invalid or missing admin API key", "admin_unauthorized"))
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		WriteError(w, entity.NewStandardError(http.StatusBadRequest, entity.ErrorTypeInvalidRequest, "Failed to read body", ""))
		return
	}
	defer r.Body.Close()

	var req usecase.SetLimitRequest
	if err := json.Unmarshal(body, &req); err != nil {
		WriteError(w, entity.NewStandardError(http.StatusBadRequest, entity.ErrorTypeInvalidRequest, "Invalid JSON: "+err.Error(), ""))
		return
	}

	if err := h.adminUseCase.SetTenantLimit(r.Context(), &req); err != nil {
		WriteError(w, entity.NewStandardError(http.StatusBadRequest, entity.ErrorTypeInvalidRequest, err.Error(), ""))
		return
	}

	WriteJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"message": "Tenant limit updated successfully",
	})
}

// ListNotifications は保存されたアプリ内通知一覧を取得する (GET /v1/admin/notifications)
func (h *AdminHandler) ListNotifications(w http.ResponseWriter, r *http.Request) {
	if !h.verifyAdminAuth(r) {
		WriteError(w, entity.NewStandardError(http.StatusUnauthorized, entity.ErrorTypeUnauthorized, "Invalid or missing admin API key", "admin_unauthorized"))
		return
	}

	limit := 20
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if val, err := strconv.ParseInt(limitStr, 10, 32); err == nil && val > 0 {
			limit = int(val)
		}
	}

	notifications, err := h.adminUseCase.ListNotifications(r.Context(), limit)
	if err != nil {
		WriteError(w, entity.NewStandardError(http.StatusBadRequest, entity.ErrorTypeInvalidRequest, err.Error(), ""))
		return
	}

	WriteJSON(w, http.StatusOK, map[string]any{
		"notifications": notifications,
	})
}
