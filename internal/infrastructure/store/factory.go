package store

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/kanmon-hq/kura/internal/domain/repository"
	"github.com/kanmon-hq/kura/internal/infrastructure/config"
	"github.com/kanmon-hq/kura/internal/infrastructure/cosmosdb"
	"github.com/kanmon-hq/kura/internal/infrastructure/dynamodb"
	"github.com/kanmon-hq/kura/internal/infrastructure/firestore"
	"github.com/kanmon-hq/kura/internal/infrastructure/sqlite"
	"github.com/kanmon-hq/kura/internal/infrastructure/valkey"
)

// StoreBundle は初期化された CostStore と UsageStore のペア
type StoreBundle struct {
	CostStore  repository.CostStore
	UsageStore repository.UsageStore
}

// InitializeStores は設定に基づいて CostStore と UsageStore を検証・初期化する
func InitializeStores(cfg *config.Config) (*StoreBundle, error) {
	costStoreType := strings.ToLower(strings.TrimSpace(cfg.CostStoreType))
	if costStoreType == "" {
		costStoreType = "sqlite"
	}

	usageStoreType := strings.ToLower(strings.TrimSpace(cfg.UsageStoreType))
	if usageStoreType == "" {
		usageStoreType = "sqlite"
	}

	// Phase B: memory の指定はエラーで拒絶し、sqlite を促す (Fail-Fast)
	if costStoreType == "memory" || usageStoreType == "memory" {
		return nil, fmt.Errorf("storage type 'memory' is no longer supported for production; use 'sqlite' (or DynamoDB / Cosmos DB / Firestore / Valkey) instead")
	}

	// 組み合わせの検証
	// 不正な組み合わせ: 集計結果ストア (UsageStore) に Valkey / Redis は不可 (永続集計データ・クエリ不能のため)
	if usageStoreType == "valkey" || usageStoreType == "redis" {
		return nil, fmt.Errorf("invalid storage combination: USAGE_STORE cannot be '%s' (Valkey/Redis does not support relational aggregation query)", usageStoreType)
	}

	// 警告ログ
	if costStoreType == "sqlite" || usageStoreType == "sqlite" {
		log.Printf("[WARN] [STORAGE] SQLite store is configured (%s). SQLite is strictly designed for single-instance deployments. Do NOT use SQLite across multiple containers.", cfg.SQLitePath)
	}

	bundle := &StoreBundle{}

	// 1. SQLite の共通インスタンス初期化
	if usageStoreType == "sqlite" && costStoreType == "sqlite" {
		sqliteStore, err := sqlite.NewSQLiteStore(cfg.SQLitePath)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize SQLite store: %w", err)
		}
		bundle.CostStore = sqliteStore
		bundle.UsageStore = sqliteStore
		log.Printf("[INFO] [STORAGE] Storage engine initialized: CostStore=sqlite, UsageStore=sqlite (path=%s)", cfg.SQLitePath)
		return bundle, nil
	}

	// UsageStore の初期化
	switch usageStoreType {
	case "sqlite":
		sqStore, err := sqlite.NewSQLiteStore(cfg.SQLitePath)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize SQLite UsageStore: %w", err)
		}
		bundle.UsageStore = sqStore
	case "dynamodb":
		dynamoRepo := dynamodb.NewQuotaRepository(cfg.DynamoDBEndpoint, cfg.AWSRegion, cfg.DynamoDBTableName, cfg.DefaultTokenQuota)
		bundle.UsageStore = dynamoRepo
		if costStoreType == "dynamodb" {
			bundle.CostStore = dynamoRepo
		}
	case "cosmosdb", "cosmos":
		cosmosStore, err := cosmosdb.NewCosmosStore(cfg.CosmosDBEndpoint, cfg.CosmosDBKey, cfg.CosmosDBConnectionString, cfg.CosmosDBDatabase, cfg.CosmosDBContainer)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize Cosmos DB UsageStore: %w", err)
		}
		bundle.UsageStore = cosmosStore
		if costStoreType == "cosmosdb" || costStoreType == "cosmos" {
			bundle.CostStore = cosmosStore
		}
	case "firestore", "datastore":
		fsStore, err := firestore.NewFirestoreStore(context.Background(), cfg.FirestoreProjectID, cfg.FirestoreDatabase)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize Firestore UsageStore: %w", err)
		}
		bundle.UsageStore = fsStore
		if costStoreType == "firestore" || costStoreType == "datastore" {
			bundle.CostStore = fsStore
		}
	default:
		return nil, fmt.Errorf("unknown USAGE_STORE type '%s'. Supported: sqlite, dynamodb, cosmosdb, firestore", usageStoreType)
	}

	// CostStore の初期化 (まだ未設定の場合)
	if bundle.CostStore == nil {
		switch costStoreType {
		case "sqlite":
			sqStore, err := sqlite.NewSQLiteStore(cfg.SQLitePath)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize SQLite CostStore: %w", err)
			}
			bundle.CostStore = sqStore
		case "dynamodb":
			bundle.CostStore = dynamodb.NewQuotaRepository(cfg.DynamoDBEndpoint, cfg.AWSRegion, cfg.DynamoDBTableName, cfg.DefaultTokenQuota)
		case "cosmosdb", "cosmos":
			cosmosStore, err := cosmosdb.NewCosmosStore(cfg.CosmosDBEndpoint, cfg.CosmosDBKey, cfg.CosmosDBConnectionString, cfg.CosmosDBDatabase, cfg.CosmosDBContainer)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize Cosmos DB CostStore: %w", err)
			}
			bundle.CostStore = cosmosStore
		case "firestore", "datastore":
			fsStore, err := firestore.NewFirestoreStore(context.Background(), cfg.FirestoreProjectID, cfg.FirestoreDatabase)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize Firestore CostStore: %w", err)
			}
			bundle.CostStore = fsStore
		case "valkey", "redis":
			redisURL := cfg.ValkeyURL
			if redisURL == "" {
				redisURL = cfg.RedisURL
			}
			if redisURL == "" {
				redisURL = "redis://localhost:6379/0"
			}
			vkStore, err := valkey.NewValkeyCostStore(redisURL)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize Valkey/Redis CostStore: %w", err)
			}
			bundle.CostStore = vkStore
			log.Printf("[INFO] [STORAGE] Initialized Valkey/Redis CostStore at %s", redisURL)
		default:
			return nil, fmt.Errorf("unknown COST_STORE type '%s'. Supported: sqlite, dynamodb, cosmosdb, firestore, valkey, redis", costStoreType)
		}
	}

	log.Printf("[INFO] [STORAGE] Storage engine initialized: CostStore=%s, UsageStore=%s", costStoreType, usageStoreType)
	return bundle, nil
}
