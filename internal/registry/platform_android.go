//go:build android

package registry

// RegisterAll 注册安卓版裁剪服务。
// 只注册 Critical + Business，排除 Edge（通知/历史/持仓/自选）等桌面特有服务。
func RegisterAll(r *Registry, p *Params) {
	cfg := p.Cfg

	// ── Critical（同步串行，<1ms）──
	market := &MarketAPIAdapter{}
	r.Register(market)

	matcher := &EventMatcherAdapter{}
	r.Register(matcher)

	// ── Business（异步并行，worker=4）──
	tushare := &TushareClientAdapter{token: p.TsToken}
	r.Register(tushare)

	ths := &THSClientAdapter{}
	r.Register(ths)

	dc := &DataCoordinatorAdapter{market: market, tushare: tushare, ths: ths}
	r.Register(dc)

	r.Register(&SectorScannerAdapter{market: market, matcher: matcher})
	r.Register(&RPSManagerAdapter{})

	r.Register(&FilterAdapter{cfg: cfg, dc: dc})
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

	// 排除：
	//   Notifier / History / Holdings / Watchlist
	// 这些 Edge 服务在安卓上完全按需懒加载，不注册
}
