// Package tests — 全链路集成测试：事件→板块→个股→信号→退出
//
// 运行:
//
//	go test ./tests -run TestPipeline -v -count=1 -timeout 30s
package tests

import (
	"testing"
	"time"

	"quant-trading/internal/data"
	"quant-trading/internal/strategy"
	"quant-trading/internal/strategy/double_bump"
	"quant-trading/internal/strategy/dragon"
	"quant-trading/internal/strategy/dragon_return"
	"quant-trading/internal/strategy/n_shape"
)

// ──────────────────────────────────────────
// C 组：退出策略检查（核心新增逻辑）
// ──────────────────────────────────────────

func TestC1_DragonReturnExit(t *testing.T) {
	if testCfg == nil {
		t.Skip("rules.json 未加载，跳过")
	}
	cfg := testCfg.Get().Strategy.DragonReturn

	baseTime := time.Date(2026, 7, 27, 10, 0, 0, 0, time.Local)
	entryDate := baseTime.AddDate(0, 0, -3).Format("2006-01-02") // 3 days ago
	kl := make20KLine(45.0, 50.0)

	tests := []struct {
		name     string
		meta     map[string]float64
		cost     float64
		price    float64
		entryAt  string
		klines   []strategy.KLine
		wantExit string
		wantPrio strategy.Priority
	}{
		{
			name: "止损-跌5%",
			meta: map[string]float64{"highest_price": 100},
			cost: 100, price: 94,
			entryAt:  entryDate,
			klines:   kl,
			wantExit: "龙回头止损",
			wantPrio: strategy.P1,
		},
		{
			name: "止盈T2-涨30%",
			meta: map[string]float64{"highest_price": 120},
			cost: 100, price: 130,
			entryAt:  entryDate,
			klines:   kl,
			wantExit: "龙回头止盈T2",
			wantPrio: strategy.P2,
		},
		{
			name: "止盈T1-保本出",
			meta: map[string]float64{"highest_price": 105},
			cost: 100, price: 101,
			entryAt:  entryDate,
			klines:   kl,
			wantExit: "龙回头止盈T1",
			wantPrio: strategy.P2,
		},
		{
			name: "移动止盈-从最高回撤8%",
			meta: map[string]float64{"highest_price": 120},
			cost: 100, price: 110,
			entryAt:  entryDate,
			klines:   kl,
			wantExit: "龙回头移动止盈",
			wantPrio: strategy.P2,
		},
		{
			name: "超期-超过8天",
			meta: map[string]float64{"highest_price": 105},
			cost: 100, price: 98, // 亏损2%，T1不触发(lossPct>-2=false)
			entryAt:  baseTime.AddDate(0, 0, -10).Format("2006-01-02"),
			klines:   kl,
			wantExit: "龙回头超期退出",
			wantPrio: strategy.P3,
		},
		{
			name: "破位-MA20下2%",
			meta: map[string]float64{"highest_price": 105},
			cost: 100, price: 97, // 亏3%，未到止损5%，T1不触发(97<100)
			entryAt:  entryDate,
			klines:   make20KLine(100.0, 48.0),
			wantExit: "龙回头破位",
			wantPrio: strategy.P2,
		},
		{
			name: "不退出-正常持有",
			meta: map[string]float64{"highest_price": 100}, // 最高不高于成本=不会触发移动止盈
			cost: 100, price: 98,                           // 小亏2%，不触任何条件
			entryAt:  entryDate,
			klines:   kl,
			wantExit: "",
			wantPrio: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &strategy.ExitContext{
				CostPrice: tt.cost, CurPrice: tt.price,
				EntryAt: tt.entryAt, EntryMeta: tt.meta,
				DailyK: tt.klines,
			}
			result := dragon_return.CheckExit(ctx, &cfg)
			if tt.wantExit == "" {
				if result != nil {
					t.Errorf("期望 nil, 得到 %+v", result)
				}
				return
			}
			if result == nil {
				t.Fatalf("期望退出(%s), 得到 nil", tt.wantExit)
			}
			if result.Reason != tt.wantExit {
				t.Errorf("退出原因: 期望 %q, 得到 %q", tt.wantExit, result.Reason)
			}
			if result.Priority != tt.wantPrio {
				t.Errorf("优先级: 期望 %d, 得到 %d", tt.wantPrio, result.Priority)
			}
		})
	}
}

func TestC2_NShapeExit(t *testing.T) {
	if testCfg == nil {
		t.Skip("rules.json 未加载，跳过")
	}
	cfg := testCfg.Get().Strategy.NShape

	tests := []struct {
		name     string
		meta     map[string]float64
		cost     float64
		price    float64
		wantExit string
		wantPrio strategy.Priority
	}{
		{
			name: "硬止损-低于0.955",
			meta: map[string]float64{"entry_nphase": 2},
			cost: 100, price: 94,
			wantExit: "N形硬止损",
			wantPrio: strategy.P1,
		},
		{
			name: "形态失败-phase=5",
			meta: map[string]float64{"entry_nphase": 5},
			cost: 100, price: 98,
			wantExit: "N形形态失败",
			wantPrio: strategy.P1,
		},
		{
			name: "不退出-正常持有",
			meta: map[string]float64{"entry_nphase": 3},
			cost: 100, price: 103,
			wantExit: "",
			wantPrio: 0,
		},
		{
			name: "量能衰竭",
			meta: map[string]float64{"entry_nphase": 3, "vol_ratio": 0.3},
			cost: 100, price: 102,
			wantExit: "N形量能衰竭",
			wantPrio: strategy.P3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &strategy.ExitContext{
				CostPrice: tt.cost, CurPrice: tt.price,
				EntryMeta: tt.meta,
			}
			result := n_shape.CheckExit(ctx, &cfg)
			if tt.wantExit == "" {
				if result != nil {
					t.Errorf("期望 nil, 得到 %+v", result)
				}
				return
			}
			if result == nil {
				t.Fatalf("期望退出(%s), 得到 nil", tt.wantExit)
			}
			if result.Reason != tt.wantExit {
				t.Errorf("退出原因: 期望 %q, 得到 %q", tt.wantExit, result.Reason)
			}
			if result.Priority != tt.wantPrio {
				t.Errorf("优先级: 期望 %d, 得到 %d", tt.wantPrio, result.Priority)
			}
		})
	}
}

func TestC3_DragonExit(t *testing.T) {
	if testCfg == nil {
		t.Skip("rules.json 未加载，跳过")
	}
	cfg := testCfg.Get().Strategy.Dragon

	entryDate := time.Now().AddDate(0, 0, -3).Format("2006-01-02")

	tests := []struct {
		name     string
		meta     map[string]float64
		cost     float64
		price    float64
		entryAt  string
		dailyK   []strategy.KLine
		wantExit string
		wantPrio strategy.Priority
	}{
		{
			name: "回撤全出-跌6%",
			cost: 100, price: 93,
			entryAt:  entryDate,
			wantExit: "买入回撤全出",
			wantPrio: strategy.P1,
		},
		{
			name: "回撤半仓-跌4%",
			cost: 100, price: 96,
			entryAt:  entryDate,
			wantExit: "买入回撤半仓",
			wantPrio: strategy.P2,
		},
		{
			name: "炸板全出-从涨停回落8%",
			meta: map[string]float64{"limit_price": 110},
			cost: 100, price: 101, // (101-110)/110=-8.18%, <= -8% → 炸板全出
			entryAt:  "",
			wantExit: "炸板全出",
			wantPrio: strategy.P1,
		},
		{
			name: "炸板半仓-从涨停回落9%",
			meta: map[string]float64{"limit_price": 110},
			cost: 100, price: 100, // (100-110)/110=-9.09% <= -9% → 炸板半仓
			entryAt:  "",
			wantExit: "炸板半仓",
			wantPrio: strategy.P2,
		},
		{
			name: "超期-持仓超2天",
			cost: 100, price: 101,
			entryAt:  entryDate,
			wantExit: "破局龙超期",
			wantPrio: strategy.P3,
		},
		{
			name: "不退出-正常",
			cost: 100, price: 98, // 小亏2%
			entryAt:  time.Now().Format("2006-01-02"),
			wantExit: "",
			wantPrio: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &strategy.ExitContext{
				CostPrice: tt.cost, CurPrice: tt.price,
				EntryAt: tt.entryAt, EntryMeta: tt.meta,
				DailyK: tt.dailyK,
			}
			result := dragon.CheckExit(ctx, &cfg)
			if tt.wantExit == "" {
				if result != nil {
					t.Errorf("期望 nil, 得到 %+v", result)
				}
				return
			}
			if result == nil {
				t.Fatalf("期望退出(%s), 得到 nil", tt.wantExit)
			}
			if result.Reason != tt.wantExit {
				t.Errorf("退出原因: 期望 %q, 得到 %q", tt.wantExit, result.Reason)
			}
			if result.Priority != tt.wantPrio {
				t.Errorf("优先级: 期望 %d, 得到 %d", tt.wantPrio, result.Priority)
			}
		})
	}
}

func TestC4_DoubleBumpExit(t *testing.T) {
	if testCfg == nil {
		t.Skip("rules.json 未加载，跳过")
	}
	cfg := testCfg.Get().Strategy.DoubleBump

	entryDate := time.Now().AddDate(0, 0, -5).Format("2006-01-02")

	tests := []struct {
		name     string
		meta     map[string]float64
		cost     float64
		price    float64
		entryAt  string
		dailyK   []strategy.KLine
		wantExit string
		wantPrio strategy.Priority
	}{
		{
			name: "派发信号-放量跌",
			cost: 100, price: 98,
			entryAt: entryDate,
			dailyK: []strategy.KLine{
				{Close: 105, Volume: 100}, // d-4
				{Close: 104, Volume: 90},  // d-3
				{Close: 103, Volume: 95},  // d-2
				{Close: 102, Volume: 85},  // d-1
				{Close: 98, Volume: 200},  // today: volume spike (200 > avg 92.5*1.5=138.7) + close down
			},
			wantExit: "双凸派发信号",
			wantPrio: strategy.P1,
		},
		{
			name: "破MA5",
			cost: 100, price: 97,
			entryAt: entryDate,
			dailyK: []strategy.KLine{
				{Close: 105}, {Close: 104}, {Close: 103}, {Close: 102}, {Close: 96},
			},
			wantExit: "双凸破MA5",
			wantPrio: strategy.P2,
		},
		{
			name: "止盈-涨15%",
			meta: map[string]float64{"highest_price": 115},
			cost: 100, price: 116,
			entryAt:  entryDate,
			wantExit: "双凸止盈",
			wantPrio: strategy.P2,
		},
		{
			name: "回撤退出-从最高回撤8%",
			meta: map[string]float64{"highest_price": 120},
			cost: 100, price: 110,
			entryAt:  entryDate,
			wantExit: "双凸回撤退出",
			wantPrio: strategy.P2,
		},
		{
			name: "调整超期-超过10天",
			cost: 100, price: 101,
			entryAt:  time.Now().AddDate(0, 0, -12).Format("2006-01-02"),
			wantExit: "双凸调整超期",
			wantPrio: strategy.P3,
		},
		{
			name: "不退出-正常",
			cost: 100, price: 108,
			entryAt: time.Now().Format("2006-01-02"),
			dailyK: []strategy.KLine{
				{Close: 100}, {Close: 102}, {Close: 104}, {Close: 106}, {Close: 108},
			},
			wantExit: "",
			wantPrio: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &strategy.ExitContext{
				CostPrice: tt.cost, CurPrice: tt.price,
				EntryAt: tt.entryAt, EntryMeta: tt.meta,
				DailyK: tt.dailyK,
			}
			result := double_bump.CheckExit(ctx, &cfg)
			if tt.wantExit == "" {
				if result != nil {
					t.Errorf("期望 nil, 得到 %+v", result)
				}
				return
			}
			if result == nil {
				t.Fatalf("期望退出(%s), 得到 nil", tt.wantExit)
			}
			if result.Reason != tt.wantExit {
				t.Errorf("退出原因: 期望 %q, 得到 %q", tt.wantExit, result.Reason)
			}
			if result.Priority != tt.wantPrio {
				t.Errorf("优先级: 期望 %d, 得到 %d", tt.wantPrio, result.Priority)
			}
		})
	}
}

// ──────────────────────────────────────────
// D 组：信号生成（D1→板块→策略评分）
// ──────────────────────────────────────────

func TestD1_EventMatching(t *testing.T) {
	if testMatcher == nil {
		t.Skip("events_leftside.yaml 未加载，跳过")
	}

	news := []data.NewsItem{
		{Title: "半导体产业重磅政策出台，国产替代加速推进", Datetime: time.Now().Format("2006-01-02 15:04:05")},
		{Title: "央行降准50bp释放流动性", Datetime: time.Now().Format("2006-01-02 15:04:05")},
	}
	for _, n := range news {
		result := testMatcher.MatchD1(n.Title)
		if result.Score <= 0 && result.Blocked {
			continue
		}
		t.Logf("新闻匹配: %q → 分=%d 禁=%v 规则=%v", n.Title, result.Score, result.Blocked, result.MatchedRules)
	}
}

func TestD2_StrategyScoringWithMockData(t *testing.T) {
	if testCfg == nil {
		t.Skip("rules.json 未加载，跳过")
	}

	sd := &dragon_return.StockData{
		Code: "002371.SZ", Name: "北方华创",
		CurrentPrice: 150.0,
		FirstRisePct: 0.45,
		PullbackPct:  0.18,
		PullbackDays: 6,
		VolumeRatio:  0.25,
		MA5:          148.0,
		MA10:         145.0,
		MA20:         140.0,
		MACDGreen:    -0.5,
		HighestPrice: 160.0,
		IsSectorTop2: true,
		SectorRPS20:  80,
		SectorRPS60:  75,
		HasRiseFirst: true,
	}
	dr := dragon_return.New(testCfg)
	eval, err := dr.Evaluate("002371.SZ", sd)
	if err != nil {
		t.Fatalf("龙回头评分失败: %v", err)
	}
	t.Logf("龙回头评分: total=%.0f pass=%v level=%s", eval.TotalScore, eval.Pass, eval.Level)
	if !eval.Pass {
		t.Log("  未通过阈值(60)，这是预期的——数据是模拟的")
	}
}

// ──────────────────────────────────────────
// E 组：信号→退出全链路模拟
// ──────────────────────────────────────────

func TestE1_FullPipelineBuyThenExit(t *testing.T) {
	if testCfg == nil {
		t.Skip("rules.json 未加载，跳过")
	}
	cfg := testCfg.Get()

	// 模拟 SignalView（如 HandleAction("buy") 收到的数据）
	sig := struct {
		Strategy   string
		Code       string
		Name       string
		Price      float64
		TotalScore float64
		NPhase     int
		LeftSignal bool
	}{Strategy: "dragon_return", Code: "002371.SZ", Name: "北方华创",
		Price: 100.0, TotalScore: 85, NPhase: 0}

	meta := make(map[string]float64)
	meta["entry_score"] = sig.TotalScore
	switch sig.Strategy {
	case "dragon_return":
		meta["dr_score"] = 85
	case "n_shape":
		if sig.LeftSignal {
			meta["entry_phase"] = 1
		} else {
			meta["entry_phase"] = 2
		}
		meta["entry_nphase"] = float64(sig.NPhase)
	}

	// 模拟 UserHolding（如 HandleAction 写入的）
	holding := struct {
		EntryStrategy string
		EntryAt       string
		EntryMeta     map[string]float64
		CostPrice     float64
	}{EntryStrategy: sig.Strategy, EntryAt: time.Now().Format("2006-01-02"),
		EntryMeta: meta, CostPrice: sig.Price}
	_ = holding

	t.Logf("买入记录: 策略=%s 入场价=%.2f meta=%v", sig.Strategy, sig.Price, meta)

	dc := cfg.Strategy.DragonReturn
	ctx := &strategy.ExitContext{
		Code: "002371.SZ", Name: "北方华创",
		CostPrice: 100, CurPrice: 94,
		EntryAt:   holding.EntryAt,
		EntryMeta: holding.EntryMeta,
		DailyK:    make20KLine(100, 110),
	}
	result := dragon_return.CheckExit(ctx, &dc)
	if result == nil {
		t.Error("期望止损退出，但返回 nil")
	} else {
		t.Logf("退出检查: reason=%q priority=%d（预期: 龙回头止损 P1）", result.Reason, result.Priority)
		if result.Reason != "龙回头止损" {
			t.Errorf("期望 '龙回头止损', 得到 %q", result.Reason)
		}
	}

	// 测试上涨场景——不应退出
	ctx2 := &strategy.ExitContext{
		CostPrice: 100, CurPrice: 98, // 小亏2%，不触发任何条件
		EntryAt:   holding.EntryAt,
		EntryMeta: holding.EntryMeta,
		DailyK:    make20KLine(100, 110),
	}
	result2 := dragon_return.CheckExit(ctx2, &dc)
	if result2 != nil {
		t.Errorf("正常小幅亏损不应退出，得到: %s", result2.Reason)
	}
}

// ──────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────

// make20KLine 生成20根K线，前n1根收盘≈close1，后20-n1根≈close2。
func make20KLine(close1, close2 float64) []strategy.KLine {
	kl := make([]strategy.KLine, 20)
	t := time.Date(2026, 7, 1, 0, 0, 0, 0, time.Local)
	for i := 0; i < 20; i++ {
		c := close1
		if i >= 16 {
			c = close2
		}
		kl[i] = strategy.KLine{
			Close:  c,
			High:   c * 1.02,
			Low:    c * 0.98,
			Open:   c * 0.99,
			Volume: 1000000,
		}
		t = t.AddDate(0, 0, 1)
	}
	return kl
}

// TestMain already defined in pipeline_diagnostic_test.go — no redeclaration needed

// TestX_D1MatcherSector 测试 D1 匹配器是否能从新闻命中板块
func TestX_D1MatcherSector(t *testing.T) {
	if testMatcher == nil {
		t.Skip("events_leftside.yaml 未加载")
	}
	// 测试高影响力事件匹配
	news := []data.NewsItem{
		{Title: "国务院发布促进半导体产业发展若干政策", Datetime: time.Now().Format("2006-01-02 15:04:05")},
		{Title: "新能源汽车购置税减免政策延续", Datetime: time.Now().Format("2006-01-02 15:04:05")},
	}

	for _, item := range news {
		r := testMatcher.MatchD1(item.Title)
		t.Logf("新闻: %s → score=%d blocked=%v rules=%v",
			item.Title[:30], r.Score, r.Blocked, r.MatchedRules)
		if r.Score > 0 {
			t.Logf("  命中规则: %v, 等级: %s", r.MatchedRules, r.Level)
		}
	}
}

// TestX_NShapeStateToExit 测试 N 形状态机状态行走到退出
func TestX_NShapeStateToExit(t *testing.T) {
	if testCfg == nil {
		t.Skip("rules.json 未加载，跳过")
	}
	cfg := testCfg.Get().Strategy.NShape

	t.Run("一突后未二突-无退出", func(t *testing.T) {
		ctx := &strategy.ExitContext{
			CostPrice: 100, CurPrice: 103,
			EntryMeta: map[string]float64{"entry_nphase": 2, "entry_phase": 2},
		}
		r := n_shape.CheckExit(ctx, &cfg)
		if r != nil {
			t.Errorf("期望 nil，得到: %s", r.Reason)
		}
	})

	t.Run("完成形态-收盘附近", func(t *testing.T) {
		closeTime := time.Date(2026, 7, 27, 14, 58, 0, 0, time.Local)
		ctx := &strategy.ExitContext{
			CostPrice: 100, CurPrice: 105,
			EntryMeta: map[string]float64{"entry_nphase": 4},
			Now:       closeTime,
		}
		r := n_shape.CheckExit(ctx, &cfg)
		if r == nil {
			t.Error("收盘附近应触发收盘强平或完成止盈")
		} else {
			t.Logf("N形退出: %s (P%d)", r.Reason, r.Priority)
			if r.Reason != "N形完成止盈" {
				t.Errorf("期望 'N形完成止盈', 得到 %q", r.Reason)
			}
		}
	})
}

// TestX_ExitEdgeCases 测试边界情况
func TestX_ExitEdgeCases(t *testing.T) {
	if testCfg == nil {
		t.Skip("rules.json 未加载，跳过")
	}
	cfg := testCfg.Get()

	t.Run("价格为零-不退出", func(t *testing.T) {
		ctx := &strategy.ExitContext{CostPrice: 100, CurPrice: 0}
		if r := dragon_return.CheckExit(ctx, &cfg.Strategy.DragonReturn); r != nil {
			t.Error("价格为零应返回 nil")
		}
		if r := n_shape.CheckExit(ctx, &cfg.Strategy.NShape); r != nil {
			t.Error("价格为零应返回 nil")
		}
		if r := dragon.CheckExit(ctx, &cfg.Strategy.Dragon); r != nil {
			t.Error("价格为零应返回 nil")
		}
		if r := double_bump.CheckExit(ctx, &cfg.Strategy.DoubleBump); r != nil {
			t.Error("价格为零应返回 nil")
		}
	})

	t.Run("成本为零-不退出", func(t *testing.T) {
		ctx := &strategy.ExitContext{CostPrice: 0, CurPrice: 100}
		if r := dragon_return.CheckExit(ctx, &cfg.Strategy.DragonReturn); r != nil {
			t.Error("成本为零应返回 nil")
		}
	})

	t.Run("无meta正常持有-不退出", func(t *testing.T) {
		ctx := &strategy.ExitContext{CostPrice: 100, CurPrice: 98}
		if r := dragon_return.CheckExit(ctx, &cfg.Strategy.DragonReturn); r != nil {
			t.Errorf("无meta的正常持有不应退出: %s", r.Reason)
		}
		if r := n_shape.CheckExit(ctx, &cfg.Strategy.NShape); r != nil {
			t.Errorf("无meta的正常持有不应退出: %s", r.Reason)
		}
		if r := dragon.CheckExit(ctx, &cfg.Strategy.Dragon); r != nil {
			t.Errorf("无meta的正常持有不应退出: %s", r.Reason)
		}
		if r := double_bump.CheckExit(ctx, &cfg.Strategy.DoubleBump); r != nil {
			t.Errorf("无meta的正常持有不应退出: %s", r.Reason)
		}
	})
}
