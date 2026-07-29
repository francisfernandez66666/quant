// Package security 安全辅助模块，提供反调试、Root/模拟器检测、完整性校验和 XOR 混淆等功能。
// 阻断逻辑仅在 ProductionMode = "true" 时生效，Android 上仅 log 不阻断。
package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	integrityMu sync.RWMutex
	passCount   int
)

func init() {
	// Android 上仅 log，不阻断 — 加固已有量产崩溃
	if strings.HasPrefix(runtime.GOOS, "android") {
		if detectPtrace() {
			log.Printf("sec: ptrace detected during init (non-blocking)")
		}
	}
}

// ----- ptrace / debugger -----

// detectPtrace 检测是否被 ptrace 附加调试。
func detectPtrace() bool {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "TracerPid:") {
			pid, _ := strconv.Atoi(strings.TrimSpace(line[10:]))
			return pid != 0
		}
	}
	return false
}

// detectFrida 检测 Frida 动态插桩工具是否存在。
func detectFrida() bool {
	paths := []string{
		"/data/local/tmp/frida-server",
		"/data/local/tmp/re.frida.server",
		"/sbin/frida-server",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// detectXposed 检测 Xposed 框架是否存在。
func detectXposed() bool {
	paths := []string{
		"/system/lib/libxposed_art.so",
		"/system/lib64/libxposed_art.so",
		"/data/data/de.robv.android.xposed.installer",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// detectSubstrate 检测 Cydia Substrate 框架是否存在。
func detectSubstrate() bool {
	paths := []string{
		"/data/data/com.saurik.substrate",
		"/system/lib/libsubstrate.so",
		"/system/lib/libsubstrated.so",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// detectEmulator 通过 build.prop 检测是否运行在模拟器中。
func detectEmulator() (found bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("sec: detectEmulator recover: %v", r)
			found = false
		}
	}()
	// 只读文件，不 exec（Android 上 exec.Command 会导致 SIGSEGV）
	data, err := os.ReadFile("/system/build.prop")
	if err != nil {
		return false
	}
	content := strings.ToLower(string(data))
	indicators := []string{"generic", "sdk_", "ro.kernel.qemu=1"}
	for _, ind := range indicators {
		if strings.Contains(content, ind) {
			return true
		}
	}
	return false
}

// timingCheck 通过耗时计算检测是否存在模拟器/调试器导致的执行延迟异常。
func timingCheck() bool {
	start := time.Now()
	sum := 0
	for i := 0; i < 1000000; i++ {
		sum += i
	}
	elapsed := time.Since(start)
	_ = sum
	// Under emulator/debugger, this loop takes significantly longer
	return elapsed < 50*time.Millisecond
}

// ----- public API -----

// ProductionMode 由 ldflags -X 注入："true"=阻断模式，检测到风险直接退出。
var ProductionMode string

// AntiDebug 执行全套反调试/反注入检查，并在后台每 30 秒持续巡检。
func AntiDebug() {
	if !strings.HasPrefix(runtime.GOOS, "android") {
		return
	}
	if detectPtrace() || detectFrida() || detectXposed() || detectSubstrate() {
		msg := "sec: debug/hook detected"
		if ProductionMode == "true" {
			log.Fatalf("FATAL: %s — 退出", msg)
		}
		log.Printf("%s (non-blocking)", msg)
	}
	if detectEmulator() {
		msg := "sec: emulator detected"
		if ProductionMode == "true" {
			log.Fatalf("FATAL: %s — 退出", msg)
		}
		log.Printf("%s (non-blocking)", msg)
	}
	if !timingCheck() {
		msg := "sec: timing anomaly (emulator/debugger)"
		if ProductionMode == "true" {
			log.Fatalf("FATAL: %s — 退出", msg)
		}
		log.Printf("%s (non-blocking)", msg)
	}
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if detectPtrace() || detectFrida() || detectXposed() || detectSubstrate() {
				msg := "sec: runtime tamper detected"
				if ProductionMode == "true" {
					log.Fatalf("FATAL: %s — 退出", msg)
				}
				log.Printf("%s (non-blocking)", msg)
			}
			integrityMu.RLock()
			pc := passCount
			integrityMu.RUnlock()
			if pc > 3 {
				msg := "sec: integrity failure"
				if ProductionMode == "true" {
					log.Fatalf("FATAL: %s — 退出", msg)
				}
				log.Printf("%s (non-blocking)", msg)
			}
		}
	}()
}

// IntegrityPass 标记完整性检查通过，重置失败计数。
func IntegrityPass() {
	integrityMu.Lock()
	passCount = 0
	integrityMu.Unlock()
}

// IntegrityFail 标记完整性检查失败，递增失败计数，超过 3 次触发告警。
func IntegrityFail() {
	integrityMu.Lock()
	passCount++
	integrityMu.Unlock()
}

// SelfCheck 使用 HMAC-SHA256 校验数据完整性，expectHash 为空时跳过检查。
func SelfCheck(data []byte, expectHash string) error {
	if expectHash == "" {
		return nil
	}
	mac := hmac.New(sha256.New, []byte("quant-integrity-key"))
	mac.Write(data)
	got := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(got), []byte(expectHash)) {
		return fmt.Errorf("integrity mismatch")
	}
	return nil
}

func logExit(msg string) {
	// Write to stderr — on Android this goes to logcat
	fmt.Fprintln(os.Stderr, msg)
}

// ----- XOR obfuscation -----

// Obfuscate 使用单字节密钥对数据进行 XOR 混淆。
func Obfuscate(data []byte, key byte) []byte {
	out := make([]byte, len(data))
	for i, b := range data {
		out[i] = b ^ key
	}
	return out
}

// Deobfuscate 使用单字节密钥对数据进行 XOR 反混淆。
func Deobfuscate(data []byte, key byte) string {
	return string(Obfuscate(data, key))
}

// ErrSig 完整性校验失败的标准错误。
var ErrSig = fmt.Errorf("integrity check failed")
