// Package filter 实现信号过滤引擎，提供多层级信号质量过滤。
// Level 1: 基本面过滤（ST/10日涨幅/换手率）
// Level 2: 筹码过滤（[TBD]）
// Level 3: 失效模式过滤（[TBD]）
// 过滤级别由 rules.json 中的 filter_level 控制。
package filter

import (
	"fmt"
	"strings"

	"quant-trading/internal/config"
	"quant-trading/internal/data"
	"quant-trading/internal/strategy"
)

// Engine 信号过滤器，依赖配置管理器和数据协调器执行多层过滤。
type Engine struct {
	cfg          *config.Manager       // 配置管理器（读取过滤级别和阈值）
	coord        *data.DataCoordinator // 数据协调器（用于筹码等高级过滤的数据查询）
	EmotionPhase string                // 当前市场情绪阶段，由引擎 scanCycle 设置
}

// New 创建信号过滤器。
// 参数 cfg: 配置管理器。
func New(cfg *config.Manager) *Engine {
	return &Engine{cfg: cfg}
}

// SetCoordinator 设置数据协调器，供筹码过滤等功能使用。
// 参数 coord: 数据协调器实例。
func (e *Engine) SetCoordinator(coord *data.DataCoordinator) {
	e.coord = coord
}

// FilterResult 单个信号的过滤结果。
type FilterResult struct {
	Pass       bool    `json:"pass"`                // 是否通过过滤
	FilteredBy string  `json:"filtered_by"`         // 过滤维度：fundamental / chip / fail_mode
	Reason     string  `json:"reason"`              // 过滤原因描述
	LossProb   float64 `json:"loss_prob,omitempty"` // 预估亏损概率（仅失效模式过滤时设置）
}

// Signals 对传入的信号列表批量执行过滤。
// 当 FilterLevel == 0 时直接返回原列表不过滤。
// 对每个信号依次调用 Check，仅保留通过的信号。
func (e *Engine) Signals(signals []strategy.Signal, cfg *config.Rules) []strategy.Signal {
	if cfg.Filter.FilterLevel == 0 {
		return signals
	}

	var result []strategy.Signal
	for _, sig := range signals {
		fr := e.Check(&sig, cfg)
		if fr.Pass {
			result = append(result, sig)
		}
	}
	return result
}

// Check 对单个信号执行多层级过滤检查。
// 根据 FilterLevel 依次执行：
//
//	Level 1: filterFundamental（基本面）
//	Level 2: filterChip（筹码分布）
//	Level 3: filterFailMode（失效模式概率）
//
// 任意一级不通过即返回该级的过滤结果。
func (e *Engine) Check(sig *strategy.Signal, cfg *config.Rules) *FilterResult {
	ft := cfg.Filter.Thresholds

	resultA := e.filterFundamental(sig, ft)
	if !resultA.Pass {
		return resultA
	}

	if cfg.Filter.FilterLevel >= 2 {
		resultB := e.filterChip(sig, ft)
		if !resultB.Pass {
			return resultB
		}
	}

	if cfg.Filter.FilterLevel >= 3 {
		resultC := e.filterFailMode(sig, ft)
		if !resultC.Pass {
			return resultC
		}
	}

	return &FilterResult{Pass: true}
}

// filterFundamental 基本面过滤：
//   - 过滤名称含 ST/*ST 的股票
//   - 过滤 10 日涨幅超限的股票
//   - 过滤换手率超限的股票
func (e *Engine) filterFundamental(sig *strategy.Signal, ft config.FilterThresholds) *FilterResult {
	// ST by name
	if ft.IsSTFilter && strings.Contains(sig.Name, "ST") {
		return &FilterResult{Pass: false, FilteredBy: "fundamental", Reason: "ST股票"}
	}
	if ft.IsSTFilter && strings.Contains(sig.Name, "*ST") {
		return &FilterResult{Pass: false, FilteredBy: "fundamental", Reason: "ST股票"}
	}

	// 10日涨幅过滤
	if gain10d, ok := sig.Meta["gain_10d"]; ok && ft.MaxGain10d > 0 {
		if gain10d > ft.MaxGain10d {
			return &FilterResult{Pass: false, FilteredBy: "fundamental", Reason: "10日涨幅过高"}
		}
	}

	// 换手率过滤
	if turnover, ok := sig.Meta["turnover"]; ok && ft.MaxTurnover > 0 {
		if turnover > ft.MaxTurnover {
			return &FilterResult{Pass: false, FilteredBy: "fundamental", Reason: "换手率过高"}
		}
	}

	return &FilterResult{Pass: true}
}

// filterChip 筹码分布过滤。
// 依据两个维度：
//  1. chip_score（来自策略评估的筹码集中度评分，0~100）
//  2. net_inflow（资金流向净额，正=流入 负=流出）
//
// 优先从 sig.Meta 读取（策略评估已计算），否则从 DataCoordinator 实时查询。
func (e *Engine) filterChip(sig *strategy.Signal, ft config.FilterThresholds) *FilterResult {
	chipScore := 0.0
	if cs, ok := sig.Meta["chip_score"]; ok {
		chipScore = cs
	}
	if ft.ChipConcentration > 0 && chipScore > 0 && chipScore < ft.ChipConcentration {
		return &FilterResult{
			Pass: false, FilteredBy: "chip",
			Reason: fmt.Sprintf("筹码评分%.0f < 阈值%.0f", chipScore, ft.ChipConcentration),
		}
	}

	netInflow := 0.0
	if ni, ok := sig.Meta["net_inflow"]; ok {
		netInflow = ni
	} else if e.coord != nil {
		if cf, err := e.coord.GetStockMoneyFlow(sig.Code); err == nil && cf != nil {
			netInflow = cf.NetInflow
		}
	}
	if netInflow < 0 && ft.ChipConcentration > 0 {
		return &FilterResult{
			Pass: false, FilteredBy: "chip",
			Reason: fmt.Sprintf("主力净流出%.0f元", -netInflow),
		}
	}

	return &FilterResult{Pass: true}
}

// filterFailMode 失效模式过滤。
// 基于当前市场情绪和信号 Meta 数据估算失效概率。
// 强势市场用 FailModeStrong（0.7）阈值，弱势用 FailModeWeak（0.55）。
func (e *Engine) filterFailMode(sig *strategy.Signal, ft config.FilterThresholds) *FilterResult {
	strongPhases := map[string]bool{"启动": true, "发酵": true, "高潮": true}
	isStrong := strongPhases[e.EmotionPhase]
	threshold := ft.FailModeWeak
	if isStrong {
		threshold = ft.FailModeStrong
	}
	if threshold <= 0 {
		return &FilterResult{Pass: true}
	}

	// 从 Meta 读取特征值
	gain10d, _ := sig.Meta["gain_10d"]
	turnover, _ := sig.Meta["turnover"]
	chipScore, _ := sig.Meta["chip_score"]
	netInflow, _ := sig.Meta["net_inflow"]
	isLeader, _ := sig.Meta["is_leader"]

	prob := 0.0
	// 10日涨幅过高 → 失效风险增加
	if gain10d > 60 {
		prob += 0.25
	} else if gain10d > 30 {
		prob += 0.10
	}
	// 换手率过高 → 筹码松动
	if turnover > 30 {
		prob += 0.20
	} else if turnover > 15 {
		prob += 0.10
	}
	// 筹码集中度低 → 不稳定
	if chipScore > 0 && chipScore < 30 {
		prob += 0.15
	} else if chipScore > 0 && chipScore < 50 {
		prob += 0.05
	}
	// 主力净流出
	if netInflow < -5000000 {
		prob += 0.20
	} else if netInflow < 0 {
		prob += 0.10
	}
	// 板块龙头 → 降低风险
	if isLeader > 0 {
		prob -= 0.15
	}
	if prob < 0 {
		prob = 0
	}

	if prob > threshold {
		return &FilterResult{
			Pass:       false,
			FilteredBy: "fail_mode",
			Reason:     fmt.Sprintf("失效概率%.0f%% > 阈值%.0f%%", prob*100, threshold*100),
			LossProb:   prob,
		}
	}
	return &FilterResult{Pass: true}
}
