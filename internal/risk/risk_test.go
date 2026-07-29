package risk

import (
	"testing"

	"quant-trading/internal/config"
	"quant-trading/internal/strategy"
)

func newTestManager() *config.Manager {
	mgr := config.NewManager("../../config/rules.json")
	if err := mgr.Load(); err != nil {
		panic(err)
	}
	return mgr
}

func TestPriorityOrder(t *testing.T) {
	e := New(newTestManager())

	signals := []strategy.Signal{
		{Code: "000001", Priority: strategy.P3, Action: strategy.ActionSell, Reason: "回撤"},
		{Code: "000001", Priority: strategy.P1, Action: strategy.ActionSell, Reason: "水下死叉"},
		{Code: "000001", Priority: strategy.P2, Action: strategy.ActionSell, Reason: "断板"},
	}

	result := e.ResolveConflict(signals)
	if result == nil {
		t.Fatal("result is nil")
	}
	if result.Priority != strategy.P1 {
		t.Errorf("期望P1(%d), 得到 %d", strategy.P1, result.Priority)
	}
	if result.Reason != "水下死叉" {
		t.Errorf("期望'水下死叉', 得到 %s", result.Reason)
	}
}

func TestNShapePositionLimitBypass(t *testing.T) {
	e := New(newTestManager())

	result := e.PositionLimitCheck(30, 85, 80, strategy.SignalNShape)
	if result.Pass != true {
		t.Error("N形仓位不受30%限制")
	}

	result = e.PositionLimitCheck(30, 95, 80, strategy.SignalNShape)
	if result.Pass != false {
		t.Error("N形仓位应受90%截断")
	}
}

func TestBlacklist(t *testing.T) {
	e := New(newTestManager())
	result := e.CheckSignal(&strategy.Signal{Code: "000001", Type: strategy.SignalDragon})
	if result.Blocked {
		t.Error("000001不应被黑名单拦截")
	}
}
