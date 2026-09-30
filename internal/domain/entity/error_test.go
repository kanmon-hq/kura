package entity_test

import (
	"encoding/json"
	"testing"

	"github.com/kanmon-hq/kura/internal/domain/entity"
)

func TestStandardErrorResponse_Error(t *testing.T) {
	errResp := entity.NewStandardError(400, entity.ErrorTypeInvalidRequest, "invalid parameter", "VEND_400")
	expected := "invalid parameter"
	if errResp.Error() != expected {
		t.Errorf("expected %q, got %q", expected, errResp.Error())
	}
}

func TestStandardErrorResponse_ToJSON(t *testing.T) {
	t.Run("with vendor code", func(t *testing.T) {
		errResp := entity.NewStandardError(500, entity.ErrorTypeInternalError, "internal error", "VEND_500")
		b := errResp.ToJSON()

		var m map[string]map[string]interface{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("failed to unmarshal JSON: %v", err)
		}
		if m["error"]["message"] != "internal error" {
			t.Errorf("expected message 'internal error', got %v", m["error"]["message"])
		}
		if m["error"]["type"] != "internal_error" {
			t.Errorf("expected type 'internal_error', got %v", m["error"]["type"])
		}
		if m["error"]["code"].(float64) != 500 {
			t.Errorf("expected code 500, got %v", m["error"]["code"])
		}
		if m["error"]["vendor_original_code"] != "VEND_500" {
			t.Errorf("expected vendor_original_code 'VEND_500', got %v", m["error"]["vendor_original_code"])
		}
	})

	t.Run("without vendor code", func(t *testing.T) {
		errResp := entity.NewStandardError(401, entity.ErrorTypeUnauthorized, "unauthorized", "")
		b := errResp.ToJSON()

		var m map[string]map[string]interface{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("failed to unmarshal JSON: %v", err)
		}
		if _, exists := m["error"]["vendor_original_code"]; exists {
			t.Errorf("expected vendor_original_code to be omitted, but it was present")
		}
	})
}
