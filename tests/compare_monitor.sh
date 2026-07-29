#!/bin/bash
# compare_monitor.sh — 原版(8080) vs 修复版(8082) 对比监控
# 运行到收盘(15:00)自动汇总

ORIG="127.0.0.1:8080"
FIX="127.0.0.1:8082"
CSV="tests/compare_$(date +%Y%m%d).csv"
FAIL_LOG="tests/compare_failures_$(date +%Y%m%d).log"
TOKEN_O=""
TOKEN_F=""
> "$CSV"
> "$FAIL_LOG"

echo "time,orig_last_scan,orig_signals,orig_raw,orig_final,orig_hot,fix_last_scan,fix_signals,fix_raw,fix_final,fix_hot,fix_n_above60,fix_dragon_above0" > "$CSV"

now_str() { date '+%Y-%m-%d %H:%M:%S'; }
log()  { echo "$(now_str) $1"; }
fail() { echo "$(now_str) FAIL $1" >> "$FAIL_LOG"; echo "$(now_str) FAIL $1"; }

do_login() {
    local addr=$1
    TOKEN=$(curl -s --max-time 3 -X POST "http://$addr/api/auth/login" \
        -H 'Content-Type: application/json' \
        -d '{"username":"liangzai","password":"123456"}' 2>/dev/null | \
        python3 -c "import sys,json;print(json.load(sys.stdin).get('token',''))" 2>/dev/null)
    echo "$TOKEN"
}

do_login_o() { TOKEN_O=$(do_login "$ORIG"); }
do_login_f() { TOKEN_F=$(do_login "$FIX"); }

do_login_o && do_login_f

cycle=0
while true; do
    cycle=$((cycle + 1))
    TS=$(now_str)

    # ── 原版(8080) ──
    s_o=$(curl -s --max-time 5 "http://$ORIG/api/status" 2>/dev/null)
    ls_o=$(echo "$s_o" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('last_scan',''))" 2>/dev/null)
    sig_o=$(echo "$s_o" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('signal_count',0))" 2>/dev/null)
    ss_o=$(echo "$s_o" | python3 -c "
import sys,json;d=json.load(sys.stdin);ss=d.get('scan_stats',{})
print(f'{ss.get(\"raw_signals\",0)},{ss.get(\"final_signals\",0)},{ss.get(\"hot_sector_count\",0)}')" 2>/dev/null)
    raw_o=$(echo "$ss_o" | cut -d, -f1)
    fin_o=$(echo "$ss_o" | cut -d, -f2)
    hot_o=$(echo "$ss_o" | cut -d, -f3)

    # ── 修复版(8082) ──
    s_f=$(curl -s --max-time 5 "http://$FIX/api/status" 2>/dev/null)
    ls_f=$(echo "$s_f" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('last_scan',''))" 2>/dev/null)
    sig_f=$(echo "$s_f" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('signal_count',0))" 2>/dev/null)
    ss_f=$(echo "$s_f" | python3 -c "
import sys,json;d=json.load(sys.stdin);ss=d.get('scan_stats',{})
print(f'{ss.get(\"raw_signals\",0)},{ss.get(\"final_signals\",0)},{ss.get(\"hot_sector_count\",0)}')" 2>/dev/null)
    raw_f=$(echo "$ss_f" | cut -d, -f1)
    fin_f=$(echo "$ss_f" | cut -d, -f2)
    hot_f=$(echo "$ss_f" | cut -d, -f3)

    # 修复版评估详情
    ev_f=$(curl -s --max-time 5 "http://$FIX/api/evaluations" -H "Authorization: Bearer $TOKEN_F" 2>/dev/null)
    n60=$(echo "$ev_f" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('n_score',0)>=60))" 2>/dev/null)
    dr0=$(echo "$ev_f" | python3 -c "import sys,json;d=json.load(sys.stdin);print(sum(1 for x in d if x.get('dragon_score',0)>0))" 2>/dev/null)

    echo "$TS,$ls_o,${sig_o:-0},${raw_o:-0},${fin_o:-0},${hot_o:-0},$ls_f,${sig_f:-0},${raw_f:-0},${fin_f:-0},${hot_f:-0},${n60:-0},${dr0:-0}" >> "$CSV"

    # 异常检测
    if [ "${raw_o:-0}" -gt 0 ] && [ "${fin_o:-0}" -eq 0 ] && [ "${raw_o:-0}" != "0" ]; then
        fail "原版: raw=${raw_o} final=0 — 全被过滤"
    fi
    if [ "${raw_f:-0}" -gt 0 ] && [ "${fin_f:-0}" -eq 0 ] && [ "${raw_f:-0}" != "0" ]; then
        fail "修复版: raw=${raw_f} final=0 — 全被过滤"
    fi

    # 阶段性输出
    if [ $((cycle % 5)) -eq 0 ]; then
        log "C$cycle orig: scan=$ls_o sig=${sig_o} hot=${hot_o} | fix: scan=$ls_f sig=${sig_f} hot=${hot_f} N≥60=${n60} dragon=${dr0}"
    fi

    # 动态间隔
    NM=$(date +%H%M | sed 's/^0*//')
    if [ "$NM" -ge 915 ] && [ "$NM" -lt 1500 ]; then sleep 60; else sleep 300; fi
done
