// Package engine 实现量化交易的核心流程引擎。
// 负责主循环驱动（Run）、数据采集、多策略评估（N形/破局龙/双凸/龙回头）、
// 信号过滤/风控/仓位计算、Android 通知推送以及 HTTP API 服务。
// 引擎通过 fetcher 定期拉取行情快照，在每个 scanCycle 中执行全链路评估，
// 输出排序后的 SignalView 列表供前端展示和半自动交易参考。
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"quant-trading/internal/calendar"
	"quant-trading/internal/config"
	"quant-trading/internal/data"
	"quant-trading/internal/filter"
	"quant-trading/internal/llm"
	"quant-trading/internal/notify"
	"quant-trading/internal/position"
	"quant-trading/internal/registry"
	"quant-trading/internal/risk"
	"quant-trading/internal/server"
	"quant-trading/internal/strategy"
	"quant-trading/internal/strategy/double_bump"
	"quant-trading/internal/strategy/dragon"
	"quant-trading/internal/strategy/dragon_return"
	"quant-trading/internal/strategy/korea"
	"quant-trading/internal/strategy/n_shape"
	"quant-trading/internal/validate"
)

// NotifyAndroid 由 main 包设置（Android JNI 桥），用于推送系统通知到手机状态栏。
var NotifyAndroid func(title, body string)

// nStockState 记录 N 形战法个股的日内状态机数据。
// 该结构体在 scanCycle 中被持续更新，实现一突→旗面→二突的完整生命周期追踪。
type nStockState struct {
	Phase      n_shape.NPhase // 当前N形阶段：Idle/一突/旗面/二突/完成/失败
	FirstPrice float64        // 一突触发价格（突破prev_high×ratio时的成交价）
	FirstVol   float64        // 一突时刻的累计成交量
	FirstTime  int            // 一突触发时间，格式 HHMM（如 0935）
	PeakPrice  float64        // 一突后的最高价（用于旗面回撤判定）
	FlagLow    float64        // 旗面阶段的最低点
	FlagVol    float64        // 旗面阶段的最低量（缩量确认）
}

// minuteBar 存储个股的1分钟K线数据，用于计算分钟级MACD指标。
// 引擎在 scanCycle 中通过 updateMinuteBars 实时构建。
type minuteBar struct {
	Time   time.Time // K线时间戳
	Open   float64   // 分钟开盘价
	High   float64   // 分钟最高价
	Low    float64   // 分钟最低价
	Close  float64   // 分钟收盘价（当前最新价）
	Volume float64   // 分钟累计成交量
}

// Engine 是量化交易系统的核心结构体，统筹管理数据源、策略评估、风控、通知等全部子系统。
// 通过 Run 启动主循环，按动态间隔执行 scanCycle 完成一轮完整的扫描-评估-过滤-推送。
type Engine struct {
	cfg          *config.Manager                     // 热加载配置管理器
	market       *data.MarketAPI                     // 公开行情API（新浪/腾讯）
	riskCtrl     *risk.Engine                        // 风控引擎：黑名单/合规/回撤/M8兜底
	posMgr       *position.Manager                   // 仓位计算器
	notifier     *notify.Notifier                    // 通知推送（WebSocket + REST）
	sectorScan   *data.SectorScanner                 // 板块热点扫描器
	watchlistMgr *data.WatchlistManager              // 用户自选股管理器
	validator    *validate.Engine                    // 三源交叉校验引擎
	filter       *filter.Engine                      // 信号过滤器（基本面/筹码/失效模式）
	dragon       *dragon.DragonStrategy              // 破局龙策略
	doubleBump   *double_bump.DoubleBumpStrategy     // 双凸策略
	nShape       *n_shape.NShapeStrategy             // N形两段式策略
	dragonReturn *dragon_return.DragonReturnStrategy // 龙回头策略
	matcher      *data.EventMatcher                  // 事件匹配器（D1事件驱动）
	rpsMgr       *data.RPSManager                    // RPS板块强度管理器
	httpSrv      *server.Server                      // HTTP API服务（H5前端）
	h5FS         http.FileSystem                     // H5静态资源文件系统
	fetcher      *data.Fetcher                       // 行情数据采集器（定时拉取快照）
	coord        *data.DataCoordinator               // 数据协调器（多源路由/容错）
	history      *notify.History                     // 信号历史记录器
	holdings     *position.HoldingsManager           // 持仓持久化管理器

	mu               sync.RWMutex        // 保护 signals / nStates 等并发字段
	signals          []server.SignalView // 当前 scanCycle 输出的最终信号视图列表
	stockEvals       []server.StockEval  // 全量个股评分（含未通过策略的原始分）
	startAt          time.Time           // 引擎启动时间
	lastScan         time.Time           // 最近一次 scanCycle 完成时间
	sectorScanCount  int                 // 当前周期内的板块扫描计数器
	sectorScanPeriod int                 // 板块扫描周期数（默认每2次scan扫一次板块）
	pushedAlerts     map[string]bool     // 已推送告警去重map（key=code+title）
	alertLog         []server.Alert      // 告警历史日志（最多保留300条）

	scanStats       server.ScanStats // 最近一次扫描的统计数据
	offHoursFetched bool             // 非交易时段是否已抓取一次数据
	hitNotified     map[string]bool  // 命中提醒去重 key=code+strategy，每次开盘重置
	signalsNotified map[string]bool  // 信号告警去重 key=code+level+strategy，每次开盘重置

	// N形日内状态机：code → nStockState
	nStates map[string]*nStockState

	// 1分钟MACD累积器：code → minuteBar slice，最多保留60根
	barBuf     map[string][]minuteBar
	lastMinute int // 上次更新的分钟数（用于去重）

	// 板块热度过滤（9:15-9:20/13:00-13:05每日两次）

	// 板块成交额追踪（用于评分器 SectorTurnoverMA20）
	sectorAmtToday map[string]float64 // 当日板块最大成交额
	sectorAmtPrev  map[string]float64 // 上日板块全日成交额（≈MA20代理值）

	// 缓存K线避免高频重复API调用（60秒过期）
	kLineCache     map[string][]data.KLine
	kLineCacheTime time.Time

	// 资金流向缓存（60s过期）
	moneyFlowCache     map[string]*data.CapitalFlow
	moneyFlowCacheTime map[string]time.Time

	// 板块龙头缓存（每scan cycle更新）
	sectorLeaders map[string]bool

	// 宏观日历
	macroCal *calendar.Calendar

	// 韩国联动（KOSPI科技股）
	koreaLnk *korea.KoreaLinkage

	// LLM 新闻情感分析
	llmClient        *llm.Client
	llmTopicCache    map[string]*llm.HotTopic // title[:60] → 多维度分析
	llmBatchDate     string                   // 上次批量LLM分析的日期（YYYYMMDD）
	llmBatchSession  data.MarketSession       // 上次批量LLM分析的时段
	llmHotSectors    map[string]float64       // LLM推断热点板块名→平均情感分
	llmHotSectorsGen int64                    // 版本号，供evaluateAll判断是否需要重新计算

	// LLM D1 评分状态
	// llmFallback 为 true 时表示 LLM 连续失败/超时，切换到纯 YAML 规则兜底。
	llmFallback  bool   // LLM 失效标记：true=跳过LLM，直接用YAML规则
	llmFailCnt   int    // LLM 连续失败次数（重置后归零）
	lastLLMInput string // 上次发送给 LLM 的输入文本（去重用）

	// stockSectorIdx 个股→板块倒排索引。
	// 由 rebuildStockSectorIndex 生成，用于将 LLM 板块级评分映射到个股。
	// key=股票代码, value=该股所属的板块名称列表。
	stockSectorIdx map[string][]string // code → [板块名]

	// stockSectorSrc 热点个股来源追溯：code → 由哪个热点板块带入 + 该板块上榜原因。
	// 在 scanCycle 的板块扩股循环中更新，供 GetHotSnapshotStocks 展示。
	stockSectorSrc map[string]hotStockSource

	// llmL1Score/llmL1Blocked 由 rebuildLLMD1Scores() 从 llmTopicCache 重建。
	// 每个 scanCycle 的 evaluateAll() 中读取并传入 scorer.Ctx。
	llmL1Score   map[string]float64 // code → LLM D1 分 (0.0~1.0), 0=无结果
	llmL1Blocked map[string]bool    // code → LLM 利空阻塞标记
	llmL1Gen     int64              // llmL1Score 版本号，evaluateAll 校验用

	// M8兜底状态
	m8PeakTotal   float64   // 持仓市值历史峰值
	m8LastCheckAt time.Time // 上次M8检查时间

	// 持久化 eventDesc（跨 scan cycle 保留，每日重置）
	lastEventDesc   string    // 当前热点事件描述（新闻标题拼接）
	lastEventDescAt time.Time // 上次更新 eventDesc 的时间

	// 登录后延迟启动数据服务
	dataStarted bool

	// 标记 Prefetch 已执行，避免 engine.Run 重复预拉覆盖数据
	prefetched bool

	// 每日重置追踪：记录上次执行 resetNStates 的日期（YYYYMMDD），跨日自动重置
	lastResetDay string

	// 每日数据量计数（每日0点重置）
	dataCountDate  string // YYYYMMDD，用于判断跨日重置
	newsCount      int    // 当日已处理的新闻条数
	hotSectorCnt   int    // 当日已扫描的热点板块数
	sectorStockCnt int    // 当日板块扩展个股数

	// 持仓止盈止损提醒去重 (上下午各一次)
	pnlAlertSent map[string]string // code → 时段标识 ("morning"/"afternoon")

	// 策略退出检查结果 (code → exitResult, 每次 scanCycle 刷新)
	exitResults map[string]*strategy.ExitResult
}

// SetH5FS 设置 H5 静态文件系统，供 HTTP 服务端提供前端页面。
func (e *Engine) SetH5FS(fs http.FileSystem) {
	e.h5FS = fs
}

// NewFromRegistry 从注册表创建引擎实例，所有子系统已通过 registry 并行初始化完毕。
func NewFromRegistry(cfg *config.Manager, reg *registry.Registry) *Engine {
	e := &Engine{
		cfg: cfg,

		market:       getService[*registry.MarketAPIAdapter](reg, "market_api", (*registry.MarketAPIAdapter).Get),
		matcher:      getService[*registry.EventMatcherAdapter](reg, "event_matcher", (*registry.EventMatcherAdapter).Get),
		coord:        getService[*registry.DataCoordinatorAdapter](reg, "data_coordinator", (*registry.DataCoordinatorAdapter).Get),
		sectorScan:   getService[*registry.SectorScannerAdapter](reg, "sector_scanner", (*registry.SectorScannerAdapter).Get),
		rpsMgr:       getService[*registry.RPSManagerAdapter](reg, "rps_manager", (*registry.RPSManagerAdapter).Get),
		riskCtrl:     getService[*registry.RiskAdapter](reg, "risk_engine", (*registry.RiskAdapter).Get),
		posMgr:       getService[*registry.PositionAdapter](reg, "position_manager", (*registry.PositionAdapter).Get),
		validator:    getService[*registry.ValidatorAdapter](reg, "validator", (*registry.ValidatorAdapter).Get),
		filter:       getService[*registry.FilterAdapter](reg, "filter_engine", (*registry.FilterAdapter).Get),
		dragon:       getService[*registry.DragonAdapter](reg, "strategy_dragon", (*registry.DragonAdapter).Get),
		doubleBump:   getService[*registry.DoubleBumpAdapter](reg, "strategy_double_bump", (*registry.DoubleBumpAdapter).Get),
		nShape:       getService[*registry.NShapeAdapter](reg, "strategy_n_shape", (*registry.NShapeAdapter).Get),
		dragonReturn: getService[*registry.DragonReturnAdapter](reg, "strategy_dragon_return", (*registry.DragonReturnAdapter).Get),
		macroCal:     getService[*registry.CalendarAdapter](reg, "calendar", (*registry.CalendarAdapter).Get),
		koreaLnk:     getService[*registry.KoreaAdapter](reg, "korea_linkage", (*registry.KoreaAdapter).Get),
		llmClient:    getService[*registry.LLMAdapter](reg, "llm_client", (*registry.LLMAdapter).Get),

		sectorScanPeriod: 1,
		startAt:          time.Now(),

		pushedAlerts:       make(map[string]bool),
		alertLog:           make([]server.Alert, 0, 200),
		nStates:            make(map[string]*nStockState),
		barBuf:             make(map[string][]minuteBar),
		sectorAmtToday:     make(map[string]float64),
		sectorAmtPrev:      make(map[string]float64),
		kLineCache:         make(map[string][]data.KLine),
		moneyFlowCache:     make(map[string]*data.CapitalFlow),
		moneyFlowCacheTime: make(map[string]time.Time),
		sectorLeaders:      make(map[string]bool),
		llmTopicCache:      make(map[string]*llm.HotTopic),
		llmHotSectors:      make(map[string]float64),
		stockSectorSrc:     make(map[string]hotStockSource),
		pnlAlertSent:       make(map[string]string),
		exitResults:        make(map[string]*strategy.ExitResult),
		hitNotified:        make(map[string]bool),
		signalsNotified:    make(map[string]bool),
	}

	if s := reg.Service("fetcher"); s != nil {
		if a, ok := s.(*registry.FetcherAdapter); ok {
			e.fetcher = a.Get()
		}
	}
	if s := reg.Service("history"); s != nil {
		if a, ok := s.(*registry.HistoryAdapter); ok {
			e.history = a.Get()
		}
	}
	if s := reg.Service("holdings"); s != nil {
		if a, ok := s.(*registry.HoldingsAdapter); ok {
			e.holdings = a.Get()
		}
	}
	if s := reg.Service("notifier"); s != nil {
		if a, ok := s.(*registry.NotifierAdapter); ok {
			e.notifier = a.Get()
		}
	}
	if s := reg.Service("watchlist"); s != nil {
		if a, ok := s.(*registry.WatchlistAdapter); ok {
			e.watchlistMgr = a.Get()
		}
	}
	// 注册表未提供的边缘服务 — 直接创建退路
	if e.holdings == nil {
		e.holdings = position.NewHoldingsManager(".")
	}
	if e.watchlistMgr == nil {
		e.watchlistMgr = data.NewWatchlistManager(".")
		e.watchlistMgr.Load()
	}
	if e.notifier == nil {
		e.notifier = notify.New()
	}

	return e
}

// getService 泛型辅助，从注册表提取服务实例。
func getService[T interface{ Get() R }, R any](reg *registry.Registry, name string, getter func(T) R) R {
	var zero R
	s := reg.Service(name)
	if s == nil {
		return zero
	}
	a, ok := s.(T)
	if !ok {
		return zero
	}
	return getter(a)
}

// New 创建引擎实例，初始化所有子系统和策略对象。
// 参数:
//   - cfg: 配置管理器（热加载）
//   - tushareToken: Tushare API Token（为空时从环境变量或配置文件读取）
//
// 返回:
//   - 已组装完成的 Engine 指针，暂未启动主循环。
func New(cfg *config.Manager, tushareToken, llmToken string) *Engine {
	eventsCfg, err := data.LoadEvents("config/events_leftside.yaml")
	if err != nil {
		eventsCfg, err = data.LoadEvents("events_leftside.yaml")
	}
	var matcher *data.EventMatcher
	if err == nil {
		matcher = data.NewEventMatcher(eventsCfg)
	}
	if tushareToken == "" {
		tushareToken = os.Getenv("TUSHARE_TOKEN")
	}
	api := data.NewMarketAPI()
	tc := data.NewTushareClient(tushareToken)
	ths := data.NewTHSClient()
	jq := data.NewJQClient(os.Getenv("JQ_MOBILE"), os.Getenv("JQ_PASSWORD"))
	dc := data.NewDataCoordinator(api, tc, ths, jq)
	ss := data.NewSectorScanner(api, matcher)

	filterEng := filter.New(cfg)
	filterEng.SetCoordinator(dc)

	return &Engine{
		cfg:                cfg,
		market:             api,
		riskCtrl:           risk.New(cfg),
		posMgr:             position.New(cfg),
		notifier:           notify.New(),
		validator:          validate.New(cfg),
		filter:             filterEng,
		dragon:             dragon.New(cfg),
		doubleBump:         double_bump.New(cfg),
		nShape:             n_shape.New(cfg, matcher),
		dragonReturn:       dragon_return.New(cfg),
		matcher:            matcher,
		coord:              dc,
		rpsMgr:             data.NewRPSManager(),
		sectorScan:         ss,
		sectorScanPeriod:   1,
		pushedAlerts:       make(map[string]bool),
		alertLog:           make([]server.Alert, 0, 200),
		startAt:            time.Now(),
		nStates:            make(map[string]*nStockState),
		barBuf:             make(map[string][]minuteBar),
		sectorAmtToday:     make(map[string]float64),
		sectorAmtPrev:      make(map[string]float64),
		kLineCache:         make(map[string][]data.KLine),
		moneyFlowCache:     make(map[string]*data.CapitalFlow),
		moneyFlowCacheTime: make(map[string]time.Time),
		sectorLeaders:      make(map[string]bool),
		macroCal:           calendar.New(cfg.Get().Calendar.Events),
		koreaLnk:           korea.New(cfg.Get().Korea),
		llmClient:          llm.New(llmToken, cfg.Get().LLM.Model),
		llmTopicCache:      make(map[string]*llm.HotTopic),
		llmHotSectors:      make(map[string]float64),
		stockSectorSrc:     make(map[string]hotStockSource),
		pnlAlertSent:       make(map[string]string),
		exitResults:        make(map[string]*strategy.ExitResult),
		hitNotified:        make(map[string]bool),
		signalsNotified:    make(map[string]bool),
	}
}

// Prefetch 同步拉取一次行情快照并执行 scanCycle（供 agent 模式升级前预热数据）。
func (e *Engine) Prefetch() {
	if e.fetcher == nil {
		watchStocks := e.cfg.Get().Theme.WatchList
		if len(watchStocks) == 0 {
			watchStocks = []string{"000001", "600519", "000858", "002594"}
		}
		e.fetcher = data.NewFetcher(watchStocks, e.coord)
	}
	e.fetcher.FetchOnce()
	snap := e.fetcher.Snapshot()
	if snap != nil && len(snap.Stocks) > 0 {
		log.Printf("Prefetch: %d stocks loaded", len(snap.Stocks))
	}
	e.prefetched = true
}

// Run 启动引擎主循环。执行流程：
//  1. 启动 HTTP API 服务（若 httpAddr 非空）
//  2. 创建数据采集器并首次拉取行情快照
//  3. 以动态间隔循环执行 scanCycle
//  4. 根据交易时段自动调整扫描频率（高频/中频/午后/普通）
//  5. 收到 ctx.Done 信号时优雅关闭所有子系统
//
// 参数:
//   - ctx: 上下文，用于控制引擎终止
//   - httpAddr: HTTP 服务监听地址，为空时不启动 Web 服务
//
// 返回:
//   - 关闭时返回 ctx.Err()
func (e *Engine) Run(ctx context.Context, httpAddr string) error {
	log.Println("量化交易引擎启动 v1.0 (N形两段式)")
	if ctx == nil {
		ctx = context.Background()
	}

	if httpAddr != "" {
		e.httpSrv = server.New(e.cfg, e, e.validator, "", e.h5FS)
		if e.watchlistMgr != nil {
			e.httpSrv.SetWatchlistManager(e.watchlistMgr)
		}
		go func() {
			log.Printf("HTTP 服务启动 %s", httpAddr)
			if err := e.httpSrv.Start(httpAddr); err != nil {
				log.Printf("HTTP server error: %v", err)
			}
		}()
	}

	if e.fetcher == nil {
		watchStocks := e.cfg.Get().Theme.WatchList
		if len(watchStocks) == 0 {
			watchStocks = []string{"000001", "600519", "000858", "002594"}
		}
		e.fetcher = data.NewFetcher(watchStocks, e.coord)
	}
	// 异步加载自选股 + 首次行情 + 新闻/D1，不阻塞主循环
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC in async data load: %v", r)
			}
		}()
		if e.watchlistMgr != nil {
			for _, c := range e.watchlistMgr.List() {
				e.fetcher.EnsureStock(c)
			}
		}
		e.fetcher.FetchOnce()
		log.Println("异步行情加载完成")
		// 提前拉新闻+D1/LLM，首次 tick 前就有事件映射
		now := time.Now()
		e.processNewsAndLLM(now, data.CurrentSession(now))
		log.Println("异步新闻/D1/LLM 完成")
	}()

	// 动态扫描间隔主循环
	defer func() {
		if r := recover(); r != nil {
			log.Printf("ENGINE PANIC: %v", r)
		}
	}()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("引擎停止")
			e.fetcher.Stop()
			if e.history != nil {
				e.history.Close()
			}
			if e.httpSrv != nil {
				e.httpSrv.Shutdown(ctx)
			}
			return ctx.Err()
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						buf := make([]byte, 4096)
						n := runtime.Stack(buf, false)
						log.Printf("PANIC in scanCycle: %v\n%s", r, buf[:n])
					}
				}()
				e.scanCycle()
			}()

			// 动态调整扫描间隔
			now := time.Now()
			session := data.CurrentSession(now)
			if session != data.SessionMorningTrade && session != data.SessionAfternoonTrade {
				ticker.Reset(30 * time.Second) // 非交易时段30秒轻量采样，快速检测时段切换
			} else {
				nc := e.cfg.Get().Strategy.NShape
				next := data.ScanInterval(now, nc.HighFreqIntervalSec, nc.MidFreqIntervalSec, nc.AfternoonFreqIntervalSec, nc.NormalFreqIntervalSec)
				ticker.Reset(time.Duration(next) * time.Second)
			}
		}
	}
}

// ── NPhase 状态机 ──

// getNState 获取或创建指定股票的 N 形状态机对象。
// 如果状态不存在则初始化为 NPhaseIdle 并返回。
// 参数 code: 六位股票代码。
func (e *Engine) getNState(code string) *nStockState {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.nStates[code]
	if !ok {
		s = &nStockState{Phase: n_shape.NPhaseIdle}
		e.nStates[code] = s
	}
	return s
}

// setNPhase 设置指定股票的 N 形阶段状态。
// 参数 code: 六位股票代码；phase: 目标阶段。
func (e *Engine) setNPhase(code string, phase n_shape.NPhase) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.nStates[code]
	if !ok {
		s = &nStockState{}
		e.nStates[code] = s
	}
	s.Phase = phase
}

// resetNStates 每天开盘重置 N 形状态机和缓存。
// 清空 nStates/kLineCache，保存上日板块成交额收盘数据到 sectorAmtPrev。
func (e *Engine) resetNStates() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nStates = make(map[string]*nStockState)
	e.kLineCache = make(map[string][]data.KLine)

	// 保存上日板块成交额收盘数据
	if len(e.sectorAmtToday) > 0 {
		for k, v := range e.sectorAmtToday {
			e.sectorAmtPrev[k] = v
		}
		e.sectorAmtToday = make(map[string]float64)
	}
}

// ── 1分钟MACD ──

// updateMinuteBars 更新指定股票的1分钟K线数据。
// 同一分钟内：更新当前bar的High/Low/Close/Volume；
// 新分钟：追加新bar并裁剪超过60根的旧数据。
// 参数 price: 当前成交价；vol: 当前累计量；t: 快照时间。
func (e *Engine) updateMinuteBars(code string, price, vol float64, t time.Time) {
	min := t.Hour()*60 + t.Minute()
	if min == e.lastMinute && len(e.barBuf[code]) > 0 {
		bar := &e.barBuf[code][len(e.barBuf[code])-1]
		if price > bar.High {
			bar.High = price
		}
		if price < bar.Low {
			bar.Low = price
		}
		bar.Close = price
		bar.Volume += vol
		return
	}
	e.lastMinute = min
	bar := minuteBar{
		Time:   t,
		Open:   price,
		High:   price,
		Low:    price,
		Close:  price,
		Volume: vol,
	}
	e.barBuf[code] = append(e.barBuf[code], bar)
	if len(e.barBuf[code]) > 60 {
		e.barBuf[code] = e.barBuf[code][len(e.barBuf[code])-60:]
	}
}

// calcMinuteMACD 基于1分钟K线计算MACD指标的DIF/DEA/柱状线。
// 需要至少27根分钟K线才能计算。返回值为浮点数。
// 参数 code: 六位股票代码。
func (e *Engine) calcMinuteMACD(code string) (dif, dea, bar float64) {
	bars := e.barBuf[code]
	if len(bars) < 27 {
		return 0, 0, 0
	}
	closes := make([]float64, len(bars))
	for i, b := range bars {
		closes[i] = b.Close
	}
	e12 := ema(closes, 12)
	e26 := ema(closes, 26)
	dif = e12 - e26
	dea = ema(append([]float64{}, dif), 9)
	bar = dif - dea
	return
}

// ema 计算指数移动平均（EMA）。
// 公式：EMA = price * k + EMA_prev * (1-k)，其中 k = 2/(period+1)。
// 参数 data: 收盘价序列（从旧到新）；period: 周期数。
func ema(data []float64, period int) float64 {
	if len(data) == 0 {
		return 0
	}
	k := 2.0 / float64(period+1)
	ema := data[0]
	for i := 1; i < len(data); i++ {
		ema = data[i]*k + ema*(1-k)
	}
	return ema
}

// ── K线缓存 ──

// getCachedKLine 获取指定股票的日K线数据（带60秒缓存）。
// 缓存过期后将清空并重新从 DataCoordinator 获取最近30根K线。
// 参数 code: 六位股票代码。
func (e *Engine) getCachedKLine(code string) ([]data.KLine, error) {
	now := time.Now()
	if now.Sub(e.kLineCacheTime) > 60*time.Second {
		e.mu.Lock()
		e.kLineCache = make(map[string][]data.KLine)
		e.kLineCacheTime = now
		e.mu.Unlock()
	}

	e.mu.RLock()
	kl, ok := e.kLineCache[code]
	e.mu.RUnlock()
	if ok && len(kl) > 0 {
		return kl, nil
	}

	kl, err := e.coord.GetKLine(code, "101", 30)
	if err == nil {
		e.mu.Lock()
		e.kLineCache[code] = kl
		e.mu.Unlock()
	}
	return kl, err
}

// ── 公开 API ──

// GetAlerts 构建并返回当前需要展示的告警列表。
// 包含两部分：
//  1. 持仓标的的止盈/止损/加减仓提示（基于当前价格 vs 成本价）
//  2. 策略买入信号的推送
//
// 返回按时间顺序排列的 Alert 切片。
func (e *Engine) GetAlerts() []server.Alert {
	var out []server.Alert
	snap := e.fetcher.Snapshot()
	signals := e.GetSignals()
	signalMap := make(map[string]server.SignalView)
	for _, s := range signals {
		signalMap[s.Code] = s
	}

	held := make(map[string]bool)
	if e.holdings != nil {
		h := e.holdings.Get()
		for _, hh := range h.Holdings {
			held[hh.Code] = true
			// 优先使用快照名称（覆盖 holdings.json 中陈旧的"未找到"）
			name := hh.Name
			var curPrice float64
			if snap != nil {
				if si, ok := snap.Stocks[hh.Code]; ok && si != nil {
					if si.Name != "" {
						name = si.Name
					}
					if si.Price > 0 {
						curPrice = si.Price
					}
				}
			}
			if curPrice <= 0 {
				if si, err := e.coord.GetQuote(hh.Code); err == nil && si != nil && si.Price > 0 {
					curPrice = si.Price
					if si.Name != "" {
						name = si.Name
					}
				}
			}
			if curPrice <= 0 {
				curPrice = hh.CostPrice
			}
			pnl := (curPrice - hh.CostPrice) / hh.CostPrice * 100
			action := ""
			level := "持有"
			if sig, ok := signalMap[hh.Code]; ok && sig.Action == "sell" {
				action = "卖出"
				level = "减仓"
			} else if sig, ok := signalMap[hh.Code]; ok && sig.Action == "buy" {
				action = "买入"
				level = "加仓"
			} else if er, ok := e.exitResults[hh.Code]; ok {
				action = "卖出"
				level = er.Reason
			} else if hh.CostPrice > 0 && curPrice <= hh.CostPrice*0.955 {
				action = "卖出"
				level = "硬止损"
			}
			body := fmt.Sprintf("[%s] 仓位%d股 成本%.2f 现价%.2f (%.1f%%)", action, hh.Quantity, hh.CostPrice, curPrice, pnl)
			title := fmt.Sprintf("%s %s %s", name, action, level)
			out = append(out, server.Alert{
				Time: time.Now().Format("15:04:05"), Code: hh.Code, Name: name,
				Title: title, Body: body, Score: pnl, D4: curPrice, Level: "持仓提示",
			})
		}
	}

	for _, sig := range signals {
		if held[sig.Code] {
			continue
		}
		if sig.Action != "buy" {
			continue
		}
		price := sig.Price
		if price <= 0 {
			if si, err := e.coord.GetQuote(sig.Code); err == nil && si != nil && si.Price > 0 {
				price = si.Price
			}
		}
		title := fmt.Sprintf("%s 买入 开仓", sig.Name)
		body := fmt.Sprintf("买入 %.2f | %s 评分%.1f", price, sig.Strategy, sig.TotalScore)
		out = append(out, server.Alert{
			Time: time.Now().Format("15:04:05"), Code: sig.Code, Name: sig.Name,
			Title: title, Body: body, Score: sig.TotalScore, D4: price, Level: "策略信号",
		})
	}
	return out
}

// GetAlertLog 返回历史告警日志的副本，供前端 API 调用。
func (e *Engine) GetAlertLog() []server.Alert {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]server.Alert, len(e.alertLog))
	copy(out, e.alertLog)
	return out
}

// GetHoldings 返回当前持仓数据的视图，包含每个持仓标的的代码/名称/数量/成本/现价/盈亏百分比。
// 现价优先从快照中获取，其次通过 GetQuote 单独查询。
func (e *Engine) GetHoldings() server.HoldingsData {
	if e.holdings == nil {
		return server.HoldingsData{}
	}
	h := e.holdings.Get()
	snap := e.fetcher.Snapshot()
	items := make([]server.HoldingItem, len(h.Holdings))
	for i, hh := range h.Holdings {
		code := strings.TrimSuffix(strings.TrimSuffix(hh.Code, ".SZ"), ".SH")
		curPrice := hh.CurPrice
		if snap != nil {
			if si, ok := snap.Stocks[code]; ok && si.Price > 0 {
				curPrice = si.Price
			}
		}
		if curPrice <= 0 {
			if si, err := e.coord.GetQuote(code); err == nil && si != nil && si.Price > 0 {
				curPrice = si.Price
			}
		}
		// 始终尝试补名称（覆盖前端传的代码占位）
		name := ""
		if snap != nil {
			if si, ok := snap.Stocks[code]; ok && si != nil && si.Name != "" {
				name = si.Name
			}
		}
		if name == "" {
			if si, err := e.coord.GetQuote(code); err == nil && si != nil && si.Name != "" {
				name = si.Name
			}
		}
		if name == "" {
			name = hh.Name
		}
		changePct := 0.0
		if snap != nil {
			if si, ok := snap.Stocks[code]; ok && si != nil {
				changePct = si.ChangePct
			}
		}
		pnl := 0.0
		if hh.CostPrice > 0 && curPrice > 0 {
			pnl = (curPrice - hh.CostPrice) / hh.CostPrice * 100
		}
		items[i] = server.HoldingItem{Code: hh.Code, Name: name, Quantity: hh.Quantity, CostPrice: hh.CostPrice, CurPrice: curPrice, PnlPct: pnl, ChangePct: changePct}
	}
	return server.HoldingsData{
		UpdatedAt: h.UpdatedAt.Format("2006-01-02 15:04:05"), AvailableBalance: h.AvailableBalance, Holdings: items,
	}
}

// SetHoldings 从前端接收并持久化持仓数据。
// 将 server.HoldingsData 转换为 position.UserHoldings 后存入 HoldingsManager。
func (e *Engine) SetHoldings(h server.HoldingsData) error {
	if e.holdings == nil {
		return fmt.Errorf("持仓管理器未就绪")
	}
	items := make([]position.UserHolding, len(h.Holdings))
	for i, hh := range h.Holdings {
		items[i] = position.UserHolding{
			Code: hh.Code, Name: hh.Name, Quantity: hh.Quantity, CostPrice: hh.CostPrice,
			CurPrice: hh.CurPrice, PnlPct: hh.PnlPct,
			TakeProfitPct: hh.TakeProfitPct, StopLossPct: hh.StopLossPct,
		}
	}
	return e.holdings.Set(position.UserHoldings{AvailableBalance: h.AvailableBalance, Holdings: items})
}

func (e *Engine) GetWatchlistMgr() *data.WatchlistManager {
	return e.watchlistMgr
}

// WatchlistAddStock 添加自选股并立即获取行情数据。
// 前端添加自选后需要立即显示名称和股价，不能等下次 scanCycle。
func (e *Engine) WatchlistAddStock(code string) error {
	if e.watchlistMgr == nil {
		return fmt.Errorf("自选管理未就绪")
	}
	if err := e.watchlistMgr.Add(code); err != nil {
		return err
	}
	if e.fetcher != nil {
		e.fetcher.EnsureStock(code)
	}
	return nil
}

// StartServices 登录成功后触发，如果预拉还没完成则补拉。
func (e *Engine) StartServices() {
	e.mu.Lock()
	if e.dataStarted {
		e.mu.Unlock()
		return
	}
	e.dataStarted = true
	e.mu.Unlock()

	snap := e.fetcher.Snapshot()
	if snap != nil && len(snap.Stocks) > 0 {
		log.Println("数据已在预拉中就绪")
		return
	}
	log.Println("补拉数据（登录触发）")
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC in StartServices scanCycle: %v", r)
			}
		}()
		e.fetcher.FetchOnce()
		snap := e.fetcher.Snapshot()
		if snap != nil && len(snap.Stocks) > 0 {
			e.scanCycle()
		}
	}()
}

// GetSignals 返回当前 scanCycle 输出的最终信号视图列表（按总分降序排列）。
func (e *Engine) GetSignals() []server.SignalView {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]server.SignalView, len(e.signals))
	copy(out, e.signals)
	return out
}

// SubscribeEvents 注册 SSE 事件通道。引擎扫描出的信号实时写入该通道（JSON 格式），
// 供前端 EventSource 消费。ctx 取消时自动注销 notifier 并关闭通道。
func (e *Engine) SubscribeEvents(ctx context.Context) chan string {
	ch := make(chan string, 200)
	id := fmt.Sprintf("sse:%d_%d", os.Getpid(), time.Now().UnixNano())
	notifyCh := e.notifier.RegisterWS(id)
	go func() {
		defer close(ch)
		defer e.notifier.UnregisterWS(id)
		for {
			select {
			case msg, ok := <-notifyCh:
				if !ok {
					return
				}
				data, err := json.Marshal(msg)
				if err != nil {
					continue
				}
				select {
				case ch <- string(data):
				default:
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}

// GetStatus 返回引擎运行状态，包含：运行时长/信号数量/是否交易时段/
// 最近扫描时间/观测股票数/数据源名称/扫描统计等字段。
func (e *Engine) GetStatus() server.StatusView {
	e.mu.RLock()
	sigCount := len(e.signals)
	ss := e.scanStats
	e.mu.RUnlock()
	now := time.Now()
	sess := data.CurrentSession(now)
	s := server.StatusView{
		Running: true, Uptime: time.Since(e.startAt).Round(time.Second).String(),
		SignalCount: sigCount, InTradeTime: data.IsTradeTime(now), LastScan: e.lastScan.Format("15:04:05"),
		Session: int(sess), SessionName: sess.String(),
	}
	if e.fetcher != nil {
		if snap := e.fetcher.Snapshot(); snap != nil {
			s.LastData = snap.Time.Format("15:04:05")
			s.StocksWatched = len(snap.Stocks)
		}
	}
	s.DataSource = e.coord.SourceName()
	if ss.TotalStocks > 0 {
		s.ScanStats = &ss
	}
	return s
}

// LookupStock 查询单只股票的基本信息（名称/现价/市盈率）。
// 先尝试从快照中获取，快照不存在时调用 GetQuote 单独查询。
// 参数 code: 六位股票代码。
func (e *Engine) LookupStock(code string) *server.StockLookupItem {
	item := &server.StockLookupItem{Code: code}
	// 去掉 .SZ/.SH 后缀
	code = strings.TrimSuffix(strings.TrimSuffix(code, ".SZ"), ".SH")
	snap := e.fetcher.Snapshot()
	if snap != nil {
		if si, ok := snap.Stocks[code]; ok && si != nil {
			item.Name = si.Name
			item.Price = si.Price
		}
	}
	if item.Name == "" {
		si, err := e.coord.GetQuote(code)
		if err == nil && si != nil {
			item.Name = si.Name
			item.Price = si.Price
		}
	}
	fi, err := e.coord.GetFinancial(code)
	if err == nil && fi != nil {
		item.PE = fi.PE
	}
	return item
}

// GetWatchlistEnriched 批量查询自选股列表的名称和现价。
// 参数 codes: 要查询的股票代码切片。
func (e *Engine) GetWatchlistEnriched(codes []string) []server.StockLookupItem {
	items := make([]server.StockLookupItem, 0, len(codes))
	snap := e.fetcher.Snapshot()
	for _, code := range codes {
		item := server.StockLookupItem{Code: code}
		if snap != nil {
			if si, ok := snap.Stocks[code]; ok && si != nil {
				item.Name = si.Name
				item.Price = si.Price
				item.ChangePct = si.ChangePct
			}
		}
		if item.Name == "" {
			si, err := e.coord.GetQuote(code)
			if err == nil && si != nil {
				item.Name = si.Name
				item.Price = si.Price
				item.ChangePct = si.ChangePct
			}
		}
		items = append(items, item)
	}
	return items
}

// fSeriesKeywords F 系列关键词，与 D1 并列用于新闻过滤（合集：任一命中即保留）。
var fSeriesKeywords = []string{
	"涨停", "跌停", "连板", "封板", "炸板", "回封",
	"龙头", "补涨", "接力", "梯队",
	"高开", "低开", "竞价", "开盘",
	"放量", "缩量", "天量", "地量",
	"突破", "新高", "破位", "支撑",
	"反包", "弱转强", "分歧转一致",
	"主升", "主浪", "调整", "回踩",
	"溢价", "折价", "换手",
	"板块", "题材", "主线", "轮动",
	"利好", "利空", "超预期", "不及预期",
}

// newsWhitelist 资讯白名单关键词，命中任一条即保留。
var newsWhitelist = []string{
	"A股", "沪指", "深成指", "创业板", "科创板",
	"股市", "行情", "涨停", "跌停", "大盘",
	"北上资金", "主力资金", "两市成交", "成交量",
	"央行", "证监会", "国务院", "财政部",
	"降准", "降息", "加息", "LPR", "MLF", "逆回购",
	"美联储", "鲍威尔", "利率", "美元",
	"CPI", "PPI", "GDP", "PMI", "通胀", "通缩",
	"黄金", "金价", "原油", "油价",
	"美股", "纳斯达克", "标普", "道指",
	"英伟达", "苹果", "微软", "特斯拉", "谷歌", "亚马逊", "Meta",
	"三星", "SK海力士", "现代",
	"中美", "贸易", "关税", "出口",
	"产业", "政策", "利好", "利空", "业绩",
	"期货", "证券", "基金", "机构",
	"科技", "芯片", "半导体", "AI", "人工智能",
	"新能源", "光伏", "锂电", "汽车",
	"医药", "医疗", "消费", "地产",
}

// filterEventNews 统一新闻过滤：D1 + fSeriesKeywords + 全量板块名称作为动态关键词。
// 只返回至少匹配一个板块/关键词的新闻（去除量价噪音）。
func (e *Engine) filterEventNews(news []data.NewsItem) []data.NewsItem {
	kw := e.eventKeywords()
	var out []data.NewsItem
	for _, n := range news {
		if len(n.Title) < 4 {
			continue
		}
		keep := false
		if e.matcher != nil {
			mr := e.matcher.MatchD1(n.Title)
			if !mr.Blocked && mr.Score > 0 {
				keep = true
			}
		}
		if !keep {
			titleLower := strings.ToLower(n.Title)
			for _, k := range kw {
				if strings.Contains(titleLower, strings.ToLower(k)) {
					keep = true
					break
				}
			}
		}
		if keep {
			out = append(out, n)
		}
	}
	return out
}

// eventKeywords 生成关键词列表：D1催化剂 + fSeriesKeywords + 所有板块名称（从 GetSectors() 获取）。
// 动态：板块列表运行时获取，无需硬编码。
func (e *Engine) eventKeywords() []string {
	base := fSeriesKeywords
	sectors, err := e.coord.GetSectors()
	if err != nil || len(sectors) == 0 {
		return base
	}
	seen := make(map[string]bool, len(base))
	for _, k := range base {
		seen[strings.ToLower(k)] = true
	}
	for _, s := range sectors {
		name := strings.TrimSpace(s.Name)
		if name != "" && !seen[strings.ToLower(name)] {
			base = append(base, name)
			seen[strings.ToLower(name)] = true
		}
	}
	return base
}

// GetAllNews 返回所有未经过滤的原始新闻（从全量数据源拉取）。
// 仅用于内部投研处理，不对外暴露给前端仪表盘展示。
func (e *Engine) GetAllNews() []data.NewsItem {
	allNews := e.filterEventNews(e.coord.GetHotNews(50))
	if len(allNews) == 0 {
		log.Printf("GetAllNews: GetHotNews returned empty")
		return allNews
	}
	out := make([]data.NewsItem, 0, len(allNews))
	for _, n := range allNews {
		e.mu.RLock()
		if ht, ok := e.llmTopicCache[truncate(n.Title, 60)]; ok {
			n.SentimentScore = ht.Score
			n.Sentiment = ht.Sentiment
			n.ImpactLevel = ht.ImpactLevel
			n.EventType = ht.EventType
			n.Urgency = ht.Urgency
			n.Direction = ht.Direction
			n.Sectors = ht.Sectors
			n.Stocks = ht.Stocks
			n.Strategy = ht.Strategy
			n.Reason = ht.Reason
		}
		e.mu.RUnlock()
		out = append(out, n)
	}
	if len(out) > 50 {
		out = out[:50]
	}
	// 追加日历事件（前后3天常驻）
	if e.macroCal != nil {
		calCfg := e.cfg.Get().Calendar
		if calCfg.Enabled {
			now := time.Now()
			for _, ce := range calCfg.Events {
				t, err := time.Parse("2006-01-02", ce.Date)
				if err != nil {
					continue
				}
				until := t.Sub(now)
				daysUntil := int(until.Hours() / 24)
				if daysUntil < -3 || daysUntil > 3 {
					continue
				}
				impactDays := 3
				startStr := t.AddDate(0, 0, -impactDays).Format("01-02")
				endStr := t.AddDate(0, 0, impactDays).Format("01-02")
				prefix := "🔴"
				if ce.Impact == "medium" {
					prefix = "🟡"
				} else if ce.Impact == "low" {
					prefix = "🟢"
				}
				title := fmt.Sprintf("%s %s | %s (影响期 %s~%s)", prefix, t.Format("01-02"), ce.Title, startStr, endStr)
				out = append(out, data.NewsItem{
					Title:    title,
					Datetime: t.Format("2006-01-02 15:04"),
					Source:   "宏观日历",
				})
			}
		}
	}
	return out
}

// getAllSectors 从快照或 coord 获取板块数据，优先 coord 直查以保证数据新鲜。
func getAllSectors(snap *data.MarketSnapshot, coord *data.DataCoordinator) []data.SectorInfo {
	if snap != nil && len(snap.Sector) > 0 {
		return snap.Sector
	}
	if coord != nil {
		s, err := coord.GetSectors()
		if err == nil && len(s) > 0 {
			return s
		}
	}
	return nil
}

// GetNews 返回经 filterEventNews 过滤后的新闻，附带 LLM 方向/板块标签。
// 每条新闻携带 direction、reason 和 matched sectors，供前端仪表盘直接渲染。
// 仅保留命中 D1/关键词/板块的事件型新闻。
func (e *Engine) GetNews() []data.NewsItem {
	allNews := e.filterEventNews(e.coord.GetHotNews(50))
	if len(allNews) == 0 {
		return allNews
	}
	filtered := make([]data.NewsItem, 0, len(allNews))
	for _, n := range allNews {
		if len(n.Title) < 4 {
			continue
		}
		e.mu.RLock()
		if ht, ok := e.llmTopicCache[truncate(n.Title, 60)]; ok {
			n.SentimentScore = ht.Score
			n.Sentiment = ht.Sentiment
			n.ImpactLevel = ht.ImpactLevel
			n.EventType = ht.EventType
			n.Urgency = ht.Urgency
			n.Direction = ht.Direction
			n.Sectors = ht.Sectors
			n.Stocks = ht.Stocks
			n.Strategy = ht.Strategy
			n.Reason = ht.Reason
		}
		e.mu.RUnlock()
		filtered = append(filtered, n)
	}
	if len(filtered) > 30 {
		filtered = filtered[:30]
	}
	return filtered
}

// GetNStates 返回所有股票的 N 形阶段映射（code → Phase 数值），供前端调试展示。
func (e *Engine) GetNStates() map[string]int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make(map[string]int)
	for code, s := range e.nStates {
		out[code] = int(s.Phase)
	}
	return out
}

// GetStockEvals 返回全量个股评分（含未通过策略门槛的原始分）。
// 非交易时段 stockEvals 可能为空，此时回退到快照数据提供基础行情。
func (e *Engine) GetStockEvals() []server.StockEval {
	e.mu.RLock()
	if len(e.stockEvals) > 0 {
		out := make([]server.StockEval, len(e.stockEvals))
		copy(out, e.stockEvals)
		e.mu.RUnlock()
		return out
	}
	e.mu.RUnlock()

	// 非交易时段回退：从 fetcher 快照构建轻量 evaluations
	snap := e.fetcher.Snapshot()
	if snap == nil || len(snap.Stocks) == 0 {
		return nil
	}
	out := make([]server.StockEval, 0, len(snap.Stocks))
	for _, si := range snap.Stocks {
		if si != nil && si.Price > 0 {
			// 与 evaluateAll 一致：黑名单 + 老登分过滤，防止降级路径漏出老登股
			if e.isStaleStock(si.Code) {
				continue
			}
			out = append(out, server.StockEval{
				Code:      si.Code,
				Name:      si.Name,
				Price:     si.Price,
				ChangePct: si.ChangePct,
				MScore:    0,
				MPass:     false,
			})
		}
	}
	return out
}

// GetSnapshotStocks 返回所有监控股票的行情（base+hot去重），按动量分倒序。
func (e *Engine) GetSnapshotStocks() []server.SnapshotStock {
	snap := e.fetcher.Snapshot()
	if snap == nil || len(snap.Stocks) == 0 {
		return nil
	}
	// 构建代码→动量分映射
	e.mu.RLock()
	mScoreMap := make(map[string]float64, len(e.stockEvals))
	for _, ev := range e.stockEvals {
		mScoreMap[ev.Code] = ev.MScore
	}
	e.mu.RUnlock()

	out := make([]server.SnapshotStock, 0, len(snap.Stocks))
	for _, si := range snap.Stocks {
		if si != nil {
			out = append(out, server.SnapshotStock{
				Code:      si.Code,
				Name:      si.Name,
				Price:     si.Price,
				ChangePct: si.ChangePct,
				Volume:    si.Volume,
				Amount:    si.Amount,
				MScore:    mScoreMap[si.Code],
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].MScore > out[j].MScore
	})
	return out
}

// GetHotSnapshotStocks 返回热点板块个股的行情快照（仅热门口）。
func (e *Engine) GetHotSnapshotStocks() []server.SnapshotStock {
	hotCodes := e.fetcher.HotStocks()
	if len(hotCodes) == 0 {
		return nil
	}
	hotSet := make(map[string]bool, len(hotCodes))
	for _, c := range hotCodes {
		hotSet[c] = true
	}
	// 黑名单 + 老登黑名单过滤
	cfg := e.cfg.Get().Theme
	for _, blk := range cfg.BlackList {
		delete(hotSet, blk)
	}
	for _, blk := range cfg.StaleBlackList {
		delete(hotSet, blk)
	}
	// 老登分过滤（MC/PE/换手率/波动率）
	for code := range hotSet {
		if e.isStaleStock(code) {
			delete(hotSet, code)
		}
	}
	// 如果热门口 √6 都得 0 分，硬邦邦放回 max_market_cap 硬检查
	if cfg.MaxMarketCap > 0 {
		for code := range hotSet {
			fi, _ := e.coord.GetFinancial(code)
			if fi != nil && fi.MarketCap > 0 && fi.MarketCap > cfg.MaxMarketCap {
				delete(hotSet, code)
			}
		}
	}
	if len(hotSet) == 0 {
		return nil
	}
	snap := e.fetcher.Snapshot()
	if snap == nil || len(snap.Stocks) == 0 {
		return nil
	}
	e.mu.RLock()
	mScoreMap := make(map[string]float64, len(e.stockEvals))
	for _, ev := range e.stockEvals {
		mScoreMap[ev.Code] = ev.MScore
	}
	srcMap := e.stockSectorSrc
	e.mu.RUnlock()
	out := make([]server.SnapshotStock, 0, len(hotCodes))
	for _, si := range snap.Stocks {
		if si != nil && si.Price > 0 && hotSet[si.Code] {
			item := server.SnapshotStock{
				Code: si.Code, Name: si.Name,
				Price: si.Price, ChangePct: si.ChangePct,
				Volume: si.Volume, Amount: si.Amount,
				MScore: mScoreMap[si.Code],
			}
			if src, ok := srcMap[si.Code]; ok {
				item.Sector = src.Sector
				item.SectorReason = src.Reason
			} else if si.Sector != "" {
				item.Sector = si.Sector
			}
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].MScore > out[j].MScore
	})
	return out
}

// isStaleStock 老登分累计检查：市值+PE+20日均换手率+20日波动率，≥阈值返回 true。
func (e *Engine) isStaleStock(code string) bool {
	cfgTheme := e.cfg.Get().Theme
	// 黑名单 + 老登黑名单快检：先于老登分，确保金融数据不可用时有兜底
	for _, blk := range cfgTheme.BlackList {
		if code == blk {
			return true
		}
	}
	for _, blk := range cfgTheme.StaleBlackList {
		if code == blk {
			return true
		}
	}

	fi, _ := e.coord.GetFinancial(code)
	score := 0
	if fi != nil && fi.MarketCap > 0 && cfgTheme.MaxMarketCap > 0 && fi.MarketCap > cfgTheme.MaxMarketCap {
		score += 2
	}
	if fi != nil && fi.PE > 0 && cfgTheme.MinPE > 0 && fi.PE < cfgTheme.MinPE {
		score += 2
	}

	si, _ := e.coord.GetQuote(code)
	kLines, _ := e.getCachedKLine(code)
	if cfgTheme.MinTurnover > 0 && si != nil && si.Turnover > 0.1 && si.Volume > 0 && len(kLines) >= 2 {
		floatShares := si.Volume / (si.Turnover / 100.0)
		n := len(kLines)
		start := n - 20
		if start < 0 {
			start = 0
		}
		var turnoverSum float64
		var turnoverCnt int
		for i := start; i < n; i++ {
			if kLines[i].Volume > 0 {
				turnoverSum += kLines[i].Volume / floatShares * 100.0
				turnoverCnt++
			}
		}
		if turnoverCnt > 0 && (turnoverSum/float64(turnoverCnt)) < cfgTheme.MinTurnover {
			score += 2
		}
	}
	if cfgTheme.MinVol20d > 0 && len(kLines) >= 21 {
		n := len(kLines)
		start := n - 20
		returns := make([]float64, 0, 20)
		for i := start; i < n; i++ {
			if kLines[i-1].Close > 0 {
				r := (kLines[i].Close - kLines[i-1].Close) / kLines[i-1].Close * 100.0
				returns = append(returns, r)
			}
		}
		if len(returns) >= 10 {
			var mean, sqSum float64
			for _, r := range returns {
				mean += r
			}
			mean /= float64(len(returns))
			for _, r := range returns {
				d := r - mean
				sqSum += d * d
			}
			stddev := math.Sqrt(sqSum / float64(len(returns)))
			if stddev < cfgTheme.MinVol20d {
				score += 2
			}
		}
	}

	return cfgTheme.StaleScoreThreshold > 0 && score >= cfgTheme.StaleScoreThreshold
}

// HandleAction 处理用户交易确认（buy/sell/ignore）。
func (e *Engine) HandleAction(code, action string) error {
	log.Printf("用户操作: %s %s", action, code)
	switch action {
	case "buy":
		e.mu.RLock()
		var sig *server.SignalView
		for i := range e.signals {
			if e.signals[i].Code == code {
				sig = &e.signals[i]
				break
			}
		}
		e.mu.RUnlock()
		if sig == nil {
			return fmt.Errorf("信号 %s 已过期", code)
		}
		meta := make(map[string]float64)
		meta["entry_score"] = sig.TotalScore
		switch sig.Strategy {
		case "n_shape":
			if sig.LeftSignal {
				meta["entry_phase"] = 1
			} else {
				meta["entry_phase"] = 2
			}
			meta["n_score"] = sig.NScore
			meta["entry_nphase"] = float64(sig.NPhase)
		case "dragon":
			meta["dragon_score"] = sig.DragonScore
		case "double_bump":
			meta["db_score"] = sig.DBScore
		case "dragon_return":
			meta["dr_score"] = sig.DRScore
		}
		h := e.holdings.Get()
		h.Holdings = append(h.Holdings, position.UserHolding{
			Code:          code,
			Name:          sig.Name,
			Quantity:      100,
			CostPrice:     sig.Price,
			EntryStrategy: sig.Strategy,
			EntryAt:       time.Now().Format("2006-01-02"),
			EntryMeta:     meta,
		})
		h.AvailableBalance -= sig.Price * 100
		return e.holdings.Set(h)

	case "sell":
		h := e.holdings.Get()
		for i, item := range h.Holdings {
			if item.Code == code {
				h.Holdings = append(h.Holdings[:i], h.Holdings[i+1:]...)
				h.AvailableBalance += item.CurPrice * float64(item.Quantity)
				return e.holdings.Set(h)
			}
		}
		return fmt.Errorf("持仓中未找到 %s", code)

	case "ignore":
		return nil

	default:
		return fmt.Errorf("未知操作: %s", action)
	}
}

// hotStockSource 热点个股来源追溯：由哪个热点板块带入 + 该板块的上榜原因。
type hotStockSource struct {
	Sector string
	Reason string
}

// expandFromHotSectors 将热点板块中的个股筛选后加入监控列表。
// 被 scanCycle 调用，确保热门板块的标的纳入数据采集范围，不漏掉潜在机会。
func (e *Engine) expandFromHotSectors(hotSectors []data.HotSector, baseList []string, cfg *config.Rules) {
	var scoredCodes []string
	tempSrc := make(map[string]hotStockSource)
	for _, hs := range hotSectors {
		topN := 5
		if len(hotSectors) > 3 {
			topN = 3
		}
		scored, err := e.sectorScan.ScoreSectorStocks(hs.Sector.Code, topN)
		if err != nil || len(scored) == 0 {
			continue
		}
		for _, s := range scored {
			scoredCodes = append(scoredCodes, s.Code)
			tempSrc[s.Code] = hotStockSource{Sector: hs.Sector.Name, Reason: hs.Reason}
		}
	}
	if len(scoredCodes) > 0 {
		allBlk := append([]string{}, cfg.Theme.BlackList...)
		allBlk = append(allBlk, cfg.Theme.StaleBlackList...)
		blkSet := make(map[string]bool, len(allBlk))
		for _, blk := range allBlk {
			blkSet[blk] = true
		}
		filtered := scoredCodes[:0]
		for _, c := range scoredCodes {
			if blkSet[c] {
				delete(tempSrc, c)
				continue
			}
			if e.isStaleStock(c) {
				delete(tempSrc, c)
				continue
			}
			filtered = append(filtered, c)
		}
		scoredCodes = filtered
	}
	if len(scoredCodes) > 0 {
		e.mu.Lock()
		e.sectorStockCnt = len(scoredCodes)
		e.stockSectorSrc = tempSrc
		e.mu.Unlock()
		e.fetcher.UpdateHotStocks(scoredCodes)
		log.Printf("盘中热点→个股(打分筛选): %d个板块→%d只热点股票(自选+持仓%d只不计上限)", len(hotSectors), len(scoredCodes), len(baseList))
		e.notifyChain(hotSectors, scoredCodes)
	} else {
		e.mu.Lock()
		e.stockSectorSrc = nil
		e.mu.Unlock()
	}
}

// sectorTrace 板块上榜信源：label 为卡片标签（LLM总结优先），detail 为点击详情（新闻源头）。
type sectorTrace struct {
	label  string
	detail string
}

// newsTraceDetail 构建新闻源头详情文本：来源 + 时间 + 标题。
func newsTraceDetail(n data.NewsItem) string {
	src := n.Source
	if src == "" {
		src = "未知来源"
	}
	dt := n.Datetime
	if len(dt) > 16 {
		dt = dt[:16]
	}
	return fmt.Sprintf("【%s】%s\n%s", src, dt, n.Title)
}

// traceFromNews 从新闻构建信源：标签优先用 LLM 分析理由，详情附原始标题+来源。
func (e *Engine) traceFromNews(n data.NewsItem) sectorTrace {
	tr := sectorTrace{label: n.Title, detail: newsTraceDetail(n)}
	e.mu.RLock()
	if ht, ok := e.llmTopicCache[truncate(n.Title, 60)]; ok && ht != nil && ht.Reason != "" {
		tr.label = ht.Reason
		tr.detail += "\nLLM分析: " + ht.Reason
	}
	e.mu.RUnlock()
	return tr
}

// GetHotSectors 返回热点板块，附带 LLM reason 文本、D1 评分和板块事件标签。
// 合并三条信源：scanner 关注板块 → 事件匹配新闻 → LLM 热点板块评分。
// llmHotSectors 的 LLM 分数在最后阶段合并到输出结果中。
var ( // 板块缓存，防渲染闪烁
	lastSectors   []server.SectorHotView
	lastSectorsAt time.Time
)

func (e *Engine) GetHotSectors() []server.SectorHotView {
	// 缓存 5 分钟
	if len(lastSectors) > 0 && time.Since(lastSectorsAt) < 5*time.Minute {
		return lastSectors
	}
	if e.sectorScan == nil || e.coord == nil {
		return lastSectors
	}

	// 收集需要关注的板块代码（D1+F过滤 + LLM提取），value 为信源追溯信息
	wanted := make(map[string]sectorTrace) // code/name → 信源

	// 1. D1+F 过滤新闻提取
	for _, n := range e.filterEventNews(e.coord.GetHotNews(20)) {
		tr := e.traceFromNews(n)
		eventMap := e.sectorScan.BuildEventMapFromNews([]data.NewsItem{n}, nil)
		for code := range eventMap {
			if _, ok := wanted[code]; !ok {
				wanted[code] = tr
			}
		}
	}
	// 2. LLM 提取的板块：Title=新闻原标题（信源），Reason=LLM总结（标签）
	e.mu.RLock()
	llmTopicCnt := 0
	llmSectorCnt := 0
	for _, ht := range e.llmTopicCache {
		if ht == nil {
			continue
		}
		llmTopicCnt++
		tr := sectorTrace{label: ht.Title, detail: "新闻: " + ht.Title}
		if ht.Reason != "" {
			tr.label = ht.Reason
			tr.detail += "\nLLM分析: " + ht.Reason
		}
		for _, sec := range ht.Sectors {
			llmSectorCnt++
			if _, ok := wanted[sec]; !ok {
				wanted[sec] = tr
			}
		}
	}
	e.mu.RUnlock()

	if len(wanted) == 0 {
		log.Printf("GetHotSectors wanted为空: llmTopics=%d llmSectors=%d — 降级到scanner量能数据", llmTopicCnt, llmSectorCnt)
		// D1+F/LLM 无匹配时，降级使用 sectorScan 数据
		if scanSectors := e.sectorScan.HotSectors(); len(scanSectors) > 0 {
			out := make([]server.SectorHotView, 0, len(scanSectors))
			hasData := false
			for _, hs := range scanSectors {
				label, detail := e.sectorFallbackTrace(hs)
				if hs.Score > 0 || hs.D1 > 0 || hs.Sector.ChangePct != 0 || hs.Sector.LimitupCnt > 0 || hs.Sector.NetInflow != 0 {
					hasData = true
				}
				out = append(out, server.SectorHotView{
					Code:         hs.Sector.Code,
					Name:         hs.Sector.Name,
					Reason:       label,
					ReasonDetail: detail,
					Score:        hs.Score,
					D1:           hs.D1,
					ChangePct:    hs.Sector.ChangePct,
					Amount:       hs.Sector.Amount,
					LimitupCnt:   float64(hs.Sector.LimitupCnt),
					NetInflow:    hs.Sector.NetInflow,
				})
			}
			if hasData {
				lastSectors = out
				lastSectorsAt = time.Now()
			}
			return out
		}
		return lastSectors
	}

	// 查全量板块行情（同花顺+东财合并）
	allSectors, err := e.coord.GetSectors()
	if err != nil || len(allSectors) == 0 {
		return lastSectors
	}

	// 扫描器评分查找表（补 Score/D1 字段）
	scoreByCode := make(map[string]data.HotSector)
	for _, hs := range e.sectorScan.HotSectors() {
		scoreByCode[hs.Sector.Code] = hs
	}

	out := make([]server.SectorHotView, 0, len(wanted))
	for _, s := range allSectors {
		if tr, ok := wanted[s.Code]; ok {
			v := server.SectorHotView{
				Code: s.Code, Name: s.Name,
				Reason:       tr.label,
				ReasonDetail: tr.detail,
				ChangePct:    s.ChangePct, Amount: s.Amount,
				LimitupCnt: float64(s.LimitupCnt), NetInflow: s.NetInflow,
			}
			if hs, ok2 := scoreByCode[s.Code]; ok2 {
				v.Score, v.D1 = hs.Score, hs.D1
			}
			out = append(out, v)
			delete(wanted, s.Code)
		} else if tr, ok := wanted[s.Name]; ok {
			v := server.SectorHotView{
				Code: s.Code, Name: s.Name,
				Reason:       tr.label,
				ReasonDetail: tr.detail,
				ChangePct:    s.ChangePct, Amount: s.Amount,
				LimitupCnt: float64(s.LimitupCnt), NetInflow: s.NetInflow,
			}
			if hs, ok2 := scoreByCode[s.Code]; ok2 {
				v.Score, v.D1 = hs.Score, hs.D1
			}
			out = append(out, v)
			delete(wanted, s.Name)
		}
	}
	// 对未匹配的 wanted 名做模糊匹配
	if len(wanted) > 0 {
		for _, s := range allSectors {
			for name, tr := range wanted {
				name = cleanSectorName(name)
				if name == "" {
					continue
				}
				if strings.Contains(s.Name, name) || strings.Contains(name, s.Name) {
					v := server.SectorHotView{
						Code: s.Code, Name: s.Name,
						Reason:       tr.label,
						ReasonDetail: tr.detail,
						ChangePct:    s.ChangePct, Amount: s.Amount,
						LimitupCnt: float64(s.LimitupCnt), NetInflow: s.NetInflow,
					}
					if hs, ok2 := scoreByCode[s.Code]; ok2 {
						v.Score, v.D1 = hs.Score, hs.D1
					}
					out = append(out, v)
					delete(wanted, name)
					break
				}
			}
		}
	}
	hasData := false
	for _, v := range out {
		if v.ChangePct != 0 || v.LimitupCnt > 0 || v.NetInflow != 0 || v.Score > 0 || v.D1 > 0 {
			hasData = true
			break
		}
	}
	if len(out) > 0 && hasData {
		lastSectors = out
		lastSectorsAt = time.Now()
	}
	return out
}

// sectorFallbackTrace 为扫描器降级路径的板块构建信源标签+详情。
// D1>0 时用真实事件描述（优先 LLM 总结）；纯量价时诚实标注"无关联资讯"并给出驱动指标。
func (e *Engine) sectorFallbackTrace(hs data.HotSector) (label, detail string) {
	if hs.D1 > 0 {
		if desc := e.sectorScan.EventDesc(hs.Sector.Code); desc != "" {
			label = desc
			detail = "事件: " + desc
			e.mu.RLock()
			if ht, ok := e.llmTopicCache[truncate(desc, 60)]; ok && ht != nil && ht.Reason != "" {
				label = ht.Reason
				detail += "\nLLM分析: " + ht.Reason
			}
			e.mu.RUnlock()
			return label, detail
		}
	}
	// 纯量价驱动：无新闻信源，展示量化指标作为上榜依据
	label = hs.Reason
	if label == "" {
		label = hs.Sector.Name + " 板块走强"
	}
	inflow := hs.Sector.NetInflow / 1e8
	detail = fmt.Sprintf("无关联资讯，纯量价驱动\n涨幅: %.2f%%\n涨停: %d家\n主力净流入: %.1f亿",
		hs.Sector.ChangePct, hs.Sector.LimitupCnt, inflow)
	return label, detail
}

// GetIPOCalendar 返回新股日历数据。
func (e *Engine) GetIPOCalendar() []data.IPOEvent {
	return e.coord.GetAllIPOCalendar()
}

// cleanSectorName 过滤板块名中的非法字符，只保留中英文、数字、空格、&、/。
func cleanSectorName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == ' ' || r == '&' || r == '/' {
			b.WriteRune(r)
		} else if r >= 0x4E00 && r <= 0x9FFF { // 中文
			b.WriteRune(r)
		}
	}
	return b.String()
}

// processNewsAndLLM 速率限制逻辑：PreMarket/PreAfternoon 5min、交易时段 15min、其他 1h。
// newsCount==0 时跳过限制：上次跑出 0 条新闻时允许立即重试（修复启动时序问题）。
// 调用 llmNewsTitles() 后起 goroutine 执行 llmClient.AnalyzeHotTopicBatch。
func (e *Engine) processNewsAndLLM(now time.Time, session data.MarketSession) {
	// 频率限制：PreMarket/PreAfternoon每5分钟集中分析 → 交易时段15分钟 → 其余1小时
	t := now.Hour()*100 + now.Minute()
	var minInterval time.Duration
	if (t >= 830 && t < 915) || (t >= 1130 && t < 1300) {
		minInterval = 300 * time.Second
	} else if t >= 915 && t < 1530 {
		minInterval = 900 * time.Second
	} else {
		minInterval = 3600 * time.Second
	}
	if !e.lastEventDescAt.IsZero() && now.Sub(e.lastEventDescAt) < minInterval {
		// 上一轮新闻为 0 条时允许提前重跑
		if e.newsCount > 0 {
			return
		}
	}
	e.lastEventDescAt = now

	// 拉取新闻用于板块事件映射（前端展示 + 板块扫描，不再拼接 eventDesc）
	cfg := e.cfg.Get()
	news := e.coord.GetHotNews(30)
	e.mu.Lock()
	e.newsCount = len(news)
	e.mu.Unlock()
	msCfg := cfg.MainSector
	if e.sectorScan != nil && len(news) > 0 {
		eventMap := e.sectorScan.BuildEventMapFromNews(news, msCfg.SectorEventMap)
		e.sectorScan.SetEventMap(eventMap)
		log.Printf("热点新闻: %d条 事件驱动板块: %d个", len(news), len(eventMap))
	}

	// LLM 降级中则跳过异步分析
	e.mu.RLock()
	fallback := e.llmFallback
	e.mu.RUnlock()
	if fallback || e.llmClient == nil {
		return
	}

	// 异步发送新闻标题给 LLM 批量分析（用于更新热点板块情感分，不用于 D1 评分）
	go func() {
		titles := e.llmNewsTitles()
		if len(titles) == 0 {
			log.Println("LLM 异步分析跳过: 无新闻标题")
			return
		}
		key := strings.Join(titles, "\n")
		e.mu.RLock()
		lastInput := e.lastLLMInput
		e.mu.RUnlock()
		if lastInput == key {
			return
		}

		results := e.llmClient.AnalyzeHotTopicBatch(titles)
		if len(results) == 0 {
			log.Println("LLM 异步分析返回空结果")
			return
		}

		e.mu.Lock()
		e.lastLLMInput = key
		for _, ht := range results {
			if ht == nil || ht.Title == "" {
				continue
			}
			key := truncate(ht.Title, 60)
			if _, ok := e.llmTopicCache[key]; !ok {
				e.llmTopicCache[key] = ht
			}
		}
		e.mu.Unlock()
		count := 0
		e.mu.RLock()
		for _, ht := range results {
			if ht != nil && ht.Title != "" {
				count++
			}
		}
		e.mu.RUnlock()
		log.Printf("LLM 异步分析完成: %d 个主题", count)
		e.rebuildLLMHotSectors()
	}()
}

// processNewsAndLLM_Sync 同步执行 LLM 结构化数据分析。
// 仅在首次运行/盘前触发，带超时控制：
//   - 成功：写入 llmTopicCache / llmL1Score / llmL1Blocked，重置 llmFallback
//   - 超时/失败：llmFallback=true，llmFailCnt++，后续 scanCycle 走 YAML 兜底
//
// 输入为结构化数据（非新浪标题），包含：
//   - IPO 日历（e.coord.GetIPOEvents）
//   - 宏观日历（e.macroCal）
//   - 热点板块名（从 GetSectors 取前 20 个板块）
//   - RPS 强势板块（e.rpsMgr.GetTopSectors）
//
// timeout 为 LLM Chat 调用的超时时间，建议 30s。
func (e *Engine) processNewsAndLLM_Sync(timeout time.Duration) {
	if e.llmClient == nil {
		e.mu.Lock()
		e.llmFallback = true
		e.mu.Unlock()
		return
	}

	// 构建结构化输入
	input := e.buildLLMStructuredInput()
	if input == "" {
		return
	}

	// 去重：同一输入不重复调用
	e.mu.RLock()
	lastInput := e.lastLLMInput
	e.mu.RUnlock()
	if lastInput == input {
		return
	}

	// 带超时的 LLM 调用
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	type chatResult struct {
		resp string
		err  error
	}
	ch := make(chan chatResult, 1)
	go func() {
		resp, err := e.llmClient.Chat(input, "")
		ch <- chatResult{resp, err}
	}()

	var resp string
	select {
	case r := <-ch:
		if r.err != nil {
			log.Printf("LLM 同步分析失败: %v", r.err)
			e.mu.Lock()
			e.llmFallback = true
			e.llmFailCnt++
			e.mu.Unlock()
			return
		}
		resp = r.resp
	case <-ctx.Done():
		log.Printf("LLM 同步分析超时 (%v)", timeout)
		e.mu.Lock()
		e.llmFallback = true
		e.llmFailCnt++
		e.mu.Unlock()
		return
	}

	// 解析 JSON 数组
	var topics []struct {
		Title     string   `json:"title"`
		Score     float64  `json:"score"`
		Direction string   `json:"direction"`
		EventType string   `json:"event_type"`
		Sectors   []string `json:"sectors"`
		Stocks    []string `json:"stocks"`
		Reason    string   `json:"reason"`
	}
	resp = cleanJSON(resp)
	if err := json.Unmarshal([]byte(resp), &topics); err != nil {
		log.Printf("LLM 同步分析 JSON 解析失败: %v", err)
		e.mu.Lock()
		e.llmFallback = true
		e.llmFailCnt++
		e.mu.Unlock()
		return
	}

	if len(topics) == 0 {
		log.Printf("LLM 同步分析返回空结果")
		return
	}

	// 写入 llmTopicCache
	e.mu.Lock()
	e.lastLLMInput = input
	for _, t := range topics {
		key := truncate(t.Title, 60)
		e.llmTopicCache[key] = &llm.HotTopic{
			Title:     t.Title,
			Score:     t.Score,
			Direction: t.Direction,
			EventType: t.EventType,
			Sectors:   t.Sectors,
			Stocks:    t.Stocks,
			Reason:    t.Reason,
		}
	}
	e.mu.Unlock()

	// 重建热点板块索引 + D1 评分矩阵
	e.rebuildLLMHotSectors()
	e.rebuildLLMD1Scores()

	// 成功，重置 fallback 状态
	e.mu.Lock()
	e.llmFallback = false
	e.llmFailCnt = 0
	e.mu.Unlock()

	log.Printf("LLM 同步分析完成: %d 个主题", len(topics))
}

// buildLLMStructuredInput 构建结构化输入，含 IPO 日历、宏观日历、热点板块、RPS 板块、新闻标题五部分。
// 用于同步 LLM 路径（异步 goroutine 改用 llmNewsTitles）。
func (e *Engine) buildLLMStructuredInput() string {
	var parts []string

	// IPO 日历（从 GetAllIPOCalendar 获取全部新股）
	ipoEvents := e.coord.GetAllIPOCalendar()
	if len(ipoEvents) > 0 {
		var ipoStrs []string
		for _, ev := range ipoEvents {
			if len(ipoStrs) >= 3 {
				break
			}
			ipoStrs = append(ipoStrs, fmt.Sprintf("%s(%s)", ev.Name, ev.Code))
		}
		parts = append(parts, "IPO日历: "+strings.Join(ipoStrs, "; "))
	}

	// 宏观日历（未来1天）
	if e.macroCal != nil {
		events := e.macroCal.UpcomingEvents(1)
		if len(events) > 0 {
			var macroStrs []string
			for _, ev := range events {
				macroStrs = append(macroStrs, fmt.Sprintf("%s(%s)", ev.Title, ev.Impact))
			}
			parts = append(parts, "宏观日历: "+strings.Join(macroStrs, "; "))
		}
	}

	// 热点板块名（从 GetSectors 取前 20）
	sectors, _ := e.coord.GetSectors()
	if len(sectors) > 0 {
		topN := minInt(len(sectors), 20)
		var secNames []string
		for i := 0; i < topN; i++ {
			secNames = append(secNames, sectors[i].Name)
		}
		parts = append(parts, "热点板块: "+strings.Join(secNames, "/"))
	}

	// RPS 强势板块
	if e.rpsMgr != nil {
		top := e.rpsMgr.GetTopSectors()
		if len(top) > 0 {
			var rpsNames []string
			for _, s := range top {
				rpsNames = append(rpsNames, s.Name)
			}
			parts = append(parts, "RPS强势: "+strings.Join(rpsNames, "/"))
		}
	}

	// 新闻标题（取前 20 条经 event filter 过滤的事件新闻）
	news := e.filterEventNews(e.coord.GetHotNews(20))
	if len(news) > 0 {
		var newsStrs []string
		for _, n := range news {
			if len(n.Title) < 4 {
				continue
			}
			newsStrs = append(newsStrs, n.Title)
		}
		if len(newsStrs) > 0 {
			parts = append(parts, "新闻标题: \n"+strings.Join(newsStrs, "\n"))
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}

// llmNewsTitles 提取 filterEventNews 过滤后的新闻标题。
// 返回长度 >= 4 字符的标题。
// 被 processNewsAndLLM goroutine 用于批量 LLM 分析。
func (e *Engine) llmNewsTitles() []string {
	news := e.filterEventNews(e.coord.GetHotNews(20))
	if len(news) == 0 {
		return nil
	}
	var titles []string
	for _, n := range news {
		if len(n.Title) >= 4 {
			titles = append(titles, n.Title)
		}
	}
	return titles
}

// cleanJSON 清理 LLM 返回文本中的非法 JSON 字符。
// 移除 markdown 代码块标记、// 注释等。
func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	return s
}

// checkHoldingsExit 遍历所有持仓执行策略退出检查，生成退出信号并持久化最高价。
func (e *Engine) checkHoldingsExit() {
	e.exitResults = make(map[string]*strategy.ExitResult)
	if e.holdings == nil {
		return
	}
	h := e.holdings.Get()
	cfg := e.cfg.Get()
	snap := e.fetcher.Snapshot()
	if snap == nil {
		return
	}
	updated := false
	for i := range h.Holdings {
		hh := &h.Holdings[i]
		if hh.EntryStrategy == "" {
			continue
		}
		si, ok := snap.Stocks[hh.Code]
		if !ok || si == nil || si.Price <= 0 {
			continue
		}
		curPrice := si.Price

		if hh.EntryMeta == nil {
			hh.EntryMeta = make(map[string]float64)
		}

		ctx := &strategy.ExitContext{
			Code:      hh.Code,
			Name:      si.Name,
			CostPrice: hh.CostPrice,
			CurPrice:  curPrice,
			EntryAt:   hh.EntryAt,
			EntryMeta: hh.EntryMeta,
			Now:       time.Now(),
		}
		if klines, err := e.getCachedKLine(hh.Code); err == nil && len(klines) > 0 {
			for _, k := range klines {
				ctx.DailyK = append(ctx.DailyK, strategy.KLine{
					Close: k.Close, High: k.High, Low: k.Low, Open: k.Open, Volume: k.Volume,
				})
			}
		}

		var result *strategy.ExitResult
		switch hh.EntryStrategy {
		case "dragon_return":
			result = dragon_return.CheckExit(ctx, &cfg.Strategy.DragonReturn)
		case "n_shape":
			result = n_shape.CheckExit(ctx, &cfg.Strategy.NShape)
		case "dragon":
			result = dragon.CheckExit(ctx, &cfg.Strategy.Dragon)
		case "double_bump":
			result = double_bump.CheckExit(ctx, &cfg.Strategy.DoubleBump)
		}
		if result != nil {
			e.exitResults[hh.Code] = result
		}

		needsUpdate := false
		switch hh.EntryStrategy {
		case "dragon_return":
			needsUpdate = dragon_return.NeedUpdateHighest()
		case "double_bump":
			needsUpdate = double_bump.NeedUpdateHighest()
		}
		if needsUpdate {
			curHighest := hh.EntryMeta["highest_price"]
			if curPrice > curHighest {
				hh.EntryMeta["highest_price"] = curPrice
				updated = true
			}
		}
	}
	if updated {
		e.holdings.Set(h)
	}
}

// ── 扫描主循环 ──

// scanCycle 执行一轮完整的扫描评估流程：
//  1. 非交易时段：只抓一次数据后返回
//  2. 开盘重置状态机，午后重置板块热度
//  3. 检测板块热度（checkSectorHeat）
//  4. 更新1分钟K线缓冲区
//  5. 板块热点扫描 & 动态扩充监控列表
//  6. N形快速路径检测（一突/旗面/二突状态机）
//  7. 板块龙头判定 + RPS计算
//  8. 多策略评估（evaluateAll）→ 生成原始信号
//  9. 信号挂载 gain_10d / turnover 元数据
//
// 10. filter 过滤 → risk 风控 → notify 推送
// 11. 排序后写入 e.signals 供前端查询
// 12. 通用止盈止损检测（非信号持仓的自选/持仓标的）
// 13. M8兜底、宏观日历、韩国联动检查
// 14. Android 通知推送
func (e *Engine) scanCycle() {
	now := time.Now()
	session := data.CurrentSession(now)

	// ── 非交易时段：仅行情快照+板块更新，不评估/不请求K线 ──
	if session != data.SessionMorningTrade && session != data.SessionAfternoonTrade {
		if session == data.SessionPreMarket || session == data.SessionPreAfternoon {
			e.preSessionLLMScan(now, session)
			// 首次运行或 LLM 降级后重新激活时，同步调用 LLM 获取结构化分析
			// 超时 30s，超时后自动切到 YAML 兜底
			e.mu.RLock()
			firstRun := e.llmL1Gen == 0
			needRetry := e.llmFailCnt > 2
			e.mu.RUnlock()
			if firstRun || needRetry {
				e.processNewsAndLLM_Sync(30 * time.Second)
			}
		}
		cfg := e.cfg.Get()
		if !e.offHoursFetched {
			if e.fetcher.Running() {
				e.fetcher.Stop()
			}
			baseList := cfg.Theme.WatchList
			if len(baseList) == 0 {
				baseList = []string{"000001", "600519", "000858", "002594"}
			}
			if e.watchlistMgr != nil {
				baseList = append(baseList, e.watchlistMgr.List()...)
			}
			if e.holdings != nil {
				h := e.holdings.Get()
				for _, hh := range h.Holdings {
					baseList = append(baseList, hh.Code)
				}
			}
			e.fetcher.SetBaseStocks(baseList)
			e.fetcher.UpdateHotStocks(nil)
			e.fetcher.FetchOnce()
			e.offHoursFetched = true
			log.Printf("非交易时段: 已加载 %d 只自选+持仓量价数据", len(baseList))
		} else {
			// 非交易时段后续轮次：仅重新拉取行情+板块数据
			e.fetcher.FetchOnce()
		}

		// 每轮都先拉新闻+D1/LLM，让板块更新使用新鲜事件映射
		e.processNewsAndLLM(now, session)

		// 更新板块热点数据，供前端展示
		snap := e.fetcher.Snapshot()
		sectorData := getAllSectors(snap, e.coord)
		if len(sectorData) > 0 && e.sectorScan != nil {
			msCfg := cfg.MainSector
			e.sectorScan.Update(sectorData, msCfg.MainSectorLimitupBull, msCfg.MainSectorLimitupShock, msCfg.MainSectorMaxCount)
			hotSectors := e.sectorScan.HotSectors()
			if len(hotSectors) > 0 {
				e.mu.Lock()
				e.hotSectorCnt = len(hotSectors)
				e.mu.Unlock()
				log.Printf("非交易时段热点板块: %d个", len(hotSectors))
			}
		}
		return
	}

	// 进入交易时段后也执行新闻+LLM
	e.processNewsAndLLM(now, session)

	// 进入交易时段：确保 fetcher 在运行
	if !e.fetcher.Running() {
		e.fetcher.Start()
		log.Println("交易时段开始，数据采集启动")
	}

	snap := e.fetcher.Snapshot()
	if snap == nil || len(snap.Stocks) == 0 {
		return
	}

	// 开盘重置状态机：首次启动或跨日时重置
	today := now.Format("20060102")
	if e.lastResetDay != today {
		e.resetNStates()
		e.lastResetDay = today
		log.Printf("每日重置N形状态机: %s", today)
	}
	// 每日数据计数器重置（0点跨日）
	if e.dataCountDate != today {
		e.mu.Lock()
		e.newsCount = 0
		e.hotSectorCnt = 0
		e.sectorStockCnt = 0
		e.dataCountDate = today
		e.hitNotified = make(map[string]bool)
		e.signalsNotified = make(map[string]bool)
		e.mu.Unlock()
		// 每日清理消息中心过期告警
		server.ClearAlertStore()
	}
	e.offHoursFetched = false

	// 更新1分钟K线用于MACD
	for code, si := range snap.Stocks {
		if si != nil && si.Price > 0 {
			e.updateMinuteBars(code, si.Price, si.Volume, snap.Time)
		}
	}

	cfg := e.cfg.Get()
	nc := cfg.Strategy.NShape

	// 板块热点扫描 & 动态扩充watchlist
	msCfg := cfg.MainSector
	sectorData := snap.Sector
	if len(sectorData) == 0 && e.coord != nil {
		if s, err := e.coord.GetSectors(); err == nil && len(s) > 0 {
			sectorData = s
		}
	}
	if len(sectorData) > 0 && e.sectorScan != nil {
		e.sectorScan.Update(sectorData, msCfg.MainSectorLimitupBull, msCfg.MainSectorLimitupShock, msCfg.MainSectorMaxCount)
		e.sectorScanCount++
	}

	// 每日重置 eventDesc + 新闻+LLM
	if e.lastEventDescAt.Day() != now.Day() || e.lastEventDescAt.IsZero() {
		e.lastEventDesc = ""
	}
	// ── 按时段区分热点→个股链路 ──
	if e.sectorScan != nil {
		hotSectors := e.sectorScan.HotSectors()
		expandStocks := true
		switch session {
		case data.SessionPreMarket, data.SessionPreAfternoon, data.SessionAfterMarket:
			expandStocks = false
		}
		e.mu.Lock()
		e.hotSectorCnt = len(hotSectors)
		e.mu.Unlock()

		// 构建基础监控池（自选+持仓，无上限）
		baseList := cfg.Theme.WatchList
		if len(baseList) == 0 {
			baseList = []string{"000001", "600519", "000858", "002594"}
		}
		if e.watchlistMgr != nil {
			baseList = append(baseList, e.watchlistMgr.List()...)
		}
		if e.holdings != nil {
			h := e.holdings.Get()
			for _, hh := range h.Holdings {
				baseList = append(baseList, hh.Code)
			}
		}
		e.fetcher.SetBaseStocks(baseList)

		if len(hotSectors) > 0 {
			if expandStocks {
				e.expandFromHotSectors(hotSectors, baseList, cfg)
			} else {
				e.fetcher.UpdateHotStocks(nil)
				e.mu.Lock()
				e.stockSectorSrc = nil
				e.mu.Unlock()
				log.Printf("非交易时段热点板块: %d个（跳过扩个股）", len(hotSectors))
			}
		} else if expandStocks {
			// LLM 板块兜底：当 scanner D1+F 未产出热点板块时，
			// 用 LLM 从新闻中推断的热点板块名称匹配实际板块并扩个股。
			if llmSectorNames := e.llmFallbackSectors(); len(llmSectorNames) > 0 {
				e.expandFromHotSectors(llmSectorNames, baseList, cfg)
				log.Printf("LLM板块→个股(降级): %d个板块→热点个股", len(llmSectorNames))
			} else {
				log.Printf("当前无热点板块（保留已有热点个股）")
			}
		}
	}

	emotionPhase := data.DetectEmotionPhase(snap, &cfg.Emotion)

	// ── N形快速路径检测（一突/旗面/二突） ──
	for code, si := range snap.Stocks {
		if si == nil || si.Price <= 0 {
			continue
		}
		state := e.getNState(code)
		kl, err := e.getCachedKLine(code)
		if err != nil || len(kl) < 2 {
			continue
		}
		last := kl[len(kl)-1]
		prevHigh := last.High
		avgVol := 0.0
		if len(kl) >= 20 {
			for i := len(kl) - 20; i < len(kl); i++ {
				avgVol += kl[i].Volume
			}
			avgVol /= 20.0
		}

		dif, dea, _ := e.calcMinuteMACD(code)
		t := snap.Time.Hour()*100 + snap.Time.Minute()
		if t < 925 {
			t = 925
		}

		switch state.Phase {
		case n_shape.NPhaseIdle:
			// 一突检测: price > prev_high × 1.005 + volume ratio >= 1.8
			volRatio := 1.0
			if avgVol > 0 && last.Volume > 0 {
				timeRatio := float64(t/100*60+t%100-570) / 330.0
				if timeRatio < 0.1 {
					timeRatio = 0.1
				}
				volRatio = si.Volume / (avgVol * timeRatio)
			}
			if si.Price > prevHigh*nc.BreakoutRatio && volRatio >= nc.VolRatio {
				state.Phase = n_shape.NPhaseFirstBreakout
				state.FirstPrice = si.Price
				state.FirstVol = si.Volume
				state.FirstTime = t
				state.PeakPrice = si.Price
				log.Printf("N形一突: %s %.2f 量比%.1f [%d]", code, si.Price, volRatio, t)
			}

		case n_shape.NPhaseFirstBreakout:
			// 记录峰顶
			if si.Price > state.PeakPrice {
				state.PeakPrice = si.Price
			}
			// 进入旗面: 价格从Peak回撤
			retreat := (state.PeakPrice - si.Price) / state.PeakPrice
			if retreat > 0.005 {
				state.Phase = n_shape.NPhaseFlag
				state.FlagLow = si.Price
				state.FlagVol = si.Volume
				log.Printf("N形旗面: %s 回撤%.2f%% 量%.0f [%d]", code, retreat*100, si.Volume, t)
			}
			// 时间限制: 上午超过11:00还没进入旗面则放弃
			if t >= 1100 {
				state.Phase = n_shape.NPhaseFailed
				log.Printf("N形超时: %s 未进旗面 [%d]", code, t)
			}

		case n_shape.NPhaseFlag:
			// 更新旗面低点
			if si.Price < state.FlagLow {
				state.FlagLow = si.Price
			}
			if si.Volume < state.FlagVol {
				state.FlagVol = si.Volume
			}
			// 旗面回撤检查: 回撤不超过 FlagRetreatPct
			if si.Price < state.FirstPrice {
				// 跌破了一突启动价 = 旗面失败
				state.Phase = n_shape.NPhaseFailed
				log.Printf("N形旗面失败: %s 跌破一突价 %.2f [%d]", code, si.Price, t)
				break
			}
			// 二突检测: price突破峰顶 + vol≥一突vol×0.8 + MACD红柱≥2
			if si.Price > state.PeakPrice {
				volOK := true
				if state.FirstVol > 0 && si.Volume < state.FirstVol*nc.NSecondBreakVolRatio {
					volOK = false
				}
				macdOK := dif > dea && dif > 0
				if volOK && macdOK {
					state.Phase = n_shape.NPhaseSecondBreakout
					log.Printf("N形二突: %s %.2f MACD dif=%.2f dea=%.2f [%d]", code, si.Price, dif, dea, t)
				}
			}
			// 时间限制: 下午超过14:30还没二突则放弃
			if t >= 1430 {
				state.Phase = n_shape.NPhaseFailed
				log.Printf("N形超时: %s 二突超时14:30 [%d]", code, t)
			}

		case n_shape.NPhaseSecondBreakout:
			// 已确认二突，等待出场信号
			if t >= 1500 {
				state.Phase = n_shape.NPhaseCompleted
			}

		case n_shape.NPhaseFailed:
			// 失败状态，不再检测，直到次日重置
		}
	}

	// ── 板块龙头判定 + RPS计算（每scan cycle） ──
	e.mu.Lock()
	e.sectorLeaders = make(map[string]bool)
	e.mu.Unlock()
	var rpsList []data.SectorRPS
	if len(snap.Sector) > 0 {
		for _, sec := range snap.Sector {
			stocks, err := e.coord.GetSectorStocks(sec.Code, 20)
			if err == nil && len(stocks) > 0 {
				sort.Slice(stocks, func(i, j int) bool {
					return stocks[i].ChangePct > stocks[j].ChangePct
				})
				// 龙头：涨幅排名前 20%（至少 1 只），且涨幅 > 0
				leaderCount := (len(stocks) + 4) / 5
				if leaderCount < 1 {
					leaderCount = 1
				}
				for i := 0; i < leaderCount && i < len(stocks); i++ {
					if stocks[i].ChangePct <= 0 {
						break
					}
					e.mu.Lock()
					e.sectorLeaders[stocks[i].Code] = true
					e.mu.Unlock()
				}
				// RPS: 用板块涨跌幅百分位排名
				rps5 := 50.0
				rps20 := 50.0
				if sec.ChangePct > 3 {
					rps5 = 90
					rps20 = 85
				} else if sec.ChangePct > 1 {
					rps5 = 75
					rps20 = 70
				} else if sec.ChangePct > 0 {
					rps5 = 60
					rps20 = 55
				}
				rpsList = append(rpsList, data.SectorRPS{
					Code: sec.Code, Name: sec.Name,
					RPS5: rps5, RPS20: rps20, RPS60: rps20 - 5,
				})
			}
		}
	}
	if len(rpsList) > 0 {
		e.rpsMgr.Update(rpsList)
	}

	benchChg := e.fetchBenchChg()
	var allSignals []strategy.Signal
	var allEvals []server.StockEval

	for code, si := range snap.Stocks {
		results := e.evaluateAll(code, si, snap, emotionPhase, e.lastEventDesc, benchChg)

		// 收集全维度评分（无论是否通过策略门槛）
		ev := server.StockEval{
			Code: code, Name: si.Name, Price: si.Price, ChangePct: si.ChangePct,
		}
		if si.Sector != "" {
			ev.Sector = si.Sector
		}
		for _, r := range results {
			if r.eval == nil {
				continue
			}
			switch r.strategy {
			case "n_shape":
				ev.NScore = r.eval.TotalScore
				ev.NLevel = r.eval.Level
				ev.NPass = r.eval.Pass
				if r.eval.Details != nil {
					ev.ND1 = r.eval.Details["d1"]
					ev.ND2 = r.eval.Details["d2"]
					ev.ND3 = r.eval.Details["d3"]
					ev.ND4 = r.eval.Details["d4"]
				}
			case "dragon":
				ev.DragonScore = r.eval.TotalScore
				ev.DragonLevel = r.eval.Level
				ev.DragonPass = r.eval.Pass
				if r.eval.Details != nil {
					ev.DragonF1 = r.eval.Details["f1"]
					ev.DragonF2 = r.eval.Details["f2"]
					ev.DragonF3 = r.eval.Details["f3"]
					ev.DragonF4 = r.eval.Details["f4"]
				}
			case "double_bump":
				ev.DBScore = r.eval.TotalScore
				ev.DBLevel = r.eval.Level
				ev.DBPass = r.eval.Pass
			case "dragon_return":
				ev.DRScore = r.eval.TotalScore
				ev.DRLevel = r.eval.Level
				ev.DRPass = r.eval.Pass
			case "momentum":
				ev.MScore = r.eval.TotalScore
				ev.MLevel = r.eval.Level
				ev.MPass = r.eval.Pass
			}
		}
		if ev.NScore > 0 || ev.DragonScore > 0 || ev.DBScore > 0 || ev.DRScore > 0 || ev.MScore > 0 {
			allEvals = append(allEvals, ev)
		}

		// 命中提醒：只推该股票评分最高的策略（避免狂弹）
		{
			bestIdx := -1
			bestScore := 0.0
			for idx, r := range results {
				if r.eval == nil || r.eval.TotalScore <= 0 || r.strategy == "momentum" {
					continue
				}
				if r.eval.TotalScore > bestScore {
					bestScore = r.eval.TotalScore
					bestIdx = idx
				}
			}
			if bestIdx >= 0 {
				r := results[bestIdx]
				key := code + "/" + r.strategy
				if !e.hitNotified[key] {
					e.hitNotified[key] = true
					meta := make(map[string]float64)
					for k, v := range r.eval.Details {
						meta[k] = v
					}
					meta["signal_type"] = 0
					hitSig := &strategy.Signal{
						Code: code, Name: si.Name,
						Type:       strategy.SignalType(r.strategy),
						Price:      si.Price,
						Confidence: r.eval.Confidence,
						Reason:     r.eval.Level,
						Meta:       meta,
					}
					e.notifier.PushHit(hitSig, si.ChangePct, si.Volume, r.eval.Reasons)
					// 同步写入 alertStore，供消息中心展示
					body := fmt.Sprintf("%.0f分 D1=%.0f(%s) D2=%.0f(%s) D3=%.0f(%s) D4=%.0f(%s) 现价%.2f %.2f%%",
						r.eval.Confidence*100,
						r.eval.Details["d1"], r.eval.Reasons["d1"],
						r.eval.Details["d2"], r.eval.Reasons["d2"],
						r.eval.Details["d3"], r.eval.Reasons["d3"],
						r.eval.Details["d4"], r.eval.Reasons["d4"],
						si.Price, si.ChangePct,
					)
					server.AddAlertToStore(server.Alert{
						Time: now.Format("15:04:05"), Code: code, Name: si.Name,
						Title: "⚡" + r.strategy + "命中", Body: body,
						Score: r.eval.Confidence * 100, Level: "命中提醒",
					})
				}
			}
		}

		// 交易信号（Pass=true）
		for _, r := range results {
			if r.eval == nil || !r.eval.Pass || r.strategy == "momentum" {
				continue
			}
			var sig *strategy.Signal
			var err error
			switch r.strategy {
			case "n_shape":
				sig, err = e.nShape.GenerateSignal(code, r.eval)
			case "dragon":
				sig, err = e.dragon.GenerateSignal(code, r.eval)
			case "double_bump":
				sig, err = e.doubleBump.GenerateSignal(code, r.eval)
			case "dragon_return":
				sig, err = e.dragonReturn.GenerateSignal(code, r.eval)
			default:
				sig, err = e.nShape.GenerateSignal(code, r.eval)
			}
			if err != nil {
				log.Printf("gen signal %s: %v", code, err)
				continue
			}
			if r.strategy == "n_shape" && sig.Action == strategy.ActionBuy && e.isHeld(code) {
				continue
			}
			sig.Code = code
			sig.Name = si.Name
			sig.Price = si.Price
			if sig.Meta == nil {
				sig.Meta = make(map[string]float64)
			}
			sig.Meta["signal_type"] = 1 // 1=交易
			sig.Reasons = r.eval.Reasons
			if r.strategy == "n_shape" {
				state := e.getNState(code)
				if state.Phase == n_shape.NPhaseFirstBreakout || state.Phase == n_shape.NPhaseSecondBreakout {
					sig.Meta["n_phase"] = float64(state.Phase)
				}
			}
			allSignals = append(allSignals, *sig)
		}
		// 观望信号（Pass=false 但有评分）
		for _, r := range results {
			if r.eval == nil || r.eval.Pass || r.eval.TotalScore <= 0 || r.strategy == "momentum" {
				continue
			}
			if r.strategy == "n_shape" && e.isHeld(code) {
				continue
			}
			sig := &strategy.Signal{
				Code: code, Name: si.Name, Type: strategy.SignalType(r.strategy),
				Price: si.Price, Confidence: r.eval.Confidence, Reason: r.eval.Level,
				Action: strategy.ActionWatch, Priority: strategy.P4,
				Meta:    map[string]float64{"signal_type": 0}, // 0=观望
				Reasons: r.eval.Reasons,
			}
			allSignals = append(allSignals, *sig)
		}
	}

	// 存全量评分供前端查询
	e.mu.Lock()
	e.stockEvals = allEvals
	e.mu.Unlock()

	// 信号复核：用东财/Tushare/同花顺校验 Sina 报价
	if len(allSignals) > 0 {
		e.crossCheckSignals(allSignals)
	}

	// 统计
	withData := 0
	totalStocks := 0
	if e.fetcher != nil {
		totalStocks = e.fetcher.StockCount()
	}
	for _, si := range snap.Stocks {
		if si != nil && si.Price > 0 {
			withData++
		}
	}
	e.mu.Lock()
	e.scanStats = server.ScanStats{
		TotalStocks: totalStocks, WithData: withData,
		WithError: totalStocks - withData, RawSignals: len(allSignals),
		NewsCount: e.newsCount, HotSectorCount: e.hotSectorCnt,
		SectorStockCount: e.sectorStockCnt,
	}
	e.mu.Unlock()

	e.lastScan = now

	// 给所有信号挂上 gain_10d + turnover（供 filter 使用）
	for i := range allSignals {
		sig := &allSignals[i]
		if sig.Meta == nil {
			sig.Meta = make(map[string]float64)
		}
		if _, ok := sig.Meta["gain_10d"]; !ok {
			if kl, err := e.getCachedKLine(sig.Code); err == nil && len(kl) >= 11 {
				sig.Meta["gain_10d"] = safePct(kl[len(kl)-1].Close, kl[len(kl)-11].Close)
			}
		}
		if _, ok := sig.Meta["turnover"]; !ok {
			if si, ok2 := snap.Stocks[sig.Code]; ok2 && si != nil {
				sig.Meta["turnover"] = si.Turnover
			}
		}
	}

	// 构建全量评分查找表
	evalMap := make(map[string]server.StockEval, len(allEvals))
	for i := range allEvals {
		evalMap[allEvals[i].Code] = allEvals[i]
	}

	e.filter.EmotionPhase = emotionPhase

	var views []server.SignalView
	for i := range allSignals {
		sig := &allSignals[i]
		isTrade := sig.Meta != nil && sig.Meta["signal_type"] == 1
		levelName := "观望"
		if isTrade {
			levelName = "交易"
		}

		result := e.riskCtrl.CheckSignal(sig)
		blocked := result.Blocked
		if blocked && isTrade {
			log.Printf("交易信号 %s %s 被风控阻断: %s", sig.Code, sig.Name, result.Reason)
			levelName = "观望"
		}
		si2, _ := snap.Stocks[sig.Code]
		chgPct := 0.0
		vol := 0.0
		if si2 != nil {
			chgPct = si2.ChangePct
			vol = si2.Volume
		}
		if isTrade && !blocked {
			e.notifier.PushTrade(sig, chgPct, vol)
		}

		remind := remindLevel(sig.Priority, sig.Confidence)
		if blocked && remind == "strong" {
			remind = "observe"
		}
		canOpen := isTrade && sig.Action == strategy.ActionBuy && !blocked

		sectorName := ""
		if si2 != nil {
			sectorName = si2.Sector
		}
		v := server.SignalView{
			Code: sig.Code, Name: sig.Name, Sector: sectorName, Strategy: string(sig.Type),
			Level: levelName, TotalScore: sig.Confidence * 100,
			D1: dFromEval(sig, "d1"), D2: dFromEval(sig, "d2"),
			D3: dFromEval(sig, "d3"), D4: dFromEval(sig, "d4"),
			CanOpen: canOpen, Priority: int(sig.Priority),
			RemindLevel: remind, Price: sig.Price, ChangePct: chgPct, Action: string(sig.Action),
		}
		if sig.Reasons != nil {
			v.D1Desc = sig.Reasons["d1"]
			v.D2Desc = sig.Reasons["d2"]
			v.D3Desc = sig.Reasons["d3"]
			v.D4Desc = sig.Reasons["d4"]
		}
		// 合并全量评分
		if ev, ok := evalMap[sig.Code]; ok {
			v.NScore = ev.NScore
			v.NLevel = ev.NLevel
			v.NPass = ev.NPass
			v.DragonScore = ev.DragonScore
			v.DragonLevel = ev.DragonLevel
			v.DragonPass = ev.DragonPass
			v.DBScore = ev.DBScore
			v.DBLevel = ev.DBLevel
			v.DBPass = ev.DBPass
			v.DRScore = ev.DRScore
			v.DRLevel = ev.DRLevel
			v.DRPass = ev.DRPass
			v.MScore = ev.MScore
			v.MLevel = ev.MLevel
			v.MPass = ev.MPass
		}
		for k, val := range sig.Meta {
			switch k {
			case "f1_seal", "f2_resonance", "f3_premium", "f4_rs",
				"vol_score", "adjust_score", "ma_score":
				switch k {
				case "f1_seal":
					v.F1 = val
				case "f2_resonance":
					v.F2 = val
				case "f3_premium":
					v.F3 = val
				case "f4_rs":
					v.F4 = val
				}
			case "n_phase":
				v.NPhase = int(val)
			}
		}
		views = append(views, v)

		// 同步写入 alertStore（信号去重，每天开盘 reset）
		alertLvl := "命中提醒"
		if isTrade {
			alertLvl = "策略信号"
		}
		sigKey := sig.Code + "/" + alertLvl + "/" + string(sig.Type)
		if !e.signalsNotified[sigKey] {
			e.signalsNotified[sigKey] = true
			server.AddAlertToStore(server.Alert{
				Time: now.Format("15:04:05"), Code: sig.Code, Name: sig.Name,
				Title: levelName + " " + sig.Name + " " + string(sig.Type),
				Body:  fmt.Sprintf("%.0f分 %s", sig.Confidence*100, sig.Reason),
				Score: sig.Confidence * 100, Level: alertLvl,
			})
		}

		if e.history != nil {
			e.history.Record(&notify.SignalRecord{
				Time: time.Now(), Code: sig.Code, Name: sig.Name,
				Strategy: string(sig.Type), Action: string(sig.Action),
				Priority: int(sig.Priority), Score: sig.Confidence * 100,
				D1: dFromEval(sig, "d1"), D2: dFromEval(sig, "d2"),
				D3: dFromEval(sig, "d3"), D4: dFromEval(sig, "d4"),
				Price: sig.Price, Level: remindLevel(sig.Priority, sig.Confidence),
			})
		}
	}

	sort.Slice(views, func(i, j int) bool { return views[i].TotalScore > views[j].TotalScore })

	e.mu.Lock()
	e.signals = views
	e.scanStats.FinalSignals = len(views)
	e.mu.Unlock()

	// ── 策略退出检查 ──
	e.checkHoldingsExit()

	// 持仓止盈止损提醒：持仓票当日+8%/-5% 触发提醒，独立于策略信号
	// 上下午各提醒一次，删持仓后自动停止
	{
		sessionTag := "morning"
		if session == data.SessionAfternoonTrade {
			sessionTag = "afternoon"
		}
		if e.holdings != nil {
			h := e.holdings.Get()
			for _, hh := range h.Holdings {
				si, ok := snap.Stocks[hh.Code]
				if !ok || si == nil || si.Price <= 0 {
					continue
				}
				chg := safePct(si.Price, kLinePrevClose(hh.Code, e))
				if chg < 8.0 && chg > -5.0 {
					continue
				}
				if e.pnlAlertSent[hh.Code] == sessionTag {
					continue
				}
				title := si.Name + " 止盈"
				level := "持仓止盈"
				if chg <= -5.0 {
					title = si.Name + " 止损"
					level = "持仓止损"
				}
				e.pnlAlertSent[hh.Code] = sessionTag
				alert := server.Alert{
					Time: now.Format("15:04:05"), Code: hh.Code, Name: si.Name,
					Title: title, Body: fmt.Sprintf("%s 日内%.1f%%，建议操作", hh.Code, chg),
					Score: chg, Level: level,
				}
				if NotifyAndroid != nil {
					NotifyAndroid(alert.Title, alert.Body)
				}
				server.AddAlertToStore(alert)
				e.mu.Lock()
				e.alertLog = append(e.alertLog, alert)
				e.mu.Unlock()
			}
		}
		// 清理已删除持仓的提醒记录
		if e.holdings != nil {
			h := e.holdings.Get()
			active := make(map[string]bool)
			for _, hh := range h.Holdings {
				active[hh.Code] = true
			}
			for code := range e.pnlAlertSent {
				if !active[code] {
					delete(e.pnlAlertSent, code)
				}
			}
		}
	}

	// 动量≥50提醒：持仓+自选+热点中动量≥50的个股，上下午各提醒一次
	{
		sessionTag := "morning"
		if session == data.SessionAfternoonTrade {
			sessionTag = "afternoon"
		}
		for _, ev := range allEvals {
			if ev.MScore < 50 {
				continue
			}
			key := fmt.Sprintf("momentum:%s:%s", ev.Code, sessionTag)
			if e.pushedAlerts[key] {
				continue
			}
			e.pushedAlerts[key] = true
			alert := server.Alert{
				Time: now.Format("15:04:05"), Code: ev.Code, Name: ev.Name,
				Title: ev.Name + " 动量活跃", Body: fmt.Sprintf("动量%.0f分，量价活跃值得关注", ev.MScore),
				Score: ev.MScore, Level: "动量关注",
			}
			if NotifyAndroid != nil {
				NotifyAndroid(alert.Title, alert.Body)
			}
			server.AddAlertToStore(alert)
			e.mu.Lock()
			e.alertLog = append(e.alertLog, alert)
			e.mu.Unlock()
		}
	}

	// ── M8 兜底 ──
	lc := cfg.RiskCtrl
	if lc.M8Enabled && time.Since(e.m8LastCheckAt) > time.Duration(lc.M8CheckIntervalSec)*time.Second {
		e.m8LastCheckAt = now
		currentTotal := 0.0
		if e.holdings != nil {
			h := e.holdings.Get()
			for _, hh := range h.Holdings {
				if si2, ok2 := snap.Stocks[hh.Code]; ok2 && si2 != nil {
					currentTotal += si2.Price * float64(hh.Quantity)
				} else {
					currentTotal += hh.CostPrice * float64(hh.Quantity)
				}
			}
		}
		if currentTotal > e.m8PeakTotal {
			e.m8PeakTotal = currentTotal
		}
		if cr := e.riskCtrl.M8Check(currentTotal, e.m8PeakTotal); cr != nil && !cr.Pass {
			alert := server.Alert{
				Time: now.Format("15:04:05"), Code: "M8", Name: "系统兜底",
				Title: "M8兜底清仓", Body: cr.Reason,
				Score: lc.M8PortfolioDrawdownPct, Level: "M8兜底",
			}
			if NotifyAndroid != nil {
				NotifyAndroid(alert.Title, alert.Body)
			}
			server.AddAlertToStore(alert)
			e.mu.Lock()
			e.alertLog = append(e.alertLog, alert)
			e.mu.Unlock()
		}
	}

	// ── 宏观日历 ──
	if e.macroCal != nil && cfg.Calendar.Enabled {
		for _, ev := range e.macroCal.UpcomingEvents(7) {
			key := "cal:" + ev.Title
			if e.pushedAlerts[key] {
				continue
			}
			e.pushedAlerts[key] = true
			dateStr := ev.Date.Format("1月2日")
			body := fmt.Sprintf("%s 发生, 距今天%.0f天, 影响:%s", dateStr, ev.Date.Sub(now).Hours()/24, ev.Impact)
			alert := server.Alert{
				Time: now.Format("15:04:05"), Code: "CAL", Name: "宏观日历",
				Title: ev.Title, Body: body, Score: 0, Level: "日历-" + ev.Impact,
			}
			server.AddAlertToStore(alert)
			e.mu.Lock()
			e.alertLog = append(e.alertLog, alert)
			e.mu.Unlock()
		}
	}

	// ── 韩国联动 ──
	if cfg.Korea.Enabled {
		if quotes, err := e.koreaLnk.Fetch(); err == nil && len(quotes) > 0 {
			for _, q := range quotes {
				key := "kr:" + q.Code
				if e.pushedAlerts[key] {
					continue
				}
				if q.ChangePct > cfg.Korea.ThresholdPct || q.ChangePct < -cfg.Korea.ThresholdPct {
					e.pushedAlerts[key] = true
					direction := "上涨"
					if q.ChangePct < 0 {
						direction = "下跌"
					}
					body := fmt.Sprintf("%s %s %.1f%%", q.Name, direction, q.ChangePct)
					alert := server.Alert{
						Time: now.Format("15:04:05"), Code: "KR", Name: q.Name,
						Title: "韩国科技联动", Body: body, Score: q.ChangePct, Level: "韩股联动",
					}
					server.AddAlertToStore(alert)
					e.mu.Lock()
					e.alertLog = append(e.alertLog, alert)
					e.mu.Unlock()
				}
			}
		}
	}

	// 推送手机通知
	alerts := e.GetAlerts()
	if NotifyAndroid != nil {
		for _, a := range alerts {
			key := a.Code + "|" + a.Title
			if e.pushedAlerts[key] {
				continue
			}
			e.pushedAlerts[key] = true
			NotifyAndroid(a.Title, a.Body)
		}
	}
	e.mu.Lock()
	e.alertLog = append(e.alertLog, alerts...)
	if len(e.alertLog) > 300 {
		e.alertLog = e.alertLog[len(e.alertLog)-300:]
	}
	e.mu.Unlock()
}

// notifyChain 打印热点→板块→个股链式日志和桌面通知。
// 盘中调用，记录从热点新闻到监控股票的全链路。
func (e *Engine) notifyChain(hotSectors []data.HotSector, expandedStocks []string) {
	if len(hotSectors) == 0 || len(expandedStocks) == 0 {
		return
	}
	// 取前3个板块名
	var names []string
	for _, s := range hotSectors {
		if len(names) >= 3 {
			break
		}
		if s.Sector.Name != "" {
			names = append(names, s.Sector.Name)
		}
	}
	sectorStr := strings.Join(names, ",")
	if sectorStr == "" {
		sectorStr = fmt.Sprintf("%d个板块", len(hotSectors))
	}
	log.Printf("热点→个股: [%s] → %d只监控", sectorStr, len(expandedStocks))
	body := fmt.Sprintf("热点板块: %s | 监控: %d只", sectorStr, len(expandedStocks))
	alert := server.Alert{
		Time: time.Now().Format("15:04:05"), Code: "HOT",
		Name: "热点链", Title: "热点→板块→个股", Body: body,
		Score: 100, Level: "热点监测",
	}
	server.AddAlertToStore(alert)
	e.mu.Lock()
	e.alertLog = append(e.alertLog, alert)
	e.mu.Unlock()
}

// rebuildLLMHotSectors 聚合 llmTopicCache 中各板块的得分。
// 只保留平均分 > 0.55 的板块（正面情感）。
// 更新 llmHotSectors 供大盘面板展示。
func (e *Engine) rebuildLLMHotSectors() {
	sectorScore := make(map[string]float64)
	sectorCnt := make(map[string]int)

	e.mu.RLock()
	for _, ht := range e.llmTopicCache {
		for _, s := range ht.Sectors {
			s = cleanSectorName(s)
			if s == "" {
				continue
			}
			sectorScore[s] += ht.Score
			sectorCnt[s]++
		}
	}
	e.mu.RUnlock()

	hot := make(map[string]float64)
	hotNames := make([]string, 0)
	for s, total := range sectorScore {
		if cnt := sectorCnt[s]; cnt >= 1 {
			avg := total / float64(cnt)
			if avg > 0.55 { // 只保留正面倾向的板块
				hot[s] = avg
				hotNames = append(hotNames, s)
			}
		}
	}
	if len(hotNames) > 0 {
		log.Printf("LLM热点板块已更新: %d个 %v", len(hotNames), hotNames)
	}

	e.mu.Lock()
	e.llmHotSectors = hot
	e.llmHotSectorsGen++
	e.mu.Unlock()

	if len(hot) > 0 {
		var names []string
		for s := range hot {
			names = append(names, s)
		}
		log.Printf("LLM热点板块已更新: %d个 [%s]", len(hot), strings.Join(names, ","))
	}
}

// llmFallbackSectors 在 LLM API 调用失败时的降级路径：用个股评估中的板块名推断热点。
// 确保即使 LLM 不可用，热点板块扩展机制仍能正常工作，不阻塞 scanCycle。
func (e *Engine) llmFallbackSectors() []data.HotSector {
	e.mu.RLock()
	llm := e.llmHotSectors
	e.mu.RUnlock()
	if len(llm) == 0 {
		return nil
	}
	names := make([]string, 0, len(llm))
	for n := range llm {
		names = append(names, n)
	}
	matched := e.sectorScan.FindSectorsByNames(names)
	if len(matched) == 0 {
		return nil
	}
	out := make([]data.HotSector, len(matched))
	for i, m := range matched {
		score := llm[m.Name]
		out[i] = data.HotSector{Sector: m, Score: score, Reason: "LLM热点"}
	}
	return out
}

// rebuildLLMD1Scores 从 llmTopicCache 重建个股级 D1 评分矩阵。
// 每次 LLM 批量分析完成后调用，将板块级评分按个股所属板块映射到具体个股。
//
// 映射逻辑：
//   - LLM 方向="利空"：该主题涉及的所有板块→所有成分股→llmL1Blocked[code]=true
//   - LLM 方向="中性"：score×0.5 计入 llmL1Score
//   - LLM 方向="利好"：score 原值计入 llmL1Score（取同一只股票多主题的最高分）
//
// 板块→个股的映射优先走 StockCodeMap（硬编码），其次走 stockSectorIdx（倒排索引）。
// rebuildLLMD1Scores 被 processNewsAndLLM_Sync 调用，不单独暴露。
func (e *Engine) rebuildLLMD1Scores() {
	e.mu.RLock()
	cache := make(map[string]*llm.HotTopic, len(e.llmTopicCache))
	for k, v := range e.llmTopicCache {
		cache[k] = v
	}
	idx := make(map[string][]string, len(e.stockSectorIdx))
	for k, v := range e.stockSectorIdx {
		codes := make([]string, len(v))
		copy(codes, v)
		idx[k] = codes
	}
	e.mu.RUnlock()

	blocked := make(map[string]bool)
	scores := make(map[string]float64)

	for _, topic := range cache {
		if len(topic.Sectors) == 0 {
			continue
		}
		// 规范化 direction（LLM 常输出 "中性（需结合数据解读）"）
		dir := topic.Direction
		switch {
		case strings.HasPrefix(dir, "利空"):
			dir = "利空"
		case strings.HasPrefix(dir, "中性"):
			dir = "中性"
		default:
			dir = "利好"
		}

		affectedStocks := make(map[string]bool)

		// LLM 直接指名的个股（最高优先级）
		if len(topic.Stocks) > 0 {
			codes, _ := llm.ResolveStocks(topic.Stocks)
			for _, code := range codes {
				affectedStocks[code] = true
			}
		}

		// 从板块→个股映射
		for _, secField := range topic.Sectors {
			// LLM 有时用 "/" 拼接多个板块，如 "半导体/国产芯片/AI算力/光模块"
			for _, sec := range strings.Split(secField, "/") {
				sec = strings.TrimSpace(sec)
				if sec == "" {
					continue
				}
				// 走 StockCodeMap
				for name, code := range llm.StockCodeMap {
					if strings.Contains(name, sec) || strings.Contains(sec, name) {
						affectedStocks[code] = true
					}
				}
				// 走 stockSectorIdx（倒排）
				for code, secs := range idx {
					for _, s := range secs {
						if strings.Contains(s, sec) || strings.Contains(sec, s) {
							affectedStocks[code] = true
						}
					}
				}
			}
		}

		baseScore := topic.Score
		if baseScore <= 0 {
			continue
		}

		switch dir {
		case "利空":
			for code := range affectedStocks {
				blocked[code] = true
				delete(scores, code)
			}
		case "中性":
			for code := range affectedStocks {
				half := baseScore * 0.5
				if existing, ok := scores[code]; !ok || half > existing {
					scores[code] = half
				}
			}
		default: // 利好
			for code := range affectedStocks {
				if !blocked[code] {
					if existing, ok := scores[code]; !ok || baseScore > existing {
						scores[code] = baseScore
					}
				}
			}
		}
	}

	e.mu.Lock()
	e.llmL1Score = scores
	e.llmL1Blocked = blocked
	e.llmL1Gen++
	e.mu.Unlock()

	if len(scores) > 0 || len(blocked) > 0 {
		log.Printf("LLM D1评分已更新: %d只打分, %d只利空阻塞", len(scores), len(blocked))
	}
}

// preSessionLLMScan 盘前/午前 LLM 批量分析。
// 每个非交易时段触发一次：拉新闻 → 全量LLM分析 → 推断热点板块 → 写入 alert。
// 不碰行情、不扩个股。
func (e *Engine) preSessionLLMScan(now time.Time, session data.MarketSession) {
	day := now.Format("20060102")
	e.mu.RLock()
	alreadyDone := e.llmBatchDate == day && e.llmBatchSession == session
	e.mu.RUnlock()
	if alreadyDone {
		return
	}

	sessionLabel := session.String()

	news := e.coord.GetHotNews(30)
	if len(news) == 0 {
		e.mu.Lock()
		e.llmBatchDate = day
		e.llmBatchSession = session
		e.mu.Unlock()
		log.Printf("[%s] 无新闻可分析", sessionLabel)
		return
	}

	e.mu.Lock()
	e.llmBatchDate = day
	e.llmBatchSession = session
	e.newsCount = len(news)
	e.mu.Unlock()

	var titles []string
	for i, n := range news {
		if i >= 3 {
			break
		}
		titles = append(titles, n.Title)
	}
	e.lastEventDesc = strings.Join(titles, "; ")
	e.lastEventDescAt = now

	if e.llmClient == nil {
		log.Printf("[%s] LLM未配置，跳过分析", sessionLabel)
		return
	}

	go func(items []data.NewsItem, label string) {
		inferredSectors := make(map[string]int)
		analyzed := 0
		for _, item := range items[:minInt(len(items), 10)] {
			key := truncate(item.Title, 60)
			if func() bool {
				e.mu.RLock()
				defer e.mu.RUnlock()
				_, ok := e.llmTopicCache[key]
				return ok
			}() {
				continue
			}
			ht, err := e.llmClient.AnalyzeHotTopic(item.Title)
			if err != nil {
				log.Printf("LLM API失败(使用关键词兜底): %v", err)
				// 继续使用 fallback 结果
			}
			e.mu.Lock()
			e.llmTopicCache[key] = ht
			e.mu.Unlock()
			analyzed++
			for _, s := range ht.Sectors {
				inferredSectors[s]++
			}
		}
		if len(inferredSectors) > 0 {
			var list []string
			for s, cnt := range inferredSectors {
				list = append(list, fmt.Sprintf("%s(%d)", s, cnt))
			}
			body := fmt.Sprintf("%sLLM推断: %s", label, strings.Join(list, ", "))
			log.Printf("[%s] 热点板块: %s", label, body)
			alert := server.Alert{
				Time: now.Format("15:04:05"), Code: "PRE",
				Name: label + "分析", Title: label + "热点板块推断", Body: body,
				Score: 100, Level: label + "监测",
			}
			server.AddAlertToStore(alert)
			e.mu.Lock()
			e.alertLog = append(e.alertLog, alert)
			e.mu.Unlock()
		}
		e.rebuildLLMHotSectors()
		log.Printf("[%s] LLM分析完成: %d条/%d条", label, analyzed, len(items))
	}(news, sessionLabel)
}

// kLinePrevClose 获取股票上一根K线的收盘价，用于计算日内涨跌幅基准。
// 参数 code: 六位股票代码。
func kLinePrevClose(code string, e *Engine) float64 {
	kl, err := e.getCachedKLine(code)
	if err != nil || len(kl) < 2 {
		return 0
	}
	prev := kl[len(kl)-1]
	return prev.Close
}

// buildDragonReturnData 为龙回头策略构建 StockData 输入结构。
// 分析近30根K线：计算首波涨幅、回撤幅度、回撤天数、量比、均线等。
// 若首波涨幅不足35%或峰值位置过近则返回 nil。
func (e *Engine) buildDragonReturnData(code string, si *data.StockInfo, kLines []data.KLine, snap *data.MarketSnapshot) *dragon_return.StockData {
	if len(kLines) < 30 {
		return nil
	}
	n := len(kLines)
	cur := kLines[n-1]
	firstHigh, firstLow := cur.High, cur.Low
	for i := n - 30; i < n; i++ {
		if kLines[i].High > firstHigh {
			firstHigh = kLines[i].High
		}
		if kLines[i].Low < firstLow {
			firstLow = kLines[i].Low
		}
	}
	var startPrice, peakPrice float64
	peakIdx := 0
	for i := n - 1; i >= 0; i-- {
		if kLines[i].High > peakPrice {
			peakPrice = kLines[i].High
			peakIdx = i
		}
	}
	if peakIdx < 5 {
		return nil
	}
	startPrice = kLines[peakIdx-5].Close
	firstRisePct := (peakPrice - startPrice) / startPrice
	if firstRisePct < 0.35 {
		return nil
	}
	pullbackPct := (peakPrice - cur.Close) / peakPrice
	pullbackDays := n - peakIdx
	if pullbackDays > 15 {
		pullbackDays = 15
	}
	peakVol := 0.0
	for i := peakIdx - 3; i <= peakIdx+2 && i < n; i++ {
		if i >= 0 && kLines[i].Volume > peakVol {
			peakVol = kLines[i].Volume
		}
	}
	minVol := cur.Volume
	for i := peakIdx; i < n; i++ {
		if kLines[i].Volume < minVol {
			minVol = kLines[i].Volume
		}
	}
	volumeRatio := minVol / peakVol
	ma5 := calcMA(kLines, n-5, n, data.KLineClose)
	ma10 := calcMA(kLines, n-10, n, data.KLineClose)
	ma20 := calcMA(kLines, n-20, n, data.KLineClose)
	return &dragon_return.StockData{
		Code: code, Name: si.Name, CurrentPrice: cur.Close,
		FirstRisePct: firstRisePct, PullbackPct: pullbackPct, PullbackDays: pullbackDays,
		VolumeRatio: volumeRatio, MA5: ma5, MA10: ma10, MA20: ma20,
		MACDGreen: calcMACDGreen(kLines), HighestPrice: peakPrice, PreviousHigh: peakPrice,
		IsSectorTop2: true, SectorRPS20: 80, SectorRPS60: 70, HasRiseFirst: true,
	}
}

// calcMA 计算指定 K 线区间 [start, end) 的简单移动平均。
// 参数 field: 取值函数（如 data.KLineClose 取收盘价）。
func calcMA(klines []data.KLine, start, end int, field func(data.KLine) float64) float64 {
	if start < 0 {
		start = 0
	}
	if end > len(klines) {
		end = len(klines)
	}
	if start >= end {
		return 0
	}
	sum := 0.0
	for i := start; i < end; i++ {
		sum += field(klines[i])
	}
	return sum / float64(end-start)
}

// calcMACDGreen 计算 MACD 绿柱值（DIF = EMA12 - EMA26）。
// 需要至少26根K线。使用标准 EMA 递推公式：
// EMA = Price * k + EMA_prev * (1 - k), k = 2/(N+1)
func calcMACDGreen(klines []data.KLine) float64 {
	if len(klines) < 26 {
		return 0
	}
	n := len(klines)
	// 初始 EMA = SMA of first N periods
	sma12 := 0.0
	for i := 0; i < 12; i++ {
		sma12 += klines[i].Close
	}
	sma12 /= 12
	sma26 := 0.0
	for i := 0; i < 26; i++ {
		sma26 += klines[i].Close
	}
	sma26 /= 26

	k12 := 2.0 / 13.0
	k26 := 2.0 / 27.0
	// B5: EMA12 从第 13 根 K 线开始递推，EMA26 从第 27 根开始递推
	ema12 := sma12
	for i := 12; i < n; i++ {
		ema12 = klines[i].Close*k12 + ema12*(1-k12)
	}
	ema26 := sma26
	for i := 26; i < n; i++ {
		ema26 = klines[i].Close*k26 + ema26*(1-k26)
	}
	return ema12 - ema26
}

// evalResult 存储单只股票在某个策略下的评估结果。
type evalResult struct {
	eval     *strategy.Evaluation // 策略评估结果（含评分/通过标志等）
	strategy string               // 策略名称：n_shape / dragon / double_bump / dragon_return
}

// evaluateAll 对单只股票执行全部策略评估，返回通过的评估结果列表。
// 评估策略依次为：
//  1. N形两段式（n_shape.EvaluateWave）
//  2. 破局龙（dragon.EvaluateReal）
//  3. 双凸（doubleBump.EvaluateReal）
//  4. 龙回头（dragonReturn.Evaluate）
//
// 参数:
//   - code: 六位股票代码
//   - si: 快照中的个股信息
//   - snap: 完整行情快照
//   - emotionPhase: 市场情绪阶段（冰点/启动/发酵/高潮/背离/退潮）
//   - eventDesc: 当前热点事件描述
//   - benchChg: 基准指数（上证）涨跌幅
func (e *Engine) evaluateAll(code string, si *data.StockInfo, snap *data.MarketSnapshot, emotionPhase, eventDesc string, benchChg float64) []evalResult {
	var results []evalResult
	if si == nil || si.Price <= 0 {
		return results
	}

	kLines, err := e.getCachedKLine(code)
	if err != nil || len(kLines) < 2 {
		return results
	}

	// ——— N形策略 ———
	prev := kLines[len(kLines)-1]
	if time.Since(prev.Date).Hours() > 48 && len(kLines) >= 3 {
		prev = kLines[len(kLines)-3]
	}

	t := snap.Time.Hour()*100 + snap.Time.Minute()
	if t < 925 {
		t = 925
	}
	open := kLines[len(kLines)-1].Open
	chg := safePct(si.Price, open)

	// 获取PE
	pe := 0.0
	fi, _ := e.coord.GetFinancial(code)
	if fi != nil {
		pe = fi.PE
	}

	// 老登分累计：每命中一项+2，总分≥阈值跳过该股票
	cfgTheme := e.cfg.Get().Theme

	// 黑名单快检：先于 老登分 执行，确保金融数据不可用时仍有兜底
	for _, blk := range cfgTheme.BlackList {
		if code == blk {
			return results
		}
	}
	for _, blk := range cfgTheme.StaleBlackList {
		if code == blk {
			return results
		}
	}

	staleScore := 0

	// ① 市值高
	if fi != nil && fi.MarketCap > 0 && cfgTheme.MaxMarketCap > 0 && fi.MarketCap > cfgTheme.MaxMarketCap {
		staleScore += 2
	}
	// ② PE 低
	if fi != nil && fi.PE > 0 && cfgTheme.MinPE > 0 && fi.PE < cfgTheme.MinPE {
		staleScore += 2
	}
	// ③ 20日平均真实换手率低：今日换手率→倒推流通股本→算历史每日换手率→平均
	if cfgTheme.MinTurnover > 0 && si.Turnover > 0.1 && si.Volume > 0 {
		floatShares := si.Volume / (si.Turnover / 100.0) // 流通股本（股）
		var turnoverSum float64
		var turnoverCnt int
		n := len(kLines)
		start := n - 20
		if start < 0 {
			start = 0
		}
		for i := start; i < n; i++ {
			if kLines[i].Volume > 0 {
				turnoverSum += kLines[i].Volume / floatShares * 100.0
				turnoverCnt++
			}
		}
		if turnoverCnt > 0 && (turnoverSum/float64(turnoverCnt)) < cfgTheme.MinTurnover {
			staleScore += 2
		}
	}
	// ④ 20日收益率标准差低（波动小）
	if cfgTheme.MinVol20d > 0 && len(kLines) >= 21 {
		n := len(kLines)
		start := n - 20
		returns := make([]float64, 0, 20)
		for i := start; i < n; i++ {
			if kLines[i-1].Close > 0 {
				r := (kLines[i].Close - kLines[i-1].Close) / kLines[i-1].Close * 100.0
				returns = append(returns, r)
			}
		}
		if len(returns) >= 10 {
			var mean, sqSum float64
			for _, r := range returns {
				mean += r
			}
			mean /= float64(len(returns))
			for _, r := range returns {
				d := r - mean
				sqSum += d * d
			}
			stddev := math.Sqrt(sqSum / float64(len(returns)))
			if stddev < cfgTheme.MinVol20d {
				staleScore += 2
			}
		}
	}

	if cfgTheme.StaleScoreThreshold > 0 && staleScore >= cfgTheme.StaleScoreThreshold {
		return results
	}

	// 计算20日均量
	avgVol := 0.0
	if len(kLines) >= 20 {
		for i := len(kLines) - 20; i < len(kLines); i++ {
			avgVol += kLines[i].Volume
		}
		avgVol /= 20.0
	}

	// 1分钟MACD
	dif, dea, _ := e.calcMinuteMACD(code)

	state := e.getNState(code)
	isLeftSignal := state.Phase == n_shape.NPhaseFirstBreakout || state.Phase == n_shape.NPhaseSecondBreakout

	// 10日涨幅
	gain10d := 0.0
	if len(kLines) >= 11 {
		gain10d = safePct(kLines[len(kLines)-1].Close, kLines[len(kLines)-11].Close)
	}

	// 集合竞价数据（9:15-9:25）
	var auctionVol, auctionChgPct float64
	if t >= 915 && t < 925 {
		if auctionSi, aerr := e.coord.GetAuctionData(code); aerr == nil && auctionSi != nil && auctionSi.Price > 0 {
			auctionVol = auctionSi.Volume
			auctionChgPct = safePct(auctionSi.Price, prev.Close)
		}
	}

	// 资金流向（60s缓存）
	netInflow := si.NetInflow
	e.mu.RLock()
	if cf, ok := e.moneyFlowCache[code]; ok && time.Since(e.moneyFlowCacheTime[code]) < 60*time.Second {
		netInflow = cf.NetInflow
	}
	e.mu.RUnlock()
	if netInflow == 0 && si.NetInflow > 0 {
		netInflow = si.NetInflow
	}
	if netInflow == 0 {
		if cf, err := e.coord.GetStockMoneyFlow(code); err == nil && cf != nil {
			netInflow = cf.NetInflow
			e.mu.Lock()
			e.moneyFlowCache[code] = cf
			e.moneyFlowCacheTime[code] = time.Now()
			e.mu.Unlock()
		}
	}

	// 筹码分析
	chipScore := 0.0
	if len(kLines) >= 30 {
		params := data.DefaultChipParams()
		if ca := data.TriangularChipDistribution(kLines, params); ca != nil {
			chipScore = ca.Score
		}
	}

	// 板块龙头
	isLeader := false
	e.mu.RLock()
	if e.sectorLeaders[code] {
		isLeader = true
	}
	e.mu.RUnlock()

	acp := chg
	if auctionChgPct != 0 {
		acp = auctionChgPct
	}
	ib := &n_shape.IntradayB{
		TTime: t, CurPrice: si.Price, CumVol: si.Volume,
		AuctionChgPct: acp, EventType: "normal",
		PrevClose: prev.Close, PrevHigh: prev.High, PrevLow: prev.Low,
		MinuteMACDDIF: dif, MinuteMACDDEA: dea,
		AvgDailyVol: avgVol,
		BenchCurChg: benchChg,
		AuctionVol:  auctionVol,
	}

	sectorAmt, sectorAmtMA := 0.0, 0.0
	for _, sec := range snap.Sector {
		match := strings.Contains(si.Name, sec.Name)
		if !match && len(si.Name) >= 2 {
			match = strings.Contains(sec.Name, si.Name[:2])
		}
		if match {
			sectorAmt = sec.Amount
			e.mu.Lock()
			sectorAmtMA = e.sectorAmtPrev[sec.Name]
			if sectorAmt > e.sectorAmtToday[sec.Name] {
				e.sectorAmtToday[sec.Name] = sectorAmt
			}
			// 首日冷启动：用当日数据 seed sectorAmtPrev
			if sectorAmtMA == 0 && sectorAmt > 0 {
				e.sectorAmtPrev[sec.Name] = sectorAmt
				sectorAmtMA = sectorAmt
			}
			e.mu.Unlock()
			break
		}
	}

	// IPO日历 → D1事件增强
	if ipo := e.coord.GetIPOByCode(code); ipo != nil {
		if ipo.ListStatus == "U" {
			eventDesc += "; IPO注册申请获批 即将上市 " + ipo.Name
		} else if isRecentIPO(ipo.ListingDate, 7) {
			eventDesc += "; 新股上市 " + ipo.Name + " 次新股"
		}
	}

	// 读取 LLM D1 评分（按代码查）
	llmD1Score := 0.0
	llmBlocked := false
	e.mu.RLock()
	if len(e.llmL1Score) > 0 {
		if s, ok := e.llmL1Score[code]; ok {
			llmD1Score = s
		}
		if b, ok := e.llmL1Blocked[code]; ok {
			llmBlocked = b
		}
	}
	e.mu.RUnlock()

	nEval, err := e.nShape.EvaluateWave(&n_shape.WaveA{
		AOpen: prev.Open, AHigh: prev.High, ALow: prev.Low,
		AClose: prev.Close, AVol: prev.Volume,
		AChgPct: safePct(prev.Close, prev.Open), AAboveMA60: true,
		IsSectorLeader: isLeader,
	}, ib, &n_shape.Ctx{
		LLMD1Score: llmD1Score, LLMBlocked: llmBlocked,
		EmotionPhase: emotionPhase, EventDesc: eventDesc,
		SectorTurnover: sectorAmt, SectorTurnoverMA20: sectorAmtMA,
		StockPE: pe, AvgDailyVol: avgVol,
	})
	if err == nil && nEval != nil && nEval.TotalScore > 0 {
		nEval.Details["n_phase"] = float64(state.Phase)
		nEval.Details["gain_10d"] = gain10d
		nEval.Details["turnover"] = si.Turnover
		nEval.Details["net_inflow"] = netInflow
		nEval.Details["chip_score"] = chipScore
		nEval.Details["is_leader"] = 0
		if isLeader {
			nEval.Details["is_leader"] = 1
		}
		// 左侧一突信号
		if isLeftSignal {
			nEval.Details["left_signal"] = 1.0
		}
		results = append(results, evalResult{eval: nEval, strategy: "n_shape"})
	}

	// ——— 破局龙 ———
	dEval := e.dragon.EvaluateReal(code, si, kLines, snap.Sector)
	if dEval != nil && dEval.TotalScore > 0 {
		if dEval.Details == nil {
			dEval.Details = make(map[string]float64)
		}
		dEval.Details["chip_score"] = chipScore
		dEval.Details["net_inflow"] = netInflow
		dEval.Details["gain_10d"] = gain10d
		dEval.Details["turnover"] = si.Turnover
		results = append(results, evalResult{eval: dEval, strategy: "dragon"})
	}

	// ——— 双凸 ———
	bEval := e.doubleBump.EvaluateReal(code, si, kLines)
	if bEval != nil && bEval.TotalScore > 0 {
		if bEval.Details == nil {
			bEval.Details = make(map[string]float64)
		}
		bEval.Details["chip_score"] = chipScore
		bEval.Details["net_inflow"] = netInflow
		bEval.Details["gain_10d"] = gain10d
		bEval.Details["turnover"] = si.Turnover
		results = append(results, evalResult{eval: bEval, strategy: "double_bump"})
	}

	// ——— 龙回头 ———
	drData := e.buildDragonReturnData(code, si, kLines, snap)
	if drData != nil {
		drEval, err := e.dragonReturn.Evaluate(code, drData)
		if err == nil && drEval != nil && drEval.TotalScore > 0 {
			if drEval.Details == nil {
				drEval.Details = make(map[string]float64)
			}
			drEval.Details["chip_score"] = chipScore
			drEval.Details["net_inflow"] = netInflow
			drEval.Details["gain_10d"] = gain10d
			drEval.Details["turnover"] = si.Turnover
			results = append(results, evalResult{eval: drEval, strategy: "dragon_return"})
		}
	}

	// ——— 通用动量评分（不计入 signals，仅展示） ———
	mScore := calcMomentum(si, kLines, gain10d, chipScore, netInflow)
	results = append(results, evalResult{
		eval: &strategy.Evaluation{
			TotalScore: mScore,
			Pass:       mScore >= 60,
			Level: func() string {
				if mScore >= 80 {
					return "strong"
				} else if mScore >= 60 {
					return "moderate"
				}
				return "weak"
			}(),
			Confidence: mScore / 100.0,
		},
		strategy: "momentum",
	})

	return results
}

// calcMomentum 通用动量评分（0-100）。
// 客观衡量个股上涨动量，下跌股自然低分。
// 核心逻辑：量价同向才计高量比分，下跌无量低分，下跌放量中低分。
// 不产生交易信号，仅用于热度排序展示。
func calcMomentum(si *data.StockInfo, kLines []data.KLine, gain10d, chipScore, netInflow float64) float64 {
	volRatio := 0.0
	if len(kLines) >= 20 {
		avgVol := 0.0
		for i := len(kLines) - 20; i < len(kLines); i++ {
			avgVol += kLines[i].Volume
		}
		avgVol /= 20.0
		if avgVol > 0 && si.Volume > 0 {
			volRatio = si.Volume / avgVol
		}
	}

	// 方向加权量比 0-25：上涨放量高分，下跌放量中低分，无量低分
	volScore := 0.0
	if si.ChangePct > 0 {
		volScore = 25.0 * math.Min(volRatio/2.0, 1.0)
	} else {
		volScore = 8.0 * math.Min(volRatio/2.0, 1.0)
	}

	// 换手率 0-10：高换手伴随上涨才加分
	turnScore := 0.0
	if si.ChangePct > 0 {
		turnScore = 10.0 * math.Min(si.Turnover/8.0, 1.0)
	}

	// 5日趋势 0-30：上涨趋势高分，下跌趋势低分
	trendScore := 2.0
	switch {
	case gain10d >= 10:
		trendScore = 30
	case gain10d >= 7:
		trendScore = 24
	case gain10d >= 5:
		trendScore = 18
	case gain10d >= 3:
		trendScore = 14
	case gain10d >= 1:
		trendScore = 8
	case gain10d >= 0:
		trendScore = 4
	case gain10d >= -3:
		trendScore = 2
	}

	// 日内强度 0-25：红盘起评分高，绿盘扣到很低
	dayScore := 0.0
	switch {
	case si.ChangePct >= 7:
		dayScore = 25
	case si.ChangePct >= 5:
		dayScore = 22
	case si.ChangePct >= 3:
		dayScore = 18
	case si.ChangePct >= 1:
		dayScore = 14
	case si.ChangePct >= 0:
		dayScore = 8
	case si.ChangePct >= -2:
		dayScore = 4
	case si.ChangePct >= -4:
		dayScore = 2
	default:
		dayScore = 1
	}

	// 成交额 0-10：仅上涨有效时加分
	valueScore := 0.0
	if si.ChangePct > 0 && si.Amount > 0 {
		valueScore = 10.0 * math.Min(si.Amount/5e8, 1.0)
	}

	total := volScore + turnScore + trendScore + dayScore + valueScore
	if total > 100 {
		total = 100
	}
	return total
}

// 用于将策略评估中的 d1/d2/d3/d4 元数据映射到 SignalView。
func dFromEval(sig *strategy.Signal, key string) float64 {
	if sig.Meta == nil {
		return 0
	}
	v, ok := sig.Meta[key]
	if !ok {
		return 0
	}
	return v
}

// safePct 计算百分比变化 (a-b)/b*100，当分母为零时返回 0 避免除零错误。
func safePct(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return (a - b) / b * 100
}

// minFloat 返回两个浮点数中的较小值。
func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// remindLevel 根据信号优先级和置信度计算提醒级别。
// P1-P2 → strong（强提醒）；置信度≥0.5 → observe（观察）；
// 置信度≥0.2 → near（关注）；否则 → mute（静默）。
func remindLevel(p strategy.Priority, c float64) string {
	if p >= strategy.P1 && p <= strategy.P2 {
		return "strong"
	}
	if c >= 0.5 {
		return "observe"
	}
	if c >= 0.2 {
		return "near"
	}
	return "mute"
}

// isHeld 判断指定股票是否已在持仓中，用于 N 形标的"只出不进"规则。
func (e *Engine) isHeld(code string) bool {
	if e.holdings == nil {
		return false
	}
	h := e.holdings.Get()
	for _, hh := range h.Holdings {
		if hh.Code == code {
			return true
		}
	}
	return false
}

// countLevel 统计信号视图中指定提醒级别的数量。
// 参数 level: "strong"/"observe"/"near"/"mute"。
func countLevel(views []server.SignalView, level string) int {
	n := 0
	for _, v := range views {
		if v.RemindLevel == level {
			n++
		}
	}
	return n
}

// parseFloat 将字符串转为 float64，空字符串或解析失败返回 0。
func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// fetchBenchChg 从新浪财经接口获取上证指数涨跌幅，用于N形评估中的基准对比。
// 通过解析新浪CSV格式返回体提取当前价和昨收计算百分比。
func (e *Engine) fetchBenchChg() float64 {
	url := "https://hq.sinajs.cn/list=s_sh000001"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Referer", "https://finance.sina.com.cn")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0
	}
	raw := string(body)
	eq := strings.IndexByte(raw, '"')
	if eq < 0 {
		return 0
	}
	raw = raw[eq+1:]
	eq = strings.IndexByte(raw, '"')
	if eq < 0 {
		return 0
	}
	fields := strings.Split(raw[:eq], ",")
	// sina index: name,open,prev_close,cur,high,low,...
	if len(fields) < 4 {
		return 0
	}
	prevClose := parseFloat(fields[2])
	cur := parseFloat(fields[3])
	if prevClose <= 0 {
		return 0
	}
	return (cur - prevClose) / prevClose * 100
}

// truncate 截断字符串到指定长度。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// minInt 返回两个整数中的较小值。
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// crossCheckSignals 对策略信号用东财/Tushare/同花顺复核价格。
// 仅做日志告警，不阻断信号——Sina 仍然是数据主力。
func (e *Engine) crossCheckSignals(signals []strategy.Signal) {
	checked := 0
	for i := range signals {
		if checked >= 3 {
			break
		}
		sig := &signals[i]
		// 用东财复核
		if emPrice, err := e.coord.CrossCheckPrice(sig.Code); err == nil && emPrice > 0 {
			diff := safeDiff(sig.Price, emPrice)
			if diff > 0.005 {
				log.Printf("[复核] %s Sina=%.2f 东财=%.2f 差异=%.3f%%", sig.Code, sig.Price, emPrice, diff*100)
			}
		}
		// 用 Tushare 复核（仅非熔断期）
		if e.coord.HasTushare() {
			today := time.Now().Format("20060102")
			kl, klErr := e.coord.GetDailyTS(sig.Code, today, today)
			if klErr == nil && len(kl) > 0 {
				tsPrice := kl[len(kl)-1].Close
				diff := safeDiff(sig.Price, tsPrice)
				if diff > 0.005 {
					log.Printf("[复核] %s Sina=%.2f Tushare=%.2f 差异=%.3f%%", sig.Code, sig.Price, tsPrice, diff*100)
				}
			}
		}
		checked++
	}
}

// safeDiff 计算两个价格的绝对差异比例。
func safeDiff(a, b float64) float64 {
	max := a
	if b > max {
		max = b
	}
	if max <= 0 {
		return 0
	}
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff / max
}

// isRecentIPO 判断上市日期是否在最近 N 天内（含 N）。
func isRecentIPO(listingDate string, days int) bool {
	if listingDate == "" {
		return false
	}
	t, err := time.Parse("20060102", listingDate)
	if err != nil {
		return false
	}
	return time.Since(t) <= time.Duration(days*24)*time.Hour
}
