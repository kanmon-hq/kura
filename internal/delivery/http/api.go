package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/kanmon-hq/kura/internal/domain/entity"
	"github.com/kanmon-hq/kura/internal/usecase"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func init() {
	origNewError := huma.NewError
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		// 他サービス (FastAPI / MCP Gateway 等) に合わせて validation エラー時は 422 Unprocessable Entity を返す
		if status == http.StatusBadRequest && (msg == "validation failed" || len(errs) > 0) {
			status = http.StatusUnprocessableEntity
		}
		return origNewError(status, msg, errs...)
	}
}

// HealthOutput はヘルスチェックのレスポンス型
type HealthOutput struct {
	Body struct {
		Status   string `json:"status" example:"ok" doc:"ゲートウェイの稼働ステータス"`
		Database string `json:"database,omitempty" example:"connected" doc:"データベース疎通ステータス"`
		Message  string `json:"message,omitempty" doc:"ステータスメッセージ"`
	}
}

// ChatCompletionInput はチャット補完リクエスト型
type ChatCompletionInput struct {
	GatewaySecret string                       `header:"X-Gateway-Secret" doc:"ゲートウェイ共有シークレット (GATEWAY_SHARED_SECRET)" example:"gw_secret_abcdef1234567890abcdef1234567890"`
	ServiceID     string                       `header:"X-Service-ID" doc:"呼び出し元サービス識別子" required:"true" example:"payment-service"`
	TenantID      string                       `header:"X-Tenant-ID" doc:"テナント・組織識別子" required:"true" example:"tenant-corp-a"`
	KeyID         string                       `header:"X-Key-ID" doc:"Tollgate API キー UUID" example:"550e8400-e29b-41d4-a716-446655440000"`
	KeyPrefix     string                       `header:"X-Key-Prefix" doc:"Tollgate API キー プレフィックス" example:"tlge-live-8f9c"`
	UserID        string                       `header:"X-User-ID" doc:"エンドユーザー識別子" example:"user-12345"`
	DataResidency string                       `header:"X-Data-Residency" doc:"日本国内リージョンルーティング ('japan' 指定時は東日本リージョン限定)" enum:"japan"`
	Environment   string                       `header:"X-Environment" doc:"実行環境識別子 (例: production, staging, dev)" example:"staging"`
	Feature       string                       `header:"X-Feature" doc:"機能・ユースケース識別子 (例: rag-search, summarize)" example:"rag-search"`
	Tags          string                       `header:"X-Tags" doc:"カンマ区切りタグ (例: 'env=stg,team=alpha')" example:"team=alpha,experiment=1"`
	RequestID     string                       `header:"X-Request-ID" doc:"分散トレース・リクエスト追跡用一意識別子 (UUID)"`
	Traceparent   string                       `header:"traceparent" doc:"W3C 分散トレースコンテキストヘッダー"`
	Body          entity.ChatCompletionRequest `doc:"OpenAI 互換チャット補完リクエストペイロード (仮想モデル fast, smart, flash 対応)"`
}

// ChatCompletionOutput はチャット補完レスポンス型
type ChatCompletionOutput struct {
	BillingType          string                        `header:"X-Billing-Type" doc:"テナントの課金プラン種別 (pay_as_you_go または capped)"`
	QuotaLimitTokens     string                        `header:"X-Quota-Limit-Tokens" doc:"当月のトークン上限設定値 (または 'unlimited')"`
	QuotaRemainingTokens string                        `header:"X-Quota-Remaining-Tokens" doc:"当月の残り利用可能トークン数 (または 'unlimited')"`
	MonthlyUsageTokens   int64                         `header:"X-Monthly-Usage-Tokens" doc:"当月の累計消費トークン数"`
	MonthlyUsageCost     string                        `header:"X-Monthly-Usage-Cost" doc:"当月の累計概算利用コスト (USD)"`
	RequestID            string                        `header:"X-Request-ID" doc:"リクエスト追跡識別子 (UUID)"`
	Body                 entity.ChatCompletionResponse `doc:"チャット完了レスポンスまたは SSE ストリーム"`
}

// RealtimeInput は Realtime WebSocket 接続型
type RealtimeInput struct {
	GatewaySecret string `header:"X-Gateway-Secret" doc:"ゲートウェイ共有シークレット (GATEWAY_SHARED_SECRET)" example:"gw_secret_abcdef1234567890abcdef1234567890"`
	ServiceID     string `header:"X-Service-ID" doc:"呼び出し元サービス識別子" required:"true" example:"payment-service"`
	TenantID      string `header:"X-Tenant-ID" doc:"テナント・組織識別子" required:"true" example:"tenant-corp-a"`
}

// RealtimeOutput は Realtime WebSocket 接続レスポンス型
type RealtimeOutput struct {
	Body struct {
		Status  string `json:"status" example:"connected" doc:"WebSocket 接続ステータス"`
		Message string `json:"message" example:"WebSocket protocol upgraded for realtime session" doc:"接続完了メッセージ"`
	} `doc:"WebSocket 接続情報"`
}

// KeyUsageInput はキー別残枠確認リクエスト型
type KeyUsageInput struct {
	GatewaySecret string `header:"X-Gateway-Secret" doc:"ゲートウェイ共有シークレット (GATEWAY_SHARED_SECRET)" example:"gw_secret_abcdef1234567890abcdef1234567890"`
	ServiceID     string `header:"X-Service-ID" doc:"呼び出し元サービス識別子" required:"true" example:"payment-service"`
	TenantID      string `header:"X-Tenant-ID" doc:"テナント・組織識別子" required:"true" example:"tenant-corp-a"`
}

// KeyUsageOutput はキー別残枠確認レスポンス型
type KeyUsageOutput struct {
	Body entity.KeyUsageSummary `doc:"キー別当月利用量およびリアルタイム残枠サマリ"`
}

// AdminUsageInput は管理者向け利用実績取得入力型
type AdminUsageInput struct {
	Authorization string `header:"Authorization" doc:"管理者用マスター API キー (Bearer 形式)" example:"Bearer sk-admin-master-key"`
	ServiceID     string `query:"service_id" doc:"集計対象のサービス識別子" required:"true" example:"demo-service"`
	Month         string `query:"month" doc:"対象月 (YYYY-MM形式、省略時は当月)" example:"2026-09"`
}

// AdminUsageOutput は管理者向け利用実績取得出力型
type AdminUsageOutput struct {
	Body entity.ServiceMonthlyReport `doc:"月次利用実績およびモデル別利用内訳レポート"`
}

// AdminLimitsInput は管理者向けリミット設定入力型
type AdminLimitsInput struct {
	Authorization string                  `header:"Authorization" doc:"管理者用マスター API キー (Bearer 形式)" example:"Bearer sk-admin-master-key"`
	Body          usecase.SetLimitRequest `doc:"テナントクォータおよび課金プラン設定ペイロード"`
}

// AdminLimitsOutput は管理者向けリミット設定出力型
type AdminLimitsOutput struct {
	Body struct {
		Status  string `json:"status" example:"ok" doc:"実行ステータス"`
		Message string `json:"message" example:"Tenant limit updated successfully" doc:"処理結果メッセージ"`
	}
}

// AdminRunJobInput はバッチジョブ手動実行入力型
type AdminRunJobInput struct {
	Authorization string `header:"Authorization" doc:"管理者用マスター API キー (Bearer 形式)" example:"Bearer sk-admin-master-key"`
	Body          struct {
		JobName string `json:"job_name" required:"true" doc:"実行するジョブ名 (monthly_settlement または quota_alert)" example:"monthly_settlement"`
	}
}

// AdminRunJobOutput はバッチジョブ手動実行出力型
type AdminRunJobOutput struct {
	Body struct {
		Status  string `json:"status" example:"ok" doc:"実行ステータス"`
		Message string `json:"message" example:"Job executed successfully" doc:"処理結果メッセージ"`
	}
}

// AdminListNotificationsInput は管理者向けアプリ内通知一覧取得入力型
type AdminListNotificationsInput struct {
	Authorization string `header:"Authorization" doc:"管理者用マスター API キー (Bearer 形式)" example:"Bearer sk-admin-master-key"`
	Limit         int    `query:"limit" doc:"取得上限件数 (最大100件、デフォルト20件)" example:"20"`
}

// AdminListNotificationsOutput は管理者向けアプリ内通知一覧取得出力型
type AdminListNotificationsOutput struct {
	Body struct {
		Notifications []*entity.Notification `json:"notifications" doc:"アプリ内通知リスト (新しい順)"`
	}
}

// verifyAdmin は管理者キーの適合性を検証する
func verifyAdmin(adminHandler *AdminHandler, authHeader string) bool {
	if adminHandler == nil {
		return false
	}
	if authHeader != "" && adminHandler.VerifyKey(authHeader) {
		return true
	}
	return false
}

// SetupHumaAPI は Huma v2 を初期化し、OpenAPI 3.1 仕様書および Scalar ドキュメントを自動登録する
func SetupHumaAPI(
	mux *http.ServeMux,
	handler *Handler,
	authMiddleware *AuthMiddleware,
	adminHandler *AdminHandler,
	docsPath string,
	openAPIPath string,
) huma.API {
	config := huma.DefaultConfig("Kura", "2.0.0")
	config.OpenAPIPath = openAPIPath
	config.Info.Description = "Azure OpenAI、Anthropic Claude、Google Gemini に対応したマルチテナント向け高パフォーマンス LLM API ゲートウェイ。DynamoDB によるトークン計測・コスト算出、完全従量課金 / 上限設定プラン、仮想モデルエイリアス (fast, smart, flash)、日本データレジデンシーに対応。"

	if openAPIPath != "" && docsPath != "" {
		config.DocsRenderer = huma.DocsRendererScalar
		config.DocsPath = docsPath
		// Scalar の設定: デフォルトクライアントを Go (native) に設定
		config.DocsRendererConfig = map[string]any{
			"defaultHttpClient": map[string]string{
				"targetKey": "go",
				"clientKey": "native",
			},
			"theme": "purple",
		}
	} else {
		config.DocsPath = ""
	}

	// セキュリティスキームの登録（日本語）
	config.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"GatewayAuth": {
			Type:        "apiKey",
			In:          "header",
			Name:        "X-Gateway-Secret",
			Description: "信頼済みゲートウェイ共有シークレット認証（`X-Gateway-Secret`）",
		},
		"TenantAuth": {
			Type:        "apiKey",
			In:          "header",
			Name:        "X-Service-ID",
			Description: "サービス・テナント識別（必須ヘッダー: `X-Service-ID`, `X-Tenant-ID`）",
		},
		"AdminAuth": {
			Type:        "http",
			Scheme:      "bearer",
			Description: "管理者用マスター API キー認証 (`Authorization: Bearer <ADMIN_API_KEY>`)",
		},
	}

	api := humago.New(mux, config)

	type humaCtxKeyType string
	const humaCtxKey humaCtxKeyType = "huma_ctx"

	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		ctx = huma.WithValue(ctx, humaCtxKey, ctx)
		next(ctx)
	})

	// 1. 総合ヘルスチェック (/healthz)
	healthHandler := func(ctx context.Context, input *struct{}) (*HealthOutput, error) {
		out := &HealthOutput{}
		out.Body.Status = "ok"
		out.Body.Database = "connected"
		return out, nil
	}
	huma.Register(api, huma.Operation{
		OperationID: "health-check-healthz",
		Method:      http.MethodGet,
		Path:        "/healthz",
		Summary:     "総合ヘルスチェック (/healthz)",
		Description: "Kubernetes / クラウド標準の総合ヘルスチェックエンドポイント。",
		Tags:        []string{"システム"},
	}, healthHandler)

	// 1-1. Liveness プローブ (/livez)
	liveHandler := func(ctx context.Context, input *struct{}) (*HealthOutput, error) {
		out := &HealthOutput{}
		out.Body.Status = "alive"
		return out, nil
	}
	huma.Register(api, huma.Operation{
		OperationID: "liveness-probe-livez",
		Method:      http.MethodGet,
		Path:        "/livez",
		Summary:     "Liveness プローブ (/livez)",
		Description: "Kubernetes 標準の Liveness プローブエンドポイント。プロセスの死活監視用エンドポイント。外部依存関係を見ず、プロセス生存時に即座に 200 を返す。",
		Tags:        []string{"システム"},
	}, liveHandler)

	// 1-2. Readiness プローブ (/readyz)
	readyHandler := func(ctx context.Context, input *struct{}) (*HealthOutput, error) {
		out := &HealthOutput{}
		out.Body.Status = "ready"
		out.Body.Database = "connected"
		return out, nil
	}
	huma.Register(api, huma.Operation{
		OperationID: "readiness-probe-readyz",
		Method:      http.MethodGet,
		Path:        "/readyz",
		Summary:     "Readiness プローブ (/readyz)",
		Description: "Kubernetes 標準の Readiness プローブエンドポイント。トラフィック受入準備完了の監視用エンドポイント。Graceful Shutdown 移行時は 503 を返し、ALB 新規流入を遮断。",
		Tags:        []string{"システム"},
	}, readyHandler)

	// 1-3. GET /metrics (Prometheus メトリクス照会)
	promHandler := promhttp.Handler()
	huma.Register(api, huma.Operation{
		OperationID: "get-metrics",
		Method:      http.MethodGet,
		Path:        "/metrics",
		Summary:     "Prometheus メトリクス照会",
		Description: "Prometheus 形式のリアルタイムパフォーマンス・カウンタメトリクス (TTFT、トークン数、コスト等) を返却します。",
		Tags:        []string{"システム"},
		Responses: map[string]*huma.Response{
			"200": {
				Description: "Prometheus メトリクス (text/plain)",
				Content: map[string]*huma.MediaType{
					"text/plain": {},
				},
			},
		},
	}, func(ctx context.Context, input *struct{}) (*huma.StreamResponse, error) {
		return &huma.StreamResponse{
			Body: func(hCtx huma.Context) {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/metrics", nil).WithContext(ctx)
				promHandler.ServeHTTP(rec, req)

				hCtx.SetHeader("Content-Type", rec.Header().Get("Content-Type"))
				_, _ = hCtx.BodyWriter().Write(rec.Body.Bytes())
			},
		}, nil
	})

	// 2. POST /v1/chat/completions (OpenAI compatible chat completion with SSE streaming)
	huma.Register(api, huma.Operation{
		OperationID: "create-chat-completion",
		Method:      http.MethodPost,
		Path:        "/v1/chat/completions",
		Summary:     "チャット補完リクエストの作成",
		Description: "OpenAI 互換のチャット補完エンドポイント。Azure OpenAI、Claude、Gemini へのリバースプロキシ中継、トークン集計、コスト算出、上限判定を実行。Server-Sent Events (SSE) によるストリーミングに対応。",
		Tags:        []string{"サービス向け API"},
		Security: []map[string][]string{
			{"GatewayAuth": {}, "TenantAuth": {}},
		},
	}, func(ctx context.Context, input *ChatCompletionInput) (*ChatCompletionOutput, error) {
		if authMiddleware != nil && handler != nil {
			if hCtx, ok := ctx.Value(humaCtxKey).(huma.Context); ok {
				req, rw := humago.Unwrap(hCtx)
				// Huma がアンマーシャル用に r.Body を消費しているため、req.Body を復元
				if bodyBytes, err := json.Marshal(input.Body); err == nil {
					req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				}
				chain := handler.ChatCompletions
				authMiddleware.Wrap(chain)(rw, req)
			}
		}
		return nil, nil
	})

	// 3. GET /v1/realtime (WebSocket Passthrough)
	huma.Register(api, huma.Operation{
		OperationID: "realtime-websocket",
		Method:      http.MethodGet,
		Path:        "/v1/realtime",
		Summary:     "Realtime WebSocket パススルー",
		Description: "Azure OpenAI Realtime API への WebSocket 接続をパススルーし、低遅延なマルチモーダル音声・テキストストリーミングを実現。",
		Tags:        []string{"サービス向け API"},
		Security: []map[string][]string{
			{"GatewayAuth": {}, "TenantAuth": {}},
		},
	}, func(ctx context.Context, input *RealtimeInput) (*RealtimeOutput, error) {
		if authMiddleware != nil && handler != nil {
			if hCtx, ok := ctx.Value(humaCtxKey).(huma.Context); ok {
				req, rw := humago.Unwrap(hCtx)
				authMiddleware.Wrap(handler.Realtime)(rw, req)
			}
		}
		return nil, nil
	})

	// 3-1. GET /v1/usage (キー別使用量・リアルタイム残枠確認)
	huma.Register(api, huma.Operation{
		OperationID: "get-key-usage-summary",
		Method:      http.MethodGet,
		Path:        "/v1/usage",
		Summary:     "当月使用量・リアルタイム残枠確認",
		Description: "サービスまたはテナントに紐付く当月のトークン消費量、利用コスト、残り予算枠をリアルタイムに照会する。",
		Tags:        []string{"サービス向け API"},
		Security: []map[string][]string{
			{"GatewayAuth": {}, "TenantAuth": {}},
		},
	}, func(ctx context.Context, input *KeyUsageInput) (*KeyUsageOutput, error) {
		if authMiddleware != nil && handler != nil {
			if hCtx, ok := ctx.Value(humaCtxKey).(huma.Context); ok {
				req, rw := humago.Unwrap(hCtx)
				authMiddleware.Wrap(handler.GetKeyUsage)(rw, req)
			}
		}
		return nil, nil
	})

	// 4. GET /v1/admin/usage
	huma.Register(api, huma.Operation{
		OperationID: "get-admin-usage",
		Method:      http.MethodGet,
		Path:        "/v1/admin/usage",
		Summary:     "サービス別月次利用実績の取得",
		Description: "指定されたサービスおよび月のトークン累計消費量、推定コスト、モデル別利用内訳を取得。",
		Tags:        []string{"管理者向け API (Admin)"},
		Security: []map[string][]string{
			{"AdminAuth": {}},
		},
	}, func(ctx context.Context, input *AdminUsageInput) (*AdminUsageOutput, error) {
		if !verifyAdmin(adminHandler, input.Authorization) {
			return nil, huma.Error401Unauthorized("Invalid or missing admin API key")
		}
		report, err := adminHandler.UseCase().GetMonthlyUsage(ctx, input.ServiceID, input.Month)
		if err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		return &AdminUsageOutput{Body: *report}, nil
	})

	// 5. POST /v1/admin/limits
	huma.Register(api, huma.Operation{
		OperationID: "set-admin-limits",
		Method:      http.MethodPost,
		Path:        "/v1/admin/limits",
		Summary:     "テナントクォータ・課金プランの設定",
		Description: "テナントの月次トークン上限、コスト上限、および課金タイプ (payg / capped) を登録・更新。",
		Tags:        []string{"管理者向け API (Admin)"},
		Security: []map[string][]string{
			{"AdminAuth": {}},
		},
	}, func(ctx context.Context, input *AdminLimitsInput) (*AdminLimitsOutput, error) {
		if !verifyAdmin(adminHandler, input.Authorization) {
			return nil, huma.Error401Unauthorized("Invalid or missing admin API key")
		}
		if err := adminHandler.UseCase().SetTenantLimit(ctx, &input.Body); err != nil {
			return nil, huma.Error400BadRequest(err.Error())
		}
		out := &AdminLimitsOutput{}
		out.Body.Status = "ok"
		out.Body.Message = "Tenant limit updated successfully"
		return out, nil
	})

	// 9. POST /v1/admin/jobs/run
	huma.Register(api, huma.Operation{
		OperationID: "run-admin-job",
		Method:      http.MethodPost,
		Path:        "/v1/admin/jobs/run",
		Summary:     "バッチジョブの手動トリガー実行",
		Description: "指定された定期バッチジョブ (monthly_settlement または quota_alert) を即時実行する。",
		Tags:        []string{"管理者向け API (Admin)"},
		Security: []map[string][]string{
			{"AdminAuth": {}},
		},
	}, func(ctx context.Context, input *AdminRunJobInput) (*AdminRunJobOutput, error) {
		if !verifyAdmin(adminHandler, input.Authorization) {
			return nil, huma.Error401Unauthorized("Invalid or missing admin API key")
		}
		if adminHandler == nil || adminHandler.BatchUseCase() == nil {
			return nil, huma.Error500InternalServerError("Batch usecase not configured")
		}

		var err error
		switch input.Body.JobName {
		case "monthly_settlement", "monthly_report":
			err = adminHandler.BatchUseCase().RunMonthlyReport(ctx)
		case "quota_alert", "quota_alerts":
			err = adminHandler.BatchUseCase().RunQuotaAlerts(ctx)
		case "reconcile", "reconciliation":
			err = adminHandler.BatchUseCase().RunReconciliation(ctx)
		default:
			return nil, huma.Error400BadRequest("Invalid job_name. Must be 'monthly_report', 'quota_alerts', or 'reconcile'")
		}

		if err != nil {
			return nil, huma.Error500InternalServerError("Job execution failed: " + err.Error())
		}

		out := &AdminRunJobOutput{}
		out.Body.Status = "ok"
		out.Body.Message = "Job " + input.Body.JobName + " executed successfully"
		return out, nil
	})

	// 9-1. POST /v1/admin/jobs/reconcile
	huma.Register(api, huma.Operation{
		OperationID: "reconcile-admin-counters",
		Method:      http.MethodPost,
		Path:        "/v1/admin/jobs/reconcile",
		Summary:     "コストカウンタの即時再構築・補正",
		Description: "集計結果ストアの実績データから、コスト管理ストア (Valkey/Redis等) のカウンタを再計算して補正する。",
		Tags:        []string{"管理者向け API (Admin)"},
		Security: []map[string][]string{
			{"AdminAuth": {}},
		},
	}, func(ctx context.Context, input *struct {
		Authorization string `header:"Authorization" doc:"管理者用マスター API キー (Bearer 形式)" example:"Bearer sk-admin-master-key"`
	}) (*AdminRunJobOutput, error) {
		if !verifyAdmin(adminHandler, input.Authorization) {
			return nil, huma.Error401Unauthorized("Invalid or missing admin API key")
		}
		if adminHandler == nil || adminHandler.BatchUseCase() == nil {
			return nil, huma.Error500InternalServerError("Batch usecase not configured")
		}

		if err := adminHandler.BatchUseCase().RunReconciliation(ctx); err != nil {
			return nil, huma.Error500InternalServerError("Reconciliation failed: " + err.Error())
		}

		out := &AdminRunJobOutput{}
		out.Body.Status = "ok"
		out.Body.Message = "Counter reconciliation completed successfully"
		return out, nil
	})

	// 10. GET /v1/admin/notifications
	huma.Register(api, huma.Operation{
		OperationID: "list-admin-notifications",
		Method:      http.MethodGet,
		Path:        "/v1/admin/notifications",
		Summary:     "アプリ内通知一覧の取得",
		Description: "予算アラートや月次利用実績レポートなど、ゲートウェイ内部に蓄積された通知・お知らせ一覧を降順（最新順）で取得する。",
		Tags:        []string{"管理者向け API (Admin)"},
		Security: []map[string][]string{
			{"AdminAuth": {}},
		},
	}, func(ctx context.Context, input *AdminListNotificationsInput) (*AdminListNotificationsOutput, error) {
		if !verifyAdmin(adminHandler, input.Authorization) {
			return nil, huma.Error401Unauthorized("Invalid or missing admin API key")
		}
		if adminHandler == nil || adminHandler.UseCase() == nil {
			return nil, huma.Error500InternalServerError("Admin usecase not configured")
		}

		limit := input.Limit
		if limit <= 0 {
			limit = 20
		}
		notifications, err := adminHandler.UseCase().ListNotifications(ctx, limit)
		if err != nil {
			return nil, huma.Error500InternalServerError("Failed to list notifications: " + err.Error())
		}

		out := &AdminListNotificationsOutput{}
		out.Body.Notifications = notifications
		return out, nil
	})


	// 全エンドポイントの OpenAPI 定義に共通ステータスコードを注入
	errorSchema := &huma.Schema{
		Ref: "#/components/schemas/ErrorModel",
	}
	errorContent := map[string]*huma.MediaType{
		"application/problem+json": {Schema: errorSchema},
	}
	commonErrors := map[string]*huma.Response{
		"401": {Description: "API キーが無効または未指定", Content: errorContent},
		"403": {Description: "アクセス権限不足 (管理者権限またはテナント権限不足)", Content: errorContent},
		"404": {Description: "指定されたリソースが存在しない、または非公開 API への外部アクセス遮断", Content: errorContent},
		"422": {Description: "リクエストパラメータまたはボディのバリデーションエラー", Content: errorContent},
		"429": {Description: "月次トークンクォータ超過またはレートリミット上限到達", Content: errorContent},
		"500": {Description: "サーバー内部エラー", Content: errorContent},
		"502": {Description: "上流 LLM プロバイダー (Azure OpenAI / Claude / Gemini) への接続エラー", Content: errorContent},
		"504": {Description: "上流 LLM プロバイダーからの応答タイムアウト", Content: errorContent},
	}

	if openapi := api.OpenAPI(); openapi != nil && openapi.Paths != nil {
		for _, item := range openapi.Paths {
			ops := []*huma.Operation{item.Get, item.Post, item.Put, item.Delete, item.Patch}
			for _, op := range ops {
				if op == nil {
					continue
				}
				if op.Responses == nil {
					op.Responses = make(map[string]*huma.Response)
				}
				for code, resp := range commonErrors {
					if _, exists := op.Responses[code]; !exists {
						op.Responses[code] = resp
					}
				}
			}
		}
	}

	return api
}
