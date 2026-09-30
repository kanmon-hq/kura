package config_test

import (
	"testing"

	"github.com/kanmon-hq/kura/internal/infrastructure/config"
)

func TestValidateGatewayAuth_InsecureFlag(t *testing.T) {
	cfg := &config.Config{
		InsecureNoGatewayAuth: true,
		GatewaySharedSecret:   "",
	}
	if err := cfg.ValidateGatewayAuth(); err != nil {
		t.Fatalf("expected nil error when InsecureNoGatewayAuth is true, got %v", err)
	}
}

func TestValidateGatewayAuth_MissingSecret(t *testing.T) {
	cfg := &config.Config{
		InsecureNoGatewayAuth: false,
		GatewaySharedSecret:   "",
	}
	if err := cfg.ValidateGatewayAuth(); err == nil {
		t.Fatalf("expected error when GatewaySharedSecret is empty and InsecureNoGatewayAuth is false")
	}
}

func TestValidateGatewayAuth_TooShortSecret(t *testing.T) {
	cfg := &config.Config{
		InsecureNoGatewayAuth: false,
		GatewaySharedSecret:   "short-secret-less-than-32-chars",
	}
	if err := cfg.ValidateGatewayAuth(); err == nil {
		t.Fatalf("expected error for secret with length < 32")
	}
}

func TestValidateGatewayAuth_ValidSecret(t *testing.T) {
	cfg := &config.Config{
		InsecureNoGatewayAuth: false,
		GatewaySharedSecret:   "12345678901234567890123456789012", // 32 chars
	}
	if err := cfg.ValidateGatewayAuth(); err != nil {
		t.Fatalf("expected nil error for valid 32 chars secret, got %v", err)
	}
}

func TestValidateGatewayAuth_InvalidPreviousSecret(t *testing.T) {
	cfg := &config.Config{
		InsecureNoGatewayAuth:       false,
		GatewaySharedSecret:         "12345678901234567890123456789012",
		GatewaySharedSecretPrevious: "too-short",
	}
	if err := cfg.ValidateGatewayAuth(); err == nil {
		t.Fatalf("expected error when GatewaySharedSecretPrevious is too short")
	}
}

func TestValidateGatewayAuth_ValidPreviousSecret(t *testing.T) {
	cfg := &config.Config{
		InsecureNoGatewayAuth:       false,
		GatewaySharedSecret:         "12345678901234567890123456789012",
		GatewaySharedSecretPrevious: "abcdefabcdefabcdefabcdefabcdefab",
	}
	if err := cfg.ValidateGatewayAuth(); err != nil {
		t.Fatalf("expected nil error for valid previous secret, got %v", err)
	}
}

func TestLoad_EnvironmentVariables(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("AWS_REGION", "us-west-2")
	t.Setenv("DEFAULT_TOKEN_QUOTA", "2000000")
	t.Setenv("CACHE_ENABLED", "true")
	t.Setenv("DOCS_PATH", "none")
	t.Setenv("OPENAPI_PATH", "/custom-openapi")
	t.Setenv("ENFORCE_TOLLGATE_AUTH", "true")

	cfg := config.Load()
	if cfg.Port != "9090" {
		t.Errorf("expected Port 9090, got %s", cfg.Port)
	}
	if cfg.AWSRegion != "us-west-2" {
		t.Errorf("expected AWSRegion us-west-2, got %s", cfg.AWSRegion)
	}
	if cfg.DefaultTokenQuota != 2000000 {
		t.Errorf("expected DefaultTokenQuota 2000000, got %d", cfg.DefaultTokenQuota)
	}
	if cfg.DocsPath != "" {
		t.Errorf("expected empty DocsPath when set to 'none', got %s", cfg.DocsPath)
	}
	if cfg.OpenAPIPath != "/custom-openapi" {
		t.Errorf("expected OpenAPIPath /custom-openapi, got %s", cfg.OpenAPIPath)
	}
	if !cfg.EnforceTollgateAuth {
		t.Errorf("expected EnforceTollgateAuth true")
	}
}
