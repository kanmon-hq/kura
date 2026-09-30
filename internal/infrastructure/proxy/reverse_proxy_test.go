package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/service"
	"github.com/kanmon-hq/kura/internal/infrastructure/proxy"
)

// mockTestAdapter はテスト用のアダプター
type mockTestAdapter struct {
	targetURL string
}

func (m *mockTestAdapter) Provider() service.ProviderType { return service.ProviderOpenAI }
func (m *mockTestAdapter) IsEnabled() bool                { return true }
func (m *mockTestAdapter) PrepareRequest(ctx context.Context, origReq *entity.ChatCompletionRequest, httpReq *http.Request) (*http.Request, error) {
	return m.PrepareRequestWithEndpoint(ctx, origReq, httpReq, nil)
}
func (m *mockTestAdapter) PrepareRequestWithEndpoint(ctx context.Context, origReq *entity.ChatCompletionRequest, httpReq *http.Request, ep *entity.EndpointConfig) (*http.Request, error) {
	url := m.targetURL
	if ep != nil && ep.URL != "" {
		url = ep.URL
	}
	return http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
}
func (m *mockTestAdapter) ExtractUsageFromResponse(body []byte) (*entity.UsageInfo, error) {
	return nil, nil
}
func (m *mockTestAdapter) ExtractUsageFromChunk(chunk []byte) (*entity.UsageInfo, error) {
	return nil, nil
}
func (m *mockTestAdapter) NormalizeResponse(statusCode int, body []byte) ([]byte, error) {
	return body, nil
}
func (m *mockTestAdapter) NormalizeSSEChunk(chunk []byte) ([][]byte, error) {
	return [][]byte{chunk}, nil
}

// flushWriter は http.Flusher を実装した httptest.ResponseRecorder のラッパー
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flushRecorder) Flush() {
	f.flushed = true
}

func TestStreamingClientDisconnect(t *testing.T) {
	var upstreamCanceled atomic.Bool
	upstreamStarted := make(chan struct{})

	// アップストリームのモックサーバー: チャンクを無限に送信し続ける
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		close(upstreamStarted)

		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-r.Context().Done():
				upstreamCanceled.Store(true)
				return
			case <-ticker.C:
				_, err := fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
				if err != nil {
					upstreamCanceled.Store(true)
					return
				}
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
		}
	}))
	defer upstreamServer.Close()

	llmProxy := proxy.NewLLMProxy(nil, nil, nil, nil)
	adapter := &mockTestAdapter{targetURL: upstreamServer.URL}

	ctx, cancel := context.WithCancel(context.Background())

	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}

	reqObj := &entity.ChatCompletionRequest{
		Model:  "gpt-4o",
		Stream: true,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		llmProxy.ServeForward(rec, clientReq, nil, reqObj, adapter)
	}()

	// アップストリームが開始するのを待つ
	select {
	case <-upstreamStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upstream to start")
	}

	// クライアント側で切断をシミュレート
	time.Sleep(100 * time.Millisecond)
	cancel()

	// プロキシの処理が即座に完了するか確認（ハングしないこと）
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeForward did not terminate promptly upon client disconnect")
	}

	// アップストリームのコネクションも切断されたか確認
	time.Sleep(100 * time.Millisecond)
	if !upstreamCanceled.Load() {
		t.Error("expected upstream request to be canceled when client disconnected")
	}
}

func TestNonStreamingClientDisconnect(t *testing.T) {
	var upstreamCanceled atomic.Bool
	upstreamStarted := make(chan struct{})

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(upstreamStarted)
		select {
		case <-r.Context().Done():
			upstreamCanceled.Store(true)
			return
		case <-time.After(5 * time.Second):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"choices":[]}`))
		}
	}))
	defer upstreamServer.Close()

	llmProxy := proxy.NewLLMProxy(nil, nil, nil, nil)
	adapter := &mockTestAdapter{targetURL: upstreamServer.URL}

	ctx, cancel := context.WithCancel(context.Background())
	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	reqObj := &entity.ChatCompletionRequest{
		Model:  "gpt-4o",
		Stream: false,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		llmProxy.ServeForward(rec, clientReq, nil, reqObj, adapter)
	}()

	select {
	case <-upstreamStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upstream to start")
	}

	// クライアント切断
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeForward did not terminate promptly upon client disconnect")
	}

	time.Sleep(100 * time.Millisecond)
	if !upstreamCanceled.Load() {
		t.Error("expected upstream request to be canceled when client disconnected in non-streaming mode")
	}
}

type fullMockAdapter struct {
	targetURL string
}

func (m *fullMockAdapter) Provider() service.ProviderType { return service.ProviderOpenAI }
func (m *fullMockAdapter) IsEnabled() bool                { return true }
func (m *fullMockAdapter) PrepareRequest(ctx context.Context, origReq *entity.ChatCompletionRequest, httpReq *http.Request) (*http.Request, error) {
	return m.PrepareRequestWithEndpoint(ctx, origReq, httpReq, nil)
}
func (m *fullMockAdapter) PrepareRequestWithEndpoint(ctx context.Context, origReq *entity.ChatCompletionRequest, httpReq *http.Request, ep *entity.EndpointConfig) (*http.Request, error) {
	url := m.targetURL
	if ep != nil && ep.URL != "" {
		url = ep.URL
	}
	return http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
}
func (m *fullMockAdapter) ExtractUsageFromResponse(body []byte) (*entity.UsageInfo, error) {
	return &entity.UsageInfo{
		PromptTokens:     10,
		CompletionTokens: 20,
		TotalTokens:      30,
	}, nil
}
func (m *fullMockAdapter) ExtractUsageFromChunk(chunk []byte) (*entity.UsageInfo, error) {
	if string(chunk) == "[DONE]" {
		return &entity.UsageInfo{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		}, nil
	}
	return nil, nil
}
func (m *fullMockAdapter) NormalizeResponse(statusCode int, body []byte) ([]byte, error) {
	return body, nil
}
func (m *fullMockAdapter) NormalizeSSEChunk(chunk []byte) ([][]byte, error) {
	return [][]byte{chunk}, nil
}

func TestServeForward_NonStreaming_Success(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-123","choices":[{"message":{"role":"assistant","content":"hello"}}]}`))
	}))
	defer upstreamServer.Close()

	llmProxy := proxy.NewLLMProxy(nil, nil, nil, nil)
	adapter := &fullMockAdapter{targetURL: upstreamServer.URL}

	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()

	tenantCtx := &entity.TenantContext{
		ServiceID: "test-service",
		TenantID:  "test-tenant",
	}
	reqObj := &entity.ChatCompletionRequest{
		Model:  "gpt-4o",
		Stream: false,
	}

	llmProxy.ServeForward(rec, clientReq, tenantCtx, reqObj, adapter)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", rec.Code)
	}
}

func TestServeForward_Streaming_Success(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer upstreamServer.Close()

	llmProxy := proxy.NewLLMProxy(nil, nil, nil, nil)
	adapter := &fullMockAdapter{targetURL: upstreamServer.URL}

	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}

	tenantCtx2 := &entity.TenantContext{
		ServiceID: "test-service",
		TenantID:  "test-tenant",
	}
	reqObj := &entity.ChatCompletionRequest{
		Model:  "gpt-4o",
		Stream: true,
	}

	llmProxy.ServeForward(rec, clientReq, tenantCtx2, reqObj, adapter)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", rec.Code)
	}
}

func TestServeForwardCandidates_429Failover_Success(t *testing.T) {
	var ep1Calls, ep2Calls atomic.Int32

	// エンドポイント1: 429 Too Many Requests を返す
	ep1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ep1Calls.Add(1)
		w.Header().Set("Retry-After", "10")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded","code":"rate_limit_exceeded"}}`))
	}))
	defer ep1Server.Close()

	// エンドポイント2: 200 OK を返す（フェイルオーバー先）
	ep2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ep2Calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-failover","choices":[{"message":{"role":"assistant","content":"success from ep2"}}]}`))
	}))
	defer ep2Server.Close()

	llmProxy := proxy.NewLLMProxy(nil, nil, nil, nil)
	cb := entity.NewCircuitBreaker()

	candidates := []entity.EndpointConfig{
		{Name: "ep-1", Provider: "azure", URL: ep1Server.URL, Priority: 1},
		{Name: "ep-2", Provider: "azure", URL: ep2Server.URL, Priority: 2},
	}

	resolver := func(p string) service.Adapter {
		return &fullMockAdapter{}
	}

	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	reqObj := &entity.ChatCompletionRequest{
		Model:  "gpt-4o",
		Stream: false,
	}

	llmProxy.ServeForwardCandidates(rec, clientReq, nil, reqObj, candidates, resolver, cb)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK after failover, got %d. Body: %s", rec.Code, rec.Body.String())
	}
	if ep1Calls.Load() != 1 {
		t.Errorf("expected 1 call to ep1, got %d", ep1Calls.Load())
	}
	if ep2Calls.Load() != 1 {
		t.Errorf("expected 1 call to ep2, got %d", ep2Calls.Load())
	}
	if !cb.IsCoolingDown("ep-1") {
		t.Errorf("expected ep-1 to be marked as cooling down after 429")
	}
}

func TestServeForward_UpstreamError(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid model"}}`))
	}))
	defer upstreamServer.Close()

	llmProxy := proxy.NewLLMProxy(nil, nil, nil, nil)
	adapter := &fullMockAdapter{targetURL: upstreamServer.URL}

	clientReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()

	reqObj := &entity.ChatCompletionRequest{
		Model:  "gpt-4o",
		Stream: false,
	}

	llmProxy.ServeForward(rec, clientReq, nil, reqObj, adapter)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", rec.Code)
	}
}
