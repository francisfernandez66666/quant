#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
export JAVA_HOME=/opt/homebrew/opt/openjdk@17
export ANDROID_HOME="$HOME/Library/Android/sdk"
export PATH="$JAVA_HOME/bin:$ANDROID_HOME/cmdline-tools/latest/bin:$ANDROID_HOME/platform-tools:$PATH"

NDK="$ANDROID_HOME/ndk/25.2.9519653"
CC="$NDK/toolchains/llvm/prebuilt/darwin-x86_64/bin/aarch64-linux-android30-clang"
JNILIBS="$ROOT/app/src/main/jniLibs/arm64-v8a"

echo "=== 1/3 编译 Go 共享库 (Android ARM64) ==="
cd "$ROOT/.."
mkdir -p "$JNILIBS"
CGO_ENABLED=1 \
GOOS=android GOARCH=arm64 \
CC="$CC" \
  go build -buildmode=c-shared -trimpath \
    -ldflags="-s -w" \
    -o "$JNILIBS/libquant.so" \
    ./android/ 2>&1
echo "  完成: $(ls -lh "$JNILIBS/libquant.so" | awk '{print $5}')"

echo "=== 2/3 复制配置 ==="
mkdir -p "$ROOT/app/src/main/assets"
cp config/rules.json "$ROOT/app/src/main/assets/rules.json" 2>/dev/null || true
cp config/events_leftside.yaml "$ROOT/app/src/main/assets/events_leftside.yaml" 2>/dev/null || true

echo "=== 3/3 Gradle 编译 APK ==="
cd "$ROOT"
./gradlew assembleDebug --no-daemon 2>&1 | tail -10

APK=$(find "$ROOT/app/build/outputs/apk" -name "*.apk" 2>/dev/null | head -1)
if [ -f "$APK" ]; then
  echo ""
  echo "========================================"
  echo "✅ APK: $APK"
  echo "   大小: $(ls -lh "$APK" | awk '{print $5}')"
  echo "========================================"
  echo ""
  echo "安装到已连接的手机: adb install -r \"$APK\""
fi
