#!/bin/bash
# e2e_full_day.sh — 全天候端到端自动测试脚本
# 运行: nohup bash tests/e2e_full_day.sh &> tests/test_day.log &
# 覆盖时段: PreMarket(8:30-9:15) / MorningTrade(9:15-11:30)
#           PreAfternoon(11:30-13:00) / AfternoonTrade(13:00-14:00)

DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIR"

ADDR="${TEST_ADDR:-127.0.0.1:8080}"
LOG_DIR="tests"
mkdir -p "$LOG_DIR"
TOKEN=""
FAIL_LOG="tests/failures_$(date +%Y%m%d).log"
> "$FAIL_LOG"

now_str() { date '+%Y-%m-%d %H:%M:%S'; }
now_min() { date '+%H%M' | sed 's/^0//'; }

log()  { echo "$(now_str) $1"; }
fail() { echo "$(now_str) FAIL $1" >> "$FAIL_LOG"; echo "$(now_str) FAIL $1"; }
warn() { echo "$(now_str) WARN $1"; }

do_login() {
    local resp
    resp=$(curl -s --max-time 5 -X POST "http://$ADDR/api/auth/login" \
        -H 'Content-Type: application/json' \
        -d '{"username":"liangzai","password":"123456"}' 2>/dev/null)
    TOKEN=$(echo "$resp" | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])" 2>/dev/null || echo "")
    [ -n "$TOKEN" ]
}

# ── API 状态检查 ──
check_status() {
    local data
    data=$(curl -s --max-time 5 "http://$ADDR/api/status" 2>/dev/null) || { fail "/api/status 无响应"; return; }
    local running in_trade session session_name data_source last_scan sig_cnt
    running=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('running',False))" 2>/dev/null)
    in_trade=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('in_trade_time',False))" 2>/dev/null)
    session=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('session',-1))" 2>/dev/null)
    session_name=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('session_name','?'))" 2>/dev/null)
    last_scan=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('last_scan',''))" 2>/dev/null)
    sig_cnt=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('signal_count',0))" 2>/dev/null)
    data_source=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('data_source','?'))" 2>/dev/null)
    last_data=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('last_data','?'))" 2>/dev/null)

    log "STATUS running=$running in_trade=$in_trade session=$session($session_name) 源=$data_source"
    log "  数据=$last_data 扫描=$last_scan 信号=$sig_cnt"

    # scan_stats 详细
    echo "$data" | python3 -c "
import sys,json;d=json.load(sys.stdin);ss=d.get('scan_stats')
if ss: print(f'  scan: total={ss.get(\"total_stocks\",0)} with={ss.get(\"with_data\",0)} raw={ss.get(\"raw_signals\",0)} final={ss.get(\"final_signals\",0)} blocks={ss.get(\"hot_sector_count\",0)} expand={ss.get(\"sector_stock_count\",0)}')
else: print('  scan: null')
" 2>/dev/null

    # session 校验
    local h m nmin expected
    h=$(date +%H); m=$(date +%M)
    nmin=$((10#$h * 100 + 10#$m))
    if [ "$nmin" -ge 830 ] && [ "$nmin" -lt 915 ]; then expected=0
    elif [ "$nmin" -ge 915 ] && [ "$nmin" -lt 1130 ]; then expected=1
    elif [ "$nmin" -ge 1130 ] && [ "$nmin" -lt 1300 ]; then expected=2
    elif [ "$nmin" -ge 1300 ] && [ "$nmin" -lt 1500 ]; then expected=3
    elif [ "$nmin" -ge 1500 ]; then expected=4
    else expected=5; fi
    if [ "$session" != "$expected" ]; then
        log "  ⚠ session不匹配: $session($session_name) vs 预期=$expected"
        fail "session偏差: $session($session_name) 预期=$expected 当前=$(date +%H:%M)"
    fi

    # 交易时段检查关键指标
    if [ "$expected" = "1" ] || [ "$expected" = "3" ]; then
        if [ "$sig_cnt" -lt 1 ]; then
            log "  ⚠ 交易时段但信号=0"
            fail "交易时段信号=0 session=$session"
        fi
        if [ "$in_trade" != "True" ]; then
            log "  ⚠ 交易时段但in_trade=False"
            fail "交易时段in_trade=False"
        fi
        if [ "$last_scan" = "00:00:00" ] || [ -z "$last_scan" ]; then
            log "  ⚠ 交易时段但last_scan未更新"
            fail "交易时段last_scan未更新"
        fi
    fi
}

# ── /api/signals ──
check_signals() {
    local data
    data=$(curl -s --max-time 5 "http://$ADDR/api/signals" -H "Authorization: Bearer $TOKEN" 2>/dev/null) || return
    local cnt
    cnt=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
    log "SIGNALS $cnt条"
    if [ "$cnt" -gt 0 ]; then
        echo "$data" | python3 -c "
import sys,json;d=json.load(sys.stdin)
top = sorted(d, key=lambda x: x.get('total_score',0), reverse=True)[:3]
for s in top: print(f'  {s.get(\"code\",\"\")} {s.get(\"name\",\"\")} {s.get(\"strategy\",\"\")} {s.get(\"total_score\",0):.0f}分 {s.get(\"action\",\"\")} N={s.get(\"n_score\",0):.0f} 凸={s.get(\"db_score\",0):.0f} 动={s.get(\"m_score\",0):.0f}')
" 2>/dev/null
    fi
}

# ── /api/evaluations ──
check_evals() {
    local data
    data=$(curl -s --max-time 5 "http://$ADDR/api/evaluations" -H "Authorization: Bearer $TOKEN" 2>/dev/null) || return
    local total scored
    total=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
    scored=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('m_score',0)>0))" 2>/dev/null)
    log "EVALS ${total}只, ${scored}只有分"
}

# ── /api/snapshot ──
check_snapshot() {
    local data
    data=$(curl -s --max-time 5 "http://$ADDR/api/snapshot" -H "Authorization: Bearer $TOKEN" 2>/dev/null) || return
    local cnt vol_ok
    cnt=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
    vol_ok=$(echo "$data" | python3 -c "
import sys,json;d=json.load(sys.stdin)
if len(d)==0: print('empty')
else: print(all(x.get('volume',0)>1000 for x in d[:3]))
" 2>/dev/null)
    log "SNAPSHOT ${cnt:-0}只 volume_correct=${vol_ok:-?}"
    if [ "${vol_ok:-}" = "False" ]; then
        fail "快照volume≤1000(价格数据泄漏)"
    fi
    if [ "${cnt:-0}" = "0" ]; then
        warn "  快照为空(引擎刚启动尚未采集)"
    fi
}

# ── /api/sector/hot ──
check_sector_hot() {
    local data
    data=$(curl -s --max-time 5 "http://$ADDR/api/sector/hot" 2>/dev/null) || return
    local cnt
    cnt=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
    log "SECTORS ${cnt:-0}个"
    if [ "${cnt:-0}" -gt 0 ]; then
        echo "$data" | python3 -c "
import sys,json;d=json.load(sys.stdin)
for s in d[:3]: print(f'  {s.get(\"name\",\"\")} {s.get(\"change_pct\",0):+.1f}% 涨停={s.get(\"limitup_cnt\",0)}')
" 2>/dev/null
    fi

    # 交易时段但没有板块 = 东财限流中
    local h m nmin
    h=$(date +%H); m=$(date +%M)
    nmin=$((10#$h * 100 + 10#$m))
    if [ "$nmin" -ge 915 ] && [ "$nmin" -lt 1500 ] && [ "${cnt:-0}" -eq 0 ]; then
        log "  ⚠ 交易时段但热点板块=0 (东财限流?)"
        fail "交易时段热点板块=0"
    fi
}

# ── /api/snapshot/hot ──
check_hot_snapshot() {
    local data
    data=$(curl -s --max-time 5 "http://$ADDR/api/snapshot/hot" -H "Authorization: Bearer $TOKEN" 2>/dev/null) || return
    local cnt
    cnt=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d) if d else 0)" 2>/dev/null)
    log "HOT_STOCKS ${cnt}只"
}

# ── /api/news ──
check_news() {
    local data
    data=$(curl -s --max-time 5 "http://$ADDR/api/news?all=true" 2>/dev/null) || return
    local cnt
    cnt=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
    log "NEWS ${cnt}条"
}

# ── /api/alerts ──
check_alerts() {
    local data
    data=$(curl -s --max-time 5 "http://$ADDR/api/alerts" 2>/dev/null) || return
    local cnt
    cnt=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
    log "ALERTS ${cnt:-0}条"
    if [ "${cnt:-0}" -gt 0 ]; then
        echo "$data" | python3 -c "
import sys,json;d=json.load(sys.stdin)
for a in d[:3]: print(f'  {a.get(\"level\",\"\")} {a.get(\"title\",\"\")}')
" 2>/dev/null
    fi
}

# ── logcat 引擎日志 ──
check_logcat() {
    local errs
    errs=$(adb logcat -d -s "QuantGo" 2>/dev/null | grep -i "error\|panic\|fail" | tail -3) || true
    if [ -n "$errs" ]; then
        log "LOGCAT 异常:"
        echo "$errs" | while IFS= read -r line; do log "  $line"; done
    fi
}

# ── 单轮测试 ──
run_cycle() {
    local c=$1
    log "======= 测试周期 #$c ======="

    check_status
    check_snapshot
    check_sector_hot
    check_hot_snapshot
    check_news
    check_alerts
    check_logcat

    local nmin
    nmin=$(date +%H%M | sed 's/^0*//')
    if [ "$nmin" -ge 915 ] && [ "$nmin" -lt 1500 ]; then
        check_signals
        check_evals
    fi
}

# ═══════════ MAIN ═══════════

log "===== 全天测试脚本启动 ====="
log "时间: $(date '+%Y-%m-%d %H:%M:%S')"
log "目标: http://$ADDR"
log "日志: tests/test_day.log"
log "失败: $FAIL_LOG"
log ""

for i in $(seq 1 5); do
    do_login && break
    sleep 3
done
if [ -z "$TOKEN" ]; then
    log "登录失败，退出"
    exit 1
fi
log "登录成功"

CYCLE=0
while true; do
    CYCLE=$((CYCLE + 1))
    run_cycle $CYCLE

    NM=$(date +%H%M | sed 's/^0*//')
    if [ "$NM" -ge 915 ] && [ "$NM" -lt 1500 ]; then
        sleep 60
    else
        sleep 120
    fi
done
