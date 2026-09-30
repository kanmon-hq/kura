package entity_test

import (
	"encoding/json"
	"testing"

	"github.com/kanmon-hq/kura/internal/domain/entity"
)

func TestChatCompletionRequest_FR06_UnknownParameters(t *testing.T) {
	rawJSON := `{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hello"}],
		"thinking": {"type": "enabled", "budget_tokens": 1024},
		"custom_vendor_flag": true
	}`

	var req entity.ChatCompletionRequest
	if err := json.Unmarshal([]byte(rawJSON), &req); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if req.Model != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %s", req.Model)
	}

	// 未知パラメータが ExtraFields に保持されているか
	if _, ok := req.ExtraFields["thinking"]; !ok {
		t.Errorf("expected 'thinking' in ExtraFields")
	}
	if _, ok := req.ExtraFields["custom_vendor_flag"]; !ok {
		t.Errorf("expected 'custom_vendor_flag' in ExtraFields")
	}

	// ToMergedJSON で未知パラメータがそのまま維持されるか
	merged, err := req.ToMergedJSON()
	if err != nil {
		t.Fatalf("ToMergedJSON error: %v", err)
	}

	var resultMap map[string]any
	if err := json.Unmarshal(merged, &resultMap); err != nil {
		t.Fatalf("result JSON unmarshal error: %v", err)
	}

	if _, ok := resultMap["thinking"]; !ok {
		t.Errorf("expected 'thinking' preserved in merged JSON")
	}
	if _, ok := resultMap["custom_vendor_flag"]; !ok {
		t.Errorf("expected 'custom_vendor_flag' preserved in merged JSON")
	}
}
