package n_shape

import (
	"testing"

	"quant-trading/internal/config"
	"quant-trading/internal/data"
)

func testMatcher() *data.EventMatcher {
	cfg, err := data.LoadEvents("../../../config/events_leftside.yaml")
	if err != nil {
		return nil
	}
	return data.NewEventMatcher(cfg)
}

func newTestManager() *config.Manager {
	mgr := config.NewManager("../../../config/rules.json")
	if err := mgr.Load(); err != nil {
		panic(err)
	}
	return mgr
}

func TestNShapeSignal(t *testing.T) {
	s := New(newTestManager(), testMatcher())
	if s == nil {
		t.Fatal("strategy is nil")
	}
}

func TestFullChainViaWave(t *testing.T) {
	s := New(newTestManager(), testMatcher())
	wa := &WaveA{
		AOpen: 10.0, AHigh: 11.8, ALow: 9.95, AClose: 11.5,
		AVol: 5_000_000, AChgPct: 0.075, AAboveMA60: true,
		IsSectorLeader: true, PrevSessionWeak: true,
	}
	ib := &IntradayB{
		TTime: 924, CurPrice: 11.2, CumVol: 800_000,
		AuctionVol: 250_000, AuctionChgPct: 3.5, AuctionTrend: "up",
		BenchCurChg: -0.003,
		PrevClose:   10.0, PrevHigh: 10.5, PrevLow: 9.8,
	}
	ctx := &Ctx{
		EmotionPhase: "发酵", EventDesc: "反包",
		SectorTurnover: 5e9, SectorTurnoverMA20: 2e9,
		StockPE: 15.0, AvgDailyVol: 2_000_000,
	}
	eval, err := s.EvaluateWave(wa, ib, ctx)
	if err != nil {
		t.Fatalf("EvaluateWave error: %v", err)
	}
	if eval.TotalScore < 60 {
		t.Errorf("总分 = %v, want >= 60", eval.TotalScore)
	}
}
