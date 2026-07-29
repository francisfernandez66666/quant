// Package config 提供 rules.json 的加载、热重载和类型安全的配置访问。
// 使用 fsnotify 监听文件变更，配置更改后自动重载并通过 OnReload 回调通知订阅者。
// 所有配置参数通过 json 标签从 rules.json 映射到 Go 结构体，不写入硬编码常量。
package config

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Rules 是 rules.json 配置文件的根结构，包含全部策略/风控/数据源/用户等配置。
type Rules struct {
	Strategy        StrategyConfig        `json:"strategy"`         // 四大策略参数
	Emotion         EmotionConfig         `json:"emotion_cycle"`    // 市场情绪周期各阶段参数
	MainSector      MainSectorConfig      `json:"main_sector"`      // 板块扫描/热点/事件驱动配置
	RiskCtrl        RiskConfig            `json:"risk_ctrl"`        // 风控参数（单票上限/止损/M8兜底等）
	DataSource      DataSourceConfig      `json:"data_source"`      // 数据源开关和Token
	Position        PositionConfig        `json:"position"`         // 仓位管理参数
	User            UserConfig            `json:"user"`             // 用户信息
	Theme           ThemeConfig           `json:"theme"`            // 红黑名单和自选股列表
	Filter          FilterConfig          `json:"filter"`           // 信号过滤级别和阈值
	RPS             RPSConfig             `json:"rps"`              // 板块RPS评分配置
	Chip            ChipConfig            `json:"chip"`             // 筹码分析配置
	MarketSentiment MarketSentimentConfig `json:"market_sentiment"` // 市场情绪辅助配置
	Calendar        CalendarConfig        `json:"calendar"`         // 宏观日历配置
	Korea           KoreaLinkageConfig    `json:"korea_linkage"`    // 韩国联动配置
	LLM             LLMConfig             `json:"llm"`              // LLM 情感分析配置
	TradeTime       TradeTimeConfig       `json:"trade_time"`       // 交易时段配置
	// License removed
	Pluggable []PluggableStrategy `json:"pluggable_strategies"` // 可插拔策略列表
	Accounts  []AccountConfig     `json:"accounts"`             // 用户账号列表（登录鉴权）
}

// AccountConfig 用户账号配置，用于 HTTP API 登录认证。
type AccountConfig struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	ExpiresAt int64  `json:"expires_at,omitempty"` // 账号过期时间戳（秒），0=永不过期
}

// IsExpired 检查账号是否已过期。
func (a AccountConfig) IsExpired() bool {
	if a.ExpiresAt <= 0 {
		return false
	}
	return time.Now().Unix() > a.ExpiresAt
}

// StrategyConfig 四大战法的配置集合。
type StrategyConfig struct {
	Dragon       DragonConfig       `json:"dragon"`        // 破局龙
	DoubleBump   DoubleBumpConfig   `json:"double_bump"`   // 双凸
	NShape       NShapeConfig       `json:"n_shape"`       // N形两段式
	DragonReturn DragonReturnConfig `json:"dragon_return"` // 龙回头
}

// DragonConfig 破局龙策略的权重/止盈/止损参数。
type DragonConfig struct {
	F1SealWeight           float64 `json:"f1_seal_weight"`             // F1 封板率权重
	F2ResonanceWeight      float64 `json:"f2_resonance_weight"`        // F2 板块共振权重
	F3PremiumWeight        float64 `json:"f3_premium_weight"`          // F3 溢价率权重
	F4RsWeight             float64 `json:"f4_rs_weight"`               // F4 RPS强度权重
	F3OneBoardDiscount     float64 `json:"f3_one_board_discount"`      // 一封时F3打折系数
	PullbackMaxPct         float64 `json:"pullback_max_pct"`           // 可接受最大回撤(%)
	BreakerSellHalfPct     float64 `json:"breaker_sell_half_pct"`      // 炸板-半仓卖出跌幅(%)
	BreakerSellAllPct      float64 `json:"breaker_sell_all_pct"`       // 炸板-全仓卖出跌幅(%)
	BuyPullbackSellHalfPct float64 `json:"buy_pullback_sell_half_pct"` // 买入后回撤-半仓触发(%)
	BuyPullbackSellAllPct  float64 `json:"buy_pullback_sell_all_pct"`  // 买入后回撤-全仓触发(%)
	BuyDayCloseBelow       float64 `json:"buy_day_close_below"`        // 买入日收盘低于此值触发卖出(%)
	NextOpenIfBelow        float64 `json:"next_open_if_below"`         // 次日开盘低于此值触发卖出(%)
	HardBreakoutOverride   bool    `json:"hard_breakout_override"`     // 是否启用龙头硬突破仓位覆盖
}

// DoubleBumpConfig 双凸策略的量价/调整日/权重/止盈参数。
type DoubleBumpConfig struct {
	FirstBreakVolumeMultiple  float64 `json:"first_break_volume_multiple"`  // 一凸量比倍数
	SecondBreakVolumeMultiple float64 `json:"second_break_volume_multiple"` // 二凸量比倍数
	BigCandleThreshold        float64 `json:"big_candle_threshold"`         // 大阳线实体阈值(%)
	AdjustVolRatioMax         float64 `json:"adjust_vol_ratio_max"`         // 调整日最大量比
	PullbackToEntityPct       float64 `json:"pullback_to_entity_pct"`       // 回踩到实体百分比
	AdjustDaysMin             int     `json:"adjust_days_min"`              // 调整日最少天数
	AdjustDaysMax             int     `json:"adjust_days_max"`              // 调整日最多天数
	AdjustDaysOverflow        int     `json:"adjust_days_overflow"`         // 调整日溢出天数
	PositionWeight            float64 `json:"position_weight"`              // 位置评分权重
	MAWeight                  float64 `json:"ma_weight"`                    // 均线评分权重
	SectorWeight              float64 `json:"sector_weight"`                // 板块评分权重
	VolumeWeight              float64 `json:"volume_weight"`                // 量能评分权重
	FirstBreakoutPositionPct  string  `json:"first_breakout_position_pct"`  // 一凸仓位百分比(范围)
	SecondBreakoutPositionPct string  `json:"second_breakout_position_pct"` // 二凸仓位百分比(范围)
	ThirdBreakoutPositionMode string  `json:"third_breakout_position_mode"` // 三凸仓位模式
	DoubleBumpTakeProfitPct   float64 `json:"double_bump_take_profit_pct"`  // 双凸止盈百分比
}

// NShapeConfig N形两段式策略的突破/旗面/仓位/时间/过滤等全部参数。
type NShapeConfig struct {
	NPatternScoreThreshold   float64 `json:"n_pattern_score_threshold"`    // N形态总评分阈值
	NShapeD1Threshold        float64 `json:"n_shape_D1_threshold"`         // D1维度阈值
	NShapeD2MinFull          float64 `json:"n_shape_D2_min_full"`          // D2满分最低值
	NShapeD3Over             float64 `json:"n_shape_D3_over"`              // D3阈值（超过为佳）
	OversoldPbRatio          float64 `json:"oversold_pb_ratio"`            // 超卖反弹阈值
	NShapeEntryLeftPct       float64 `json:"n_shape_entry_left_pct"`       // 左侧一突仓位(%)
	NShapeEntryRightPct      float64 `json:"n_shape_entry_right_pct"`      // 右侧二突仓位(%)
	NShapeTotalMaxPct        float64 `json:"n_shape_total_max_pct"`        // N形总仓位上限(%)
	BreakoutRatio            float64 `json:"n_shape_breakout_ratio"`       // 突破昨日高点倍数(如1.005)
	VolRatio                 float64 `json:"n_shape_vol_ratio"`            // 一突量比阈值
	FlagRetreatPct           float64 `json:"n_shape_flag_retreat_pct"`     // 旗面回撤阈值(%)
	NFlagVolRatioMax         float64 `json:"n_flag_vol_ratio_max"`         // 旗面量比上限
	NSecondBreakVolRatio     float64 `json:"n_second_break_vol_ratio"`     // 二突量比阈值(一突量的倍数)
	NSecondBreakMacdRedBars  int     `json:"n_second_break_macd_red_bars"` // 二突需连续红柱数
	NFlagDurationMin         int     `json:"n_flag_duration_min"`          // 旗面最小时长(分钟)
	NFlagDurationMax         int     `json:"n_flag_duration_max"`          // 旗面最大时长(分钟)
	NSecondBreakTimeLimit    string  `json:"n_second_break_time_limit"`    // 二突最晚时间(HHMM)
	HardStopLoss             float64 `json:"hard_stop_loss"`               // 硬止损百分比
	HighFreqStart            string  `json:"high_freq_start"`              // 高频扫描开始时间
	HighFreqEnd              string  `json:"high_freq_end"`                // 高频扫描结束时间
	HighFreqIntervalSec      int     `json:"high_freq_interval_sec"`       // 高频扫描间隔(秒)
	MidFreqIntervalSec       int     `json:"mid_freq_interval_sec"`        // 中频扫描间隔(秒)
	AfternoonFreqIntervalSec int     `json:"afternoon_freq_interval_sec"`  // 午后扫描间隔(秒)
	NormalFreqIntervalSec    int     `json:"normal_freq_interval_sec"`     // 普通扫描间隔(秒)
	SectorGainPctMin         float64 `json:"sector_gain_pct_min"`          // 板块最小涨幅(%)
	VolumeSurgeThreshold     float64 `json:"volume_surge_threshold"`       // 成交量异动阈值
	InflowSurgeMultiple      float64 `json:"inflow_surge_multiple"`        // 资金流入异动倍数
	NextDayCheckTime         string  `json:"next_day_check_time"`          // 次日检查时间
	CatalystMandatory        bool    `json:"catalyst_mandatory"`           // 是否强制催化剂事件
	SellBaseOnLimitUp        bool    `json:"sell_base_on_limit_up"`        // 是否基于涨停决策卖出
}

// EmotionConfig 市场情绪周期各阶段（冰点/启动/发酵/高潮/背离/退潮）的阈值和仓位比例。
type EmotionConfig struct {
	EmoIceBoardMax        int     `json:"emo_ice_board_max"` // 冰点期涨停板数上限
	EmoIceLimitupMax      int     `json:"emo_ice_limitup_max"`
	EmoIceBlastMin        float64 `json:"emo_ice_blast_min"`
	EmoPositionIce        float64 `json:"emo_position_ice"`
	EmoStartBoardMax      int     `json:"emo_start_board_max"`
	EmoStartLimitupMin    int     `json:"emo_start_limitup_min"`
	EmoStartLimitupMax    int     `json:"emo_start_limitup_max"`
	EmoStartBlastMin      float64 `json:"emo_start_blast_min"`
	EmoStartBlastMax      float64 `json:"emo_start_blast_max"`
	EmoPositionStart      float64 `json:"emo_position_start"`
	EmoFermentBoardMax    int     `json:"emo_ferment_board_max"`
	EmoFermentLimitupMin  int     `json:"emo_ferment_limitup_min"`
	EmoFermentLimitupMax  int     `json:"emo_ferment_limitup_max"`
	EmoFermentBlastMax    float64 `json:"emo_ferment_blast_max"`
	EmoPositionFerment    float64 `json:"emo_position_ferment"`
	EmoClimaxBoardMin     int     `json:"emo_climax_board_min"`
	EmoClimaxLimitupMin   int     `json:"emo_climax_limitup_min"`
	EmoClimaxBlastMax     float64 `json:"emo_climax_blast_max"`
	EmoPositionClimax     float64 `json:"emo_position_climax"`
	EmoDivergeBoardDrop   int     `json:"emo_diverge_board_drop"`
	EmoDivergeLimitupDrop int     `json:"emo_diverge_limitup_drop"`
	EmoDivergeBlastRise   float64 `json:"emo_diverge_blast_rise"`
	EmoPositionDiverge    float64 `json:"emo_position_diverge"`
	EmoRetreatBoardMax    int     `json:"emo_retreat_board_max"`
	EmoRetreatLimitupMax  int     `json:"emo_retreat_limitup_max"`
	EmoRetreatBlastMin    float64 `json:"emo_retreat_blast_min"`
	EmoPositionRetreat    float64 `json:"emo_position_retreat"`
}

// MainSectorConfig 板块扫描和事件驱动的配置，
// 区分牛市/震荡市不同参数，支持事件→板块映射。
type MainSectorConfig struct {
	MainSectorLimitupBull  int               `json:"main_sector_limitup_bull"`   // 牛市板块涨停阈值
	MainSectorVolrankBull  int               `json:"main_sector_volrank_bull"`   // 牛市板块量比排名阈值
	MainSectorGain2dBull   float64           `json:"main_sector_gain2d_bull"`    // 牛市两日涨幅阈值(%)
	MainSectorLimitupShock int               `json:"main_sector_limitup_shock"`  // 震荡市板块涨停阈值
	MainSectorVolrankShock int               `json:"main_sector_volrank_shock"`  // 震荡市板块量比排名阈值
	MainSectorGain2dShock  float64           `json:"main_sector_gain2d_shock"`   // 震荡市两日涨幅阈值(%)
	MainSectorMaxCount     int               `json:"main_sector_max_count"`      // 最多追踪板块数量
	SectorEventMap         map[string]string `json:"sector_event_map,omitempty"` // 事件→板块名映射表
}

// RiskConfig 风控参数集合，包含单票/单板块限制、止损规则、M8兜底等。
type RiskConfig struct {
	PerStockMax            float64            `json:"per_stock_max"`             // 单票最大仓位(%)
	PerSectorMax           float64            `json:"per_sector_max"`            // 单板块最大仓位(%)
	StopLoss               StopLossConfig     `json:"stop_loss"`                 // 止损配置
	StrategyStop           StrategyStopConfig `json:"strategy_level_stop"`       // 策略级止蚀配置
	TakeProfitExit         TPConfig           `json:"take_profit_exit"`          // 止盈出场配置
	Compliance             ComplianceConfig   `json:"compliance"`                // 合规配置
	M8Enabled              bool               `json:"m8_enabled"`                // 是否启用M8兜底
	M8PortfolioDrawdownPct float64            `json:"m8_portfolio_drawdown_pct"` // M8组合回撤触发(%)
	M8CheckIntervalSec     int                `json:"m8_check_interval_sec"`     // M8检查间隔(秒)
}

// StopLossConfig 买入日收盘/次日开盘/买入后回撤等多级止损规则。
type StopLossConfig struct {
	BuyDayCloseBelow float64        `json:"buy_day_close_below"` // 买入日收盘跌破此值触发
	NextOpenIfBelow  float64        `json:"next_open_if_below"`  // 次日开盘低于此值触发
	DrawdownAfterBuy []DrawdownRule `json:"drawdown_after_buy"`  // 买入后回撤阶梯规则
}

// DrawdownRule 回撤阶梯规则：达到指定回撤百分比后执行卖出动作。
type DrawdownRule struct {
	Pct    float64 `json:"pct"`    // 回撤百分比(负值)
	Action string  `json:"action"` // 动作：sell / sell_half
	Order  string  `json:"order"`  // 委托方式：market / limit
}

// StrategyStopConfig 各策略级别的止蚀信号配置。
type StrategyStopConfig struct {
	Dragon       []StopCondition `json:"dragon"`        // 破局龙止蚀条件列表
	DoubleBump   []StopCondition `json:"double_bump"`   // 双凸止蚀条件列表
	DragonReturn []StopCondition `json:"dragon_return"` // 龙回头止蚀条件列表
}

// StopCondition 单条止蚀条件。
type StopCondition struct {
	Condition string `json:"condition"` // 触发条件描述
	Action    string `json:"action"`    // 对应动作
	Order     string `json:"order"`     // 委托方式
}

// TPConfig 止盈出场配置，含炸板止盈和双凸止盈。
type TPConfig struct {
	BlastOffBroken       []BlastRule `json:"blast_off_broken"`        // 炸板止盈规则列表
	DoubleBumpTakeProfit float64     `json:"double_bump_take_profit"` // 双凸止盈百分比
}

// BlastRule 炸板止盈规则：达涨幅阈值后执行对应动作。
type BlastRule struct {
	PctTrigger float64  `json:"pct_trigger"` // 触发涨幅(%)
	Action     string   `json:"action"`      // 动作：sell / sell_half
	Order      string   `json:"order"`       // 委托方式
	Scenarios  []string `json:"scenarios"`   // 适用场景列表
}

// ComplianceConfig 合规风控配置，用于控制交易频率/挂单时长/撤单率等。
type ComplianceConfig struct {
	MaxOrderFreqPerMin  int     `json:"max_order_freq_per_min"` // 每分钟最大下单次数
	MinOrderStaySeconds int     `json:"min_order_stay_seconds"` // 订单最短驻留时间(秒)
	MaxCancelRate       float64 `json:"max_cancel_rate"`        // 最大撤单率(%)
	ComplianceMode      bool    `json:"compliance_mode"`        // 合规模式开关
}

// DataSourceConfig 三源数据提供商的开关和Token配置。
type DataSourceConfig struct {
	BottomAPI   SourceItem `json:"bottom_api"`  // 底源API
	Tushare     SourceItem `json:"tushare"`     // Tushare Pro
	Tonghuashun SourceItem `json:"tonghuashun"` // 同花顺
}

// SourceItem 单个数据源的配置项。
type SourceItem struct {
	Enabled bool   `json:"enabled"`         // 是否启用
	Token   string `json:"token,omitempty"` // API Token（序列化时若为空则忽略）
}

// MarketSentimentConfig 市场情绪辅助配置，在大跌时控制仓位。
type MarketSentimentConfig struct {
	MediumDropThreshold float64 `json:"medium_drop_threshold"`  // 中跌阈值(%)
	PositionHalveOnDrop bool    `json:"position_halve_on_drop"` // 大跌时是否半仓
}

// PositionConfig 仓位管理参数：单票/总仓/板块上限、模式等。
type PositionConfig struct {
	MaxSinglePositionPct float64 `json:"max_single_position_pct"` // 单票最大仓位(%)
	MaxTotalPositionPct  float64 `json:"max_total_position_pct"`  // 总仓位上限(%)
	PerSectorMax         float64 `json:"per_sector_max"`          // 单板块仓位上限(%)
	Mode                 string  `json:"mode"`                    // 仓位模式：aggressive/conservative/auto
	UpdateInterval       int     `json:"update_interval"`         // 仓位更新间隔(秒)
}

// UserConfig 用户信息配置。
type UserConfig struct {
	Name          string `json:"name"`           // 用户名
	TotalCapital  string `json:"total_capital"`  // 总资金（字符串，支持万/亿单位）
	RiskTolerance string `json:"risk_tolerance"` // 风险偏好：low/medium/high
	Broker        string `json:"broker"`         // 券商名称
}

// ThemeConfig 主题相关配置：红名单（优先）/黑名单（排除）/自选股列表。
type ThemeConfig struct {
	RedList             []string `json:"red_list"`
	BlackList           []string `json:"black_list"`
	StaleBlackList      []string `json:"stale_black_list"`
	WatchList           []string `json:"watch_list"`
	MaxMarketCap        float64  `json:"max_market_cap"`
	MinPE               float64  `json:"min_pe"`
	MinTurnover         float64  `json:"min_turnover_rate"`
	MinVol20d           float64  `json:"min_volatility_20d"`
	StaleScoreThreshold int      `json:"stale_score_threshold"`
}

// FilterConfig 信号过滤器配置，包含过滤级别和各维度阈值。
type FilterConfig struct {
	FilterLevel int              `json:"filter_level"` // 过滤级别：0=不过滤，1=基本面，2=筹码，3=失效模式
	Thresholds  FilterThresholds `json:"thresholds"`   // 各过滤维度阈值
}

// FilterThresholds 各过滤维度的阈值配置。
type FilterThresholds struct {
	IsSTFilter           bool    `json:"is_st_filter"`           // 是否过滤ST股票
	MaxTurnover          float64 `json:"max_turnover"`           // 最大换手率(%)
	MaxGain10d           float64 `json:"max_gain_10d"`           // 10日最大涨幅(%)
	GoodwillRatio        float64 `json:"goodwill_ratio"`         // 商誉占比上限(%)
	PledgeRatio          float64 `json:"pledge_ratio"`           // 质押比例上限(%)
	ConsecutiveLossYears int     `json:"consecutive_loss_years"` // 连续亏损年数上限
	UnlockRatio30d       float64 `json:"unlock_ratio_30d"`       // 30日解禁比例上限(%)
	ProfitTakeRatio      float64 `json:"profit_take_ratio"`      // 盈利兑现比例
	ChipConcentration    float64 `json:"chip_concentration"`     // 筹码集中度阈值
	FailModeStrong       float64 `json:"fail_mode_strong"`       // 强势失效模式概率阈值
	FailModeWeak         float64 `json:"fail_mode_weak"`         // 弱势失效模式概率阈值
}

// CalendarConfig 宏观日历配置，控制是否启用和经济事件列表。
type CalendarConfig struct {
	Enabled bool            `json:"enabled"` // 是否启用宏观日历提醒
	Events  []CalendarEvent `json:"events"`  // 经济事件列表
}

// CalendarEvent 宏观日历中的单个经济事件。
type CalendarEvent struct {
	Date        string `json:"date"`         // 事件日期（YYYY-MM-DD）
	Title       string `json:"title"`        // 事件标题
	Impact      string `json:"impact"`       // 影响程度：high/medium/low
	DaysAdvance int    `json:"days_advance"` // 提前N天预警
}

// KoreaLinkageConfig 韩国科技股联动配置，用于跟踪三星/SK海力士等走势对A股影响。
type KoreaLinkageConfig struct {
	Enabled       bool    `json:"enabled"`         // 是否启用韩国联动
	TickerSamsung string  `json:"ticker_samsung"`  // 三星电子代码
	TickerSKHynix string  `json:"ticker_sk_hynix"` // SK海力士代码
	ThresholdPct  float64 `json:"threshold_pct"`   // 涨跌幅阈值(%)(默认3)
	Weight        float64 `json:"weight"`          // 联动权重系数
}

// LLMConfig LLM 推荐配置：模型名（默认 THUDM/GLM-Z1-9B-0414）。
type LLMConfig struct {
	Model string `json:"model"` // 模型名（如 THUDM/GLM-Z1-9B-0414）
}

// TradeTimeConfig 交易时段配置，所有值均为 HHMM 整数格式。
type TradeTimeConfig struct {
	TradeOpen      int `json:"trade_open"`       // 开盘（默认 925）
	TradeClose     int `json:"trade_close"`      // 收盘（默认 1500）
	FullOpen       int `json:"full_open"`        // 完整开盘（默认 915）
	FullClose      int `json:"full_close"`       // 完整收盘（默认 1530）
	PreOpenStart   int `json:"pre_open_start"`   // 集合竞价开始（默认 915）
	PreOpenEnd     int `json:"pre_open_end"`     // 集合竞价结束（默认 925）
	MorningHighEnd int `json:"morning_high_end"` // 早盘高频结束（默认 1000）
	MidFreqStart   int `json:"mid_freq_start"`   // 中频窗口开始（默认 945）
	AfternoonStart int `json:"afternoon_start"`  // 午后高频开始（默认 1300）
	AfternoonEnd   int `json:"afternoon_end"`    // 午后高频结束（默认 1330）
}

// PluggableStrategy 可插拔策略的配置项，支持动态注册/启停。
type PluggableStrategy struct {
	ID       string  `json:"id"`       // 策略唯一ID
	Name     string  `json:"name"`     // 策略名称
	Trigger  string  `json:"trigger"`  // 触发条件描述
	Weight   float64 `json:"weight"`   // 权重
	Enabled  bool    `json:"enabled"`  // 是否启用
	Schedule string  `json:"schedule"` // 执行计划(cron表达式)
}

// DragonReturnConfig 龙回头策略的全部参数：首波/回撤/量比/止盈/止损等。
type DragonReturnConfig struct {
	FirstRiseMin        float64 `json:"first_rise_min"`        // 首波最小涨幅(%)
	FirstRiseMax        float64 `json:"first_rise_max"`        // 首波最大涨幅(%)
	PullbackOptimalLow  float64 `json:"pullback_optimal_low"`  // 回撤最优区间下限(%)
	PullbackOptimalHigh float64 `json:"pullback_optimal_high"` // 回撤最优区间上限(%)
	PullbackDaysMin     int     `json:"pullback_days_min"`     // 回撤最少天数
	PullbackDaysMax     int     `json:"pullback_days_max"`     // 回撤最多天数
	VolumeRatioGood     float64 `json:"volume_ratio_good"`     // 缩量比阈值(表明缩量充分)
	ScoreThreshold      float64 `json:"score_threshold"`       // 入场评分阈值
	MainPositionScore   float64 `json:"main_position_score"`   // 主力仓位评分阈值
	AccelerateScore     float64 `json:"accelerate_score"`      // 加速预期评分
	StopLossPct         float64 `json:"stop_loss_pct"`         // 止损百分比
	Target1Multiplier   float64 `json:"target1_multiplier"`    // 目标价1倍数
	Target2Multiplier   float64 `json:"target2_multiplier"`    // 目标价2倍数
	TrailingDrawback    float64 `json:"trailing_drawback"`     // 移动止盈回撤(%)
	MaxHoldDays         int     `json:"max_hold_days"`         // 最大持仓天数
}

// RPSConfig 板块RPS强度排名系统的配置。
type RPSConfig struct {
	MainThreshold    int     `json:"rps_main_threshold"`   // RPS主力阈值
	LongThreshold    int     `json:"rps_long_threshold"`   // RPS长期阈值
	ConfirmDays      int     `json:"rps_confirm_days"`     // RPS确认天数
	SlopeAccelerate  float64 `json:"rps_slope_accelerate"` // RPS斜率加速阈值
	SlopeDecelerate  float64 `json:"rps_slope_decelerate"` // RPS斜率减速阈值
	SectorTotalCount int     `json:"sector_total_count"`   // 板块总数
	TopSectorCount   int     `json:"top_sector_count"`     // Top板块数量
}

// ChipConfig 筹码分布分析配置，含衰减系数/回看天数/集中度阈值等。
type ChipConfig struct {
	DecayDelta          float64 `json:"chip_decay_delta"`      // 筹码衰减系数
	LookbackDays        int     `json:"chip_lookback_days"`    // 筹码回看天数
	Concentration70Good float64 `json:"concentration_70_good"` // 70%筹码集中度良好阈值
	Concentration70Bad  float64 `json:"concentration_70_bad"`  // 70%筹码集中度不佳阈值
	Concentration90Good float64 `json:"concentration_90_good"` // 90%筹码集中度良好阈值
	ProfitRatioHigh     float64 `json:"profit_ratio_high"`     // 高盈利比率阈值
	ProfitRatioLow      float64 `json:"profit_ratio_low"`      // 低盈利比率阈值
	MainCostTouchBand   float64 `json:"main_cost_touch_band"`  // 主力成本触及区间(%)
	PeakMoveThreshold   float64 `json:"peak_move_threshold"`   // 筹码峰移动阈值
}

// Manager 管理 rules.json 的加载、线程安全访问和文件变更热重载。
type Manager struct {
	mu       sync.RWMutex // 保护 rules 读写并发安全
	rules    *Rules       // 当前生效的配置规则
	path     string       // rules.json 文件路径
	onReload func(*Rules) // 配置重载后的回调函数
}

// NewManager 创建配置管理器实例。
// 参数 path: rules.json 的路径。
func NewManager(path string) *Manager {
	return &Manager{path: path}
}

// Load 从文件读取并解析 rules.json，更新内存中的配置。
// 返回解析错误（文件不存在或JSON格式错误）。
func (m *Manager) Load() error {
	data, err := os.ReadFile(m.path)
	if err != nil {
		return err
	}
	var rules Rules
	if err := json.Unmarshal(data, &rules); err != nil {
		return err
	}
	m.mu.Lock()
	m.rules = &rules
	m.mu.Unlock()
	return nil
}

// Path 返回配置文件路径。
func (m *Manager) Path() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.path
}

// Get 返回当前配置的只读指针（线程安全）。
func (m *Manager) Get() *Rules {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rules
}

// OnReload 注册配置重载后的回调函数，在 Watch 检测到文件变更时自动调用。
func (m *Manager) OnReload(fn func(*Rules)) {
	m.onReload = fn
}

// Watch 启动 fsnotify 监听 rules.json 文件变更。
// 检测到 Write/Rename 事件后自动重载并调用 OnReload 回调。
// 返回 watcher 创建阶段的错误。
func (m *Manager) Watch() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := watcher.Add(m.path); err != nil {
		watcher.Close()
		return err
	}
	go func() {
		defer watcher.Close()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC in config watcher: %v", r)
			}
		}()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Rename) != 0 {
					if err := m.Load(); err != nil {
						log.Printf("config reload error: %v", err)
						continue
					}
					log.Println("规则已重载！")
					if m.onReload != nil {
						m.onReload(m.Get())
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("config watch error: %v", err)
			}
		}
	}()
	return nil
}
