package adapter_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/infrastructure/adapter"
	"github.com/kanmon-hq/kura/internal/infrastructure/config"
)

func TestOpenAIAdapter_AzureEndpoint(t *testing.T) {
	cfg := &config.Config{
		AzureOpenAIEndpoint:    "https://my-azure-resource.openai.azure.com",
		AzureOpenAIAPIKey:      "test-azure-key",
		AzureAPIVersion:        "2024-02-15-preview",
		AzureDefaultDeployment: "gpt-4o-default",
	}

	adp := adapter.NewOpenAIAdapter(cfg)

	// 1. モデル名を指定した場合 (プレフィックスなし)
	req := &entity.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []entity.ChatMessage{
			{Role: "user", Content: "Hello"},
		},
	}

	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	targetReq, err := adp.PrepareRequest(context.Background(), req, httpReq)
	if err != nil {
		t.Fatalf("PrepareRequest error: %v", err)
	}

	expectedURL := "https://my-azure-resource.openai.azure.com/openai/deployments/gpt-4o/chat/completions?api-version=2024-02-15-preview"
	if targetReq.URL.String() != expectedURL {
		t.Errorf("expected URL %s, got %s", expectedURL, targetReq.URL.String())
	}
	if targetReq.Header.Get("api-key") != "test-azure-key" {
		t.Errorf("expected api-key header to be test-azure-key")
	}

	// 2. azure/ プレフィックス付きの場合
	req2 := &entity.ChatCompletionRequest{
		Model: "azure/custom-deploy",
		Messages: []entity.ChatMessage{
			{Role: "user", Content: "Hello"},
		},
	}
	targetReq2, err := adp.PrepareRequest(context.Background(), req2, httpReq)
	if err != nil {
		t.Fatalf("PrepareRequest error: %v", err)
	}
	expectedURL2 := "https://my-azure-resource.openai.azure.com/openai/deployments/custom-deploy/chat/completions?api-version=2024-02-15-preview"
	if targetReq2.URL.String() != expectedURL2 {
		t.Errorf("expected URL %s, got %s", expectedURL2, targetReq2.URL.String())
	}

	// 3. Azure AI Foundry (models.ai.azure.com) の場合
	cfgFoundry := &config.Config{
		AzureOpenAIEndpoint: "https://my-foundry-model.eastus2.models.ai.azure.com",
		AzureOpenAIAPIKey:   "foundry-key",
	}
	adpFoundry := adapter.NewOpenAIAdapter(cfgFoundry)
	req3 := &entity.ChatCompletionRequest{
		Model: "gpt-5.4-mini",
		Messages: []entity.ChatMessage{
			{Role: "user", Content: "Hello"},
		},
	}
	targetReq3, err := adpFoundry.PrepareRequest(context.Background(), req3, httpReq)
	if err != nil {
		t.Fatalf("PrepareRequest Foundry error: %v", err)
	}
	expectedFoundryURL := "https://my-foundry-model.eastus2.models.ai.azure.com/v1/chat/completions"
	if targetReq3.URL.String() != expectedFoundryURL {
		t.Errorf("expected Foundry URL %s, got %s", expectedFoundryURL, targetReq3.URL.String())
	}
	if targetReq3.Header.Get("api-key") != "foundry-key" {
		t.Errorf("expected api-key header")
	}
	if targetReq3.Header.Get("Authorization") != "Bearer foundry-key" {
		t.Errorf("expected Bearer Authorization header")
	}

	// 4. Azure OpenAI /openai/v1 互換パスの場合
	cfgV1 := &config.Config{
		AzureOpenAIEndpoint: "https://sample-foundry.openai.azure.com/openai/v1",
		AzureOpenAIAPIKey:   "azure-key",
	}
	adpV1 := adapter.NewOpenAIAdapter(cfgV1)
	req4 := &entity.ChatCompletionRequest{
		Model: "gpt-5.4-mini",
		Messages: []entity.ChatMessage{
			{Role: "user", Content: "Hello"},
		},
	}
	targetReq4, err := adpV1.PrepareRequest(context.Background(), req4, httpReq)
	if err != nil {
		t.Fatalf("PrepareRequest V1 error: %v", err)
	}
	expectedV1URL := "https://sample-foundry.openai.azure.com/openai/v1/chat/completions"
	if targetReq4.URL.String() != expectedV1URL {
		t.Errorf("expected V1 URL %s, got %s", expectedV1URL, targetReq4.URL.String())
	}
}
