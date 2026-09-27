package usecase_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/northfieldzz/kura/internal/domain/entity"
	"github.com/northfieldzz/kura/internal/domain/service"
	"github.com/northfieldzz/kura/internal/infrastructure/proxy"
	"github.com/northfieldzz/kura/internal/usecase"
)

type dummyAdapter struct {
	provider service.ProviderType
	enabled  bool
}

func (d *dummyAdapter) Provider() service.ProviderType { return d.provider }
func (d *dummyAdapter) IsEnabled() bool                { return d.enabled }
func (d *dummyAdapter) PrepareRequest(ctx context.Context, origReq *entity.ChatCompletionRequest, httpReq *http.Request) (*http.Request, error) {
	return d.PrepareRequestWithEndpoint(ctx, origReq, httpReq, nil)
}
func (d *dummyAdapter) PrepareRequestWithEndpoint(ctx context.Context, origReq *entity.ChatCompletionRequest, httpReq *http.Request, ep *entity.EndpointConfig) (*http.Request, error) {
	return http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:0/chat/completions", nil)
}
func (d *dummyAdapter) ExtractUsageFromResponse(body []byte) (*entity.UsageInfo, error) {
	return nil, nil
}
func (d *dummyAdapter) ExtractUsageFromChunk(chunk []byte) (*entity.UsageInfo, error) {
	return nil, nil
}
func (d *dummyAdapter) NormalizeResponse(statusCode int, body []byte) ([]byte, error) {
	return body, nil
}
func (d *dummyAdapter) NormalizeSSEChunk(chunk []byte) ([][]byte, error) {
	return [][]byte{chunk}, nil
}

func TestChatUseCase_DisabledProvider(t *testing.T) {
	// Azure(GPT/Claude) と Gemini(GCP) のシミュレート
	openAI := &dummyAdapter{provider: service.ProviderAzure, enabled: false}
	gemini := &dummyAdapter{provider: service.ProviderGemini, enabled: false}

	llmProxy := proxy.NewLLMProxy(nil, nil, nil, nil)
	uc := usecase.NewChatUseCase(openAI, llmProxy)
	uc.RegisterAdapter("gemini", gemini)

	tenantCtx := &entity.TenantContext{ServiceID: "team-test", TenantID: "default"}

	// 1. 無効な Azure (Claude 含む) へのリクエスト
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	chatReq := &entity.ChatCompletionRequest{Model: "claude-3-5-sonnet"}

	uc.HandleChatCompletion(rec, req, tenantCtx, chatReq)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for disabled azure (claude), got %d", rec.Code)
	}

	var errResp entity.StandardErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to unmarshal error response: %v", err)
	}
	if errResp.Err.VendorOriginalCode != "provider_disabled" {
		t.Errorf("expected vendor_original_code 'provider_disabled', got %s", errResp.Err.VendorOriginalCode)
	}

	// 2. 動的登録した無効な Gemini (GCP) へのリクエスト
	rec2 := httptest.NewRecorder()
	chatReq2 := &entity.ChatCompletionRequest{Model: "gemini-1.5-flash"}
	uc.HandleChatCompletion(rec2, req, tenantCtx, chatReq2)

	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for disabled gemini, got %d", rec2.Code)
	}

	// 3. 仮想エイリアス "smart" (-> claude-3-5-sonnet) が default (openAIAdapter) に解決されること
	chatReq3 := &entity.ChatCompletionRequest{Model: "smart"}
	resolvedAdapter := uc.ResolveAdapter(entity.ResolveModelAlias(chatReq3.Model))
	if resolvedAdapter.Provider() != service.ProviderAzure {
		t.Errorf("expected smart to resolve to Azure provider, got %v", resolvedAdapter.Provider())
	}
}

func TestChatUseCase_AllowedModelsAccessControl(t *testing.T) {
	openAI := &dummyAdapter{provider: service.ProviderAzure, enabled: true}
	llmProxy := proxy.NewLLMProxy(nil, nil, nil, nil)
	uc := usecase.NewChatUseCase(openAI, llmProxy)

	tenantCtxWithRestrictions := &entity.TenantContext{
		ServiceID:     "team-test",
		TenantID:      "default",
		AllowedModels: []string{"gpt-4o-mini", "gemini-*"},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	// 1. 許可モデルへのリクエスト -> 403 にはならず進む (モックプロキシ呼出)
	// (dummyAdapterはPrepareRequestがnilを返すため400 Bad Requestになるが、403 Forbiddenにはならない)
	recAllowed := httptest.NewRecorder()
	chatReqAllowed := &entity.ChatCompletionRequest{Model: "gpt-4o-mini"}
	uc.HandleChatCompletion(recAllowed, req, tenantCtxWithRestrictions, chatReqAllowed)
	if recAllowed.Code == http.StatusForbidden {
		t.Errorf("allowed model should not receive 403 Forbidden")
	}

	// 2. 制限対象の高額モデルへのリクエスト -> 403 Forbidden で即座に遮断
	recBlocked := httptest.NewRecorder()
	chatReqBlocked := &entity.ChatCompletionRequest{Model: "claude-3-5-sonnet"}
	uc.HandleChatCompletion(recBlocked, req, tenantCtxWithRestrictions, chatReqBlocked)

	if recBlocked.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for restricted model, got %d", recBlocked.Code)
	}

	var errResp entity.StandardErrorResponse
	if err := json.Unmarshal(recBlocked.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if errResp.Err.VendorOriginalCode != "model_not_allowed" {
		t.Errorf("expected vendor_original_code 'model_not_allowed', got %s", errResp.Err.VendorOriginalCode)
	}
}

