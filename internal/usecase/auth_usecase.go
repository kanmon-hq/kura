package usecase

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/repository"
)

// AuthResult は認証・クォータ検査後の結果およびレスポンスヘッダー用データ
type AuthResult struct {
	TenantContext        *entity.TenantContext
	BillingType          string // "payg" | "capped"
	QuotaLimitTokens     string // 数値文字列 または "unlimited"
	QuotaRemainingTokens string // 数値文字列 または "unlimited"
	MonthlyUsageTokens   int64
	MonthlyUsageCost     float64
}

// AuthUseCase は認証およびクォータ制御のビジネスロジックを担うインターフェース
type AuthUseCase interface {
	// AuthenticateRequest は HTTP リクエストヘッダーからサービス・テナント情報を解決し、クォータ判定を行う
	AuthenticateRequest(ctx context.Context, r *http.Request) (*AuthResult, *entity.StandardErrorResponse)

	// GetKeyUsageSummary は指定された TenantContext の当月消費量とリアルタイム残枠サマリを取得する
	GetKeyUsageSummary(ctx context.Context, tenantCtx *entity.TenantContext) (*entity.KeyUsageSummary, error)
}

// AuthUseCaseConfig は認証ユースケースの設定オプション
type AuthUseCaseConfig struct {
	EnforceTollgateAuth bool
}

type authUseCase struct {
	costStore  repository.CostStore
	usageStore repository.UsageStore
	cfg        AuthUseCaseConfig
}

type negativeCacher interface {
	IsNegativeCached(serviceID, tenantID string) bool
	MarkNegativeCached(serviceID, tenantID string, now time.Time)
}

// NewAuthUseCase はデフォルト設定で AuthUseCase を生成する
func NewAuthUseCase(costStore repository.CostStore, usageStore repository.UsageStore) AuthUseCase {
	return NewAuthUseCaseWithConfig(costStore, usageStore, AuthUseCaseConfig{
		EnforceTollgateAuth: false,
	})
}

// NewAuthUseCaseWithConfig は指定された設定で AuthUseCase を生成する
func NewAuthUseCaseWithConfig(costStore repository.CostStore, usageStore repository.UsageStore, cfg AuthUseCaseConfig) AuthUseCase {
	return &authUseCase{
		costStore:  costStore,
		usageStore: usageStore,
		cfg:        cfg,
	}
}

func (u *authUseCase) AuthenticateRequest(
	ctx context.Context,
	r *http.Request,
) (*AuthResult, *entity.StandardErrorResponse) {
	// 1. テナント・ユーザー・サービス識別子およびメタデータの解決 (HTTP ヘッダー)
	serviceID := getHeaderFast(r.Header, "X-Service-Id")
	if serviceID == "" {
		serviceID = getHeaderFast(r.Header, "X-Consumer-Id")
	}
	tenantID := getHeaderFast(r.Header, "X-Tenant-Id")
	userID := getHeaderFast(r.Header, "X-User-Id")
	keyID := getHeaderFast(r.Header, "X-Key-Id")
	keyPrefix := getHeaderFast(r.Header, "X-Key-Prefix")
	dataResidency := getHeaderFast(r.Header, "X-Data-Residency")
	environment := getHeaderFast(r.Header, "X-Environment")
	feature := getHeaderFast(r.Header, "X-Feature")
	tagsHeader := getHeaderFast(r.Header, "X-Tags")

	// サービス識別子の必須検証 (トレーサビリティ担保のため暗黙のフォールバックは行わず 400 で即時拒否)
	if serviceID == "" {
		return nil, entity.NewStandardError(
			http.StatusBadRequest,
			entity.ErrorTypeInvalidRequest,
			"X-Service-ID header is required",
			"missing_service_id",
		)
	}

	// テナント識別子の必須検証 (トレーサビリティ担保のため暗黙のフォールバックは行わず 400 で即時拒否)
	if tenantID == "" {
		return nil, entity.NewStandardError(
			http.StatusBadRequest,
			entity.ErrorTypeInvalidRequest,
			"X-Tenant-ID header is required",
			"missing_tenant_id",
		)
	}

	// Tollgate 等のプロキシ経由フラグの判定
	isProxied := keyID != ""

	// 認証強制モード時のバリデーション
	if u.cfg.EnforceTollgateAuth && !isProxied {
		return nil, entity.NewStandardError(
			http.StatusUnauthorized,
			entity.ErrorTypeUnauthorized,
			"Gateway proxy authentication is enforced but required header (X-Key-ID) is missing or empty",
			"missing_gateway_headers",
		)
	}

	if dataResidency == "" {
		dataResidency = "global"
	}

	tenantCtx := &entity.TenantContext{
		ServiceID:     serviceID,
		TenantID:      tenantID,
		UserID:        userID,
		KeyID:         keyID,
		KeyPrefix:     keyPrefix,
		IsProxied:     isProxied,
		DataResidency: strings.ToLower(dataResidency),
		Environment:   environment,
		Feature:       feature,
	}
	if tagsHeader != "" {
		tenantCtx.Tags = parseTagsHeader(tagsHeader)
	}

	// 2. ネガティブキャッシュ（遮断済みテナント）の高速判定 (ホットパス最適化)
	var negCacher negativeCacher
	if nc, ok := u.costStore.(negativeCacher); ok {
		negCacher = nc
		if negCacher.IsNegativeCached(tenantCtx.ServiceID, tenantCtx.TenantID) {
			return nil, entity.NewStandardError(
				http.StatusTooManyRequests,
				entity.ErrorTypeQuotaExceeded,
				fmt.Sprintf("Monthly cost quota exceeded for tenant %s (service %s) [cached negative]",
					tenantCtx.TenantID, tenantCtx.ServiceID),
				"quota_exceeded",
			)
		}
	}

	// 3. サービス全体の当月累計利用量およびリミットを取得 (日本時間基準)
	currentMonth := entity.CurrentMonthJST()

	totalCost, totalTokens, _ := u.costStore.GetServiceCost(ctx, tenantCtx.ServiceID, currentMonth)

	billingType := string(entity.BillingTypePAYG)
	costLimit := float64(0)

	if svcConfig, _ := u.costStore.GetServiceConfig(ctx, tenantCtx.ServiceID); svcConfig != nil {
		costLimit = svcConfig.CostLimit
		billingType = string(svcConfig.EffectiveBillingType())
	}

	// 4. サービス全体の月次コスト上限超過判定 (ソフトリミット)
	if costLimit > 0 && totalCost >= costLimit {
		return nil, entity.NewStandardError(
			http.StatusTooManyRequests,
			entity.ErrorTypeQuotaExceeded,
			fmt.Sprintf("Monthly cost quota exceeded for service %s. Limit: $%.2f, Total Used: $%.4f",
				tenantCtx.ServiceID, costLimit, totalCost),
			"quota_exceeded",
		)
	}

	// 5. テナント個別の月次コスト上限超過判定 (設定されている場合)
	if tenantCtx.TenantID != "" {
		if tenantCfg, _ := u.costStore.GetTenantConfig(ctx, tenantCtx.ServiceID, tenantCtx.TenantID); tenantCfg != nil && tenantCfg.CostLimit > 0 {
			tenantCost, _, _ := u.costStore.GetTenantCost(ctx, tenantCtx.ServiceID, tenantCtx.TenantID, currentMonth)
			if tenantCost >= tenantCfg.CostLimit {
				if negCacher != nil {
					negCacher.MarkNegativeCached(tenantCtx.ServiceID, tenantCtx.TenantID, time.Now())
				}
				return nil, entity.NewStandardError(
					http.StatusTooManyRequests,
					entity.ErrorTypeQuotaExceeded,
					fmt.Sprintf("Monthly cost quota exceeded for tenant %s (service %s). Limit: $%.2f, Total Used: $%.4f",
						tenantCtx.TenantID, tenantCtx.ServiceID, tenantCfg.CostLimit, tenantCost),
					"quota_exceeded",
				)
			}
		}
	}

	return &AuthResult{
		TenantContext:        tenantCtx,
		BillingType:          billingType,
		QuotaLimitTokens:     "unlimited",
		QuotaRemainingTokens: "unlimited",
		MonthlyUsageTokens:   totalTokens,
		MonthlyUsageCost:     totalCost,
	}, nil
}

// ⚡ Bolt Optimization: getHeaderFast avoids r.Header.Get() overhead.
// net/http already canonicalizes headers during parsing. By directly accessing the map,
// we skip the string allocations and overhead in net/textproto.CanonicalMIMEHeaderKey.
func getHeaderFast(h http.Header, key string) string {
	if v, ok := h[key]; ok && len(v) > 0 {
		return v[0]
	}
	return ""
}

// parseTagsHeader はカンマ区切りのタグヘッダー（例: "env=prod,team=alpha,experiment"）を map に変換する
func parseTagsHeader(header string) map[string]string {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}

	count := strings.Count(header, ",") + 1
	tags := make(map[string]string, count)

	for len(header) > 0 {
		var p string
		if idx := strings.IndexByte(header, ','); idx >= 0 {
			p = header[:idx]
			header = header[idx+1:]
		} else {
			p = header
			header = ""
		}

		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		if key, val, found := strings.Cut(p, "="); found {
			tags[strings.TrimSpace(key)] = strings.TrimSpace(val)
		} else {
			tags[p] = "true"
		}
	}
	return tags
}

func (u *authUseCase) GetKeyUsageSummary(ctx context.Context, tenantCtx *entity.TenantContext) (*entity.KeyUsageSummary, error) {
	if tenantCtx == nil {
		return nil, fmt.Errorf("tenant context is required")
	}

	currentMonth := entity.CurrentMonthJST()

	// 1. サービス全体の当月利用実績・上限取得
	serviceTotalCost, totalTokens, _ := u.costStore.GetServiceCost(ctx, tenantCtx.ServiceID, currentMonth)

	billingType := string(entity.BillingTypePAYG)
	serviceCostLimit := float64(0)
	promptTokens := int64(0)
	completionTokens := int64(0)

	if svcConfig, _ := u.costStore.GetServiceConfig(ctx, tenantCtx.ServiceID); svcConfig != nil {
		serviceCostLimit = svcConfig.CostLimit
		billingType = string(svcConfig.EffectiveBillingType())
	}

	// 2. テナント個別の詳細内訳 (モデル別) を取得
	var tenantCostLimit float64
	var tenantRemaining float64 = -1
	if tenantCtx.TenantID != "" {
		if tCfg, _ := u.costStore.GetTenantConfig(ctx, tenantCtx.ServiceID, tenantCtx.TenantID); tCfg != nil {
			tenantCostLimit = tCfg.CostLimit
			tCost, _, _ := u.costStore.GetTenantCost(ctx, tenantCtx.ServiceID, tenantCtx.TenantID, currentMonth)
			if tenantCostLimit > 0 {
				tenantRemaining = tenantCostLimit - tCost
				if tenantRemaining < 0 {
					tenantRemaining = 0
				}
			}
		}

		usage, err := u.usageStore.GetTenantUsage(ctx, tenantCtx.ServiceID, tenantCtx.TenantID, currentMonth)
		if err == nil && usage != nil {
			for _, mu := range usage.Models {
				promptTokens += mu.PromptTokens
				completionTokens += mu.CompletionTokens
			}
		}
	}

	var serviceRemaining float64 = -1
	if serviceCostLimit > 0 {
		serviceRemaining = serviceCostLimit - serviceTotalCost
		if serviceRemaining < 0 {
			serviceRemaining = 0
		}
	}

	isExceeded := (serviceCostLimit > 0 && serviceTotalCost >= serviceCostLimit) ||
		(tenantCostLimit > 0 && (tenantCostLimit-tenantRemaining) >= tenantCostLimit)

	return &entity.KeyUsageSummary{
		ServiceID:           tenantCtx.ServiceID,
		TenantID:            tenantCtx.TenantID,
		Month:               currentMonth,
		BillingType:         billingType,
		ServiceCostLimitUSD: serviceCostLimit,
		ServiceTotalCostUSD: serviceTotalCost,
		ServiceRemainingUSD: serviceRemaining,
		TenantCostLimitUSD:  tenantCostLimit,
		TenantRemainingUSD:  tenantRemaining,
		TotalTokens:         totalTokens,
		PromptTokens:        promptTokens,
		CompletionTokens:    completionTokens,
		AllowedModels:       tenantCtx.AllowedModels,
		IsQuotaExceeded:     isExceeded,
	}, nil
}
