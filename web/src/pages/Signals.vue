<template>
    <div class="signals-page">
      <div class="page-header">
        <h2>策略信号</h2>
        <div class="filter-row">
          <button v-for="f in filters" :key="f.key"
            :class="['filter-btn', activeFilter === f.key ? 'active' : '']"
            @click="activeFilter = f.key">
            {{ f.label }}
          </button>
        </div>
      </div>

      <div class="signals-table">
        <div class="table-scroll">
          <div class="table-inner">
            <div class="table-header">
              <span class="col-code">代码</span>
              <span class="col-name">名称</span>
              <!-- 当前价格与涨跌幅列：让用户快速感知每只信号股的最新行情 -->
              <span class="col-price">价格</span>
              <span class="col-chg">涨跌幅</span>
              <span class="col-strategy">策略</span>
              <span class="col-score">总分</span>
              <span class="col-level">等级</span>
              <span class="col-detail">D1/D2/D3/D4</span>
            </div>
            <div v-for="s in filteredSignals" :key="s.code" class="table-row" @click="tapRow(s)">
              <span class="col-code">{{ s.code }}</span>
              <span class="col-name">{{ s.name || '-' }}</span>
              <!-- 每行显示信号股的最新价和涨跌幅，涨跌幅 >=0 标红色，<0 标绿色 -->
              <span class="col-price">{{ s.price ? '¥' + s.price.toFixed(2) : '-' }}</span>
              <span class="col-chg" :class="s.change_pct >= 0 ? 'up' : 'down'">{{ s.change_pct != null ? (s.change_pct >= 0 ? '+' : '') + s.change_pct.toFixed(2) + '%' : '-' }}</span>
              <span class="col-strategy">{{ s.strategy }}</span>
              <span class="col-score">{{ s.total_score?.toFixed(0) }}</span>
              <span class="col-level">
                <span :class="['tag', s.remind_level]">
                  {{ s.level === '交易' ? '交易' : s.level === '观望' ? '观望' : s.remind_level === 'strong' ? '可开仓' : s.remind_level === 'observe' ? '观察' : '静默' }}
                </span>
              </span>
              <span class="col-detail">
                <span class="d-pill d1" :title="'D1: ' + (s.d1_desc || '')">
                  {{ (s.d1 || 0).toFixed(0) }}<em v-if="s.d1_desc">{{ shortDesc(s.d1_desc) }}</em>
                </span>
                <span class="d-pill d2" :title="'D2: ' + (s.d2_desc || '')">
                  {{ (s.d2 || 0).toFixed(0) }}<em v-if="s.d2_desc">{{ shortDesc(s.d2_desc) }}</em>
                </span>
                <span class="d-pill d3" :title="'D3: ' + (s.d3_desc || '')">
                  {{ (s.d3 || 0).toFixed(0) }}<em v-if="s.d3_desc">{{ shortDesc(s.d3_desc) }}</em>
                </span>
                <span class="d-pill d4" :title="'D4: ' + (s.d4_desc || '')">
                  {{ (s.d4 || 0).toFixed(0) }}<em v-if="s.d4_desc">{{ shortDesc(s.d4_desc) }}</em>
                </span>
              </span>
            </div>
          </div>
        </div>
        <!-- 操作面板 -->
      <div class="sheet-overlay" v-if="actionTarget" @click="actionTarget = null"></div>
      <div class="action-sheet" v-if="actionTarget">
        <div class="sheet-title">{{ actionTarget.name || actionTarget.code }}</div>
        <button v-if="actionTarget.can_open" class="sheet-btn" @click="confirmTrade(actionTarget, 'buy'); actionTarget = null">买入</button>
        <button v-if="actionTarget.action === 'buy'" class="sheet-btn" @click="confirmTrade(actionTarget, 'ignore'); actionTarget = null">忽略信号</button>
        <button class="sheet-btn sheet-cancel" @click="actionTarget = null">取消</button>
      </div>
      <div class="empty" v-if="filteredSignals.length === 0">暂无信号</div>
    </div>

    <div class="modal-overlay" v-if="showConfirm">
      <div class="modal">
        <h3>确认交易</h3>
        <div class="modal-body">
          <p><strong>{{ tradeTarget.code }}</strong> {{ tradeTarget.name }}</p>
          <p>策略: {{ tradeTarget.strategy }}</p>
          <p>总分: {{ tradeTarget.total_score?.toFixed(0) }}</p>
          <p>价格: {{ tradeTarget.price ? '¥' + tradeTarget.price.toFixed(2) : '—' }}</p>
        </div>
        <div class="modal-actions">
          <button class="btn-cancel" @click="showConfirm = false">取消</button>
          <button v-if="tradeAction === 'buy'" class="btn-buy" @click="doAction('buy')">确认买入</button>
          <button v-if="tradeAction === 'ignore'" class="btn-ignore" @click="doAction('ignore')">忽略</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import * as api from '../api/index.js'

const signals = ref([])
const activeFilter = ref('all')
const showConfirm = ref(false)
const tradeTarget = ref({})
const tradeAction = ref('')
const actionTarget = ref(null)
function tapRow(s) { actionTarget.value = s }

const filters = [
  { key: 'all', label: '全部' },
  { key: 'strong', label: '可开仓' },
  { key: 'observe', label: '观察' },
  { key: 'mute', label: '静默' },
]

const filteredSignals = computed(() => {
  if (activeFilter.value === 'all') return signals.value
  return signals.value.filter(s => s.remind_level === activeFilter.value)
})

function shortDesc(s) {
  if (!s) return ''
  const idx = s.indexOf(',')
  return idx > 0 ? s.slice(0, idx) : s.slice(0, 6)
}

function confirmTrade(s, action) {
  tradeTarget.value = s
  tradeAction.value = action
  showConfirm.value = true
}

async function doAction(action) {
  try {
    const res = await api.actionSignal(tradeTarget.value.code, action)
    showConfirm.value = false
    await load()
  } catch (e) {
    showConfirm.value = false
    alert('操作失败: ' + e.message)
  }
}

async function load() {
  try { signals.value = await api.fetchSignals() } catch (_) {}
}

let timer = null
let unsubSSE = null

function handleSSE(msg) {
  if (msg.signal || msg.type === 'scan') load()
}

onMounted(() => {
  load()
  timer = setInterval(load, 3000)
  api.connectSSE()
  unsubSSE = api.onSSE(handleSSE)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
  if (unsubSSE) unsubSSE()
})
</script>

<style scoped>
.signals-page { max-width: 1200px; }
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 16px; }
.page-header h2 { font-size: 18px; font-weight: 600; }
.filter-row { display: flex; gap: 8px; }
.filter-btn {
  padding: 6px 16px; border-radius: 6px; border: 1px solid #333;
  background: transparent; color: #888; font-size: 16px; cursor: pointer;
}
.filter-btn.active { background: #FF4D4F; border-color: #FF4D4F; color: #fff; }
.signals-table { background: #1a1a2e; border-radius: 10px; overflow: hidden; font-size: 16px; }
.table-scroll { overflow-x: auto; }
.table-inner { min-width: 720px; }
.table-header, .table-inner > div {
  display: flex; align-items: center; min-height: 46px; padding: 4px 12px; gap: 0;
  border-bottom: 1px solid #1a1a26;
}
.table-header { background: #2a2a3e; color: #888; font-weight: 600; }
.table-inner > div:last-child { border-bottom: none; }

.col-code { flex: 0.7; font-family: monospace; color: #4fc3f7; }
.col-name { flex: 0.8; color: #e0e0e0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.col-price { flex: 0.6; color: #e0e0e0; font-family: monospace; text-align: right; }
.col-chg { flex: 0.7; font-family: monospace; text-align: right; }
.col-chg.up { color: #FF4D4F; }
.col-chg.down { color: #4caf50; }
.col-strategy { flex: 0.7; color: #e0e0e0; }
.col-score { flex: 1; font-weight: 600; color: #FAAD14; text-align: center; }
.col-level { flex: 1; }
.col-detail { flex: 1; }
.col-detail { display: flex; gap: 6px; align-items: center; }
.d-pill {
  display: inline-flex; align-items: center; gap: 2px;
  font-size: 13px; padding: 0 6px; border-radius: 4px; white-space: nowrap;
}
.d-pill em { font-size: 11px; font-style: normal; opacity: 0.85; }
.d-pill.d1 { color: #FF4D4F; background: rgba(255,77,79,0.10); }
.d-pill.d2 { color: #FAAD14; background: rgba(250,173,20,0.10); }
.d-pill.d3 { color: #4fc3f7; background: rgba(79,195,247,0.10); }
.d-pill.d4 { color: #4caf50; background: rgba(76,175,80,0.10); }

/* 操作面板 */
.sheet-overlay { position: fixed; inset: 0; background: rgba(0,0,0,0.5); z-index: 300; }
.action-sheet {
  position: fixed; bottom: 0; left: 0; right: 0; z-index: 301;
  background: #1a1a2e; border-radius: 16px 16px 0 0; padding: 20px 16px;
  padding-bottom: calc(20px + env(safe-area-inset-bottom));
  display: flex; flex-direction: column; gap: 10px;
}
.sheet-title { font-size: 16px; color: #ccc; text-align: center; padding: 4px 0 8px; border-bottom: 1px solid #2a2a3e; }
.sheet-btn {
  width: 100%; padding: 14px; border-radius: 12px; border: none;
  font-size: 16px; cursor: pointer; text-align: center; background: #2a2a3e; color: #ccc;
}
.sheet-btn:active { opacity: 0.8; }
.sheet-cancel { background: #2a2a3e; color: #999; }
.signals-table .table-inner > div { cursor: pointer; }
.signals-table .table-inner > div:active { background: rgba(255,255,255,0.05); }
.col-action { display: none; }
.tag { font-size: 16px; padding: 2px 10px; border-radius: 10px; }
.tag.strong { background: rgba(255,77,79,0.15); color: #FF4D4F; }
.tag.observe { background: rgba(250,173,20,0.15); color: #FAAD14; }
.tag.mute { background: rgba(153,153,153,0.15); color: #999; }
.btn-buy {
  padding: 4px 12px; border-radius: 4px; border: none;
  background: #FF4D4F; color: #fff; font-size: 16px; cursor: pointer;
}
.btn-ignore {
  padding: 4px 12px; border-radius: 4px; border: 1px solid #555;
  background: transparent; color: #888; font-size: 16px; cursor: pointer;
}
.text-muted { color: #555; font-size: 16px; }
.empty { text-align: center; padding: 40px; color: #555; font-size: 16px; }

.modal-overlay {
  position: fixed; inset: 0; background: rgba(0,0,0,0.6);
  display: flex; align-items: center; justify-content: center; z-index: 100;
}
.modal { background: #1a1a2e; border-radius: 8px; padding: 24px; width: 360px; }
.modal h3 { font-size: 16px; margin-bottom: 16px; color: #e0e0e0; }
.modal-body p { font-size: 16px; color: #888; margin-bottom: 6px; }
.modal-body strong { color: #e0e0e0; }
.modal-actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 20px; }
.btn-cancel {
  padding: 8px 16px; border-radius: 4px; border: 1px solid #333;
  background: transparent; color: #888; cursor: pointer; font-size: 16px;
}
</style>
