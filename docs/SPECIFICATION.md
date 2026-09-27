# Kura システム仕様書 (SPECIFICATION)

本ドキュメントは、Kura の全体仕様書のインデックスである。
詳細な技術仕様は、責務ごとに以下の各ドキュメントへ細分化されている。
各エンドポイントの詳細なリクエスト/レスポンススキーマは、自動生成される **[Scalar API ドキュメント](http://localhost:8080/docs)** を参照のこと。

---

## 📚 詳細仕様ドキュメント一覧

1. **[アーキテクチャ & システム設計](architecture.md)**
   - 全体アーキテクチャ構成図
   - レイヤー設計 (Standard Go Project Layout / Clean Architecture)
   - ストア分離アーキテクチャ (CostStore / UsageStore) & キャッシュ層
   - 仮想モデルエイリアス解決 (`fast`, `smart`, `flash`)
   - 日本データレジデンシー仕様 (`X-Data-Residency: japan`)
   - 未知パラメータの透過パススルー (`thinking` 等)

2. **[認証 & テナント解決仕様](auth-and-virtual-keys.md)**
   - ゲートウェイ共有シークレットによる信頼確認 (`X-Gateway-Secret`)
   - ヘッダー信頼モデル (`X-Service-ID`, `X-Tenant-ID`)
   - 管理者マスターキー認証 (`ADMIN_API_KEY`)
   - 透過メタデータ・タグ収集 (`X-Environment`, `X-Feature`, `X-Tags`)
   - 予算・クォータガード仕様

3. **[課金モデル & クォータ制御](billing-and-quota.md)**
   - 課金プラン種別 (`pay_as_you_go` / `capped`)
   - 外部 JSON 単価表ファイル (`pricing.json`) とコスト計算式
   - 未定義モデルポリシー (`warn` / `reject`)
   - レスポンスヘッダー仕様 (`X-Billing-Type`, `X-Quota-*`, `X-Monthly-Usage-*`)

4. **[DynamoDB スキーマ & 永続化仕様](dynamodb-schema.md)**
   - Single Table Design 設計方針
   - テーブル定義 & グローバルセカンダリインデックス (`GSI_ServiceUsage`)
   - エンティティ設計 & レコードパターン
   - アトミック集計加算 (Atomic Counter)
   - JST 基準の月次締め切り境界仕様

5. **[スケジューラー & オブザーバビリティ](scheduler-and-observability.md)**
   - 内蔵バッチスケジューラー (`CronScheduler`)
   - 分散ロック制御 (DynamoDB / Cosmos DB / Firestore / SQLite)
   - 月次レポートバッチ、予算残量低下アラート、残高補正ジョブ
   - 非同期構造化ロガー (`UsageLogEvent`) & 分散トレース伝播
   - Prometheus メトリクス定義

6. **[API リファレンス概要](api-reference.md)**
   - Scalar API ドキュメント UI (`/docs`)
   - OpenAPI 3.1 仕様書 (`/openapi`)
   - カテゴリ別エンドポイントサマリ一覧
