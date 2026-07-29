// d1_e2e — D1 LLM 管线端到端测试。
// 使用今日实盘数据构造结构化输入 → 调用 LLM → 解析响应 → 映射个股 D1 分。
// 实盘数据源不可用时自动降级为 mock 数据。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"quant-trading/internal/calendar"
	"quant-trading/internal/config"
	"quant-trading/internal/data"
	"quant-trading/internal/llm"
)

func main() {
	dateStr := time.Now().Format("2006-01-02")
	fmt.Printf("═══ D1 LLM 管线 E2E 测试 ═══\n")
	fmt.Printf("日期: %s\n\n", dateStr)

	// 1. 初始化数据源
	api := data.NewMarketAPI()
	ths := data.NewTHSClient()
	tsToken := os.Getenv("TUSHARE_TOKEN")
	if tsToken == "" {
		if raw, err := os.ReadFile("config/secrets.json"); err == nil {
			var sec struct {
				Tushare string `json:"tushare_token"`
			}
			if json.Unmarshal(raw, &sec) == nil && sec.Tushare != "" {
				tsToken = sec.Tushare
			}
		}
	}
	var tc *data.TushareClient
	if tsToken != "" {
		tc = data.NewTushareClient(tsToken)
	}
	coord := data.NewDataCoordinator(api, tc, ths, nil)

	// 2. 宏观日历
	var macroCal *calendar.Calendar
	if raw, err := os.ReadFile("config/rules.json"); err == nil {
		var cfg struct {
			Calendar struct {
				Events []config.CalendarEvent `json:"events"`
			} `json:"calendar"`
		}
		if json.Unmarshal(raw, &cfg) == nil {
			macroCal = calendar.New(cfg.Calendar.Events)
		}
	}

	// 3. 尝试获取实盘数据，失败则用 mock
	fmt.Println("── 今日实盘数据 ──")

	// IPO 日历
	ipoAll := coord.GetAllIPOCalendar()
	var ipoLines []string
	if len(ipoAll) > 0 {
		fmt.Printf("IPO日历(实盘): %d 条\n", len(ipoAll))
		for _, ev := range ipoAll[:minInt(len(ipoAll), 3)] {
			line := fmt.Sprintf("%s(%s)", ev.Name, ev.Code)
			fmt.Printf("  %s 申购%s\n", line, ev.IPODate)
			ipoLines = append(ipoLines, line)
		}
	} else {
		fmt.Println("IPO日历(实盘): 无数据，使用 mock")
		// mock: 典型周一的 IPO 数据
		ipoLines = []string{
			"芯动联科(688582)",
			"广钢气体(688548)",
			"蓝箭电子(301348)",
		}
		for _, l := range ipoLines {
			fmt.Printf("  %s (mock)\n", l)
		}
	}

	// 宏观日历（未来1天）
	var macroLines []string
	if macroCal != nil {
		events := macroCal.UpcomingEvents(1)
		if len(events) > 0 {
			fmt.Printf("宏观日历(实盘): %d 条\n", len(events))
			for _, ev := range events {
				line := fmt.Sprintf("%s(%s)", ev.Title, ev.Impact)
				fmt.Printf("  %s %s\n", line, ev.Date.Format("01-02"))
				macroLines = append(macroLines, line)
			}
		}
	}
	if len(macroLines) == 0 {
		fmt.Println("宏观日历: 无今日事件，使用 mock")
		macroLines = []string{
			"中国PMI数据发布(high)",
			"科创板半年报密集披露(medium)",
		}
		for _, l := range macroLines {
			fmt.Printf("  %s (mock)\n", l)
		}
	}

	// 热点板块
	var sectorNames []string
	sectors, err := coord.GetSectors()
	if err == nil && len(sectors) > 0 {
		fmt.Printf("热点板块(实盘): %d 个\n", len(sectors))
		for i := 0; i < minInt(len(sectors), 10); i++ {
			s := sectors[i]
			if s.ChangePct != 0 {
				fmt.Printf("  %s %s  %+.2f%%\n", s.Code, s.Name, s.ChangePct)
			} else {
				fmt.Printf("  %s %s\n", s.Code, s.Name)
			}
		}
		topN := minInt(len(sectors), 20)
		for i := 0; i < topN; i++ {
			sectorNames = append(sectorNames, sectors[i].Name)
		}
	} else {
		fmt.Println("热点板块(实盘): 无数据，使用 mock")
		sectorNames = []string{
			"半导体", "新能源车", "AI算力", "低空经济",
			"机器人", "消费电子", "固态电池", "光模块",
			"国产芯片", "光伏", "储能", "创新药",
			"汽车零部件", "军工航天", "数据要素", "华为概念",
			"稀土永磁", "工业母机", "CPO", "智能驾驶",
		}
		for _, n := range sectorNames[:10] {
			fmt.Printf("  %s (mock)\n", n)
		}
	}

	// 4. 构造 LLM 输入
	fmt.Println("\n── 结构化 LLM 输入 ──")
	var parts []string
	if len(ipoLines) > 0 {
		parts = append(parts, "IPO日历: "+strings.Join(ipoLines, "; "))
	}
	if len(macroLines) > 0 {
		parts = append(parts, "宏观日历: "+strings.Join(macroLines, "; "))
	}
	if len(sectorNames) > 0 {
		parts = append(parts, "热点板块: "+strings.Join(sectorNames, "/"))
	}
	// 附加 prompt 指明任务
	prompt := fmt.Sprintf(`今天是%s。你是A股量化投研助手。分析以下盘前数据，输出最重要的3-5个投资主题。

【输入数据】
%s

【输出要求】
严格按JSON数组格式,每个元素:
{
  "title": "主题名称",
  "score": 0.0-1.0,
  "direction": "利好|利空|中性",
  "event_type": "政策|行业趋势|技术突破|事件驱动",
  "sectors": ["关联板块名称"],
  "stocks": [],
  "reason": "一句话理由"
}
只输出JSON数组。`, dateStr, strings.Join(parts, "\n"))

	fmt.Println(prompt)

	// 5. 调用 LLM
	fmt.Println("\n── LLM 分析 ──")
	apiKey := os.Getenv("LLM_API_KEY")
	if apiKey == "" {
		data, err := os.ReadFile("config/secrets.json")
		if err == nil {
			var sec struct {
				LlmToken string `json:"llm_token"`
			}
			if json.Unmarshal(data, &sec) == nil && sec.LlmToken != "" {
				apiKey = sec.LlmToken
			}
		}
	}
	if apiKey == "" {
		fmt.Println("❌ 未找到 LLM_API_KEY")
		os.Exit(1)
	}

	model := "THUDM/GLM-Z1-9B-0414"
	if m := os.Getenv("LLM_MODEL"); m != "" {
		model = m
	}

	client := llm.New(apiKey, model)
	start := time.Now()
	resp, err := client.Chat(prompt, "")
	elapsed := time.Since(start)
	if err != nil {
		fmt.Printf("❌ LLM 调用失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("⏱ %v  返回 %d 字符\n\n", elapsed, len(resp))

	resp = cleanJSON(resp)
	fmt.Println("【LLM 原始输出】")
	fmt.Println(truncate(resp, 800))

	// 6. 解析 JSON
	var topics []struct {
		Title     string   `json:"title"`
		Score     float64  `json:"score"`
		Direction string   `json:"direction"`
		EventType string   `json:"event_type"`
		Sectors   []string `json:"sectors"`
		Stocks    []string `json:"stocks"`
		Reason    string   `json:"reason"`
	}
	if err := json.Unmarshal([]byte(resp), &topics); err != nil {
		fmt.Printf("\n❌ JSON 解析失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\n✅ 解析成功: %d 个主题\n", len(topics))

	// 7. 逐主题展示
	for i, t := range topics {
		fmt.Printf("\n── [%d] %s ──\n", i+1, t.Title)
		fmt.Printf("   Score=%.2f Dir=%s Type=%s\n", t.Score, t.Direction, t.EventType)
		if len(t.Sectors) > 0 {
			fmt.Printf("   板块: %s\n", strings.Join(t.Sectors, ", "))
		}
		fmt.Printf("   理由: %s\n", truncate(t.Reason, 100))
	}

	// 8. 映射到个股 D1 分
	fmt.Println("\n═════════════ 个股 D1 评分矩阵 ═════════════")

	// 板块→个股映射表（硬编码，用于测试 LLM 板块到个股的映射通道）
	// 映射逻辑：LLM 输出板块名 → 展开该板块的个股列表 → 评分
	sectorStocks := map[string][]string{
		"半导体":   {"002371.SZ", "688981.SH", "688012.SH", "603501.SH", "600584.SH"},
		"国产芯片":  {"002371.SZ", "688981.SH", "688012.SH"},
		"AI算力":  {"603019.SH", "000977.SZ"},
		"光模块":   {"300308.SZ", "300502.SZ"},
		"新能源车":  {"002594.SZ", "300750.SZ", "002460.SZ"},
		"储能":    {"300274.SZ", "002074.SZ"},
		"固态电池":  {"300750.SZ", "002460.SZ", "300769.SZ"},
		"机器人":   {"002747.SZ", "300124.SZ", "688017.SH"},
		"低空经济":  {"000768.SZ", "600879.SH"},
		"工业母机":  {"000988.SZ"},
		"消费电子":  {"002475.SZ", "000725.SZ", "002241.SZ"},
		"光伏":    {"601012.SH", "600438.SH"},
		"汽车零部件": {"600741.SH", "002048.SZ"},
		"创新药":   {"600276.SH", "300760.SZ"},
		"军工航天":  {"600760.SH", "600893.SH"},
		"数据要素":  {"000977.SZ"},
		"华为概念":  {"002049.SZ", "300308.SZ"},
		"稀土永磁":  {"600010.SH"},
		"CPO":   {"300308.SZ", "300502.SZ"},
		"智能驾驶":  {"002920.SZ", "600745.SH"},
		"锂电":    {"300750.SZ", "002460.SZ", "002074.SZ"},
	}

	type stockD1 struct {
		name  string
		score float64
		dir   string
		src   string
	}
	stockMap := make(map[string]*stockD1)
	blockedMap := make(map[string]string)
	matchCount := 0

	for _, t := range topics {
		if len(t.Sectors) == 0 {
			continue
		}
		affected := make(map[string]bool)
		for _, secField := range t.Sectors {
			// LLM 有时用 "/" 拼接多个板块，如 "半导体/国产芯片/AI算力/光模块"
			for _, sec := range strings.Split(secField, "/") {
				sec = strings.TrimSpace(sec)
				if codes, ok := sectorStocks[sec]; ok {
					for _, code := range codes {
						affected[code] = true
					}
				} else {
					fmt.Printf("  未知板块名: \"%s\" (可用板块: ", sec)
					known := make([]string, 0, len(sectorStocks))
					for k := range sectorStocks {
						known = append(known, k)
					}
					fmt.Printf("%s)\n", strings.Join(known, ", "))
				}
			}
		}
		if len(affected) == 0 {
			fmt.Printf("  主题[%s] → 无代码匹配 (板块: %s)\n", t.Title, strings.Join(t.Sectors, ","))
			continue
		}
		matchCount++

		baseScore := t.Score
		if baseScore <= 0 {
			continue
		}

		dir := t.Direction
		// 规范化 LLM 输出的 direction（"中性（需结合数据解读）" → "中性"）
		if strings.HasPrefix(dir, "利空") {
			dir = "利空"
		} else if strings.HasPrefix(dir, "中性") {
			dir = "中性"
		} else {
			dir = "利好"
		}

		switch dir {
		case "利空":
			for code := range affected {
				blockedMap[code] = t.Title
				delete(stockMap, code)
			}
		case "中性":
			half := baseScore * 0.5
			for code := range affected {
				if _, blocked := blockedMap[code]; blocked {
					continue
				}
				if existing, ok := stockMap[code]; !ok || half > existing.score {
					stockMap[code] = &stockD1{name: stockName(code), score: half, dir: "中性", src: t.Title}
				}
			}
		default: // 利好
			for code := range affected {
				if _, blocked := blockedMap[code]; blocked {
					continue
				}
				if existing, ok := stockMap[code]; !ok || baseScore > existing.score {
					stockMap[code] = &stockD1{name: stockName(code), score: baseScore, dir: "利好", src: t.Title}
				}
			}
		}
	}

	if matchCount == 0 {
		fmt.Println("\n⚠ 所有主题均无代码匹配。检查 sectorStocks 映射表是否覆盖了 LLM 输出的板块。")
	} else {
		fmt.Printf("\n── 利好: %d 只 ──\n", len(stockMap))
		for code, d1 := range stockMap {
			fmt.Printf("  %s %-6s  D1=%5.1f/40  %s  via: [%s]\n", code, d1.name, d1.score*40, d1.dir, d1.src)
		}
	}

	fmt.Printf("\n── 利空阻塞: %d 只 ──\n", len(blockedMap))
	for code, src := range blockedMap {
		fmt.Printf("  %s %-6s  BLOCKED  via: %s\n", code, stockName(code), src)
	}

	fmt.Println("\n═══════════════════════════════════════════════")
}

func stockName(code string) string {
	for n, c := range llm.StockCodeMap {
		if c == code {
			return n
		}
	}
	return "?"
}

func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	// 移除行内 // 注释
	var out strings.Builder
	inStr := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '"' && (i == 0 || s[i-1] != '\\') {
			inStr = !inStr
		}
		if !inStr && ch == '/' && i+1 < len(s) && s[i+1] == '/' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		out.WriteByte(ch)
	}
	return out.String()
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
