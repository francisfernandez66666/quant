#!/bin/bash
# 快速冒烟测试 — 极简版，直接 curl 不依赖复杂 bash
set -e

HOST="${1:-localhost}"
PORT="${2:-8080}"
BASE="http://${HOST}:${PORT}"
OUTDIR="$(cd "$(dirname "$0")" && pwd)/daily_results"
mkdir -p "$OUTDIR"
T=$(date +%Y%m%d_%H%M%S)
LOG="$OUTDIR/quick_${T}.log"

log() { echo "[$(date '+%H:%M:%S')] $*" | tee -a "$LOG"; }

# 获取测试账号
ACCT_FILE=""
for f in "../android/app/src/main/test_accounts.txt" "android/app/src/main/test_accounts.txt"; do
  [ -f "$f" ] && ACCT_FILE="$f" && break
done
if [ -z "$ACCT_FILE" ]; then
  log "没找到 test_accounts.txt，请提供: $0 localhost 8080 user pass"
  USERNAME="$3" PASSWORD="$4"
else
  USERNAME=$(sed -n 's/.*用户名: \([^ ]*\).*/\1/p' "$ACCT_FILE" | head -1)
  PASSWORD=$(sed -n 's/.*密 *码: \([^ ]*\).*/\1/p' "$ACCT_FILE" | head -1)
fi

log "===== 快速冒烟 ($BASE) ====="

# login
log "登录: $USERNAME"
RESP=$(curl -s -X POST -H "Content-Type: application/json" \
  -d "{\"username\":\"$USERNAME\",\"password\":\"$PASSWORD\"}" \
  "${BASE}/api/auth/login")
TOKEN=$(echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('token','') or d.get('session',''))" 2>/dev/null || echo "")
if [ -z "$TOKEN" ]; then
  log "登录失败: $RESP"
  exit 1
fi
log "登录成功"

# 表头
EVALS_CSV="$OUTDIR/evals_${T}.csv"
SIGNALS_CSV="$OUTDIR/signals_${T}.csv"
echo "time,code,name,n_score,dragon,db,dr,m_score" > "$EVALS_CSV"
echo "time,code,strategy,level,score" > "$SIGNALS_CSV"

# 采样
DURATION=300  # 5分钟
INTERVAL=15
log "采样开始 (${DURATION}s,间隔${INTERVAL}s)"
START=$(date +%s)
ROUND=0

while true; do
  NOW=$(date +%H:%M:%S)

  # evaluations
  E=$(curl -s -H "Authorization: Bearer $TOKEN" "${BASE}/api/evaluations" || echo '[]')
  EC=$(echo "$E" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print(len(d) if isinstance(d,list) else 0)
" 2>/dev/null || echo 0)

  echo "$E" | python3 -c "
import sys,json
ts='$NOW'
data=sys.stdin.read()
d=json.loads(data) if data else []
for s in (d if isinstance(d,list) else []):
    if max(s.get('n_score',0),s.get('dragon_score',0),s.get('db_score',0),s.get('dr_score',0),s.get('m_score',0))>40:
        print(f'{ts},{s[\"code\"]},{s.get(\"name\",\"?\")},{s.get(\"n_score\",0):.0f},{s.get(\"dragon_score\",0):.0f},{s.get(\"db_score\",0):.0f},{s.get(\"dr_score\",0):.0f},{s.get(\"m_score\",0):.0f}')
" >> "$EVALS_CSV"

  # signals
  S=$(curl -s -H "Authorization: Bearer $TOKEN" "${BASE}/api/signals" || echo '[]')
  SC=$(echo "$S" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print(len(d) if isinstance(d,list) else 0)
" 2>/dev/null || echo 0)

  echo "$S" | python3 -c "
import sys,json
ts='$NOW'
data=sys.stdin.read()
d=json.loads(data) if data else []
for s in (d if isinstance(d,list) else []):
    print(f'{ts},{s[\"code\"]},{s.get(\"strategy\",\"\")},{s.get(\"remind_level\",\"\")},{s.get(\"total_score\",0):.0f}')
" >> "$SIGNALS_CSV"

  ROUND=$((ROUND + 1))
  log "R${ROUND} 评分${EC}只 信号${SC}个"

  ELAPSED=$(($(date +%s) - START))
  [ $ELAPSED -ge $DURATION ] && break
  sleep $INTERVAL
done

log "===== 结果 ====="
log "轮次: $ROUND"

if [ -s "$EVALS_CSV" ]; then
  EV_TOTAL=$(tail -n +2 "$EVALS_CSV" | wc -l | tr -d ' ')
  log "评分条数: $EV_TOTAL"

  # Top N 股
  log "--- 高分个股 Top10 ---"
  tail -n +2 "$EVALS_CSV" | awk -F, '{print $3}' | sort | uniq -c | sort -rn | head -10 | tee -a "$LOG"

  # 检查老登股关键词
  log "--- 老登股检查 ---"
  BAD="银行|保险|电力|白酒|茅台|五粮|洋河|石油|石化|移动|联通|电信|工行|建行|农行|中行|招行|邮储|交行|神华|中煤|陕煤|华能|华电|长江|三峡|核电|大秦|宁沪|高速|海螺|福耀|上汽|格力"
  HIGH_NAME=$(tail -n +2 "$EVALS_CSV" | awk -F, '{if($4+$5+$6+$7+$8>60) print $3}' | sort -u)
  FOUND=$(echo "$HIGH_NAME" | grep -iE "$BAD" 2>/dev/null || true)
  if [ -n "$FOUND" ]; then
    log "[WARN] 发现疑似老登股:"
    echo "$FOUND"
  else
    log "[PASS] 高分股中未发现老登股"
  fi
fi

if [ -s "$SIGNALS_CSV" ]; then
  SIG_TOTAL=$(tail -n +2 "$SIGNALS_CSV" | wc -l | tr -d ' ')
  log "信号条数: $SIG_TOTAL"
fi

log "文件: $EVALS_CSV"
log "文件: $SIGNALS_CSV"
log "日志: $LOG"
