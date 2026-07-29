#!/bin/bash
set -euo pipefail

export JAVA_HOME=/opt/homebrew/opt/openjdk@17
export ANDROID_HOME=$HOME/Library/Android/sdk
export PATH=$JAVA_HOME/bin:$ANDROID_HOME/build-tools/35.0.0:$ANDROID_HOME/platform-tools:$PATH

VERSION="2.0.0"
PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
APK_DIR="$PROJECT_DIR"
OUTPUT_DIR="$PROJECT_DIR/build"
KEYSTORE="$PROJECT_DIR/debug.keystore"
KEYALIAS="debug"
STOREPASS="android"

PLATFORM="android-35"
BUILD_TOOLS="35.0.0"

echo "==> Liangzai v${VERSION} APK build"

# 1) Build Go shared library
echo "==> Go -> libquant.so ..."
JNIDIR="$APK_DIR/app/src/main/jniLibs/arm64-v8a"
mkdir -p "$JNIDIR"
NDK="$ANDROID_HOME/ndk/25.2.9519653"
CC="$NDK/toolchains/llvm/prebuilt/darwin-x86_64/bin/aarch64-linux-android30-clang"
CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC="$CC" \
  go build -tags android -buildmode=c-shared \
    -ldflags="-s -w" \
    -trimpath \
    -o "$JNIDIR/libquant.so" \
    "$PROJECT_DIR/../android/"
echo "==> libquant.so $(ls -lh "$JNIDIR/libquant.so" | awk '{print $5}')"

# 2) Debug keystore
if [ ! -f "$KEYSTORE" ]; then
    echo "==> creating debug keystore..."
    keytool -genkey -v -keystore "$KEYSTORE" -alias "$KEYALIAS" \
        -keyalg RSA -keysize 2048 -validity 10000 \
        -storepass "$STOREPASS" -keypass "$STOREPASS" \
        -dname "CN=Debug, OU=Debug, O=Debug, L=Debug, ST=Debug, C=US" 2>/dev/null
fi

# 3) Build H5 frontend + prepare assets
echo "==> building H5..."
H5_SRC=""
# APK 优先用 web/ 前端（hash路由+移动端布局）
for dir in "$PROJECT_DIR/../web" "$PROJECT_DIR/../desktop"; do
    if [ -d "$dir" ] && [ -f "$dir/package.json" ]; then
        (cd "$dir" && npm run build 2>&1) && H5_SRC="$dir/dist" && break
    fi
done
if [ -z "$H5_SRC" ] || [ ! -f "$H5_SRC/index.html" ]; then
    echo "ERROR: no H5 build found"
    exit 1
fi

ASSETS_DIR="$APK_DIR/app/src/main/assets"
rm -rf "$ASSETS_DIR"
mkdir -p "$ASSETS_DIR/www"
cp -r "$H5_SRC/"* "$ASSETS_DIR/www/"
echo "==> H5 -> assets/www/"
cp "$PROJECT_DIR/../config/rules.json" "$ASSETS_DIR/" 2>/dev/null || true
cp "$PROJECT_DIR/../config/events_leftside.yaml" "$ASSETS_DIR/" 2>/dev/null || true

# 生成5个14天过期测试账号并注入 assets/rules.json
TEST_ACCTS=$(cd "$PROJECT_DIR/.." && go run ./cmd/genaccount -n 5 -days 14 -append "$ASSETS_DIR/rules.json" 2>&1)
echo "==> 测试账号已注入，详情保存到 $ASSETS_DIR/../test_accounts.txt"
echo "$TEST_ACCTS" > "$ASSETS_DIR/../test_accounts.txt"
echo "$TEST_ACCTS" | head -15
echo "==> assets ready"

# 4) Prepare gen/obj/dex dirs
GEN="$APK_DIR/app/gen"
OBJ="$GEN/obj"
DEX="$GEN/dex"
mkdir -p "$OBJ" "$DEX"

# 5) Compile R.java
echo "==> compiling R.java..."
aapt package -f -m -J "$GEN" \
    -M "$APK_DIR/app/src/main/AndroidManifest.xml" \
    -S "$APK_DIR/app/src/main/res" \
    -I "$ANDROID_HOME/platforms/$PLATFORM/android.jar"

# 6) Compile Java sources
echo "==> compiling Java..."
javac -source 17 -target 17 \
    -d "$OBJ" \
    -classpath "$ANDROID_HOME/platforms/$PLATFORM/android.jar:$GEN" \
    "$APK_DIR/app/src/main/java/com/liangzai/quant/"*.java

# 7) D8 -> dex
echo "==> D8 -> dex..."
d8 --lib "$ANDROID_HOME/platforms/$PLATFORM/android.jar" \
    --output "$DEX" \
    --min-api 26 \
    $(find "$OBJ" -name "*.class")

# 8) Package unsigned APK
echo "==> packaging APK..."
mkdir -p "$OUTPUT_DIR"
aapt package -f \
    -M "$APK_DIR/app/src/main/AndroidManifest.xml" \
    -S "$APK_DIR/app/src/main/res" \
    -A "$APK_DIR/app/src/main/assets" \
    -I "$ANDROID_HOME/platforms/$PLATFORM/android.jar" \
    -F "$OUTPUT_DIR/Liangzai_unsigned.apk" \
    "$DEX"

# 9) Add native .so
if [ -f "$JNIDIR/libquant.so" ]; then
    echo "==> adding libquant.so..."
    mkdir -p /tmp/apklib/lib/arm64-v8a
    cp "$JNIDIR/libquant.so" /tmp/apklib/lib/arm64-v8a/
    (cd /tmp/apklib && aapt add "$OUTPUT_DIR/Liangzai_unsigned.apk" lib/arm64-v8a/libquant.so)
    rm -rf /tmp/apklib
fi

# 10) Align
echo "==> zipalign..."
zipalign -f 4 "$OUTPUT_DIR/Liangzai_unsigned.apk" "$OUTPUT_DIR/Liangzai_aligned.apk"

# 11) Sign
echo "==> signing..."
apksigner sign --ks "$KEYSTORE" --ks-key-alias "$KEYALIAS" \
    --ks-pass pass:"$STOREPASS" --key-pass pass:"$STOREPASS" \
    --v1-signing-enabled true \
    --v2-signing-enabled true \
    --out "$OUTPUT_DIR/Liangzai_v${VERSION}.apk" \
    "$OUTPUT_DIR/Liangzai_aligned.apk"

# 12) Clean up
rm -f "$OUTPUT_DIR/Liangzai_unsigned.apk" "$OUTPUT_DIR/Liangzai_aligned.apk"
rm -rf "$GEN" "$APK_DIR/app/src/main/assets"

echo ""
echo "==> APK ready"
ls -lh "$OUTPUT_DIR/Liangzai_v${VERSION}.apk"
