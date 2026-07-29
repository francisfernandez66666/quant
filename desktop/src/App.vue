<template>
  <div class="app" v-if="loggedIn">
    <!-- Mobile hamburger -->
    <div class="hamburger" @click="menuOpen = !menuOpen">
      <span></span><span></span><span></span>
    </div>
    <!-- Sidebar overlay -->
    <div class="sidebar-overlay" v-if="menuOpen" @click="menuOpen = false"></div>
    <aside class="sidebar" :class="{ open: menuOpen }">
      <div class="logo">量仔期货</div>
      <nav class="nav">
        <router-link to="/dashboard" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">📊</span> 仪表盘
        </router-link>
        <router-link to="/signals" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">⚡</span> 信号
          <span class="badge" v-if="signalCount > 0">{{ signalCount }}</span>
        </router-link>
        <router-link to="/watchlist" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">👁</span> 自选
        </router-link>
        <router-link to="/hotspot" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">🔥</span> 热点
        </router-link>
        <router-link to="/msgcenter" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">💬</span> 消息
          <span class="badge" v-if="alertCount > 0">{{ alertCount }}</span>
        </router-link>
        <router-link to="/positions" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">💼</span> 持仓
        </router-link>
        <router-link to="/settings" class="nav-item" active-class="active" @click="menuOpen = false">
          <span class="nav-icon">⚙</span> 设置
        </router-link>
      </nav>
      <div class="sidebar-footer">
        <div class="server-status" :class="{ online: serverOnline }">
          {{ serverOnline ? '服务在线' : '离线' }}
        </div>
        <div class="account-name">{{ account }}</div>
      </div>
    </aside>
    <main class="main">
      <div class="topbar">
        <div class="trade-time" v-if="inTradeTime !== null">
          {{ inTradeTime ? '🟢 交易时段' : '🔴 盘前/盘后' }}
        </div>
        <div class="topbar-right">
          <button class="btn-notify" @click="testNotify">🔔</button>
          <button class="btn-logout" @click="logout">退出</button>
        </div>
      </div>
      <div class="content">
        <router-view />
      </div>
    </main>

    <div class="toast-container">
      <div v-for="(t, i) in toasts" :key="i" :class="['toast', t.type]">{{ t.msg }}</div>
    </div>
  </div>
  <div class="app login-page" v-else>
    <div class="login-box">
      <h1>量仔期货</h1>
      <p class="subtitle">量化交易辅助工具</p>
      <div class="form-group">
        <label>服务器地址</label>
        <input v-model="serverUrl" placeholder="http://127.0.0.1:8080" />
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

const serverUrl = ref(api.getStoredServer() || 'http://127.0.0.1:8080')
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

async function testNotify() {
  if ('Notification' in window && Notification.permission === 'granted') {
    new Notification('量仔期货', { body: '通知测试成功', icon: '' })
  }
  addToast('通知测试' + (Notification.permission === 'granted' ? '已发送' : '（通知未授权）'), 'info')
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
  menuOpen.value = false
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
  if (msg.signal) {
    addToast('新信号: ' + (msg.signal.code || ''), 'warning')
    refreshStatus()
  }
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
  background: #0f0f23; color: #e0e0e0;
  -webkit-tap-highlight-color: transparent;
}

/* ====== Login ====== */
.app { display: flex; height: 100vh; width: 100vw; }
.login-page { align-items: center; justify-content: center; }
.login-box {
  background: #1a1a2e; padding: 40px 28px; border-radius: 12px; width: 90%; max-width: 380px;
}
.login-box h1 { font-size: 26px; margin-bottom: 4px; color: #FF4D4F; text-align: center; }
.login-box .subtitle { color: #888; margin-bottom: 24px; font-size: 13px; text-align: center; }
.form-group { margin-bottom: 14px; }
.form-group label { display: block; font-size: 12px; color: #999; margin-bottom: 5px; }
.form-group input {
  width: 100%; padding: 11px 12px; border-radius: 8px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 15px; outline: none;
  -webkit-appearance: none;
}
.form-group input:focus { border-color: #FF4D4F; }
.btn-login {
  width: 100%; padding: 12px; border-radius: 8px; border: none;
  background: #FF4D4F; color: #fff; font-size: 16px; cursor: pointer; margin-top: 6px;
  -webkit-appearance: none;
}
.btn-login:disabled { opacity: 0.5; }
.login-error { color: #FF4D4F; font-size: 13px; margin-top: 12px; text-align: center; }

/* ====== Hamburger ====== */
.hamburger {
  display: none; position: fixed; top: 0; left: 0; z-index: 1001;
  width: 44px; height: 44px; padding: 12px 10px; cursor: pointer;
  flex-direction: column; justify-content: center; gap: 5px;
}
.hamburger span { display: block; height: 2px; background: #999; border-radius: 1px; transition: 0.2s; }
.hamburger span:nth-child(2) { width: 70%; }

/* ====== Sidebar ====== */
.sidebar-overlay {
  display: none; position: fixed; inset: 0; z-index: 998;
  background: rgba(0,0,0,0.5);
}
.sidebar {
  width: 200px; background: #1a1a2e; display: flex; flex-direction: column;
  border-right: 1px solid #2a2a3e; flex-shrink: 0;
}
.logo {
  padding: 20px 16px; font-size: 18px; font-weight: 700; color: #FF4D4F;
  border-bottom: 1px solid #2a2a3e;
}
.nav { flex: 1; padding: 8px 0; }
.nav-item {
  display: flex; align-items: center; gap: 8px; padding: 12px 16px;
  color: #999; text-decoration: none; font-size: 14px; position: relative;
  transition: all 0.2s;
}
.nav-item:hover { background: rgba(255,77,79,0.06); color: #e0e0e0; }
.nav-item.active { color: #FF4D4F; background: rgba(255,77,79,0.1); }
.nav-icon { font-size: 16px; }
.badge {
  position: absolute; right: 12px; background: #FF4D4F; color: #fff;
  font-size: 11px; min-width: 18px; height: 18px; border-radius: 9px;
  display: flex; align-items: center; justify-content: center;
}
.sidebar-footer {
  padding: 14px 16px; border-top: 1px solid #2a2a3e;
}
.server-status { font-size: 12px; color: #888; margin-bottom: 4px; }
.server-status.online { color: #4caf50; }
.account-name { font-size: 13px; color: #e0e0e0; }

/* ====== Main ====== */
.main { flex: 1; display: flex; flex-direction: column; overflow: hidden; min-width: 0; }
.topbar {
  height: 44px; display: flex; align-items: center; justify-content: space-between;
  padding: 0 14px; background: #1a1a2e; border-bottom: 1px solid #2a2a3e;
  flex-shrink: 0;
}
.trade-time { font-size: 12px; }
.topbar-right { display: flex; gap: 6px; }
.btn-notify, .btn-logout {
  padding: 5px 12px; border-radius: 6px; border: 1px solid #333;
  background: transparent; color: #999; font-size: 12px; cursor: pointer;
}
.btn-notify:hover, .btn-logout:hover { background: #2a2a3e; color: #e0e0e0; }
.content { flex: 1; overflow-y: auto; padding: 14px; }

/* ====== Toast ====== */
.toast-container { position: fixed; top: 50px; right: 12px; z-index: 9999; max-width: 90vw; }
.toast {
  padding: 10px 16px; border-radius: 6px; margin-bottom: 8px; font-size: 13px;
  animation: slideIn 0.3s; word-break: break-word;
}
.toast.info { background: #1a1a2e; border: 1px solid #333; color: #e0e0e0; }
.toast.warning { background: rgba(255,77,79,0.15); border: 1px solid #FF4D4F; color: #FF4D4F; }
.toast.success { background: rgba(76,175,80,0.15); border: 1px solid #4caf50; color: #4caf50; }
@keyframes slideIn { from { transform: translateX(100%); opacity: 0; } to { transform: translateX(0); opacity: 1; } }

/* ====== Mobile ====== */
@media (max-width: 768px) {
  .hamburger { display: flex; position: fixed; top: 36px; left: 0; z-index: 1001; }
  .sidebar-overlay { display: block; }
  .sidebar {
    position: fixed; top: 0; left: 0; bottom: 0; z-index: 999;
    transform: translateX(-100%); transition: transform 0.25s ease;
  }
  .sidebar.open { transform: translateX(0); }
  .main { position: relative; z-index: 0; clip-path: inset(0); }
  .topbar {
    position: fixed; top: 36px; left: 0; right: 0; z-index: 100;
    padding: 12px 14px 12px 52px; height: 44px; margin-top: 0;
    background: #1a1a2e; border-bottom: 1px solid #2a2a3e;
  }
  .content {
    padding: 8px; margin-top: 80px; padding-top: 8px;
    background: #0f0f23; overscroll-behavior: contain;
  }
  .toast-container { right: 8px; top: 50px; }
}

@media (min-width: 769px) {
  .hamburger { display: none; top: auto; }
  .sidebar-overlay { display: none; }
  .sidebar { position: relative; transform: none; }
}
</style>
