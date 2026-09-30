package adapter_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/service"
	"github.com/kanmon-hq/kura/internal/infrastructure/adapter"
	"github.com/kanmon-hq/kura/internal/infrastructure/config"
)

func TestBedrockAdapter_PrepareRequest(t *testing.T) {
	cfg := &config.Config{
		BedrockRegion:   "us-east-1",
		BedrockAPIKey:   "test-bedrock-key",
		BedrockEndpoint: "https://bedrock-mantle.us-east-1.api.aws",
	}

	adp := adapter.NewBedrockAdapter(cfg)
	if adp.Provider() != service.ProviderBedrock {
		t.Fatalf("expected provider bedrock, got %s", adp.Provider())
	}
	if !adp.IsEnabled() {
		t.Fatalf("expected enabled adapter")
	}

	origReq := &entity.ChatCompletionRequest{
		Model: "bedrock/anthropic.claude-3-5-sonnet-20240620-v1:0",
		Messages: []entity.ChatMessage{
			{Role: "user", Content: "Hello Bedrock"},
		},
		Stream: true,
	}

	httpReq, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
	httpReq.Header.Set("X-Custom-Test", "test-value")

	prepared, err := adp.PrepareRequest(context.Background(), origReq, httpReq)
	if err != nil {
		t.Fatalf("PrepareRequest failed: %v", err)
	}

	if prepared.URL.String() != "https://bedrock-mantle.us-east-1.api.aws/v1/chat/completions" {
		t.Errorf("expected URL https://bedrock-mantle.us-east-1.api.aws/v1/chat/completions, got %s", prepared.URL.String())
	}
	if prepared.Header.Get("Authorization") != "Bearer test-bedrock-key" {
		t.Errorf("expected Authorization header, got %s", prepared.Header.Get("Authorization"))
	}
	if prepared.Header.Get("X-Custom-Test") != "test-value" {
		t.Errorf("expected X-Custom-Test header passed through")
	}
}

func TestBedrockAdapter_ExtractUsage(t *testing.T) {
	cfg := &config.Config{
		BedrockAPIKey: "key",
	}
	adp := adapter.NewBedrockAdapter(cfg)

	respJSON := []byte(`{"id":"chat-123","choices":[{"message":{"role":"assistant","content":"Hi"}}],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}`)
	usage, err := adp.ExtractUsageFromResponse(respJSON)
	if err != nil {
		t.Fatalf("ExtractUsageFromResponse failed: %v", err)
	}
	if usage == nil || usage.TotalTokens != 20 {
		t.Errorf("expected total tokens 20, got %+v", usage)
	}

	sseChunk := []byte(`data: {"id":"chat-123","choices":[],"usage":{"prompt_tokens":15,"completion_tokens":10,"total_tokens":25}}`)
	chunkUsage, err := adp.ExtractUsageFromChunk(sseChunk)
	if err != nil {
		t.Fatalf("ExtractUsageFromChunk failed: %v", err)
	}
	if chunkUsage == nil || chunkUsage.TotalTokens != 25 {
		t.Errorf("expected total tokens 25, got %+v", chunkUsage)
	}
}
