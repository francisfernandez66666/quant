#!/bin/bash
# continuous_monitor.sh — 全天候全链路连续监控
# 每轮检查：信号量、评分分布、数据流健康度 → 写入 CSV 以便后续分析
# 运行: bash tests/continuous_monitor.sh 2>&1 | tee tests/monitor_$(date +%Y%m%d).log

DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIR"

ADDR="${TEST_ADDR:-127.0.0.1:8080}"
CSV="tests/monitor_$(date +%Y%m%d).csv"
FAIL_LOG="tests/monitor_failures_$(date +%Y%m%d).log"
TOKEN=""
CYCLE=0

> "$FAIL_LOG"
# CSV 头
echo "time,session,signal_count,trade_count,watch_count,can_open,hot_sectors,total_stocks,with_data,raw_signals,final_signals,n_scored,d_scored,m_scored,hot_news,last_scan_age" > "$CSV"

now_str() { date '+%Y-%m-%d %H:%M:%S'; }
log()  { echo "$(now_str) $1"; }
fail() { echo "$(now_str) FAIL $1" >> "$FAIL_LOG"; echo "$(now_str) FAIL $1"; }

do_login() {
    local resp
    resp=$(curl -s --max-time 5 -X POST "http://$ADDR/api/auth/login" \
        -H 'Content-Type: application/json' \
        -d '{"username":"liangzai","password":"123456"}' 2>/dev/null)
    TOKEN=$(echo "$resp" | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])" 2>/dev/null || echo "")
    [ -n "$TOKEN" ]
}

# 获取 session 数值（同 CurrentSession）
get_session() {
    local h m nmin
    h=$(date +%H); m=$(date +%M)
    nmin=$((10#$h * 100 + 10#$m))
    if [ "$nmin" -ge 830 ] && [ "$nmin" -lt 915 ]; then echo 0
    elif [ "$nmin" -ge 915 ] && [ "$nmin" -lt 1130 ]; then echo 1
    elif [ "$nmin" -ge 1130 ] && [ "$nmin" -lt 1300 ]; then echo 2
    elif [ "$nmin" -ge 1300 ] && [ "$nmin" -lt 1500 ]; then echo 3
    elif [ "$nmin" -ge 1500 ]; then echo 4
    else echo 5; fi
}

run_diagnostic_cycle() {
    CYCLE=$((CYCLE + 1))
    local TS
    TS=$(now_str)
    local SESSION
    SESSION=$(get_session)

    log "=== 监控周期 #$CYCLE session=$SESSION $(date '+%H:%M:%S') ==="

    # ── 获取 status ──
    local status_data
    status_data=$(curl -s --max-time 5 "http://$ADDR/api/status" 2>/dev/null) || status_data=""

    local running in_trade last_scan sig_cnt total_stocks with_data raw_sig final_sig hsc ssc data_time
    running=$(echo "$status_data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('running',False))" 2>/dev/null)
    in_trade=$(echo "$status_data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('in_trade_time',False))" 2>/dev/null)
    last_scan=$(echo "$status_data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('last_scan',''))" 2>/dev/null)
    sig_cnt=$(echo "$status_data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('signal_count',0))" 2>/dev/null)
    total_stocks=$(echo "$status_data" | python3 -c "
import sys,json;d=json.load(sys.stdin);ss=d.get('scan_stats',{});print(ss.get('total_stocks',0))" 2>/dev/null)
    with_data=$(echo "$status_data" | python3 -c "
import sys,json;d=json.load(sys.stdin);ss=d.get('scan_stats',{});print(ss.get('with_data',0))" 2>/dev/null)
    raw_sig=$(echo "$status_data" | python3 -c "
import sys,json;d=json.load(sys.stdin);ss=d.get('scan_stats',{});print(ss.get('raw_signals',0))" 2>/dev/null)
    final_sig=$(echo "$status_data" | python3 -c "
import sys,json;d=json.load(sys.stdin);ss=d.get('scan_stats',{});print(ss.get('final_signals',0))" 2>/dev/null)
    hsc=$(echo "$status_data" | python3 -c "
import sys,json;d=json.load(sys.stdin);ss=d.get('scan_stats',{});print(ss.get('hot_sector_count',0))" 2>/dev/null)
    ssc=$(echo "$status_data" | python3 -c "
import sys,json;d=json.load(sys.stdin);ss=d.get('scan_stats',{});print(ss.get('sector_stock_count',0))" 2>/dev/null)
    data_time=$(echo "$status_data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('last_data',''))" 2>/dev/null)

    # ── 获取 evaluations ──
    local eval_data n_scored d_scored m_scored
    eval_data=$(curl -s --max-time 5 "http://$ADDR/api/evaluations" -H "Authorization: Bearer $TOKEN" 2>/dev/null) || eval_data=""
    n_scored=$(echo "$eval_data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('n_score',0)>0))" 2>/dev/null)
    d_scored=$(echo "$eval_data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('dragon_score',0)>0))" 2>/dev/null)
    m_scored=$(echo "$eval_data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('m_score',0)>0))" 2>/dev/null)

    # ── 获取 signals ──
    local sig_data trade_cnt watch_cnt can_open_cnt
    sig_data=$(curl -s --max-time 5 "http://$ADDR/api/signals" -H "Authorization: Bearer $TOKEN" 2>/dev/null) || sig_data=""
    trade_cnt=$(echo "$sig_data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('level','')=='交易'))" 2>/dev/null)
    watch_cnt=$(echo "$sig_data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('level','')=='观望'))" 2>/dev/null)
    can_open_cnt=$(echo "$sig_data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('can_open',False)))" 2>/dev/null)

    # ── 获取 news ──
    local news_data news_cnt
    news_data=$(curl -s --max-time 5 "http://$ADDR/api/news?all=true" 2>/dev/null) || news_data=""
    news_cnt=$(echo "$news_data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)

    # ── last_scan 年龄计算 ──
    local last_scan_age=999
    if [ -n "$last_scan" ] && [ "$last_scan" != "00:00:00" ]; then
        local scan_epoch now_epoch
        scan_epoch=$(date -j -f "%H:%M:%S" "$last_scan" +%s 2>/dev/null || echo 0)
        now_epoch=$(date +%s)
        if [ "$scan_epoch" -gt 0 ]; then
            # 跨天处理
            last_scan_age=$(( (now_epoch - scan_epoch) % 86400 ))
        fi
    fi

    # ── 写入 CSV ──
    echo "$TS,$SESSION,${sig_cnt:-0},${trade_cnt:-0},${watch_cnt:-0},${can_open_cnt:-0},${hsc:-0},${total_stocks:-0},${with_data:-0},${raw_sig:-0},${final_sig:-0},${n_scored:-0},${d_scored:-0},${m_scored:-0},${news_cnt:-0},${last_scan_age}" >> "$CSV"

    # ── 终端输出摘要 ──
    log "  running=$running in_trade=$in_trade last_scan=$last_scan(${last_scan_age}s前)"
    log "  信号: ${sig_cnt} (交易${trade_cnt}/观望${watch_cnt}/可开${can_open_cnt})"
    log "  扫描: total=${total_stocks} data=${with_data} raw=${raw_sig} final=${final_sig}"
    log "  板块: hot=${hsc} expand=${ssc}"
    log "  评分: N=${n_scored} 龙=${d_scored} 动量=${m_scored}"
    log "  新闻: ${news_cnt}条"

    # ── 警告检测 ──
    if [ "$SESSION" = "1" ] || [ "$SESSION" = "3" ]; then
        if [ "${total_stocks:-0}" -eq 0 ]; then
            fail "交易时段 total_stocks=0 — fetcher 未初始化"
        fi
        if [ "${with_data:-0}" -eq 0 ] && [ "${total_stocks:-0}" -gt 0 ]; then
            fail "交易时段 with_data=0 — 所有股票无行情"
        fi
        if [ "${raw_sig:-0}" -gt 0 ] && [ "${final_sig:-0}" -eq 0 ]; then
            fail "raw_signals=${raw_sig} 但 final_signals=0 — 全被过滤/风控"
        fi
        if [ "${hsc:-0}" -eq 0 ]; then
            fail "交易时段 hot_sector_count=0"
        fi
        if [ "${sig_cnt:-0}" -eq 0 ] && [ "${raw_sig:-0}" -eq 0 ]; then
            fail "交易时段 无原始信号 — evaluateAll 未产出任何评分"
        fi
        if [ "${n_scored:-0}" -eq 0 ] && [ "${m_scored:-0}" -eq 0 ]; then
            fail "交易时段 所有策略评分为0"
        fi
        if [ "$last_scan_age" -gt 60 ]; then
            fail "last_scan 已 ${last_scan_age}s 未更新（正常应每5-30s更新）"
        fi
        if [ "${news_cnt:-0}" -eq 0 ]; then
            warn "交易时段 news=0 — D1事件可能无法匹配"
        fi
    fi
}

# ═══════════ MAIN ═══════════

log "===== 全天候连续监控启动 ====="
log "时间: $(date '+%Y-%m-%d %H:%M:%S')"
log "目标: http://$ADDR"
log "CSV: $CSV"
log "失败: $FAIL_LOG"

for i in $(seq 1 5); do
    do_login && break
    sleep 3
done
if [ -z "$TOKEN" ]; then
    log "登录失败，退出"
    exit 1
fi
log "登录成功"
log ""

while true; do
    run_diagnostic_cycle
    # 动态间隔
    local NM
    NM=$(date +%H%M | sed 's/^0*//')
    if [ "$NM" -ge 915 ] && [ "$NM" -lt 1500 ]; then
        sleep 30  # 交易时段30秒
    else
        sleep 120 # 非交易时段2分钟
    fi
done
