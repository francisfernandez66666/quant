#!/bin/bash
# start-agent.sh — 量仔期货 一键启动（Agent 渐进式版本）
# 启动流程：编译 → HTTP 立即可用（<1ms）→ 后台全量预热 → 自动升级
# 用法：./start-agent.sh [--dev] [--port 8080]
set -e

DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR"

PORT=8080
DEV_MODE=false
for arg in "$@"; do
  case "$arg" in
    --dev) DEV_MODE=true ;;
    --port=*) PORT="${arg#*=}" ;;
  esac
done

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; NC='\033[0m'

info()  { echo -e "${CYAN}==>${NC} $1"; }
ok()    { echo -e "${GREEN}✔${NC} $1"; }
warn()  { echo -e "${YELLOW}⚠${NC} $1"; }
err()   { echo -e "${RED}✘${NC} $1"; }

# ── 1. 清理端口（不管是 quant 还是 quant_agent，一律杀掉）──
info "检查端口 :$PORT"
OLD_PIDS=$(lsof -ti tcp:"$PORT" 2>/dev/null || true)
if [ -n "$OLD_PIDS" ]; then
  info "关闭占用 :$PORT 的进程 (PID $OLD_PIDS)"
  kill $OLD_PIDS 2>/dev/null || true
  sleep 1
  # 确保彻底释放
  STILL=$(lsof -ti tcp:"$PORT" 2>/dev/null || true)
  if [ -n "$STILL" ]; then
    info "强制终止…"
    kill -9 $STILL 2>/dev/null || true
    sleep 1
  fi
fi

# ── 2. 编译+验证 ──
info "gofmt..."
gofmt -w .
info "编译 cmd/quant_agent"
go build -o quant_agent ./cmd/quant_agent/
ok "编译完成"

info "go vet..."
go vet ./...

info "单元测试..."
if go test ./... -count=1 -timeout 120s 2>&1 | grep -q "^FAIL"; then
  err "测试未通过，请检查"
  go test ./... -count=1 -timeout 120s 2>&1 | grep "FAIL"
  exit 1
fi
ok "全部测试通过"

# ── 3. Token 检测 ──
if [ -z "$TUSHARE_TOKEN" ] && [ ! -f config/secrets.json ]; then
  warn "未设置 TUSHARE_TOKEN 且无 config/secrets.json，行情受限"
fi
if [ -z "$LLM_API_KEY" ] && [ ! -f config/secrets.json ]; then
  warn "未设置 LLM_API_KEY 且无 config/secrets.json，LLM 不可用"
fi
if [ -z "$JQ_MOBILE" ] || [ -z "$JQ_PASSWORD" ] && [ ! -f config/secrets.json ]; then
  warn "未设置 JQ_MOBILE/JQ_PASSWORD 且无 config/secrets.json，板块数据无法降级到聚宽"
fi

# ── 4. 编译前端 ──
if [ -d desktop ]; then
  if [ ! -d desktop/node_modules ]; then
    info "安装桌面依赖..."
    cd desktop && npm install && cd ..
  fi
  info "编译桌面前端..."
  cd desktop && npm run build && cd ..
  ok "前端编译完成"
fi

# ── 5. 检测 H5 前端 ──
H5_ARGS=""
if [ -d desktop/dist ]; then
  H5_ARGS="--h5-dir desktop/dist"
  ok "H5 前端: desktop/dist"
elif [ -d web/dist ]; then
  H5_ARGS="--h5-dir web/dist"
  ok "H5 前端: web/dist"
else
  warn "未找到前端目录，使用嵌入 HTML（功能受限）"
fi

# ── 6. 启动 ──
info "启动服务 (端口 $PORT)"
./quant_agent --listen ":$PORT" $H5_ARGS &
AGENT_PID=$!

echo ""
echo -e "  ${CYAN}加载进度:${NC} http://localhost:$PORT/loading"
echo -e "  ${CYAN}API 健康:${NC} http://localhost:$PORT/api/health"
echo -e "  ${CYAN}Agent 状态:${NC} http://localhost:$PORT/api/agent/status"
echo ""

# ── 7. 等待端口就绪 ──
for i in $(seq 1 10); do
  if lsof -ti tcp:"$PORT" 2>/dev/null | grep -q .; then
    break
  fi
  # 检查进程是否还活着
  if ! kill -0 $AGENT_PID 2>/dev/null; then
    err "进程异常退出，请检查日志"
    exit 1
  fi
  sleep 0.5
done

# ── 8. 轮询等待引擎就绪 ──
info "等待引擎就绪…"
for i in $(seq 1 300); do
  health=$(curl -s http://localhost:$PORT/api/health 2>/dev/null || echo "")
  if echo "$health" | grep -q '"engine":true'; then
    echo ""
    ok "全量服务就绪（${i}s）"
    agent_status=$(curl -s http://localhost:$PORT/api/agent/status 2>/dev/null || echo "[]")
    ready=$(echo "$agent_status" | grep -o '"status":"ready"' | wc -l)
    total=$(echo "$agent_status" | grep -o '"name":' | wc -l)
    echo -e "  ${GREEN}Agent: $ready/$total 就绪${NC}"

    if [ "$DEV_MODE" = false ]; then
      open "http://localhost:$PORT"
      ok "浏览器已打开"
    fi
    echo ""
    info "按 Ctrl+C 停止服务"
    wait $AGENT_PID
    exit 0
  fi

  # 实时进度
  if [ $((i % 5)) -eq 0 ]; then
    agent_status=$(curl -s http://localhost:$PORT/api/agent/status 2>/dev/null || echo "[]")
    ready=$(echo "$agent_status" | grep -o '"status":"ready"' | wc -l)
    total=$(echo "$agent_status" | grep -o '"name":' | wc -l)
    if [ "$total" -gt 0 ]; then
      echo -ne "\r  ${YELLOW}加载中… ($ready/$total)${NC}   "
    else
      echo -ne "\r  ${YELLOW}加载中…${NC}   "
    fi
  fi
  sleep 1
done

echo ""
err "启动超时（>60s）"
kill $AGENT_PID 2>/dev/null || true
exit 1
