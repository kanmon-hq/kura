package entity_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kanmon-hq/kura/internal/domain/entity"
)

func TestUsageLogEvent_JSON(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	t.Run("FullFields", func(t *testing.T) {
		event := entity.UsageLogEvent{
			TeamID:           "team1",
			Model:            "gpt-4",
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
			Cost:             0.001,
			TenantID:         "tenant-corp-a",
			KeyID:            "550e8400-e29b-41d4-a716-446655440000",
			KeyPrefix:        "tlge-live-8f9c",
			IsProxied:        true,
			Environment:      "prod",
			Feature:          "chat",
			Tags:             map[string]string{"env": "prod"},
			Timestamp:        now,
		}

		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var unmarshaled entity.UsageLogEvent
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if unmarshaled.Cost != event.Cost {
			t.Errorf("expected cost %v, got %v", event.Cost, unmarshaled.Cost)
		}
		if unmarshaled.TenantID != event.TenantID {
			t.Errorf("expected tenant_id %v, got %v", event.TenantID, unmarshaled.TenantID)
		}
		if unmarshaled.KeyID != event.KeyID {
			t.Errorf("expected key_id %v, got %v", event.KeyID, unmarshaled.KeyID)
		}
		if unmarshaled.KeyPrefix != event.KeyPrefix {
			t.Errorf("expected key_prefix %v, got %v", event.KeyPrefix, unmarshaled.KeyPrefix)
		}
		if unmarshaled.IsProxied != event.IsProxied {
			t.Errorf("expected is_proxied %v, got %v", event.IsProxied, unmarshaled.IsProxied)
		}
		if unmarshaled.Tags["env"] != event.Tags["env"] {
			t.Errorf("expected tag %v, got %v", event.Tags["env"], unmarshaled.Tags["env"])
		}
		if !unmarshaled.Timestamp.Equal(event.Timestamp) {
			t.Errorf("expected timestamp %v, got %v", event.Timestamp, unmarshaled.Timestamp)
		}
	})

	t.Run("OmitEmpty", func(t *testing.T) {
		event := entity.UsageLogEvent{
			TeamID: "team1",
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var raw map[string]interface{}
		json.Unmarshal(data, &raw)
		if _, ok := raw["cost"]; ok {
			t.Errorf("cost should be omitted")
		}
		if _, ok := raw["tenant_id"]; ok {
			t.Errorf("tenant_id should be omitted")
		}
		if _, ok := raw["key_id"]; ok {
			t.Errorf("key_id should be omitted")
		}
		if _, ok := raw["key_prefix"]; ok {
			t.Errorf("key_prefix should be omitted")
		}
		if _, ok := raw["environment"]; ok {
			t.Errorf("environment should be omitted")
		}
		if _, ok := raw["feature"]; ok {
			t.Errorf("feature should be omitted")
		}
		if _, ok := raw["tags"]; ok {
			t.Errorf("tags should be omitted")
		}
	})
}

func TestUsageSummary_JSON(t *testing.T) {
	t.Run("FullFields", func(t *testing.T) {
		summary := entity.UsageSummary{
			ServiceID:           "svc1",
			TenantID:            "tenant1",
			Month:               "2023-10",
			BillingType:         "capped",
			ServiceCostLimitUSD: 100.0,
			ServiceTotalCostUSD: 50.0,
			ServiceRemainingUSD: 50.0,
			TenantCostLimitUSD:  10.0,
			TenantRemainingUSD:  5.0,
			TotalTokens:         100,
			PromptTokens:        50,
			CompletionTokens:    50,
			AllowedModels:       []string{"gpt-4"},
			IsQuotaExceeded:     false,
		}

		data, err := json.Marshal(summary)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var unmarshaled entity.UsageSummary
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if unmarshaled.TenantID != summary.TenantID {
			t.Errorf("expected TenantID %v, got %v", summary.TenantID, unmarshaled.TenantID)
		}
		if len(unmarshaled.AllowedModels) != 1 || unmarshaled.AllowedModels[0] != summary.AllowedModels[0] {
			t.Errorf("expected AllowedModels %v, got %v", summary.AllowedModels, unmarshaled.AllowedModels)
		}
	})

	t.Run("OmitEmpty", func(t *testing.T) {
		summary := entity.UsageSummary{
			ServiceID: "svc1",
		}
		data, err := json.Marshal(summary)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var raw map[string]interface{}
		json.Unmarshal(data, &raw)
		if _, ok := raw["tenant_id"]; ok {
			t.Errorf("tenant_id should be omitted")
		}
		if _, ok := raw["tenant_cost_limit_usd"]; ok {
			t.Errorf("tenant_cost_limit_usd should be omitted")
		}
		if _, ok := raw["tenant_remaining_cost_usd"]; ok {
			t.Errorf("tenant_remaining_cost_usd should be omitted")
		}
		if _, ok := raw["allowed_models"]; ok {
			t.Errorf("allowed_models should be omitted")
		}
	})
}
