// Package tests — 全链路诊断测试
// 目标：定位数据流断点、评分异常、模块传导失效
// 运行: go test ./tests -v -count=1 -timeout 120s 2>&1 | tee tests/diagnostic_$(date +%Y%m%d_%H%M).log

package tests

import (
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"quant-trading/internal/config"
	"quant-trading/internal/data"
	"quant-trading/internal/engine"
	"quant-trading/internal/filter"
	"quant-trading/internal/notify"
	"quant-trading/internal/position"
	"quant-trading/internal/risk"
	"quant-trading/internal/strategy"
	"quant-trading/internal/strategy/double_bump"
	"quant-trading/internal/strategy/dragon"
	"quant-trading/internal/strategy/dragon_return"
	"quant-trading/internal/strategy/n_shape"
	"quant-trading/internal/validate"
)

var (
	testCfg     *config.Manager
	testMatcher *data.EventMatcher
)

func TestMain(m *testing.M) {
	// 加载配置
	testCfg = config.NewManager("../config/rules.json")
	if err := testCfg.Load(); err != nil {
		log.Printf("WARN: 无法加载 rules.json: %v (部分测试将跳过)", err)
	} else {
		// 初始化交易时段（引擎启动时自动执行 data.ApplyConfig）
		cfg := testCfg.Get()
		if cfg.TradeTime.TradeOpen > 0 {
			data.ApplyConfig(data.TradeTimeConfig{
				TradeOpen:      cfg.TradeTime.TradeOpen,
				TradeClose:     cfg.TradeTime.TradeClose,
				FullOpen:       cfg.TradeTime.FullOpen,
				FullClose:      cfg.TradeTime.FullClose,
				PreOpenStart:   cfg.TradeTime.PreOpenStart,
				PreOpenEnd:     cfg.TradeTime.PreOpenEnd,
				MorningHighEnd: cfg.TradeTime.MorningHighEnd,
				MidFreqStart:   cfg.TradeTime.MidFreqStart,
				AfternoonStart: cfg.TradeTime.AfternoonStart,
				AfternoonEnd:   cfg.TradeTime.AfternoonEnd,
			})
		}
		// 热加载监听（非阻塞）
		go testCfg.Watch()
	}

	// 加载事件规则
	em, err := data.LoadEvents("../config/events_leftside.yaml")
	if err != nil {
		log.Printf("WARN: 无法加载 events_leftside.yaml: %v (D1 测试跳过)", err)
	} else {
		testMatcher = data.NewEventMatcher(em)
	}

	os.Exit(m.Run())
}

// ──────────────────────────────────────────
// A 组：交易日历 & 时段检测
// ──────────────────────────────────────────

func TestA1_TradeSessionDetection(t *testing.T) {
	// 验证不同时间点的 session 判断是否正确
	cases := []struct {
		tStr     string // "15:04"
		expected data.MarketSession
		name     string
	}{
		{"08:00", data.SessionClosed, "凌晨"},
		{"08:30", data.SessionPreMarket, "盘前开始"},
		{"09:00", data.SessionPreMarket, "盘前"},
		{"09:14", data.SessionPreMarket, "盘前最后"},
		{"09:15", data.SessionMorningTrade, "早盘开始(集合竞价)"},
		{"09:30", data.SessionMorningTrade, "早盘连续竞价"},
		{"11:00", data.SessionMorningTrade, "早盘中段"},
		{"11:29", data.SessionMorningTrade, "早盘最后"},
		{"11:30", data.SessionPreAfternoon, "午休开始"},
		{"12:00", data.SessionPreAfternoon, "午休"},
		{"12:59", data.SessionPreAfternoon, "午休最后"},
		{"13:00", data.SessionAfternoonTrade, "下午开盘"},
		{"14:00", data.SessionAfternoonTrade, "下午中段"},
		{"14:59", data.SessionAfternoonTrade, "下午尾盘"},
		{"15:00", data.SessionAfterMarket, "收盘"},
		{"15:01", data.SessionAfterMarket, "收盘后"},
	}

	// 用今天日期构造时间
	now := time.Now()
	for _, tc := range cases {
		tm := parseTimeToday(now, tc.tStr)
		got := data.CurrentSession(tm)
		if got != tc.expected {
			t.Errorf("[%s] %s → session=%d, 期望=%d", tc.name, tc.tStr, got, tc.expected)
		}
	}
}

func TestA2_ScanIntervalDuringTrade(t *testing.T) {
	if testCfg == nil {
		t.Skip("rules.json 未加载，跳过间隔测试")
	}
	cfg := testCfg.Get()
	if cfg == nil {
		t.Fatal("testCfg.Get() 返回 nil")
	}
	nc := cfg.Strategy.NShape
	t.Logf("Config 高频=%ds 中频=%ds 午后=%ds 普通=%ds", nc.HighFreqIntervalSec, nc.MidFreqIntervalSec, nc.AfternoonFreqIntervalSec, nc.NormalFreqIntervalSec)
	// 验证不同时段扫描间隔
	cases := []struct {
		tStr string
		desc string
	}{
		{"09:15", "早盘集合竞价"},
		{"09:30", "早盘高频段"},
		{"09:45", "早盘高频段中"},
		{"11:00", "早盘中频段"},
		{"13:00", "午后开盘"},
		{"14:00", "午后普通段"},
	}
	for _, tc := range cases {
		tm := parseTimeToday(time.Now(), tc.tStr)
		got := data.ScanInterval(tm, nc.HighFreqIntervalSec, nc.MidFreqIntervalSec, nc.AfternoonFreqIntervalSec, nc.NormalFreqIntervalSec)
		t.Logf("  %s (%s) → interval=%ds", tc.tStr, tc.desc, got)
	}
}

// ──────────────────────────────────────────
// B 组：D1 事件匹配
// ──────────────────────────────────────────

func TestB1_D1EventMatching(t *testing.T) {
	if testMatcher == nil {
		t.Skip("events_leftside.yaml 未加载")
	}

	cases := []struct {
		desc      string
		wantScore int
		wantBlock bool
		minRules  int
	}{
		{"半导体产业链受益国产替代加速推进", 40, false, 1},
		{"公司发布并购重组预案", 40, false, 1},
		{"因信息披露违规被证监会立案调查", 0, true, 0}, // blocked时MatchedRules不填充
		{"", 0, false, 0},
		{"null", 0, false, 0},
		{"市场整体震荡整理", 0, false, 0},
		{"业绩预增,公司利润大幅提升", 30, false, 1},
		{"回购注销,控股股东增持", 20, false, 1},
		{"高开5%以上,开盘瞬狙", 0, false, 0},
	}

	for _, tc := range cases {
		// 模拟 calcD1 逻辑
		score, tags, blocked := calcD1Direct(tc.desc)
		if blocked != tc.wantBlock {
			t.Errorf("desc=%q blocked=%v, 期望=%v", tc.desc, blocked, tc.wantBlock)
		}
		if score != float64(tc.wantScore) {
			t.Errorf("desc=%q score=%.0f, 期望=%d", tc.desc, score, tc.wantScore)
		}
		if len(tags) < tc.minRules {
			t.Errorf("desc=%q matched_rules=%d, 期望≥%d, tags=%v", tc.desc, len(tags), tc.minRules, tags)
		}
	}
}

func TestB2_D1EmptyDescReturnsZero(t *testing.T) {
	// 关键：空 EventDesc 返回 0 分、不阻塞 → 不能生成 Valid 信号
	score, _, blocked := calcD1Direct("")
	if score != 0 {
		t.Errorf("空desc score=%.0f, 期望=0", score)
	}
	if blocked {
		t.Errorf("空desc 不应被blocked")
	}
}

// ──────────────────────────────────────────
// C 组：N 形评分全链路
// ──────────────────────────────────────────

func TestC1_FullScoreChain_ValidSignal(t *testing.T) {
	if testMatcher == nil {
		t.Skip("events_leftside.yaml 未加载")
	}
	scorer := n_shape.NewLeftSideScorer(testMatcher)

	wa := &n_shape.WaveA{
		AOpen: 10.0, AHigh: 11.5, ALow: 9.8, AClose: 11.2,
		AVol: 500000, AChgPct: 12.0, AAboveMA60: true, IsSectorLeader: true,
	}
	ib := &n_shape.IntradayB{
		TTime: 940, CurPrice: 10.5, CumVol: 300000,
		AuctionChgPct: 2.5, EventType: "normal",
		PrevClose: 11.2, PrevHigh: 11.5, PrevLow: 9.8,
		MinuteMACDDIF: 0.3, MinuteMACDDEA: 0.1,
		AvgDailyVol: 1000000, BenchCurChg: 0.5,
	}
	ctx := &n_shape.Ctx{
		EmotionPhase: "启动", EventDesc: "半导体产业链受益国产替代加速推进",
		SectorTurnover: 5e9, SectorTurnoverMA20: 2e9,
		StockPE: 12, AvgDailyVol: 1000000,
	}

	res := scorer.Evaluate(wa, ib, ctx)
	if res == nil {
		t.Fatal("Evaluate 返回 nil")
	}

	t.Logf("D1=%.0f D2=%.0f D3=%.0f D4=%.0f Total=%.0f Valid=%v",
		res.D1Event, res.D2RS, res.D3Pullback, res.D4Accept, res.Total, res.Valid)
	t.Logf("Priority=%d Remind=%s CanOpen=%v Reason=%s",
		res.Priority, res.RemindLevel, res.CanOpen, res.Reason)

	if !res.Valid {
		t.Errorf("期望 Valid=true, 得到 false. Reason=%s", res.Reason)
	}
	if res.Total < 60 {
		t.Errorf("Total=%.0f < 60", res.Total)
	}
	if res.D1Event < 40 {
		t.Errorf("D1=%.0f < 40", res.D1Event)
	}
}

func TestC2_ScoreChain_NoD1_NotValid(t *testing.T) {
	if testMatcher == nil {
		t.Skip("events_leftside.yaml 未加载")
	}
	scorer := n_shape.NewLeftSideScorer(testMatcher)

	wa := &n_shape.WaveA{
		AOpen: 10.0, AHigh: 11.5, ALow: 9.8, AClose: 11.2,
		AVol: 500000, AChgPct: 12.0, AAboveMA60: true,
	}
	ib := &n_shape.IntradayB{
		TTime: 940, CurPrice: 10.5, CumVol: 300000,
		AuctionChgPct: 2.5,
		PrevClose:     11.2, PrevHigh: 11.5, PrevLow: 9.8,
		MinuteMACDDIF: 0.3, MinuteMACDDEA: 0.1,
		AvgDailyVol: 1000000, BenchCurChg: 0.5,
	}
	ctx := &n_shape.Ctx{
		EmotionPhase: "启动", EventDesc: "", // 空事件 → D1=0
		SectorTurnover: 5e9, SectorTurnoverMA20: 2e9,
		StockPE: 12, AvgDailyVol: 1000000,
	}

	res := scorer.Evaluate(wa, ib, ctx)
	if res == nil {
		t.Fatal("Evaluate 返回 nil")
	}
	if res.Valid {
		t.Errorf("空D1时 Valid 应为 false, 得到 true")
	}
	if res.Total >= 60 {
		// 即使D1=0，总分可能够。但信号硬闸门要求D1=40
		t.Logf("注意: 空D1但Total=%.0f（信号仍不生效）", res.Total)
	}
}

func TestC3_EmotionRecessionBlocks(t *testing.T) {
	if testMatcher == nil {
		t.Skip("events_leftside.yaml 未加载")
	}
	scorer := n_shape.NewLeftSideScorer(testMatcher)

	wa := &n_shape.WaveA{
		AOpen: 10.0, AHigh: 11.5, ALow: 9.8, AClose: 11.2,
		AVol: 500000, AChgPct: 12.0, AAboveMA60: true,
	}
	ib := &n_shape.IntradayB{
		TTime: 940, CurPrice: 10.5, CumVol: 300000,
		AuctionChgPct: 2.5,
		PrevClose:     11.2, PrevHigh: 11.5, PrevLow: 9.8,
		MinuteMACDDIF: 0.3, MinuteMACDDEA: 0.1,
		AvgDailyVol: 1000000, BenchCurChg: 0.5,
	}
	ctx := &n_shape.Ctx{
		EmotionPhase: "衰退", EventDesc: "半导体产业链受益国产替代加速推进",
		SectorTurnover: 5e9, SectorTurnoverMA20: 2e9,
		StockPE: 12, AvgDailyVol: 1000000,
	}

	res := scorer.Evaluate(wa, ib, ctx)
	if res == nil {
		t.Fatal("Evaluate 返回 nil")
	}
	t.Logf("Total=%.0f Reason=%s", res.Total, res.Reason)
	if res.Reason != "emotion_recession_block" {
		t.Errorf("期望衰退阻断, 得到 reason=%s", res.Reason)
	}
}

func TestC4_SectorColdCheck(t *testing.T) {
	if testMatcher == nil {
		t.Skip("events_leftside.yaml 未加载")
	}
	scorer := n_shape.NewLeftSideScorer(testMatcher)

	wa := &n_shape.WaveA{
		AOpen: 10.0, AHigh: 11.5, ALow: 9.8, AClose: 11.2,
		AVol: 500000, AChgPct: 12.0, AAboveMA60: true,
	}
	ib := &n_shape.IntradayB{
		TTime: 940, CurPrice: 10.5, CumVol: 300000,
		AuctionChgPct: 2.5,
		PrevClose:     11.2, PrevHigh: 11.5, PrevLow: 9.8,
		MinuteMACDDIF: 0.3, MinuteMACDDEA: 0.1,
		AvgDailyVol: 1000000, BenchCurChg: 0.5,
	}
	// 板块成交额 < 20日均量×2 → sector_cold
	ctx := &n_shape.Ctx{
		EmotionPhase: "启动", EventDesc: "半导体产业链受益国产替代加速推进",
		SectorTurnover: 1e9, SectorTurnoverMA20: 3e9,
		StockPE: 25, AvgDailyVol: 1000000,
	}

	res := scorer.Evaluate(wa, ib, ctx)
	if res == nil {
		t.Fatal("Evaluate 返回 nil")
	}
	t.Logf("Total=%.0f Valid=%v Reason=%s D1=%.0f D2=%.0f", res.Total, res.Valid, res.Reason, res.D1Event, res.D2RS)
	// sector_cold 不阻断评分计算，但会记在 Reason 里
	if !strings.Contains(res.Reason, "sector_cold") {
		t.Logf("sector_cold 未触发（可能其他原因更优先）")
	}
}

func TestC5_Post10amDowngrade(t *testing.T) {
	if testMatcher == nil {
		t.Skip("events_leftside.yaml 未加载")
	}
	scorer := n_shape.NewLeftSideScorer(testMatcher)

	wa := &n_shape.WaveA{
		AOpen: 10.0, AHigh: 11.5, ALow: 9.8, AClose: 11.2,
		AVol: 500000, AChgPct: 12.0, AAboveMA60: true,
	}
	// 10:00 后，priority < 80 应降级
	ib := &n_shape.IntradayB{
		TTime: 1045, CurPrice: 10.5, CumVol: 800000,
		AuctionChgPct: 2.0,
		PrevClose:     11.2, PrevHigh: 11.5, PrevLow: 9.8,
		MinuteMACDDIF: 0.3, MinuteMACDDEA: 0.1,
		AvgDailyVol: 1000000, BenchCurChg: 0.5,
	}
	ctx := &n_shape.Ctx{
		EmotionPhase: "启动", EventDesc: "半导体产业链受益国产替代加速推进",
		SectorTurnover: 5e9, SectorTurnoverMA20: 2e9,
		StockPE: 12, AvgDailyVol: 1000000,
	}

	res := scorer.Evaluate(wa, ib, ctx)
	if res == nil {
		t.Fatal("Evaluate 返回 nil")
	}
	t.Logf("Total=%.0f Valid=%v Priority=%d CanOpen=%v Remind=%s Reason=%s",
		res.Total, res.Valid, res.Priority, res.CanOpen, res.RemindLevel, res.Reason)
	// 10:45 priority=90-5-5=80, 刚好=StrongMin, 不会降级
	// 但如果 D2 不够，会 d2_below_full
	if res.Valid && !res.CanOpen {
		t.Logf("Valid=true 但 CanOpen=false（10点后降级）")
	}
}

// ──────────────────────────────────────────
// D 组：数据源 & K 线
// ──────────────────────────────────────────

func TestD1_KLineCacheBehavior(t *testing.T) {
	// 检查 getCachedKLine 的缓存过期逻辑
	// 引擎每 60s 清空一次 kLineCache，导致每轮 scan 都要重新拉取
	// 这在大盘扫描时会造成大量并发请求
	t.Log("getCachedKLine 每60s清空全缓存 → 每只股票独立请求，无批量化")
	t.Log("建议: 改为批量请求或延长缓存时间")
}

func TestD2_SectorCache(t *testing.T) {
	// 检查板块缓存
	coord := data.NewDataCoordinator(nil, nil, nil, nil)
	if coord == nil {
		t.Fatal("NewDataCoordinator 返回 nil")
	}
	// DataCoordinator 的 sectorCache 60s 过期，但各调用方可能未读取缓存
	t.Log("sectorCache 60s 过期缓存 -> sector_stock_cache 按 code 缓存")
	t.Log("但 ScoreSectorStocks 每轮重新请求 GetSectorStocks")
	t.Log("建议: 减少 sector_stock_cache 过期时间或持久化")
}

// ──────────────────────────────────────────
// E 组：策略阈值硬闸检查
// ──────────────────────────────────────────

func TestE1_DragonThresholds(t *testing.T) {
	if testCfg == nil {
		t.Skip("rules.json 未加载")
	}
	cfg := testCfg.Get()
	d := dragon.New(testCfg)
	if d == nil {
		t.Fatal("dragon.New 返回 nil")
	}
	t.Logf("破局龙: F1-F4 权重可配置, 阈值=70(full_chain)/50(brief)")
	t.Logf("关键参数: HardBreakoutOverride=%v", cfg.Strategy.Dragon.HardBreakoutOverride)
}

func TestE2_DoubleBumpCheck(t *testing.T) {
	db := double_bump.New(testCfg)
	if db == nil {
		t.Fatal("double_bump.New 返回 nil")
	}
	t.Log("双凸: Vol/Adjust/MA 三维评分, 最大85")
}

func TestE3_DragonReturnCheck(t *testing.T) {
	dr := dragon_return.New(testCfg)
	if dr == nil {
		t.Fatal("dragon_return.New 返回 nil")
	}
	t.Log("龙回头: DragonIdentity/Pullback/DuckHead/Confirm 四维, 最大85")
	t.Log("Hard Gate: 板块前2 + 首波35%-70% + RPS20≥75")
	t.Logf("注意: 高门槛可能导致无信号")
}

// ──────────────────────────────────────────
// F 组：风控 & 过滤链
// ──────────────────────────────────────────

func TestF1_RiskCheck(t *testing.T) {
	if testCfg == nil {
		t.Skip("rules.json 未加载，跳过风控测试")
	}
	r := risk.New(testCfg)
	if r == nil {
		t.Fatal("risk.New 返回 nil")
	}
	// 检查黑名单
	sig := &strategy.Signal{Code: "000001", Name: "平安银行"}
	result := r.CheckSignal(sig)
	if result.Blocked {
		t.Errorf("000001 不应被黑名单阻断: %s", result.Reason)
	}
}

func TestF2_FilterFundamental(t *testing.T) {
	f := filter.New(testCfg)
	sig := &strategy.Signal{
		Code: "600123", Name: "ST华仪",
		Meta: map[string]float64{},
	}
	cfg := &config.Rules{Filter: config.FilterConfig{Thresholds: config.FilterThresholds{IsSTFilter: true}}}
	r := f.Check(sig, cfg)
	if r.Pass {
		t.Error("ST 股票应被过滤")
	}
	if r.FilteredBy != "fundamental" {
		t.Errorf("期望 fundamental 过滤, 得到 %s", r.FilteredBy)
	}
}

func TestF3_FilterGain10d(t *testing.T) {
	f := filter.New(testCfg)
	cfg := &config.Rules{Filter: config.FilterConfig{Thresholds: config.FilterThresholds{MaxGain10d: 80}}}
	sig := &strategy.Signal{Meta: map[string]float64{"gain_10d": 95}}
	r := f.Check(sig, cfg)
	if r.Pass {
		t.Error("10日涨幅95% > 80% 应被过滤")
	}
}

func TestF4_FilterTurnover(t *testing.T) {
	f := filter.New(testCfg)
	cfg := &config.Rules{Filter: config.FilterConfig{Thresholds: config.FilterThresholds{MaxTurnover: 40}}}
	sig := &strategy.Signal{Meta: map[string]float64{"turnover": 55}}
	r := f.Check(sig, cfg)
	if r.Pass {
		t.Error("换手率55% > 40% 应被过滤")
	}
}

// ──────────────────────────────────────────
// G 组：全链路集成诊断
// ──────────────────────────────────────────

func TestG1_FullChainDiagnostic(t *testing.T) {
	// 模拟一条完整 pipeline：新闻 → 板块 → 个股 → 评分 → 信号
	if testCfg == nil || testMatcher == nil {
		t.Skip("配置或事件规则未加载")
	}

	// Step 1: D1 事件匹配
	eventDesc := "国产芯片替代加速推进,产业链景气度提升"
	t.Logf("[Step1] EventDesc=%q", eventDesc)
	mr := testMatcher.MatchD1(eventDesc)
	t.Logf("  D1 score=%d blocked=%v rules=%v", mr.Score, mr.Blocked, mr.MatchedRules)
	if mr.Score < 30 {
		t.Logf("  ⚠ D1=%d < 40, N形信号将不生效", mr.Score)
	}

	// Step 2: 板块热度
	// 模拟 hotSectors
	t.Log("[Step2] 板块热点评分（使用 Engine 内的 SectorScanner）")

	// Step 3: 个股评分
	t.Log("[Step3] 个股 N 形评分")
	scorer := n_shape.NewLeftSideScorer(testMatcher)
	wa := &n_shape.WaveA{
		AOpen: 10.0, AHigh: 11.5, ALow: 9.8, AClose: 11.2,
		AVol: 500000, AChgPct: 12.0, AAboveMA60: true, IsSectorLeader: false,
	}
	ib := &n_shape.IntradayB{
		TTime: 935, CurPrice: 10.5, CumVol: 200000,
		AuctionChgPct: 2.0,
		PrevClose:     11.2, PrevHigh: 11.5, PrevLow: 9.8,
		MinuteMACDDIF: 0.15, MinuteMACDDEA: 0.05,
		AvgDailyVol: 1000000, BenchCurChg: 0.3,
	}
	ctx := &n_shape.Ctx{
		EmotionPhase: "启动", EventDesc: eventDesc,
		SectorTurnover: 5e9, SectorTurnoverMA20: 2e9,
		StockPE: 15, AvgDailyVol: 1000000,
	}

	res := scorer.Evaluate(wa, ib, ctx)
	if res == nil {
		t.Fatal("scorer.Evaluate 返回 nil")
	}
	t.Logf("  D1=%.0f D2=%.0f D3=%.0f D4=%.0f Total=%.0f", res.D1Event, res.D2RS, res.D3Pullback, res.D4Accept, res.Total)
	t.Logf("  Valid=%v Priority=%d CanOpen=%v Reason=%s", res.Valid, res.Priority, res.CanOpen, res.Reason)

	if !res.Valid {
		t.Logf("  ❌ 信号无效，原因链: %s", res.Reason)
		// 诊断具体哪个环节断裂
		if res.D1Event < 40 {
			t.Logf("  断点: D1=%0.f < 40 (事件评分不足)", res.D1Event)
		}
		if res.D2RS < 15 {
			t.Logf("  断点: D2=%.0f < 15 (相对强度不足)", res.D2RS)
		}
		if res.Total < 60 {
			t.Logf("  断点: Total=%.0f < 60", res.Total)
		}
		if res.Priority < 80 {
			t.Logf("  观察: Priority=%d < 80 (时间降级)", res.Priority)
		}
	} else {
		t.Logf("  ✅ 信号有效")
	}

	// Step 4: 风控检查
	t.Log("[Step4] 风控检查")
	sig := &strategy.Signal{
		Code: "002371", Name: "北方华创",
		Type: strategy.SignalNShape, Action: strategy.ActionBuy,
		Confidence: float64(res.Total) / 100.0,
		Meta:       map[string]float64{},
	}
	riskEngine := risk.New(testCfg)
	riskResult := riskEngine.CheckSignal(sig)
	if riskResult.Blocked {
		t.Logf("  ❌ 风控阻断: %s", riskResult.Reason)
	} else {
		t.Logf("  ✅ 风控通过")
	}

	// Step 5: 仓位检查
	t.Log("[Step5] 仓位计算")
	posMgr := position.New(testCfg)
	if posMgr == nil {
		t.Log("  position.New 返回 nil")
	} else {
		calc := posMgr.Calculate(sig, 100000, nil, 50, "启动")
		if calc == nil {
			t.Log("  CalcResult 为 nil")
		} else {
			t.Logf("  建议仓位: %d股 (%.0f元, 占比%.1f%%)", calc.Quantity, calc.Amount, calc.PositionPct)
		}
	}
}

// ──────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────

func parseTimeToday(base time.Time, timeStr string) time.Time {
	var h, m int
	fmt.Sscanf(timeStr, "%d:%d", &h, &m)
	return time.Date(base.Year(), base.Month(), base.Day(), h, m, 0, 0, base.Location())
}

// calcD1Direct 直接调用 EventMatcher，模拟 scorer.calcD1 逻辑
func calcD1Direct(desc string) (float64, []string, bool) {
	if desc == "" || desc == "null" || testMatcher == nil {
		return 0, nil, false
	}
	mr := testMatcher.MatchD1(desc)
	if mr.Blocked {
		return 0, mr.MatchedRules, true
	}
	return float64(mr.Score), mr.MatchedRules, false
}

// ──────────────────────────────────────────
// H 组：Engine 引擎完整构造测试（不启动网络）
// ──────────────────────────────────────────

func TestH1_EngineConstruction(t *testing.T) {
	if testCfg == nil {
		t.Skip("rules.json 未加载")
	}

	marketAPI := data.NewMarketAPI()
	rpsMgr := data.NewRPSManager()
	// 注意: NewEventMatcher(nil) 会 panic，必须传入非空 EventsConfig
	eventMatcher := data.NewEventMatcher(&data.EventsConfig{})
	sectorScan := data.NewSectorScanner(marketAPI, eventMatcher)
	riskEngine := risk.New(testCfg)
	posMgr := position.New(testCfg)
	notifier := notify.New()
	history := notify.NewHistory(".")
	validator := validate.New(testCfg)
	filterEng := filter.New(testCfg)
	holdingsMgr := position.NewHoldingsManager(".")
	dragonStr := dragon.New(testCfg)
	doubleBumpStr := double_bump.New(testCfg)
	nShapeStr := n_shape.New(testCfg, eventMatcher)
	dragonReturnStr := dragon_return.New(testCfg)

	eng := engine.New(testCfg, "", "")
	if eng == nil {
		t.Fatal("engine.New 返回 nil")
	}
	_ = eng
	_ = marketAPI
	_ = rpsMgr
	_ = eventMatcher
	_ = sectorScan
	_ = riskEngine
	_ = posMgr
	_ = notifier
	_ = history
	_ = validator
	_ = filterEng
	_ = holdingsMgr
	_ = dragonStr
	_ = doubleBumpStr
	_ = nShapeStr
	_ = dragonReturnStr
	t.Logf("Engine 构造成功")
}

// ──────────────────────────────────────────
// I 组：热点新闻和LLM链路诊断（仅日志，不依赖LLM KEY）
// ──────────────────────────────────────────

func TestI1_NewsToSectorMapping(t *testing.T) {
	if testMatcher == nil || testCfg == nil {
		t.Skip("events_leftside.yaml 或 rules.json 未加载")
	}

	marketAPI := data.NewMarketAPI()
	ss := data.NewSectorScanner(marketAPI, testMatcher)

	// 模拟新闻
	news := []data.NewsItem{
		{Title: "半导体产业链国产替代加速推进,设备龙头受益", Content: "test"},
		{Title: "新能源汽车销量持续增长,锂电板块走强", Content: "test"},
		{Title: "AI大模型落地应用场景拓展,算力需求提升", Content: "test"},
	}
	baseSectors := map[string]string{
		"BK0477": "半导体",
		"BK0473": "新能源车",
		"BK0981": "人工智能",
	}

	em := ss.BuildEventMapFromNews(news, baseSectors)
	t.Logf("新闻→板块映射: %d 条关联", len(em))
	if len(em) == 0 {
		t.Log("⚠ 新闻→板块映射为空")
		t.Log("可能原因: 新闻标题中的行业关键词未在 events_leftside.yaml 中配置")
	}
	for secCode, stocks := range em {
		t.Logf("  %s → %d 条关联", secCode, len(stocks))
	}

	// 验证核心板块 BK0477（半导体）是否匹配到新闻
	if _, ok := em["BK0477"]; !ok {
		t.Log("⚠ 半导体板块(BK0477) 未从新闻映射到")
	}
}

// ──────────────────────────────────────────
// Z 组：运行期诊断摘要
// ──────────────────────────────────────────

func TestZ1_NoSignalRootCauseSummary(t *testing.T) {
	t.Log("═══════════════════════════════════════════════")
	t.Log("  无信号（或信号极少）的根因排查清单")
	t.Log("═══════════════════════════════════════════════")
	t.Log("")
	t.Log("1. D1 事件评分不足 (D1 < 40)")
	t.Log("   - 检查 EventDesc 是否为空: 空→D1=0")
	t.Log("   - 检查 events_leftside.yaml 规则是否覆盖当前热点")
	t.Log("   - 检查 processNewsAndLLM 是否设定了 lastEventDesc")
	t.Log("")
	t.Log("2. D2 相对强度不足 (D2 < 15)")
	t.Log("   - 集合竞价涨幅须 1.5%~5%（太高中低都扣分）")
	t.Log("   - 量比须 1.8~3.0")
	t.Log("   - 超额收益须 > 基准")
	t.Log("")
	t.Log("3. D3 深度不足")
	t.Log("   - PE > 50 得0分")
	t.Log("   - 斐波那契深度不在 0.2~1.0 → 0分")
	t.Log("")
	t.Log("4. 板块冷清 (sector_cold)")
	t.Log("   - 板块成交额 < 20日均量×2")
	t.Log("")
	t.Log("5. 情绪硬闸")
	t.Log("   - EmotionPhase == '衰退' → 全部阻断")
	t.Log("")
	t.Log("6. 时间降级")
	t.Log("   - 10:00后 priority < 80 → can_open=false")
	t.Log("   - 11:00后 priority 更低")
	t.Log("")
	t.Log("7. 风控/过滤")
	t.Log("   - ST/换手率>40/10日涨幅>80 过滤")
	t.Log("   - 黑名单/合规检查")
	t.Log("")
	t.Log("8. 数据源")
	t.Log("   - K线不足2根 → evaluateAll 跳过")
	t.Log("   - K线不足20根 → 无法计算均量")
	t.Log("   - 快照为空 → scanCycle 跳过")
	t.Log("   - 板块数据为空 → 无法评分")
	t.Log("")
	t.Log("9. scanCycle 本身未执行")
	t.Log("   - 检查 fetcher 是否在运行")
	t.Log("   - 检查 session 判断（是否在交易时段）")
	t.Log("   - 检查 ticker 是否被重置")
	t.Log("")
	t.Log("10. N 形状态机初始化")
	t.Log("    - resetNStates 清空 kLineCache → 重新拉取耗时")
	t.Log("═══════════════════════════════════════════════")
}
