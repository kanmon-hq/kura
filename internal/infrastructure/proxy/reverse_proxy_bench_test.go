package proxy_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/infrastructure/metrics"
	"github.com/kanmon-hq/kura/internal/infrastructure/proxy"
)

func BenchmarkStreamingProxy_ServeForward(b *testing.B) {
	// 50 チャンクの SSE ストリームペイロードを生成
	var buf bytes.Buffer
	for i := 0; i < 50; i++ {
		buf.WriteString(fmt.Sprintf("data: {\"id\":\"chatcmpl-%d\",\"choices\":[{\"delta\":{\"content\":\"token%d \"}}]}\n\n", i, i))
	}
	buf.WriteString("data: {\"id\":\"chatcmpl-end\",\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":50,\"total_tokens\":150}}\n\n")
	buf.WriteString("data: [DONE]\n\n")
	sseData := buf.Bytes()

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(sseData)
	}))
	defer upstreamServer.Close()

	m := metrics.NewMetrics()
	p := proxy.NewLLMProxy(nil, nil, nil, nil, m)

	tenantCtx := &entity.TenantContext{
		ServiceID: "bench-service",
		TenantID:  "tenant-bench",
	}
	adapter := &mockTestAdapter{targetURL: upstreamServer.URL}
	reqObj := &entity.ChatCompletionRequest{Model: "gpt-4o", Stream: true}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-4o","stream":true}`)))
		rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
		p.ServeForward(rec, req, tenantCtx, reqObj, adapter)
	}
}
