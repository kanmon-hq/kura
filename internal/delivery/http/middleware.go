package http

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/infrastructure/metrics"
	"github.com/kanmon-hq/kura/internal/usecase"
)

type contextKey string

const (
	TenantContextKey    contextKey = "tenant_info"
	RequestIDContextKey contextKey = "request_id"
)

// GatewayAuthConfig はゲートウェイ共有シークレット認証の設定
type GatewayAuthConfig struct {
	SharedSecret         string
	SharedSecretPrevious string
	HeaderName           string
	InsecureNoAuth       bool
}

// AuthMiddleware はマルチテナント解決およびクォータ検証を行う HTTP ミドルウェア
type AuthMiddleware struct {
	authUseCase       usecase.AuthUseCase
	gatewayAuthConfig GatewayAuthConfig
	metrics           *metrics.Metrics
}

// NewAuthMiddleware は AuthMiddleware を生成する
func NewAuthMiddleware(authUseCase usecase.AuthUseCase, opts ...any) *AuthMiddleware {
	m := &AuthMiddleware{authUseCase: authUseCase}
	for _, opt := range opts {
		switch v := opt.(type) {
		case GatewayAuthConfig:
			m.gatewayAuthConfig = v
		case *metrics.Metrics:
			m.metrics = v
		}
	}
	if m.gatewayAuthConfig.HeaderName == "" {
		m.gatewayAuthConfig.HeaderName = "X-Gateway-Secret"
	}
	return m
}

// GatewayAuthConfig を返す
func (m *AuthMiddleware) GatewayAuthConfig() GatewayAuthConfig {
	return m.gatewayAuthConfig
}

// Metrics を返す
func (m *AuthMiddleware) Metrics() *metrics.Metrics {
	return m.metrics
}

// VerifyGatewaySecret はヘッダー値が現行または旧シークレットと定数時間一致するか検証する
func VerifyGatewaySecret(headerVal, currentSecret, previousSecret string) bool {
	if headerVal == "" {
		return false
	}
	if currentSecret != "" && subtle.ConstantTimeCompare([]byte(headerVal), []byte(currentSecret)) == 1 {
		return true
	}
	if previousSecret != "" && subtle.ConstantTimeCompare([]byte(headerVal), []byte(previousSecret)) == 1 {
		return true
	}
	return false
}

// Wrap は HTTP ハンドラを認証 & クォータ検証 & レスポンスヘッダー付与でラップする
func (m *AuthMiddleware) Wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Trace Context & Request ID
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = uuid.New().String()
		}
		w.Header().Set("X-Request-ID", requestID)

		// 2. ゲートウェイ共有シークレット認証 (INSECURE_NO_GATEWAY_AUTH=true でない限り必須)
		if !m.gatewayAuthConfig.InsecureNoAuth {
			headerName := m.gatewayAuthConfig.HeaderName
			secretHeader := r.Header.Get(headerName)
			if !VerifyGatewaySecret(secretHeader, m.gatewayAuthConfig.SharedSecret, m.gatewayAuthConfig.SharedSecretPrevious) {
				if m.metrics != nil {
					m.metrics.RecordGatewayAuthFailure()
				}
				WriteError(w, entity.NewStandardError(
					http.StatusUnauthorized,
					entity.ErrorTypeUnauthorized,
					"Unauthorized: invalid or missing gateway authentication",
					"unauthorized",
				))
				return
			}
		}

		// 3. 認証 & クォータ超過判定 (HTTP ヘッダーから解決)
		authResult, errResp := m.authUseCase.AuthenticateRequest(r.Context(), r)
		if errResp != nil {
			WriteError(w, errResp)
			return
		}

		// 4. 仕様書 2.2 節に基づくレスポンスヘッダーの付与
		w.Header().Set("X-Billing-Type", authResult.BillingType)
		w.Header().Set("X-Quota-Limit-Tokens", authResult.QuotaLimitTokens)
		w.Header().Set("X-Quota-Remaining-Tokens", authResult.QuotaRemainingTokens)
		w.Header().Set("X-Monthly-Usage-Tokens", strconv.FormatInt(authResult.MonthlyUsageTokens, 10))
		w.Header().Set("X-Monthly-Usage-Cost", fmt.Sprintf("$%.2f", authResult.MonthlyUsageCost))

		// 5. コンテキストに TenantContext と RequestID をセット
		ctx := context.WithValue(r.Context(), TenantContextKey, authResult.TenantContext)
		ctx = context.WithValue(ctx, RequestIDContextKey, requestID)

		next(w, r.WithContext(ctx))
	}
}

// GetTenantContextFromContext はコンテキストから TenantContext を取得する
func GetTenantContextFromContext(ctx context.Context) *entity.TenantContext {
	if val := ctx.Value(TenantContextKey); val != nil {
		if tc, ok := val.(*entity.TenantContext); ok {
			return tc
		}
	}
	return nil
}

// GetRequestIDFromContext はコンテキストから Request ID を取得する
func GetRequestIDFromContext(ctx context.Context) string {
	if val := ctx.Value(RequestIDContextKey); val != nil {
		if reqID, ok := val.(string); ok {
			return reqID
		}
	}
	return ""
}
