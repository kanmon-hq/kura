package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kanmon-hq/kura/internal/infrastructure/config"
	"github.com/kanmon-hq/kura/internal/infrastructure/store"
)

func TestInitializeStores_ValidSQLite(t *testing.T) {
	dir, err := os.MkdirTemp("", "kura_store_factory_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	cfg := &config.Config{
		CostStoreType:  "sqlite",
		UsageStoreType: "sqlite",
		SQLitePath:     filepath.Join(dir, "kura.db"),
	}

	bundle, err := store.InitializeStores(cfg)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if bundle.CostStore == nil || bundle.UsageStore == nil {
		t.Fatalf("expected non-nil stores")
	}
}

func TestInitializeStores_DefaultIsSQLite(t *testing.T) {
	dir, err := os.MkdirTemp("", "kura_store_factory_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	cfg := &config.Config{
		CostStoreType:  "",
		UsageStoreType: "",
		SQLitePath:     filepath.Join(dir, "kura.db"),
	}

	bundle, err := store.InitializeStores(cfg)
	if err != nil {
		t.Fatalf("expected success with default sqlite, got error: %v", err)
	}
	if bundle.CostStore == nil || bundle.UsageStore == nil {
		t.Fatalf("expected non-nil stores")
	}
}

func TestInitializeStores_RejectMemory(t *testing.T) {
	cfg1 := &config.Config{
		CostStoreType:  "memory",
		UsageStoreType: "sqlite",
	}
	if _, err := store.InitializeStores(cfg1); err == nil {
		t.Fatalf("expected error when COST_STORE is memory, got nil")
	}

	cfg2 := &config.Config{
		CostStoreType:  "sqlite",
		UsageStoreType: "memory",
	}
	if _, err := store.InitializeStores(cfg2); err == nil {
		t.Fatalf("expected error when USAGE_STORE is memory, got nil")
	}
}

func TestInitializeStores_InvalidUsageStoreValkey(t *testing.T) {
	cfg := &config.Config{
		CostStoreType:  "sqlite",
		UsageStoreType: "valkey",
	}

	_, err := store.InitializeStores(cfg)
	if err == nil {
		t.Fatalf("expected error when USAGE_STORE is valkey, got nil")
	}
}

func TestInitializeStores_InvalidCostStore(t *testing.T) {
	cfg := &config.Config{
		CostStoreType:  "invalid-backend",
		UsageStoreType: "sqlite",
	}

	_, err := store.InitializeStores(cfg)
	if err == nil {
		t.Fatalf("expected error for invalid COST_STORE, got nil")
	}
}

func TestInitializeStores_CosmosDB_MissingConfig(t *testing.T) {
	cfg := &config.Config{
		CostStoreType:  "cosmosdb",
		UsageStoreType: "cosmosdb",
	}

	_, err := store.InitializeStores(cfg)
	if err == nil {
		t.Fatalf("expected error when Cosmos DB credentials are missing, got nil")
	}
}

func TestInitializeStores_DynamoDB_Success(t *testing.T) {
	cfg := &config.Config{
		CostStoreType:     "dynamodb",
		UsageStoreType:    "dynamodb",
		DynamoDBTableName: "KuraUsage",
		AWSRegion:         "ap-northeast-1",
	}

	bundle, err := store.InitializeStores(cfg)
	if err != nil {
		t.Fatalf("expected success with dynamodb fallback, got %v", err)
	}
	if bundle.CostStore == nil || bundle.UsageStore == nil {
		t.Fatalf("expected non-nil stores")
	}
}

func TestInitializeStores_Valkey_Success(t *testing.T) {
	dir, err := os.MkdirTemp("", "kura_store_factory_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	cfg := &config.Config{
		CostStoreType:  "valkey",
		UsageStoreType: "sqlite",
		SQLitePath:     filepath.Join(dir, "kura.db"),
		ValkeyURL:      "redis://127.0.0.1:6379/0",
	}

	bundle, err := store.InitializeStores(cfg)
	if err != nil {
		t.Fatalf("expected success with valkey cost store, got %v", err)
	}
	if bundle.CostStore == nil || bundle.UsageStore == nil {
		t.Fatalf("expected non-nil stores")
	}
}
