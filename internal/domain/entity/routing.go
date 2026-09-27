package entity

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// EndpointConfig は個別 LLM エンドポイントの設定
type EndpointConfig struct {
	Name       string `json:"name,omitempty"`
	Provider   string `json:"provider"` // "azure" | "bedrock" | "gemini" | "openai"
	URL        string `json:"url,omitempty"`
	Key        string `json:"key,omitempty"` // 直接値または "env:ENV_NAME"
	Region     string `json:"region,omitempty"`
	APIVersion string `json:"api_version,omitempty"`
	Deployment string `json:"deployment,omitempty"`
	Priority   int    `json:"priority,omitempty"` // 1 が最優先
	Weight     int    `json:"weight,omitempty"`   // 同一優先度時の重み
}

// GetResolvedKey は "env:NAME" 形式の環境変数を展開した API キーを返す
func (e *EndpointConfig) GetResolvedKey() string {
	if strings.HasPrefix(e.Key, "env:") {
		envName := strings.TrimPrefix(e.Key, "env:")
		return os.Getenv(envName)
	}
	return e.Key
}

// RoutingConfig は階層型ルーティング設定
type RoutingConfig struct {
	Version   string                      `json:"version,omitempty"`
	Default   []EndpointConfig            `json:"default,omitempty"`
	Prefixes  map[string][]EndpointConfig `json:"prefixes,omitempty"`
	Overrides map[string][]EndpointConfig `json:"overrides,omitempty"`
}

// CircuitBreaker はエンドポイントの 429 冷却状態（サーキットブレーカー）を管理する
type CircuitBreaker struct {
	mu            sync.RWMutex
	cooldownUntil map[string]time.Time
}

// NewCircuitBreaker は CircuitBreaker インスタンスを生成する
func NewCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		cooldownUntil: make(map[string]time.Time),
	}
}

// MarkCooldown は指定エンドポイントを冷却期間としてマークする
func (cb *CircuitBreaker) MarkCooldown(endpointID string, duration time.Duration) {
	if cb == nil {
		return
	}
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.cooldownUntil[endpointID] = time.Now().Add(duration)
}

// IsCoolingDown は指定エンドポイントが冷却中かを判定する
func (cb *CircuitBreaker) IsCoolingDown(endpointID string) bool {
	if cb == nil {
		return false
	}
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	until, ok := cb.cooldownUntil[endpointID]
	if !ok {
		return false
	}
	return time.Now().Before(until)
}

// Router はモデル名とリクエスト情報から候補エンドポイントを解決・ソートする
type Router struct {
	config *RoutingConfig
	cb     *CircuitBreaker
}

// NewRouter は Router インスタンスを生成する
func NewRouter(cfg *RoutingConfig, cb *CircuitBreaker) *Router {
	if cfg == nil {
		cfg = &RoutingConfig{}
	}
	if cb == nil {
		cb = NewCircuitBreaker()
	}
	return &Router{
		config: cfg,
		cb:     cb,
	}
}

// LoadRoutingConfig は JSON ファイルからルーティング設定を読み込む
func LoadRoutingConfig(filePath string) (*RoutingConfig, error) {
	if filePath == "" {
		return &RoutingConfig{}, nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read routing config file: %w", err)
	}
	var cfg RoutingConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse routing config JSON: %w", err)
	}
	return &cfg, nil
}

// GetCircuitBreaker は CircuitBreaker を返す
func (r *Router) GetCircuitBreaker() *CircuitBreaker {
	return r.cb
}

// GetConfig は現在のルーティング設定を返す
func (r *Router) GetConfig() *RoutingConfig {
	return r.config
}

// ResolveCandidates はモデル名から候補エンドポイントのリストを優先度順・冷却状態順に解決する
func (r *Router) ResolveCandidates(model string) []EndpointConfig {
	normalizedModel := strings.TrimSpace(strings.ToLower(model))

	// 1. 明示プレフィックス判定 (azure/..., bedrock/..., gemini/...)
	if strings.HasPrefix(normalizedModel, "azure/") {
		cleanModel := strings.TrimPrefix(normalizedModel, "azure/")
		return r.filterAndSort(r.resolveByHierarchy(cleanModel), "azure")
	}
	if strings.HasPrefix(normalizedModel, "bedrock/") {
		cleanModel := strings.TrimPrefix(normalizedModel, "bedrock/")
		return r.filterAndSort(r.resolveByHierarchy(cleanModel), "bedrock")
	}
	if strings.HasPrefix(normalizedModel, "gemini/") {
		cleanModel := strings.TrimPrefix(normalizedModel, "gemini/")
		return r.filterAndSort(r.resolveByHierarchy(cleanModel), "gemini")
	}

	// 2. 階層解決 (Overrides -> Prefixes -> Default)
	candidates := r.resolveByHierarchy(normalizedModel)
	return r.sortCandidates(candidates)
}

func (r *Router) resolveByHierarchy(model string) []EndpointConfig {
	// 2-1. Overrides 完全一致
	if len(r.config.Overrides) > 0 {
		if endpoints, ok := r.config.Overrides[model]; ok && len(endpoints) > 0 {
			return endpoints
		}
	}

	// 2-2. Prefixes 前方一致（最長プレフィックス優先）
	if len(r.config.Prefixes) > 0 {
		var bestMatchPrefix string
		for prefix := range r.config.Prefixes {
			lowerPrefix := strings.ToLower(prefix)
			if strings.HasPrefix(model, lowerPrefix) {
				if len(lowerPrefix) > len(bestMatchPrefix) {
					bestMatchPrefix = prefix
				}
			}
		}
		if bestMatchPrefix != "" {
			return r.config.Prefixes[bestMatchPrefix]
		}
	}

	// 2-3. Default 共通プール
	return r.config.Default
}

func (r *Router) filterAndSort(endpoints []EndpointConfig, provider string) []EndpointConfig {
	var filtered []EndpointConfig
	for _, ep := range endpoints {
		if strings.EqualFold(ep.Provider, provider) {
			filtered = append(filtered, ep)
		}
	}
	if len(filtered) == 0 {
		// 指定プロバイダのエンドポイントが階層内で見つからない場合は Default から同一プロバイダを抽出
		for _, ep := range r.config.Default {
			if strings.EqualFold(ep.Provider, provider) {
				filtered = append(filtered, ep)
			}
		}
	}
	return r.sortCandidates(filtered)
}

func (r *Router) sortCandidates(endpoints []EndpointConfig) []EndpointConfig {
	if len(endpoints) == 0 {
		return endpoints
	}

	// コピーを作成してソート
	res := make([]EndpointConfig, len(endpoints))
	copy(res, endpoints)

	// ソート順:
	// 1. 冷却中（Cooldown）でないものを優先
	// 2. Priority 昇順 (1 > 2 > 3)（未指定 0 は優先度 999 扱い）
	// 3. 同一 Priority の場合は定義順を維持
	sort.SliceStable(res, func(i, j int) bool {
		idI := endpointIdentifier(&res[i])
		idJ := endpointIdentifier(&res[j])
		coolingI := r.cb.IsCoolingDown(idI)
		coolingJ := r.cb.IsCoolingDown(idJ)

		if coolingI != coolingJ {
			return !coolingI // 冷却中でない方を前に
		}

		pI := res[i].Priority
		if pI == 0 {
			pI = 999
		}
		pJ := res[j].Priority
		if pJ == 0 {
			pJ = 999
		}
		return pI < pJ
	})

	return res
}

func endpointIdentifier(ep *EndpointConfig) string {
	if ep.Name != "" {
		return ep.Name
	}
	if ep.URL != "" {
		return ep.URL
	}
	return fmt.Sprintf("%s:%s", ep.Provider, ep.Region)
}

// EndpointIdentifier はエンドポイントを一意に識別する文字列を返す
func EndpointIdentifier(ep *EndpointConfig) string {
	return endpointIdentifier(ep)
}
