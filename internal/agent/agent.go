// Package agent 实现渐进式启动框架。
// 核心思路：HTTP 先启动（<1ms），服务后台并行预热，引擎就绪后热替换 handler。
//
// 启动流程：
//
//	main → agent.NewLauncher(cfg)
//	launcher.StartHTTP(ctx)     ← 立即返回，<1ms
//	launcher.Warmup(ctx)        ← 后台并行初始化所有服务
//	                              → 就绪后自动升级 handler 为全量引擎
package agent

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"quant-trading/internal/config"
	"quant-trading/internal/engine"
	"quant-trading/internal/registry"
	"quant-trading/internal/server"
	"quant-trading/internal/validate"
)

// Params 共享依赖，同 registry.Params。
type Params = registry.Params

type Status int

const (
	StatusPending Status = iota
	StatusStarting
	StatusReady
	StatusFailed
)

func (s Status) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusStarting:
		return "starting"
	case StatusReady:
		return "ready"
	case StatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}

type Agent struct {
	Name   string
	Status Status
	fn     func() error
}

type Manager struct {
	mu     sync.Mutex
	agents []*Agent
}

func NewManager() *Manager { return &Manager{} }

func (m *Manager) Register(name string, fn func() error) *Agent {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := &Agent{Name: name, Status: StatusPending, fn: fn}
	m.agents = append(m.agents, a)
	return a
}

func (m *Manager) Start(ctx context.Context, workers int) {
	t0 := time.Now()
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)

	for _, a := range m.agents {
		a.Status = StatusStarting
		wg.Add(1)
		go func(agent *Agent) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := agent.fn(); err != nil {
				agent.Status = StatusFailed
				log.Printf("agent %q: %v", agent.Name, err)
				return
			}
			agent.Status = StatusReady
		}(a)
	}
	wg.Wait()
	log.Printf("agent: all %d ready (%v)", len(m.agents), time.Since(t0))
}

func (m *Manager) Stats() []server.AgentState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]server.AgentState, 0, len(m.agents))
	for _, a := range m.agents {
		out = append(out, server.AgentState{Name: a.Name, Status: a.Status.String()})
	}
	return out
}

// ── upgradeHandler ──

type upgradeHandler struct {
	mu     sync.RWMutex
	engine http.Handler
	basic  http.Handler
	ready  atomic.Bool
}

func (h *upgradeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.ready.Load() {
		h.engine.ServeHTTP(w, r)
		return
	}
	h.basic.ServeHTTP(w, r)
}

func (h *upgradeHandler) Upgrade(engine http.Handler) {
	h.mu.Lock()
	h.engine = engine
	h.mu.Unlock()
	h.ready.Store(true)
	log.Println("[agent] handler upgraded → 全量引擎")
}

// ── Launcher ──

type Launcher struct {
	Cfg     *config.Manager
	Mgr     *Manager
	Reg     *registry.Registry
	Params  *registry.Params
	H5FS    http.FileSystem
	handler *upgradeHandler
	srv     *http.Server
	eng     *engine.Engine
	apiSrv  *server.Server
}

func NewLauncher(cfg *config.Manager, p *registry.Params) *Launcher {
	return &Launcher{
		Cfg:    cfg,
		Mgr:    NewManager(),
		Reg:    registry.New(),
		Params: p,
		H5FS:   p.H5FS,
	}
}

// StartHTTP 启动 HTTP 服务（<1ms 返回，无需等待任何服务就绪）。
func (l *Launcher) StartHTTP(ctx context.Context, addr string) error {
	basic := http.NewServeMux()
	basic.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if l.handler.ready.Load() {
			w.Write([]byte(`{"status":"ok","engine":true}`))
		} else {
			w.Write([]byte(`{"status":"loading","engine":false}`))
		}
	})
	basic.HandleFunc("/api/agent/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(l.Mgr.Stats())
	})
	basic.HandleFunc("/loading", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(loadingPageHTML)
	})
	basic.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/loading", http.StatusFound)
	})

	l.handler = &upgradeHandler{basic: basic}
	l.srv = &http.Server{Addr: addr, Handler: corsMiddleware(l.handler)}

	go func() {
		log.Printf("[agent] HTTP 启动 %s", addr)
		if err := l.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[agent] HTTP error: %v", err)
		}
	}()
	return nil
}

// Warmup 后台启动所有 agent，就绪后构建引擎并热升级 handler。
// 然后启动扫描主循环（与原始 engine.Run 相同，但不重复创建 HTTP）。
func (l *Launcher) Warmup(ctx context.Context, workers int) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[agent] WARMUP PANIC: %v", r)
		}
	}()

	log.Println("[agent] warmup: starting agents")
	l.Mgr.Start(ctx, workers)
	log.Println("[agent] warmup: agents done, building engine")

	l.eng = engine.NewFromRegistry(l.Cfg, l.Reg)
	log.Println("[agent] warmup: engine built")

	l.eng.Prefetch()
	log.Println("[agent] warmup: prefetch done")

	var validator *validate.Engine
	if svc := l.Reg.Service("validator"); svc != nil {
		if a, ok := svc.(*registry.ValidatorAdapter); ok {
			validator = a.Get()
		}
	}
	l.apiSrv = server.New(l.Cfg, l.eng, validator, "", l.H5FS)
	if wl := l.eng.GetWatchlistMgr(); wl != nil {
		l.apiSrv.SetWatchlistManager(wl)
	}
	fullHandler := l.apiSrv.BuildHandler()

	l.handler.Upgrade(fullHandler)
	log.Println("[agent] 全量服务就绪")

	// 启动扫描主循环（传入空地址，跳过 HTTP 创建）
	go func() {
		if err := l.eng.Run(ctx, ""); err != nil && err != context.Canceled {
			log.Printf("[agent] 扫描循环退出: %v", err)
		}
	}()
}

func (l *Launcher) Shutdown(ctx context.Context) {
	if l.srv != nil {
		l.srv.Shutdown(ctx)
	}
}

// ── loadingPageHTML ──

var loadingPageHTML = []byte(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,user-scalable=no">
<title>启动中…</title>
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
<h2>正在启动</h2>
<div id="status">初始化服务…</div>
<div id="progress"><div id="bar"></div></div>
</div>
<script>
function poll(){
  var x=new XMLHttpRequest();
  x.open('GET','/api/health',true);
  x.timeout=3000;
  x.onload=function(){
    if(x.status===200&&JSON.parse(x.responseText).engine===true){
      window.location.replace('/');
      return;
    }
  };
  x.send();
  var y=new XMLHttpRequest();
  y.open('GET','/api/agent/status',true);
  y.timeout=5000;
  y.onload=function(){
    if(y.status===200){
      try{
        var d=JSON.parse(y.responseText);
        var st=document.getElementById('status');
        var bar=document.getElementById('bar');
        if(d&&d.length>0){
          var ready=d.filter(function(a){return a.status==='ready'}).length;
          var total=d.length;
          var pct=Math.min(Math.floor(ready/total*100),99);
          bar.style.width=pct+'%';
          st.textContent='服务 ('+ready+'/'+total+') 就绪';
        }
      }catch(e){}
    }
  };
  y.send();
}
setInterval(poll,1000);
poll();
</script>
</body>
</html>`)

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
