package http

import (
	"encoding/json"
	"net/http"

	"github.com/kanmon-hq/kura/internal/domain/entity"
)

// RespondJSON は JSON レスポンスを返すユーティリティ
func RespondJSON(w http.ResponseWriter, statusCode int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if payload != nil {
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			// エンコード失敗時は最低限のエラーを返す
			http.Error(w, `{"error":{"message":"Internal Server Error"}}`, http.StatusInternalServerError)
		}
	}
}

// WriteJSON は JSON レスポンスを出力する (互換性維持)
func WriteJSON(w http.ResponseWriter, status int, data any) {
	RespondJSON(w, status, data)
}

// WriteError は正規化された共通エラーレスポンスを出力する
func WriteError(w http.ResponseWriter, errResp *entity.StandardErrorResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(errResp.Err.Code)
	_ = json.NewEncoder(w).Encode(errResp)
}
