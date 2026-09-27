# Kura — Lightweight Multi-Tenant LLM Gateway

[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat&logo=go)](https://golang.org/)
[![Huma v2](https://img.shields.io/badge/Huma-v2.39+-8A2BE2.svg)](https://huma.rocks/)
[![Storage](https://img.shields.io/badge/Storage-SQLite%20%7C%20DynamoDB%20%7C%20PostgreSQL%20%7C%20Valkey-4053D6.svg)](https://github.com/northfieldzz/kura)
[![Prometheus](https://img.shields.io/badge/Prometheus-Metrics-E6522C.svg?logo=prometheus)](https://prometheus.io/)
[![OpenAPI](https://img.shields.io/badge/OpenAPI-3.1-6BA539.svg?logo=openapiinitiative)](https://spec.openapis.org/oas/v3.1.0)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED.svg?logo=docker)](https://www.docker.com/)
[![License](https://img.shields.io/badge/license-MPL--2.0-blue.svg)](LICENSE)

**Kura** は、マルチテナント・複数プロダクト環境における大規模言語モデル（Microsoft Foundry / Azure OpenAI、Google Gemini、Amazon Bedrock 等）へのアクセスを一元管理・中継する、超軽量・極低レイテンシーな LLM API ゲートウェイである。

Kura は独立した LLM ゲートウェイとして単体で運用できるほか、前段に認証プロキシや API ゲートウェイ（Kong, Envoy, AWS API Gateway, 自前認証層、または姉妹プロジェクトの [**Tollgate**](https://github.com/northfieldzz/tollgate) 等）を配置して組み合わせることも可能である。Kura 自身は上流プロバイダーへの安全なルーティング、マルチコンテナ環境でのリアルタイムなコストガード（ソフトリミット）、仮想モデルエイリアス解決、国内データレジデンシー制御に専念する。分間リクエスト制限（RPM）などのレート制限は前段ゲートウェイの責務として分離している。

> [!IMPORTANT]
> Kura はリクエストヘッダー（`X-Service-ID`, `X-Tenant-ID` 等）を信頼してテナント解決およびクォータ制御を行う設計である。エンドユーザーの API キー認証は前段ゲートウェイの責務とし、Kura は「リクエストが信頼するゲートウェイから到達したこと」を**ゲートウェイ共有シークレット**（`X-Gateway-Secret`）で検証する。共有シークレットはネットワーク分離（VPC / ファイアウォール等）の代替ではなく、多層防御の一部である。Kura を単体でパブリックインターネットへ直接公開してはならない。

---

## 運用モードと信頼モデル

Kura はシステムのセキュリティ要件やネットワーク構成に応じて、以下の 2 つの運用形態に対応している。

| 運用形態 | 適用シナリオ | 認証・信頼確認の仕組み |
| :--- | :--- | :--- |
| **単体運用 / 開発**<br/>(Standalone / Dev) | ローカル開発環境、または完全隔離されたプライベートネットワーク | `INSECURE_NO_GATEWAY_AUTH=true` を明示して起動。クライアントが直接 `X-Service-ID` および `X-Tenant-ID` を付与してリクエストする（検証なし）。 |
| **認証ゲートウェイ併用**<br/>(Gateway-Backed / Production) | 本番環境、マルチコンテナ環境、パブリックインターネット境界 | 前段に認証ゲートウェイ（Tollgate, Kong, 自前プロキシ等）を配置。ゲートウェイがエンドユーザー認証を行い、`X-Gateway-Secret` と正しいテナント識別ヘッダーを付与して Kura へ転送する。 |

---

## 信頼関係とゲートウェイ認証 (Gateway Shared Secret)

Kura は `X-Tenant-ID` や `X-Service-ID` 等のヘッダーを信頼してクォータ制御を行う設計である。プライベートネットワーク内であっても、同一ネットワーク内の別ワークロードによるヘッダー偽装を防ぐため、**ゲートウェイ共有シークレット**による相互信頼確認を提供する。

### 基本方針
- **エンドユーザー認証の分離**: Kura はエンドユーザーの API キー認証を行わない（前段ゲートウェイの責務）。
- **ゲートウェイ信頼確認**: Kura は「リクエストが信頼するゲートウェイから送信されたこと」のみを、リクエストヘッダー（既定: `X-Gateway-Secret`）の定数時間比較（`crypto/subtle.ConstantTimeCompare`）により確認する。
- **プロバイダー中継からの除外**: `X-Gateway-Secret` は Kura 内部で消費・検証され、ログ・メトリクスはもちろん、上流の LLM プロバイダー（Foundry / Gemini / Bedrock）へのリクエストヘッダーには一切転送されない。

### 設定方法

シークレットは **32 文字以上**のランダムな文字列を設定する必要がある（32 文字未満または未設定の場合は起動を拒否する）。

```bash
# 安全な 32 バイト（64 文字の 16 進数）シークレットの生成例
openssl rand -hex 32
```

```env
# 本番環境 (.env)
GATEWAY_SHARED_SECRET="a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef0123456789abcdef0"
GATEWAY_SECRET_HEADER="X-Gateway-Secret"   # 任意 (既定: X-Gateway-Secret)

# 開発・検証専用 (シークレット検証をバイパス)
# INSECURE_NO_GATEWAY_AUTH=true
```

### 無停止シークレットローテーション手順

サービスを停止することなくシークレットをローテーションできるよう、新旧シークレットを並行して受け付ける環境変数 `GATEWAY_SHARED_SECRET_PREVIOUS` を提供している。

```mermaid
sequenceDiagram
    autonumber
    participant Admin as 管理者
    participant Kura as Kura (LLM Gateway)
    participant GW as 認証ゲートウェイ (Tollgate / Proxy)

    Admin->>Kura: 1. 旧シークレットを PREVIOUS に移動、新シークレットを SHARED_SECRET に設定して再起動
    Note over Kura: 新旧いずれのシークレットでも 200 OK
    Admin->>GW: 2. ゲートウェイの送信シークレットを新シークレットへ切り替え
    GW->>Kura: 新シークレットでリクエスト送信 (200 OK)
    Admin->>Kura: 3. Kura から PREVIOUS を削除して再起動
    Note over Kura: 新シークレットのみを受け付ける状態へ完全移行
```

1. 新しいシークレット `SECRET_NEW` を生成する。
2. Kura の環境変数を `GATEWAY_SHARED_SECRET=SECRET_NEW`、`GATEWAY_SHARED_SECRET_PREVIOUS=SECRET_OLD` に更新してデプロイ/再起動する（この期間、Kura は新旧両方のシークレットを受け付ける）。
3. 前段ゲートウェイ側の送信ヘッダー設定を `SECRET_NEW` に切り替える。
4. Kura から `GATEWAY_SHARED_SECRET_PREVIOUS` を削除して再起動し、ローテーションを完了する。

---

## Kura の位置づけ

既存の LLM ゲートウェイ・プロキシ製品と比較した Kura の主な位置づけは以下の通りである。

| 項目 | Kura | LiteLLM | Cloudflare AI Gateway |
| :--- | :--- | :--- | :--- |
| **言語 / ランタイム** | Go（単一バイナリ、純 Go SQLite 内蔵、極低フットプリント） | Python（FastAPI） | 独自ワーカー基盤（Cloudflare Edge） |
| **ホスティング形態** | セルフホスト（Docker / Kubernetes / ECS / 単一バイナリ） | セルフホスト / SaaS | SaaS 専用（エッジ基盤依存） |
| **マルチテナント・予算制御** | ストア抽象化（SQLite / DynamoDB / Valkey / PostgreSQL）によるリアルタイム月次予算ソフトリミット遮断 | 仮想キー・月次/日次クォータ | キャッシュ・レートリミット中心 |
| **前段認証ゲートウェイ連携** | 透過ヘッダー信頼設計（自前プロキシ / Tollgate 等と柔軟に連携） | 独自認証テーブル内蔵 | Cloudflare アカウント認証 |
| **対応プロバイダー** | Microsoft Foundry (Azure OpenAI, Claude), Google Gemini, Amazon Bedrock (Mantle) | 100+ プロバイダー（幅広さ重視） | 主要プロバイダー |

---

## 主な機能

- **OpenAI 互換プロキシ (`/v1/chat/completions`)**:
  - 単一のエンドポイントで各種プロバイダー（Microsoft Foundry, Google Gemini, Amazon Bedrock）を中継。
  - 完全非バッファリングの SSE ストリーミング対応。最終フレームから正確なトークン使用量をリアルタイムインターセプト。
  - 各ベンダーの新機能パラメータ（`thinking`, `reasoning_effort` 等）を未知パラメータとしてそのまま透過。
- **ストア抽象化 & リアルタイムコストガード**:
  - **コスト管理ストア**（ホットパス。残枠確認とアトミック加算）と**集計結果ストア**（永続。実績記録・月次レポート・分散ロック）の役割分離。
  - SQLite（既定・単一ノード向け標準）、DynamoDB、Valkey / Redis、PostgreSQL に対応。
  - 応答完了後にトークン実測値からコストを加算し、月間上限到達後の次回リクエストから自動遮断するソフトリミット方式。
  - Valkey / Redis 運用時の整合性を担保する自律・手動の補正（Reconciliation）エンジン。
- **インメモリキャッシュ層（デコレーター）**:
  - DynamoDB / Valkey / PostgreSQL 向けにホットパスの負荷を削減するキャッシュデコレーターを内包（SQLite では自動バイパス）。
  - 遮断済みテナントのネガティブキャッシュ、設定キャッシュ、残枠キャッシュ、加算バッチ書き込みを提供。
- **JSON 単価表エンジン (`pricing.json`)**:
  - モデル別単価（入力/出力/キャッシュ入力/推論トークン）を外部 JSON ファイルで定義・管理。バージョン追跡と未定義モデルポリシー制御。
- **仮想モデルエイリアス (Routing & Fallback)**:
  - アプリ側コードを変更せずに、`fast` / `default` (`gpt-5.4-mini`)、`smart` (`claude-3-5-sonnet`)、`flash` (`gemini-1.5-flash`) などの共通エイリアスで裏側のモデルを柔軟に切り替え。
- **リアルタイム通信**:
  - `/v1/realtime` の WebSocket 双方向パススルー中継。
- **メタデータ・透過タグ収集**:
  - 事前マスタ定義なしで、`X-Environment`, `X-Feature`, `X-Tags` ヘッダーを自動収集し非同期構造化ログに付与。
- **自律的バックグラウンド処理 & アラート**:
  - 内蔵スケジューラーと分散ロックによる完全自律型の月次締めレポート集計・クォータ残量低下アラート・残高補正ジョブ（外部バッチ不要）。
- **クラウドネイティブ運用**:
  - `/healthz`（総合）、`/livez`（Liveness）、`/readyz`（Readiness）プローブによる Kubernetes / ECS 完全適合。
  - `/metrics` による Prometheus パフォーマンス・コスト・キャッシュメトリクス公開。
- **Scalar API ドキュメント & OpenAPI 3.1 自動生成**:
  - Huma v2 により、最新の対話的 API ドキュメント（Scalar）と OpenAPI 3.1 スキーマを起動時に自動生成。

---

## ストレージ構成

Kura はマルチコンテナでの水平スケールと高スループットを実現するため、ストアの役割を「**コスト管理ストア**」と「**集計結果ストア**」の 2 つに分離している。

```
┌─────────────────────────────────────────────────────────────┐
│                       Kura LLM Gateway                      │
├──────────────────────────────┬──────────────────────────────┤
│ コスト管理ストア (ホットパス)   │ 集計結果ストア (永続・分析)    │
│ ・残枠の高速確認 (ソフトリミット)│ ・利用実績の正のデータ記録    │
│ ・トークン・費用の加算        │ ・月次レポート集計・分散ロック │
└──────────────┬───────────────┴──────────────┬───────────────┘
               ▼                              ▼
      [ SQLite / DynamoDB /          [ SQLite / DynamoDB /
        Cosmos DB / Firestore /        Cosmos DB / Firestore ]
        Valkey / Redis ]                      │
               │                              │
               └──────── 補正 (Reconcile) ────┘
```

### 推奨構成一覧

| 用途 | 推奨構成 | 特徴 |
| :--- | :--- | :--- |
| **試用・開発・単一ノード** | **SQLite（既定）** | 外部 DB 不要。単一バイナリ・1 つのファイルで再起動後もデータが残る。 |
| **マルチコンテナ（AWS）** | **DynamoDB** | 水平スケール、アトミック更新による高スループットと完全な永続性。 |
| **マルチコンテナ（Azure）** | **Azure Cosmos DB** | NoSQL API によるアトミックパッチ更新とスケーラブルな時系列集計。 |
| **マルチコンテナ（GCP）** | **Google Cloud Firestore** | マネージド NoSQL。アトミックインクリメントと分散トランザクション。 |
| **マルチコンテナ・速度優先** | **Valkey / Redis**（コスト管理）+ 補正<br/>**DynamoDB / Cosmos DB / Firestore 等**（集計結果） | サブミリ秒の極低レイテンシーと定期補正による整合性維持。 |

### 対応バックエンド詳細

環境変数 `COST_STORE` および `USAGE_STORE` で各ストアの種別を独立して指定できる（未指定時は両方 `sqlite` が既定）。

| 役割 | 対応バックエンド | 特徴・注意点 |
| :--- | :--- | :--- |
| **コスト管理ストア**<br/>(`COST_STORE`) | **sqlite** | **既定・単一ノード標準**。CGO 不要の純 Go 実装。単一インスタンス専用（マルチコンテナ不可）。 |
| | **dynamodb** | **AWS 推奨**。アトミック更新による高スループットと完全な永続性を提供。 |
| | **cosmosdb** | **Azure 推奨**。Partial Document Update (Patch) によるアトミック加算。 |
| | **firestore** | **GCP 推奨**。`firestore.Increment` によるアトミック加算。 |
| | **valkey** / **redis** | **超高速**。サブミリ秒の極低レイテンシー。下記の**補正機能**とセットで運用する。 |
| **集計結果ストア**<br/>(`USAGE_STORE`) | **sqlite** | **既定・単一ノード標準**。単一ファイルに時系列利用実績・月次レポート・ロックを永続化。 |
| | **dynamodb** | **AWS 推奨**。Single Table Design による時系列利用実績の永続化。 |
| | **cosmosdb** | **Azure 推奨**。コンテナ分割または Single Container による集計とロック。 |
| | **firestore** | **GCP 推奨**。コレクション別ドキュメント永続化と分散ロック。 |

> [!NOTE]
> - **SQLite の単一インスタンス制約**: SQLite はファイルロックを用いるため、複数コンテナからの同時マウント・並行書き込みには対応していない。マルチコンテナでスケールさせる場合は DynamoDB / Cosmos DB / Firestore または Valkey との組み合わせを使用すること。
> - **不正な組み合わせの検知**: 起動時にストアの組み合わせを検証し、不正な構成（例: 集計結果ストアに Valkey / Redis を指定するなど、集計クエリが実行できない構成）は起動時に即座にエラーで終了（Fail-Fast）する。
> - **`memory` 設定の廃止**: `memory` は本番設定の選択肢から外され、テスト専用パッケージへ移行した。環境変数で `memory` を指定すると起動を拒否する。

### 補正 (Reconciliation) 機能

コスト管理ストアに Valkey / Redis を使用する場合、インスタンスのフェイルオーバーや再起動によってメモリ上のカウンタが失われる可能性がある。Kura は集計結果ストアの実績データから当月の利用累計を再集計し、コスト管理ストアの残高を自動再構築・同期する補正機能を備えている。

- **自動実行**: 起動時、および一定間隔（環境変数 `RECONCILE_INTERVAL_SECONDS`、デフォルト 300 秒）でバックグラウンド実行される。
- **排他制御**: 複数コンテナが同時に補正を実行しないよう、集計結果ストアの分散ロック（DynamoDB / Cosmos DB / Firestore / SQLite ロック）を獲得して安全に実行する。
- **手動実行**: 管理 API（`POST /v1/admin/jobs/reconcile`）から即時補正をトリガー可能である。

---

## キャッシュ層 (Cache Layer)

マルチコンテナ運用時（DynamoDB / Valkey 等）、ホットパスでのストア負荷を軽減し極低レイテンシーを実現するため、ストアインターフェースをラップする**キャッシュデコレーター**を内包している。

※ SQLite バックエンド使用時は、単一ノード前提のためキャッシュ層は自動的にバイパスされる。

### キャッシュ対象と仕様

1. **遮断済みテナントのネガティブキャッシュ**
   - ストアが「予算超過」と判定したテナントをローカルメモリに記憶し、以降のリクエストはストアへの問い合わせを行わずに即座に `429 Too Many Requests` で拒絶する。
   - **保持期間 (TTL)**: **予算期間の終了時刻（当月末）と最大 TTL（`CACHE_NEGATIVE_TTL_SECONDS`、既定 300 秒）のうち短い方**。
   - **即時無効化**: 同一インスタンスで Admin API による予算変更・リセットが行われた場合は、該当エントリを即時に無効化する。他コンテナへは TTL 経過後に反映される。
2. **テナント / サービス設定の読み取りキャッシュ**
   - 予算額や許可モデルなどの永続設定をローカルメモリにキャッシュ（`CACHE_CONFIG_TTL_SECONDS`、既定 60 秒）。Admin API 操作時に即時無効化される。
3. **残枠（バランス）の読み取りキャッシュ**（**既定: オフ**）
   - `CACHE_BALANCE_TTL_SECONDS` を指定することで、残枠確認の読み取りをキャッシュ可能。
4. **加算のバッファリング / バッチ書き込み**（**既定: オフ**）
   - `CACHE_BATCH_FLUSH_INTERVAL_SECONDS` を指定することで、コンテナ内で加算をまとめ、一定間隔でストアへ一括反映可能。
   - **グレースフルシャットダウン**: SIGTERM 等の停止シグナル受信時に、バッファ内の未反映分を必ずストアへフラッシュする。

### ソフトリミット超過幅の目安とリスク

Kura は応答完了後に実測トークンからコストを加算する**ソフトリミット方式**を採用している。キャッシュ層を有効化した場合、超過幅の理論上の最大目安は以下の通りとなる：

$$\text{最大超過幅} \approx \text{稼働コンテナ数} \times \text{設定TTL} \times \text{単位時間あたりの消費コスト}$$

> [!WARNING]
> - **バッチ書き込み時の過少集計リスク**: 加算バッチ書き込みを有効にしている状態でプロセスが `kill -9` やノード障害で異常終了した場合、バッファ内の未反映コストが失われて過少集計になりうる。厳密性を重視する場合はバッチ書き込みをオフ（既定値 `0`）のまま運用すること。

---

## 料金表 (Pricing Table)

モデルごとの利用単価はデータベースではなく、外部 **JSON ファイル**（`pricing.json`）で定義・管理する。

### 仕様
- **設定ファイルパス**: 環境変数 `PRICING_FILE`（デフォルト: `pricing.json`）で指定。起動時にスキーマ検証が行われ、不正なファイルは起動を拒絶する。
- **単価項目**: 入力（`input_cost`）、出力（`output_cost`）、キャッシュ入力（`cached_input_cost`）、推論トークン（`reasoning_cost`）を 100 万トークンあたりの USD 単価で定義。
- **フォールバック規則**: キャッシュ単価や推論単価が未定義のモデルは、それぞれ通常の入力単価・出力単価へ自動フォールバックする。
- **バージョン追跡**: 料金表ファイル自体に `version`（適用日等）を持たせ、利用実績の記録時にその時点のバージョンを一緒に永続化する（後日の単価改定後も過去実績を正確に再現可能）。
- **未定義モデルポリシー**: 環境変数 `UNKNOWN_MODEL_POLICY` で未定義モデル呼び出し時の挙動を設定可能。
  - `warn`（デフォルト）: 警告ログを出力し、フォールバック単価（Input: $1.00 / Output: $3.00 per 1M）を適用して集計を継続。
  - `reject`: `400 Bad Request` で即座にリクエストを拒絶。
- **運用方針**: 全コンテナが同一の単価表を参照するよう、コンテナイメージへの焼き込みまたは Kubernetes ConfigMap / 共通ボリュームによるマウントを推奨する。

---

## 仮想モデルエイリアス

Kura ではリクエストモデル名に仮想エイリアスを指定することで、コードを変更することなく最適な物理モデルへルーティングできる。

| エイリアス名 | 物理解決モデル | 経由プロバイダー | 主な用途・特徴 |
| :--- | :--- | :--- | :--- |
| `fast`, `default` | `gpt-5.4-mini` | Microsoft Foundry / Azure OpenAI | 高速応答、低コスト、一般的なチャット・分類 |
| `smart`, `code` | `claude-3-5-sonnet` | Microsoft Foundry (Serverless API) | 高度な推論、コーディング、長文コンテキスト処理 |
| `flash` | `gemini-1.5-flash` | Google Gemini | 超高速処理、マルチモーダル入力、軽量タスク |

※ 物理モデル名（例: `gpt-4o`, `claude-3-5-sonnet`, `gemini-1.5-pro`, `anthropic.claude-3-5-sonnet-20240620-v1:0`）を直接指定した場合は、エイリアス変換を行わず指定モデルへ透過ルーティングされる。

---

## アーキテクチャ

```mermaid
flowchart TD
    Client["クライアント アプリケーション<br/>(SDK / Curl / WebApp)"]
    
    subgraph AuthLayer ["認証ゲートウェイ（任意。例: Tollgate / ALB / 自前プロキシ）"]
        AUTH["API キー / JWT 認証<br/>テナント・サービス識別ヘッダー付与<br/>RPM レートリミット"]
    end

    subgraph Kura ["Kura — LLM Gateway (:8080)"]
        direction TB
        ROUTER["Huma v2 Router / ServeMux"]

        subgraph Endpoints ["公開インターフェース"]
            CHAT["POST /v1/chat/completions"]
            REALTIME["GET /v1/realtime (WebSocket)"]
            USAGE["GET /v1/usage"]
            ADMIN["POST /v1/admin/*"]
            HEALTH["GET /healthz, /livez, /readyz"]
            METRICS["GET /metrics"]
        end

        subgraph CoreLogic ["コア制御 (UseCase)"]
            ALIAS["仮想モデル・ルーティング"]
            QUOTA["クォータ & 予算ガード (429)"]
            PRICING["JSON 単価表エンジン"]
            STREAM["SSE Usage インターセプター"]
        end

        subgraph CacheLayer ["キャッシュ層 (デコレーター)"]
            NEG_CACHE["ネガティブキャッシュ (遮断済み)"]
            CFG_CACHE["設定キャッシュ (TTL 60s)"]
            BATCH_BUF["加算バッチバッファ (Optional)"]
        end

        subgraph StorageLayer ["ストレージ層 (役割分離)"]
            COST_STORE["コスト管理ストア (ホットパス)<br/>SQLite / DynamoDB / Cosmos DB / Firestore / Valkey"]
            USAGE_STORE["集計結果ストア (永続)<br/>SQLite / DynamoDB / Cosmos DB / Firestore"]
            CRON["自律スケジューラー (月次締め・補正)"]
        end
    end

    subgraph Providers ["外部 LLM プロバイダー"]
        AZURE["Microsoft Foundry / Azure OpenAI"]
        GEMINI["Google Gemini (AI Studio)"]
        BEDROCK["Amazon Bedrock (bedrock-mantle)"]
        MOCK["統合モックサーバー (:8090)"]
    end

    Client -.->|"前段なし直接接続 (プライベート環境)"| ROUTER
    Client -->|"Authorization: Bearer <key>"| AUTH
    AUTH -->|"X-Service-ID / X-Tenant-ID 付与"| ROUTER
    ROUTER --> Endpoints
    CHAT --> ALIAS --> QUOTA --> STREAM
    QUOTA <-->|"ネガティブ判定 / キャッシュ"| CacheLayer
    CacheLayer <-->|"残枠確認 / アトミック加算"| COST_STORE
    STREAM -->|"利用実績記録"| USAGE_STORE
    STREAM -->|"リバースプロキシ"| Providers
    CRON <-->|"実績から残高補正"| COST_STORE
    CRON <-->|"集計・分散ロック"| USAGE_STORE
```

---

## ディレクトリ構成

```
kura/
├── cmd/
│   ├── server/                     # Gateway 本体エントリーポイント (main.go)
│   └── mock_server/                # ローカル検証用モック LLM サーバー
├── internal/
│   ├── delivery/
│   │   └── http/                   # HTTP ハンドラ、Huma v2 定義、ミドルウェア
│   ├── domain/
│   │   ├── entity/                 # ドメインエンティティ、価格エンジン (pricing.go)
│   │   ├── repository/             # CostStore / UsageStore インターフェース
│   │   └── service/                # プロバイダーアダプターインターフェース
│   ├── infrastructure/
│   │   ├── adapter/                # プロバイダーアダプター (OpenAI / Azure / Bedrock)
│   │   ├── cache/                  # キャッシュ層デコレーター (CachedCostStore)
│   │   ├── config/                 # 環境変数設定ローダー (config.go)
│   │   ├── dynamodb/               # DynamoDB ストア実装
│   │   ├── logger/                 # 非同期バッファリング構造化ロガー
│   │   ├── metrics/                # Prometheus メトリクスコレクター
│   │   ├── notifier/               # アプリ内通知・ログ出力アダプター
│   │   ├── postgres/               # PostgreSQL ストア実装
│   │   ├── proxy/                  # LLM リバースプロキシ & ストリーミング処理
│   │   ├── scheduler/              # 自律 Cron & 補正スケジューラー
│   │   ├── sqlite/                 # SQLite ストア実装 (純 Go / CGO 不要)
│   │   ├── store/                  # ストアファクトリ & 組み合わせ検証
│   │   ├── valkey/                 # Valkey / Redis コストストア実装
│   │   └── websocket/              # Realtime WebSocket プロキシ
│   └── usecase/                    # ビジネスロジック (Auth, Chat, Admin, Batch)
├── docs/                           # 詳細仕様ドキュメント群
├── deploy/
│   └── monitoring/                 # 監視スタック設定 (Prometheus / Grafana ダッシュボード)
├── http/                           # API 動作検証用 REST Client テストスイート (test.http)
├── pricing.json                    # モデル別単価定義ファイル
├── Dockerfile                      # Gateway マルチステージビルド定義
├── Dockerfile.mock                 # モックサーバービルド定義
├── compose.yaml                    # ローカル開発・検証用 Compose 定義 (既定: SQLite)
├── CONTRIBUTING.md                 # コントリビューション & CI/CD ガイド
├── CHANGELOG.md                    # 変更履歴
└── SECURITY.md                     # セキュリティポリシー
```

---

## API エンドポイント一覧

### 公開 API (サービス向け)

| パス | メソッド | 認証 | 概要 |
| :--- | :---: | :---: | :--- |
| `/v1/chat/completions` | `POST` | 共有シークレット (`X-Gateway-Secret`)<br/>+ ヘッダー信頼 (`X-Service-ID`, `X-Tenant-ID`) | OpenAI 互換チャット補完 (同期 / SSE ストリーミング) |
| `/v1/realtime` | `GET` | 共有シークレット (`X-Gateway-Secret`)<br/>+ ヘッダー信頼 (`X-Service-ID`, `X-Tenant-ID`) | OpenAI Realtime API (WebSocket 双方向パススルー) |
| `/v1/usage` | `GET` | 共有シークレット (`X-Gateway-Secret`)<br/>+ ヘッダー信頼 (`X-Service-ID`, `X-Tenant-ID`) | テナント月次利用量・残予算枠の照会 |

### 管理 API (Admin)

| パス | メソッド | 認証 | 概要 |
| :--- | :---: | :---: | :--- |
| `/v1/admin/usage` | `GET` | Bearer キー (`ADMIN_API_KEY`) | サービス別月次利用実績およびモデル別内訳の照会 |
| `/v1/admin/limits` | `POST` | Bearer キー (`ADMIN_API_KEY`) | テナントクォータ・月次予算上限・課金プランの設定 |
| `/v1/admin/jobs/run` | `POST` | Bearer キー (`ADMIN_API_KEY`) | 定期バッチジョブ（月次締め・アラート）の手動即時実行 |
| `/v1/admin/jobs/reconcile` | `POST` | Bearer キー (`ADMIN_API_KEY`) | Valkey/Redis コストストア残高の集計結果ストアからの手動補正実行 |
| `/v1/admin/notifications` | `GET` | Bearer キー (`ADMIN_API_KEY`) | アプリ内通知一覧（月次レポート・予算警告）の取得 |

### 運用・システム API

| パス | メソッド | 認証 | 概要 |
| :--- | :---: | :---: | :--- |
| `/healthz` | `GET` | 不要 | 総合ヘルスチェック |
| `/livez` | `GET` | 不要 | **Liveness プローブ**: プロセス生存監視 (外部依存なし) |
| `/readyz` | `GET` | 不要 | **Readiness プローブ**: ストレージ疎通・トラフィック受入監視 |
| `/metrics` | `GET` | 不要 | **Prometheus メトリクス**: レイテンシー・トークン数・コスト・キャッシュ照会 |
| `/docs` | `GET` | 不要 | **Scalar UI API ドキュメント** |
| `/openapi` | `GET` | 不要 | **OpenAPI 3.1 JSON スキーマ** |

---

## 環境変数設定

Kura は起動時に以下の環境変数を読み込んで動作する。

| 変数名 | デフォルト値 | 必須 | 説明 |
| :--- | :--- | :---: | :--- |
| `GATEWAY_SHARED_SECRET` | 空文字 | **必須** | 信頼するゲートウェイとの共有シークレット（32 文字以上必須。未設定時は起動拒否） |
| `GATEWAY_SHARED_SECRET_PREVIOUS` | 空文字 | 任意 | ローテーション用の旧シークレット（32 文字以上。新旧両方を並行して受け付け） |
| `GATEWAY_SECRET_HEADER` | `X-Gateway-Secret` | 任意 | 共有シークレットを受信する HTTP ヘッダー名 |
| `INSECURE_NO_GATEWAY_AUTH` | `false` | 任意 | `true` の場合、共有シークレットの検証なしで起動（**開発・検証専用**。起動時に警告出力） |
| `PORT` | `8080` | 任意 | HTTP サーバー待受ポート番号 |
| `AWS_REGION` | `ap-northeast-1` | 任意 | AWS リージョン |
| `COST_STORE` | `sqlite` | 任意 | コスト管理ストア種別 (`sqlite` / `dynamodb` / `cosmosdb` / `firestore` / `valkey` / `redis`) |
| `USAGE_STORE` | `sqlite` | 任意 | 集計結果ストア種別 (`sqlite` / `dynamodb` / `cosmosdb` / `firestore`) |
| `SQLITE_PATH` | `./data/kura.db` | 任意 | SQLite データベースファイルパス |
| `CACHE_ENABLED` | `true` | 任意 | キャッシュ層の有効化フラグ (SQLite では自動バイパス) |
| `CACHE_NEGATIVE_TTL_SECONDS` | `300` | 任意 | 遮断済みテナントのネガティブキャッシュ最大 TTL (秒) |
| `CACHE_CONFIG_TTL_SECONDS` | `60` | 任意 | テナント/サービス永続設定の読み取りキャッシュ TTL (秒) |
| `CACHE_BALANCE_TTL_SECONDS` | `0` | 任意 | 残枠（バランス）読み取りキャッシュ TTL (秒、`0` で無効) |
| `CACHE_BATCH_FLUSH_INTERVAL_SECONDS` | `0` | 任意 | 加算バッチ書き込みフラッシュ間隔 (秒、`0` で即時反映) |
| `DYNAMODB_ENDPOINT` | 空文字 | 任意 | DynamoDB エンドポイント URL (ローカル開発時: `http://dynamodb:8000`) |
| `DYNAMODB_TABLE_NAME` | `KuraUsage` | 任意 | 利用実績・クォータ管理用 DynamoDB テーブル名 |
| `COSMOSDB_ENDPOINT` | 空文字 | 任意 | Azure Cosmos DB エンドポイント URL |
| `COSMOSDB_KEY` | 空文字 | 任意 | Azure Cosmos DB プライマリキー |
| `COSMOSDB_CONNECTION_STRING` | 空文字 | 任意 | Azure Cosmos DB 接続文字列 |
| `COSMOSDB_DATABASE` | `kura` | 任意 | Azure Cosmos DB データベース名 |
| `COSMOSDB_CONTAINER` | `usage` | 任意 | Azure Cosmos DB コンテナ名 |
| `FIRESTORE_PROJECT_ID` | 空文字 | 任意 | Google Cloud Firestore GCP プロジェクト ID |
| `FIRESTORE_DATABASE` | `(default)` | 任意 | Google Cloud Firestore データベース ID |
| `VALKEY_URL` | 空文字 | 任意 | Valkey / Redis 接続 URL (`redis://localhost:6379/0`) |
| `PRICING_FILE` | `pricing.json` | 任意 | モデル別単価表 JSON ファイルパス |
| `UNKNOWN_MODEL_POLICY` | `warn` | 任意 | 単価表未定義モデルの扱い (`warn`: デフォルト単価でフォールバック / `reject`: 400 で遮断) |
| `RECONCILE_INTERVAL_SECONDS` | `300` | 任意 | Valkey/Redis 利用時の残高補正ジョブ実行間隔 (秒) |
| `MICROSOFT_FOUNDRY_ENDPOINT` | 空文字 | 任意 | Microsoft Foundry / Azure OpenAI ベース URL (`AZURE_OPENAI_ENDPOINT` でも指定可) |
| `MICROSOFT_FOUNDRY_API_KEY` | 空文字 | 任意 | Microsoft Foundry / Azure OpenAI API キー (`AZURE_OPENAI_API_KEY` でも指定可) |
| `MICROSOFT_FOUNDRY_API_VERSION` | `2024-02-15-preview` | 任意 | Microsoft Foundry / Azure OpenAI API バージョン |
| `MICROSOFT_FOUNDRY_DEFAULT_DEPLOYMENT` | 空文字 | 任意 | デフォルトデプロイメントモデル名 |
| `MICROSOFT_FOUNDRY_ENDPOINT_JAPAN` | 空文字 | 任意 | 日本国内限定ルーティング用エンドポイント (`X-Data-Residency: japan` 指定時に使用) |
| `BEDROCK_API_KEY` | 空文字 | 任意 | Amazon Bedrock API キー |
| `BEDROCK_REGION` | `us-east-1` | 任意 | Amazon Bedrock リージョン |
| `BEDROCK_ENDPOINT` | 空文字 | 任意 | Amazon Bedrock Mantle エンドポイント (未指定時は `https://bedrock-mantle.{region}.api.aws/v1`) |
| `GEMINI_API_KEY` | 空文字 | 任意 | Google Gemini API キー |
| `GEMINI_BASE_URL` | `https://generativelanguage.googleapis.com` | 任意 | Google Gemini API ベース URL |
| `DEFAULT_TOKEN_QUOTA` | `1000000` | 任意 | 新規テナント初期月間トークン上限 (デフォルト100万トークン) |
| `DEFAULT_BILLING_TYPE` | `payg` | 任意 | デフォルト課金タイプ (`payg`: 従量課金 / `capped`: 上限到達時遮断) |
| `LOG_CHANNEL_BUFFER_SIZE` | `10000` | 任意 | 非同期ロガーのメモリバッファキューサイズ |
| `ADMIN_API_KEY` | 空文字 | 任意 | 管理者マスター API キー (未設定時は管理 API を 401 で遮断) |
| `ENABLE_INTERNAL_CRON` | `true` | 任意 | 内蔵スケジューラー (月次締め・毎時アラート・定期補正) の有効化フラグ |
| `ENFORCE_TOLLGATE_AUTH` | `false` | 任意 | `true` の場合、API キー識別ヘッダー (`X-Key-ID`) 欠落リクエストを 401 で拒絶 |
| `DOCS_PATH` | `/docs` | 任意 | Scalar UI ドキュメントの公開パス (空文字 / `none` / `off` で無効化) |
| `OPENAPI_PATH` | `/openapi` | 任意 | OpenAPI 3.1 スキーマの公開パス (空文字 / `none` / `off` で無効化) |

---

## クイックスタート

### 前提条件
- [Go](https://go.dev/) 1.24 以上
- [Docker](https://www.docker.com/) または [nerdctl](https://github.com/containerd/nerdctl) / Docker Compose

### 1. リポジトリのクローン & 設定準備
```bash
git clone https://github.com/northfieldzz/kura.git
cd kura
cp .env.example .env  # 必要に応じてシークレットや環境変数を編集
```

### 2. Docker Compose での最短起動 (SQLite 既定)
`compose.yaml` はローカル開発用に `INSECURE_NO_GATEWAY_AUTH=true` が指定されているため、シークレット生成なしで即座に起動する（データ用ボリュームをマウントした SQLite 構成）。

```bash
docker compose up -d --build
```

マルチコンテナ構成（DynamoDB や PostgreSQL、Valkey）やモニタリングスタック（Prometheus & Grafana）を起動する場合はプロファイルを指定する：
```bash
# DynamoDB Local 構成で起動
docker compose --profile dynamodb up -d --build

# Valkey + PostgreSQL 構成で起動
docker compose --profile valkey --profile postgres up -d --build

# モニタリングスタック (Prometheus & Grafana) を併せて起動
docker compose --profile monitor up -d --build
```

### 3. Docker 単体での最短起動

**本番 / 通常モード (共有シークレットを設定して起動)**:
```bash
docker run -d -p 8080:8080 \
  -e GATEWAY_SHARED_SECRET="a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef0123456789abcdef0" \
  -v kura-data:/data ghcr.io/northfieldzz/kura:latest
```

**開発・検証モード (シークレット検証をバイパス)**:
```bash
docker run -d -p 8080:8080 \
  -e INSECURE_NO_GATEWAY_AUTH=true \
  -v kura-data:/data ghcr.io/northfieldzz/kura:latest
```

### 4. Go 単体での起動
```bash
# 開発時 (バイパスフラグで起動)
INSECURE_NO_GATEWAY_AUTH=true go run ./cmd/server

# または共有シークレットを指定して起動
GATEWAY_SHARED_SECRET="a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef0123456789abcdef0" go run ./cmd/server
```
※ 既定で `./data/kura.db` に SQLite データベースが自動生成され、再起動後もデータが保持される。

起動後、以下のエンドポイントへアクセス可能である：
- **Kura Gateway**: `http://localhost:8080`
- **Scalar API ドキュメント**: `http://localhost:8080/docs`
- **OpenAPI 3.1 仕様書**: `http://localhost:8080/openapi`
- **統合 LLM モックサーバー**: `http://localhost:8090`
- **Prometheus UI**（`--profile monitor` 起動時）: `http://localhost:9090`
- **Grafana ダッシュボード**（`--profile monitor` 起動時）: `http://localhost:3000`（匿名管理者ログイン対応）

---

## API 利用例

### 1. 共有シークレット付き呼び出し例 (本番・ゲートウェイ経由)

ヘッダーに `X-Gateway-Secret`、`X-Service-ID`、`X-Tenant-ID` を付与してリクエストを送信する。

```bash
# チャット補完 (OpenAI 互換・仮想モデル fast 指定・SSE ストリーミング)
curl -X POST http://localhost:8080/v1/chat/completions \
  -H "X-Gateway-Secret: a1b2c3d4e5f60718293a4b5c6d7e8f90123456789abcdef0123456789abcdef0" \
  -H "X-Service-ID: payment-service" \
  -H "X-Tenant-ID: tenant-corp-a" \
  -H "X-Environment: staging" \
  -H "X-Feature: summarize" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "fast",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant."},
      {"role": "user", "content": "こんにちは！"}
    ],
    "stream": true
  }'
```
※ `INSECURE_NO_GATEWAY_AUTH=true` で起動している場合は `X-Gateway-Secret` なしでもリクエスト可能。

### 2. 前段ゲートウェイ併用例 (Tollgate / Proxy による構成例)

エンドユーザーの API キー検証や RPM レートリミットは前段ゲートウェイ側で実施する。前段ゲートウェイが認証成功後に `X-Gateway-Secret` と `X-Tenant-ID` を注入して Kura へリバースプロキシする。

```bash
# クライアントからゲートウェイへのリクエスト
curl -X POST http://localhost:8000/llm/v1/chat/completions \
  -H "Authorization: Bearer tlge_live_9a8b7c6d..." \
  -H "Content-Type: application/json" \
  -d '{
    "model": "smart",
    "messages": [
      {"role": "user", "content": "Hello via API Gateway and Kura!"}
    ]
  }'
```
> [!NOTE]
> ゲートウェイ製品側で Kura へのプロキシ転送時に固定ヘッダー（`X-Gateway-Secret`）を付与し、クライアントから持ち込まれた同名ヘッダーを除去・上書きする設定を行うこと（Tollgate 等の連携設定については後述の調査結果または各製品ドキュメントを参照）。

### 3. テナント別クォータ・課金プラン設定 (Admin API)

```bash
curl -X POST http://localhost:8080/v1/admin/limits \
  -H "Authorization: Bearer ${ADMIN_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "service_id": "payment-service",
    "tenant_id": "tenant-corp-a",
    "cost_limit": 100.0,
    "billing_type": "capped"
  }'
```

---

## 制限事項

- **SQLite は単一インスタンス専用である**: SQLite はファイルベースのデータベースであるため、複数コンテナからの同時書き込み・水平スケールには対応していない。マルチコンテナでスケールさせる場合は DynamoDB または Valkey + PostgreSQL / DynamoDB を使用すること。
- **コスト上限はソフトリミット方式である**: Kura ではストリーミング応答を含め、応答完了時に実測されたトークン数からコストを加算する。そのため、上限到達間際の同時並行リクエストでは上限をわずかに超過して処理される場合がある。厳密な事前引き当て（Hard Limit）は未実装である。
- **キャッシュ有効時の超過幅**: キャッシュ層（特に残枠キャッシュ）を有効化した場合、各コンテナがローカルキャッシュを参照するため、ソフトリミットの超過幅が「コンテナ数 × TTL × 単位時間あたりコスト」の範囲で広がる可能性がある。
- **RPM（分間リクエスト制限）の非内包**: プロセス内メモリ依存を排除してマルチコンテナでの水平スケールを担保するため、RPM 制限は Kura から排除されている。レートリミットが必要な場合は前段の認証ゲートウェイ（Tollgate 等）で実施すること。
- **Valkey / Redis 利用時のカウンタ消失リスク**: コスト管理ストアに Valkey / Redis を使用する場合、インスタンス障害等でメモリ上の累計値が消失するリスクがある。AOF 等の永続化設定を行うとともに、定期的な補正（Reconciliation）機能を併用すること。
- **Amazon Bedrock の対応モデル**: Amazon Bedrock は `bedrock-mantle` の OpenAI 互換エンドポイントで中継される。Mantle で提供されていないモデル（Converse API / InvokeModel のみで提供されるモデル）は対象外となる。また Claude などのモデルも Mantle の Chat Completions 互換形式で呼び出す。
- **エンドユーザー認証の非内包**: Kura 自身はエンドユーザーの API キー認証を行わない。ゲートウェイ共有シークレット（`X-Gateway-Secret`）によるゲートウェイ・Kura 間の信頼確認を行い、ユーザー認証は前段ゲートウェイ（Tollgate 等）の責務となる。

---

## ロードマップ (今後の検討事項)

以下の機能は現行バージョンでは未実装であり、将来的な拡張候補として検討されている。

- **相互 TLS (mTLS) 認証**: ゲートウェイ・Kura 間の信頼確認を共有シークレットヘッダーだけでなく、クライアント証明書による mTLS で行う機能。
- **クラウド・ワークロード ID 認証**: AWS IAM ロール / Azure マネージド ID / GCP サービスアカウント等のクラウドネイティブなサービス間トークン検証による認証。
- **事前コスト予約 (Pre-allocation)**: リクエスト開始時に最大想定コストを事前予約し、完了時に確定差分を精算することで同時並行リクエストによる上限超過を抑制する機能。
- **fail-closed モード**: リクエストごとに集計結果ストア（正のデータ）に対して残額の完全同期検証を行い、超過時の通過を完全に防ぐ高厳密性モード。
- **Bedrock の Claude 系ネイティブ（Anthropic Messages API）対応**: Mantle の OpenAI 互換形式を介さず、Anthropic Messages API 形式への直接相互変換レイヤーの提供。

---

## テスト実行

```bash
# 全ユニットテストの実行 (キャッシュ無効・外部依存なし)
go test -v -count=1 ./...

# CGO_ENABLED=0 でのビルド検証 (静的バイナリ / distroless 対応)
CGO_ENABLED=0 go build -o ./bin/server ./cmd/server
```

---

## 詳細仕様書 & ガイド

詳細なアーキテクチャ設計、仕様書、運用ガイドは `docs/` 配下の各ドキュメントを参照のこと。

- [システムアーキテクチャ詳細](docs/architecture.md) — 全体構造、データフロー、コンポーネント設計
- [API リファレンス](docs/api-reference.md) — 全エンドポイントの詳細仕様とスキーマ
- [認証 & テナント解決仕様](docs/auth-and-virtual-keys.md) — ヘッダー解決、管理者認証、透過タグ
- [課金プラン & クォータ制御仕様](docs/billing-and-quota.md) — トークンメータリング、コスト算出、上限遮断ルール
- [DynamoDB スキーマ設計](docs/dynamodb-schema.md) — Single Table Design、PK/SK 構造、GSI 設計
- [スケジューラー & オブザーバビリティ](docs/scheduler-and-observability.md) — Cron 定期バッチ、Prometheus メトリクス、構造化ログ
- [基本要件定義書](docs/SPECIFICATION.md) — プロジェクト要件および機能一覧
- [コントリビューションガイド](CONTRIBUTING.md) — 開発フロー、コーディング規約、CI/CD 構成
- [変更履歴](CHANGELOG.md) — バージョン別変更履歴
- [セキュリティポリシー](SECURITY.md) — 脆弱性報告手順およびセキュリティ設計原則

---

## 関連プロジェクト

- [**Tollgate**](https://github.com/northfieldzz/tollgate) — マルチテナント向け高パフォーマンス API キー管理・レート制限プロキシゲートウェイ（Kura の前段に配置して併用可能。併用は任意）。
- [**Portico**](https://github.com/northfieldzz/portico) — エンタープライズ向け MCP (Model Context Protocol) ゲートウェイ・オーケストレーター（併用は任意）。

---

## ライセンス

本プロジェクトは [Mozilla Public License 2.0 (MPL-2.0)](LICENSE) のもとで公開されている。
