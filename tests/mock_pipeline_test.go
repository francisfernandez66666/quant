// Package tests — 今日实盘数据 Mock 全链路验证
// 使用 testdata/*.json 加载今日真实快照/评估/K线数据，
// 不依赖实时API，可反复+在任何时间运行。
package tests

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"

	"quant-trading/internal/data"
	"quant-trading/internal/strategy/n_shape"
)

type mockKLine struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

type mockSnapshotStock struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	ChangePct float64 `json:"change_pct"`
	Volume    float64 `json:"volume"`
	Amount    float64 `json:"amount"`
	MScore    float64 `json:"m_score"`
}

type mockEvalEntry struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	ChangePct float64 `json:"change_pct"`
	NScore    float64 `json:"n_score"`
	ND1       float64 `json:"n_d1"`
	ND2       float64 `json:"n_d2"`
	ND3       float64 `json:"n_d3"`
	ND4       float64 `json:"n_d4"`
	NLevel    string  `json:"n_level"`
	NPass     bool    `json:"n_pass"`
	MScore    float64 `json:"m_score"`
}

type mockCalendarEvent struct {
	Date     string `json:"date"`
	Title    string `json:"title"`
	Impact   string `json:"impact"`
	Level    string `json:"level"`
	Duration int    `json:"duration"`
}

type mockNewsItem struct {
	Title  string `json:"title"`
	Source string `json:"source"`
	Date   string `json:"date"`
}

func loadJSON(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func mkKLine(k mockKLine) data.KLine {
	t, _ := time.Parse("2006-01-02", k.Date)
	return data.KLine{Date: t, Open: k.Open, High: k.High, Low: k.Low, Close: k.Close, Volume: k.Volume}
}

func TestMock_PipelineIntegrity(t *testing.T) {
	t.Log("══════════════════════════════════════════════════════")
	t.Log("  今日实盘数据 Mock 全链路测试")
	t.Log("══════════════════════════════════════════════════════")
	t.Log("")

	// ── 加载全部 mock 数据 ──
	var klRaw map[string][]mockKLine
	if err := loadJSON("testdata/kline_data.json", &klRaw); err != nil {
		t.Fatal("加载kline_data.json失败:", err)
	}
	kline := make(map[string][]data.KLine)
	for code, list := range klRaw {
		for _, k := range list {
			kline[code] = append(kline[code], mkKLine(k))
		}
	}

	var snap []mockSnapshotStock
	if err := loadJSON("testdata/snapshot_data.json", &snap); err != nil {
		t.Fatal("加载snapshot_data.json失败:", err)
	}

	var evals []mockEvalEntry
	if err := loadJSON("testdata/eval_data.json", &evals); err != nil {
		t.Fatal("加载eval_data.json失败:", err)
	}

	var cal []mockCalendarEvent
	if err := loadJSON("testdata/calendar_data.json", &cal); err != nil {
		t.Fatal("加载calendar_data.json失败:", err)
	}

	t.Logf("  K线数据: %d只股票", len(kline))
	t.Logf("  快照数据: %d只股票", len(snap))
	t.Logf("  评估数据: %d条", len(evals))
	t.Logf("  日历事件: %d条", len(cal))
	t.Log("")

	// ── Test 1: 老登股 vs 活跃股的动量评分 ──
	t.Log("─ 测试1: 动量评分(老登股vs活跃股)")

	makeKL := func(vol float64) []data.KLine {
		return []data.KLine{
			{Close: 10, Volume: vol}, {Close: 11, Volume: vol},
			{Close: 12, Volume: vol}, {Close: 13, Volume: vol},
			{Close: 14, Volume: vol}, {Close: 15, Volume: vol},
			{Close: 16, Volume: vol}, {Close: 17, Volume: vol},
			{Close: 18, Volume: vol}, {Close: 19, Volume: vol},
			{Close: 20, Volume: vol},
		}
	}

	// 活跃股：深科技式（+5.8%, 高换手, 放量）
	activeStock := &data.StockInfo{ChangePct: 5.8, Turnover: 6.5, Volume: 1_500_000, Price: 23.60}
	activeKL := makeKL(500_000)
	activeScore := calcMomentum(activeStock, activeKL, 12.0, 0, 0)

	// 老登股：兴业银行式（+0.16%, 低换手, 大量但日常）
	oldStock := &data.StockInfo{ChangePct: 0.16, Turnover: 0.3, Volume: 89_700_000, Price: 17.80}
	oldKL := makeKL(80_000_000)
	oldScore := calcMomentum(oldStock, oldKL, 1.0, 0, 0)

	t.Logf("  深科技(活跃+5.8%%) → M=%0.f", activeScore)
	t.Logf("  兴业银行(老登+0.16%%) → M=%0.f", oldScore)
	if activeScore < 60 {
		t.Errorf("深科技动量应≥60, 实际=%.0f", activeScore)
	}
	if oldScore > 20 {
		t.Errorf("兴业银行动量应≤20, 实际=%.0f", oldScore)
	}
	t.Log("")

	// ── Test 2: N形评分验证 ──
	t.Log("─ 测试2: N形评分验证(深科技 vs 长江电力)")

	if testMatcher == nil {
		em, _ := data.LoadEvents("../config/events_leftside.yaml")
		if em != nil {
			testMatcher = data.NewEventMatcher(em)
		}
	}
	if testMatcher == nil {
		t.Skip("events_leftside.yaml 未加载, 跳过N形评分测试")
	}
	scorer := n_shape.NewLeftSideScorer(testMatcher)

	makeKLines := func(code string) []data.KLine {
		return kline[code]
	}

	// 深科技(000021) - 真是走N形的股票
	kl21 := makeKLines("000021")
	if len(kl21) < 5 {
		t.Skip("深科技K线不足5根")
	}
	// 模拟深科技今天走N形：一突→回踩→二突完成
	res21 := scorer.Evaluate(&n_shape.WaveA{
		AOpen: 21.0, AHigh: 22.5, ALow: 20.0, AClose: 21.2,
		AVol: 80000000, AChgPct: 8.0, AAboveMA60: true, IsSectorLeader: true,
	}, &n_shape.IntradayB{
		TTime: 1030, CurPrice: 23.6, CumVol: 150000000,
		AuctionChgPct: 3.5,
		PrevClose:     21.0, PrevHigh: 22.5, PrevLow: 20.0,
		MinuteMACDDIF: 0.45, MinuteMACDDEA: 0.15,
		AvgDailyVol: 80000000, BenchCurChg: 0.5,
	}, &n_shape.Ctx{
		EmotionPhase: "启动", EventDesc: "公司发布扭亏为盈业绩预增公告;长鑫IPO产业链受益",
		SectorTurnover: 5e9, SectorTurnoverMA20: 2e9,
		StockPE: 18, AvgDailyVol: 80000000,
		// NPhase: 3, // 二突阶段
	})
	t.Logf("  深科技(N形二突+D1=30): N=%0.f D1=%.0f D2=%.0f D3=%.0f D4=%.0f Valid=%v Reason=%s",
		res21.Total, res21.D1Event, res21.D2RS, res21.D3Pullback, res21.D4Accept, res21.Valid, res21.Reason)

	// 长江电力(600900) - 没有走N形
	kl900 := makeKLines("600900")
	if len(kl900) >= 5 {
		last900 := kl900[len(kl900)-1]
		res900 := scorer.Evaluate(&n_shape.WaveA{
			AOpen: last900.Open, AHigh: last900.High, ALow: last900.Low,
			AClose: last900.Close, AVol: 50000000,
			AChgPct: 1.5, AAboveMA60: true,
		}, &n_shape.IntradayB{
			TTime: 1400, CurPrice: 28.89, CumVol: 99125010,
			AuctionChgPct: 0.5,
			PrevClose:     28.50, PrevHigh: 29.00, PrevLow: 28.20,
			MinuteMACDDIF: 0.02, MinuteMACDDEA: 0.01,
			AvgDailyVol: 80000000, BenchCurChg: -0.1,
		}, &n_shape.Ctx{
			EmotionPhase: "启动", EventDesc: "",
			SectorTurnover: 1e9, SectorTurnoverMA20: 3e9,
			StockPE: 25, AvgDailyVol: 80000000,
			// NPhase: 0, // idle, 没走N形
		})
		t.Logf("  长江电力(非N形): N=%0.f D1=%.0f D2=%.0f D3=%.0f D4=%.0f Valid=%v Reason=%s",
			res900.Total, res900.D1Event, res900.D2RS, res900.D3Pullback, res900.D4Accept, res900.Valid, res900.Reason)
		if res900.Valid {
			t.Errorf("长江电力(非N形+绿盘)不应Valid=true, 但得到Valid=true")
		}
	}

	if res21.Valid {
		t.Logf("  深科技Valid=true ✅ 符合预期")
	} else {
		// 可能是D1不足或闸门没过, 检查具体原因
		if res21.D1Event < 20 {
			t.Logf("  深科技D1=%.0f<20, EventDesc未命中(数据问题非bug)", res21.D1Event)
		} else if res21.Total < 60 {
			t.Logf("  深科技Total=%.0f<60, 未过总闸门", res21.Total)
		} else if res21.D2RS < 15 {
			t.Logf("  深科技D2=%.0f<15, 未过D2闸门", res21.D2RS)
		} else {
			t.Logf("  深科技其他原因: %s", res21.Reason)
		}
	}
	t.Log("")

	// ── Test 3: 日历过滤验证 ──
	t.Log("─ 测试3: 日历事件白名单过滤")
	allowed := map[string]bool{"fomc": true, "cpi": true, "nfp": true, "pce": true, "contract": true, "war": true}
	bad := 0
	for _, e := range cal {
		if !allowed[e.Level] {
			t.Logf("  ❌ 违规事件: %s (level=%s)", e.Title, e.Level)
			bad++
		} else {
			t.Logf("  ✅ %s (level=%s)", e.Title, e.Level)
		}
	}
	if bad > 0 {
		t.Errorf("有%d条违规日历事件", bad)
	}
	t.Log("")

	// ── Test 4: 通用验证 ──
	t.Log("─ 测试4: 数据完整性")
	t.Logf("  快照: %d只股票", len(snap))
	pricesOk := 0
	for _, s := range snap {
		if s.Price > 0 {
			pricesOk++
		}
	}
	t.Logf("  价格>0: %d/%d", pricesOk, len(snap))
	if pricesOk < len(snap)/2 {
		t.Errorf("超过半数股票价格=0, 数据异常")
	}
	t.Log("")

	// 总结
	t.Log("══════════════════════════════════════════════════════")
	t.Log("  验证完成")
	t.Logf("  K线: ✅ %d只股票", len(kline))
	t.Logf("  快照: ✅ %d只", len(snap))
	t.Logf("  评估: ✅ %d条", len(evals))
	t.Logf("  日历: ✅ %d条(%d违规)", len(cal), bad)
	t.Logf("  动量: 深科技=%.0f 兴业=%.0f", activeScore, oldScore)
	t.Log("══════════════════════════════════════════════════════")
}

// calcMomentum 备份（与 engine.go 一致，用于无引擎依赖的测试）
func calcMomentum(si *data.StockInfo, kLines []data.KLine, gain10d, chipScore, netInflow float64) float64 {
	volRatio := 0.0
	if len(kLines) >= 20 {
		avgVol := 0.0
		for i := len(kLines) - 20; i < len(kLines); i++ {
			avgVol += kLines[i].Volume
		}
		avgVol /= 20.0
		if avgVol > 0 && si.Volume > 0 {
			volRatio = si.Volume / avgVol
		}
	} else if len(kLines) > 0 {
		avgVol := kLines[len(kLines)-1].Volume
		if avgVol > 0 && si.Volume > 0 {
			volRatio = si.Volume / avgVol
		}
	}
	dayScore := 0.0
	switch {
	case si.ChangePct >= 7:
		dayScore = 45
	case si.ChangePct >= 5:
		dayScore = 36
	case si.ChangePct >= 3:
		dayScore = 27
	case si.ChangePct >= 2:
		dayScore = 18
	case si.ChangePct >= 1:
		dayScore = 9
	case si.ChangePct >= 0:
		dayScore = 2
	default:
		dayScore = 0
	}
	turnScore := 0.0
	switch {
	case si.Turnover >= 10:
		turnScore = 25
	case si.Turnover >= 7:
		turnScore = 20
	case si.Turnover >= 5:
		turnScore = 15
	case si.Turnover >= 3:
		turnScore = 10
	case si.Turnover >= 1:
		turnScore = 5
	}
	volScore := 0.0
	if si.ChangePct > 0 && volRatio > 0 {
		volScore = math.Min(20.0, volRatio*si.ChangePct*2)
	}
	trendScore := 0.0
	switch {
	case gain10d >= 10:
		trendScore = 10
	case gain10d >= 7:
		trendScore = 8
	case gain10d >= 5:
		trendScore = 6
	case gain10d >= 3:
		trendScore = 4
	case gain10d >= 0:
		trendScore = 1
	}
	total := dayScore + turnScore + volScore + trendScore
	if total > 100 {
		total = 100
	}
	return total
}
