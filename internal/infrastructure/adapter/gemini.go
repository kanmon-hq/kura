package adapter

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/service"
	"github.com/kanmon-hq/kura/internal/infrastructure/config"
)

type geminiAdapter struct {
	cfg           *config.Config
	openaiAdapter service.Adapter
}

// NewGeminiAdapter は Google Gemini 用アダプターを生成する
// Gemini は v1beta/openai/chat/completions による公式 OpenAI 互換インターフェースをサポートしている
func NewGeminiAdapter(cfg *config.Config) service.Adapter {
	return &geminiAdapter{
		cfg:           cfg,
		openaiAdapter: NewOpenAIAdapter(cfg),
	}
}

func (a *geminiAdapter) Provider() service.ProviderType {
	return service.ProviderGemini
}

func (a *geminiAdapter) IsEnabled() bool {
	return a.cfg.GeminiAPIKey != ""
}

func (a *geminiAdapter) PrepareRequest(ctx context.Context, origReq *entity.ChatCompletionRequest, httpReq *http.Request) (*http.Request, error) {
	return a.PrepareRequestWithEndpoint(ctx, origReq, httpReq, nil)
}

func (a *geminiAdapter) PrepareRequestWithEndpoint(ctx context.Context, origReq *entity.ChatCompletionRequest, httpReq *http.Request, ep *entity.EndpointConfig) (*http.Request, error) {
	apiKey := a.cfg.GeminiAPIKey
	baseURL := a.cfg.GeminiBaseURL

	if ep != nil {
		if resolvedKey := ep.GetResolvedKey(); resolvedKey != "" {
			apiKey = resolvedKey
		}
		if ep.URL != "" {
			baseURL = ep.URL
		}
	}

	if apiKey == "" {
		return nil, fmt.Errorf("gemini API key is not configured")
	}
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	// Gemini のモデル名正規化 (gemini/ プレフィックス除去)
	model := strings.TrimPrefix(origReq.Model, "gemini/")
	if model == "" {
		model = "gemini-1.5-flash"
	}
	origReq.Model = model

	// OpenAI 互換エンドポイント URL の構築
	var targetURL string
	if strings.Contains(baseURL, "/chat/completions") {
		targetURL = baseURL
	} else if strings.HasSuffix(baseURL, "/openai") || strings.HasSuffix(baseURL, "/v1beta/openai") {
		targetURL = fmt.Sprintf("%s/chat/completions", baseURL)
	} else {
		targetURL = fmt.Sprintf("%s/v1beta/openai/chat/completions", baseURL)
	}

	reqBody, err := origReq.ToMergedJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to prepare request body: %w", err)
	}

	newReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}

	newReq.Header.Set("Content-Type", "application/json")
	newReq.Header.Set("Authorization", "Bearer "+apiKey)
	newReq.Header.Set("x-goog-api-key", apiKey)

	// クライアントが送信した X- などのカスタムヘッダーを透過
	for k, v := range httpReq.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-") {
			newReq.Header[k] = v
		}
	}

	return newReq, nil
}

func (a *geminiAdapter) ExtractUsageFromResponse(body []byte) (*entity.UsageInfo, error) {
	return a.openaiAdapter.ExtractUsageFromResponse(body)
}

func (a *geminiAdapter) ExtractUsageFromChunk(chunk []byte) (*entity.UsageInfo, error) {
	return a.openaiAdapter.ExtractUsageFromChunk(chunk)
}

func (a *geminiAdapter) NormalizeResponse(statusCode int, body []byte) ([]byte, error) {
	return a.openaiAdapter.NormalizeResponse(statusCode, body)
}

func (a *geminiAdapter) NormalizeSSEChunk(chunk []byte) ([][]byte, error) {
	return a.openaiAdapter.NormalizeSSEChunk(chunk)
}
