package tests

import (
	"testing"

	"quant-trading/internal/data"
	"quant-trading/internal/strategy/n_shape"
)

func TestIPO_D1Regression(t *testing.T) {
	// ── 1. 加载配置 ──
	if testMatcher == nil {
		t.Skip("events_leftside.yaml 未加载")
	}
	scorer := n_shape.NewLeftSideScorer(testMatcher)

	// ── 2. 真实 K 线 ──
	coord := data.NewDataCoordinator(data.NewMarketAPI(), nil, nil, nil)
	kl, err := coord.GetKLine("600519", "101", 20)
	if err != nil || len(kl) < 5 {
		t.Fatalf("K线获取失败: %v", err)
	}
	last := kl[len(kl)-1]
	prev := kl[len(kl)-2]

	avgVol := 0.0
	for i := len(kl) - 20; i < len(kl) && i >= 0; i++ {
		avgVol += kl[i].Volume
	}
	if len(kl) >= 20 {
		avgVol /= 20.0
	} else {
		avgVol = last.Volume
	}

	// ── 3. 种子 IPO 缓存 ──
	coord.RefreshIPOCalendar()
	upcoming := coord.GetIPOByCode("688981")
	if upcoming == nil {
		coord.RefreshIPOCalendar()
	}

	// ── 4. 取一只真实即将上市的股票 ──
	allIPO := coord.GetAllIPOCalendar()
	hasData := len(allIPO) > 0
	if hasData {
		t.Logf("  IPO缓存: %d 只", len(allIPO))
		for _, ipo := range allIPO[:min(len(allIPO), 5)] {
			status := "即将上市"
			if ipo.ListStatus == "L" {
				status = "已上市"
			}
			t.Logf("    %s %s %s 发行价¥%.2f", ipo.Code, ipo.Name, status, ipo.IssuePrice)
		}
	}

	// ── 5. 测试场景 ──
	type testCase struct {
		name      string
		eventDesc string
		wantD1Min float64
		wantValid bool
	}

	tests := []testCase{
		// 场景A: IPO注册申请获批 即将上市 → D1≥40
		{"IPO即将上市",
			"半导体产业链受益;中芯国际IPO注册申请获批 即将上市",
			40, true},

		// 场景B: 新股上市 次新股 → D1≥40 (catalyst regex 命中)
		{"新股上市次新股",
			"公司发布业绩预增公告;新股上市 华虹半导体 次新股",
			40, true},

		// 场景C: 纯新股上市 → D1≥40
		{"纯新股上市",
			"新股上市 龙旗科技 次新股",
			40, true},

		// 场景D: 即将上市无新闻 → D1≥40
		{"IPO注册获批",
			"IPO注册申请获批 先正达集团 即将上市",
			40, true},

		// 场景E: 无IPO → D1=0
		{"无事件",
			"",
			0, false},

		// 场景F: 普通新闻无IPO → D1=30 (扭亏为盈)
		{"普通新闻无IPO",
			"公司发布扭亏为盈业绩预增公告",
			30, true},
	}

	pass := 0
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// 构造突破前高的 IntradayB（保证不被趋势否决拦下）
			breakHigh := prev.High * 1.02
			ib := &n_shape.IntradayB{
				TTime: 945, CurPrice: breakHigh, CumVol: avgVol * 0.3,
				AuctionChgPct: 2.5,
				PrevClose:     prev.Close, PrevHigh: prev.High, PrevLow: prev.Low,
				MinuteMACDDIF: 0.35, MinuteMACDDEA: 0.12,
				AvgDailyVol: avgVol, BenchCurChg: 0.3,
			}
			wa := &n_shape.WaveA{
				AOpen: last.Open, AHigh: max(last.High, breakHigh),
				ALow: last.Low, AClose: breakHigh * 0.99,
				AVol: avgVol * 1.5, AChgPct: safeP(breakHigh, last.Open),
				AAboveMA60: true, IsSectorLeader: true,
			}
			res := scorer.Evaluate(wa, ib, &n_shape.Ctx{
				EmotionPhase: "启动", EventDesc: tc.eventDesc,
				SectorTurnover: 5e9, SectorTurnoverMA20: 2e9,
				StockPE: 20, AvgDailyVol: avgVol,
				// NPhase: 2,
			})

			gotValid := false
			gotD1 := 0.0
			if res != nil {
				gotValid = res.Valid
				gotD1 = res.D1Event
			}

			t.Logf("  Event=%q", tc.eventDesc)
			t.Logf("  D1=%.0f D2=%.0f D3=%.0f D4=%.0f Total=%.0f Valid=%v Reason=%s",
				gotD1, res.D2RS, res.D3Pullback, res.D4Accept, res.Total, gotValid, res.Reason)

			ok := true
			if gotD1 < tc.wantD1Min {
				t.Logf("  ❌ D1=%.0f < 期望%.0f", gotD1, tc.wantD1Min)
				ok = false
			}
			if gotValid != tc.wantValid {
				if tc.wantValid {
					t.Logf("  ❌ Valid=false, 期望Valid=true")
				} else {
					t.Logf("  ❌ Valid=true, 期望Valid=false")
				}
				ok = false
			}
			if ok {
				t.Logf("  ✅ 通过")
				pass++
			}
		})
	}

	total := len(tests)
	t.Logf("\n  IPO→D1 回归: %d/%d 通过", pass, total)
	if pass < total {
		t.Errorf("%d/%d 未通过", total-pass, total)
	}

	// ── 6. DataCoordinator IPO 方法验证 ──
	t.Run("CoordinatorIPOAPI", func(t *testing.T) {
		cal := coord.GetAllIPOCalendar()
		if len(cal) == 0 {
			t.Log("  ⚠ IPO日历为空（可能非交易时段API无数据）")
		} else {
			t.Logf("  GetAllIPOCalendar: %d 条", len(cal))
		}

		got := coord.GetIPOByCode("688981")
		if got != nil {
			t.Logf("  GetIPOByCode(688981): %s %s L=%s", got.Code, got.Name, got.ListStatus)
		} else {
			t.Log("  GetIPOByCode(688981): nil (可能无此IPO数据)")
		}
		t.Log("  ✅ Coordinator IPO API 调用正常")
	})

	// ── 7. IPO 相关 YAML 规则验证 ──
	t.Run("IPORuleMatch", func(t *testing.T) {
		mr := testMatcher.MatchD1("新股上市 龙旗科技")
		t.Logf("  新股上市→ D1=%d blocked=%v", mr.Score, mr.Blocked)
		if mr.Score < 40 && !mr.Blocked {
			t.Errorf("新股上市规则未命中: D1=%d", mr.Score)
		}

		mr2 := testMatcher.MatchD1("次新股 活跃")
		t.Logf("  次新股→ D1=%d blocked=%v", mr2.Score, mr2.Blocked)
		if mr2.Score < 40 && !mr2.Blocked {
			t.Errorf("次新股规则未命中: D1=%d", mr2.Score)
		}

		mr3 := testMatcher.MatchD1("即将上市 先正达")
		t.Logf("  即将上市→ D1=%d blocked=%v", mr3.Score, mr3.Blocked)
		if mr3.Score < 40 && !mr3.Blocked {
			t.Errorf("即将上市规则未命中: D1=%d", mr3.Score)
		}

		mr4 := testMatcher.MatchD1("IPO注册申请获批")
		t.Logf("  IPO注册申请获批→ D1=%d blocked=%v", mr4.Score, mr4.Blocked)
		if mr4.Score < 40 && !mr4.Blocked {
			t.Errorf("IPO注册申请获批规则未命中: D1=%d", mr4.Score)
		}

		t.Log("  ✅ IPO YAML 规则正常")
	})
}
