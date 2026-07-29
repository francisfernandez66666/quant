// Package korea 实现韩国市场指数联动监控（Korea Linkage）。
//
// 策略目标：监测韩国科技股（三星电子、SK海力士等）的大幅波动，
// 利用 A 股开盘时间滞后于韩股的特性，提前感知半导体/消费电子板块的潜在联动效应。
//
// 工作原理：
//  1. 通过 Yahoo Finance API（query1.finance.yahoo.com）获取韩股实时报价
//  2. 解析 JSON 提取 regularMarketPrice（当前价）和 regularMarketChangePercent（涨跌幅）
//  3. 缓存最新报价，Signal() 方法返回超过阈值（ThresholdPct）的异常波动标的
//
// 联动逻辑：
//   - 三星电子/海力士大幅上涨 → A股半导体/消费电子板块可能跟随
//   - 大幅下跌 → 风险预警，A股同板块个股应考虑规避
//
// 数据源：
//   - Yahoo Finance API（免密钥，仅需 User-Agent 头）
//   - Ticker 格式：韩国代码后加 .KS（如 005930.KS）
//
// 输出：
//   - KoreaQuote 结构体（代码/名称/价格/涨跌幅）
//   - Signal() 在涨跌幅超阈值时返回预警信号
package korea

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"quant-trading/internal/config"
	"quant-trading/internal/data"
)

// KoreaQuote 韩国股票行情数据结构。
type KoreaQuote struct {
	Code      string  `json:"code"`       // Yahoo Finance ticker（如 "005930.KS"）
	Name      string  `json:"name"`       // 中文名称（如 "三星电子"）
	Price     float64 `json:"price"`      // 当前价格（美元或韩元，取决于Yahoo数据源）
	ChangePct float64 `json:"change_pct"` // 涨跌幅百分比（如 2.5 = +2.5%）
}

// KoreaLinkage 韩国市场联动监控器。
// 通过 HTTP 定时拉取 Yahoo Finance 行情，缓存最新报价，
// 提供 Signal() 接口供主策略判断是否触发联动预警。
type KoreaLinkage struct {
	cfg    config.KoreaLinkageConfig // 配置（启停开关、Ticker、阈值）
	client *http.Client              // HTTP 客户端（10 秒超时）
	cache  map[string]*KoreaQuote    // 行情缓存 key=ticker
}

// New 创建韩国联动监控器实例。
// cfg 从 config 传入，包含 Enabled/TickerSamsung/TickerSKHynix/ThresholdPct 字段。
func New(cfg config.KoreaLinkageConfig) *KoreaLinkage {
	return &KoreaLinkage{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
		cache:  make(map[string]*KoreaQuote),
	}
}

// Fetch 批量拉取所有配置的韩股行情。
// 遍历 Samsung 和 SK Hynix 的 Ticker，逐个调用 fetchOne，
// 成功的结果写入缓存并加入返回列表。
func (k *KoreaLinkage) Fetch() ([]KoreaQuote, error) {
	if !k.cfg.Enabled {
		return nil, nil
	}
	var out []KoreaQuote
	for _, ticker := range []string{k.cfg.TickerSamsung, k.cfg.TickerSKHynix} {
		if ticker == "" {
			continue
		}
		q, err := k.fetchOne(ticker)
		if err != nil {
			continue
		}
		k.cache[ticker] = q
		out = append(out, *q)
	}
	return out, nil
}

// fetchOne 调用 Yahoo Finance Chart API 获取单只韩股行情。
// URL 格式：https://query1.finance.yahoo.com/v8/finance/chart/{ticker}?interval=1d&range=1d
// 使用 Mozilla User-Agent 避免被拦截。
// 响应体传给 parseYahooQuote 做简易 JSON 解析。
func (k *KoreaLinkage) fetchOne(ticker string) (*KoreaQuote, error) {
	url := fmt.Sprintf("https://query1.finance.yahoo.com/v8/finance/chart/%s?interval=1d&range=1d", ticker)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	data.YahooLimiter.Wait()
	resp, err := k.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return parseYahooQuote(body, ticker)
}

// parseYahooQuote 解析 Yahoo Finance API 返回的 JSON 响应。
// 采用字符串查找方式（不引入第三方 JSON 库），
// 提取 regularMarketPrice 和 regularMarketChangePercent 两个字段。
// Ticker 含 005930 或 000660 时自动映射中文名称。
func parseYahooQuote(body []byte, ticker string) (*KoreaQuote, error) {
	raw := string(body)
	// 简易字符串解析：查找 key 后的数值（无需 JSON 反序列化库）
	price := extractFloat(raw, `"regularMarketPrice":`)
	chg := extractFloat(raw, `"regularMarketChangePercent":`)
	name := ticker
	if strings.Contains(ticker, "005930") {
		name = "三星电子"
	} else if strings.Contains(ticker, "000660") {
		name = "SK海力士"
	}
	return &KoreaQuote{
		Code: ticker, Name: name, Price: price, ChangePct: chg,
	}, nil
}

// extractFloat 从 JSON 字符串中查找指定 key 后的 float64 值。
// 定位 key 起始位置，取到下一个逗号或右花括号前的子串后解析。
// 找不到 key 或解析失败时返回 0。
func extractFloat(s, key string) float64 {
	idx := strings.Index(s, key)
	if idx < 0 {
		return 0
	}
	start := idx + len(key)
	end := strings.IndexByte(s[start:], ',')
	if end < 0 {
		end = strings.IndexByte(s[start:], '}')
	}
	if end < 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(s[start:start+end]), 64)
	return v
}

// Signal 检查缓存的韩股行情是否有超过阈值的异常波动。
// 当任意标的涨跌幅绝对值 > cfg.ThresholdPct 时返回该标的 Quote，
// 用于触发风险预警或联动交易信号。
// 若无异常波动则返回 nil。
func (k *KoreaLinkage) Signal() *KoreaQuote {
	for _, q := range k.cache {
		if q.ChangePct > k.cfg.ThresholdPct || q.ChangePct < -k.cfg.ThresholdPct {
			return q
		}
	}
	return nil
}
