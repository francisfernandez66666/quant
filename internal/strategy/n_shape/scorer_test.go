package n_shape

import (
	"strings"
	"testing"

	"quant-trading/internal/data"
)

func testScorerV4() *LeftSideScorer {
	cfg, err := data.LoadEvents("../../../config/events_leftside.yaml")
	if err != nil {
		return nil
	}
	return NewLeftSideScorer(data.NewEventMatcher(cfg))
}

func TestPriority_Auction(t *testing.T) {
	p := priorityOf(924, 40, true, "发酵")
	if p.Level != 100 {
		t.Errorf("9:24 = %d, want 100", p.Level)
	}
	if !p.CanOpen {
		t.Error("9:24 应可开仓")
	}
}

func TestPriority_OpenConfirm(t *testing.T) {
	p := priorityOf(940, 40, true, "发酵")
	if p.Level != 100 {
		t.Errorf("9:40 = %d, want 100（9:20-10:00均为100）", p.Level)
	}
}

func TestPriority_MorningLeft(t *testing.T) {
	p1 := priorityOf(1015, 40, true, "发酵")
	if p1.Level != 90 {
		t.Errorf("10:15 = %d, want 90（10:00起90，每30分-5）", p1.Level)
	}
	p2 := priorityOf(1045, 0, false, "发酵")
	if p2.Level != 85 {
		t.Errorf("10:45 = %d, want 85（blocks=1）", p2.Level)
	}
}

func TestPriority_Midday(t *testing.T) {
	p := priorityOf(1100, 40, true, "发酵")
	if p.Level != 80 {
		t.Errorf("11:00 = %d, want 80（blocks=2）", p.Level)
	}
	p2 := priorityOf(1130, 40, true, "发酵")
	if p2.Level != 75 {
		t.Errorf("11:30 = %d, want 75（blocks=3）", p2.Level)
	}
}

func TestPriority_Tail(t *testing.T) {
	p := priorityOf(1445, 40, true, "发酵")
	if p.Level != 67 {
		t.Errorf("14:45 = %d, want 67（90-22.5=67.5→67）", p.Level)
	}
}

func TestPriority_Afternoon(t *testing.T) {
	p1 := priorityOf(1300, 40, true, "发酵")
	if p1.Level != 90 {
		t.Errorf("13:00 = %d, want 90", p1.Level)
	}
	p2 := priorityOf(1330, 40, true, "发酵")
	if p2.Level != 82 {
		t.Errorf("13:30 = %d, want 82（90-7.5=82.5→82）", p2.Level)
	}
	p3 := priorityOf(1500, 40, true, "发酵")
	if p3.Level != 60 {
		t.Errorf("15:00 = %d, want 60（blocks=4, 90-30=60）", p3.Level)
	}
}

func TestPriority_EmotionBlock(t *testing.T) {
	p := priorityOf(940, 40, true, "衰退")
	if p.Level != -1 {
		t.Errorf("衰退 = %d, want -1", p.Level)
	}
	if p.CanOpen {
		t.Error("衰退不可开仓")
	}
}

func TestPriority_RetreatReduce(t *testing.T) {
	p := priorityOf(940, 40, true, "退潮")
	if p.Level != 70 {
		t.Errorf("退潮 9:40 = %d, want 70（100-30）", p.Level)
	}
}

func TestPriority_OffHours(t *testing.T) {
	p := priorityOf(900, 40, true, "发酵")
	if p.Level != -1 {
		t.Errorf("9:00 = %d, want -1", p.Level)
	}
}

func TestD2_WeakToStrong(t *testing.T) {
	wa := &WaveA{AClose: 10.0, AVol: 5_000_000, AChgPct: 0.075, AAboveMA60: true}
	ib := &IntradayB{AuctionChgPct: 3.5, AuctionTrend: "up", AuctionVol: 250_000, CurPrice: 11.2, BenchCurChg: -0.003}
	score := (&LeftSideScorer{}).calcD2(wa, ib)
	if score <= 0 {
		t.Error("弱转强应得分 > 0")
	}
}

func TestD3_Fib382_618(t *testing.T) {
	wa := &WaveA{AHigh: 11.8, ALow: 9.95}
	ib := &IntradayB{CurPrice: 10.9}
	ctx := &Ctx{StockPE: 0, AvgDailyVol: 0}
	score := (&LeftSideScorer{}).calcD3(wa, ib, ctx)
	// 无PE时Fibonacci 0.382-0.618 = MaxD3*0.6
	expected := MaxD3 * 0.6
	if score != expected {
		t.Errorf("0.382-0.618 应得 %v, 得 %v", expected, score)
	}
}

func TestMorphologyGate_BrokeALow(t *testing.T) {
	wa := &WaveA{ALow: 10.0}
	ib := &IntradayB{CurPrice: 9.5}
	if g := morphologyGate(wa, ib); g != "broke_a_low" {
		t.Errorf("wants broke_a_low, got %s", g)
	}
}

func TestD4_VolumeAndMACD(t *testing.T) {
	ib := &IntradayB{
		TTime: 1000, CumVol: 1_000_000,
		MinuteMACDDIF: 0.5, MinuteMACDDEA: 0.2,
	}
	score := (&LeftSideScorer{}).calcD4(ib, 3_000_000)
	// MACD dif > dea && dif > 0 → +5; 累计量1M < 3M*330/330*0.1 → timeRatio=0.1 → expectedVol=300K, 1M > 300K*1.5 → +5
	if score != MaxD4 {
		t.Errorf("D4 = %v, want %v", score, MaxD4)
	}
}

func TestLeftSignal(t *testing.T) {
	s := testScorerV4()
	if s == nil {
		t.Skip("events_leftside.yaml not found")
	}
	wa := &WaveA{
		AHigh: 11.8, ALow: 9.95, AClose: 11.5, AVol: 5_000_000,
		AChgPct: 0.075, AAboveMA60: true, IsSectorLeader: true, PrevSessionWeak: true,
	}
	ib := &IntradayB{
		TTime: 935, CurPrice: 10.9, CumVol: 800_000,
		AuctionVol: 250_000, AuctionChgPct: 3.5, AuctionTrend: "up",
		BenchCurChg: -0.003,
		PrevClose:   10.0, PrevHigh: 10.5, PrevLow: 9.8,
	}
	ctx := &Ctx{
		EmotionPhase: "发酵", EventDesc: "反包",
		SectorTurnover: 5e9, SectorTurnoverMA20: 2e9,
		StockPE: 15.0, AvgDailyVol: 2_000_000,
	}
	r := s.Evaluate(wa, ib, ctx)
	if !r.LeftSignal {
		t.Error("早盘反包应触发 left_signal")
	}
	if r.RemindLevel != "strong" {
		t.Errorf("remind = %s, want strong", r.RemindLevel)
	}
}

func TestD1_IntradayHard(t *testing.T) {
	s := testScorerV4()
	if s == nil {
		t.Skip("events_leftside.yaml not found")
	}
	d1, tags, blocked := s.calcD1(&Ctx{EventDesc: "反包"})
	if blocked {
		t.Fatal("反包不应被拦截")
	}
	if d1 != 40 {
		t.Errorf("D1 = %v, want 40", d1)
	}
	if len(tags) == 0 {
		t.Error("应命中词库")
	}
}

func TestD1_AnnounceHard(t *testing.T) {
	s := testScorerV4()
	if s == nil {
		t.Skip("events_leftside.yaml not found")
	}
	d1, tags, blocked := s.calcD1(&Ctx{EventDesc: "并购重组获证监会核准"})
	if blocked {
		t.Fatal("并购重组不应被拦截")
	}
	if d1 != 40 {
		t.Errorf("D1 = %v, want 40", d1)
	}
	if len(tags) == 0 {
		t.Error("应命中词库")
	}
}

func TestD1_Negative(t *testing.T) {
	s := testScorerV4()
	if s == nil {
		t.Skip("events_leftside.yaml not found")
	}
	d1, tags, blocked := s.calcD1(&Ctx{EventDesc: "公司涉嫌违规被立案调查"})
	if !blocked {
		t.Error("负面词应拦截")
	}
	if d1 != 0 {
		t.Errorf("D1 = %v, want 0", d1)
	}
	_ = tags
}

func TestD3_BelowLow(t *testing.T) {
	wa := &WaveA{AHigh: 11.8, ALow: 9.95}
	ib := &IntradayB{CurPrice: 9.50}
	ctx := &Ctx{StockPE: 0, AvgDailyVol: 0}
	score := (&LeftSideScorer{}).calcD3(wa, ib, ctx)
	if score != 0 {
		t.Errorf("跌破A低应得 0, 得 %v", score)
	}
}

func TestFullEval_AuctionIntradayHard(t *testing.T) {
	s := testScorerV4()
	if s == nil {
		t.Skip("events_leftside.yaml not found")
	}
	wa := &WaveA{
		ADate: "2026-07-18", AOpen: 10.0, AHigh: 11.8, ALow: 9.95, AClose: 11.5,
		AVol: 5_000_000, AChgPct: 0.075, AAboveMA60: true,
		IsSectorLeader: true, PrevSessionWeak: true,
	}
	ib := &IntradayB{
		TTime: 924, CurPrice: 11.2, CumVol: 800_000,
		AuctionVol: 250_000, AuctionHigh: 11.3, AuctionLow: 10.9,
		AuctionChgPct: 3.5, AuctionTrend: "up",
		BenchCurChg: -0.003,
		PrevClose:   10.0, PrevHigh: 10.5, PrevLow: 9.8,
	}
	ctx := &Ctx{
		EmotionPhase: "发酵", EventDesc: "反包",
		SectorTurnover: 5e9, SectorTurnoverMA20: 2e9,
		StockPE: 15.0, AvgDailyVol: 2_000_000,
	}
	r := s.Evaluate(wa, ib, ctx)
	if r.Priority != 100 {
		t.Errorf("priority = %d, want 100", r.Priority)
	}
	if r.RemindLevel != "strong" {
		t.Errorf("remind = %s, want strong", r.RemindLevel)
	}
	if !r.CanOpen {
		t.Error("应可开仓")
	}
	if !r.LeftSignal {
		t.Error("应触发 left_signal")
	}
}

func TestFullEval_MiddayDowngrade(t *testing.T) {
	s := testScorerV4()
	if s == nil {
		t.Skip("events_leftside.yaml not found")
	}
	wa := &WaveA{
		ADate: "2026-07-18", AOpen: 10.0, AHigh: 11.8, ALow: 9.95, AClose: 11.5,
		AVol: 5_000_000, AChgPct: 0.075, AAboveMA60: true, IsSectorLeader: true,
	}
	ib := &IntradayB{
		TTime: 1130, CurPrice: 10.9, CumVol: 2_500_000,
		AuctionChgPct: 2.0, AuctionTrend: "flat",
		BenchCurChg: -0.005,
	}
	ctx := &Ctx{
		EmotionPhase: "发酵",
		StockPE:      15.0,
		AvgDailyVol:  2_000_000,
	}
	r := s.Evaluate(wa, ib, ctx)
	if r.Priority >= StrongMin {
		t.Errorf("11:30 priority = %d, 应 < %d", r.Priority, StrongMin)
	}
	if r.CanOpen {
		t.Error("11:30 不可开仓")
	}
}

func TestD1D4_Descriptions_Populated(t *testing.T) {
	s := testScorerV4()
	if s == nil {
		t.Skip("events_leftside.yaml not found")
	}
	wa := &WaveA{
		ADate: "2026-07-18", AOpen: 10.0, AHigh: 11.8, ALow: 9.95, AClose: 11.5,
		AVol: 5_000_000, AChgPct: 0.075, AAboveMA60: true, IsSectorLeader: true,
	}
	ib := &IntradayB{
		TTime: 940, CurPrice: 11.0, CumVol: 3_000_000,
		AuctionChgPct: 3.5, AuctionTrend: "up",
		PrevClose: 10.5, PrevHigh: 11.8, PrevLow: 9.95,
		MinuteMACDDIF: 0.3, MinuteMACDDEA: 0.1,
		AvgDailyVol: 4_000_000, BenchCurChg: 0.005,
	}
	ctx := &Ctx{
		EmotionPhase:       "发酵",
		SectorTurnover:     5e9,
		SectorTurnoverMA20: 2e9,
		StockPE:            18,
		AvgDailyVol:        4_000_000,
	}
	r := s.Evaluate(wa, ib, ctx)

	if r.D2Desc == "" {
		t.Error("D2Desc 为空")
	}
	if r.D3Desc == "" {
		t.Error("D3Desc 为空")
	}
	if r.D4Desc == "" {
		t.Error("D4Desc 为空")
	}
}

func TestD2Desc_ContainsExpectedKeywords(t *testing.T) {
	tests := []struct {
		name     string
		wa       *WaveA
		ib       *IntradayB
		score    float64
		contains []string
	}{
		{
			name:  "竞价强+放量+超额收益",
			score: 25,
			wa:    &WaveA{AChgPct: 0.075},
			ib: &IntradayB{
				TTime: 940, CurPrice: 11.0, CumVol: 5_000_000,
				AuctionChgPct: 3.5, PrevClose: 10.5,
				AvgDailyVol: 4_000_000, BenchCurChg: 0.0,
			},
			contains: []string{"竞价强", "放量", "超额收益"},
		},
		{
			name:  "竞价过强+缩量+落后大盘",
			score: 15,
			wa:    &WaveA{AChgPct: 0.02},
			ib: &IntradayB{
				TTime: 1000, CurPrice: 10.2, CumVol: 300_000,
				AuctionChgPct: 6.0, PrevClose: 10.0,
				AvgDailyVol: 4_000_000, BenchCurChg: 0.05,
			},
			contains: []string{"竞价过强", "缩量", "落后大盘"},
		},
		{
			name:  "竞价弱+量平",
			score: 5,
			wa:    &WaveA{AChgPct: -0.01},
			ib: &IntradayB{
				TTime: 940, CurPrice: 9.8, CumVol: 600_000,
				AuctionChgPct: -0.5, PrevClose: 10.0,
				AvgDailyVol: 4_000_000, BenchCurChg: 0.0,
			},
			contains: []string{"竞价弱", "量平"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc := d2desc(tt.wa, tt.ib, tt.score)
			for _, kw := range tt.contains {
				if !strings.Contains(desc, kw) {
					t.Errorf("d2desc 应包含 %q, 实际=%q", kw, desc)
				}
			}
		})
	}
}

func TestD3Desc_PEAndFibonacci(t *testing.T) {
	tests := []struct {
		name     string
		wa       *WaveA
		ib       *IntradayB
		ctx      *Ctx
		score    float64
		contains string
	}{
		{
			name:     "PE低估",
			wa:       &WaveA{},
			ib:       &IntradayB{CurPrice: 11.0},
			ctx:      &Ctx{StockPE: 10},
			contains: "PE低估",
		},
		{
			name:     "深回调",
			wa:       &WaveA{AHigh: 12.0, ALow: 10.0},
			ib:       &IntradayB{CurPrice: 10.5},
			ctx:      &Ctx{},
			contains: "深回调",
		},
		{
			name:     "黄金回撤区",
			wa:       &WaveA{AHigh: 12.0, ALow: 10.0},
			ib:       &IntradayB{CurPrice: 11.2},
			ctx:      &Ctx{},
			contains: "黄金回撤区",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc := d3desc(tt.wa, tt.ib, tt.ctx, tt.score)
			if !strings.Contains(desc, tt.contains) {
				t.Errorf("d3desc 应包含 %q, 实际=%q", tt.contains, desc)
			}
		})
	}
}

func TestD4Desc_MACDAndVolume(t *testing.T) {
	tests := []struct {
		name     string
		ib       *IntradayB
		avgVol   float64
		score    float64
		contains []string
	}{
		{
			name: "MACD水上+增量资金",
			ib: &IntradayB{
				TTime: 940, CumVol: 8_000_000,
				MinuteMACDDIF: 0.3, MinuteMACDDEA: 0.1,
			},
			avgVol:   4_000_000,
			contains: []string{"MACD水上", "增量资金"},
		},
		{
			name: "MACD水下+量能平平",
			ib: &IntradayB{
				TTime: 940, CumVol: 400_000,
				MinuteMACDDIF: -0.1, MinuteMACDDEA: 0.05,
			},
			avgVol:   4_000_000,
			contains: []string{"MACD水下", "量能平平"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc := d4desc(tt.ib, tt.avgVol, tt.score)
			for _, kw := range tt.contains {
				if !strings.Contains(desc, kw) {
					t.Errorf("d4desc 应包含 %q, 实际=%q", kw, desc)
				}
			}
		})
	}
}

func TestD1Desc_EmptyAndTags(t *testing.T) {
	if d := d1desc(nil); d != "无事件" {
		t.Errorf("空tags应为'无事件', 实际=%q", d)
	}
	if d := d1desc([]string{}); d != "无事件" {
		t.Errorf("空tags应为'无事件', 实际=%q", d)
	}
	if d := d1desc([]string{"芯片"}); d != "事件:芯片" {
		t.Errorf("期望'事件:芯片', 实际=%q", d)
	}
	if d := d1desc([]string{"芯片", "半导体"}); d != "事件:芯片,半导体" {
		t.Errorf("期望'事件:芯片,半导体', 实际=%q", d)
	}
}
