package cosmosdb_test

import (
	"testing"

	"github.com/kanmon-hq/kura/internal/infrastructure/cosmosdb"
)

func TestCostMicroConversion(t *testing.T) {
	tests := []struct {
		cost      float64
		wantMicro int64
	}{
		{cost: 0.0, wantMicro: 0},
		{cost: 1.0, wantMicro: 1000000},
		{cost: 0.000123, wantMicro: 123},
		{cost: 15.24, wantMicro: 15240000},
	}

	for _, tt := range tests {
		micro := cosmosdb.CostToMicroUSD(tt.cost)
		if micro != tt.wantMicro {
			t.Errorf("CostToMicroUSD(%f) = %d, want %d", tt.cost, micro, tt.wantMicro)
		}
		back := cosmosdb.MicroUSDToCost(micro)
		if back != tt.cost {
			t.Errorf("MicroUSDToCost(%d) = %f, want %f", micro, back, tt.cost)
		}
	}
}

func TestNewCosmosStore_Validation(t *testing.T) {
	// 接続情報が空の場合はエラー
	_, err := cosmosdb.NewCosmosStore("", "", "", "kura", "usage")
	if err == nil {
		t.Fatalf("expected error when connection string and endpoint/key are empty")
	}
}
