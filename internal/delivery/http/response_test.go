package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kanmon-hq/kura/internal/domain/entity"
)

func TestRespondJSON(t *testing.T) {
	t.Run("success_with_data", func(t *testing.T) {
		w := httptest.NewRecorder()
		data := map[string]string{"message": "success"}

		RespondJSON(w, http.StatusOK, data)

		if w.Code != http.StatusOK {
			t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
		}

		if cType := w.Header().Get("Content-Type"); cType != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", cType)
		}

		var resp map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response body: %v", err)
		}

		if resp["message"] != "success" {
			t.Errorf("expected message 'success', got '%s'", resp["message"])
		}
	})

	t.Run("nil_data", func(t *testing.T) {
		w := httptest.NewRecorder()

		RespondJSON(w, http.StatusCreated, nil)

		if w.Code != http.StatusCreated {
			t.Errorf("expected status %d, got %d", http.StatusCreated, w.Code)
		}

		if cType := w.Header().Get("Content-Type"); cType != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", cType)
		}

		// Body should be empty when payload is nil
		expected := ""
		if w.Body.String() != expected {
			t.Errorf("expected empty body, got %q", w.Body.String())
		}
	})

	t.Run("encoding_error", func(t *testing.T) {
		w := httptest.NewRecorder()
		// channel cannot be JSON encoded
		data := make(chan int)

		RespondJSON(w, http.StatusOK, data)

		// Encode failure sets 500 error via http.Error but http.Error appends a newline
		expectedBody := "{\"error\":{\"message\":\"Internal Server Error\"}}\n"
		if w.Body.String() != expectedBody {
			t.Errorf("expected body %q, got %q", expectedBody, w.Body.String())
		}
	})
}

func TestWriteError(t *testing.T) {
	t.Run("success_error_response", func(t *testing.T) {
		w := httptest.NewRecorder()
		errResp := entity.NewStandardError(http.StatusBadRequest, entity.ErrorTypeInvalidRequest, "invalid request message", "VENDOR_ERR_001")

		WriteError(w, errResp)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
		}

		if cType := w.Header().Get("Content-Type"); cType != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", cType)
		}

		var resp entity.StandardErrorResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response body: %v", err)
		}

		if resp.Err.Code != http.StatusBadRequest {
			t.Errorf("expected code %d, got %d", http.StatusBadRequest, resp.Err.Code)
		}
		if resp.Err.Type != entity.ErrorTypeInvalidRequest {
			t.Errorf("expected type %s, got %s", entity.ErrorTypeInvalidRequest, resp.Err.Type)
		}
		if resp.Err.Message != "invalid request message" {
			t.Errorf("expected message 'invalid request message', got '%s'", resp.Err.Message)
		}
		if resp.Err.VendorOriginalCode != "VENDOR_ERR_001" {
			t.Errorf("expected vendor_original_code 'VENDOR_ERR_001', got '%s'", resp.Err.VendorOriginalCode)
		}
	})
}
