package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/service"
	"github.com/kanmon-hq/kura/internal/infrastructure/config"
)

type bedrockAdapter struct {
	cfg *config.Config
}

// NewBedrockAdapter は Amazon Bedrock (bedrock-mantle) 用アダプターを生成する
func NewBedrockAdapter(cfg *config.Config) service.Adapter {
	return &bedrockAdapter{cfg: cfg}
}

func (a *bedrockAdapter) Provider() service.ProviderType {
	return service.ProviderBedrock
}

func (a *bedrockAdapter) IsEnabled() bool {
	return a.cfg.BedrockAPIKey != "" || a.cfg.BedrockEndpoint != ""
}

func (a *bedrockAdapter) PrepareRequest(ctx context.Context, origReq *entity.ChatCompletionRequest, httpReq *http.Request) (*http.Request, error) {
	return a.PrepareRequestWithEndpoint(ctx, origReq, httpReq, nil)
}

func (a *bedrockAdapter) PrepareRequestWithEndpoint(ctx context.Context, origReq *entity.ChatCompletionRequest, httpReq *http.Request, ep *entity.EndpointConfig) (*http.Request, error) {
	apiKey := a.cfg.BedrockAPIKey
	region := a.cfg.BedrockRegion
	baseEndpoint := a.cfg.BedrockEndpoint

	if ep != nil {
		if key := ep.GetResolvedKey(); key != "" {
			apiKey = key
		}
		if ep.Region != "" {
			region = ep.Region
		}
		if ep.URL != "" {
			baseEndpoint = ep.URL
		}
	}

	if region == "" {
		region = a.cfg.AWSRegion
	}
	if region == "" {
		region = "us-east-1"
	}

	if apiKey == "" && baseEndpoint == "" {
		return nil, fmt.Errorf("amazon bedrock (bedrock-mantle) is not configured (missing API key or endpoint)")
	}

	modelID := strings.TrimPrefix(origReq.Model, "bedrock/")
	if modelID == "" {
		modelID = "amazon.titan-text-express-v1"
	}
	origReq.Model = modelID

	cleanBase := strings.TrimRight(baseEndpoint, "/")
	if cleanBase == "" {
		cleanBase = fmt.Sprintf("https://bedrock-mantle.%s.api.aws", region)
	}

	var targetURL string
	if strings.HasSuffix(cleanBase, "/chat/completions") {
		targetURL = cleanBase
	} else if strings.HasSuffix(cleanBase, "/v1") {
		targetURL = fmt.Sprintf("%s/chat/completions", cleanBase)
	} else {
		targetURL = fmt.Sprintf("%s/v1/chat/completions", cleanBase)
	}

	// ストリーミング時に usage 情報を強制取得
	if origReq.Stream {
		if origReq.ExtraFields == nil {
			origReq.ExtraFields = make(map[string]any)
		}
		origReq.ExtraFields["stream_options"] = map[string]any{
			"include_usage": true,
		}
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
	if apiKey != "" {
		newReq.Header.Set("Authorization", "Bearer "+apiKey)
		newReq.Header.Set("x-api-key", apiKey)
	}

	// クライアントのカスタムヘッダーを透過
	for k, v := range httpReq.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-") {
			newReq.Header[k] = v
		}
	}

	return newReq, nil
}

func (a *bedrockAdapter) ExtractUsageFromResponse(body []byte) (*entity.UsageInfo, error) {
	var fastResp struct {
		Usage *entity.UsageInfo `json:"usage"`
	}
	if err := json.Unmarshal(body, &fastResp); err != nil {
		return nil, err
	}
	return fastResp.Usage, nil
}

var bedrockUsageBytes = []byte(`"usage"`)

func (a *bedrockAdapter) ExtractUsageFromChunk(chunk []byte) (*entity.UsageInfo, error) {
	if !bytes.Contains(chunk, bedrockUsageBytes) {
		return nil, nil
	}

	trimmed := bytes.TrimSpace(chunk)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return nil, nil
	}
	data := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
	if bytes.Equal(data, []byte("[DONE]")) || len(data) == 0 {
		return nil, nil
	}

	var fastChunk struct {
		Usage *entity.UsageInfo `json:"usage"`
	}
	if err := json.Unmarshal(data, &fastChunk); err != nil {
		return nil, nil
	}
	return fastChunk.Usage, nil
}

func (a *bedrockAdapter) NormalizeResponse(statusCode int, body []byte) ([]byte, error) {
	return body, nil
}

func (a *bedrockAdapter) NormalizeSSEChunk(chunk []byte) ([][]byte, error) {
	return [][]byte{chunk}, nil
}
