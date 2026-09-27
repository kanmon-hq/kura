package adapter

import (
	"context"
	"net/http"
	"testing"

	"github.com/northfieldzz/kura/internal/domain/entity"
	"github.com/northfieldzz/kura/internal/infrastructure/config"
)

func TestGeminiAdapter_PrepareRequest(t *testing.T) {
	cfg := &config.Config{
		GeminiAPIKey:  "test-gemini-key",
		GeminiBaseURL: "https://generativelanguage.googleapis.com",
	}
	ad := NewGeminiAdapter(cfg)

	if !ad.IsEnabled() {
		t.Errorf("expected adapter to be enabled")
	}

	reqObj := &entity.ChatCompletionRequest{
		Model: "gemini-1.5-pro",
		Messages: []entity.ChatMessage{
			{Role: "user", Content: "Hello"},
		},
		Stream: false,
	}

	httpReq, err := http.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("failed to create http request: %v", err)
	}

	targetReq, err := ad.PrepareRequest(context.Background(), reqObj, httpReq)
	if err != nil {
		t.Fatalf("PrepareRequest failed: %v", err)
	}

	if targetReq.Header.Get("Authorization") != "Bearer test-gemini-key" {
		t.Errorf("unexpected Authorization header: %s", targetReq.Header.Get("Authorization"))
	}
	if targetReq.Header.Get("x-goog-api-key") != "test-gemini-key" {
		t.Errorf("unexpected x-goog-api-key header: %s", targetReq.Header.Get("x-goog-api-key"))
	}
	if targetReq.URL.Path != "/v1beta/openai/chat/completions" {
		t.Errorf("unexpected URL path: %s", targetReq.URL.Path)
	}

	// Dynamic endpoint override
	ep := &entity.EndpointConfig{
		URL: "https://custom-gemini.proxy.com",
		Key: "custom-key-xyz",
	}
	targetReq2, err := ad.PrepareRequestWithEndpoint(context.Background(), reqObj, httpReq, ep)
	if err != nil {
		t.Fatalf("PrepareRequestWithEndpoint failed: %v", err)
	}
	if targetReq2.Header.Get("Authorization") != "Bearer custom-key-xyz" {
		t.Errorf("unexpected custom auth header: %s", targetReq2.Header.Get("Authorization"))
	}
	if targetReq2.URL.Host != "custom-gemini.proxy.com" {
		t.Errorf("unexpected target host: %s", targetReq2.URL.Host)
	}
}
