// Package strategy 定义量化策略的统一接口与核心数据类型。
// 所有策略（N形、破局龙、双凸、龙回头、韩国联动）均实现 Strategy 接口，
// 通过信号生成（Signal）与评估（Evaluation）两条链路驱动交易决策。
package strategy

import "time"

// SignalType 信号类型枚举，标识策略来源。
// 用于风控模块区分不同策略的信号以执行优先级排队。
type SignalType string

const (
	SignalDragon       SignalType = "dragon"        // 破局龙战法信号
	SignalDoubleBump   SignalType = "double_bump"   // 双凸战法信号
	SignalNShape       SignalType = "n_shape"       // N形超短信号
	SignalDragonReturn SignalType = "dragon_return" // 龙回头战法信号
)

// TradeAction 交易动作枚举。
// 策略评估后输出的操作指令，引擎根据 action 决定是否下单。
type TradeAction string

const (
	ActionBuy   TradeAction = "buy"   // 买入信号，触发下单流程
	ActionSell  TradeAction = "sell"  // 卖出信号，触发平仓流程
	ActionHold  TradeAction = "hold"  // 持仓不动，不产生新操作
	ActionWatch TradeAction = "watch" // 观察中，不操作但持续监控
)

// Priority 优先级枚举（P1 最高，P4 最低）。
// 引擎按优先级排序信号队列，高优先级信号可覆盖/抢占低优先级操作。
// 参考规则：P1（紧急）> P2（重要）> P3_5（半优先）> P3（常规）> P4（记录）
type Priority int

const (
	P1   Priority = 1 // P1 — 强制操作，必须执行
	P2   Priority = 2 // P2 — 高优先级，建议执行
	P3_5 Priority = 3 // P3_5 — 中高优先级（部分策略半确认信号）
	P3   Priority = 4 // P3 — 常规优先级
	P4   Priority = 5 // P4 — 仅记录，不触发操作
)

// Signal 策略输出信号的结构体。
// 包含标的、方向、优先级、价格数量等完整下单要素。
// Meta 字段携带策略特有评分细节，供后续分析日志。
type Signal struct {
	Code       string             `json:"code"`           // 股票代码（如 "000001.SZ"）
	Name       string             `json:"name"`           // 股票名称
	Type       SignalType         `json:"type"`           // 信号来源策略类型
	Action     TradeAction        `json:"action"`         // 交易动作（buy/sell/hold/watch）
	Priority   Priority           `json:"priority"`       // 优先级（P1-P4）
	Price      float64            `json:"price"`          // 触发价格（当前价或策略计算的目标价）
	Qty        int                `json:"qty"`            // 建议数量（股数）
	Amount     float64            `json:"amount"`         // 建议金额（元）
	Reason     string             `json:"reason"`         // 信号原因简述（如 "full_chain" / "brief" / "d2_below_full"）
	Confidence float64            `json:"confidence"`     // 置信度 0~1，用于仓位计算
	Timestamp  int64              `json:"timestamp"`      // 信号生成时间戳（Unix 秒）
	Meta       map[string]float64 `json:"meta,omitempty"` // 策略评分明细（如 d1/d2/d3/d4, f1/f2/f3/f4）
	Reasons    map[string]string  `json:"-"`              // 各维度文字说明（不序列化）
}

// SignalResult 策略分析的输出包装。
// 包含信号列表与分析状态，供引擎统一调度。
type SignalResult struct {
	Signals  []Signal `json:"signals"`  // 当前周期生成的信号列表
	Analyzed bool     `json:"analyzed"` // 是否已完成分析
}

// Evaluation 策略评分结果。
// TotalScore 为综合评分（0~100），Details 存储各维度细分分数。
// Pass 表示是否通过阈值，Level 为级别标签（如 "full_chain"/"brief"/"observe"）。
// Confidence 归一化置信度 = TotalScore / 100。
type Evaluation struct {
	TotalScore float64            `json:"total_score"` // 总分（0~100），各维度分项累加
	Details    map[string]float64 `json:"details"`     // 各维度评分明细（如 n_shape 的 d1,d2,d3,d4）
	Pass       bool               `json:"pass"`        // 是否通过阈值（>=50 或策略自有标准）
	Level      string             `json:"level"`       // 信号级别（full_chain/brief/watch/observe/nodata）
	Confidence float64            `json:"confidence"`  // 置信度（TotalScore/100），用于仓位比例计算
	Reasons    map[string]string  `json:"reasons"`     // 各维度文字说明（如 d1="事件:利好政策"）
}

// ExitContext 策略退出检查的输入上下文。
type ExitContext struct {
	Code      string
	Name      string
	CostPrice float64
	CurPrice  float64
	EntryAt   string
	EntryMeta map[string]float64
	DailyK    []KLine
	Now       time.Time // 当前时间（zero=跳过时间相关检查）
}

// KLine 日K线数据（策略退出用，避免 import data 包）。
type KLine struct {
	Close  float64
	High   float64
	Low    float64
	Open   float64
	Volume float64
}

// ExitResult 策略退出检查结果，nil 表示不退出。
type ExitResult struct {
	Reason   string   // 退出原因描述
	Priority Priority // P1 强制/P2 建议/P3 提示
}

// Strategy 策略接口，所有具体策略必须实现。
// Name 返回中文名称，Type 返回信号类型标识。
// Evaluate 执行评分逻辑，GenerateSignal 将评分转化为交易信号。
type Strategy interface {
	Name() string                                                  // 返回策略显示名称（如 "N形超短"）
	Type() SignalType                                              // 返回策略信号类型
	Evaluate(code string, data interface{}) (*Evaluation, error)   // 执行策略评分
	GenerateSignal(code string, eval *Evaluation) (*Signal, error) // 将评分转化为交易信号
}
