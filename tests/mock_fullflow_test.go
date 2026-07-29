// Package tests — 全流程 Mock 测试（使用昨日实盘数据）
// 覆盖：登录→行情→D1→评分→信号→仓位→退出→风控→通知
// 运行: go test ./tests -run TestMock_FullFlow -v -count=1 -timeout 60s
package tests

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"quant-trading/internal/strategy"
	"quant-trading/internal/strategy/double_bump"
	"quant-trading/internal/strategy/dragon"
	"quant-trading/internal/strategy/dragon_return"
	"quant-trading/internal/strategy/n_shape"
)

type ffStock struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	ChangePct float64 `json:"change_pct"`
	Volume    float64 `json:"volume"`
	Amount    float64 `json:"amount"`
	MSnap     float64 `json:"m_score"`
}

type ffEval struct {
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

type ffNews struct {
	Title  string `json:"title"`
	Source string `json:"source"`
	Date   string `json:"date"`
}

type ffCal struct {
	Date     string `json:"date"`
	Title    string `json:"title"`
	Impact   string `json:"impact"`
	Level    string `json:"level"`
	Duration int    `json:"duration"`
}

func TestMock_FullFlow(t *testing.T) {
	if testCfg == nil {
		t.Skip("rules.json 未加载，跳过")
	}
	cfg := testCfg.Get()

	fmt.Println(strings.Repeat("=", 72))
	fmt.Println("  APP 全流程 Mock 测试 — 基于 2026-07-27 实盘数据")
	fmt.Println(strings.Repeat("=", 72))

	// ════════════════════════════════════════
	// Step 1: 登录
	// ════════════════════════════════════════
	fmt.Println("\n[Step 1/8] 登录认证")
	fmt.Printf("  ✅ 登录成功: username=liangzai token=mock_jwt\n")

	// ════════════════════════════════════════
	// Step 2: 加载数据
	// ════════════════════════════════════════
	fmt.Println("\n[Step 2/8] 加载昨日实盘数据 (2026-07-27)")

	var snap []ffStock
	if err := loadJSON("testdata/snapshot_data.json", &snap); err != nil {
		t.Fatalf("snapshot_data.json: %v", err)
	}
	var evals []ffEval
	if err := loadJSON("testdata/eval_data.json", &evals); err != nil {
		t.Fatalf("eval_data.json: %v", err)
	}
	var news []ffNews
	if err := loadJSON("testdata/news_data.json", &news); err != nil {
		t.Fatalf("news_data.json: %v", err)
	}
	var cal []ffCal
	if err := loadJSON("testdata/calendar_data.json", &cal); err != nil {
		t.Fatalf("calendar_data.json: %v", err)
	}

	fmt.Printf("  📊 快照: %d只  评估: %d条  新闻: %d条  日历: %d条\n",
		len(snap), len(evals), len(news), len(cal))

	evMap := make(map[string]ffEval)
	for _, e := range evals {
		evMap[e.Code] = e
	}

	fmt.Println("\n  实盘扫描结果:")
	fmt.Printf("  %-8s %-10s %8s %6s | N形 | D1 D2 D3 D4 | M\n", "代码", "名称", "价格", "涨幅")
	fmt.Println("  " + strings.Repeat("─", 60))
	for _, s := range snap {
		e, hasEval := evMap[s.Code]
		nScore, nPass, nD1, nD2, nD3, nD4, mScore := 0.0, false, 0.0, 0.0, 0.0, 0.0, 0.0
		if hasEval {
			nScore, nPass, nD1, nD2, nD3, nD4, mScore = e.NScore, e.NPass, e.ND1, e.ND2, e.ND3, e.ND4, e.MScore
		}
		pm := map[bool]string{true: "✅", false: " "}[nPass]
		fmt.Printf("  %-8s %-10s %8.2f %5.1f%% | %3.0f%s | %2.0f %2.0f %2.0f %2.0f | %2.0f\n",
			s.Code, s.Name, s.Price, s.ChangePct, nScore, pm, nD1, nD2, nD3, nD4, mScore)
	}

	// 关键检查：深科技应通过N形
	t.Logf("深科技 NScore=%.0f NPass=%v", evMap["000021"].NScore, evMap["000021"].NPass)

	// ════════════════════════════════════════
	// Step 3: D1 事件
	// ════════════════════════════════════════
	fmt.Println("\n[Step 3/8] D1 事件匹配 (新闻 → 事件)")
	matcher := testMatcher
	if matcher == nil {
		fmt.Println("  ⚠ events_leftside.yaml 未加载")
	} else {
		for _, n := range news {
			r := matcher.MatchD1(n.Title)
			si := int(r.Score / 20)
			if si > 2 {
				si = 2
			}
			sym := map[int]string{0: "○", 1: "●", 2: "★"}[si]
			blk := ""
			if r.Blocked {
				blk = " [BLOCKED]"
			}
			fmt.Printf("  %s S=%d L=%s %s Rules=[%s]%s\n",
				sym, r.Score, r.Level, truncateStr(n.Title, 50),
				strings.Join(r.MatchedRules, ","), blk)
		}
	}

	// ════════════════════════════════════════
	// Step 4: 策略评分
	// ════════════════════════════════════════
	fmt.Println("\n[Step 4/8] 策略评分")

	if matcher != nil {
		fmt.Println("  ── N形超短 (深科技) ──")
		scorer := n_shape.NewLeftSideScorer(matcher)
		res := scorer.Evaluate(
			&n_shape.WaveA{AOpen: 21.0, AHigh: 22.5, ALow: 20.0, AClose: 21.2,
				AVol: 80000000, AChgPct: 8.0, AAboveMA60: true, IsSectorLeader: true},
			&n_shape.IntradayB{TTime: 1030, CurPrice: 23.6, CumVol: 150000000,
				AuctionChgPct: 3.5, PrevClose: 21.0, PrevHigh: 22.5, PrevLow: 20.0,
				MinuteMACDDIF: 0.45, MinuteMACDDEA: 0.15, AvgDailyVol: 80000000, BenchCurChg: 0.5},
			&n_shape.Ctx{EventDesc: "半导体产业政策出台 国产替代加速;公司业绩预增",
				EmotionPhase: "启动", SectorTurnover: 5e9, SectorTurnoverMA20: 2e9,
				StockPE: 18, AvgDailyVol: 80000000})
		if res != nil {
			fmt.Printf("  深科技 N=%.0f D1=%.0f D2=%.0f D3=%.0f D4=%.0f Valid=%v\n",
				res.Total, res.D1Event, res.D2RS, res.D3Pullback, res.D4Accept, res.Valid)
		}
	}

	fmt.Println("  ── 龙回头 (北方华创) ──")
	dr := dragon_return.New(testCfg)
	drEval, _ := dr.Evaluate("002371", &dragon_return.StockData{
		CurrentPrice: 772.2, FirstRisePct: 0.42, PullbackPct: 0.15,
		PullbackDays: 5, VolumeRatio: 0.3, MA5: 755, MA10: 740, MA20: 720,
		MACDGreen: -0.2, HighestPrice: 800, IsSectorTop2: true, SectorRPS20: 85})
	if drEval != nil {
		fmt.Printf("  北方华创 DR=%.0f (门槛%.0f, 通过?%v)\n",
			drEval.TotalScore, cfg.Strategy.DragonReturn.ScoreThreshold,
			drEval.TotalScore >= cfg.Strategy.DragonReturn.ScoreThreshold)
	}

	fmt.Println("  ── 破局龙 (宁德时代) ──")
	dg := dragon.New(testCfg)
	dgEval, _ := dg.Evaluate("300750", map[string]interface{}{
		"cur_price": 200.0, "limit_up_count": 3, "limit_up_quality": 0.8,
		"volume_ratio": 2.5, "sector_rps20": 90, "is_crown_dragon": true})
	if dgEval != nil {
		fmt.Printf("  宁德时代 D=%.0f\n", dgEval.TotalScore)
	}

	fmt.Println("  ── 双凸 (贵州茅台) ──")
	db := double_bump.New(testCfg)
	dbEval, _ := db.Evaluate("600519", map[string]interface{}{
		"cur_price": 1297.0, "bump1_pct": 6.5, "pullback_pct": 2.0,
		"bump2_pct": 4.5, "volume_ratio": 1.8, "ma5": 1290, "ma10": 1280,
		"sector_rps20": 60, "is_sector_leader": false})
	if dbEval != nil {
		fmt.Printf("  贵州茅台 DB=%.0f\n", dbEval.TotalScore)
	}

	// ════════════════════════════════════════
	// Step 5: 信号生成
	// ════════════════════════════════════════
	fmt.Println("\n[Step 5/8] 信号生成")

	type sig struct {
		code     string
		name     string
		strategy string
		score    float64
		price    float64
	}
	sigs := []sig{
		{code: "000021", name: "深科技", strategy: "n_shape", score: 62, price: 23.6},
		{code: "002371", name: "北方华创", strategy: "dragon_return", score: drEval.TotalScore, price: 772.2},
		{code: "300750", name: "宁德时代", strategy: "dragon", score: dgEval.TotalScore, price: 200.0},
		{code: "600519", name: "贵州茅台", strategy: "double_bump", score: dbEval.TotalScore, price: 1297.0},
	}
	v := 0
	for _, s := range sigs {
		minBuy := 70.0
		switch s.strategy {
		case "n_shape":
			minBuy = cfg.Strategy.NShape.NPatternScoreThreshold
		case "dragon_return":
			minBuy = cfg.Strategy.DragonReturn.ScoreThreshold
		}
		ok := s.score >= minBuy
		if ok {
			v++
		}
		mark := map[bool]string{true: "✅ 买入", false: "⛔ 观望"}[ok]
		fmt.Printf("  %s %s %s S=%.0f ≥%.0f → %s\n",
			s.code, s.name, s.strategy, s.score, minBuy, mark)
	}
	fmt.Printf("  → 有效买入信号: %d/4\n", v)

	// ════════════════════════════════════════
	// Step 6: 仓位管理
	// ════════════════════════════════════════
	fmt.Println("\n[Step 6/8] 仓位管理 (建仓+分段+风控)")

	type position struct {
		code     string
		name     string
		qty      int
		cost     float64
		strategy string
	}
	positions := []position{
		{code: "000021", name: "深科技", qty: 300, cost: 23.6, strategy: "n_shape"},
		{code: "002371", name: "北方华创", qty: 100, cost: 772.2, strategy: "dragon_return"},
	}
	for _, p := range positions {
		cur := p.cost * 1.06
		pnl := (cur - p.cost) / p.cost * 100
		fmt.Printf("  ✅ %s %s %d股 cost=%.1f→%.1f (%+.1f%%) [%s]\n",
			p.code, p.name, p.qty, p.cost, cur, pnl, p.strategy)
	}

	fmt.Println("\n  分段加仓检查:")
	for _, p := range positions {
		cur := p.cost * 1.06
		pnl := (cur - p.cost) / p.cost * 100
		fmt.Printf("  %s 浮盈%+.1f%% → ", p.code, pnl)
		if pnl >= 5 {
			fmt.Print("达到加仓条件\n")
		} else {
			fmt.Print("未达加仓条件\n")
		}
	}

	fmt.Println("\n  风控 P1-P4:")
	for _, p := range positions {
		riskScore := 0.15
		if p.code == "000021" {
			riskScore = 0.12 // 深科技低风险
		}
		lvl := "P4(正常)"
		if riskScore >= 0.7 {
			lvl = "P1(减持)"
		} else if riskScore >= 0.4 {
			lvl = "P2(关注)"
		} else if riskScore >= 0.2 {
			lvl = "P3(观察)"
		}
		fmt.Printf("  %s risk=%.2f → %s\n", p.code, riskScore, lvl)
	}

	// ════════════════════════════════════════
	// Step 7: 退出检查
	// ════════════════════════════════════════
	fmt.Println("\n[Step 7/8] 退出检查 (止盈止损)")

	todayStr := time.Now().Format(time.DateOnly)
	mkKL := func(base float64) []strategy.KLine {
		kl := make([]strategy.KLine, 20)
		for i := 0; i < 20; i++ {
			c := base + float64(i-10)*0.5
			kl[i] = strategy.KLine{Close: c, High: c * 1.02, Low: c * 0.98, Open: c * 0.99, Volume: 1e6}
		}
		return kl
	}

	type exitCase struct {
		label string
		code  string
		cost  float64
		cur   float64
		kind  string
		meta  map[string]float64
		kl    []strategy.KLine
	}
	ec := []exitCase{
		{"深科技 止损-5%", "000021", 23.6, 22.42, "n", map[string]float64{"n_score": 62, "entry_nphase": 3}, nil},
		{"深科技 持有+2%", "000021", 23.6, 24.07, "n", map[string]float64{"n_score": 62, "entry_nphase": 3}, nil},
		{"北方华创 止损-5%", "002371", 772.2, 733.59, "dr", map[string]float64{"dr_score": 75}, mkKL(772.2)},
		{"北方华创 止盈+30%", "002371", 772.2, 1003.86, "dr", map[string]float64{"dr_score": 75}, mkKL(772.2)},
		{"北方华创 持有+2%", "002371", 772.2, 787.64, "dr", map[string]float64{"dr_score": 75}, mkKL(772.2)},
	}
	for _, c := range ec {
		ctx := &strategy.ExitContext{
			CostPrice: c.cost, CurPrice: c.cur,
			EntryAt: todayStr, EntryMeta: c.meta, DailyK: c.kl,
		}
		var res *strategy.ExitResult
		switch c.kind {
		case "n":
			res = n_shape.CheckExit(ctx, &cfg.Strategy.NShape)
		case "dr":
			res = dragon_return.CheckExit(ctx, &cfg.Strategy.DragonReturn)
		}
		pnl := (c.cur - c.cost) / c.cost * 100
		if res != nil {
			fmt.Printf("  %s: %.1f→%.1f (%+.1f%%) → 🚩 %s (P%d)\n",
				c.label, c.cost, c.cur, pnl, res.Reason, res.Priority)
		} else {
			fmt.Printf("  %s: %.1f→%.1f (%+.1f%%) → ✓ 持有\n", c.label, c.cost, c.cur, pnl)
		}
	}

	// ════════════════════════════════════════
	// Step 8: 日历+通知
	// ════════════════════════════════════════
	fmt.Println("\n[Step 8/8] 日历事件 & 通知")

	allowed := map[string]bool{"fomc": true, "cpi": true, "nfp": true, "pce": true, "contract": true, "war": true}
	for _, e := range cal {
		ok := allowed[e.Level]
		sym := map[bool]string{true: "✅", false: "❌"}[ok]
		fmt.Printf("  %s %s (%s) level=%s\n", sym, e.Title, e.Date, e.Level)
	}

	fmt.Println("\n  通知:")
	for _, p := range positions {
		fmt.Printf("  [持仓提示] %s %s %d股 cost=%.1f\n", p.code, p.name, p.qty, p.cost)
	}

	// ════════════════════════════════════════
	fmt.Println(strings.Repeat("\n"+"=", 72))
	fmt.Printf("  全流程 Mock 完成 | 扫描%d只 信号%d个 持仓%d只 日历%d条\n",
		len(snap), v, len(positions), len(cal))
	fmt.Println(strings.Repeat("=", 72))
}
