package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/repository"
	"github.com/kanmon-hq/kura/internal/domain/service"
	"github.com/kanmon-hq/kura/internal/infrastructure/metrics"
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

// CandidateAdapterResolver はプロバイダー名から対応する Adapter を解決する関数型
type CandidateAdapterResolver func(provider string) service.Adapter

// ServeForward は単一 Adapter への互換フォワード
func (p *LLMProxy) ServeForward(
	w http.ResponseWriter,
	r *http.Request,
	tenantCtx *entity.TenantContext,
	reqObj *entity.ChatCompletionRequest,
	adapter service.Adapter,
) {
	providerName := "azure"
	if adapter != nil {
		providerName = string(adapter.Provider())
	}
	candidates := []entity.EndpointConfig{
		{
			Name:     providerName,
			Provider: providerName,
		},
	}
	resolver := func(p string) service.Adapter {
		return adapter
	}
	p.ServeForwardCandidates(w, r, tenantCtx, reqObj, candidates, resolver, nil)
}

// ServeForwardCandidates は候補エンドポイントリストを順次試し、429/503/接続断時に自動フェイルオーバーする
func (p *LLMProxy) ServeForwardCandidates(
	w http.ResponseWriter,
	r *http.Request,
	tenantCtx *entity.TenantContext,
	reqObj *entity.ChatCompletionRequest,
	candidates []entity.EndpointConfig,
	adapterResolver CandidateAdapterResolver,
	cb *entity.CircuitBreaker,
) {
	p.metrics.IncActive()
	defer p.metrics.DecActive()

	ctx := r.Context()
	gatewayStartTime := time.Now()

	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = uuid.New().String()
	}
	traceParent := r.Header.Get("traceparent")

	if len(candidates) == 0 {
		sendError(w, http.StatusBadRequest, entity.ErrorTypeInvalidRequest, "No available upstream endpoints found for model", "")
		return
	}

	for i, cand := range candidates {
		adapter := adapterResolver(cand.Provider)
		if adapter == nil || !adapter.IsEnabled() {
			continue
		}

		// リクエストオブジェクトのディープコピー（モデル名書き換えの汚染防止）
		reqCopy := *reqObj
		if reqObj.ExtraFields != nil {
			reqCopy.ExtraFields = make(map[string]any, len(reqObj.ExtraFields))
			for k, v := range reqObj.ExtraFields {
				reqCopy.ExtraFields[k] = v
			}
		}

		targetReq, err := adapter.PrepareRequestWithEndpoint(ctx, &reqCopy, r, &cand)
		if err != nil {
			log.Printf("[WARN] Failed to prepare request for candidate %s: %v", cand.Name, err)
			continue
		}
		if targetReq == nil {
			continue
		}

		targetReq.Header.Set("X-Request-ID", requestID)
		if traceParent != "" {
			targetReq.Header.Set("traceparent", traceParent)
		}

		endpointID := entity.EndpointIdentifier(&cand)
		log.Printf("[DEBUG] Forwarding to candidate [%d/%d] name=%s provider=%s url=%s (RequestID: %s)",
			i+1, len(candidates), cand.Name, cand.Provider, targetReq.URL.String(), requestID)

		// 試行実行
		var retryNext bool
		if reqObj.Stream {
			retryNext = p.tryStreaming(w, r, targetReq, tenantCtx, &reqCopy, adapter, gatewayStartTime, requestID, &cand, cb, i < len(candidates)-1)
		} else {
			retryNext = p.tryNonStreaming(w, r, targetReq, tenantCtx, &reqCopy, adapter, gatewayStartTime, requestID, &cand, cb, i < len(candidates)-1)
		}

		if !retryNext {
			return // 成功またはクライアント返却済み
		}

		log.Printf("[WARN] [FAILOVER] Candidate %s throttled or failed. Failing over to next candidate (RequestID: %s)",
			endpointID, requestID)
	}

	// 全候補がスキップされた場合
	sendError(w, http.StatusServiceUnavailable, entity.ErrorTypeVendorError, "All upstream candidates failed or are unavailable", "")
}

func (p *LLMProxy) tryStreaming(
	w http.ResponseWriter,
	r *http.Request,
	targetReq *http.Request,
	tenantCtx *entity.TenantContext,
	reqObj *entity.ChatCompletionRequest,
	adapter service.Adapter,
	gatewayStartTime time.Time,
	requestID string,
	cand *entity.EndpointConfig,
	cb *entity.CircuitBreaker,
	hasNext bool,
) bool {
	// 即時フラッシャーの取得
	flusher, ok := w.(http.Flusher)
	if !ok {
		sendError(w, http.StatusInternalServerError, entity.ErrorTypeInternalError, "Streaming not supported by server", "")
		return false
	}

	clientCtx := r.Context()
	streamCtx, cancelStream := context.WithCancel(clientCtx)
	defer cancelStream()

	targetReq = targetReq.WithContext(streamCtx)

	vendorStartTime := time.Now()
	resp, err := p.transport.RoundTrip(targetReq)
	if err != nil {
		if errors.Is(streamCtx.Err(), context.Canceled) {
			log.Printf("[INFO] Client canceled streaming before response headers received. RequestID: %s", requestID)
			return false
		}
		log.Printf("[ERROR] Vendor connection error for %s (RequestID: %s): %v", cand.Name, requestID, err)
		if hasNext {
			if cb != nil {
				cb.MarkCooldown(entity.EndpointIdentifier(cand), 15*time.Second)
			}
			return true // 次の候補へフェイルオーバー
		}
		sendError(w, http.StatusBadGateway, entity.ErrorTypeVendorError, "Vendor connection error", "")
		return false
	}
	defer resp.Body.Close()

	// 429 / 503 / 504 の場合はフェイルオーバー判定
	if isThrottledOrUnavailable(resp.StatusCode) {
		cooldown := parseRetryAfter(resp.Header.Get("Retry-After"))
		if cb != nil {
			cb.MarkCooldown(entity.EndpointIdentifier(cand), cooldown)
		}
		if hasNext {
			return true // 次の候補へフェイルオーバー
		}
		p.handleVendorError(w, resp)
		return false
	}

	// 4xx / 5xx (フェイルオーバー対象外のエラー: 400 Bad Request, 401 Unauthorized 等)
	if resp.StatusCode >= 400 {
		p.handleVendorError(w, resp)
		return false
	}

	// 200 OK: ストリーム送出開始（これ以降はクライアントへ送信中のためフェイルオーバー不可）
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

			// チャンクをクライアントへ即時転送
			if _, writeErr := w.Write(line); writeErr != nil {
				log.Printf("[INFO] Client connection lost during stream write (RequestID: %s): %v", requestID, writeErr)
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
				log.Printf("[WARN] Streaming read error from vendor: %v (RequestID: %s)", err, requestID)
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
		log.Printf("[INFO] Client disconnected during streaming. Upstream canceled -> RequestID: %s, Duration: %dms\n",
			requestID, totalDuration.Milliseconds())
	} else {
		log.Printf("[OBSERVABILITY] Streaming Finished -> RequestID: %s, Total: %dms, Vendor: %dms, Gateway: %dms, TTFT: %dms\n",
			requestID, totalDuration.Milliseconds(), vendorDuration.Milliseconds(), gatewayLatencyMs, ttftMs)
	}

	var promptTokens, completionTokens, totalTokens int64
	if finalUsage != nil {
		promptTokens = int64(finalUsage.PromptTokens)
		completionTokens = int64(finalUsage.CompletionTokens)
		totalTokens = int64(finalUsage.TotalTokens)
	} else if !clientDisconnected {
		log.Printf("[WARN] No usage information returned from vendor for streaming request %s", requestID)
	}

	var cost float64
	if totalTokens > 0 {
		engine := p.pricingEngine
		if engine == nil {
			engine = entity.DefaultEngine()
		}
		cost, _ = engine.CalculateCost(reqObj.Model, promptTokens, completionTokens, 0, 0)
		p.recordUsage(tenantCtx, reqObj.Model, promptTokens, completionTokens, totalTokens, cost)
	}

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
	return false
}

func (p *LLMProxy) tryNonStreaming(
	w http.ResponseWriter,
	r *http.Request,
	targetReq *http.Request,
	tenantCtx *entity.TenantContext,
	reqObj *entity.ChatCompletionRequest,
	adapter service.Adapter,
	gatewayStartTime time.Time,
	requestID string,
	cand *entity.EndpointConfig,
	cb *entity.CircuitBreaker,
	hasNext bool,
) bool {
	clientCtx := r.Context()
	reqCtx, cancelReq := context.WithCancel(clientCtx)
	defer cancelReq()

	targetReq = targetReq.WithContext(reqCtx)

	vendorStartTime := time.Now()
	resp, err := p.transport.RoundTrip(targetReq)
	if err != nil {
		if errors.Is(reqCtx.Err(), context.Canceled) {
			log.Printf("[INFO] Client canceled non-streaming request before response headers received. RequestID: %s", requestID)
			return false
		}
		log.Printf("[ERROR] Vendor connection error for %s (RequestID: %s): %v", cand.Name, requestID, err)
		if hasNext {
			if cb != nil {
				cb.MarkCooldown(entity.EndpointIdentifier(cand), 15*time.Second)
			}
			return true // 次の候補へフェイルオーバー
		}
		sendError(w, http.StatusBadGateway, entity.ErrorTypeVendorError, "Vendor connection error", "")
		return false
	}
	defer resp.Body.Close()

	// 429 / 503 / 504 の場合はフェイルオーバー判定
	if isThrottledOrUnavailable(resp.StatusCode) {
		cooldown := parseRetryAfter(resp.Header.Get("Retry-After"))
		if cb != nil {
			cb.MarkCooldown(entity.EndpointIdentifier(cand), cooldown)
		}
		if hasNext {
			return true // 次の候補へフェイルオーバー
		}
	}

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
			log.Printf("[INFO] Client canceled non-streaming request while reading body. RequestID: %s", requestID)
			return false
		}
		sendError(w, http.StatusInternalServerError, entity.ErrorTypeInternalError, "Failed to read vendor response", "")
		return false
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
		return false
	}

	var promptTokens, completionTokens, totalTokens int64
	if usage, _ := adapter.ExtractUsageFromResponse(respBody); usage != nil {
		promptTokens = int64(usage.PromptTokens)
		completionTokens = int64(usage.CompletionTokens)
		totalTokens = int64(usage.TotalTokens)
	} else {
		log.Printf("[WARN] No usage information returned from vendor for non-streaming request %s", requestID)
	}

	var cost float64
	if totalTokens > 0 {
		engine := p.pricingEngine
		if engine == nil {
			engine = entity.DefaultEngine()
		}
		cost, _ = engine.CalculateCost(reqObj.Model, promptTokens, completionTokens, 0, 0)
	}

	p.recordUsage(tenantCtx, reqObj.Model, promptTokens, completionTokens, totalTokens, cost)

	log.Printf("[OBSERVABILITY] Non-Streaming Finished -> RequestID: %s, Total: %dms, Vendor: %dms, Gateway: %dms, Tokens: %d, Cost: $%.6f\n",
		requestID, totalDuration.Milliseconds(), vendorDuration.Milliseconds(), gatewayLatencyMs, totalTokens, cost)

	normalizedBody, err := adapter.NormalizeResponse(resp.StatusCode, respBody)
	if err != nil {
		normalizedBody = respBody
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", requestID)
	w.WriteHeader(resp.StatusCode)
	if _, err := w.Write(normalizedBody); err != nil {
		log.Printf("[INFO] Failed to write response to client. RequestID: %s, Err: %v", requestID, err)
	}

	serviceID := "unknown"
	if tenantCtx != nil && tenantCtx.ServiceID != "" {
		serviceID = tenantCtx.ServiceID
	}
	p.metrics.RecordRequest(reqObj.Model, false, resp.StatusCode, totalDuration, serviceID)
	p.metrics.RecordTokens(reqObj.Model, promptTokens, completionTokens, totalTokens, cost, serviceID)
	return false
}

func isThrottledOrUnavailable(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusServiceUnavailable ||
		statusCode == http.StatusGatewayTimeout
}

func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 20 * time.Second // デフォルト冷却時間 20 秒
	}
	if seconds, err := strconv.Atoi(header); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 20 * time.Second
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
				log.Printf("[ERROR] Failed to increment cost in CostStore: %v", err)
			}
		}()
	}

	// 2. UsageStore への詳細記録 (永続・集計用)
	if p.usageStore != nil && totalTokens > 0 && serviceID != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := p.usageStore.RecordUsage(ctx, serviceID, tenantID, currentMonth, model, promptTokens, completionTokens, cost, pricingVersion); err != nil {
				log.Printf("[ERROR] Failed to record usage in UsageStore: %v", err)
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
