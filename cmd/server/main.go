package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	delivery "github.com/kanmon-hq/kura/internal/delivery/http"
	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/domain/repository"
	"github.com/kanmon-hq/kura/internal/infrastructure/adapter"
	"github.com/kanmon-hq/kura/internal/infrastructure/cache"
	"github.com/kanmon-hq/kura/internal/infrastructure/config"
	"github.com/kanmon-hq/kura/internal/infrastructure/logger"
	"github.com/kanmon-hq/kura/internal/infrastructure/metrics"
	"github.com/kanmon-hq/kura/internal/infrastructure/notifier"
	"github.com/kanmon-hq/kura/internal/infrastructure/proxy"
	"github.com/kanmon-hq/kura/internal/infrastructure/scheduler"
	"github.com/kanmon-hq/kura/internal/infrastructure/store"
	"github.com/kanmon-hq/kura/internal/infrastructure/websocket"
	"github.com/kanmon-hq/kura/internal/usecase"
)

func main() {
	// 1. 設定のロード & 検証
	cfg := config.Load()
	if err := cfg.ValidateGatewayAuth(); err != nil {
		log.Fatalf("[FATAL] Gateway authentication configuration invalid: %v", err)
	}

	log.Printf("[INFO] Starting Kura on port %s (Region: %s, CostStore: %s, UsageStore: %s)",
		cfg.Port, cfg.AWSRegion, cfg.CostStoreType, cfg.UsageStoreType)

	// 2. 単価表 (Pricing Engine) のロードと検証
	pricingEngine := entity.DefaultEngine()
	if cfg.PricingFilePath != "" {
		if err := pricingEngine.LoadFromFile(cfg.PricingFilePath); err != nil {
			log.Fatalf("[FATAL] Failed to load pricing file '%s': %v", cfg.PricingFilePath, err)
		}
	}
	if cfg.UnknownModelPolicy != "" {
		pricingEngine.SetUnknownModelPolicy(cfg.UnknownModelPolicy)
	}

	// 3. インフラ層（ストア・ロガー・メトリクス・アダプター）の初期化
	usageLogger := logger.NewConsoleLogger(cfg.LogChannelBufferSize)

	storeBundle, err := store.InitializeStores(cfg)
	if err != nil {
		log.Fatalf("[FATAL] Failed to initialize storage backends: %v", err)
	}
	rawCostStore := storeBundle.CostStore
	usageStore := storeBundle.UsageStore

	// Prometheus メトリクス
	promMetrics := metrics.NewMetrics()

	// キャッシュ層デコレーター (SQLite 以外で CacheEnabled の場合に適用)
	var costStore repository.CostStore = rawCostStore
	var cachedStore *cache.CachedCostStore
	isSQLite := strings.ToLower(strings.TrimSpace(cfg.CostStoreType)) == "sqlite"

	if cfg.CacheEnabled && !isSQLite {
		cacheOpts := cache.CacheOptions{
			Enabled:            true,
			NegativeTTL:        time.Duration(cfg.CacheNegativeTTLSeconds) * time.Second,
			ConfigTTL:          time.Duration(cfg.CacheConfigTTLSeconds) * time.Second,
			BalanceTTL:         time.Duration(cfg.CacheBalanceTTLSeconds) * time.Second,
			BatchFlushInterval: time.Duration(cfg.CacheBatchFlushIntervalSeconds) * time.Second,
		}
		cachedStore = cache.NewCachedCostStore(rawCostStore, promMetrics, cacheOpts)
		costStore = cachedStore
		log.Printf("[INFO] [CACHE] Cache layer enabled (NegativeTTL: %ds, ConfigTTL: %ds, BalanceTTL: %ds, BatchFlush: %ds)",
			cfg.CacheNegativeTTLSeconds, cfg.CacheConfigTTLSeconds, cfg.CacheBalanceTTLSeconds, cfg.CacheBatchFlushIntervalSeconds)
	} else if isSQLite {
		log.Printf("[INFO] [CACHE] Cache layer bypassed for SQLite single-node backend.")
	}

	// ベンダーアダプター
	openAIAdapter := adapter.NewOpenAIAdapter(cfg)
	bedrockAdapter := adapter.NewBedrockAdapter(cfg)
	geminiAdapter := adapter.NewGeminiAdapter(cfg)

	// プロバイダ有効化状況のロギング
	log.Printf("[INFO] Provider Status -> Azure (GPT/Claude): %t, Bedrock: %t, Gemini: %t",
		openAIAdapter.IsEnabled(), bedrockAdapter.IsEnabled(), geminiAdapter.IsEnabled())

	// エンドポイント・ルーティング設定のロード (endpoints.json)
	routingCfg, err := entity.LoadRoutingConfig(cfg.EndpointsFilePath)
	if err != nil {
		log.Printf("[WARN] [ROUTING] Failed to load %s: %v. Generating default pool from environment variables.", cfg.EndpointsFilePath, err)
		routingCfg = &entity.RoutingConfig{}
	}
	// 設定ファイルに default がない場合、環境変数からフォールバックプールを構築
	if len(routingCfg.Default) == 0 && openAIAdapter.IsEnabled() {
		routingCfg.Default = append(routingCfg.Default, entity.EndpointConfig{
			Name:          "azure-default",
			Provider:      "azure",
			URL:           cfg.AzureOpenAIEndpoint,
			Key:           cfg.AzureOpenAIAPIKey,
			DataResidency: "global",
			Priority:      1,
		})
		if cfg.AzureOpenAIEndpointJapan != "" {
			routingCfg.Default = append(routingCfg.Default, entity.EndpointConfig{
				Name:          "azure-japaneast",
				Provider:      "azure",
				URL:           cfg.AzureOpenAIEndpointJapan,
				Key:           cfg.AzureOpenAIAPIKey,
				DataResidency: "japan",
				Priority:      1,
			})
		}
	}
	if routingCfg.Prefixes == nil {
		routingCfg.Prefixes = make(map[string][]entity.EndpointConfig)
	}
	if _, ok := routingCfg.Prefixes["claude"]; !ok && (bedrockAdapter.IsEnabled() || openAIAdapter.IsEnabled()) {
		if bedrockAdapter.IsEnabled() {
			routingCfg.Prefixes["claude"] = append(routingCfg.Prefixes["claude"], entity.EndpointConfig{
				Name:     "bedrock-claude",
				Provider: "bedrock",
				Region:   cfg.BedrockRegion,
				Key:      cfg.BedrockAPIKey,
				Priority: 1,
			})
		}
		if openAIAdapter.IsEnabled() {
			routingCfg.Prefixes["claude"] = append(routingCfg.Prefixes["claude"], entity.EndpointConfig{
				Name:     "azure-claude",
				Provider: "azure",
				URL:      cfg.AzureOpenAIEndpoint,
				Key:      cfg.AzureOpenAIAPIKey,
				Priority: 2,
			})
		}
	}
	if _, ok := routingCfg.Prefixes["gemini"]; !ok && geminiAdapter.IsEnabled() {
		routingCfg.Prefixes["gemini"] = []entity.EndpointConfig{
			{
				Name:     "gemini-default",
				Provider: "gemini",
				Key:      cfg.GeminiAPIKey,
				URL:      cfg.GeminiBaseURL,
				Priority: 1,
			},
		}
	}

	router := entity.NewRouter(routingCfg, entity.NewCircuitBreaker())
	log.Printf("[INFO] [ROUTING] Intelligent router initialized (Default: %d endpoints, Prefixes: %d, Overrides: %d)",
		len(routingCfg.Default), len(routingCfg.Prefixes), len(routingCfg.Overrides))

	// プロキシ & WebSocket
	llmProxy := proxy.NewLLMProxy(usageLogger, costStore, usageStore, pricingEngine, promMetrics)
	realtimeProxy := websocket.NewRealtimeProxy(cfg)

	// 4. ユースケース層の初期化
	authUseCase := usecase.NewAuthUseCaseWithConfig(costStore, usageStore, usecase.AuthUseCaseConfig{
		EnforceTollgateAuth: cfg.EnforceTollgateAuth,
	})
	adminUseCase := usecase.NewAdminUseCase(costStore, usageStore)
	chatUseCase := usecase.NewChatUseCase(openAIAdapter, llmProxy)
	chatUseCase.SetRouter(router)
	chatUseCase.RegisterProviderAdapter("azure", openAIAdapter)
	chatUseCase.RegisterProviderAdapter("bedrock", bedrockAdapter)
	chatUseCase.RegisterProviderAdapter("gemini", geminiAdapter)
	chatUseCase.RegisterProviderAdapter("openai", openAIAdapter)

	chatUseCase.RegisterAdapter("bedrock/", bedrockAdapter)
	chatUseCase.RegisterAdapter("amazon.", bedrockAdapter)
	chatUseCase.RegisterAdapter("anthropic.", bedrockAdapter)
	chatUseCase.RegisterAdapter("gemini/", geminiAdapter)
	chatUseCase.RegisterAdapter("gemini-", geminiAdapter)

	internalNotifier := notifier.NewInternalNotifier(usageStore)
	batchUseCase := usecase.NewBatchUseCase(costStore, usageStore, internalNotifier)

	// 5. 定期バッチ・補正スケジューラーの初期化
	var cronScheduler *scheduler.CronScheduler
	if cfg.EnableInternalCron {
		cronScheduler = scheduler.NewCronSchedulerWithReconcile(batchUseCase, cfg.ReconcileIntervalSeconds)
		cronScheduler.Start()
	}

	// 6. プレゼンテーション層（HTTP ハンドラ・ミドルウェア）の初期化
	gatewayAuthConfig := delivery.GatewayAuthConfig{
		SharedSecret:         cfg.GatewaySharedSecret,
		SharedSecretPrevious: cfg.GatewaySharedSecretPrevious,
		HeaderName:           cfg.GatewaySecretHeader,
		InsecureNoAuth:       cfg.InsecureNoGatewayAuth,
	}
	authMiddleware := delivery.NewAuthMiddleware(authUseCase, gatewayAuthConfig, promMetrics)
	handler := delivery.NewHandler(chatUseCase, realtimeProxy, costStore, usageStore, authUseCase)
	adminHandler := delivery.NewAdminHandler(adminUseCase, batchUseCase, cfg.AdminAPIKey)

	// 7. ルーティング & Huma v2 (OpenAPI 3.1 & Scalar 自動生成) 設定
	mux := http.NewServeMux()
	delivery.SetupHumaAPI(mux, handler, authMiddleware, adminHandler, cfg.DocsPath, cfg.OpenAPIPath)

	// OpenAI 互換標準パスの直接ルーティング
	standardChatHandler := authMiddleware.Wrap(handler.ChatCompletions)
	mux.HandleFunc("/v1/chat/completions", standardChatHandler)
	mux.HandleFunc("/v1/realtime", authMiddleware.Wrap(handler.Realtime))
	mux.HandleFunc("/v1/usage", authMiddleware.Wrap(handler.GetKeyUsage))
	mux.HandleFunc("/healthz", handler.HealthCheck)
	mux.HandleFunc("/livez", handler.Liveness)
	mux.HandleFunc("/readyz", handler.Readiness)
	mux.Handle("/metrics", promMetrics.Handler())

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           delivery.CORSMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// 8. サーバー起動
	go func() {
		log.Printf("[INFO] Server listening on http://0.0.0.0:%s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[FATAL] Server listen failed: %v", err)
		}
	}()

	// 9. Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	sig := <-quit
	log.Printf("[INFO] Received shutdown signal: %v. Initiating graceful shutdown...", sig)

	// ALB / クラスターからの新規トラフィックを遮断するため、即座に Readiness を落とす
	handler.SetShuttingDown(true)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("[ERROR] Server forced shutdown: %v", err)
	}

	// スケジューラーの安全停止
	if cronScheduler != nil {
		cronScheduler.Stop()
	}

	// キャッシュバッファのフラッシュ
	if cachedStore != nil {
		if err := cachedStore.Close(); err != nil {
			log.Printf("[ERROR] Error flushing cached store: %v", err)
		}
	}

	// ロガーのフラッシュ
	if err := usageLogger.Close(); err != nil {
		log.Printf("[ERROR] Error closing usage logger: %v", err)
	}

	log.Println("[INFO] Kura exited successfully.")
}
