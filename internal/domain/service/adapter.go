package service

import (
	"context"
	"net/http"

	"github.com/northfieldzz/kura/internal/domain/entity"
)

// ProviderType は LLM ベンダー種別
type ProviderType string

const (
	ProviderOpenAI    ProviderType = "openai"
	ProviderAzure     ProviderType = "azure"
	ProviderGemini    ProviderType = "gemini"
	ProviderBedrock   ProviderType = "bedrock"
	ProviderRealtime  ProviderType = "realtime"
)

// Adapter は各ベンダーのプロキシ・リクエスト/レスポンス変換を抽象化するインターフェース
type Adapter interface {
	// Provider はアダプターのプロバイダ種別を返す
	Provider() ProviderType

	// IsEnabled は必要な環境変数（APIキーやエンドポイント等）が設定されているかを返す
	IsEnabled() bool

	// PrepareRequest は OpenAI 互換の受信リクエストをベンダー固有の http.Request に変換する
	// ターゲットURL、HTTPメソッド、ベンダー固有ヘッダー、ボディの変換を行う
	PrepareRequest(ctx context.Context, origReq *entity.ChatCompletionRequest, httpReq *http.Request) (*http.Request, error)

	// PrepareRequestWithEndpoint は指定されたエンドポイント設定を用いてリクエストを変換する
	PrepareRequestWithEndpoint(ctx context.Context, origReq *entity.ChatCompletionRequest, httpReq *http.Request, ep *entity.EndpointConfig) (*http.Request, error)

	// ExtractUsageFromResponse は非ストリーミングのレスポンスボディからトークン利用量を抽出する
	ExtractUsageFromResponse(body []byte) (*entity.UsageInfo, error)

	// ExtractUsageFromChunk は SSE の1行（チャンク）からトークン利用量を抽出する
	ExtractUsageFromChunk(chunk []byte) (*entity.UsageInfo, error)

	// NormalizeResponse はベンダー固有レスポンス（Claude/Gemini）を OpenAI 互換 JSON に正規化する（必要な場合）
	NormalizeResponse(statusCode int, body []byte) ([]byte, error)

	// NormalizeSSEChunk はベンダー固有 SSE チャンク（Claude/Gemini）を OpenAI 互換の SSE チャンクに変換する（必要な場合）
	NormalizeSSEChunk(chunk []byte) ([][]byte, error)
}
