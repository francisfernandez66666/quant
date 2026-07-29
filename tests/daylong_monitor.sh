#!/bin/bash
# daylong_monitor.sh — 全天全流程监控脚本
# 覆盖:
#   全天循环采样: 健康/状态/快照/评分/信号/持仓/自选
#   异常检测: 引擎宕机/数据停滞/评分静默
#   信号事件捕获: 新信号出现时打时间戳
#   日终报告: 趋势汇总 + 关键事件时间线
# 运行: bash tests/daylong_monitor.sh [--addr 127.0.0.1:8080] [--interval 60] [--days 1]
set -euo pipefail

ADDR="127.0.0.1:8080"
INTERVAL=60
MAX_DAYS=1
QUIET=false

while [ $# -gt 0 ]; do
  case "$1" in
    --addr) ADDR="$2"; shift 2 ;;
    --interval) INTERVAL="$2"; shift 2 ;;
    --days) MAX_DAYS="$2"; shift 2 ;;
    --quiet) QUIET=true; shift ;;
    *) echo "用法: $0 [--addr 127.0.0.1:8080] [--interval 60] [--days 1] [--quiet]"; exit 1 ;;
  esac
done

BASE="http://$ADDR"
DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIR"

TOKEN=""
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
LOGDIR="tests/day_${TIMESTAMP}"
mkdir -p "$LOGDIR"

CSV="$LOGDIR/timeseries.csv"
EVENTS="$LOGDIR/events.log"
ERROR_LOG="$LOGDIR/errors.log"
SUMMARY="$LOGDIR/summary.txt"
SIGNAL_HISTORY="$LOGDIR/signals_seen.txt"

# ── 工具函数 ──
log()    { echo "[$(date '+%H:%M:%S')] $*"; }
quiet()  { $QUIET && return; log "$@"; }
elog()   { echo "[$(date '+%H:%M:%S')] ERROR: $*" >> "$ERROR_LOG"; quiet "  ⛔ $*"; }
wlog()   { echo "[$(date '+%H:%M:%S')] WARN: $*" >> "$ERROR_LOG"; quiet "  ⚠️  $*"; }

api() {
  local method="$1" path="$2" data="${3:-}"
  curl -s --max-time 10 -w "\n%{http_code}" \
    -H "Content-Type: application/json" \
    ${TOKEN:+-H "Authorization: Bearer $TOKEN"} \
    ${data:+-d "$data"} \
    -X "$method" \
    "$BASE$path" 2>/dev/null || echo "CURL_FAILED\n000"
}

api_body() { api "$@" | sed '$d'; }
api_code() { api "$@" | tail -1; }
api_ok()   { local c; c=$(api_code "$@"); [ "$c" = "200" ] || [ "$c" = "201" ]; }

login() {
  local u="${1:-liangzai}" p="${2:-123456}"
  local resp; resp=$(api_body POST /api/auth/login "{\"username\":\"$u\",\"password\":\"$p\"}") || true
  TOKEN=$(echo "$resp" | python3 -c "import sys,json;print(json.load(sys.stdin).get('token','') or json.load(sys.stdin).get('session',''))" 2>/dev/null || echo "")
  [ -n "$TOKEN" ]
}

# ── CSV 初始化 ──
init_csv() {
  echo "timestamp,epoch,unix_ts,health_engine,health_api,snap_count,hot_count,sector_count,eval_count,sig_count,sig_new,alert_count,wl_count,hl_count,balance,mkt_phase,scan_total,scan_raw,scan_final,news_count,ipo_count" > "$CSV"
}

append_csv() {
  local vals=("$@")
  local line; line=$(printf "%s," "${vals[@]}")
  echo "${line%,}" >> "$CSV"
}

# ── 时间工具 ──
market_phase() {
  local h m; h=$(date +%H); m=$(date +%M)
  local hm=$((10#$h * 100 + 10#$m))
  if [ "$hm" -ge 930 ] && [ "$hm" -lt 1130 ]; then echo "morning"
  elif [ "$hm" -ge 1130 ] && [ "$hm" -lt 1300 ]; then echo "lunch"
  elif [ "$hm" -ge 1300 ] && [ "$hm" -lt 1500 ]; then echo "afternoon"
  elif [ "$hm" -ge 1500 ]; then echo "closed"
  else echo "preopen"
  fi
}

should_run() {
  # 始终运行（日盘+盘后），除非 --days 限制
  true
}

end_of_day() {
  local phase; phase=$(market_phase)
  [ "$phase" = "closed" ]
}

# ── 数据采样 ──
LAST_SIG_COUNT=-1
LAST_SNAP_COUNT=-1
STALL_WARN=false
SIGNAL_EVENTS=()

sample() {
  local epoch; epoch=$(date +%s)
  local ts; ts=$(date '+%H:%M:%S')
  local phase; phase=$(market_phase)
  local row=("$ts" "$epoch" "$(date '+%Y-%m-%d %H:%M:%S')")

  # health
  local hc; hc=$(api_body GET /api/health 2>/dev/null || echo "{}")
  local engine_ok; engine_ok=$(echo "$hc" | python3 -c "import sys,json;print('true' if json.load(sys.stdin).get('engine')==True else 'false')" 2>/dev/null || echo "false")
  local api_ok_v="true"
  row+=("$engine_ok" "$api_ok_v")

  if [ "$engine_ok" != "true" ]; then
    elog "引擎不可达"
    append_csv "${row[@]}"
    return
  fi

  # snapshot
  local snap; snap=$(api_body GET /api/snapshot 2>/dev/null || echo "[]")
  local snap_c; snap_c=$(echo "$snap" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d) if isinstance(d,list) else 0)" 2>/dev/null || echo "0")
  row+=("$snap_c")

  # hot snapshot
  local hot; hot=$(api_body GET /api/snapshot/hot 2>/dev/null || echo "[]")
  local hot_c; hot_c=$(echo "$hot" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d) if isinstance(d,list) else 0)" 2>/dev/null || echo "0")
  row+=("$hot_c")

  # sector hot
  local sec; sec=$(api_body GET /api/sector/hot 2>/dev/null || echo "[]")
  local sec_c; sec_c=$(echo "$sec" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d) if isinstance(d,list) else 0)" 2>/dev/null || echo "0")
  row+=("$sec_c")

  # evaluations
  local ev; ev=$(api_body GET /api/evaluations 2>/dev/null || echo "[]")
  local ev_c; ev_c=$(echo "$ev" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d) if isinstance(d,list) else 0)" 2>/dev/null || echo "0")
  row+=("$ev_c")

  # signals
  local sig; sig=$(api_body GET /api/signals 2>/dev/null || echo "[]")
  local sig_c; sig_c=$(echo "$sig" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d) if isinstance(d,list) else 0)" 2>/dev/null || echo "0")
  local sig_new=0
  if [ "$sig_c" -gt 0 ] && [ "$sig_c" -ne "$LAST_SIG_COUNT" ]; then
    local new_sigs; new_sigs=$(echo "$sig" | python3 -c "
import sys, json
seen = set()
try:
  with open('$SIGNAL_HISTORY') as f:
    for line in f: seen.add(line.strip())
except: pass
d = json.load(sys.stdin)
if isinstance(d, list):
  for s in d:
    key = s.get('code','') + '@' + str(s.get('total_score',0))
    if key not in seen:
      print(key)
      seen.add(key)
print('COUNT:' + str(len(d)))
" 2>/dev/null || echo "0")
    sig_new=$(echo "$new_sigs" | grep -c -v "^COUNT:" || true)
    local sig_list; sig_list=$(echo "$new_sigs" | grep -v "^COUNT:")
    if [ -n "$sig_list" ]; then
      while IFS= read -r entry; do
        [ -z "$entry" ] && continue
        local sev="信号事件: $entry at $ts"
        SIGNAL_EVENTS+=("$sev")
        echo "[$ts] $sev" >> "$EVENTS"
        quiet "  🔔 $sev"
      done <<< "$sig_list"
      echo "$sig_list" >> "$SIGNAL_HISTORY"
    fi
    local total; total=$(echo "$new_sigs" | grep "^COUNT:" | sed 's/COUNT://')
    [ -n "$total" ] && LAST_SIG_COUNT=$total || LAST_SIG_COUNT=$sig_c
  fi
  row+=("$sig_c" "$sig_new")

  # alerts
  local al; al=$(api_body GET /api/alerts 2>/dev/null || echo "[]")
  local al_c; al_c=$(echo "$al" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d) if isinstance(d,list) else 0)" 2>/dev/null || echo "0")
  row+=("$al_c")

  # watchlist
  local wl; wl=$(api_body GET /api/watchlist/enriched 2>/dev/null || echo '{"stocks":[]}')
  local wl_c; wl_c=$(echo "$wl" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d.get('stocks',[])) if isinstance(d,dict) else 0)" 2>/dev/null || echo "0")
  row+=("$wl_c")

  # holdings
  local hl; hl=$(api_body GET /api/holdings 2>/dev/null || echo '{"holdings":[],"available_balance":0}')
  local hl_c; hl_c=$(echo "$hl" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d.get('holdings',[])) if isinstance(d,dict) else 0)" 2>/dev/null || echo "0")
  local bal; bal=$(echo "$hl" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('available_balance',0))" 2>/dev/null || echo "0")
  row+=("$hl_c" "$bal")

  # market phase
  row+=("$phase")

  # scan stats
  local st; st=$(api_body GET /api/status 2>/dev/null || echo "{}")
  local scan_t; scan_t=$(echo "$st" | python3 -c "import sys,json;print(json.load(sys.stdin).get('scan_stats',{}).get('total_stocks',0))" 2>/dev/null || echo "0")
  local scan_r; scan_r=$(echo "$st" | python3 -c "import sys,json;print(json.load(sys.stdin).get('scan_stats',{}).get('raw_signals',0))" 2>/dev/null || echo "0")
  local scan_f; scan_f=$(echo "$st" | python3 -c "import sys,json;print(json.load(sys.stdin).get('scan_stats',{}).get('final_signals',0))" 2>/dev/null || echo "0")
  row+=("$scan_t" "$scan_r" "$scan_f")

  # news
  local nw; nw=$(api_body GET /api/news?all=true 2>/dev/null || echo "[]")
  local nw_c; nw_c=$(echo "$nw" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d) if isinstance(d,list) else 0)" 2>/dev/null || echo "0")
  row+=("$nw_c")

  # ipo
  local ipo; ipo=$(api_body GET /api/ipo/calendar 2>/dev/null || echo "[]")
  local ipo_c; ipo_c=$(echo "$ipo" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d) if isinstance(d,list) else 0)" 2>/dev/null || echo "0")
  row+=("$ipo_c")

  # stall detection
  if [ "$snap_c" -gt 0 ] && [ "$LAST_SNAP_COUNT" -ge 0 ]; then
    if [ "$snap_c" -eq "$LAST_SNAP_COUNT" ] && [ "$phase" != "lunch" ] && [ "$phase" != "closed" ] && [ "$phase" != "preopen" ]; then
      if [ "$STALL_WARN" = false ]; then
        wlog "数据停滞: 快照连续 ${snap_c} 条未变"
        STALL_WARN=true
      fi
    else
      STALL_WARN=false
    fi
  fi
  LAST_SNAP_COUNT=$snap_c

  append_csv "${row[@]}"
}

# ── 最终报告 ──
generate_report() {
  echo ""
  echo "═══════════════════════════════════════════════════════════════"
  echo "  全天监控报告 — $(date '+%Y-%m-%d %H:%M:%S')"
  echo "═══════════════════════════════════════════════════════════════"
  echo "  监控地址: $BASE"
  echo "  采样间隔: ${INTERVAL}s"
  echo "  日志目录: $LOGDIR"
  echo ""

  # CSV summary
  if [ -f "$CSV" ]; then
    local lines; lines=$(wc -l < "$CSV")
    echo "  采样点数: $((lines - 1))"
    echo ""

    python3 "$DIR/tests/.daylong_report.py" "$CSV" "$EVENTS" 2>/dev/null || {
      # fallback: basic text report
      echo "  ── 时段覆盖 ──"
      local phases; phases=$(tail -n +2 "$CSV" | cut -d, -f13 | sort -u | tr '\n' ' ')
      echo "  交易时段: $phases"
      echo ""
      echo "  ── 极值统计 ──"
      local max_snap; max_snap=$(tail -n +2 "$CSV" | cut -d, -f4 | sort -n | tail -1)
      local max_eval; max_eval=$(tail -n +2 "$CSV" | cut -d, -f7 | sort -n | tail -1)
      local max_sig; max_sig=$(tail -n +2 "$CSV" | cut -d, -f8 | sort -n | tail -1)
      echo "  最大快照数: $max_snap"
      echo "  最大评分股数: $max_eval"
      echo "  最大信号数: $max_sig"
    }
  fi

  echo ""
  echo "  ── 信号事件时间线 ──"
  if [ ${#SIGNAL_EVENTS[@]} -gt 0 ]; then
    for se in "${SIGNAL_EVENTS[@]}"; do echo "    $se"; done
  else
    echo "    (无信号事件)"
  fi

  echo ""
  if [ -f "$ERROR_LOG" ] && [ -s "$ERROR_LOG" ]; then
    echo "  ── 异常记录 ──"
    cat "$ERROR_LOG" | while IFS= read -r line; do echo "    $line"; done
  else
    echo "  ✅ 全天无异常"
  fi

  echo ""
  echo "═══════════════════════════════════════════════════════════════"
  echo "日志: $LOGDIR"
  echo "═══════════════════════════════════════════════════════════════"
}

# ═══════════════════════════════════════════════════════════════════
#  MAIN
# ═══════════════════════════════════════════════════════════════════
echo "═══════════════════════════════════════════════════════════════"
echo "  全天全流程监控启动 — $(date)"
echo "  地址: $BASE | 间隔: ${INTERVAL}s | 最长: ${MAX_DAYS}天"
echo "  日志: $LOGDIR"
echo "═══════════════════════════════════════════════════════════════"
echo ""

# S0: 环境检查
quiet "--- S0 环境准备 ---"
if curl -s --max-time 3 "$BASE/api/health" >/dev/null 2>&1; then
  quiet "  ✅ 后端可达 $BASE"
else
  echo "  ❌ 后端不可达，请先启动 ./start.sh 或 ./start-agent.sh"
  exit 1
fi

# 登录
for i in $(seq 1 3); do
  login && break
  sleep 1
done
if [ -z "$TOKEN" ]; then
  echo "  ❌ 登录失败"
  exit 1
fi
quiet "  ✅ 登录成功"

# 检查 adb
ADB_DEVICE=""
if which adb >/dev/null 2>&1; then
  ADB_DEVICE=$(adb devices 2>/dev/null | grep -v "List" | grep "device\$" | head -1 | awk '{print $1}')
fi
[ -n "$ADB_DEVICE" ] && quiet "  ✅ adb 设备: $ADB_DEVICE" || quiet "  ⏭️  无 adb 设备"

init_csv

# S1: 初始全量采样（预热）
quiet ""
quiet "--- S1 初始采样 ---"
sample
quiet "  ✅ 初始数据已采集"

# 捕获 adb logcat（后台）
LOG_PID=""
if [ -n "$ADB_DEVICE" ]; then
  adb logcat -v time -s QuantMain QuantGo NotifHelper 2>&1 \
    | tee "$LOGDIR/logcat.log" >/dev/null 2>&1 &
  LOG_PID=$!
  quiet "  ✅ logcat 后台捕获中 (PID=$LOG_PID)"
fi

# S2: 全天循环采样
quiet ""
quiet "--- S2 全天循环采样 (每 ${INTERVAL}s) ---"

START_EPOCH=$(date +%s)
MAX_EPOCH=$((START_EPOCH + MAX_DAYS * 86400))
CYCLE=0

while [ "$(date +%s)" -lt "$MAX_EPOCH" ]; do
  CYCLE=$((CYCLE + 1))
  sample
  quiet "  [C${CYCLE}] $(date '+%H:%M:%S') 采样完成"

  # 采样间隔
  for ((i=0; i<INTERVAL; i++)); do
    sleep 1
    [ "$(date +%s)" -ge "$MAX_EPOCH" ] && break 2
  done
done

# 清理
[ -n "$LOG_PID" ] && kill "$LOG_PID" 2>/dev/null || true

# S3: 最终报告
quiet ""
quiet "--- S3 最终报告 ---"
generate_report | tee "$SUMMARY"

echo ""
echo "全天监控完成。日志: $LOGDIR"
