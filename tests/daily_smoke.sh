#!/bin/bash
# 日间效果监测脚本
# 用法: ./tests/daily_smoke.sh [--port 8080] [--host localhost] [--interval 60] [--duration 28800]
# 默认: 每分钟采样，持续8小时(一个交易日)

set -euo pipefail

HOST="localhost"
PORT="8080"
INTERVAL=60       # 采样间隔(秒)
DURATION=28800    # 总时长(秒)，默认8小时
USERNAME=""
PASSWORD=""
TOKEN=""

OUTDIR="$(cd "$(dirname "$0")" && pwd)/daily_results"
mkdir -p "$OUTDIR"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
MAIN_LOG="$OUTDIR/smoke_${TIMESTAMP}.log"
EVAL_LOG="$OUTDIR/evals_${TIMESTAMP}.csv"
SIGNAL_LOG="$OUTDIR/signals_${TIMESTAMP}.csv"
ALERT_LOG="$OUTDIR/alerts_${TIMESTAMP}.csv"
SUMMARY="$OUTDIR/summary_${TIMESTAMP}.txt"

# --- 命令行解析 ---
while [[ $# -gt 0 ]]; do
  case "$1" in
    --port) PORT="$2"; shift 2;;
    --host) HOST="$2"; shift 2;;
    --interval) INTERVAL="$2"; shift 2;;
    --duration) DURATION="$2"; shift 2;;
    -u|--user) USERNAME="$2"; shift 2;;
    -p|--pass) PASSWORD="$2"; shift 2;;
    *) echo "未知参数: $1"; exit 1;;
  esac
done

BASE="http://${HOST}:${PORT}"
COOKIE_JAR="/tmp/liangzai_smoke_cookies.$$"

log() { echo "[$(date '+%H:%M:%S')] $*" | tee -a "$MAIN_LOG"; }

api() {
  local method="$1" path="$2" data="${3:-}"
  local cmd=(curl -s -w "\n%{http_code}" -o /tmp/smoke_resp_$$.json)
  if [ -n "$TOKEN" ]; then
    cmd+=(-H "Authorization: Bearer $TOKEN")
  fi
  cmd+=(-H "Content-Type: application/json")
  if [ "$method" != "GET" ]; then
    cmd+=(-X "$method")
  fi
  if [ -n "$data" ]; then
    cmd+=(-d "$data")
  fi
  cmd+=("${BASE}${path}")
  local http_code
  http_code=$("${cmd[@]}" 2>/dev/null)
  if [ "$http_code" = "200" ]; then
    cat /tmp/smoke_resp_$$.json
    return 0
  else
    log "API失败 $path -> HTTP $http_code"
    cat /tmp/smoke_resp_$$.json 2>/dev/null || true
    return 1
  fi
}

login() {
  if [ -z "$USERNAME" ]; then
    log "请提供测试账号: -u 用户名 -p 密码"
    log "或从 test_accounts.txt 自动读取..."
    # 尝试从android构建产物读取
    local af="$(cd "$(dirname "$0")/.." && pwd)/android/app/src/main/test_accounts.txt"
    if [ -f "$af" ]; then
      USERNAME=$(sed -n 's/.*用户名: \([^ ]*\).*/\1/p' "$af" | head -1)
      PASSWORD=$(sed -n 's/.*密 *码: \([^ ]*\).*/\1/p' "$af" | head -1)
      log "从 $af 读取: $USERNAME"
    fi
  fi
  if [ -z "$USERNAME" ]; then
    log "无法获取账号，终止"
    exit 1
  fi

  log "登录 $BASE/api/auth/login (${USERNAME})"
  local resp
  resp=$(api POST /api/auth/login "{\"username\":\"$USERNAME\",\"password\":\"$PASSWORD\"}")
  if [ $? -ne 0 ]; then
    log "登录失败"
    exit 1
  fi
  TOKEN=$(echo "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))" 2>/dev/null || echo "")
  if [ -z "$TOKEN" ]; then
    TOKEN=$(echo "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session',''))" 2>/dev/null || echo "")
  fi
  if [ -z "$TOKEN" ]; then
    log "无法提取token: $resp"
    exit 1
  fi
  log "登录成功 token=${TOKEN:0:16}..."
}

# --- 采样函数 ---

sample() {
  local round="$1"
  local now=$(date +%H:%M:%S)

  # 1. 持仓状态
  local holdings
  holdings=$(api GET /api/holdings 2>/dev/null || echo '{"holdings":[]}')
  local h_count=$(echo "$holdings" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d.get('holdings',[])))" 2>/dev/null || echo 0)

  # 2. 全量评分（含被过滤的股票直接不出现在此列表）
  local evals
  evals=$(api GET /api/evaluations 2>/dev/null || echo '[]')
  local eval_count=$(echo "$evals" | python3 -c "import sys,json; e=json.load(sys.stdin); print(len(e) if isinstance(e,list) else 0)" 2>/dev/null || echo 0)
  local top5
  top5=$(echo "$evals" | python3 -c "
import sys, json
e = json.load(sys.stdin) if isinstance(sys.stdin.read(), str) else []
if not isinstance(e, list): e = []
e = sorted(e, key=lambda x: max(x.get('n_score',0), x.get('dragon_score',0), x.get('db_score',0), x.get('dr_score',0), x.get('m_score',0)), reverse=True)[:5]
print(json.dumps(e))
" 2>/dev/null || echo '[]')

  # 写入评分CSV（含名字/分数）
  if [ "$eval_count" -gt 0 ] 2>/dev/null; then
    echo "$evals" | python3 -c "
import sys, json
e = json.load(sys.stdin) if sys.stdin.read() else []
if not isinstance(e, list): e = []
for s in e:
    code = s.get('code','')
    name = s.get('name','?')
    n = s.get('n_score',0)
    d = s.get('dragon_score',0)
    db = s.get('db_score',0)
    dr = s.get('dr_score',0)
    m = s.get('m_score',0)
    print(f'{code},{name},{n:.0f},{d:.0f},{db:.0f},{dr:.0f},{m:.0f}')
" >> "$EVAL_LOG"
  fi

  # 3. 信号
  local signals
  signals=$(api GET /api/signals 2>/dev/null || echo '[]')
  local sig_count=$(echo "$signals" | python3 -c "import sys,json; s=json.load(sys.stdin); print(len(s) if isinstance(s,list) else 0)" 2>/dev/null || echo 0)

  if [ "$sig_count" -gt 0 ] 2>/dev/null; then
    echo "$signals" | python3 -c "
import sys, json
s = json.load(sys.stdin) if sys.stdin.read() else []
if not isinstance(s, list): s = []
for sig in s:
    code = sig.get('code','')
    strategy = sig.get('strategy','')
    level = sig.get('remind_level','')
    score = sig.get('total_score',0)
    print(f'{code},{strategy},{level},{score:.0f}')
" >> "$SIGNAL_LOG"
  fi

  # 4. 告警
  local alerts
  alerts=$(api GET /api/alerts 2>/dev/null || echo '[]')
  local alert_count=$(echo "$alerts" | python3 -c "import sys,json; a=json.load(sys.stdin); print(len(a) if isinstance(a,list) else 0)" 2>/dev/null || echo 0)

  if [ "$alert_count" -gt 0 ] 2>/dev/null; then
    echo "$alerts" | python3 -c "
import sys, json
a = json.load(sys.stdin) if sys.stdin.read() else []
if not isinstance(a, list): a = []
for al in a:
    level = al.get('level','')
    title = (al.get('title','') or '').replace(',',';')
    body = (al.get('body','') or '').replace(',',';')
    code = al.get('code','')
    print(f'{code},{level},{title},{body}')
" >> "$ALERT_LOG"
  fi

  # 日志
  log "R${round} 持仓${h_count} 评分${eval_count} 信号${sig_count} 告警${alert_count}"

  # 打印top5热股
  echo "$top5" | python3 -c "
import sys, json
ts = json.load(sys.stdin)
for t in ts[:5]:
    code = t.get('code','')
    name = t.get('name','?')
    ms = max(t.get('n_score',0), t.get('dragon_score',0), t.get('db_score',0), t.get('dr_score',0), t.get('m_score',0))
    print(f'  {code} {name} max={ms:.0f}')
" 2>/dev/null | tee -a "$MAIN_LOG"
}

# --- 汇总报告 ---

generate_summary() {
  log "===== 生成日终报告 ====="
  {
    echo "============================================"
    echo "  量仔系统 $TIMESTAMP 日间效果报告"
    echo "============================================"
    echo ""
    echo "配置: HOST=$HOST PORT=$PORT 间隔=${INTERVAL}s"
    echo "总轮次: $((total_rounds + 1))"
    echo ""

    # 评分统计
    if [ -f "$EVAL_LOG" ]; then
      local eval_lines=$(wc -l < "$EVAL_LOG" | tr -d ' ')
      echo "--- 评分覆盖 ---"
      echo "总评分条数: $eval_lines"
      echo "评分最高个股(top10):"
      sort -t, -k7 -nr "$EVAL_LOG" | head -10 | awk -F, '{printf "  %-8s %-10s N=%-4s 龙=%-4s 凸=%-4s 回=%-4s 量=%-4s\n", $1, $2, $3, $4, $5, $6, $7}'
      echo ""
    fi

    # 信号统计
    if [ -f "$SIGNAL_LOG" ]; then
      local sig_lines=$(wc -l < "$SIGNAL_LOG" | tr -d ' ')
      echo "--- 信号统计 ---"
      echo "总信号条数: $sig_lines"
      echo "策略分布:"
      awk -F, '{print $2}' "$SIGNAL_LOG" | sort | uniq -c | sort -rn | head -10
      echo ""
      echo "等级分布:"
      awk -F, '{print $3}' "$SIGNAL_LOG" | sort | uniq -c | sort -rn
      echo ""
    fi

    # 告警统计
    if [ -f "$ALERT_LOG" ]; then
      local alert_lines=$(wc -l < "$ALERT_LOG" | tr -d ' ')
      echo "--- 告警统计 ---"
      echo "总告警条数: $alert_lines"
      echo "级别分布:"
      awk -F, '{print $2}' "$ALERT_LOG" | sort | uniq -c | sort -rn
      echo ""

      # 检查是否有「换手率过低」被过滤的记录（从日志中）
      echo "--- 过滤检查 ---"
      if grep -q "换手率过低\|僵尸股" "$MAIN_LOG" 2>/dev/null; then
        echo "[OK] 检测到换手率过低过滤（老登股已拦截）"
      else
        echo "[INFO] 无僵尸股过滤记录（如大盘活跃则正常）"
      fi
      echo ""
    fi

    echo "--- 数据文件 ---"
    echo "主日志:   $MAIN_LOG"
    echo "评分CSV:  $EVAL_LOG"
    echo "信号CSV:  $SIGNAL_LOG"
    echo "告警CSV:  $ALERT_LOG"
    echo ""
    echo "报告生成时间: $(date '+%Y-%m-%d %H:%M:%S')"
  } > "$SUMMARY"

  echo ""
  cat "$SUMMARY"
  log "报告已保存: $SUMMARY"
}

# --- cleanup ---
cleanup() {
  log "接收到中断信号，正在汇总..."
  generate_summary
  rm -f /tmp/smoke_resp_$$.json
  exit 0
}
trap cleanup INT TERM

# --- 主流程 ---
log "======== 量仔日间效果测试 ========"
log "BASE=$BASE 间隔=${INTERVAL}s 时长=${DURATION}s"

login

# 写入CSV表头
echo "code,name,n_score,dragon_score,db_score,dr_score,m_score" > "$EVAL_LOG"
echo "code,strategy,level,score" > "$SIGNAL_LOG"
echo "code,level,title,body" > "$ALERT_LOG"

total_rounds=0
start_time=$(date +%s)

while true; do
  sample $total_rounds
  total_rounds=$((total_rounds + 1))

  elapsed=$(($(date +%s) - start_time))
  if [ $elapsed -ge $DURATION ]; then
    log "达到设定时长 ${DURATION}s，停止采样"
    break
  fi
  sleep $INTERVAL
done

generate_summary
log "测试完成"
