//go:build android && cgo

package main

/*
#include <jni.h>
#include <stdlib.h>
#include <signal.h>
#include <stdio.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#include <android/log.h>
#include <sys/mman.h>
#include <elf.h>
#include <sys/syscall.h>
#include <fcntl.h>
#include <dlfcn.h>

#define LOG_TAG "QuantGo"

static void _go_log(int prio, const char *msg) {
    __android_log_write(prio, LOG_TAG, msg);
}

// ---- VDSO workaround: SIGSEGV handler that emulates clock_gettime ----

static char vdso_handler_buf[1024];
static unsigned long vdso_page_start;
static unsigned long vdso_page_end;

// Raw syscall for clock_gettime (bypasses VDSO/libc entirely).
static long raw_clock_gettime(clockid_t clk_id, struct timespec *tp) {
    return syscall(SYS_clock_gettime, clk_id, tp);
}

static void vdso_sigsegv_handler(int signo, siginfo_t *info, void *ucontext) {
    if ((uintptr_t)info->si_addr >= vdso_page_start &&
        (uintptr_t)info->si_addr < vdso_page_end &&
        vdso_page_start != 0) {
        // VDSO crash - emulate clock_gettime via raw syscall
        ucontext_t *uc = (ucontext_t *)ucontext;
        clockid_t clk_id = (clockid_t)uc->uc_mcontext.regs[0];
        struct timespec *tp = (struct timespec *)uc->uc_mcontext.sp;
        raw_clock_gettime(clk_id, tp);
        uc->uc_mcontext.pc = uc->uc_mcontext.regs[30]; // skip to LR (return addr)
        return;
    }
    // Non-VDSO crash: write to log and terminate
    FILE *crf = fopen("/data/data/com.liangzai.quant/files/quant/native_crash.log", "a");
    if (crf) {
        fprintf(crf, "[%ld] NATIVE CRASH signal=%d addr=%p\n",
                (long)time(0), signo, info->si_addr);
        fclose(crf);
    }
    signal(signo, SIG_DFL);
    raise(signo);
}

static void install_vdso_handler(void) {
    // Find VDSO page range
    unsigned long vdso = 0;
    unsigned long buf[2];
    FILE *f = fopen("/proc/self/auxv", "rb");
    if (f) {
        while (fread(buf, sizeof(buf), 1, f) == 1) {
            if (buf[0] == AT_SYSINFO_EHDR) { vdso = buf[1]; break; }
            if (buf[0] == AT_NULL) break;
        }
        fclose(f);
    }
    if (!vdso || vdso < 0x1000) return;
    vdso_page_start = vdso & ~(unsigned long)0xfff;
    vdso_page_end = vdso_page_start + 0x2000; // VDSO typically < 2 pages

    struct sigaction sa;
    memset(&sa, 0, sizeof(sa));
    sa.sa_sigaction = vdso_sigsegv_handler;
    sa.sa_flags = SA_SIGINFO | SA_ONSTACK;
    sigaltstack(&((stack_t){.ss_sp = vdso_handler_buf, .ss_size = sizeof(vdso_handler_buf)}), NULL);
    sigaction(SIGSEGV, &sa, NULL);
}

__attribute__((constructor))
static void _init_vdso_fix(void) {
    install_vdso_handler();
}

static void crash_handler(int sig) {
    FILE *f = fopen("/data/data/com.liangzai.quant/files/quant/native_crash.log", "a");
    if (f) {
        time_t now = time(0);
        struct tm *tm = localtime(&now);
        fprintf(f, "[%04d-%02d-%02d %02d:%02d:%02d] CRASH sig=%d\n",
                1900+tm->tm_year, tm->tm_mon+1, tm->tm_mday,
                tm->tm_hour, tm->tm_min, tm->tm_sec, sig);
        fclose(f);
    }
    signal(sig, SIG_DFL);
    raise(sig);
}

static void install_handlers(void) {
    signal(SIGABRT, crash_handler);
    signal(SIGBUS, crash_handler);
    signal(SIGILL, crash_handler);
    signal(SIGFPE, crash_handler);
    // SIGSEGV handled by vdso_sigsegv_handler
}

static int check_tracer_pid() {
    char buf[256];
    FILE *f = fopen("/proc/self/status", "r");
    if (!f) return 0;
    while (fgets(buf, sizeof(buf), f)) {
        if (strncmp(buf, "TracerPid:", 10) == 0) {
            int pid = 0;
            sscanf(buf + 10, "%d", &pid);
            fclose(f);
            return pid != 0;
        }
    }
    fclose(f);
    return 0;
}

static int check_debug_flags() {
    if (check_tracer_pid()) return 1;
    // detect ptrace by trying to attach to self
    int fd = open("/proc/self/status", O_RDONLY);
    if (fd < 0) return 1;
    close(fd);
    return 0;
}

// simple integrity: compare a checksum from .so load address
static unsigned long compute_self_hash(void) {
    Dl_info info;
    unsigned long hash = 0;
    if (dladdr((void*)compute_self_hash, &info)) {
        unsigned long *p = (unsigned long*)info.dli_fbase;
        for (int i = 0; i < 64; i++) {
            hash ^= p[i] ^ (hash << 5) ^ (hash >> 3);
        }
    }
    return hash;
}

static jstring strToJString(JNIEnv *env, const char* str) {
    return (*env)->NewStringUTF(env, str);
}

static int getJavaVM(JNIEnv *env, JavaVM **vm) {
    return (*env)->GetJavaVM(env, vm);
}

static jclass findClass(JNIEnv *env, const char *name) {
    return (*env)->FindClass(env, name);
}

static jmethodID getStaticMethodID(JNIEnv *env, jclass cls, const char *name, const char *sig) {
    return (*env)->GetStaticMethodID(env, cls, name, sig);
}

static void callStaticVoidMethod(JNIEnv *env, jclass cls, jmethodID mid, jstring arg1, jstring arg2) {
    (*env)->CallStaticVoidMethod(env, cls, mid, arg1, arg2);
}

static jobject newGlobalRef(JNIEnv *env, jobject obj) {
    return (*env)->NewGlobalRef(env, obj);
}

static void deleteLocalRef(JNIEnv *env, jobject obj) {
    (*env)->DeleteLocalRef(env, obj);
}

static const char* jstring_to_c(JNIEnv *env, jstring s) {
    if (!s) return NULL;
    return (*env)->GetStringUTFChars(env, s, NULL);
}

static void jstring_free(JNIEnv *env, jstring s, const char *cstr) {
    if (cstr) (*env)->ReleaseStringUTFChars(env, s, cstr);
}

static int attachThread(JavaVM *vm, JNIEnv **env) {
    return (*vm)->AttachCurrentThread(vm, env, NULL);
}

static int detachThread(JavaVM *vm) {
    return (*vm)->DetachCurrentThread(vm);
}

static void warmup_vdso(void) {
    unsigned long vdso = 0;
    FILE *f = fopen("/proc/self/auxv", "rb");
    if (f) {
        unsigned long key, val;
        while (fread(&key, sizeof(key), 1, f) == 1 &&
               fread(&val, sizeof(val), 1, f) == 1) {
            if (key == 33) {
                vdso = val;
                break;
            }
        }
        fclose(f);
    }
    if (!vdso) return;
    for (int i = 0; i < 4096; i += 64) {
        volatile char *p = (volatile char *)(vdso + i);
        (void)*p;
    }
}
*/
import "C"
import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"time"
	"unsafe"

	"quant-trading/internal/config"
	"quant-trading/internal/data"
	"quant-trading/internal/engine"
	"quant-trading/internal/registry"
)

func init() {
	// Android 无 /etc/localtime，Go 默认 UTC，强制使用 CST (UTC+8)
	time.Local = time.FixedZone("CST", 8*3600)
}

const xorKey = "LzQt2024!Xor#Secret"

func xorDecrypt(data []byte) []byte {
	out := make([]byte, len(data))
	for i := range data {
		out[i] = data[i] ^ xorKey[i%len(xorKey)]
	}
	return out
}

func androidLog(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	cstr := C.CString(msg)
	C._go_log(C.int(4), cstr)
	C.free(unsafe.Pointer(cstr))
}

func androidLogWarn(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	cstr := C.CString(msg)
	C._go_log(C.int(5), cstr)
	C.free(unsafe.Pointer(cstr))
}

func androidLogErr(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	cstr := C.CString(msg)
	C._go_log(C.int(6), cstr)
	C.free(unsafe.Pointer(cstr))
}

var (
	globalEngine      *engine.Engine
	globalCtx         context.Context
	globalCancel      context.CancelFunc
	globalJavaVM      *C.JavaVM
	notifHelperClazz  C.jclass
	notifHelperMethod C.jmethodID
)

//go:linkname vdsoDisable runtime.vdsoClockgettimeSym
var vdsoDisable uintptr

func jstringToGo(env *C.JNIEnv, s C.jstring) string {
	if uintptr(unsafe.Pointer(s)) == 0 {
		return ""
	}
	cstr := C.jstring_to_c(env, s)
	if uintptr(unsafe.Pointer(cstr)) == 0 {
		return ""
	}
	defer C.jstring_free(env, s, cstr)
	return C.GoString(cstr)
}

//export Java_com_liangzai_quant_JNIBridge_startServer
func Java_com_liangzai_quant_JNIBridge_startServer(
	env *C.JNIEnv, cls C.jobject, configPathC C.jstring, listenAddrC C.jstring) {

	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 16384)
			n := runtime.Stack(buf, true)
			androidLogErr("GO PANIC: %v\n%s", r, buf[:n])
		}
	}()

	androidLog("=== Go engine JNI start ===")
	vdsoDisable = 0 // Android 16 VDSO workaround: force raw syscall for clock_gettime
	C.install_handlers()
	debug.SetPanicOnFault(true)

	configPath := jstringToGo(env, configPathC)
	listenAddr := jstringToGo(env, listenAddrC)
	cfgDir := filepath.Dir(configPath)

	androidLog("configPath=%s listenAddr=%s cfgDir=%s", configPath, listenAddr, cfgDir)

	logFile, err := os.Create(filepath.Join(cfgDir, "backend.log"))
	if err != nil {
		androidLogWarn("cannot create log file: %v", err)
	} else {
		log.SetOutput(io.MultiWriter(logFile, os.Stderr))
	}

	if int(C.check_debug_flags()) != 0 {
		androidLogWarn("tracer detected, exiting")
		return
	}

	if err := os.Chdir(cfgDir); err != nil {
		androidLogWarn("chdir %s: %v", cfgDir, err)
	}

	androidLog("loading config...")
	cfgMgr := config.NewManager(configPath)
	if err := cfgMgr.Load(); err != nil {
		androidLogErr("config load failed: %v", err)
		return
	}
	androidLog("config loaded OK")

	// apply trade time config from rules.json
	cfg := cfgMgr.Get()
	if cfg.TradeTime.TradeOpen > 0 {
		data.ApplyConfig(data.TradeTimeConfig{
			TradeOpen:      cfg.TradeTime.TradeOpen,
			TradeClose:     cfg.TradeTime.TradeClose,
			FullOpen:       cfg.TradeTime.FullOpen,
			FullClose:      cfg.TradeTime.FullClose,
			PreOpenStart:   cfg.TradeTime.PreOpenStart,
			PreOpenEnd:     cfg.TradeTime.PreOpenEnd,
			MorningHighEnd: cfg.TradeTime.MorningHighEnd,
			MidFreqStart:   cfg.TradeTime.MidFreqStart,
			AfternoonStart: cfg.TradeTime.AfternoonStart,
			AfternoonEnd:   cfg.TradeTime.AfternoonEnd,
		})
		androidLog("交易时段配置已应用")
	}

	// self-integrity check
	selfHash := uint64(C.compute_self_hash())
	androidLog("self hash: %016x", selfHash)

	var vm *C.JavaVM
	if ret := C.getJavaVM(env, &vm); int(ret) == 0 {
		globalJavaVM = vm
	}
	// 缓存 NotificationHelper class + method（从 Java 线程调用 FindClass 才能找到应用类）
	notifCls := C.findClass(env, C.CString("com/liangzai/quant/NotificationHelper"))
	if uintptr(notifCls) != 0 {
		notifHelperClazz = C.jclass(C.newGlobalRef(env, C.jobject(notifCls)))
		notifHelperMethod = C.getStaticMethodID(env, notifCls, C.CString("pushNotification"),
			C.CString("(Ljava/lang/String;Ljava/lang/String;)V"))
		androidLog("NotificationHelper JNI 缓存成功")
	} else {
		androidLogWarn("找不到 NotificationHelper 类，通知不可用")
	}

	engine.NotifyAndroid = pushNotification

	// ── 凭证读取（与桌面版一致：env → secrets.json → XOR解密）──
	tsToken := os.Getenv("TUSHARE_TOKEN")
	llmToken := os.Getenv("LLM_API_KEY")
	jqMobile := os.Getenv("JQ_MOBILE")
	jqPassword := os.Getenv("JQ_PASSWORD")
	if tsToken == "" || llmToken == "" {
		var sec struct {
			Tushare    string `json:"tushare_token"`
			LlmToken   string `json:"llm_token"`
			JQMobile   string `json:"jq_mobile"`
			JQPassword string `json:"jq_password"`
		}
		secPath := filepath.Join(cfgDir, "secrets.json")
		if data, err := os.ReadFile(secPath); err == nil {
			if err := json.Unmarshal(data, &sec); err == nil {
				if tsToken == "" && sec.Tushare != "" {
					tsToken = sec.Tushare
				}
				if llmToken == "" && sec.LlmToken != "" {
					llmToken = sec.LlmToken
				}
				if jqMobile == "" && sec.JQMobile != "" {
					jqMobile = sec.JQMobile
				}
				if jqPassword == "" && sec.JQPassword != "" {
					jqPassword = sec.JQPassword
				}
			}
		}
	}
	// 注入 JQ 凭证供 DataCoordinatorAdapter 读取
	if jqMobile != "" {
		os.Setenv("JQ_MOBILE", jqMobile)
	}
	if jqPassword != "" {
		os.Setenv("JQ_PASSWORD", jqPassword)
	}
	// XOR解密 Tushare token
	if decoded, err := base64.StdEncoding.DecodeString(tsToken); err == nil {
		plain := xorDecrypt(decoded)
		if len(plain) > 0 && plain[0] != 0 {
			tsToken = string(plain)
		}
	}
	// 配置热加载
	if err := cfgMgr.Watch(); err != nil {
		androidLogWarn("config watch not available: %v", err)
	}

	ctx := context.Background()
	reg := registry.New()
	registry.RegisterAll(reg, &registry.Params{
		Cfg:     cfgMgr,
		TsToken: tsToken,
		LlmKey:  llmToken,
	})

	androidLog("critical services...")
	if err := reg.StartCritical(ctx); err != nil {
		androidLogErr("critical: %v", err)
		return
	}
	androidLog("critical OK")

	androidLog("business services...")
	if err := reg.StartBusiness(ctx, 4); err != nil {
		androidLogWarn("business partial: %v", err)
	}
	androidLog("business OK")

	globalEngine = engine.NewFromRegistry(cfgMgr, reg)
	if globalEngine == nil {
		androidLogErr("engine is nil")
		return
	}

	h5Dir := filepath.Join(cfgDir, "www")
	if fi, err := os.Stat(h5Dir); err == nil && fi.IsDir() {
		globalEngine.SetH5FS(http.Dir(h5Dir))
		androidLog("h5 FS from %s", h5Dir)
	} else {
		androidLogWarn("h5 dir %s not found: %v", h5Dir, err)
	}

	globalCtx, globalCancel = context.WithCancel(context.Background())

	httpReady := make(chan error, 1)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				androidLogErr("ENGINE RUN PANIC: %v\n%s", r, debug.Stack())
			}
		}()
		androidLog("engine.Run(%s) starting...", listenAddr)
		if err := globalEngine.Run(globalCtx, listenAddr); err != nil {
			androidLogErr("engine.Run exit: %v", err)
		}
	}()

	go func() {
		for i := 0; i < 50; i++ {
			conn, err := net.DialTimeout("tcp", listenAddr, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				httpReady <- nil
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		httpReady <- fmt.Errorf("HTTP server did not start within 5s")
	}()

	select {
	case err := <-httpReady:
		if err != nil {
			androidLogErr("HTTP server wait: %v", err)
		} else {
			androidLog("HTTP server ready on %s", listenAddr)
		}
	case <-time.After(6 * time.Second):
		androidLogErr("timeout waiting for HTTP server")
	}

	androidLog("=== Go engine started OK ===")
}

//export Java_com_liangzai_quant_JNIBridge_stopServer
func Java_com_liangzai_quant_JNIBridge_stopServer() {
	if globalCancel != nil {
		globalCancel()
	}
	log.Println("=== engine stopped ===")
}

func pushNotification(title, body string) {
	vm := globalJavaVM
	if vm == nil || uintptr(notifHelperClazz) == 0 {
		return
	}

	var env *C.JNIEnv
	switch int(C.attachThread(vm, &env)) {
	case int(C.JNI_OK):
	case int(C.JNI_EDETACHED):
		if int(C.attachThread(vm, &env)) != int(C.JNI_OK) {
			return
		}
	default:
		return
	}
	defer C.detachThread(vm)

	titleJ := C.strToJString(env, C.CString(title))
	bodyJ := C.strToJString(env, C.CString(body))
	C.callStaticVoidMethod(env, notifHelperClazz, notifHelperMethod, titleJ, bodyJ)
	C.deleteLocalRef(env, C.jobject(titleJ))
	C.deleteLocalRef(env, C.jobject(bodyJ))
}

func main() {}
