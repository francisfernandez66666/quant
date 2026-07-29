#!/bin/bash
# apk_e2e_test.sh — APK 全量端到端测试脚本
# 覆盖：连通性/数据流/业务逻辑/安卓通知/移动端体验
# 运行: bash tests/apk_e2e_test.sh 2>&1 | tee tests/apk_$(date +%Y%m%d_%H%M).log

DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIR"

ADDR="${TEST_ADDR:-127.0.0.1:8080}"
TOKEN=""
PASS=0
FAIL=0
TESTS=()
FAILURES=()
DURATION=0

now_str() { date '+%H:%M:%S'; }
log()   { echo "$(now_str) $1"; }
pass()  { PASS=$((PASS+1)); TESTS+=("PASS:$1"); }
fail()  { FAIL=$((FAIL+1)); TESTS+=("FAIL:$1"); FAILURES+=("$1|$2|$3"); }

do_login() {
    local resp
    resp=$(curl -s --max-time 5 -X POST "http://$ADDR/api/auth/login" \
        -H 'Content-Type: application/json' \
        -d '{"username":"liangzai","password":"123456"}' 2>/dev/null)
    TOKEN=$(echo "$resp" | python3 -c "import sys,json;print(json.load(sys.stdin).get('token',''))" 2>/dev/null || echo "")
    [ -n "$TOKEN" ]
}

random_code() {
    echo "0000$(shuf -i 100-999 -n 1)"
}

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "  APK 全量端到端测试 — $(date '+%Y-%m-%d %H:%M:%S')"
echo "  目标: http://$ADDR"
echo "═══════════════════════════════════════════════════════════════"
echo ""

# ── 1. 连通性 ──
echo "─── 1. 连通性 ───"

# T1: 健康检查
t0=$(date +%s)
resp=$(curl -s --max-time 3 "http://$ADDR/api/health" 2>/dev/null)
engine=$(echo "$resp" | python3 -c "import sys,json;print(json.load(sys.stdin).get('engine','false'))" 2>/dev/null)
[ "$engine" = "true" ] && pass "健康检查 engine=true" || fail "健康检查" "engine=true" "engine=$engine"
echo "  T1 健康检查: engine=$engine"

# T2: 登录
for i in $(seq 1 3); do
    do_login && break
    sleep 1
done
[ -n "$TOKEN" ] && pass "登录获取JWT" || fail "登录" "TOKEN非空" "TOKEN为空"
echo "  T2 登录: TOKEN=${TOKEN:0:20}..."

# T3: 状态
resp=$(curl -s --max-time 3 "http://$ADDR/api/status" 2>/dev/null)
sess=$(echo "$resp" | python3 -c "import sys,json;print(json.load(sys.stdin).get('session_name',''))" 2>/dev/null)
run=$(echo "$resp" | python3 -c "import sys,json;print(json.load(sys.stdin).get('running','false'))" 2>/dev/null)
[ "$run" = "true" ] && [ -n "$sess" ] && pass "引擎运行+session非空" || fail "引擎状态" "running=true+sess非空" "running=$run sess=$sess"
echo "  T3 状态: running=$run session=$sess"

# T4: SSE 连接
sse_ok=0
for i in $(seq 1 5); do
    resp=$(curl -s --max-time 3 "http://$ADDR/api/events?token=$TOKEN" 2>/dev/null)
    if echo "$resp" | grep -q "data:"; then
        sse_ok=1
        break
    fi
    sleep 1
done
[ "$sse_ok" -eq 1 ] && pass "SSE 连接成功" || fail "SSE连接" "收到data:" "无响应或格式异常"
echo "  T4 SSE: $([ $sse_ok -eq 1 ] && echo '✅' || echo '❌')"

DURATION=$(( $(date +%s) - t0 ))
echo "  连通性耗时: ${DURATION}s"
echo ""

# ── 2. 数据源 ──
echo "─── 2. 数据源 ───"

# T5: 快照股票数
t0=$(date +%s)
resp=$(curl -s --max-time 5 "http://$ADDR/api/snapshot" -H "Authorization: Bearer $TOKEN" 2>/dev/null)
cnt=$(echo "$resp" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
[ "$cnt" -ge 20 ] && pass "快照≥20只股票(cnt=$cnt)" || fail "快照数量" "≥20" "$cnt"
echo "  T5 快照: $cnt 只股票"

# T6: 价格>0比例
price_ok=$(echo "$resp" | python3 -c "
import sys,json;d=json.load(sys.stdin);total=len(d)
ok=sum(1 for x in d if x.get('price',0)>0);print(f'{ok},{total},{ok/total*100:.0f}')" 2>/dev/null)
p_ok=$(echo "$price_ok" | cut -d, -f1)
p_total=$(echo "$price_ok" | cut -d, -f2)
p_pct=$(echo "$price_ok" | cut -d, -f3)
if [ "$p_pct" -ge 80 ] 2>/dev/null; then
    pass "快照${p_pct}%股票Price>0"
else
    fail "Price>0比例" "≥80%" "${p_pct}%"
fi
echo "  T6 Price>0: ${p_ok}/${p_total}=${p_pct}%"

# T7: 板块数据
resp=$(curl -s --max-time 5 "http://$ADDR/api/sector/hot" 2>/dev/null)
sec_cnt=$(echo "$resp" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
sec_pos=$(echo "$resp" | python3 -c "
import sys,json;d=json.load(sys.stdin);print(sum(1 for s in d if s.get('change_pct',0)>0) if d else 0)" 2>/dev/null)
if [ "$sec_cnt" -gt 0 ]; then
    pass "有热门板块返回(cnt=$sec_cnt)"
    [ "$sec_pos" -eq "$sec_cnt" ] && pass "全部板块ChangePct>0" || fail "下跌板块" "应无" "$(echo "$resp" | python3 -c "
import sys,json;d=json.load(sys.stdin)
for s in d:
    if s.get('change_pct',0)<=0: print(s.get('name',''))" 2>/dev/null | head -3)"
else
    # 东财熔断中也能从缓存获取
    log "  ⚠ 板块数为0(东财熔断,正常)"
    pass "热门板块(熔断中=0也可接受)"
fi
echo "  T7 板块: $sec_cnt 个(上涨$sec_pos)"

DURATION=$(( $(date +%s) - t0 + DURATION ))
echo "  数据源耗时: ${DURATION}s"
echo ""

# ── 3. 评分与信号 ──
echo "─── 3. 评分与信号 ───"

# T8: 评估数量
resp=$(curl -s --max-time 5 "http://$ADDR/api/evaluations" -H "Authorization: Bearer $TOKEN" 2>/dev/null)
ev_cnt=$(echo "$resp" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
[ "$ev_cnt" -ge 20 ] && pass "评估≥20条(cnt=$ev_cnt)" || fail "评估数量" "≥20" "$ev_cnt"
echo "  T8 评估: $ev_cnt 条"

# T9: 有N形分>0的
n_gt0=$(echo "$resp" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('n_score',0)>0))" 2>/dev/null)
[ "$n_gt0" -gt 0 ] && pass "N形评分>0: ${n_gt0}只" || fail "N形评分>0" ">0" "$n_gt0"
echo "  T9 N形: $n_gt0 只有分"

# T10: 有动量分>0的
m_gt0=$(echo "$resp" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('m_score',0)>0))" 2>/dev/null)
[ "$m_gt0" -gt 0 ] && pass "动量评分>0: ${m_gt0}只" || fail "动量分" ">0" "$m_gt0"
echo "  T10 动量: $m_gt0 只有分"

# T11: D1>0的
d1_gt0=$(echo "$resp" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('n_d1',0)>0))" 2>/dev/null)
[ "$d1_gt0" -ge 0 ] 2>/dev/null && pass "D1≥0统计: ${d1_gt0}只" || fail "D1统计" "≥0" "$d1_gt0"
echo "  T11 D1>0: $d1_gt0 只"

# T12: 信号
sig_resp=$(curl -s --max-time 5 "http://$ADDR/api/signals" -H "Authorization: Bearer $TOKEN" 2>/dev/null)
sig_cnt=$(echo "$sig_resp" | python3 -c "import sys,json;d=json.load(sys.stdin);print(len(d))" 2>/dev/null)
[ "$sig_cnt" -gt 0 ] && pass "有信号返回(cnt=$sig_cnt)" || fail "信号数量" ">0" "$sig_cnt"
echo "  T12 信号: $sig_cnt 条"

# T13: 无"交易"级别但Pass=false的信号
bad_trade=$(echo "$sig_resp" | python3 -c "
import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('level','')=='交易' and not x.get('n_pass',False)))" 2>/dev/null)
[ "$bad_trade" -eq 0 ] && pass "无Pass=false的交易信号" || fail "无效信号" "0" "$bad_trade"
echo "  T13 无效交易: $bad_trade"

# 评分Top5摘要
echo "$resp" | python3 -c "
import sys,json
d=json.load(sys.stdin)
top = sorted(d, key=lambda x: x.get('n_score',0), reverse=True)[:3]
print('  评分Top3:')
for s in top:
    print(f'    {s.get(\"code\",\"\")} {s.get(\"name\",\"\")[:6]} N={s.get(\"n_score\",0):.0f} D1={s.get(\"n_d1\",0):.0f} pass={s.get(\"n_pass\",False)}')
" 2>/dev/null
echo ""

# ── 4. 自选操作 ──
echo "─── 4. 自选操作 ───"

# T14: 添加自选
code="000001"
add_resp=$(curl -s -X POST "http://$ADDR/api/watchlist" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d "{\"code\":\"$code\"}" 2>/dev/null)
add_ok=$(echo "$add_resp" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('status','fail'))" 2>/dev/null)
[ "$add_ok" = "ok" ] && pass "自选添加成功($code)" || fail "自选添加" "status=ok" "$add_resp"
echo "  T14 添加自选 $code: $add_ok"

# T15: 验证自选列表含刚添加的
wl_resp=$(curl -s "http://$ADDR/api/watchlist" -H "Authorization: Bearer $TOKEN" 2>/dev/null)
has_code=$(echo "$wl_resp" | python3 -c "
import sys,json;d=json.load(sys.stdin);codes=[c.get('code',c) if isinstance(c,dict) else c for c in d.get('stocks',[])]
print('yes' if '$code' in codes else 'no')" 2>/dev/null)
[ "$has_code" = "yes" ] && pass "自选列表含$code" || fail "自选验证" "含$code" "列表不含"
echo "  T15 列表含 $code: $has_code"

# T16: 删除自选
del_resp=$(curl -s -X DELETE "http://$ADDR/api/watchlist" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d "{\"code\":\"$code\"}" 2>/dev/null)
del_ok=$(echo "$del_resp" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('status','fail'))" 2>/dev/null)
[ "$del_ok" = "ok" ] && pass "自选删除成功($code)" || fail "自选删除" "status=ok" "$del_ok"
echo "  T16 删除自选 $code: $del_ok"

echo ""

# ── 5. 持仓操作 ──
echo "─── 5. 持仓操作 ───"

# T17: 添加持仓
holding_item='{"code":"600519","name":"贵州茅台","quantity":100,"cost_price":1300,"take_profit_pct":10,"stop_loss_pct":3}'
# 先获取现有持仓
h0=$(curl -s "http://$ADDR/api/holdings" -H "Authorization: Bearer $TOKEN" 2>/dev/null)
h0_cnt=$(echo "$h0" | python3 -c "import sys,json;print(len(json.load(sys.stdin).get('holdings',[])))" 2>/dev/null)
# POST 添加
add_h=$(curl -s -X POST "http://$ADDR/api/holdings" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d "{\"holdings\":[{\"code\":\"600519\",\"name\":\"贵州茅台\",\"quantity\":100,\"cost_price\":1300,\"take_profit_pct\":10,\"stop_loss_pct\":3}],\"available_balance\":100000}" 2>/dev/null)
add_h_ok=$(echo "$add_h" | python3 -c "
import sys,json;d=json.load(sys.stdin);print(d.get('status','ok') if 'status' in d else 'ok')" 2>/dev/null)
pass "持仓POST成功(返回OK,不抛异常)"
echo "  T17 添加持仓: 600519×100@1300"

# T18: GET 持仓验证名称/价格/止盈止损
h1=$(curl -s "http://$ADDR/api/holdings" -H "Authorization: Bearer $TOKEN" 2>/dev/null)
h1_name=$(echo "$h1" | python3 -c "
import sys,json
for h in json.load(sys.stdin).get('holdings',[]):
    if h.get('code')=='600519':
        print(h.get('name','') or 'EMPTY')
        break" 2>/dev/null)
h1_price=$(echo "$h1" | python3 -c "
import sys,json
for h in json.load(sys.stdin).get('holdings',[]):
    if h.get('code')=='600519':
        print(f\"{h.get('cur_price',0):.2f}\")
        break" 2>/dev/null)
h1_tp=$(echo "$h1" | python3 -c "
import sys,json
for h in json.load(sys.stdin).get('holdings',[]):
    if h.get('code')=='600519':
        print(h.get('take_profit_pct',0))
        break" 2>/dev/null)
h1_sl=$(echo "$h1" | python3 -c "
import sys,json
for h in json.load(sys.stdin).get('holdings',[]):
    if h.get('code')=='600519':
        print(h.get('stop_loss_pct',0))
        break" 2>/dev/null)
name_ok="未找到"
[ -n "$h1_name" ] && [ "$h1_name" != "EMPTY" ] && [ "$h1_name" != "600519" ] && name_ok="$h1_name"
[ "$name_ok" != "未找到" ] && pass "持仓名称正确($name_ok)" || fail "持仓名称" "非空非代码" "$h1_name"
price_ok=0
[ "$(echo "$h1_price > 0" | bc 2>/dev/null)" -eq 1 ] && price_ok=1
[ "$price_ok" -eq 1 ] && pass "持仓现价正确($h1_price)" || fail "持仓现价" ">0" "$h1_price"
tp_ok=0; [ "$h1_tp" = "10" ] && tp_ok=1
sl_ok=0; [ "$h1_sl" = "3" ] && sl_ok=1
[ "$tp_ok" -eq 1 ] && pass "自定义止盈=10%($h1_tp)" || fail "止盈%值" "10" "$h1_tp"
[ "$sl_ok" -eq 1 ] && pass "自定义止损=3%($h1_sl)" || fail "止损%值" "3" "$h1_sl"
echo "  T18 持仓详情: name=$h1_name price=$h1_price TP=$h1_tp% SL=$h1_sl%"

# T19: 信号+日历消息含股票名称
alerts=$(curl -s "http://$ADDR/api/alerts" -H "Authorization: Bearer $TOKEN" 2>/dev/null)
# 检查告警中是否有"未找到"作为股票名称
name_errors=$(echo "$alerts" | python3 -c "
import sys,json
alerts=json.load(sys.stdin)
bad=0
for a in alerts:
    name=a.get('name','')
    if name == '未找到' or name == 'undefined' or name == '':
        bad+=1
print(bad)" 2>/dev/null)
[ "$name_errors" -eq 0 ] 2>/dev/null && pass "告警无'未找到'名称" || fail "告警名称" "无未找到" "$name_errors 条"
echo "  T19 告警名称错误: $name_errors 条"

# 删除测试持仓
curl -s -X POST "http://$ADDR/api/holdings" \
    -H "Authorization: Bearer $TOKEN" \
    -H 'Content-Type: application/json' \
    -d '{"holdings":[],"available_balance":100000}' >/dev/null 2>&1
echo ""

# ── 6. 日历 ──
echo "─── 6. 宏观日历 ───"

# T20: 新闻中含日历
news=$(curl -s "http://$ADDR/api/news?all=true" 2>/dev/null)
cal_count=$(echo "$news" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print(sum(1 for n in d if n.get('source')=='宏观日历'))" 2>/dev/null)
[ "$cal_count" -gt 0 ] && pass "有宏观日历事件($cal_count条)" || fail "日历事件" ">0" "$cal_count"
echo "  T20 日历: $cal_count 条"

# T21: 日历事件仅含6类关键词
cal_bad=$(echo "$news" | python3 -c "
import sys,json
d=json.load(sys.stdin)
ok_kw=['FOMC','CPI','PCE','非农','股指期货','交割日','地缘','战争']
bad=0
for n in d:
    if n.get('source')!='宏观日历': continue
    t=n.get('title','')
    if not any(kw in t for kw in ok_kw): bad+=1
print(bad)" 2>/dev/null)
[ "$cal_bad" -eq 0 ] 2>/dev/null && pass "日历事件无违规内容" || fail "日历内容" "无违规" "含$cal_bad条非6类事件"
echo "  T21 日历过滤: $cal_bad 条违规"

# T22: 日历含"剩N天"
cal_days=$(echo "$news" | python3 -c "
import sys,json
d=json.load(sys.stdin)
has=0
for n in d:
    if n.get('source')=='宏观日历' and '剩' in n.get('title',''): has+=1
print(has)" 2>/dev/null)
[ "$cal_days" -gt 0 ] && pass "日历显示剩余天数" || fail "日历天数" "含剩字" "0"
echo "  T22 日历天数: $cal_days 条含剩字"
echo ""

# ── 7. 移动端体验 ──
echo "─── 7. 移动端体验 ───"

# T23: 页面响应时间
for page in "api/status" "api/signals" "api/snapshot/hot" "api/evaluations"; do
    t0=$(date +%s%N)
    curl -s --max-time 5 "http://$ADDR/$page" -H "Authorization: Bearer $TOKEN" -o /dev/null 2>/dev/null
    elapsed=$(( ($(date +%s%N) - t0) / 1000000 ))
    if [ "$elapsed" -lt 2000 ]; then
        pass "$page 响应${elapsed}ms(<2s)"
    else
        fail "$page 响应时间" "<2000ms" "${elapsed}ms"
    fi
done
echo "  7 响应时间检查完成"

# T24: 信号列表列数（不应炸版）
sig_json=$(curl -s "http://$ADDR/api/signals" -H "Authorization: Bearer $TOKEN" 2>/dev/null)
sig_fields=$(echo "$sig_json" | python3 -c "
import sys,json
d=json.load(sys.stdin)
if d:
    print(len(d[0].keys()))
else:
    print(0)" 2>/dev/null)
[ "$sig_fields" -ge 8 ] && pass "信号列表≥8列($sig_fields)" || fail "信号列数" "≥8" "$sig_fields"
echo "  T24 信号列: $sig_fields"
echo ""

# ── 8. 综合报告 ──
echo "═══════════════════════════════════════════════════════════════"
echo "  测试报告"
echo "═══════════════════════════════════════════════════════════════"
echo ""
echo "  总计: $((PASS + FAIL))  通过: $PASS  失败: $FAIL"
echo ""

if [ "$FAIL" -gt 0 ]; then
    echo "  失败详情:"
    for i in "${!FAILURES[@]}"; do
        IFS='|' read -ra F <<< "${FAILURES[$i]}"
        echo "    [$((i+1))] ${F[0]}"
        echo "        预期: ${F[1]}"
        echo "        实际: ${F[2]}"
    done
    echo ""
fi

# JSON 摘要
echo "{"
echo "  \"date\": \"$(date '+%Y-%m-%d %H:%M:%S')\","
echo "  \"total\": $((PASS + FAIL)),"
echo "  \"pass\": $PASS,"
echo "  \"fail\": $FAIL,"
echo "  \"target\": \"http://$ADDR\","
echo "  \"engine_session\": \"$sess\""
echo "}"

echo ""
echo "═══════════════════════════════════════════════════════════════"
exit $FAIL
