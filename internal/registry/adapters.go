package registry

import (
	"context"
	"log"
	"net/http"
	"os"

	"quant-trading/internal/calendar"
	"quant-trading/internal/config"
	"quant-trading/internal/data"
	"quant-trading/internal/filter"
	"quant-trading/internal/llm"
	"quant-trading/internal/notify"
	"quant-trading/internal/position"
	"quant-trading/internal/risk"
	"quant-trading/internal/server"
	"quant-trading/internal/strategy/double_bump"
	"quant-trading/internal/strategy/dragon"
	"quant-trading/internal/strategy/dragon_return"
	"quant-trading/internal/strategy/korea"
	"quant-trading/internal/strategy/n_shape"
	"quant-trading/internal/validate"
)

const defaultLLMModel = "Qwen/Qwen3-8B"

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Params holds shared dependencies passed into RegisterAll.
type Params struct {
	Cfg     *config.Manager
	TsToken string
	LlmKey  string
	H5FS    http.FileSystem
}

// ── Adapter: Market API ────────────────────────────────────────

type MarketAPIAdapter struct {
	status Status
	api    *data.MarketAPI
}

func (s *MarketAPIAdapter) Name() string       { return "market_api" }
func (s *MarketAPIAdapter) Priority() Priority { return PriorityCritical }
func (s *MarketAPIAdapter) Status() Status     { return s.status }
func (s *MarketAPIAdapter) Start(context.Context) error {
	s.api = data.NewMarketAPI()
	s.status = StatusReady
	return nil
}
func (s *MarketAPIAdapter) Stop(context.Context) error { s.status = StatusStopped; return nil }
func (s *MarketAPIAdapter) Get() *data.MarketAPI       { return s.api }

// ── Adapter: Tushare Client ────────────────────────────────────

type TushareClientAdapter struct {
	status Status
	token  string
	client *data.TushareClient
}

func (s *TushareClientAdapter) Name() string       { return "tushare_client" }
func (s *TushareClientAdapter) Priority() Priority { return PriorityCritical }
func (s *TushareClientAdapter) Status() Status     { return s.status }
func (s *TushareClientAdapter) Start(context.Context) error {
	s.client = data.NewTushareClient(s.token)
	s.status = StatusReady
	return nil
}
func (s *TushareClientAdapter) Stop(context.Context) error { s.status = StatusStopped; return nil }
func (s *TushareClientAdapter) Get() *data.TushareClient   { return s.client }

// ── Adapter: THS Client ────────────────────────────────────────

type THSClientAdapter struct {
	status Status
	client *data.THSClient
}

func (s *THSClientAdapter) Name() string       { return "ths_client" }
func (s *THSClientAdapter) Priority() Priority { return PriorityCritical }
func (s *THSClientAdapter) Status() Status     { return s.status }
func (s *THSClientAdapter) Start(context.Context) error {
	s.client = data.NewTHSClient()
	s.status = StatusReady
	return nil
}
func (s *THSClientAdapter) Stop(context.Context) error { s.status = StatusStopped; return nil }
func (s *THSClientAdapter) Get() *data.THSClient       { return s.client }

// ── Adapter: Data Coordinator ──────────────────────────────────

type DataCoordinatorAdapter struct {
	status  Status
	market  *MarketAPIAdapter
	tushare *TushareClientAdapter
	ths     *THSClientAdapter
	dc      *data.DataCoordinator
}

func (s *DataCoordinatorAdapter) Name() string       { return "data_coordinator" }
func (s *DataCoordinatorAdapter) Priority() Priority { return PriorityCritical }
func (s *DataCoordinatorAdapter) Status() Status     { return s.status }
func (s *DataCoordinatorAdapter) Start(context.Context) error {
	jq := data.NewJQClient(os.Getenv("JQ_MOBILE"), os.Getenv("JQ_PASSWORD"))
	s.dc = data.NewDataCoordinator(s.market.Get(), s.tushare.Get(), s.ths.Get(), jq)
	s.status = StatusReady
	return nil
}
func (s *DataCoordinatorAdapter) Stop(context.Context) error { s.status = StatusStopped; return nil }
func (s *DataCoordinatorAdapter) Get() *data.DataCoordinator { return s.dc }

// ── Adapter: Sector Scanner ────────────────────────────────────

type SectorScannerAdapter struct {
	status  Status
	market  *MarketAPIAdapter
	matcher *EventMatcherAdapter
	scanner *data.SectorScanner
}

func (s *SectorScannerAdapter) Name() string       { return "sector_scanner" }
func (s *SectorScannerAdapter) Priority() Priority { return PriorityBusiness }
func (s *SectorScannerAdapter) Status() Status     { return s.status }
func (s *SectorScannerAdapter) Start(context.Context) error {
	s.scanner = data.NewSectorScanner(s.market.Get(), s.matcher.Get())
	s.status = StatusReady
	return nil
}
func (s *SectorScannerAdapter) Stop(context.Context) error { s.status = StatusStopped; return nil }
func (s *SectorScannerAdapter) Get() *data.SectorScanner   { return s.scanner }

// ── Adapter: RPS Manager ──────────────────────────────────────

type RPSManagerAdapter struct {
	status Status
	rps    *data.RPSManager
}

func (s *RPSManagerAdapter) Name() string       { return "rps_manager" }
func (s *RPSManagerAdapter) Priority() Priority { return PriorityBusiness }
func (s *RPSManagerAdapter) Status() Status     { return s.status }
func (s *RPSManagerAdapter) Start(context.Context) error {
	s.rps = data.NewRPSManager()
	s.status = StatusReady
	return nil
}
func (s *RPSManagerAdapter) Stop(context.Context) error { s.status = StatusStopped; return nil }
func (s *RPSManagerAdapter) Get() *data.RPSManager      { return s.rps }

// ── Adapter: Fetcher ──────────────────────────────────────────

type FetcherAdapter struct {
	status  Status
	dc      *DataCoordinatorAdapter
	stocks  []string
	fetcher *data.Fetcher
}

func (s *FetcherAdapter) Name() string       { return "fetcher" }
func (s *FetcherAdapter) Priority() Priority { return PriorityBusiness }
func (s *FetcherAdapter) Status() Status     { return s.status }
func (s *FetcherAdapter) Start(context.Context) error {
	s.fetcher = data.NewFetcher(s.stocks, s.dc.Get())
	s.status = StatusReady
	return nil
}
func (s *FetcherAdapter) Stop(context.Context) error {
	if s.fetcher != nil {
		s.fetcher.Stop()
	}
	s.status = StatusStopped
	return nil
}
func (s *FetcherAdapter) Get() *data.Fetcher { return s.fetcher }

// ── Adapter: Event Matcher ────────────────────────────────────

type EventMatcherAdapter struct {
	status  Status
	matcher *data.EventMatcher
}

func (s *EventMatcherAdapter) Name() string       { return "event_matcher" }
func (s *EventMatcherAdapter) Priority() Priority { return PriorityCritical }
func (s *EventMatcherAdapter) Status() Status     { return s.status }
func (s *EventMatcherAdapter) Start(ctx context.Context) error {
	eventsCfg, err := data.LoadEvents("config/events_leftside.yaml")
	if err != nil {
		eventsCfg, err = data.LoadEvents("events_leftside.yaml")
	}
	if err == nil {
		s.matcher = data.NewEventMatcher(eventsCfg)
	}
	if s.matcher == nil {
		log.Printf("registry: event_matcher started without events config")
	}
	s.status = StatusReady
	return nil
}
func (s *EventMatcherAdapter) Stop(context.Context) error { s.status = StatusStopped; return nil }
func (s *EventMatcherAdapter) Get() *data.EventMatcher    { return s.matcher }

// ── Adapter: Risk Engine ──────────────────────────────────────

type RiskAdapter struct {
	status Status
	cfg    *config.Manager
	risk   *risk.Engine
}

func (s *RiskAdapter) Name() string       { return "risk_engine" }
func (s *RiskAdapter) Priority() Priority { return PriorityBusiness }
func (s *RiskAdapter) Status() Status     { return s.status }
func (s *RiskAdapter) Start(context.Context) error {
	s.risk = risk.New(s.cfg)
	s.status = StatusReady
	return nil
}
func (s *RiskAdapter) Stop(context.Context) error { s.status = StatusStopped; return nil }
func (s *RiskAdapter) Get() *risk.Engine          { return s.risk }

// ── Adapter: Position Manager ──────────────────────────────────

type PositionAdapter struct {
	status Status
	cfg    *config.Manager
	mgr    *position.Manager
}

func (s *PositionAdapter) Name() string       { return "position_manager" }
func (s *PositionAdapter) Priority() Priority { return PriorityBusiness }
func (s *PositionAdapter) Status() Status     { return s.status }
func (s *PositionAdapter) Start(context.Context) error {
	s.mgr = position.New(s.cfg)
	s.status = StatusReady
	return nil
}
func (s *PositionAdapter) Stop(context.Context) error { s.status = StatusStopped; return nil }
func (s *PositionAdapter) Get() *position.Manager     { return s.mgr }

// ── Adapter: Validator ────────────────────────────────────────

type ValidatorAdapter struct {
	status Status
	cfg    *config.Manager
	v      *validate.Engine
}

func (s *ValidatorAdapter) Name() string       { return "validator" }
func (s *ValidatorAdapter) Priority() Priority { return PriorityBusiness }
func (s *ValidatorAdapter) Status() Status     { return s.status }
func (s *ValidatorAdapter) Start(context.Context) error {
	s.v = validate.New(s.cfg)
	s.status = StatusReady
	return nil
}
func (s *ValidatorAdapter) Stop(context.Context) error { s.status = StatusStopped; return nil }
func (s *ValidatorAdapter) Get() *validate.Engine      { return s.v }

// ── Adapter: Filter Engine ─────────────────────────────────────

type FilterAdapter struct {
	status Status
	cfg    *config.Manager
	dc     *DataCoordinatorAdapter
	f      *filter.Engine
}

func (s *FilterAdapter) Name() string       { return "filter_engine" }
func (s *FilterAdapter) Priority() Priority { return PriorityBusiness }
func (s *FilterAdapter) Status() Status     { return s.status }
func (s *FilterAdapter) Start(context.Context) error {
	s.f = filter.New(s.cfg)
	if s.dc != nil {
		s.f.SetCoordinator(s.dc.Get())
	}
	s.status = StatusReady
	return nil
}
func (s *FilterAdapter) Stop(context.Context) error { s.status = StatusStopped; return nil }
func (s *FilterAdapter) Get() *filter.Engine        { return s.f }

// ── Adapter: Dragon ────────────────────────────────────────────

type DragonAdapter struct {
	status Status
	cfg    *config.Manager
	strat  *dragon.DragonStrategy
}

func (s *DragonAdapter) Name() string       { return "strategy_dragon" }
func (s *DragonAdapter) Priority() Priority { return PriorityBusiness }
func (s *DragonAdapter) Status() Status     { return s.status }
func (s *DragonAdapter) Start(context.Context) error {
	s.strat = dragon.New(s.cfg)
	s.status = StatusReady
	return nil
}
func (s *DragonAdapter) Stop(context.Context) error  { s.status = StatusStopped; return nil }
func (s *DragonAdapter) Get() *dragon.DragonStrategy { return s.strat }

// ── Adapter: Double Bump ───────────────────────────────────────

type DoubleBumpAdapter struct {
	status Status
	cfg    *config.Manager
	strat  *double_bump.DoubleBumpStrategy
}

func (s *DoubleBumpAdapter) Name() string       { return "strategy_double_bump" }
func (s *DoubleBumpAdapter) Priority() Priority { return PriorityBusiness }
func (s *DoubleBumpAdapter) Status() Status     { return s.status }
func (s *DoubleBumpAdapter) Start(context.Context) error {
	s.strat = double_bump.New(s.cfg)
	s.status = StatusReady
	return nil
}
func (s *DoubleBumpAdapter) Stop(context.Context) error           { s.status = StatusStopped; return nil }
func (s *DoubleBumpAdapter) Get() *double_bump.DoubleBumpStrategy { return s.strat }

// ── Adapter: N-Shape ───────────────────────────────────────────

type NShapeAdapter struct {
	status  Status
	cfg     *config.Manager
	matcher *EventMatcherAdapter
	strat   *n_shape.NShapeStrategy
}

func (s *NShapeAdapter) Name() string       { return "strategy_n_shape" }
func (s *NShapeAdapter) Priority() Priority { return PriorityBusiness }
func (s *NShapeAdapter) Status() Status     { return s.status }
func (s *NShapeAdapter) Start(context.Context) error {
	s.strat = n_shape.New(s.cfg, s.matcher.Get())
	s.status = StatusReady
	return nil
}
func (s *NShapeAdapter) Stop(context.Context) error   { s.status = StatusStopped; return nil }
func (s *NShapeAdapter) Get() *n_shape.NShapeStrategy { return s.strat }

// ── Adapter: Dragon Return ─────────────────────────────────────

type DragonReturnAdapter struct {
	status Status
	cfg    *config.Manager
	strat  *dragon_return.DragonReturnStrategy
}

func (s *DragonReturnAdapter) Name() string       { return "strategy_dragon_return" }
func (s *DragonReturnAdapter) Priority() Priority { return PriorityBusiness }
func (s *DragonReturnAdapter) Status() Status     { return s.status }
func (s *DragonReturnAdapter) Start(context.Context) error {
	s.strat = dragon_return.New(s.cfg)
	s.status = StatusReady
	return nil
}
func (s *DragonReturnAdapter) Stop(context.Context) error               { s.status = StatusStopped; return nil }
func (s *DragonReturnAdapter) Get() *dragon_return.DragonReturnStrategy { return s.strat }

// ── Adapter: Calendar ──────────────────────────────────────────

type CalendarAdapter struct {
	status Status
	cfg    *config.Manager
	cal    *calendar.Calendar
}

func (s *CalendarAdapter) Name() string       { return "calendar" }
func (s *CalendarAdapter) Priority() Priority { return PriorityBusiness }
func (s *CalendarAdapter) Status() Status     { return s.status }
func (s *CalendarAdapter) Start(context.Context) error {
	s.cal = calendar.New(s.cfg.Get().Calendar.Events)
	s.status = StatusReady
	return nil
}
func (s *CalendarAdapter) Stop(context.Context) error { s.status = StatusStopped; return nil }
func (s *CalendarAdapter) Get() *calendar.Calendar    { return s.cal }

// ── Adapter: Korea Linkage ─────────────────────────────────────

type KoreaAdapter struct {
	status Status
	cfg    *config.Manager
	lnk    *korea.KoreaLinkage
}

func (s *KoreaAdapter) Name() string       { return "korea_linkage" }
func (s *KoreaAdapter) Priority() Priority { return PriorityBusiness }
func (s *KoreaAdapter) Status() Status     { return s.status }
func (s *KoreaAdapter) Start(context.Context) error {
	s.lnk = korea.New(s.cfg.Get().Korea)
	s.status = StatusReady
	return nil
}
func (s *KoreaAdapter) Stop(context.Context) error { s.status = StatusStopped; return nil }
func (s *KoreaAdapter) Get() *korea.KoreaLinkage   { return s.lnk }

// ── Adapter: LLM Client ────────────────────────────────────────

type LLMAdapter struct {
	status Status
	key    string
	model  string
	client *llm.Client
}

func (s *LLMAdapter) Name() string       { return "llm_client" }
func (s *LLMAdapter) Priority() Priority { return PriorityBusiness }
func (s *LLMAdapter) Status() Status     { return s.status }
func (s *LLMAdapter) Start(ctx context.Context) error {
	s.client = llm.New(s.key, s.model)
	s.status = StatusReady
	model := s.model
	if model == "" {
		model = defaultLLMModel
	}
	if s.key != "" {
		mask := s.key[:minInt(len(s.key), 12)] + "..."
		log.Printf("[LLM] 客户端已创建 model=%s api_key=%s", model, mask)
	} else {
		log.Printf("[LLM] ⚠ 客户端已创建但 api_key 为空，LLM功能不可用")
	}
	return nil
}
func (s *LLMAdapter) Stop(context.Context) error { s.status = StatusStopped; return nil }
func (s *LLMAdapter) Get() *llm.Client           { return s.client }

// ── Adapter: Notifier ──────────────────────────────────────────

type NotifierAdapter struct {
	status Status
	n      *notify.Notifier
}

func (s *NotifierAdapter) Name() string       { return "notifier" }
func (s *NotifierAdapter) Priority() Priority { return PriorityBusiness }
func (s *NotifierAdapter) Status() Status     { return s.status }
func (s *NotifierAdapter) Start(context.Context) error {
	s.n = notify.New()
	s.status = StatusReady
	return nil
}
func (s *NotifierAdapter) Stop(context.Context) error { s.status = StatusStopped; return nil }
func (s *NotifierAdapter) Get() *notify.Notifier      { return s.n }

// ── Adapter: History ───────────────────────────────────────────

type HistoryAdapter struct {
	status Status
	dir    string
	h      *notify.History
}

func (s *HistoryAdapter) Name() string       { return "history" }
func (s *HistoryAdapter) Priority() Priority { return PriorityEdge }
func (s *HistoryAdapter) Status() Status     { return s.status }
func (s *HistoryAdapter) Start(context.Context) error {
	s.h = notify.NewHistory(s.dir)
	s.status = StatusReady
	return nil
}
func (s *HistoryAdapter) Stop(context.Context) error {
	if s.h != nil {
		s.h.Close()
	}
	s.status = StatusStopped
	return nil
}
func (s *HistoryAdapter) Get() *notify.History { return s.h }

// ── Adapter: Holdings ──────────────────────────────────────────

type HoldingsAdapter struct {
	status Status
	dir    string
	hm     *position.HoldingsManager
}

func (s *HoldingsAdapter) Name() string       { return "holdings" }
func (s *HoldingsAdapter) Priority() Priority { return PriorityEdge }
func (s *HoldingsAdapter) Status() Status     { return s.status }
func (s *HoldingsAdapter) Start(context.Context) error {
	s.hm = position.NewHoldingsManager(s.dir)
	s.status = StatusReady
	return nil
}
func (s *HoldingsAdapter) Stop(context.Context) error     { s.status = StatusStopped; return nil }
func (s *HoldingsAdapter) Get() *position.HoldingsManager { return s.hm }

// ── Adapter: Watchlist ─────────────────────────────────────────

type WatchlistAdapter struct {
	status Status
	dir    string
	wl     *data.WatchlistManager
}

func (s *WatchlistAdapter) Name() string       { return "watchlist" }
func (s *WatchlistAdapter) Priority() Priority { return PriorityEdge }
func (s *WatchlistAdapter) Status() Status     { return s.status }
func (s *WatchlistAdapter) Start(context.Context) error {
	s.wl = data.NewWatchlistManager(s.dir)
	s.wl.Load()
	s.status = StatusReady
	return nil
}
func (s *WatchlistAdapter) Stop(context.Context) error  { s.status = StatusStopped; return nil }
func (s *WatchlistAdapter) Get() *data.WatchlistManager { return s.wl }

// ── Adapter: HTTP Server ───────────────────────────────────────

type HTTPServerAdapter struct {
	status Status
	cfg    *config.Manager
	v      *ValidatorAdapter
	h5FS   http.FileSystem
	eng    server.EngineAPI
	srv    *server.Server
	addr   string
}

func (s *HTTPServerAdapter) Name() string                   { return "http_server" }
func (s *HTTPServerAdapter) Priority() Priority             { return PriorityCritical }
func (s *HTTPServerAdapter) Status() Status                 { return s.status }
func (s *HTTPServerAdapter) SetEngine(eng server.EngineAPI) { s.eng = eng }
func (s *HTTPServerAdapter) Start(ctx context.Context) error {
	if s.eng == nil {
		s.status = StatusFailed
		return nil
	}
	s.srv = server.New(s.cfg, s.eng, s.v.Get(), "", s.h5FS)
	s.status = StatusReady
	return nil
}
func (s *HTTPServerAdapter) Stop(ctx context.Context) error {
	if s.srv != nil {
		s.srv.Shutdown(ctx)
	}
	s.status = StatusStopped
	return nil
}
func (s *HTTPServerAdapter) StartWithEngine(ctx context.Context, eng server.EngineAPI, addr string) error {
	s.eng = eng
	s.addr = addr
	s.srv = server.New(s.cfg, s.eng, s.v.Get(), "", s.h5FS)
	s.status = StatusReady
	return nil
}
func (s *HTTPServerAdapter) Serve(addr string) {
	if s.srv != nil {
		go func() {
			log.Printf("HTTP 服务启动 %s", addr)
			if err := s.srv.Start(addr); err != nil {
				os.Stderr.WriteString("http server: " + err.Error() + "\n")
			}
		}()
	}
}
func (s *HTTPServerAdapter) Get() *server.Server { return s.srv }
