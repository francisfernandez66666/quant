#!/bin/bash
# start_e2e_test.sh — 启动全天端到端测试
# 1. 设置 ADB forward 连接手机 APK
# 2. 启动桌面后端作为备份
# 3. 启动测试脚本

DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIR"
LOG_DIR="tests"
mkdir -p "$LOG_DIR"

echo "===== $(date) 启动端到端测试 ====="
echo ""

# 1. ADB forward（如果有手机）
echo "==> ADB forward..."
if adb devices 2>/dev/null | grep -q "device$"; then
    adb forward tcp:8080 tcp:8080 2>/dev/null || true
    echo "   手机 adb forward: localhost:8080 → 手机:8080"
else
    echo "   未检测到 adb 设备"
fi

# 2. 启动桌面后端
echo "==> 启动桌面后端..."
pkill -f "quant --listen" 2>/dev/null || true
sleep 1

DESKTOP_PORT=8081
nohup ./quant --listen :$DESKTOP_PORT --h5-dir desktop/dist &>/tmp/quant_e2e.log &
echo "   桌面后端 PID=$! 端口=$DESKTOP_PORT"

# 3. 等待服务就绪
echo "==> 等待服务..."
for i in $(seq 1 10); do
    # 先试 ADB（手机端）
    if curl -s --max-time 2 http://127.0.0.1:8080/api/health 2>/dev/null | grep -q "ok"; then
        echo "   手机端服务就绪 (localhost:8080)"
        export TEST_ADDR="127.0.0.1:8080"
        break
    fi
    # 再试桌面端
    if curl -s --max-time 2 http://127.0.0.1:$DESKTOP_PORT/api/health 2>/dev/null | grep -q "ok"; then
        echo "   桌面端服务就绪 (localhost:$DESKTOP_PORT)"
        export TEST_ADDR="127.0.0.1:$DESKTOP_PORT"
        break
    fi
    sleep 1
done

# 4. 启动测试
echo ""
echo "==> 启动测试脚本..."
echo "    目标: $TEST_ADDR"
echo "    日 志: $LOG_DIR/test_day.log"
echo "    失 败: $LOG_DIR/failures_$(date +%Y%m%d).log"
echo ""

nohup bash "$DIR/tests/e2e_full_day.sh" &>"$LOG_DIR/test_day.log" &
TEST_PID=$!
echo "   测试脚本 PID=$TEST_PID"
echo ""

echo "===== 测试已启动 ====="
echo "查看日志: tail -f $LOG_DIR/test_day.log"
echo "查看失败: tail -f $LOG_DIR/failures_$(date +%Y%m%d).log"
echo "停止测试: kill $TEST_PID"
