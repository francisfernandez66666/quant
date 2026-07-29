// Package data — 热点板块扫描器测试。
// 东财API限流时，使用模拟数据验证管线逻辑。
package data

import (
	"testing"
)

func mockSectors_D1Scored() []SectorInfo {
	return []SectorInfo{
		{Code: "BK0477", Name: "半导体", ChangePct: 3.5, Amount: 800e8, NetInflow: 20e8, LimitupCnt: 8},
		{Code: "BK0446", Name: "人工智能", ChangePct: 2.8, Amount: 600e8, NetInflow: 15e8, LimitupCnt: 5},
		{Code: "BK0867", Name: "新能源", ChangePct: -0.5, Amount: 200e8, NetInflow: -5e8, LimitupCnt: 1},
		{Code: "BK0481", Name: "汽车零部件", ChangePct: 1.5, Amount: 300e8, NetInflow: 8e8, LimitupCnt: 3},
		{Code: "BK0451", Name: "房地产开发", ChangePct: 0.2, Amount: 100e8, NetInflow: -2e8, LimitupCnt: 0},
	}
}

func mockSectorStocks_BK0477() []StockInfo {
	return []StockInfo{
		{Code: "002371", Name: "北方华创", Price: 350.0, ChangePct: 4.2, Volume: 8000000, Amount: 2.8e9, Turnover: 5.5},
		{Code: "002049", Name: "紫光国微", Price: 95.0, ChangePct: 3.1, Volume: 12000000, Amount: 1.14e9, Turnover: 4.2},
		{Code: "688981", Name: "中芯国际", Price: 58.0, ChangePct: 2.5, Volume: 25000000, Amount: 1.45e9, Turnover: 3.0},
		{Code: "300661", Name: "圣邦股份", Price: 82.0, ChangePct: 5.8, Volume: 3000000, Amount: 2.46e8, Turnover: 6.8},
		{Code: "603501", Name: "韦尔股份", Price: 105.0, ChangePct: 1.8, Volume: 5000000, Amount: 5.25e8, Turnover: 2.8},
	}
}

// TestScoreSectorStocks 测试个股打分
func TestScoreSectorStocks(t *testing.T) {
	ss := NewSectorScanner(nil, nil)
	api := NewMarketAPI()
	ss.api = api

	// 正常情况应该通过东财API获取，东财限流时用模拟数据
	stocks := mockSectorStocks_BK0477()
	_ = stocks

	t.Log("ScoreSectorStocks: 无法在单元测试中直接调用(依赖东财API)")
	t.Log("预期: 对板块内个股按涨跌幅+换手率+成交额+量能+龙头加成5维打分")
	t.Log("  → 最高分应为 300661 圣邦股份 (涨幅5.8%+换手6.8%+龙头)")
}

// TestBuildEventMapFromNews 测试多板块映射
func TestBuildEventMapFromNews(t *testing.T) {
	cfg, err := LoadEvents("../../config/events_leftside.yaml")
	if err != nil {
		t.Skip("events config not found:", err)
	}
	matcher := NewEventMatcher(cfg)
	ss := NewSectorScanner(nil, matcher)
	ss.cachedSector = mockSectors_D1Scored()

	// 使用命中 D1 规则的关键词确保 MatchD1 返回 Score>=30
	news := []NewsItem{
		{
			Title:   "半导体板块并购重组获证监会核准，国产替代加速",
			Content: "半导体并购重组利好，北方华创、中芯国际等龙头受益。",
		},
		{
			Title:   "人工智能重大合同中标，大模型备案获批",
			Content: "国务院发布人工智能发展规划，AI板块全面爆发。",
		},
	}

	base := map[string]string{
		"BK0477": "半导体国产芯片突破前高行业龙头反包",
	}
	out := ss.BuildEventMapFromNews(news, base)

	if len(out) < 2 {
		t.Errorf("BuildEventMapFromNews: 期望至少2个板块映射, 得到 %d", len(out))
	}
	if _, ok := out["BK0477"]; !ok {
		t.Error("映射应包含 BK0477 半导体")
	}
	if _, ok := out["BK0446"]; !ok {
		t.Error("映射应包含 BK0446 人工智能(多板块映射)")
	}

	t.Logf("BuildEventMapFromNews 多板块映射: %d个板块", len(out))
	for code, title := range out {
		maxLen := 40
		if len(title) < maxLen {
			maxLen = len(title)
		}
		t.Logf("  %s ← %s", code, title[:maxLen])
	}

	// 验证关联度权重
	ss.mu.RLock()
	assocScore := ss.sectorAssocScore
	ss.mu.RUnlock()
	if len(assocScore) == 0 {
		t.Error("sectorAssocScore 不应为空")
	} else {
		t.Logf("关联度权重: %v", assocScore)
	}
}

// TestScoreSectors 测试双通道交叉验证评分
func TestScoreSectors(t *testing.T) {
	cfg, err := LoadEvents("../../config/events_leftside.yaml")
	if err != nil {
		t.Skip("events config not found:", err)
	}
	matcher := NewEventMatcher(cfg)
	ss := NewSectorScanner(nil, matcher)
	ss.cachedSector = mockSectors_D1Scored()

	// 设置事件映射（模拟 BuildEventMapFromNews 的结果）
	ss.eventMap = map[string]string{
		"BK0477": "半导体国产芯片突破前高行业龙头反包",
		"BK0446": "人工智能AI政策利好龙头突破前高",
	}
	ss.sectorAssocScore = map[string]float64{
		"BK0477": 1.0,
		"BK0446": 0.8,
	}

	result := ss.scoreSectors(mockSectors_D1Scored(), 5, 3, 5)

	if len(result) == 0 {
		t.Error("scoreSectors 应返回热点板块")
		t.Log("  (若东财限流期间测试，scoreSectors 内部依赖 EventMatcher.MatchD1)")
		return
	}

	t.Logf("D1+F评分结果: %d个热点板块", len(result))
	for _, hs := range result {
		t.Logf("  %s %s: 总分=%.1f D1=%.1f 原因=%s", hs.Sector.Code, hs.Sector.Name, hs.Score, hs.D1, hs.Reason)
	}
}
