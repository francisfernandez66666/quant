<template>
  <div class="settings-page">
    <h2>设置</h2>

    <div class="setting-card">
      <div class="setting-header">服务器连接</div>
      <div class="setting-row">
        <label>服务器地址</label>
        <input v-model="serverUrl" placeholder="http://localhost:8080" />
      </div>
      <div class="setting-row">
        <label>连接状态</label>
        <span :class="['status', serverOnline ? 'online' : 'offline']">
          {{ serverOnline ? '已连接' : '离线' }}
        </span>
      </div>
      <button class="btn-save" @click="saveServer">保存</button>
    </div>

    <div class="setting-card">
      <div class="setting-header">通知设置</div>
      <div class="setting-row">
        <label>浏览器通知</label>
        <button class="btn-test" @click="requestNotify">授权并测试</button>
      </div>
      <div class="setting-row">
        <label>声音提醒</label>
        <button class="btn-test" @click="playTest">测试声音</button>
      </div>
      <div class="setting-row">
        <label>macOS 通知</label>
        <span class="status online">后台自动发送</span>
      </div>
    </div>

    <div class="setting-card">
      <div class="setting-header">账户信息</div>
      <div class="setting-row">
        <label>账号</label>
        <span class="account">{{ account }}</span>
      </div>
      <div class="setting-row">
        <label>令牌</label>
        <span class="status offline">{{ token ? token.slice(0, 20) + '...' : '未登录' }}</span>
      </div>
    </div>

    <div class="setting-card">
      <div class="setting-header">系统</div>
      <div class="setting-row">
        <label>版本</label>
        <span>量仔期货 v1.1.0 桌面版</span>
      </div>
      <div class="setting-row">
        <label>后端</label>
        <span>Go 1.22+ 单二进制</span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import * as api from '../api/index.js'

const serverUrl = ref(api.getStoredServer() || '')
const serverOnline = ref(false)
const account = ref(api.getAccount())
const token = ref(localStorage.getItem('liangzai_token') || '')

function saveServer() {
  api.setStoredServer(serverUrl.value)
  alert('服务器地址已保存')
}

function requestNotify() {
  if ('Notification' in window) {
    Notification.requestPermission().then(perm => {
      if (perm === 'granted') {
        new Notification('量仔期货', { body: '通知授权成功' })
        alert('通知授权成功')
      } else {
        alert('通知被拒绝，请在浏览器设置中开启')
      }
    })
  } else {
    alert('浏览器不支持通知')
  }
}

function playTest() {
  try {
    const ctx = new (window.AudioContext || window.webkitAudioContext)()
    const osc = ctx.createOscillator()
    const gain = ctx.createGain()
    osc.connect(gain); gain.connect(ctx.destination)
    osc.frequency.value = 660; osc.type = 'sine'
    gain.gain.value = 0.1; osc.start(); osc.stop(ctx.currentTime + 0.2)
  } catch (_) {}
}

onMounted(async () => {
  try {
    await api.fetchStatus()
    serverOnline.value = true
  } catch (_) { serverOnline.value = false }
})
</script>

<style scoped>
.settings-page { max-width: 600px; }
.settings-page h2 { font-size: 18px; font-weight: 600; margin-bottom: 16px; }
.setting-card {
  background: #1a1a2e; border-radius: 8px; padding: 16px; margin-bottom: 12px;
}
.setting-header { font-size: 16px; font-weight: 600; color: #ccc; margin-bottom: 12px; }
.setting-row {
  display: flex; align-items: center; justify-content: space-between;
  padding: 8px 0; font-size: 16px;
}
.setting-row label { color: #888; }
.setting-row input {
  padding: 6px 10px; border-radius: 4px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 16px; width: 240px; outline: none;
}
.setting-row input:focus { border-color: #FF4D4F; }
.status.online { color: #4caf50; }
.status.offline { color: #888; }
.account { color: #FF4D4F; }
.btn-save, .btn-test {
  margin-top: 8px; padding: 6px 16px; border-radius: 4px; border: 1px solid #333;
  background: transparent; color: #e0e0e0; cursor: pointer; font-size: 16px;
}
.btn-save:hover, .btn-test:hover { background: #2a2a3e; }
</style>
