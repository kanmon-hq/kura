package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/northfieldzz/kura/internal/domain/entity"
	"github.com/northfieldzz/kura/internal/domain/repository"
	"github.com/northfieldzz/kura/internal/domain/service"
	"github.com/northfieldzz/kura/internal/infrastructure/metrics"
)

// LLMProxy は HTTP / SSE ストリーミングリバースプロキシ
type LLMProxy struct {
	transport     *http.Transport
	usageLogger   service.UsageLogger
	costStore     repository.CostStore
	usageStore    repository.UsageStore
	pricingEngine *entity.PricingEngine
	metrics       *metrics.Metrics
}

// NewLLMProxy は LLMProxy インスタンスを生成する
func NewLLMProxy(
	logger service.UsageLogger,
	costStore repository.CostStore,
	usageStore repository.UsageStore,
	pricingEngine *entity.PricingEngine,
	m ...*metrics.Metrics,
) *LLMProxy {
	var metricCollector *metrics.Metrics
	if len(m) > 0 {
		metricCollector = m[0]
	}
	if pricingEngine == nil {
		pricingEngine = entity.DefaultEngine()
	}
	return &LLMProxy{
		transport: &http.Transport{
			MaxIdleConns:        1000,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true, // SSE の即時転送とチャンク制御のため圧縮を無効化
		},
		usageLogger:   logger,
		costStore:     costStore,
		usageStore:    usageStore,
		pricingEngine: pricingEngine,
		metrics:       metricCollector,
	}
}

// ServeForward は受信リクエストを対象ベンダーへフォワードし、SSE / 非ストリーミングのレスポンスを処理する
func (p *LLMProxy) ServeForward(
	w http.ResponseWriter,
	r *http.Request,
	tenantCtx *entity.TenantContext,
	reqObj *entity.ChatCompletionRequest,
	adapter service.Adapter,
) {
	p.metrics.IncActive()
	defer p.metrics.DecActive()

	ctx := r.Context()
	gatewayStartTime := time.Now()

	// 1. Trace Context & Request ID の解決と伝播
	requestID := getHeaderFast(r.Header, "X-Request-Id")
	if requestID == "" {
		requestID = uuid.New().String()
	}
	traceParent := getHeaderFast(r.Header, "Traceparent")

	// ベンダー用リクエストの準備
	targetReq, err := adapter.PrepareRequest(ctx, reqObj, r)
	if err != nil {
		sendError(w, http.StatusBadRequest, entity.ErrorTypeInvalidRequest, err.Error(), "")
		return
	}
	if targetReq == nil {
		sendError(w, http.StatusInternalServerError, entity.ErrorTypeInternalError, "adapter prepared nil request", "")
		return
	}

	// アップストリームへトレース情報を伝播
	targetReq.Header.Set("X-Request-ID", requestID)
	if traceParent != "" {
		targetReq.Header.Set("traceparent", traceParent)
	}

	// 転送先 URL のログ出力 (デバッグ用)
	log.Printf("[DEBUG] Forwarding request to vendor (Method: %s)\n", targetReq.Method)

	// ストリーミング処理
	if reqObj.Stream {
		p.handleStreaming(w, r, targetReq, tenantCtx, reqObj, adapter, gatewayStartTime, requestID)
		return
	}

	// 非ストリーミング処理
	p.handleNonStreaming(w, r, targetReq, tenantCtx, reqObj, adapter, gatewayStartTime, requestID)
}

func (p *LLMProxy) handleStreaming(
	w http.ResponseWriter,
	r *http.Request,
	targetReq *http.Request,
	tenantCtx *entity.TenantContext,
	reqObj *entity.ChatCompletionRequest,
	adapter service.Adapter,
	gatewayStartTime time.Time,
	requestID string,
) {
	// 即時フラッシャーの取得
	flusher, ok := w.(http.Flusher)
	if !ok {
		sendError(w, http.StatusInternalServerError, entity.ErrorTypeInternalError, "Streaming not supported by server", "")
		return
	}

	// クライアント切断と連動するキャンセル可能コンテキスト
	clientCtx := r.Context()
	streamCtx, cancelStream := context.WithCancel(clientCtx)
	defer cancelStream()

	// アップストリーム用リクエストに streamCtx を紐付け
	targetReq = targetReq.WithContext(streamCtx)

	vendorStartTime := time.Now()
	resp, err := p.transport.RoundTrip(targetReq)
	if err != nil {
		if errors.Is(streamCtx.Err(), context.Canceled) {
			log.Printf("[INFO] Client canceled streaming before response headers received")
			return
		}
		log.Printf("[ERROR] Vendor connection error")
		sendError(w, http.StatusBadGateway, entity.ErrorTypeVendorError, "Vendor connection error", "")
		return
	}
	defer resp.Body.Close()

	// クライアント切断時にアップストリームの resp.Body を即座にクローズしてブロッキング読み込みを強制解除する
	stopMonitor := make(chan struct{})
	defer close(stopMonitor)
	go func() {
		select {
		case <-streamCtx.Done():
			_ = resp.Body.Close()
		case <-stopMonitor:
		}
	}()

	// エラーレスポンス (4xx/5xx) のハンドリング
	if resp.StatusCode >= 400 {
		p.handleVendorError(w, resp)
		return
	}

	// 仕様要件: X-Accel-Buffering: no および Cache-Control: no-cache を強制付与
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Request-ID", requestID)
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	reader := bufio.NewReader(resp.Body)
	var finalUsage *entity.UsageInfo
	var ttftRecorded bool
	var ttftMs int64
	var clientDisconnected bool

	for {
		select {
		case <-streamCtx.Done():
			clientDisconnected = true
			break
		default:
		}
		if clientDisconnected {
			break
		}

		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if !ttftRecorded {
				ttftMs = time.Since(vendorStartTime).Milliseconds()
				ttftRecorded = true
				p.metrics.RecordTTFT(reqObj.Model, time.Since(vendorStartTime))
			}

			// usage のインターセプト
			if usage, _ := adapter.ExtractUsageFromChunk(line); usage != nil {
				finalUsage = usage
			}

			// チャンクをクライアントへ即時転送（クライアント切断時は即時キャンセル）
			if _, writeErr := w.Write(line); writeErr != nil {
				log.Printf("[INFO] Client connection lost during stream write")
				clientDisconnected = true
				cancelStream()
				break
			}
			flusher.Flush()
		}

		if err != nil {
			if errors.Is(streamCtx.Err(), context.Canceled) {
				clientDisconnected = true
			} else if err != io.EOF {
				log.Printf("[WARN] Streaming read error from vendor")
			}
			break
		}
	}

	totalDuration := time.Since(gatewayStartTime)
	vendorDuration := time.Since(vendorStartTime)
	gatewayLatencyMs := (totalDuration - vendorDuration).Milliseconds()
	if gatewayLatencyMs < 0 {
		gatewayLatencyMs = 0
	}

	if clientDisconnected {
		log.Printf("[INFO] Client disconnected during streaming. Upstream canceled -> Duration: %dms\n", totalDuration.Milliseconds())
	} else {
		// オブザーバビリティ ログ出力
		log.Printf("[OBSERVABILITY] Streaming Finished -> Total: %dms, Vendor: %dms, Gateway: %dms, TTFT: %dms\n", totalDuration.Milliseconds(), vendorDuration.Milliseconds(), gatewayLatencyMs, ttftMs)
	}

	var promptTokens, completionTokens, totalTokens int64
	if finalUsage != nil {
		promptTokens = int64(finalUsage.PromptTokens)
		completionTokens = int64(finalUsage.CompletionTokens)
		totalTokens = int64(finalUsage.TotalTokens)
	} else if !clientDisconnected {
		log.Printf("[WARN] No usage information returned from vendor for streaming request")
	}

	// クレジット・費用計算 & 集計（途中で切断されてもトークン情報が取れていれば計上）
	var cost float64
	if totalTokens > 0 {
		engine := p.pricingEngine
		if engine == nil {
			engine = entity.DefaultEngine()
		}
		cost, _ = engine.CalculateCost(reqObj.Model, promptTokens, completionTokens, 0, 0)
		p.recordUsage(tenantCtx, reqObj.Model, promptTokens, completionTokens, totalTokens, cost)
	}

	// Prometheus メトリクス記録
	status := http.StatusOK
	if clientDisconnected {
		status = 499
	}
	serviceID := "unknown"
	if tenantCtx != nil && tenantCtx.ServiceID != "" {
		serviceID = tenantCtx.ServiceID
	}
	p.metrics.RecordRequest(reqObj.Model, true, status, totalDuration, serviceID)
	p.metrics.RecordTokens(reqObj.Model, promptTokens, completionTokens, totalTokens, cost, serviceID)
}

func (p *LLMProxy) handleNonStreaming(
	w http.ResponseWriter,
	r *http.Request,
	targetReq *http.Request,
	tenantCtx *entity.TenantContext,
	reqObj *entity.ChatCompletionRequest,
	adapter service.Adapter,
	gatewayStartTime time.Time,
	requestID string,
) {
	clientCtx := r.Context()
	reqCtx, cancelReq := context.WithCancel(clientCtx)
	defer cancelReq()

	targetReq = targetReq.WithContext(reqCtx)

	vendorStartTime := time.Now()
	resp, err := p.transport.RoundTrip(targetReq)
	if err != nil {
		if errors.Is(reqCtx.Err(), context.Canceled) {
			log.Printf("[INFO] Client canceled non-streaming request before response headers received")
			return
		}
		log.Printf("[ERROR] Vendor connection error")
		sendError(w, http.StatusBadGateway, entity.ErrorTypeVendorError, "Vendor connection error", "")
		return
	}
	defer resp.Body.Close()

	// 読み込み監視
	stopMonitor := make(chan struct{})
	defer close(stopMonitor)
	go func() {
		select {
		case <-reqCtx.Done():
			_ = resp.Body.Close()
		case <-stopMonitor:
		}
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		if errors.Is(reqCtx.Err(), context.Canceled) {
			log.Printf("[INFO] Client canceled non-streaming request while reading body")
			return
		}
		sendError(w, http.StatusInternalServerError, entity.ErrorTypeInternalError, "Failed to read vendor response", "")
		return
	}

	vendorDuration := time.Since(vendorStartTime)
	totalDuration := time.Since(gatewayStartTime)
	gatewayLatencyMs := (totalDuration - vendorDuration).Milliseconds()
	if gatewayLatencyMs < 0 {
		gatewayLatencyMs = 0
	}

	// エラーハンドリング (4xx/5xx)
	if resp.StatusCode >= 400 {
		p.handleVendorErrorWithBody(w, resp.StatusCode, respBody)
		return
	}

	var promptTokens, completionTokens, totalTokens int64
	if usage, _ := adapter.ExtractUsageFromResponse(respBody); usage != nil {
		promptTokens = int64(usage.PromptTokens)
		completionTokens = int64(usage.CompletionTokens)
		totalTokens = int64(usage.TotalTokens)
	} else {
		log.Printf("[WARN] No usage information returned from vendor for non-streaming request")
	}

	// クレジット・費用計算
	var cost float64
	if totalTokens > 0 {
		engine := p.pricingEngine
		if engine == nil {
			engine = entity.DefaultEngine()
		}
		cost, _ = engine.CalculateCost(reqObj.Model, promptTokens, completionTokens, 0, 0)
	}

	// 利用量集計 (CostStore & UsageStore)
	p.recordUsage(tenantCtx, reqObj.Model, promptTokens, completionTokens, totalTokens, cost)

	// オブザーバビリティ ログ出力
	log.Printf("[OBSERVABILITY] Non-Streaming Finished -> RequestID: %s, Total: %dms, Vendor: %dms, Gateway: %dms, Tokens: %d, Cost: $%.6f\n",
		requestID, totalDuration.Milliseconds(), vendorDuration.Milliseconds(), gatewayLatencyMs, totalTokens, cost)

	// レスポンスの正規化（Claude/Gemini -> OpenAI 互換形式）
	normalizedBody, err := adapter.NormalizeResponse(resp.StatusCode, respBody)
	if err != nil {
		normalizedBody = respBody
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", requestID)
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(normalizedBody); err != nil {
		log.Printf("[INFO] Failed to write response to client (client likely disconnected)")
	}

	// Prometheus メトリクス記録
	serviceID := "unknown"
	if tenantCtx != nil && tenantCtx.ServiceID != "" {
		serviceID = tenantCtx.ServiceID
	}
	p.metrics.RecordRequest(reqObj.Model, false, resp.StatusCode, totalDuration, serviceID)
	p.metrics.RecordTokens(reqObj.Model, promptTokens, completionTokens, totalTokens, cost, serviceID)
}

func (p *LLMProxy) recordUsage(
	tenantCtx *entity.TenantContext,
	model string,
	promptTokens, completionTokens, totalTokens int64,
	cost float64,
) {
	currentMonth := entity.CurrentMonthJST()

	var serviceID, tenantID string
	if tenantCtx != nil {
		serviceID = tenantCtx.ServiceID
		tenantID = tenantCtx.TenantID
	}

	pricingVersion := "2026-09-24"
	if p.pricingEngine != nil {
		pricingVersion = p.pricingEngine.Version()
	}

	// 1. CostStore へのアトミック加算 (ホットパス・ソフトリミット)
	if p.costStore != nil && totalTokens > 0 && serviceID != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := p.costStore.IncrementCost(ctx, serviceID, tenantID, currentMonth, promptTokens, completionTokens, cost); err != nil {
				log.Printf("[ERROR] Failed to increment cost in CostStore")
			}
		}()
	}

	// 2. UsageStore への詳細記録 (永続・集計用)
	if p.usageStore != nil && totalTokens > 0 && serviceID != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := p.usageStore.RecordUsage(ctx, serviceID, tenantID, currentMonth, model, promptTokens, completionTokens, cost, pricingVersion); err != nil {
				log.Printf("[ERROR] Failed to record usage in UsageStore")
			}
		}()
	}

	// 3. UsageLogger への記録
	if p.usageLogger != nil {
		event := entity.UsageLogEvent{
			TeamID:           serviceID,
			Model:            model,
			PromptTokens:     int(promptTokens),
			CompletionTokens: int(completionTokens),
			TotalTokens:      int(totalTokens),
			Cost:             cost,
			Timestamp:        time.Now().UTC(),
		}
		if tenantCtx != nil {
			event.TenantID = tenantCtx.TenantID
			event.KeyID = tenantCtx.KeyID
			event.KeyPrefix = tenantCtx.KeyPrefix
			event.IsProxied = tenantCtx.IsProxied
			event.Environment = tenantCtx.Environment
			event.Feature = tenantCtx.Feature
			event.Tags = tenantCtx.Tags
		}
		p.usageLogger.Log(context.Background(), event)
	}
}

func (p *LLMProxy) handleVendorError(w http.ResponseWriter, resp *http.Response) {
	body, _ := io.ReadAll(resp.Body)
	p.handleVendorErrorWithBody(w, resp.StatusCode, body)
}

func (p *LLMProxy) handleVendorErrorWithBody(w http.ResponseWriter, statusCode int, body []byte) {
	var originalCode string
	var message string = string(body)

	// ⚡ Bolt Optimization: Use a fast anonymous struct instead of unmarshaling into map[string]any
	// This avoids expensive memory allocations and recursive parsing when the vendor returns large
	// error payloads with extra metadata, significantly speeding up error handling.
	var fastErr struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}

	if err := json.Unmarshal(body, &fastErr); err == nil {
		if fastErr.Error.Message != "" {
			message = fastErr.Error.Message
		}
		if fastErr.Error.Code != "" {
			originalCode = fastErr.Error.Code
		}
	}

	sendError(w, statusCode, entity.ErrorTypeVendorError, message, originalCode)
}

func sendError(w http.ResponseWriter, statusCode int, errType, message, vendorCode string) {
	errResp := entity.NewStandardError(statusCode, errType, message, vendorCode)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write(errResp.ToJSON())
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
