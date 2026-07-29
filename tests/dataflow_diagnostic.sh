#!/bin/bash
# dataflow_diagnostic.sh — 全链路数据流诊断脚本
# 覆盖：数据源 → 板块扫描 → 个股评分 → 信号输出
# 运行: bash tests/dataflow_diagnostic.sh 2>&1 | tee tests/diagnostic_$(date +%Y%m%d).log

DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIR"

ADDR="${TEST_ADDR:-127.0.0.1:8080}"
FAIL_LOG="tests/dataflow_failures_$(date +%Y%m%d).log"
TOKEN=""
> "$FAIL_LOG"

now_str() { date '+%Y-%m-%d %H:%M:%S'; }
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

# ── 1. 数据源健康检查 ──
check_data_sources() {
    log "=== 1. 数据源健康检查 ==="
    local data
    data=$(curl -s --max-time 10 "http://$ADDR/api/status" 2>/dev/null)
    if [ -z "$data" ]; then
        fail "status 无响应"
        return
    fi
    local ds last last_data
    ds=$(echo "$data" | python3 -c "import sys,json;print(json.load(sys.stdin).get('data_source','?'))" 2>/dev/null)
    last=$(echo "$data" | python3 -c "import sys,json;print(json.load(sys.stdin).get('last_data','?'))" 2>/dev/null)
    last_data=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('last_data',''))" 2>/dev/null)
    log "  数据源: $ds"
    log "  最后数据: $last"
    log "  last_data: $last_data"

    # 检查数据源是否正常
    if [ "$ds" = "?" ] || [ -z "$ds" ]; then
        fail "数据源状态未知"
    fi
    if [ -n "$last_data" ] && [ "$last_data" != "0001-01-01 00:00:00" ]; then
        # 检查数据是否太旧（>5分钟）
        local last_epoch now_epoch diff
        last_epoch=$(date -j -f "%Y-%m-%d %H:%M:%S" "$last_data" +%s 2>/dev/null || echo 0)
        now_epoch=$(date +%s)
        if [ "$last_epoch" -gt 0 ]; then
            diff=$((now_epoch - last_epoch))
            if [ "$diff" -gt 300 ]; then
                warn "  数据已过时 ${diff}s (＞5min)"
            fi
        fi
    fi
}

# ── 2. 板块数据流检查 ──
check_sector_flow() {
    log "=== 2. 板块数据流检查 ==="
    local data
    data=$(curl -s --max-time 10 "http://$ADDR/api/sector/hot" 2>/dev/null)
    if [ -z "$data" ]; then
        fail "sector/hot 无响应"
        return
    fi

    local cnt
    cnt=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
    log "  热点板块数: ${cnt:-0}"

    if [ "${cnt:-0}" -eq 0 ]; then
        fail "热点板块=0 — 板块数据流断裂"
        log "  可能原因:"
        log "    a) 东财板块API限流/被封"
        log "    b) 板块缓存未初始化（DataCoordinator.sectorCache 60s过期）"
        log "    c) SectorScanner 未正确 Update"
    else
        # 检查板块数据完整性
        echo "$data" | python3 -c "
import sys,json
d=json.load(sys.stdin)
for s in d[:5]:
    name = s.get('name','')
    cp = s.get('change_pct',0)
    lu = s.get('limitup_cnt',0)
    v = s.get('vol_rank',0)
    g2 = s.get('gain_2d',0)
    score = s.get('score',0)
    d1_score = s.get('d1_score',0)
    print(f'  {name} 涨幅{cp:+.1f}% 涨停{lu} vol_rank={v} gain_2d={g2} score={score} d1={d1_score}')
" 2>/dev/null
    fi
}

# ── 3. 快照数据流检查 ──
check_snapshot_flow() {
    log "=== 3. 快照数据流检查 ==="
    local data
    data=$(curl -s --max-time 10 "http://$ADDR/api/snapshot" -H "Authorization: Bearer $TOKEN" 2>/dev/null)
    if [ -z "$data" ]; then
        fail "snapshot 无响应"
        return
    fi
    local cnt price_ok vol_ok
    cnt=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
    log "  快照股票数: ${cnt:-0}"
    if [ "${cnt:-0}" = "0" ]; then
        fail "快照为空 — fetcher 未拉取数据"
        return
    fi

    # 检查数据质量
    echo "$data" | python3 -c "
import sys,json
d=json.load(sys.stdin)
price_ok = sum(1 for x in d if x.get('price',0) > 0)
vol_ok = sum(1 for x in d if x.get('volume',0) > 1000)
cp_ok = sum(1 for x in d if abs(x.get('change_pct',0)) > 0.01)
sector_ok = sum(1 for x in d if x.get('sector','') != '')
top5 = sorted(d, key=lambda x: abs(x.get('change_pct',0)), reverse=True)[:5]
print(f'  Price>0: {price_ok}/{len(d)}')
print(f'  Volume>1000: {vol_ok}/{len(d)}')
print(f'  ChangePct!=0: {cp_ok}/{len(d)}')
print(f'  HasSector: {sector_ok}/{len(d)}')
print(f'  涨幅前5:')
for s in top5:
    sec = s.get('sector','无')
    print(f'    {s.get(\"code\",\"\")} {s.get(\"name\",\"\")} {s.get(\"change_pct\",0):+.2f}% vol={s.get(\"volume\",0):.0f} 板块={sec}')
" 2>/dev/null
}

# ── 4. 评估评分检查 ──
check_eval_flow() {
    log "=== 4. 评估评分检查 ==="
    local data
    data=$(curl -s --max-time 10 "http://$ADDR/api/evaluations" -H "Authorization: Bearer $TOKEN" 2>/dev/null)
    if [ -z "$data" ]; then
        fail "evaluations 无响应"
        return
    fi
    local total n_scored m_scored d_scored db_scored dr_scored
    total=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
    n_scored=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('n_score',0)>0))" 2>/dev/null)
    m_scored=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('m_score',0)>0))" 2>/dev/null)
    d_scored=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('dragon_score',0)>0))" 2>/dev/null)
    db_scored=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('db_score',0)>0))" 2>/dev/null)
    dr_scored=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('dr_score',0)>0))" 2>/dev/null)

    log "  总评估: ${total}只"
    log "  N形有分: ${n_scored}只"
    log "  动量有分: ${m_scored}只"
    log "  破局龙有分: ${d_scored}只"
    log "  双凸有分: ${db_scored}只"
    log "  龙回头有分: ${dr_scored}只"

    if [ "${total}" -eq 0 ]; then
        fail "全量评估为空 — evaluateAll 未执行或全部跳过"
        log "  可能原因:"
        log "    a) K线数据未就绪（getCachedKLine 需要2根K线）"
        log "    b) 快照中股票的 Price <= 0"
        log "    c) scanCycle 未进入交易时段分支"
    fi

    # 展示得分分布
    echo "$data" | python3 -c "
import sys,json
d=json.load(sys.stdin)
n_high = sum(1 for x in d if x.get('n_score',0)>=80)
n_mid = sum(1 for x in d if 60<=x.get('n_score',0)<80)
n_low = sum(1 for x in d if 0<x.get('n_score',0)<60)
m_high = sum(1 for x in d if x.get('m_score',0)>=80)
m_mid = sum(1 for x in d if 60<=x.get('m_score',0)<80)
m_low = sum(1 for x in d if 0<x.get('m_score',0)<60)
print(f'  N形分布: ≥80分={n_high}  60-80={n_mid}  <60={n_low}')
print(f'  动量分布: ≥80分={m_high}  60-80={m_mid}  <60={m_low}')
if d:
    top_n = sorted(d, key=lambda x: x.get('n_score',0), reverse=True)[:3]
    print(f'  N形前三:')
    for s in top_n:
        print(f'    {s.get(\"code\",\"\")} {s.get(\"name\",\"\")} N={s.get(\"n_score\",0):.0f} D1={s.get(\"n_d1\",0):.0f} D2={s.get(\"n_d2\",0):.0f} D3={s.get(\"n_d3\",0):.0f} D4={s.get(\"n_d4\",0):.0f}')
" 2>/dev/null
}

# ── 5. 信号检查 ──
check_signals() {
    log "=== 5. 信号输出检查 ==="
    local data
    data=$(curl -s --max-time 10 "http://$ADDR/api/signals" -H "Authorization: Bearer $TOKEN" 2>/dev/null)
    if [ -z "$data" ]; then
        fail "signals 无响应"
        return
    fi
    local total trade watch
    total=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
    trade=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('level','')=='交易'))" 2>/dev/null)
    watch=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('level','')=='观望'))" 2>/dev/null)
    can_open=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('can_open',False)))" 2>/dev/null)

    log "  信号总数: ${total}"
    log "  交易信号: ${trade}  观望信号: ${watch}  可开仓: ${can_open}"

    if [ "${trade}" -eq 0 ] && [ "${total}" -gt 0 ]; then
        warn "  只有观望信号，没有交易信号 — 所有信号未通过 Pass 检查"
    fi
    if [ "${total}" -eq 0 ]; then
        fail "信号=0 — 全链路断裂"
        log "  可能原因链:"
        log "    a) evaluateAll 未返回任何 result"
        log "    b) 所有策略 TotalScore <= 0"
        log "    c) 所有结果 eval.Pass == false"
        log "    d) GenerateSignal 返回 error"
        log "    e) 风控阻断全部信号"
        log "    f) filter.Signals 过滤掉全部"
    fi

    # 展示前5信号
    if [ "${total}" -gt 0 ]; then
        echo "$data" | python3 -c "
import sys,json
d=json.load(sys.stdin)
top5 = sorted(d, key=lambda x: x.get('total_score',0), reverse=True)[:5]
for s in top5:
    print(f'  {s.get(\"code\",\"\")} {s.get(\"name\",\"\")} {s.get(\"strategy\",\"\")} 总分{s.get(\"total_score\",0):.0f} {s.get(\"level\",\"\")} {s.get(\"action\",\"\")} remind={s.get(\"remind_level\",\"\")} can_open={s.get(\"can_open\",False)} N={s.get(\"n_score\",0):.0f} 龙={s.get(\"dragon_score\",0):.0f}')
" 2>/dev/null
    fi
}

# ── 6. Scan Stats 诊断 ──
check_scan_stats() {
    log "=== 6. Scan Cycle 统计 ==="
    local data
    data=$(curl -s --max-time 5 "http://$ADDR/api/status" 2>/dev/null)
    if [ -z "$data" ]; then
        return
    fi
    echo "$data" | python3 -c "
import sys,json
d=json.load(sys.stdin)
ss=d.get('scan_stats')
if ss:
    print(f'  total_stocks={ss.get(\"total_stocks\",0)} with_data={ss.get(\"with_data\",0)}')
    print(f'  raw_signals={ss.get(\"raw_signals\",0)} final_signals={ss.get(\"final_signals\",0)}')
    print(f'  hot_sector_count={ss.get(\"hot_sector_count\",0)} sector_stock_count={ss.get(\"sector_stock_count\",0)}')
    print(f'  news_count={ss.get(\"news_count\",0)} with_error={ss.get(\"with_error\",0)}')
    # 诊断点
    if ss.get('total_stocks',0) == 0:
        print('  ❌ total_stocks=0 — fetcher 无股票')
    if ss.get('with_data',0) == 0:
        print('  ❌ with_data=0 — 所有股票无行情数据')
    if ss.get('raw_signals',0) > 0 and ss.get('final_signals',0) == 0:
        print('  ❌ raw>0 但 final=0 — 所有信号被过滤/风控阻断')
    if ss.get('hot_sector_count',0) == 0:
        print('  ⚠ hot_sector_count=0 — 板块扫描未出结果')
else:
    print('  scan_stats: null')
" 2>/dev/null
}

# ── 7. N 状态机检查 ──
check_nstates() {
    log "=== 7. N 形状态机检查 ==="
    local data
    data=$(curl -s --max-time 5 "http://$ADDR/api/nstates" 2>/dev/null)
    if [ -z "$data" ]; then
        log "  nstates 无响应（可能引擎版本不支持该API）"
        return
    fi
    local total idle fb flag sb comp failed
    total=$(echo "$data" | python3 -c "import sys,json;d=json.load(sys.stdin);if isinstance(d,list): print(len(d)); else: print(len(d.keys()));" 2>/dev/null)
    idle=$(echo "$data" | python3 -c "
import sys,json
d=json.load(sys.stdin)
if isinstance(d,list):
    items = d
else:
    items = d.values()
print(sum(1 for s in items if s.get('phase',-1)==0))
" 2>/dev/null)
    fb=$(echo "$data" | python3 -c "
import sys,json
d=json.load(sys.stdin)
if isinstance(d,list): items=d
else: items=d.values()
print(sum(1 for s in items if s.get('phase',-1)==1))
" 2>/dev/null)
    flag=$(echo "$data" | python3 -c "
import sys,json
d=json.load(sys.stdin)
if isinstance(d,list): items=d
else: items=d.values()
print(sum(1 for s in items if s.get('phase',-1)==2))
" 2>/dev/null)
    sb=$(echo "$data" | python3 -c "
import sys,json
d=json.load(sys.stdin)
if isinstance(d,list): items=d
else: items=d.values()
print(sum(1 for s in items if s.get('phase',-1)==3))
" 2>/dev/null)

    log "  NStates总数: ${total:-0}"
    log "  Idle: ${idle:-0} 一突: ${fb:-0} 旗面: ${flag:-0} 二突: ${sb:-0}"
    if [ "${total:-0}" -eq 0 ]; then
        warn "  状态机为空（可能未初始化或未进入交易时段）"
    fi
}

# ── 8. 告警和新闻检查 ──
check_alert_news() {
    log "=== 8. 告警 & 新闻 ==="
    local alerts news
    alerts=$(curl -s --max-time 5 "http://$ADDR/api/alerts" 2>/dev/null)
    news=$(curl -s --max-time 5 "http://$ADDR/api/news?all=true" 2>/dev/null)
    local a_cnt n_cnt
    a_cnt=$(echo "$alerts" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
    n_cnt=$(echo "$news" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
    log "  告警: ${a_cnt:-0}条  新闻: ${n_cnt:-0}条"
    if [ "${n_cnt:-0}" -gt 0 ]; then
        echo "$news" | python3 -c "
import sys,json
d=json.load(sys.stdin)
for n in d[:3]:
    print(f'  {n.get(\"title\",\"\")[:60]}')
" 2>/dev/null
    fi
    if [ "${a_cnt:-0}" -gt 0 ]; then
        echo "$alerts" | python3 -c "
import sys,json
d=json.load(sys.stdin)
for a in d[-5:]:
    print(f'  [{a.get(\"level\",\"\")}] {a.get(\"title\",\"\")} {a.get(\"time\",\"\")}')" 2>/dev/null
    fi
}

# ── 9. HTTP 日志诊断 ──
check_engine_logs() {
    log "=== 9. 引擎日志（最近错误）==="
    # 从 logcat 或直接日志检查
    if [ -f /tmp/quant_e2e.log ]; then
        local err_count panic_count warn_count
        err_count=$(grep -c "error\|Error\|ERROR" /tmp/quant_e2e.log 2>/dev/null || echo 0)
        panic_count=$(grep -c "PANIC\|panic" /tmp/quant_e2e.log 2>/dev/null || echo 0)
        warn_count=$(grep -c "WARN\|warn" /tmp/quant_e2e.log 2>/dev/null || echo 0)
        log "  错误: ${err_count}  PANIC: ${panic_count}  警告: ${warn_count}"
        if [ "$err_count" -gt 0 ]; then
            echo "  最近错误:"
            grep -i "error\|fail\|panic\|warn" /tmp/quant_e2e.log | tail -10 | while IFS= read -r line; do
                echo "    $line"
            done
        fi
    else
        log "  无引擎日志文件 (/tmp/quant_e2e.log)"
    fi
}

# ── 10. 完整诊断报告摘要 ──
generate_summary() {
    log ""
    log "═══════════════════════════════════════════════"
    log "  诊断摘要 $(date '+%H:%M')"
    log "═══════════════════════════════════════════════"
    log ""
    log "  数据流健康度检查清单:"
    log ""
    log "  [ ] 数据源 (新浪/东财/Tushare/同花顺)"
    log "  [ ] 板块数据 (SectorScanner)"
    log "  [ ] 快照数据 (Fetcher → MarketSnapshot)"
    log "  [ ] K线数据 (getCachedKLine)"
    log "  [ ] D1 事件匹配 (EventMatcher)"
    log "  [ ] 策略评分 (N/Dragon/DB/DR)"
    log "  [ ] 风控检查 (RiskCtrl)"
    log "  [ ] 过滤器 (FilterEngine)"
    log "  [ ] 信号输出 (SignalView)"
    log ""

    # 统计失败数
    local fcnt
    fcnt=$(wc -l < "$FAIL_LOG" 2>/dev/null || echo 0)
    if [ "$fcnt" -gt 0 ]; then
        log "  ❌ 本轮诊断发现 ${fcnt} 个问题（详见 $FAIL_LOG）"
        cat "$FAIL_LOG"
    else
        log "  ✅ 本轮诊断未发现问题"
    fi
    log "═══════════════════════════════════════════════"
}

# ═══════════ MAIN ═══════════

log "===== 全链路数据流诊断脚本 ====="
log "时间: $(date '+%Y-%m-%d %H:%M:%S')"
log "目标: http://$ADDR"
log "失败: $FAIL_LOG"

for i in $(seq 1 5); do
    do_login && break
    sleep 2
done
if [ -z "$TOKEN" ]; then
    log "登录失败，退出"
    exit 1
fi
log "登录成功"
log ""

# 执行诊断
check_data_sources
echo ""
check_sector_flow
echo ""
check_snapshot_flow
echo ""
check_eval_flow
echo ""
check_signals
echo ""
check_scan_stats
echo ""
check_nstates
echo ""
check_alert_news
echo ""
check_engine_logs
echo ""
generate_summary
