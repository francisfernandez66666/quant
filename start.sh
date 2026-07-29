#!/bin/bash
# start.sh — 启动量仔期货后端 + 桌面前端
# 用法: ./start.sh [--port 8080]
set -e

DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR"

PORT=8080
for arg in "$@"; do
  case "$arg" in
    --port=*) PORT="${arg#*=}" ;;
  esac
done

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
info()  { echo -e "${CYAN}==>${NC} $1"; }
ok()    { echo -e "${GREEN}✔${NC} $1"; }
err()   { echo -e "${RED}✘${NC} $1"; }

# ── 1. 清理旧进程 ──
info "清理端口 :$PORT"
OLD_PIDS=$(lsof -ti tcp:"$PORT" 2>/dev/null || true)
if [ -n "$OLD_PIDS" ]; then
  info "关闭占用 :$PORT 的进程 (PID $OLD_PIDS)"
  kill $OLD_PIDS 2>/dev/null || true
  sleep 1
  STILL=$(lsof -ti tcp:"$PORT" 2>/dev/null || true)
  if [ -n "$STILL" ]; then
    kill -9 $STILL 2>/dev/null || true
    sleep 1
  fi
fi

# ── 2. Token 检测 ──
if [ -z "$TUSHARE_TOKEN" ] && [ ! -f config/secrets.json ]; then
  echo -e "${YELLOW}⚠${NC} 未设置 TUSHARE_TOKEN，行情数据可能受限"
fi
if [ -z "$LLM_API_KEY" ] && [ ! -f config/secrets.json ]; then
  echo -e "${YELLOW}⚠${NC} 未设置 LLM_API_KEY，LLM 功能不可用"
fi
if { [ -z "$JQ_MOBILE" ] || [ -z "$JQ_PASSWORD" ]; } && [ ! -f config/secrets.json ]; then
  echo -e "${YELLOW}⚠${NC} 未设置 JQ_MOBILE/JQ_PASSWORD，板块数据无法降级到聚宽"
fi

# ── 3. 编译+测试 ──
info "gofmt..."
gofmt -w .
info "编译后端..."
go build -o quant ./cmd/quant/
ok "编译完成"

info "go vet..."
go vet ./...

info "单元测试..."
if go test ./... -count=1 -timeout 60s 2>&1 | grep -q "^FAIL"; then
  err "测试未通过，请检查"
  go test ./... -count=1 -timeout 60s 2>&1 | grep "FAIL"
  exit 1
fi
ok "全部测试通过"

# ── 4. 前端 ──
if [ ! -d desktop/node_modules ]; then
  info "安装桌面依赖..."
  cd desktop && npm install && cd ..
fi
info "编译桌面前端..."
cd desktop && npm run build && cd ..
ok "前端编译完成"

# ── 5. 启动 ──
echo ""
info "启动服务 (端口 $PORT)"
echo -e "  前端: ${CYAN}http://localhost:$PORT${NC}"
echo -e "  API:  ${CYAN}http://localhost:$PORT/api/health${NC}"
echo ""

./quant --listen ":$PORT" --h5-dir desktop/dist &
PID=$!

for i in $(seq 1 30); do
  if curl -s http://localhost:$PORT/api/health 2>/dev/null | grep -q '"engine":true'; then
    ok "后端就绪 ($i s)"
    open http://localhost:$PORT
    echo ""
    info "按 Ctrl+C 停止服务"
    wait $PID
    exit 0
  fi
  sleep 1
done

err "启动超时"
kill $PID 2>/dev/null || true
exit 1
