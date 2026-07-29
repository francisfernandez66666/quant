#!/bin/bash
# start-desktop.sh — 启动量仔期货桌面前端（开发模式，带热更新）
set -e

DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR/desktop"

if [ ! -d node_modules ]; then
  echo "==> 安装依赖..."
  npm install
fi

echo "==> 启动桌面前端 (开发模式)"
echo "    代理后端: http://localhost:8080"
echo "    访问地址: http://localhost:5173"
echo ""
echo "    如果后端不在本机 8080 端口，"
echo "    请在 Settings 页面修改服务器地址"
echo ""

npm run dev
