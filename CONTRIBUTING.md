# Contributing to Kura

Kura への関心とコントリビューションを歓迎します！  
このガイドラインでは、開発環境のセットアップ、コーディング規約、プルリクエスト（PR）の提出手順について説明します。

---

## 目次

1. [行動指針](#1-行動指針)
2. [コントリビューションの種類](#2-コントリビューションの種類)
3. [開発環境のセットアップ](#3-開発環境のセットアップ)
4. [開発ワークフロー](#4-開発ワークフロー)
5. [アーキテクチャ & コーディング規約](#5-アーキテクチャ--コーディング規約)
6. [セキュリティ & API 設計方針](#6-セキュリティ--api-設計方針)
7. [テスト & 検証](#7-テスト--検証)
8. [プルリクエストの提出チェックリスト](#8-プルリクエストの提出チェックリスト)
9. [CI / CD (GitHub Actions)](#9-ci--cd-github-actions)

---

## 1. 行動指針

Kura コミュニティは、すべての参加者にとってオープンで包括的、敬意を持った環境を目指しています。  
議論やコードレビューでは、建設的かつ尊重し合うコミュニケーションをお願いします。

---

## 2. コントリビューションの種類

- **バグ報告**: 不具合を発見した場合は、再現手順、環境情報（Goバージョン、OS、コンテナランタイム）、期待される動作と実際の動作を記載して GitHub Issue を作成してください。
- **機能提案**: 新機能や改善提案は、Issue または Discussion にて背景やユースケースを共有してください。
- **コードの改善**: バグ修正、パフォーマンス改善、ドキュメントの拡充、テストケースの追加など、プルリクエスト（PR）を歓迎します。

---

## 3. 開発環境のセットアップ

### 前提ツール
- **Go**: 1.25 以上
- **Docker** または **nerdctl** (Docker Compose 対応)
- **Git**

### リポジトリの準備
```bash
# フォークしたリポジトリをクローン
git clone https://github.com/<your-username>/kura.git
cd kura

# アップストリームの設定
git remote add upstream https://github.com/kanmon-hq/kura.git
```

### ローカルスタックの起動
ローカル環境では、Amazon DynamoDB Local および統合モック LLM サーバーを Compose で起動できます。

```bash
# DynamoDB Local + Mock LLM Server を起動
docker compose --profile database up -d
# または nerdctl
nerdctl compose --profile database up -d

# サーバー本体をローカルで起動
go run ./cmd/server
```

---

## 4. 開発ワークフロー

### ブランチ運用
- `main` ブランチから機能ごとにトピックブランチを作成してください。
```bash
git checkout -b feature/awesome-feature
# または
git checkout -b fix/issue-description
```

### コミットメッセージ規約
[Conventional Commits](https://www.conventionalcommits.org/) に準拠した明確なメッセージを推奨します。

- `feat:` 新機能の追加
- `fix:` バグ修正
- `docs:` ドキュメントの変更
- `test:` テストの追加・修正
- `refactor:` 仕様変更を伴わないコードリファクタリング
- `perf:` パフォーマンス改善
- `chore:` ビルド・ツール関連の変更

---

## 5. アーキテクチャ & コーディング規約

Kura は **Standard Go Project Layout** と **クリーンアーキテクチャ（オニオンアーキテクチャ）** を採用しています。

### レイヤー間の依存関係ルール
依存の方向は常に **外側から内側** でなければなりません。

```
delivery (HTTP / Huma v2)  ─┐
                             ├──> usecase ──> domain (Entities, Interfaces)
infrastructure (DB / Proxy) ─┘
```

- **`internal/domain`**:
  - 純粋なビジネスエンティティとインターフェースのみを配置。
  - 外部ライブラリ（HTTP, AWS SDK, DB等）への依存は禁止。
- **`internal/usecase`**:
  - ビジネスロジック・クォータ判定・ルーティングオーケストレーション。
  - インフラの具象型ではなく、Domain で定義されたインターフェースに依存。
- **`internal/delivery/http`**:
  - Huma v2 OpenAPI ルーティング、ハンドラ、ミドルウェア。
- **`internal/infrastructure`**:
  - DynamoDB、リバースプロキシ、アダプター等の具象実装。

### Go コーディングスタイル
- `go fmt` および `go vet` を必ず通過させること。
- パッケージレベルの可視性を適切に管理し、不要なグローバル変数・エクスポートを避けること。
- エラーは握りつぶさず、文脈を付与して適切にラップ（`fmt.Errorf("...: %w", err)`）すること。

---

## 6. セキュリティ & API 設計方針

本プロジェクトでは以下のセキュリティ・設計原則を厳格に適用します：

1. **シークレットのハードコード禁止 (Fail-Fast)**:
   - API キー、認証トークン、マスター管理者シークレット等の認証情報において、コード内にフォールバック用のデフォルト値をハードコードしてはなりません。
   - 環境変数未設定時は、安全側に倒して明示的に認証無効または起動時エラー（Fail-Fast）とすること。
2. **認証コンテキストのコンフリクト防止**:
   - プロキシ・ゲートウェイにおける認証コンテキストヘッダー（`X-Tenant-ID`, `X-Service-ID` 等）において、クライアント指定ヘッダーと検証属性値の不一致（コンフリクト）を暗黙的に上書きして処理を継続させてはなりません。
   - コンフリクト時は `403 Forbidden`（または `400 Bad Request`）で即座に拒絶（Fail-Fast）すること。
3. **データレジデンシー遵守**:
   - `X-Data-Residency: japan` が指定された場合は、日本国内リージョンのエンドポイントのみにルーティングし、国外へ転送してはなりません。

---

## 7. テスト & 検証

変更を加えた際は、すべてのテストがパスすることを確認してください。

```bash
# 全ユニットテストの実行 (キャッシュ無効)
go test -v -count=1 ./...

# レースコンディション（競合状態）の検出
go test -race ./...

# ビルド検証
go build -o ./bin/server ./cmd/server
```

---

## 8. プルリクエストの提出チェックリスト

PR を作成する前に、以下を確認してください：

- [ ] `main` ブランチの最新コミットをリベースしているか
- [ ] すべてのテスト（`go test ./...`）が正常にパスしているか
- [ ] `go fmt` および `go vet` でコードスタイルが整っているか
- [ ] 新機能・修正に対して適切なユニットテストを追加したか
- [ ] 必要に応じて `README.md` や `docs/` 配下の仕様ドキュメントを更新したか
- [ ] 機密情報（API キー、個人情報等）がコミットに含まれていないか

---

## 9. CI / CD (GitHub Actions)

Kura では GitHub Actions により自動テストおよびコンテナイメージの自動配信を行っています。

- **自動テスト (`.github/workflows/test.yml`)**:
  - トリガー: `main` ブランチへの Push および Pull Request 作成時。
  - 内容: `go mod verify`, `go vet`, `go test -v -race` によるコード品質検証とテストカバレッジ計測。
- **イメージ自動配信 (`.github/workflows/release.yml`)**:
  - トリガー: セマンティックバージョニングタグ（例: `v1.0.0`）の Push、または手動実行 (`workflow_dispatch`)。
  - パブリッシュ先: **GitHub Container Registry (`ghcr.io`)** (`ghcr.io/<owner>/kura:<tag>`)。
  - マルチアーキテクチャ対応: `linux/amd64`, `linux/arm64`。
  - 認証: リポジトリ標準の `GITHUB_TOKEN`（`packages: write` 権限）を使用。

---

## ライセンス

Kura へのすべてのコントリビューションは、リポジトリの [Mozilla Public License 2.0 (MPL-2.0)](LICENSE) のもとで提供されるものとします。
