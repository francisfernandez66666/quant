// Package position 提供仓位计算和回撤检查功能。
// 根据策略信号、情绪阶段、总资金和已有持仓计算建议的买入数量和金额，
// 支持 N 形特殊仓位规则（不受30%/80%限制，仅90%截断）和龙头硬突破覆盖。
package position

import (
	"math"
	"strconv"
	"strings"

	"quant-trading/internal/config"
	"quant-trading/internal/strategy"
)

// Manager 仓位计算器，根据配置和实时数据计算建议仓位。
type Manager struct {
	cfg *config.Manager // 配置管理器（仓位参数+情绪参数）
}

// New 创建仓位计算器。
// 参数 cfg: 配置管理器。
func New(cfg *config.Manager) *Manager {
	return &Manager{cfg: cfg}
}

// CalcResult 仓位计算结果。
type CalcResult struct {
	Price          float64 `json:"price"`              // 计算基准价
	Quantity       int     `json:"quantity"`           // 建议买入数量（以100为单位的整手）
	Amount         float64 `json:"amount"`             // 建议买入金额
	PositionPct    float64 `json:"position_pct"`       // 本次买入占总资金百分比
	AfterPct       float64 `json:"after_position_pct"` // 买入后总仓位百分比
	IsHardBreakout bool    `json:"is_hard_breakout"`   // 是否为龙头硬突破（仓位覆盖）
	StrongAlert    bool    `json:"strong_alert"`       // 是否需要强提醒
}

// Holding 单笔持仓信息，用于回撤检查。
type Holding struct {
	Code       string  `json:"code"`        // 股票代码
	EntryPrice float64 `json:"entry_price"` // 入场价格
	EntryTime  string  `json:"entry_time"`  // 入场时间
	Quantity   int     `json:"qty"`         // 持仓数量
	CurrentVal float64 `json:"current_val"` // 当前市值
	ProfitPct  float64 `json:"profit_pct"`  // 盈亏百分比
}

// Calculate 根据信号、总资金、已有持仓和情绪阶段计算建议仓位。
// 计算流程：
//  1. 计算可用资金（总资金 - 已用持仓市值）
//  2. 判断是否为龙头硬突破（覆盖单票上限）
//  3. 根据滑条 + 情绪阶段调整攻击性
//  4. N 形策略使用特殊仓位规则（calcNShapePosition）
//  5. 按100股整手计算买入数量和金额
//
// 参数 sig: 交易信号；totalCapital: 总资金；holdings: 已有持仓列表；
// slider: 攻击性滑条（0-100）；emotionPhase: 当前市场情绪阶段。
func (m *Manager) Calculate(sig *strategy.Signal, totalCapital float64, holdings []Holding, slider int, emotionPhase string) *CalcResult {
	cfg := m.cfg.Get()
	pc := cfg.Position
	used := sumHoldings(holdings)
	available := totalCapital - used

	isHardBreakout := sig.Type == strategy.SignalDragon && cfg.Strategy.Dragon.HardBreakoutOverride

	maxSingle := totalCapital * pc.MaxSinglePositionPct / 100
	if isHardBreakout {
		maxSingle = available
	}

	aggression := 0.5 + float64(slider)/100.0
	suggested := math.Min(maxSingle, available) * aggression

	emotionLimit := m.getEmotionLimit(emotionPhase)
	maxPosition := totalCapital * emotionLimit / 100

	if suggested > available {
		suggested = available
	}
	if used+suggested > maxPosition {
		suggested = maxPosition - used
	}

	if sig.Type == strategy.SignalNShape {
		suggested = m.calcNShapePosition(sig, available, cfg)
	}

	price := sig.Price
	if price <= 0 {
		price = 10.0
	}
	qty := int(suggested/price/100) * 100
	amount := float64(qty) * price
	pct := amount / totalCapital * 100
	afterPct := (used + amount) / totalCapital * 100

	strong := false
	if isHardBreakout || (sig.Type == strategy.SignalNShape && afterPct > 80) {
		strong = true
	}

	return &CalcResult{
		Price:          price,
		Quantity:       qty,
		Amount:         amount,
		PositionPct:    pct,
		AfterPct:       afterPct,
		IsHardBreakout: isHardBreakout,
		StrongAlert:    strong,
	}
}

// CheckDrawdown 检查所有持仓的回撤情况，触发止损规则时生成卖出信号。
// 对每个持仓逐条匹配 DrawdownAfterBuy 规则，回撤达标即生成对应动作的信号。
// 参数 holdings: 持仓列表；currentPrices: 当前价格映射（code → price）。
// 返回需要执行的止损信号列表。
func (m *Manager) CheckDrawdown(holdings []Holding, currentPrices map[string]float64) []strategy.Signal {
	cfg := m.cfg.Get()
	dc := cfg.RiskCtrl.StopLoss.DrawdownAfterBuy
	var signals []strategy.Signal

	for _, h := range holdings {
		curPrice, ok := currentPrices[h.Code]
		if !ok {
			continue
		}
		drawdown := (curPrice - h.EntryPrice) / h.EntryPrice * 100
		for _, rule := range dc {
			if drawdown <= rule.Pct {
				action := strategy.ActionSell
				if rule.Action == "sell_half" {
					action = strategy.ActionSell
				}
				signals = append(signals, strategy.Signal{
					Code:     h.Code,
					Action:   action,
					Priority: strategy.P3,
					Price:    curPrice,
					Qty:      h.Quantity,
					Reason:   "回撤触发:" + rule.Action,
				})
				break
			}
		}
	}
	return signals
}

// getEmotionLimit 根据市场情绪阶段返回对应的仓位上限百分比。
// 阶段映射：冰点/启动/发酵/高潮/背离/退潮 → EmotionConfig 对应字段。
func (m *Manager) getEmotionLimit(phase string) float64 {
	cfg := m.cfg.Get()
	ec := cfg.Emotion
	switch phase {
	case "冰点":
		return ec.EmoPositionIce
	case "启动":
		return ec.EmoPositionStart
	case "发酵":
		return ec.EmoPositionFerment
	case "高潮":
		return ec.EmoPositionClimax
	case "背离":
		return ec.EmoPositionDiverge
	case "退潮":
		return ec.EmoPositionRetreat
	default:
		return 50
	}
}

// calcNShapePosition 计算 N 形策略的特殊仓位。
// 左侧（一突）和右侧（二突）使用不同仓位百分比。
// N 形仓位不受普通 30%/80% 限制，仅受 NShapeTotalMaxPct 约束。
func (m *Manager) calcNShapePosition(sig *strategy.Signal, available float64, cfg *config.Rules) float64 {
	nc := cfg.Strategy.NShape
	isLeft := false
	if sig.Meta != nil {
		if v, ok := sig.Meta["left_signal"]; ok && v > 0 {
			isLeft = true
		}
	}
	var pct float64
	if isLeft {
		pct = nc.NShapeEntryLeftPct
	} else {
		pct = nc.NShapeEntryRightPct
	}
	pos := available * pct / 100
	maxPos := available * nc.NShapeTotalMaxPct / 100
	if pos > maxPos {
		pos = maxPos
	}
	return pos
}

// sumHoldings 计算持仓总市值。
func sumHoldings(holdings []Holding) float64 {
	var total float64
	for _, h := range holdings {
		total += h.CurrentVal
	}
	return total
}

// ParsePctRange 解析百分比范围字符串，支持 "20-30" / "20%~30%" / "20%-30%" 格式。
func ParsePctRange(s string) (min, max float64) {
	min, max = 20, 30
	s = strings.ReplaceAll(s, "%", "")
	parts := strings.Split(strings.ReplaceAll(s, "~", "-"), "-")
	if len(parts) == 2 {
		m1, e1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		m2, e2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if e1 == nil && e2 == nil && m1 < m2 && m1 > 0 {
			return m1, m2
		}
	}
	return
}
