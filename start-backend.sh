#!/bin/bash
# start-backend.sh — 仅启动后端（信号扫描 + HTTP API）
# 桌面版前端通过 start-desktop.sh 单独启动（开发模式）或通过 start.sh 一键启动
set -e

DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR"

echo "==> 编译后端..."
go build -o quant ./cmd/quant/

echo "==> 启动后端 (端口 8080)"
echo "    API:  http://localhost:8080/api"
echo "    前端: http://localhost:8080（嵌入版）"
echo ""

# 使用嵌入的 H5 前端（不需要 desktop/dist）
./quant --listen :8080
