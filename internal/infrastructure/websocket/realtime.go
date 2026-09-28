package websocket

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/northfieldzz/kura/internal/domain/entity"
	"github.com/northfieldzz/kura/internal/infrastructure/config"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  32 * 1024,
	WriteBufferSize: 32 * 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // プロキシとして全オリジンを許可
	},
}

// RealtimeProxy は WebSocket パススルーを管理する
type RealtimeProxy struct {
	cfg *config.Config
}

// NewRealtimeProxy は RealtimeProxy を生成する
func NewRealtimeProxy(cfg *config.Config) *RealtimeProxy {
	return &RealtimeProxy{cfg: cfg}
}

// ServeWebSocket はクライアントと各プロバイダー (Azure OpenAI, OpenAI, Gemini 等) 間の双方向 WebSocket 通信を中継する
func (p *RealtimeProxy) ServeWebSocket(w http.ResponseWriter, r *http.Request, tenantCtx *entity.TenantContext) {
	// クライアント側 WebSocket アップグレード
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer clientConn.Close()

	targetURL, headers, err := p.resolveUpstream(r)
	if err != nil {
		log.Printf("[ERROR] Realtime resolveUpstream failed: %v", err)
		_ = clientConn.WriteJSON(entity.NewStandardError(
			http.StatusBadRequest,
			entity.ErrorTypeInvalidRequest,
			"Invalid request or missing upstream configuration",
			"",
		))
		return
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}

	upstreamConn, resp, err := dialer.DialContext(r.Context(), targetURL, headers)
	if err != nil {
		statusCode := http.StatusBadGateway
		if resp != nil {
			statusCode = resp.StatusCode
		}
		log.Printf("[ERROR] Failed to connect to upstream Realtime WebSocket (%s): %v", targetURL, err)
		_ = clientConn.WriteJSON(entity.NewStandardError(
			statusCode,
			entity.ErrorTypeVendorError,
			"Vendor connection error",
			"",
		))
		return
	}
	defer upstreamConn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)

	// クライアント -> アップストリーム
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			msgType, data, err := clientConn.ReadMessage()
			if err != nil {
				return
			}
			if err := upstreamConn.WriteMessage(msgType, data); err != nil {
				return
			}
		}
	}()

	// アップストリーム -> クライアント
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			msgType, data, err := upstreamConn.ReadMessage()
			if err != nil {
				return
			}
			if err := clientConn.WriteMessage(msgType, data); err != nil {
				return
			}
		}
	}()

	// どちらかが切断したら終了
	<-ctx.Done()
	_ = clientConn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	_ = upstreamConn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	wg.Wait()
}

// resolveUpstream はリクエストパラメータや環境設定からプロバイダーを判定し、接続先 WebSocket URL とヘッダーを構築する
func (p *RealtimeProxy) resolveUpstream(r *http.Request) (string, http.Header, error) {
	q := r.URL.Query()
	model := strings.TrimSpace(q.Get("model"))
	provider := strings.ToLower(strings.TrimSpace(q.Get("provider")))

	// 1. Gemini Multimodal Live API 判定
	if provider == "gemini" || provider == "google" || strings.HasPrefix(model, "gemini") {
		if p.cfg.GeminiAPIKey == "" {
			return "", nil, fmt.Errorf("gemini api key is not configured")
		}
		targetURL := fmt.Sprintf("wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1alpha.GenerativeService.BidiGenerateContent?key=%s", p.cfg.GeminiAPIKey)
		headers := http.Header{}
		return targetURL, headers, nil
	}

	// 2. Azure OpenAI Realtime
	if p.cfg.AzureOpenAIEndpoint == "" || p.cfg.AzureOpenAIAPIKey == "" {
		return "", nil, fmt.Errorf("azure openai endpoint or api key is not configured")
	}

	baseEndpoint := strings.TrimRight(p.cfg.AzureOpenAIEndpoint, "/")
	// 日本東リージョン切り替え
	if strings.EqualFold(r.Header.Get("X-Data-Residency"), "japan") && p.cfg.AzureOpenAIEndpointJapan != "" {
		baseEndpoint = strings.TrimRight(p.cfg.AzureOpenAIEndpointJapan, "/")
	}

	// URL ホストの抽出 (https://... -> host)
	u, err := url.Parse(baseEndpoint)
	if err != nil {
		return "", nil, fmt.Errorf("invalid azure openai endpoint URL: %w", err)
	}
	host := u.Host
	if host == "" {
		clean := strings.TrimPrefix(baseEndpoint, "https://")
		clean = strings.TrimPrefix(clean, "http://")
		host = strings.Split(clean, "/")[0]
	}

	// デプロイメント名の解決
	deployment := q.Get("deployment")
	if deployment == "" {
		deployment = strings.TrimPrefix(model, "azure/")
	}
	if deployment == "" && p.cfg.AzureDefaultDeployment != "" {
		deployment = p.cfg.AzureDefaultDeployment
	}
	if deployment == "" {
		deployment = "gpt-4o-realtime-preview"
	}

	// API バージョン
	apiVersion := q.Get("api-version")
	if apiVersion == "" {
		apiVersion = "2024-10-01-preview"
	}

	scheme := "wss"
	if u.Scheme == "http" || u.Scheme == "ws" {
		scheme = "ws"
	}

	// Azure Realtime WebSocket URL:
	// wss://<host>/openai/realtime?api-version=<apiVersion>&deployment=<deployment>
	targetURL := fmt.Sprintf("%s://%s/openai/realtime?api-version=%s&deployment=%s", scheme, host, apiVersion, deployment)

	headers := http.Header{}
	headers.Set("api-key", p.cfg.AzureOpenAIAPIKey)
	headers.Set("OpenAI-Beta", "realtime=v1")
	return targetURL, headers, nil
}
