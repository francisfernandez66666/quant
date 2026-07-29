<template>
  <div class="app" v-if="loggedIn">
    <!-- 顶部状态栏 -->
    <header class="topbar">
      <span class="hamburger" @click="menuOpen = !menuOpen">☰</span>
      <span class="logo">量仔期货</span>
      <span class="session-tag" v-if="inTradeTime !== null">
        {{ inTradeTime ? '🟢 交易中' : '🔴 盘前/盘后' }}
      </span>
    </header>

    <!-- 汉堡菜单遮罩 -->
    <div class="menu-overlay" v-if="menuOpen" @click="menuOpen = false"></div>
    <nav class="menu-drawer" :class="{ open: menuOpen }">
      <div class="menu-header">
        <span class="menu-title">导航</span>
        <span class="menu-online" :class="{ on: serverOnline }">{{ serverOnline ? '服务在线' : '离线' }}</span>
      </div>
      <router-link to="/dashboard" class="menu-item" @click="menuOpen = false">
        <span class="menu-icon">📊</span> 仪表盘
      </router-link>
      <router-link to="/signals" class="menu-item" @click="menuOpen = false">
        <span class="menu-icon">⚡</span> 信号
        <span class="badge" v-if="signalCount">{{ signalCount }}</span>
      </router-link>
      <router-link to="/watchlist" class="menu-item" @click="menuOpen = false">
        <span class="menu-icon">👁</span> 自选
      </router-link>
      <router-link to="/hotspot" class="menu-item" @click="menuOpen = false">
        <span class="menu-icon">🔥</span> 热点
      </router-link>
      <router-link to="/msgcenter" class="menu-item" @click="menuOpen = false">
        <span class="menu-icon">💬</span> 消息
        <span class="badge" v-if="alertCount">{{ alertCount }}</span>
      </router-link>
      <router-link to="/positions" class="menu-item" @click="menuOpen = false">
        <span class="menu-icon">💼</span> 持仓
      </router-link>
      <div class="menu-divider"></div>
      <router-link to="/settings" class="menu-item" @click="menuOpen = false">
        <span class="menu-icon">⚙️</span> 设置
      </router-link>
      <div class="menu-footer">
        <button class="menu-btn" @click="handleNotifyTest; menuOpen = false">🔔 测试通知</button>
        <button class="menu-btn logout" @click="logout; menuOpen = false">退出</button>
      </div>
    </nav>

    <!-- 主内容区 -->
    <main class="main-content">
      <router-view />
    </main>

    <!-- 底部标签栏 -->
    <nav class="tabbar">
      <router-link to="/dashboard" class="tab-item" active-class="tab-active">
        <span class="tab-icon">📊</span>
        <span class="tab-label">仪表盘</span>
      </router-link>
      <router-link to="/signals" class="tab-item" active-class="tab-active">
        <span class="tab-icon">⚡</span>
        <span class="tab-label">信号</span>
      </router-link>
      <router-link to="/watchlist" class="tab-item" active-class="tab-active">
        <span class="tab-icon">👁</span>
        <span class="tab-label">自选</span>
      </router-link>
      <router-link to="/hotspot" class="tab-item" active-class="tab-active">
        <span class="tab-icon">🔥</span>
        <span class="tab-label">热点</span>
      </router-link>
      <router-link to="/positions" class="tab-item" active-class="tab-active">
        <span class="tab-icon">💼</span>
        <span class="tab-label">持仓</span>
      </router-link>
    </nav>

    <!-- Toast -->
    <div class="toast-container">
      <div v-for="(t, i) in toasts" :key="i" :class="['toast', t.type]">{{ t.msg }}</div>
    </div>
  </div>

  <!-- 登录页 -->
  <div class="app login-page" v-else>
    <div class="login-box">
      <h1>量仔期货</h1>
      <p class="subtitle">炒股小助手</p>
      <div class="form-group">
        <label>服务器地址</label>
        <input v-model="serverUrl" placeholder="http://localhost:8080" />
      </div>
      <div class="form-group">
        <label>账号</label>
        <input v-model="username" placeholder="输入账号" />
      </div>
      <div class="form-group">
        <label>密码</label>
        <input v-model="password" type="password" placeholder="输入密码" @keyup.enter="handleLogin" />
      </div>
      <button class="btn-login" @click="handleLogin" :disabled="logging">
        {{ logging ? '登录中...' : '登录' }}
      </button>
      <p class="login-error" v-if="loginError">{{ loginError }}</p>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import * as api from './api/index.js'

const router = useRouter()
const loggedIn = ref(false)
const account = ref('')
const serverOnline = ref(false)
const inTradeTime = ref(null)
const signalCount = ref(0)
const alertCount = ref(0)
const toasts = ref([])
const menuOpen = ref(false)

const serverUrl = ref(api.getStoredServer() || 'http://localhost:8080')
const username = ref('')
const password = ref('')
const logging = ref(false)
const loginError = ref('')

let statusTimer = null
let unsubSSE = null

function addToast(msg, type = 'info') {
  toasts.value.push({ msg, type })
  setTimeout(() => { toasts.value.shift() }, 3000)
}

function testNotify() {
  api.testNotification()
  addToast('通知测试已发送', 'success')
}

function handleNotifyTest() {
  menuOpen.value = false
  setTimeout(() => testNotify(), 200)
}

async function checkAuth() {
  if (api.isLoggedIn()) {
    loggedIn.value = true
    account.value = api.getAccount()
    api.setStoredServer(serverUrl.value)
    return true
  }
  loggedIn.value = false
  return false
}

async function handleLogin() {
  logging.value = true
  loginError.value = ''
  api.setStoredServer(serverUrl.value)
  try {
    await api.login(username.value, password.value)
    account.value = api.getAccount()
    loggedIn.value = true
    startPolling()
    addToast('登录成功', 'success')
    if ('Notification' in window && Notification.permission === 'default') {
      Notification.requestPermission()
    }
  } catch (e) {
    loginError.value = e.message || '登录失败'
  } finally {
    logging.value = false
  }
}

function logout() {
  api.clearAuth()
  stopPolling()
  loggedIn.value = false
  router.push('/')
}

async function refreshStatus() {
  try {
    const st = await api.fetchStatus()
    serverOnline.value = true
    signalCount.value = st.signal_count || 0
    inTradeTime.value = st.in_trade_time
  } catch (_) { serverOnline.value = false }
  try {
    const alerts = await api.fetchAlerts()
    alertCount.value = alerts?.length || 0
  } catch (_) {}
}

function handleSSE(msg) {
  if (msg.signal) { addToast('新信号: ' + (msg.signal.code || ''), 'warning'); refreshStatus() }
}

function startPolling() {
  refreshStatus()
  statusTimer = setInterval(refreshStatus, 15000)
  api.connectSSE()
  unsubSSE = api.onSSE(handleSSE)
}

function stopPolling() {
  if (statusTimer) { clearInterval(statusTimer); statusTimer = null }
  api.disconnectSSE()
  if (unsubSSE) { unsubSSE(); unsubSSE = null }
}

onMounted(async () => {
  const ok = await checkAuth()
  if (ok) startPolling()
})
onUnmounted(stopPolling)
</script>

<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body {
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'PingFang SC', sans-serif;
  font-size: 16px; line-height: 1.5;
  background: #0f0f23; color: #e0e0e0; overscroll-behavior: none;
  -webkit-tap-highlight-color: transparent;
}
a { text-decoration: none; color: inherit; }
.app { display: flex; flex-direction: column; height: 100vh; height: 100dvh; }
.login-page { align-items: center; justify-content: center; }

/* ── 顶部状态栏（倍宽）── */
.topbar {
  height: 80px; min-height: 80px; display: flex; align-items: center;
  justify-content: space-between; padding: 0 20px;
  padding-top: env(safe-area-inset-top, 0px);
  background: #1a1a2e; border-bottom: 1px solid #2a2a3e;
  flex-shrink: 0; z-index: 100;
  position: sticky; top: 0;
}
.logo { font-size: 22px; font-weight: 700; color: #FF4D4F; position: absolute; left: 50%; transform: translateX(-50%); }
.session-tag { font-size: 18px; }
.hamburger { font-size: 26px; color: #999; cursor: pointer; padding: 8px 12px; }

/* ── 汉堡菜单 ── */
.menu-overlay {
  position: fixed; inset: 0; background: rgba(0,0,0,0.5); z-index: 200;
}
.menu-drawer {
  position: fixed; top: 0; left: 0; width: 75vw; max-width: 280px; height: 100vh; height: 100dvh;
  background: #1a1a2e; z-index: 201; transform: translateX(-100%);
  transition: transform 0.25s; display: flex; flex-direction: column; overflow-y: auto;
}
.menu-drawer.open { transform: translateX(0); }
.menu-header { padding: 18px 18px 14px; border-bottom: 1px solid #2a2a3e; display: flex; justify-content: space-between; align-items: center; }
.menu-title { font-size: 16px; font-weight: 600; color: #e0e0e0; }
.menu-online { font-size: 12px; color: #888; }
.menu-online.on { color: #4caf50; }
.menu-item {
  display: flex; align-items: center; gap: 12px; padding: 16px 18px;
  font-size: 16px; color: #ccc; border-bottom: 1px solid rgba(42,42,62,0.5);
  position: relative;
}
.menu-item:active { background: rgba(255,77,79,0.08); }
.menu-icon { font-size: 18px; width: 28px; text-align: center; }
.badge {
  position: absolute; right: 14px; background: #FF4D4F; color: #fff;
  font-size: 12px; min-width: 20px; height: 20px; border-radius: 10px;
  display: flex; align-items: center; justify-content: center; padding: 0 6px;
}
.menu-divider { height: 1px; background: #2a2a3e; margin: 8px 0; }
.menu-footer { padding: 14px 18px; display: flex; gap: 10px; }
.menu-btn {
  flex: 1; padding: 12px; border-radius: 8px; border: 1px solid #333;
  background: transparent; color: #999; font-size: 14px; text-align: center; cursor: pointer;
}
.menu-btn.logout { border-color: rgba(255,77,79,0.3); color: #FF4D4F; }

/* ── 主内容 ── */
.main-content { flex: 1; overflow-y: auto; padding: 16px; padding-bottom: 80px; }

/* ── 底部标签栏 ── */
.tabbar {
  position: fixed; bottom: 0; left: 0; right: 0; height: 68px;
  padding-bottom: env(safe-area-inset-bottom, 0px);
  display: flex; background: #1a1a2e; border-top: 1px solid #2a2a3e;
  z-index: 100;
  box-sizing: content-box;
}
.tab-item {
  flex: 1; display: flex; flex-direction: column; align-items: center;
  justify-content: center; gap: 4px; font-size: 14px; color: #666;
  cursor: pointer; transition: color 0.2s; padding: 6px 0;
}
.tab-item:active { color: #999; }
.tab-active { color: #FF4D4F; }
.tab-icon { font-size: 24px; }
.tab-label { font-size: 12px; }

/* ── 登录 ── */
.login-box {
  background: #1a1a2e; padding: 40px 28px; border-radius: 14px; width: 90vw; max-width: 380px;
}
.login-box h1 { font-size: 28px; margin-bottom: 6px; color: #FF4D4F; text-align: center; }
.login-box .subtitle { color: #888; margin-bottom: 28px; font-size: 14px; text-align: center; }
.form-group { margin-bottom: 16px; }
.form-group label { display: block; font-size: 14px; color: #999; margin-bottom: 6px; }
.form-group input {
  width: 100%; padding: 12px 14px; border-radius: 8px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 16px; outline: none;
}
.form-group input:focus { border-color: #FF4D4F; }
.btn-login {
  width: 100%; padding: 15px; border-radius: 8px; border: none;
  background: #FF4D4F; color: #fff; font-size: 17px; cursor: pointer; margin-top: 10px;
}
.btn-login:disabled { opacity: 0.5; }
.login-error { color: #FF4D4F; font-size: 14px; margin-top: 12px; text-align: center; }

/* ── Toast ── */
.toast-container { position: fixed; top: 56px; left: 50%; transform: translateX(-50%); z-index: 9999; width: 90vw; max-width: 360px; }
.toast {
  padding: 14px 18px; border-radius: 10px; margin-bottom: 8px; font-size: 14px; text-align: center;
  animation: slideIn 0.3s;
}
.toast.info { background: rgba(26,26,46,0.95); border: 1px solid #333; color: #e0e0e0; }
.toast.warning { background: rgba(255,77,79,0.15); border: 1px solid #FF4D4F; color: #FF4D4F; }
.toast.success { background: rgba(76,175,80,0.15); border: 1px solid #4caf50; color: #4caf50; }
@keyframes slideIn { from { transform: translateY(-100%); opacity: 0; } to { transform: translateY(0); opacity: 1; } }
</style>
