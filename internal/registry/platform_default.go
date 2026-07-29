//go:build !android

package registry

// RegisterAll 注册桌面版全量服务。
func RegisterAll(r *Registry, p *Params) {
	cfg := p.Cfg

	// ── Critical ──
	market := &MarketAPIAdapter{}
	r.Register(market)

	matcher := &EventMatcherAdapter{}
	r.Register(matcher)

	// ── Business ──
	tushare := &TushareClientAdapter{token: p.TsToken}
	r.Register(tushare)

	ths := &THSClientAdapter{}
	r.Register(ths)

	dc := &DataCoordinatorAdapter{market: market, tushare: tushare, ths: ths}
	r.Register(dc)

	ss := &SectorScannerAdapter{market: market, matcher: matcher}
	r.Register(ss)

	r.Register(&RPSManagerAdapter{})

	filterEng := &FilterAdapter{cfg: cfg, dc: dc}
	r.Register(filterEng)

	r.Register(&RiskAdapter{cfg: cfg})
	r.Register(&PositionAdapter{cfg: cfg})
	r.Register(&ValidatorAdapter{cfg: cfg})

	watchStocks := cfg.Get().Theme.WatchList
	if len(watchStocks) == 0 {
		watchStocks = []string{"000001", "600519", "000858", "002594"}
	}
	r.Register(&FetcherAdapter{dc: dc, stocks: watchStocks})

	r.Register(&DragonAdapter{cfg: cfg})
	r.Register(&DoubleBumpAdapter{cfg: cfg})
	r.Register(&NShapeAdapter{cfg: cfg, matcher: matcher})
	r.Register(&DragonReturnAdapter{cfg: cfg})

	r.Register(&CalendarAdapter{cfg: cfg})
	r.Register(&KoreaAdapter{cfg: cfg})
	r.Register(&LLMAdapter{key: p.LlmKey, model: cfg.Get().LLM.Model})

	r.Register(&NotifierAdapter{})
	r.Register(&HistoryAdapter{dir: "."})
	r.Register(&HoldingsAdapter{dir: "."})
	r.Register(&WatchlistAdapter{dir: "."})
}
