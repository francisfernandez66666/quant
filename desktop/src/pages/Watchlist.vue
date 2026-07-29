<template>
  <div class="watchlist-page">
    <div class="page-header">
      <h2>自选股</h2>
      <div class="add-row">
        <input v-model="newCode" placeholder="输入代码 (如 000001)" @keyup.enter="add" :disabled="adding" />
        <button @click="add" class="btn-add" :disabled="adding">{{ adding ? '添加中…' : '添加' }}</button>
        <span v-if="feedback" class="feedback" :class="feedbackType">{{ feedback }}</span>
      </div>
    </div>

    <div class="eval-table" v-if="stocks.length">
      <div class="ev-header">
        <span class="ev-code sortable" @click="setSort('code')">代码{{ sortArrow('code') }}</span>
        <span class="ev-name sortable" @click="setSort('name')">名称{{ sortArrow('name') }}</span>
        <span class="ev-price sortable" @click="setSort('price')">现价{{ sortArrow('price') }}</span>
        <span class="ev-chg sortable" @click="setSort('change_pct')">涨跌{{ sortArrow('change_pct') }}</span>
        <span class="ev-n sortable" @click="setSort('n_score')" title="N形≥60可操作">N≥60{{ sortArrow('n_score') }}</span>
        <span class="ev-dragon sortable" @click="setSort('dragon_score')" title="龙头≥70买入,≥50观察">龙≥70{{ sortArrow('dragon_score') }}</span>
        <span class="ev-db sortable" @click="setSort('db_score')" title="双凸≥70买入,50-70观察">凸≥70{{ sortArrow('db_score') }}</span>
        <span class="ev-dr sortable" @click="setSort('dr_score')" title="龙回头≥60首次入场">回≥60{{ sortArrow('dr_score') }}</span>
        <span class="ev-m sortable" @click="setSort('m_score')" title="动量≥50值得看">量≥50{{ sortArrow('m_score') }}</span>
      </div>
      <div class="ev-body">
          <div v-for="e in sortedEvals" :key="e.code" :class="rowClass(e)">
          <span class="ev-code">{{ e.code }}</span>
          <span class="ev-name">{{ e.name || '-' }}</span>
          <span class="ev-price">¥{{ (e.price || 0).toFixed(2) }}</span>
          <span :class="['ev-chg', (e.change_pct || 0) >= 0 ? 'up' : 'down']">
            {{ (e.change_pct || 0) > 0 ? '+' : '' }}{{ (e.change_pct || 0).toFixed(2) }}%
          </span>
          <span :class="scoreClass(e.n_score, e.n_pass, 80)">{{ e.n_score > 0 ? e.n_score.toFixed(0) : '—' }}</span>
          <span :class="scoreClass(e.dragon_score, e.dragon_pass, 80)">{{ e.dragon_score > 0 ? e.dragon_score.toFixed(0) : '—' }}</span>
          <span :class="scoreClass(e.db_score, e.db_pass, 80)">{{ e.db_score > 0 ? e.db_score.toFixed(0) : '—' }}</span>
          <span :class="scoreClass(e.dr_score, e.dr_pass, 80)">{{ e.dr_score > 0 ? e.dr_score.toFixed(0) : '—' }}</span>
          <span :class="scoreClass(e.m_score, e.m_pass, 70)">{{ e.m_score > 0 ? e.m_score.toFixed(0) : '—' }}</span>
          <span><button class="btn-remove" @click="remove(e.code)">✕</button></span>
        </div>
      </div>
    </div>
    <div class="empty" v-else>暂无自选股，输入代码添加</div>

    <div class="legend">
      <span class="lg-strong">≥80 强势</span>
      <span class="lg-pass">≥门槛 达标</span>
      <span class="lg-low">&lt;门槛 偏低</span>
      <span class="lg-sep">|</span>
      <span class="lg-item">N形≥60操作, 龙头≥70买入/≥50观察, 双凸≥70买入/50-70观察, 回头≥60入场, 动量≥50关注</span>
      <span class="lg-sep">|</span>
      <span class="lg-item">点击表头排序</span>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import * as api from '../api/index.js'

const stocks = ref([])
const newCode = ref('')
const sortKey = ref('')
const sortDir = ref(-1)
const adding = ref(false)
const feedback = ref('')
const feedbackType = ref('ok')

function scoreClass(score, pass, strongMin) {
  if (!score || score <= 0) return 'ev-score'
  if (score >= strongMin) return 'ev-score strong'
  if (pass) return 'ev-score pass'
  return 'ev-score'
}

function rowClass(e) {
  const strong = (e.n_score || 0) >= 80 || (e.dragon_score || 0) >= 80 || (e.db_score || 0) >= 80 || (e.dr_score || 0) >= 80 || (e.m_score || 0) >= 70
  if (strong) return 'ev-row strong'
  const watch = (e.n_score || 0) >= 60 || (e.dragon_score || 0) >= 70 || (e.db_score || 0) >= 70 || (e.dr_score || 0) >= 60 || (e.m_score || 0) >= 50
  if (watch) return 'ev-row watch'
  return 'ev-row'
}

function setSort(key) {
  if (sortKey.value === key) {
    sortDir.value *= -1
  } else {
    sortKey.value = key
    sortDir.value = -1
  }
}

function sortArrow(key) {
  if (sortKey.value !== key) return ''
  return sortDir.value === -1 ? ' ▼' : ' ▲'
}

function val(e, key) {
  const v = e[key]
  if (typeof v === 'string') return v || ''
  return v || 0
}

const sortedEvals = computed(() => {
  const arr = [...stocks.value]
  const sk = sortKey.value
  if (!sk) {
    return arr.sort((a, b) => {
      const sa = Math.max(a.n_score || 0, a.dragon_score || 0, a.db_score || 0, a.dr_score || 0, a.m_score || 0)
      const sb = Math.max(b.n_score || 0, b.dragon_score || 0, b.db_score || 0, b.dr_score || 0, b.m_score || 0)
      return sb - sa
    })
  }
  const dir = sortDir.value
  return arr.sort((a, b) => {
    const va = val(a, sk)
    const vb = val(b, sk)
    if (typeof va === 'string') return va.localeCompare(vb) * dir
    return (va - vb) * dir
  })
})

async function load() {
  try {
    const st = await api.fetchStatus()
    if (!api.isTradingSession(st.session) && stocks.value.length) {
      return
    }
    api.setLastSession(st.session)
    const [snap, wl, ev] = await Promise.all([
      api.fetchSnapshot(), api.fetchWatchlist(), api.fetchEvaluations()
    ])
    const codes = (wl.stocks || []).map(c => c.code || c)
    if (!codes.length) { stocks.value = []; return }
    // 评分 map：code → { n_score, dragon_score, ... }
    const evMap = {}
    if (ev) ev.forEach(e => { evMap[e.code] = e })
    // 快照拿实时价格，合并评分
    if (snap && snap.length) {
      stocks.value = snap
        .filter(s => codes.includes(s.code))
        .map(s => ({
          ...s,
          price: s.price, change_pct: s.change_pct,
          n_score: evMap[s.code]?.n_score || 0, n_pass: evMap[s.code]?.n_pass || false,
          dragon_score: evMap[s.code]?.dragon_score || 0, dragon_pass: evMap[s.code]?.dragon_pass || false,
          db_score: evMap[s.code]?.db_score || 0, db_pass: evMap[s.code]?.db_pass || false,
          dr_score: evMap[s.code]?.dr_score || 0, dr_pass: evMap[s.code]?.dr_pass || false,
          m_score: evMap[s.code]?.m_score || 0, m_pass: evMap[s.code]?.m_pass || false,
        }))
    } else if (ev && ev.length) {
      stocks.value = ev.filter(e => codes.includes(e.code))
    }
    // 兜底：快照也查不到的，做个空占位等下次刷新
    for (const c of codes) {
      if (!stocks.value.find(s => s.code === c)) {
        stocks.value.push({ code: c, name: c, price: 0, change_pct: 0,
          n_score: evMap[c]?.n_score || 0, dragon_score: evMap[c]?.dragon_score || 0,
          db_score: evMap[c]?.db_score || 0, dr_score: evMap[c]?.dr_score || 0, m_score: evMap[c]?.m_score || 0 })
      }
    }
  } catch (_) { stocks.value = [] }
}

async function add() {
  const code = newCode.value.trim()
  if (!code) return
  if (adding.value) return
  adding.value = true
  feedback.value = ''
  try {
    await api.addWatchlist(code)
    newCode.value = ''
    showFeedback('已添加 ' + code, 'ok')
    stocks.value = [] // 清除缓存的早返保护，强制刷新
    await load()
  } catch (e) { showFeedback('添加失败: ' + e.message, 'err') }
  adding.value = false
}

async function remove(code) {
  try {
    await api.removeWatchlist(code)
    showFeedback('已移除 ' + code, 'ok')
    await load()
  } catch (e) { showFeedback('删除失败: ' + e.message, 'err') }
}

function showFeedback(msg, type) {
  feedback.value = msg
  feedbackType.value = type || 'ok'
  setTimeout(() => { feedback.value = '' }, 2500)
}

let timer = null
onMounted(() => { load(); timer = setInterval(load, 3000) })
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<style scoped>
.watchlist-page { max-width: 1200px; }
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
.page-header h2 { font-size: 18px; font-weight: 600; }
.add-row { display: flex; gap: 8px; }
.add-row input {
  padding: 8px 12px; border-radius: 6px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 13px; width: 160px; outline: none;
}
.add-row input:focus { border-color: #FF4D4F; }
.btn-add {
  padding: 8px 16px; border-radius: 6px; border: none;
  background: #FF4D4F; color: #fff; font-size: 13px; cursor: pointer;
}
.btn-add:disabled { opacity: 0.5; cursor: not-allowed; }
.feedback { font-size: 12px; padding: 4px 10px; border-radius: 4px; white-space: nowrap; }
.feedback.ok { color: #4caf50; }
.feedback.err { color: #FF4D4F; }
.eval-table { font-size: 12px; background: #1a1a2e; border-radius: 8px; overflow-x: auto; white-space: nowrap; }
.ev-header, .ev-row { display: flex; align-items: center; padding: 4px 12px; gap: 0; min-width: 660px; }
.ev-header { background: #2a2a3e; color: #888; font-weight: 600; border-bottom: 1px solid #2a2a3e; }
.ev-row { border-bottom: 1px solid #1a1a26; }
.ev-row.watch { background: rgba(250,173,20,0.08); }
.ev-row.strong { background: rgba(255,77,79,0.10); }
.ev-row:last-child { border-bottom: none; }
.ev-body { max-height: 500px; overflow-y: auto; }
.ev-body::-webkit-scrollbar { width: 4px; }
.ev-body::-webkit-scrollbar-thumb { background: #333; border-radius: 2px; }
.ev-code { flex: 1; font-family: monospace; color: #4fc3f7; text-align: center; }
.ev-name { flex: 1; color: #ccc; overflow: hidden; text-overflow: ellipsis; }
.ev-price { flex: 1; text-align: center; color: #e0e0e0; }
.ev-chg { flex: 1; text-align: center; font-weight: 600; }
.ev-chg.up { color: #FF4D4F; }
.ev-chg.down { color: #4caf50; }
.ev-score, .ev-n, .ev-dragon, .ev-db, .ev-dr, .ev-m { flex: 1; text-align: center; color: #555; font-weight: 600; }
.ev-score.pass { color: #FAAD14; }
.ev-score.strong { color: #FF4D4F; }
.ev-act { flex: 0 0 30px; text-align: center; }
.sortable { cursor: pointer; user-select: none; }
.sortable:hover { color: #ccc; }
.btn-remove {
  background: transparent; border: 1px solid #333; color: #666;
  width: 22px; height: 22px; border-radius: 4px; cursor: pointer; font-size: 11px;
  display: flex; align-items: center; justify-content: center;
}
.btn-remove:hover { border-color: #FF4D4F; color: #FF4D4F; }
.empty { text-align: center; padding: 40px; color: #555; font-size: 13px; }
.legend {
  margin-top: 8px; padding: 6px 12px; font-size: 11px; color: #666;
  background: #1a1a2e; border-radius: 6px; display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
}
.lg-strong { color: #FF4D4F; }
.lg-pass { color: #FAAD14; }
.lg-low { color: #555; }
.lg-sep { color: #333; }
.lg-item { color: #666; }
</style>
