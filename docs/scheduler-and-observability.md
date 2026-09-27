# スケジューラー & オブザーバビリティ仕様書

## 1. 概要
Kura は、外部の AWS EventBridge や AWS Lambda を必要とせず、Go サーバープロセス内（Goroutine）で自律的に動作する内蔵バッチスケジューラー（`CronScheduler`）を備えている。
また、メインのリクエスト処理を一切ブロックしない非同期構造化ロガーにより、CloudWatch Logs や Firehose への低遅延なログ転送を実現する。

---

## 2. 内蔵バッチスケジューラー (In-Process Scheduler)

### 2.1 定期ジョブ一覧
日本標準時 (JST) 基準で以下の定期バッチが自動実行される。

| ジョブ名 | 実行スケジュール | ロックキー形式 | TTL | 処理内容 |
|---|---|---|---|---|
| **月次締めレポート通知**<br>(`monthly_report`) | 毎月1日 00:05 (JST) | `monthly_report#<YYYY-MM>` | 30日 | 前月の全サービス・全テナントのトークン消費量・概算コストを集計し、Slack / アプリ内通知へ配信。 |
| **クォータ残量低下アラート**<br>(`quota_alerts`) | 毎時 00分 (JST) | `quota_alert#<YYYY-MM-DD-HH>` | 1時間 | `capped` プランで月次予算消費率が 80% / 90% を超過したテナントを検知し、Slack / アプリ内通知へ警告配信。 |
| **残高補正ジョブ**<br>(`reconcile`) | 一定間隔（既定: 300秒） | `reconcile#<YYYY-MM>` | 10分 | Valkey/Redis コストストアの残高を集計結果ストアの実績データから再構築・同期。 |

※ 管理用 API（`POST /v1/admin/jobs/run`, `POST /v1/admin/jobs/reconcile`）から手動で即時トリガー実行することも可能。

### 2.2 分散ロックによる二重実行防止
マルチコンテナ構成（Auto Scaling）時でも、同一バッチの二重実行を防止するため、集計結果ストア（DynamoDB, Cosmos DB, Firestore, SQLite）を用いた分散ロック制御を行う。

1. 実行タイミングで一意のロックキー（例: `LOCK#monthly_report#2026-09`）を作成。
2. ストアへ条件付き書き込みまたはトランザクションロックを試行。
3. ロック獲得に成功したインスタンスのみがジョブを実行し、他インスタンスはスキップ。
4. 有効期限を設定することで、ジョブ異常終了時でも一定時間後にロックが自動解放される。

---

## 3. オブザーバビリティ & 構造化ログ

### 3.1 非同期ロギングアーキテクチャ
Gateway のリバースプロキシ層は、リクエスト完了時に `UsageLogEvent` を Go チャネル（デフォルトバッファサイズ: 10,000）へ投入し、即座にクライアントへ制御を返す。バックグラウンド Goroutine がチャネルから順次取り出し、標準出力 (`stdout`) へ 1 行 1 JSON 形式で出力する。

```
Client <─── Gateway Proxy (即時レスポンス)
                 │ (非同期投入)
                 v
         Go Channel Buffer (10,000)
                 │
                 v (バックグラウンド Worker)
         stdout (JSON 構造化ログ) ───> CloudWatch Logs / Firehose
```

### 3.2 ログイベント形式 (`UsageLogEvent`)
```json
{
  "request_id": "4b421984-d6a1-48b0-a822-cc3b872bf01a",
  "service_id": "payment-service",
  "tenant_id": "team-alpha",
  "user_id": "user-01",
  "model": "gpt-5.4-mini",
  "prompt_tokens": 15,
  "completion_tokens": 12,
  "total_tokens": 27,
  "cost_usd": 0.000009,
  "latency_ms": 142,
  "vendor_latency_ms": 141,
  "gateway_latency_ms": 1,
  "is_stream": true,
  "environment": "staging",
  "feature": "checkout-rag",
  "tags": {
    "team": "infra",
    "experiment": "v1"
  },
  "timestamp": "2026-09-17T01:51:53.671Z"
}
```

### 3.3 分散トレーシング (W3C Trace Context)
リクエストヘッダーに含まれる `traceparent` および `X-Request-ID` をプロバイダー側へ透過的に中継し、Gateway 前後のマイクロサービスと一気通貫した分散トレース（AWS X-Ray / OpenTelemetry 等）を維持する。

### 3.4 Prometheus メトリクス (`/metrics`)
Gateway は標準で Prometheus スクレイピング用エンドポイント（`GET /metrics`）を公開しており、Grafana、Datadog、CloudWatch Agent、OpenTelemetry Collector 等によるリアルタイム監視に対応している。

| メトリクス名 | 種別 | ラベル | 説明 |
|---|:---:|---|---|
| `kura_requests_total` | Counter | `model`, `status`, `stream`, `service_id` | HTTP / WebSocket リクエスト総数 |
| `kura_tokens_total` | Counter | `model`, `type`, `service_id` | 累積トークン消費量 |
| `kura_request_duration_seconds` | Histogram | `model`, `stream`, `status` | リクエスト全体の所要時間（レイテンシー分布） |
| `kura_time_to_first_token_seconds` | Histogram | `model` | ストリーミング時の初速トークン生成時間 (TTFT) |
| `kura_estimated_cost_usd_total` | Counter | `model`, `service_id` | トークン消費に基づく概算コスト累計 (USD) |
| `kura_active_requests` | Gauge | なし | 現在処理中のアクティブ接続数 |
| `kura_gateway_auth_failures_total` | Counter | なし | ゲートウェイ共有シークレット認証失敗総数 |
| `kura_cache_hits_total` | Counter | `type` | キャッシュヒット回数 |
| `kura_cache_misses_total` | Counter | `type` | キャッシュミス回数 |
| `kura_cache_negative_rejections_total` | Counter | `service_id` | ネガティブキャッシュによる 429 即時遮断数 |
| `kura_cache_flush_total` | Counter | なし | キャッシュバッチ書き込みフラッシュ回数 |
| `kura_cache_buffered_cost_usd` | Gauge | なし | バッファ内未反映コスト額 (USD) |
| `go_*`, `process_*` | Various | なし | Go ランタイムおよびプロセスメトリクス |
