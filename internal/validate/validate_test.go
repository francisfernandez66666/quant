package validate

import (
	"testing"

	"quant-trading/internal/config"
)

func newTestManager() *config.Manager {
	mgr := config.NewManager("../../config/rules.json")
	if err := mgr.Load(); err != nil {
		panic(err)
	}
	return mgr
}

func TestCrossCheckConsistent(t *testing.T) {
	e := New(newTestManager())
	sources := map[PriceSource]float64{
		SourceAPI:     10.50,
		SourceTushare: 10.51,
	}
	result := e.CrossCheck(sources)
	if !result.Consistent {
		t.Error("差异在0.1%内应一致")
	}
}

func TestCrossCheckInconsistent(t *testing.T) {
	e := New(newTestManager())
	sources := map[PriceSource]float64{
		SourceAPI:     10.00,
		SourceTushare: 11.00,
	}
	result := e.CrossCheck(sources)
	if result.Consistent {
		t.Error("差异10%应不一致")
	}
	if !result.Alert {
		t.Error("应触发告警")
	}
}
