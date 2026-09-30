package usecase

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/service"
	"github.com/kanmon-hq/kura/internal/infrastructure/proxy"
)

// ChatUseCase はリクエストのプロバイダ解決および転送処理を行うインターフェース
type ChatUseCase interface {
	HandleChatCompletion(w http.ResponseWriter, r *http.Request, tenantCtx *entity.TenantContext, req *entity.ChatCompletionRequest)
	RegisterAdapter(prefix string, adapter service.Adapter)
	RegisterProviderAdapter(provider string, adapter service.Adapter)
	SetRouter(router *entity.Router)
	ResolveAdapter(model string) service.Adapter
}

type chatUseCase struct {
	defaultAdapter   service.Adapter
	adapters         map[string]service.Adapter
	providerAdapters map[string]service.Adapter
	router           *entity.Router
	proxy            *proxy.LLMProxy
}

// NewChatUseCase は ChatUseCase を生成する
func NewChatUseCase(
	defaultAdapter service.Adapter,
	proxy *proxy.LLMProxy,
) ChatUseCase {
	return &chatUseCase{
		defaultAdapter:   defaultAdapter,
		adapters:         make(map[string]service.Adapter),
		providerAdapters: make(map[string]service.Adapter),
		proxy:            proxy,
	}
}

func (u *chatUseCase) SetRouter(router *entity.Router) {
	u.router = router
}

func (u *chatUseCase) RegisterAdapter(prefix string, adapter service.Adapter) {
	u.adapters[strings.ToLower(prefix)] = adapter
	if adapter != nil {
		u.providerAdapters[strings.ToLower(string(adapter.Provider()))] = adapter
	}
}

func (u *chatUseCase) RegisterProviderAdapter(provider string, adapter service.Adapter) {
	u.providerAdapters[strings.ToLower(provider)] = adapter
}

func (u *chatUseCase) ResolveAdapter(model string) service.Adapter {
	lowerModel := strings.ToLower(model)
	for prefix, adapter := range u.adapters {
		if strings.HasPrefix(lowerModel, prefix) {
			return adapter
		}
	}
	return u.defaultAdapter
}

func (u *chatUseCase) ResolveProviderAdapter(provider string) service.Adapter {
	if adapter, ok := u.providerAdapters[strings.ToLower(provider)]; ok {
		return adapter
	}
	return u.defaultAdapter
}

func (u *chatUseCase) HandleChatCompletion(
	w http.ResponseWriter,
	r *http.Request,
	tenantCtx *entity.TenantContext,
	req *entity.ChatCompletionRequest,
) {
	// 仮想モデルエイリアスの解決 (fast -> gpt-5.4-mini 等)
	req.Model = entity.ResolveModelAlias(req.Model)

	// API キーに設定された許可モデル制限の検証
	if tenantCtx != nil && len(tenantCtx.AllowedModels) > 0 {
		if !entity.ValidateModelAccess(tenantCtx.AllowedModels, req.Model) {
			errResp := entity.NewStandardError(
				http.StatusForbidden,
				entity.ErrorTypeInvalidRequest,
				fmt.Sprintf("Model '%s' is not allowed for this API Key. Allowed models: %v", req.Model, tenantCtx.AllowedModels),
				"model_not_allowed",
			)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write(errResp.ToJSON())
			return
		}
	}

	// ルーターによる候補エンドポイントの解決
	if u.router != nil {
		dataResidency := ""
		if tenantCtx != nil && tenantCtx.DataResidency != "" {
			dataResidency = tenantCtx.DataResidency
		} else if r != nil {
			dataResidency = r.Header.Get("X-Data-Residency")
		}

		candidates := u.router.ResolveCandidates(req.Model, dataResidency)
		if len(candidates) > 0 {
			u.proxy.ServeForwardCandidates(
				w,
				r,
				tenantCtx,
				req,
				candidates,
				u.ResolveProviderAdapter,
				u.router.GetCircuitBreaker(),
			)
			return
		}
	}

	adapter := u.ResolveAdapter(req.Model)

	// プロバイダが未設定（キーやエンドポイント不足）の場合は即座に遮断
	if adapter == nil || !adapter.IsEnabled() {
		providerName := "unknown"
		if adapter != nil {
			providerName = string(adapter.Provider())
		}
		errResp := entity.NewStandardError(
			http.StatusBadRequest,
			entity.ErrorTypeInvalidRequest,
			fmt.Sprintf("Provider '%s' is not configured or disabled (missing API key or endpoint in environment variables)", providerName),
			"provider_disabled",
		)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(errResp.ToJSON())
		return
	}

	u.proxy.ServeForward(w, r, tenantCtx, req, adapter)
}
