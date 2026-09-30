package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/infrastructure/config"
)

func TestRealtimeProxy_resolveUpstream(t *testing.T) {
	tests := []struct {
		name         string
		cfg          *config.Config
		urlStr       string
		header       http.Header
		wantErr      bool
		wantURL      string
		wantProvider string // "gemini" or "azure"
	}{
		{
			name: "Gemini provider via query",
			cfg: &config.Config{
				GeminiAPIKey: "gemini-secret-key",
			},
			urlStr:       "http://localhost/realtime?provider=gemini",
			header:       http.Header{},
			wantErr:      false,
			wantURL:      "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1alpha.GenerativeService.BidiGenerateContent?key=gemini-secret-key",
			wantProvider: "gemini",
		},
		{
			name: "Gemini provider via model prefix",
			cfg: &config.Config{
				GeminiAPIKey: "gemini-secret-key",
			},
			urlStr:       "http://localhost/realtime?model=gemini-1.5-pro",
			header:       http.Header{},
			wantErr:      false,
			wantURL:      "wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1alpha.GenerativeService.BidiGenerateContent?key=gemini-secret-key",
			wantProvider: "gemini",
		},
		{
			name: "Gemini missing API key",
			cfg: &config.Config{
				GeminiAPIKey: "",
			},
			urlStr:  "http://localhost/realtime?provider=gemini",
			header:  http.Header{},
			wantErr: true,
		},
		{
			name: "Azure OpenAI Default",
			cfg: &config.Config{
				AzureOpenAIEndpoint: "https://my-azure.openai.azure.com/",
				AzureOpenAIAPIKey:   "azure-secret-key",
			},
			urlStr:       "http://localhost/realtime",
			header:       http.Header{},
			wantErr:      false,
			wantURL:      "wss://my-azure.openai.azure.com/openai/realtime?api-version=2024-10-01-preview&deployment=gpt-4o-realtime-preview",
			wantProvider: "azure",
		},
		{
			name: "Azure OpenAI Default Deployment Configured",
			cfg: &config.Config{
				AzureOpenAIEndpoint:    "https://my-azure.openai.azure.com/",
				AzureOpenAIAPIKey:      "azure-secret-key",
				AzureDefaultDeployment: "my-default-deployment",
			},
			urlStr:       "http://localhost/realtime",
			header:       http.Header{},
			wantErr:      false,
			wantURL:      "wss://my-azure.openai.azure.com/openai/realtime?api-version=2024-10-01-preview&deployment=my-default-deployment",
			wantProvider: "azure",
		},
		{
			name: "Azure OpenAI Custom Deployment and API Version",
			cfg: &config.Config{
				AzureOpenAIEndpoint: "https://my-azure.openai.azure.com",
				AzureOpenAIAPIKey:   "azure-secret-key",
			},
			urlStr:       "http://localhost/realtime?deployment=custom-dep&api-version=2023-12-01",
			header:       http.Header{},
			wantErr:      false,
			wantURL:      "wss://my-azure.openai.azure.com/openai/realtime?api-version=2023-12-01&deployment=custom-dep",
			wantProvider: "azure",
		},
		{
			name: "Azure OpenAI Deployment from model param",
			cfg: &config.Config{
				AzureOpenAIEndpoint: "https://my-azure.openai.azure.com",
				AzureOpenAIAPIKey:   "azure-secret-key",
			},
			urlStr:       "http://localhost/realtime?model=azure/my-model",
			header:       http.Header{},
			wantErr:      false,
			wantURL:      "wss://my-azure.openai.azure.com/openai/realtime?api-version=2024-10-01-preview&deployment=my-model",
			wantProvider: "azure",
		},
		{
			name: "Azure OpenAI Japan Region Routing",
			cfg: &config.Config{
				AzureOpenAIEndpoint:      "https://my-azure.openai.azure.com",
				AzureOpenAIEndpointJapan: "https://my-azure-japan.openai.azure.com",
				AzureOpenAIAPIKey:        "azure-secret-key",
			},
			urlStr: "http://localhost/realtime",
			header: http.Header{
				"X-Data-Residency": []string{"japan"},
			},
			wantErr:      false,
			wantURL:      "wss://my-azure-japan.openai.azure.com/openai/realtime?api-version=2024-10-01-preview&deployment=gpt-4o-realtime-preview",
			wantProvider: "azure",
		},
		{
			name: "Azure OpenAI missing API key",
			cfg: &config.Config{
				AzureOpenAIEndpoint: "https://my-azure.openai.azure.com",
				AzureOpenAIAPIKey:   "",
			},
			urlStr:  "http://localhost/realtime",
			header:  http.Header{},
			wantErr: true,
		},
		{
			name: "Azure OpenAI HTTP scheme translates to WS scheme",
			cfg: &config.Config{
				AzureOpenAIEndpoint: "http://localhost:8080",
				AzureOpenAIAPIKey:   "azure-secret-key",
			},
			urlStr:       "http://localhost/realtime",
			header:       http.Header{},
			wantErr:      false,
			wantURL:      "ws://localhost:8080/openai/realtime?api-version=2024-10-01-preview&deployment=gpt-4o-realtime-preview",
			wantProvider: "azure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxy := NewRealtimeProxy(tt.cfg)
			req, err := http.NewRequest(http.MethodGet, tt.urlStr, nil)
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}
			req.Header = tt.header

			gotURL, _, gotHeaders, err := proxy.resolveUpstream(req)

			if (err != nil) != tt.wantErr {
				t.Errorf("resolveUpstream() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if gotURL != tt.wantURL {
					t.Errorf("resolveUpstream() gotURL = %v, want %v", gotURL, tt.wantURL)
				}

				if tt.wantProvider == "azure" {
					if gotHeaders.Get("api-key") != tt.cfg.AzureOpenAIAPIKey {
						t.Errorf("resolveUpstream() missing or incorrect api-key header for azure")
					}
					if gotHeaders.Get("OpenAI-Beta") != "realtime=v1" {
						t.Errorf("resolveUpstream() missing OpenAI-Beta header for azure")
					}
				}
			}
		})
	}
}

func TestRealtimeProxy_ServeWebSocket_Integration(t *testing.T) {
	// Upstream test server acting as the target provider (e.g. Azure OpenAI)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Echo messages back
		for {
			mt, message, err := conn.ReadMessage()
			if err != nil {
				break
			}
			err = conn.WriteMessage(mt, message)
			if err != nil {
				break
			}
		}
	}))
	defer upstreamServer.Close()

	// Configuration to point to our test upstream server
	cfg := &config.Config{
		AzureOpenAIEndpoint: upstreamServer.URL,
		AzureOpenAIAPIKey:   "test-key",
	}

	proxy := NewRealtimeProxy(cfg)

	// Proxy test server acting as Kura Realtime Proxy
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mock tenantCtx, not heavily used in ServeWebSocket currently
		proxy.ServeWebSocket(w, r, &entity.TenantContext{})
	}))
	defer proxyServer.Close()

	// Convert proxy HTTP URL to WS URL
	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http")

	// Connect a client to the proxy
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer clientConn.Close()

	// Test message flow: Client -> Proxy -> Upstream -> Proxy -> Client
	testMsg := []byte("hello realtime")
	if err := clientConn.WriteMessage(websocket.TextMessage, testMsg); err != nil {
		t.Fatalf("Failed to write message: %v", err)
	}

	clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	mt, receivedMsg, err := clientConn.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read message: %v", err)
	}

	if mt != websocket.TextMessage {
		t.Errorf("Expected text message type, got %v", mt)
	}

	if string(receivedMsg) != string(testMsg) {
		t.Errorf("Expected message %q, got %q", string(testMsg), string(receivedMsg))
	}
}

func TestRealtimeProxy_ServeWebSocket_ResolveError(t *testing.T) {
	cfg := &config.Config{
		// Missing credentials will cause resolveUpstream to fail
	}

	proxy := NewRealtimeProxy(cfg)

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeWebSocket(w, r, &entity.TenantContext{})
	}))
	defer proxyServer.Close()

	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http")

	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer clientConn.Close()

	clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	mt, receivedMsg, err := clientConn.ReadMessage()

	// We expect the server to send an error message (entity.StandardError as JSON) and close the connection
	if err != nil {
		// Connection might be closed cleanly or with an error, that's fine if we get the close error
		if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
			t.Logf("Connection closed with error (expected): %v", err)
		}
	} else {
		// If we read a message before close, it should be the StandardError JSON
		if mt != websocket.TextMessage {
			t.Errorf("Expected text message type, got %v", mt)
		}
		if !strings.Contains(string(receivedMsg), "error") {
			t.Errorf("Expected error JSON message, got: %s", string(receivedMsg))
		}
	}
}

func TestRealtimeProxy_ServeWebSocket_UpstreamDialError(t *testing.T) {
	cfg := &config.Config{
		AzureOpenAIEndpoint: "http://127.0.0.1:0", // Invalid/closed port to cause dial error
		AzureOpenAIAPIKey:   "test-key",
	}

	proxy := NewRealtimeProxy(cfg)

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeWebSocket(w, r, &entity.TenantContext{})
	}))
	defer proxyServer.Close()

	wsURL := "ws" + strings.TrimPrefix(proxyServer.URL, "http")

	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect to proxy: %v", err)
	}
	defer clientConn.Close()

	clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	mt, receivedMsg, err := clientConn.ReadMessage()

	// We expect the server to send an error message and close
	if err != nil {
		if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
			t.Logf("Connection closed with error (expected): %v", err)
		}
	} else {
		if mt != websocket.TextMessage {
			t.Errorf("Expected text message type, got %v", mt)
		}
		if !strings.Contains(string(receivedMsg), "Vendor connection error") {
			t.Errorf("Expected upstream dial error message, got: %s", string(receivedMsg))
		}
	}
}
