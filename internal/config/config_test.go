package config

import (
	"os"
	"testing"
)

func TestLoadRules(t *testing.T) {
	mgr := NewManager("../../config/rules.json")
	if err := mgr.Load(); err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	rules := mgr.Get()
	if rules == nil {
		t.Fatal("rules is nil")
	}
	if rules.Strategy.NShape.NPatternScoreThreshold != 60 {
		t.Errorf("NPatternScoreThreshold = %v, want 60", rules.Strategy.NShape.NPatternScoreThreshold)
	}
	if rules.Strategy.Dragon.HardBreakoutOverride != true {
		t.Errorf("HardBreakoutOverride = %v, want true", rules.Strategy.Dragon.HardBreakoutOverride)
	}
	if rules.RiskCtrl.PerStockMax != 30 {
		t.Errorf("PerStockMax = %v, want 30", rules.RiskCtrl.PerStockMax)
	}
}

func TestNShapePositionLimit(t *testing.T) {
	rules := &Rules{}
	rules.Position.MaxTotalPositionPct = 80
	rules.Position.MaxSinglePositionPct = 30
	rules.RiskCtrl.PerStockMax = 30
	rules.Strategy.NShape.NShapeEntryLeftPct = 40
	rules.Strategy.NShape.NShapeEntryRightPct = 60
	rules.Strategy.NShape.NShapeTotalMaxPct = 90
	rules.Strategy.Dragon.HardBreakoutOverride = true
	_ = rules
}

func TestMain(m *testing.M) {
	code := m.Run()
	os.Exit(code)
}
