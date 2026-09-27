package entity

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRouter_ResolveCandidates(t *testing.T) {
	cfg := &RoutingConfig{
		Default: []EndpointConfig{
			{Name: "def-azure-1", Provider: "azure", URL: "https://res1.openai.azure.com", Priority: 2},
			{Name: "def-azure-2", Provider: "azure", URL: "https://res2.openai.azure.com", Priority: 1},
		},
		Prefixes: map[string][]EndpointConfig{
			"claude": {
				{Name: "claude-bedrock", Provider: "bedrock", Region: "us-east-1", Priority: 1},
				{Name: "claude-azure", Provider: "azure", URL: "https://foundry-claude.services.ai.azure.com", Priority: 2},
			},
			"gemini": {
				{Name: "gemini-main", Provider: "gemini", Key: "env:TEST_GEMINI_KEY", Priority: 1},
			},
		},
		Overrides: map[string][]EndpointConfig{
			"gpt-4o": {
				{Name: "gpt4o-ptu", Provider: "azure", URL: "https://ptu.openai.azure.com", Priority: 1},
				{Name: "gpt4o-fallback", Provider: "azure", URL: "https://res1.openai.azure.com", Priority: 2},
			},
		},
	}

	cb := NewCircuitBreaker()
	router := NewRouter(cfg, cb)

	// 1. Overrides 完全一致 (gpt-4o)
	cands := router.ResolveCandidates("gpt-4o")
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates for gpt-4o, got %d", len(cands))
	}
	if cands[0].Name != "gpt4o-ptu" || cands[1].Name != "gpt4o-fallback" {
		t.Errorf("unexpected order for gpt-4o: %v, %v", cands[0].Name, cands[1].Name)
	}

	// 2. Prefixes 前方一致 (claude-3-5-sonnet)
	cands = router.ResolveCandidates("claude-3-5-sonnet")
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates for claude-3-5-sonnet, got %d", len(cands))
	}
	if cands[0].Name != "claude-bedrock" || cands[1].Name != "claude-azure" {
		t.Errorf("unexpected order for claude: %v, %v", cands[0].Name, cands[1].Name)
	}

	// 3. Default 共通プール (gpt-4o-mini)
	cands = router.ResolveCandidates("gpt-4o-mini")
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates for gpt-4o-mini, got %d", len(cands))
	}
	// Priority 1 の def-azure-2 が先頭に来るはず
	if cands[0].Name != "def-azure-2" || cands[1].Name != "def-azure-1" {
		t.Errorf("unexpected order for default: %v, %v", cands[0].Name, cands[1].Name)
	}

	// 4. 明示的プレフィックス指定 (azure/claude-3-5-sonnet) -> Azure のみ抽出
	cands = router.ResolveCandidates("azure/claude-3-5-sonnet")
	if len(cands) != 1 || cands[0].Name != "claude-azure" {
		t.Errorf("expected only claude-azure for azure/claude-3-5-sonnet, got %v", cands)
	}

	// 5. サーキットブレーカー (429 冷却) による動的優先度反転
	// Priority 1 の def-azure-2 を冷却中にする
	cb.MarkCooldown("def-azure-2", 5*time.Second)
	if !cb.IsCoolingDown("def-azure-2") {
		t.Errorf("expected def-azure-2 to be cooling down")
	}

	cands = router.ResolveCandidates("gpt-4o-mini")
	// 冷却中ではない def-azure-1 が先頭に繰り上がる
	if cands[0].Name != "def-azure-1" || cands[1].Name != "def-azure-2" {
		t.Errorf("expected def-azure-1 first due to cooldown of def-azure-2, got %v, %v", cands[0].Name, cands[1].Name)
	}
}

func TestEndpointConfig_GetResolvedKey(t *testing.T) {
	_ = os.Setenv("TEST_ROUTING_KEY_VAL", "secret-key-12345")
	defer func() { _ = os.Unsetenv("TEST_ROUTING_KEY_VAL") }()

	epEnv := EndpointConfig{Key: "env:TEST_ROUTING_KEY_VAL"}
	if epEnv.GetResolvedKey() != "secret-key-12345" {
		t.Errorf("expected secret-key-12345, got %s", epEnv.GetResolvedKey())
	}

	epDirect := EndpointConfig{Key: "direct-key"}
	if epDirect.GetResolvedKey() != "direct-key" {
		t.Errorf("expected direct-key, got %s", epDirect.GetResolvedKey())
	}
}

func TestLoadRoutingConfig(t *testing.T) {
	tempDir := t.TempDir()
	jsonPath := filepath.Join(tempDir, "endpoints.json")

	content := `{
		"version": "1.0",
		"default": [
			{"name": "ep1", "provider": "azure", "url": "https://test.com", "priority": 1}
		]
	}`
	if err := os.WriteFile(jsonPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test json: %v", err)
	}

	cfg, err := LoadRoutingConfig(jsonPath)
	if err != nil {
		t.Fatalf("LoadRoutingConfig failed: %v", err)
	}
	if len(cfg.Default) != 1 || cfg.Default[0].Name != "ep1" {
		t.Errorf("unexpected loaded config: %+v", cfg)
	}

	// 空パス
	emptyCfg, err := LoadRoutingConfig("")
	if err != nil || emptyCfg == nil {
		t.Errorf("expected empty config for empty path, got err: %v", err)
	}
}
