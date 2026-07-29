// Package server 提供 HTTP REST API 服务，暴露策略信号、状态、持仓等接口供 H5 前端调用。
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"quant-trading/internal/auth"
	"quant-trading/internal/config"
	"quant-trading/internal/data"
	"quant-trading/internal/validate"
)

// ── Types ──

// SignalView 对外展示的信号视图，包含评分、策略、可开仓标记等字段。
type SignalView struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Strategy    string  `json:"strategy"`
	Level       string  `json:"level"`
	TotalScore  float64 `json:"total_score"`
	Sector      string  `json:"sector"`
	D1          float64 `json:"d1"`
	D2          float64 `json:"d2"`
	D3          float64 `json:"d3"`
	D4          float64 `json:"d4"`
	D1Desc      string  `json:"d1_desc"`
	D2Desc      string  `json:"d2_desc"`
	D3Desc      string  `json:"d3_desc"`
	D4Desc      string  `json:"d4_desc"`
	F1          float64 `json:"f1"`
	F2          float64 `json:"f2"`
	F3          float64 `json:"f3"`
	F4          float64 `json:"f4"`
	CanOpen     bool    `json:"can_open"`
	Priority    int     `json:"priority"`
	RemindLevel string  `json:"remind_level"`
	LeftSignal  bool    `json:"left_signal"`
	NPhase      int     `json:"n_phase"`
	Matched     string  `json:"matched"`
	Price       float64 `json:"price"`
	ChangePct   float64 `json:"change_pct"` // 信号列表展示用：个股当前涨跌幅，前端据此标红/绿
	Action      string  `json:"action"`
	// 四大策略总分（从 StockEval 合并）
	NScore      float64 `json:"n_score"`
	DragonScore float64 `json:"dragon_score"`
	DBScore     float64 `json:"db_score"`
	DRScore     float64 `json:"dr_score"`
	MScore      float64 `json:"m_score"`
	NLevel      string  `json:"n_level"`
	DragonLevel string  `json:"dragon_level"`
	DBLevel     string  `json:"db_level"`
	DRLevel     string  `json:"dr_level"`
	MLevel      string  `json:"m_level"`
	NPass       bool    `json:"n_pass"`
	DragonPass  bool    `json:"dragon_pass"`
	DBPass      bool    `json:"db_pass"`
	DRPass      bool    `json:"dr_pass"`
	MPass       bool    `json:"m_pass"`
}

// StatusView 系统运行状态视图，包括运行时长、数据源、信号计数等。
type StatusView struct {
	Running       bool       `json:"running"`
	Uptime        string     `json:"uptime"`
	Account       string     `json:"account"`
	LastData      string     `json:"last_data"`
	DataSource    string     `json:"data_source"`
	LastScan      string     `json:"last_scan"`
	StocksWatched int        `json:"stocks_watched"`
	SignalCount   int        `json:"signal_count"`
	InTradeTime   bool       `json:"in_trade_time"`
	Session       int        `json:"session"`
	SessionName   string     `json:"session_name"`
	ScanStats     *ScanStats `json:"scan_stats"`
}

// Alert 告警消息结构，包含时间、股票代码、标题、级别等信息。
type Alert struct {
	Time  string  `json:"time"`
	Code  string  `json:"code"`
	Name  string  `json:"name"`
	Title string  `json:"title"`
	Body  string  `json:"body"`
	Level string  `json:"level"`
	Score float64 `json:"score"`
	D4    float64 `json:"d4"`
}

// HoldingsData 持仓数据，包含可用余额和持仓列表。
type HoldingsData struct {
	UpdatedAt        string        `json:"updated_at,omitempty"`
	AvailableBalance float64       `json:"available_balance"`
	Holdings         []HoldingItem `json:"holdings"`
}

// HoldingItem 单只持仓，含代码、名称、数量、成本价、现价、盈亏比、当日涨跌幅。
type HoldingItem struct {
	Code          string  `json:"code"`
	Name          string  `json:"name"`
	Quantity      int     `json:"quantity"`
	CostPrice     float64 `json:"cost_price"`
	CurPrice      float64 `json:"cur_price"`
	PnlPct        float64 `json:"pnl_pct"`
	ChangePct     float64 `json:"change_pct"`
	TakeProfitPct float64 `json:"take_profit_pct"`
	StopLossPct   float64 `json:"stop_loss_pct"`
}

// StockLookupItem 股票快速查询结果。
type StockLookupItem struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	ChangePct float64 `json:"change_pct"`
	PE        float64 `json:"pe"`
}

// ScanStats 扫描统计，记录总股票数、有数据、有错误、原始/最终信号数、
// 以及当前处理的新闻条数、热点板块数、板块内个股数。
type ScanStats struct {
	TotalStocks      int `json:"total_stocks"`
	WithData         int `json:"with_data"`
	WithError        int `json:"with_error"`
	RawSignals       int `json:"raw_signals"`
	FinalSignals     int `json:"final_signals"`
	NewsCount        int `json:"news_count"`
	HotSectorCount   int `json:"hot_sector_count"`
	SectorStockCount int `json:"sector_stock_count"`
}

// SectorHotView 热点板块视图，包含板块评分、涨幅、涨停家数、净流入等。
type SectorHotView struct {
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Reason       string  `json:"reason"`        // 卡片标签：LLM总结优先，其次新闻标题/量价标签
	ReasonDetail string  `json:"reason_detail"` // 点击详情：新闻源头（来源+时间+标题+LLM分析）
	Score        float64 `json:"score"`
	D1           float64 `json:"d1"`
	ChangePct    float64 `json:"change_pct"`
	Amount       float64 `json:"amount"`
	LimitupCnt   float64 `json:"limitup_cnt"`
	NetInflow    float64 `json:"net_inflow"`
}

// AgentState Agent 状态（供 agent 包返回前端）。
type AgentState struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// ── EngineAPI ──

// SnapshotStock 快照中的个股行情。
type SnapshotStock struct {
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Price        float64 `json:"price"`
	ChangePct    float64 `json:"change_pct"`
	Volume       float64 `json:"volume"`
	Amount       float64 `json:"amount"`
	MScore       float64 `json:"m_score"`
	Sector       string  `json:"sector,omitempty"`        // 来源板块（由哪个热点板块带入）
	SectorReason string  `json:"sector_reason,omitempty"` // 来源板块的上榜原因
}

// StockEval 个股全维度评分（含各策略评分明细）。
type StockEval struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Sector    string  `json:"sector"`
	Price     float64 `json:"price"`
	ChangePct float64 `json:"change_pct"`
	// N形评分
	NScore float64 `json:"n_score"`
	ND1    float64 `json:"n_d1"`
	ND2    float64 `json:"n_d2"`
	ND3    float64 `json:"n_d3"`
	ND4    float64 `json:"n_d4"`
	NLevel string  `json:"n_level"`
	NPass  bool    `json:"n_pass"`
	// 龙头评分
	DragonScore float64 `json:"dragon_score"`
	DragonF1    float64 `json:"dragon_f1"`
	DragonF2    float64 `json:"dragon_f2"`
	DragonF3    float64 `json:"dragon_f3"`
	DragonF4    float64 `json:"dragon_f4"`
	DragonLevel string  `json:"dragon_level"`
	DragonPass  bool    `json:"dragon_pass"`
	// 双凸评分
	DBScore float64 `json:"db_score"`
	DBLevel string  `json:"db_level"`
	DBPass  bool    `json:"db_pass"`
	// 龙回头评分
	DRScore float64 `json:"dr_score"`
	DRLevel string  `json:"dr_level"`
	DRPass  bool    `json:"dr_pass"`
	// 通用动量评分
	MScore float64 `json:"m_score"`
	MLevel string  `json:"m_level"`
	MPass  bool    `json:"m_pass"`
}

// EngineAPI 引擎接口，Server 通过该接口与后端引擎交互获取数据。
type EngineAPI interface {
	GetSignals() []SignalView
	GetStatus() StatusView
	GetAlerts() []Alert
	GetAlertLog() []Alert
	GetHoldings() HoldingsData
	SetHoldings(HoldingsData) error
	GetNews() []data.NewsItem
	GetAllNews() []data.NewsItem
	LookupStock(code string) *StockLookupItem
	GetWatchlistEnriched(codes []string) []StockLookupItem
	GetNStates() map[string]int
	GetHotSectors() []SectorHotView
	SubscribeEvents(ctx context.Context) chan string
	HandleAction(code, action string) error
	GetSnapshotStocks() []SnapshotStock
	GetHotSnapshotStocks() []SnapshotStock
	GetStockEvals() []StockEval
	GetIPOCalendar() []data.IPOEvent
	StartServices()
	WatchlistAddStock(code string) error
}

// ── Alert Store ──

var (
	alertStore []Alert
	alertMu    sync.Mutex
)

// AddAlertToStore 将告警加入内存存储，最多保留 500 条。
func AddAlertToStore(a Alert) {
	alertMu.Lock()
	defer alertMu.Unlock()
	alertStore = append(alertStore, a)
	if len(alertStore) > 500 {
		alertStore = alertStore[len(alertStore)-500:]
	}
}

// ClearAlertStore 清空告警存储（每天开盘时由引擎调用）。
func ClearAlertStore() {
	alertMu.Lock()
	alertStore = nil
	alertMu.Unlock()
}

// ── Server ──

// Server HTTP API 服务，注册所有 REST 端点并提供认证、CORS 等中间件。
type Server struct {
	engine        EngineAPI
	cfg           *config.Manager
	validator     *validate.Engine
	authenticator *auth.Authenticator
	h5FS          http.FileSystem
	watchlistMgr  *data.WatchlistManager
	httpSrv       *http.Server
	logDir        string
}

// New 创建 HTTP 服务实例。
func New(cfg *config.Manager, eng EngineAPI, v *validate.Engine, secret string, h5FS http.FileSystem) *Server {
	return &Server{
		engine:        eng,
		cfg:           cfg,
		validator:     v,
		authenticator: auth.New(secret),
		h5FS:          h5FS,
		logDir:        ".",
	}
}

// NewAuthServer 创建轻量级登录鉴权服务（不含完整引擎）。
func NewAuthServer(cfg *config.Manager) *Server {
	return &Server{
		engine:        nil,
		cfg:           cfg,
		validator:     nil,
		authenticator: auth.New(""),
		h5FS:          nil,
		logDir:        ".",
	}
}

// SetWatchlistManager 注入自选股管理器。
func (s *Server) SetWatchlistManager(wm *data.WatchlistManager) {
	s.watchlistMgr = wm
}

// Start 启动 HTTP 服务，注册所有路由端点后监听 addr。
// 路由包括：登录、状态、信号、告警、新闻、自选、持仓、股票查询、日志、消息、N形状态、热门板块。
func (s *Server) Start(addr string) error {
	s.httpSrv = &http.Server{
		Addr:    addr,
		Handler: s.BuildHandler(),
	}
	log.Printf("HTTP 服务启动 %s", addr)
	return s.httpSrv.ListenAndServe()
}

// Engine 返回当前引擎实例。
func (s *Server) Engine() EngineAPI { return s.engine }

// SetEngine 注入引擎（agent 模式热替换用）。
func (s *Server) SetEngine(eng EngineAPI) {
	s.engine = eng
}

// SetValidator 注入验证器（agent 模式热替换用）。
func (s *Server) SetValidator(v *validate.Engine) {
	s.validator = v
}

// BuildHandler 构建完整的 http.Handler（含所有引擎路由），供 Start 和 agent 模式共用。
func (s *Server) BuildHandler() http.Handler {
	mux := http.NewServeMux()

	if s.h5FS != nil {
		mux.Handle("/", http.FileServer(s.h5FS))
	}

	mux.HandleFunc("/api/auth/login", s.handleLogin)
	mux.HandleFunc("/api/auth/genaccount", s.handleGenAccount)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/loading", s.handleLoadingPage)

	if s.engine != nil {
		mux.HandleFunc("/api/status", s.handleStatus)
		mux.HandleFunc("/api/signals", s.authMiddleware(s.handleSignals))
		mux.HandleFunc("/api/alerts", s.handleAlerts)
		mux.HandleFunc("/api/alerts/log", s.handleAlertLog)
		mux.HandleFunc("/api/news", s.handleNews)
		mux.HandleFunc("/api/watchlist", s.authMiddleware(s.handleWatchlist))
		mux.HandleFunc("/api/holdings", s.authMiddleware(s.handleHoldings))
		mux.HandleFunc("/api/stock/lookup", s.authMiddleware(s.handleStockLookup))
		mux.HandleFunc("/api/watchlist/enriched", s.authMiddleware(s.handleWatchlistEnriched))
		mux.HandleFunc("/api/log", s.handleLog)
		mux.HandleFunc("/api/messages", s.authMiddleware(s.handleMessages))
		mux.HandleFunc("/api/nstates", s.handleNStates)
		mux.HandleFunc("/api/sector/hot", s.handleSectorHot)
		mux.HandleFunc("/api/events", s.handleEvents)
		mux.HandleFunc("/api/action", s.authMiddleware(s.handleAction))
		mux.HandleFunc("/api/snapshot", s.authMiddleware(s.handleSnapshot))
		mux.HandleFunc("/api/snapshot/hot", s.authMiddleware(s.handleHotSnapshot))
		mux.HandleFunc("/api/evaluations", s.authMiddleware(s.handleEvaluations))
		mux.HandleFunc("/api/ipo/calendar", s.handleIPOCalendar)
	}

	return corsMiddleware(mux)
}

// Shutdown 优雅关闭 HTTP 服务。
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

// ── CORS ──

// corsMiddleware 处理跨域请求，允许所有来源的 GET/POST/DELETE/OPTIONS 请求。
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── Auth Middleware ──

// authMiddleware JWT 认证中间件，验证 Authorization Header 中的 Bearer 令牌，
// 解析后将用户名注入请求上下文。
func (s *Server) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, `{"error":"未授权"}`, http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == authHeader {
			http.Error(w, `{"error":"未授权"}`, http.StatusUnauthorized)
			return
		}
		claims, err := s.authenticator.VerifyToken(token)
		if err != nil {
			http.Error(w, `{"error":"令牌无效或已过期"}`, http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), "username", claims.Username)
		next(w, r.WithContext(ctx))
	}
}

// ── Handlers ──

// handleLogin POST /api/auth/login — 账号密码登录，返回 JWT 令牌。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"请求格式错误"}`, http.StatusBadRequest)
		return
	}

	accounts := s.cfg.Get().Accounts
	valid := false
	expired := false
	for _, acct := range accounts {
		if acct.Username == req.Username {
			hash := sha256.Sum256([]byte(req.Password))
			expected := hex.EncodeToString(hash[:])
			if acct.Password == expected {
				if acct.IsExpired() {
					expired = true
					break
				}
				valid = true
				break
			}
		}
	}
	if expired {
		http.Error(w, `{"error":"账号已过期，请联系管理员"}`, http.StatusForbidden)
		return
	}
	if !valid {
		http.Error(w, `{"error":"用户名或密码错误"}`, http.StatusUnauthorized)
		return
	}

	token, err := s.authenticator.GenerateToken(req.Username)
	if err != nil {
		http.Error(w, `{"error":"生成令牌失败"}`, http.StatusInternalServerError)
		return
	}
	claims, _ := s.authenticator.VerifyToken(token)
	expiresAt := int64(0)
	if claims != nil {
		expiresAt = claims.ExpiresAt
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"token":      token,
		"account":    req.Username,
		"expires_at": expiresAt,
	})
}

// handleGenAccount POST /api/auth/genaccount — 生成随机账号密码并加入配置，返回明文。
func (s *Server) handleGenAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	adj := []string{"Fast", "Sharp", "Bold", "Cool", "Top", "Big", "Hot", "Red", "Blue", "Gold", "Dark", "Safe", "Real", "High", "Low"}
	noun := []string{"Quant", "Trade", "Bull", "Bear", "Wave", "Edge", "Alpha", "Beta", "Risk", "Fund", "Vest", "Pulse", "Spark", "Storm", "Maker"}
	chars := "abcdefghkmnpqrstuvwxyz23456789"

	randStr := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			v, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
			b[i] = chars[v.Int64()]
		}
		return string(b)
	}

	username := adj[randInt(len(adj))] + noun[randInt(len(noun))] + randStr(3)
	password := randStr(4) + "@" + randStr(4)
	hash := sha256.Sum256([]byte(password))
	hashHex := hex.EncodeToString(hash[:])

	// 追加到运行配置
	cfg := s.cfg.Get()
	cfg.Accounts = append(cfg.Accounts, config.AccountConfig{
		Username: username,
		Password: hashHex,
	})

	// 追加到 rules.json 文件（持久化）
	if data, err := os.ReadFile(s.cfg.Path()); err == nil {
		var raw interface{}
		if json.Unmarshal(data, &raw) == nil {
			if m, ok := raw.(map[string]interface{}); ok {
				accts, _ := m["accounts"].([]interface{})
				accts = append(accts, map[string]interface{}{
					"username": username,
					"password": hashHex,
				})
				m["accounts"] = accts
				if output, err := json.MarshalIndent(raw, "", "  "); err == nil {
					os.WriteFile(s.cfg.Path(), output, 0644)
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"username": username,
		"password": password,
	})
}

func randInt(n int) int {
	v, _ := rand.Int(rand.Reader, big.NewInt(int64(n)))
	return int(v.Int64())
}

// handleLoadingPage GET /loading — 登录后加载进度页，轮询 status 直到数据就绪后跳转前端。
func (s *Server) handleLoadingPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,user-scalable=no">
<title>加载中…</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{background:#0f0f23;color:#e0e0e0;font-family:-apple-system,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh}
.box{text-align:center;width:85%;max-width:360px}
.spinner{width:40px;height:40px;border:3px solid #2a2a3e;border-top-color:#FF4D4F;border-radius:50%;animation:spin .8s linear infinite;margin:0 auto 24px}
@keyframes spin{to{transform:rotate(360deg)}}
h2{font-size:18px;color:#e0e0e0;margin-bottom:8px}
#status{font-size:13px;color:#888;min-height:20px}
#progress{width:100%;height:4px;background:#2a2a3e;border-radius:2px;margin-top:20px;overflow:hidden}
#bar{height:100%;width:0%;background:#FF4D4F;border-radius:2px;transition:width .5s}
</style>
</head>
<body>
<div class="box">
<div class="spinner"></div>
<h2>正在加载数据</h2>
<div id="status">初始化…</div>
<div id="progress"><div id="bar"></div></div>
</div>
<script>
var token=location.search.match(/[?&]token=([^&]+)/);
token=token?decodeURIComponent(token[1]):'';
var total=0,loaded=0,signalCount=0;
function update(){
  var x=new XMLHttpRequest();
  x.open('GET','/api/status',true);
  x.setRequestHeader('Authorization','Bearer '+token);
  x.timeout=5000;
  x.onload=function(){
    if(x.status===200){
      try{
        var d=JSON.parse(x.responseText);
        var bar=document.getElementById('bar');
        var st=document.getElementById('status');
        if(d.scan_stats&&d.scan_stats.total_stocks>0){
          total=d.scan_stats.total_stocks;
          loaded=d.scan_stats.with_data||0;
          signalCount=d.signal_count||0;
          var pct=total>0?Math.min(Math.floor(loaded/total*80)+20,95):0;
          bar.style.width=pct+'%';
          st.textContent='扫描 '+(d.last_scan||'…')+' | 信号 '+signalCount;
          if(loaded>=total){
            bar.style.width='100%';
            st.textContent='数据加载完成 ('+total+'只)';
            setTimeout(function(){window.location.replace('/?token='+encodeURIComponent(token));},500);
            return;
          }
        }else if(d.stocks_watched>0){
          total=d.stocks_watched;
          loaded=d.stocks_watched;
          var pct=Math.min(loaded/total*50,50);
          bar.style.width=pct+'%';
          st.textContent='已获取行情 '+(d.last_data||'');
        }else{
          bar.style.width='5%';
          st.textContent='等待数据…';
        }
      }catch(e){}
    }
    setTimeout(update,2000);
  };
  x.onerror=function(){setTimeout(update,2000);};
  x.ontimeout=function(){setTimeout(update,2000);};
  x.send();
}
update();
</script>
</body>
</html>`))
}

// handleHealth GET /api/health — 健康检查端点，确认服务就绪。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok","engine":true}`))
}

// handleStatus GET /api/status — 返回引擎运行状态。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.engine.GetStatus())
}

// handleSignals GET /api/signals — 返回当前所有策略信号列表。
func (s *Server) handleSignals(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.engine.GetSignals())
}

// handleAlerts GET/DELETE /api/alerts — 获取告警列表或清空告警。
func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		alerts := s.engine.GetAlerts()
		alertMu.Lock()
		for _, a := range alertStore {
			if a.Code != "CAL" && !strings.HasPrefix(a.Level, "日历") {
				alerts = append(alerts, a)
			}
		}
		alertMu.Unlock()
		json.NewEncoder(w).Encode(alerts)
	case http.MethodDelete:
		alertMu.Lock()
		alertStore = nil
		alertMu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// handleAlertLog GET /api/alerts/log — 返回持久化的告警日志。
func (s *Server) handleAlertLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.engine.GetAlertLog())
}

// handleNews GET /api/news — 返回最新财经新闻。
// 支持 ?all=true 返回未经 D1 过滤的全部新闻。
func (s *Server) handleNews(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	all := r.URL.Query().Get("all") == "true"
	if all {
		json.NewEncoder(w).Encode(s.engine.GetAllNews())
		return
	}
	json.NewEncoder(w).Encode(s.engine.GetNews())
}

// handleWatchlist GET/POST/DELETE /api/watchlist — 自选股管理（列表、添加、删除）。
func (s *Server) handleWatchlist(w http.ResponseWriter, r *http.Request) {
	if s.watchlistMgr == nil {
		http.Error(w, `{"error":"自选管理未就绪"}`, http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(map[string]interface{}{
			"stocks": s.watchlistMgr.List(),
		})
	case http.MethodPost:
		var req struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"请求格式错误"}`, http.StatusBadRequest)
			return
		}
		if err := s.engine.WatchlistAddStock(req.Code); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	case http.MethodDelete:
		var req struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"请求格式错误"}`, http.StatusBadRequest)
			return
		}
		if err := s.watchlistMgr.Remove(req.Code); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// handleHoldings GET/POST /api/holdings — 获取或更新持仓数据。
func (s *Server) handleHoldings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(s.engine.GetHoldings())
	case http.MethodPost:
		var h HoldingsData
		if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
			http.Error(w, `{"error":"请求格式错误"}`, http.StatusBadRequest)
			return
		}
		if err := s.engine.SetHoldings(h); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// handleStockLookup GET /api/stock/lookup?code=XXXXXX — 按代码查询股票基本信息。
func (s *Server) handleStockLookup(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, `{"error":"缺少code参数"}`, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	item := s.engine.LookupStock(code)
	if item == nil {
		json.NewEncoder(w).Encode(map[string]string{"error": "未找到"})
		return
	}
	json.NewEncoder(w).Encode(item)
}

// handleWatchlistEnriched GET/POST /api/watchlist/enriched — 获取自选股的增强信息（含实时价格等）。
func (s *Server) handleWatchlistEnriched(w http.ResponseWriter, r *http.Request) {
	var codes []string
	if r.Method == "GET" {
		if s.watchlistMgr != nil {
			codes = s.watchlistMgr.List()
		}
	} else {
		var req struct {
			Codes []string `json:"codes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"请求格式错误"}`, http.StatusBadRequest)
			return
		}
		codes = req.Codes
		if len(codes) == 0 && s.watchlistMgr != nil {
			codes = s.watchlistMgr.List()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"stocks": s.engine.GetWatchlistEnriched(codes),
	})
}

// handleLog GET /api/log — 返回 signals.log 原始日志文件内容。
func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	f, err := os.Open(filepath.Join(s.logDir, "signals.log"))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]string{})
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.Copy(w, f)
}

// handleMessages GET/POST /api/messages — 用户留言获取/提交（当前为占位实现）。
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode([]string{})
	case http.MethodPost:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, `{"error":"读取失败"}`, http.StatusBadRequest)
			return
		}
		log.Printf("用户消息: %s", string(body))
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	default:
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
	}
}

// handleNStates GET /api/nstates — 返回 N 形策略各股票的状态映射。
func (s *Server) handleNStates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.engine.GetNStates())
}

// handleSectorHot GET /api/sector/hot — 返回热门板块排行榜。
func (s *Server) handleSectorHot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.engine.GetHotSectors())
}

// handleEvents GET /api/events — SSE 实时事件推送端点。
// 前端使用 new EventSource('/api/events?token=...') 连接。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		token = r.Header.Get("Authorization")
	}
	if token == "" {
		http.Error(w, `{"error":"token required"}`, http.StatusUnauthorized)
		return
	}
	if _, err := s.authenticator.VerifyToken(token); err != nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming not supported"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ctx := r.Context()
	ch := s.engine.SubscribeEvents(ctx)
	defer func() {
		log.Printf("sse client disconnected")
	}()

	for {
		select {
		case data, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}

// handleSnapshot GET /api/snapshot — 返回当前快照中所有股票行情。
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.engine.GetSnapshotStocks())
}

// handleHotSnapshot GET /api/snapshot/hot — 返回热点板块个股行情。
func (s *Server) handleHotSnapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.engine.GetHotSnapshotStocks())
}

// handleEvaluations GET /api/evaluations — 返回所有个股的全维度评分。
func (s *Server) handleEvaluations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.engine.GetStockEvals())
}

// handleAction POST /api/action — 用户交易确认（buy/sell/ignore）。
func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Code   string `json:"code"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"请求格式错误"}`, http.StatusBadRequest)
		return
	}
	if req.Code == "" || req.Action == "" {
		http.Error(w, `{"error":"缺少 code 或 action"}`, http.StatusBadRequest)
		return
	}
	if err := s.engine.HandleAction(req.Code, req.Action); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleIPOCalendar GET /api/ipo/calendar — 新股日历数据。
func (s *Server) handleIPOCalendar(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.engine.GetIPOCalendar())
}
