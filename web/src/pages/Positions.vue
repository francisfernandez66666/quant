<template>
  <div class="positions-page">
    <div class="page-header">
      <h2>持仓管理</h2>
    </div>

    <!-- 新增/编辑弹窗 -->
    <div class="modal-overlay" v-if="showAdd" @click.self="showAdd = false">
      <div class="modal">
        <div class="modal-title">{{ editingIdx >= 0 ? '编辑持仓' : '新增持仓' }}</div>
        <div class="form-row">
          <label>代码</label>
          <input v-model="formCode" placeholder="输入代码" @input="onCodeInput" :disabled="editingIdx >= 0" />
          <span class="lookup-result" v-if="lookupName">{{ lookupName }} ¥{{ lookupPrice?.toFixed(2) }}</span>
        </div>
        <div class="form-row">
          <label>成本价</label>
          <input v-model.number="formCost" type="number" step="0.001" placeholder="成本价" />
        </div>
        <div class="form-row">
          <label>持股数</label>
          <input v-model.number="formQty" type="number" step="1" placeholder="持股数量" />
        </div>
        <div class="form-row">
          <label>止盈%</label>
          <input v-model.number="formTp" type="number" step="0.1" placeholder="默认+8%" />
        </div>
        <div class="form-row">
          <label>止损%</label>
          <input v-model.number="formSl" type="number" step="0.1" placeholder="默认-5%" />
        </div>
        <div class="modal-actions">
          <button class="btn-cancel" @click="showAdd = false">取消</button>
          <button class="btn-confirm" @click="confirmAdd">确定</button>
        </div>
      </div>
    </div>

    <div class="positions-table" v-if="holdings.length">
      <div class="table-scroll">
        <div class="table-inner">
          <div class="table-header">
            <span class="col-code">代码</span>
            <span class="col-name">名称</span>
            <span class="col-num">数量</span>
            <span class="col-price">成本价</span>
            <span class="col-price">现价</span>
            <span class="col-chg">涨跌</span>
            <span class="col-chg">盈亏</span>
            <span class="col-sig">⚡</span>
            <span class="col-score">N</span>
            <span class="col-score">龙</span>
            <span class="col-score">量</span>
            <span class="col-sl">止盈/止损</span>
          </div>
          <div v-for="h in holdings" :key="h.code" :class="rowClass(h)" @click="tapRow(h)">
            <span class="col-code">{{ h.code }}</span>
            <span class="col-name">{{ h.name }}</span>
            <span class="col-num">{{ h.quantity }}</span>
            <span class="col-price">{{ h.cost_price?.toFixed(2) }}</span>
            <span class="col-price">{{ h.cur_price?.toFixed(2) }}</span>
            <span :class="['col-chg', (h.change_pct || 0) >= 0 ? 'up' : 'down']">
              {{ (h.change_pct || 0) > 0 ? '+' : '' }}{{ (h.change_pct || 0).toFixed(2) }}%
            </span>
            <span :class="['col-chg', (h.pnl_pct || 0) >= 0 ? 'up' : 'down']">
              {{ (h.pnl_pct || 0) > 0 ? '+' : '' }}{{ (h.pnl_pct || 0).toFixed(2) }}%
            </span>
            <span v-if="h.signal_active" class="col-sig">⚡</span>
            <span v-else class="col-sig dim">—</span>
            <span :class="['col-score', (h.n_score||0) >= 60 ? 'strong' : ((h.n_score||0) > 0 ? 'watch' : '')]">
              {{ (h.n_score || 0) > 0 ? h.n_score.toFixed(0) : '—' }}
            </span>
            <span :class="['col-score', (h.dragon_score||0) >= 70 ? 'strong' : ((h.dragon_score||0) >= 50 ? 'watch' : '')]">
              {{ (h.dragon_score || 0) > 0 ? h.dragon_score.toFixed(0) : '—' }}
            </span>
            <span :class="['col-score', (h.m_score||0) >= 50 ? 'watch' : '']">
              {{ (h.m_score || 0) > 0 ? h.m_score.toFixed(0) : '—' }}
            </span>
            <span class="col-sl">
              <span class="sl-tp">+{{ (h.take_profit_pct||8).toFixed(1) }}%</span>
              <span class="sl-div">/</span>
              <span class="sl-sel">-{{ (h.stop_loss_pct||5).toFixed(1) }}%</span>
            </span>
          </div>
        </div>
      </div>
    </div>
    <div class="empty" v-else>
      <p>暂无持仓</p>
      <p class="hint">点击下方「新增持仓」或通过信号页确认买入自动更新</p>
    </div>

    <div class="legend">
      <span><span class="lg-dot up"></span>当日涨跌红涨绿跌</span>
      <span class="lg-sep">|</span>
      <span><span class="lg-dot warn"></span>持仓盈亏红赚绿亏</span>
      <span class="lg-sep">|</span>
      <span>⚡ 有策略信号</span>
      <span class="lg-sep">|</span>
      <span class="lg-item">止盈+8% / 止损-5%</span>
      <span class="lg-sep">|</span>
      <span>N≥60可买 龙≥70买 量≥50关注</span>
    </div>

    <!-- 底部操作栏 -->
    <div class="bottom-bar">
      <div class="bottom-balance" v-if="!editingBalance" @click="editBalanceStart">
        可用 ¥{{ availableBalance.toFixed(2) }} ✏️
      </div>
      <div class="balance-editing" v-else>
        <input ref="balanceInput" v-model.number="balanceInputVal" type="number" step="0.01" @blur="editBalanceSave" @keydown.enter="editBalanceSave" @keydown.escape="editBalanceCancel" />
      </div>
      <button class="btn-add" @click="showAdd = true">+ 新增</button>
    </div>

    <!-- 操作面板 -->
    <div class="sheet-overlay" v-if="actionTarget" @click="actionTarget = null"></div>
    <div class="action-sheet" v-if="actionTarget">
      <div class="sheet-title">{{ actionTarget.name || actionTarget.code }}</div>
      <button class="sheet-btn" @click="editHolding(actionTarget); actionTarget = null">编辑持仓</button>
      <button class="sheet-btn sheet-danger" @click="removeHolding(actionTarget); actionTarget = null">删除持仓</button>
      <button class="sheet-btn sheet-cancel" @click="actionTarget = null">取消</button>
    </div>
  </div>
</template>

<script setup>
import { ref, nextTick, onMounted, onUnmounted } from 'vue'
import * as api from '../api/index.js'

const holdings = ref([])
const availableBalance = ref(0)
const showAdd = ref(false)
const editingIdx = ref(-1)
const formCode = ref('')
const formCost = ref(0)
const formQty = ref(0)
const lookupName = ref('')
const lookupPrice = ref(0)
const formTp = ref(8)
const formSl = ref(5)
let codeTimer = null
const editingBalance = ref(false)
const balanceInputVal = ref(0)
const balanceInput = ref(null)
const actionTarget = ref(null)
function tapRow(h) { actionTarget.value = h }

function editBalanceStart() {
  balanceInputVal.value = availableBalance.value
  editingBalance.value = true
  nextTick(() => balanceInput.value?.focus())
}
function editBalanceSave() {
  availableBalance.value = balanceInputVal.value
  editingBalance.value = false
  saveHoldings()
}
function editBalanceCancel() {
  editingBalance.value = false
}

function rowClass(h) {
  const chg = h.change_pct || 0
  const pnl = h.pnl_pct || 0
  if (h.signal_active) return 'hl-signal'
  if (chg >= 5 || pnl >= 8) return 'hl-strong'
  if (curReachedStop(h)) return 'hl-danger'
  if (chg >= 3 || pnl >= 5 || chg <= -3 || pnl <= -5) return 'hl-watch'
  return 'table-row'
}
function curReachedStop(h) {
  if (!h.cur_price || !h.stop_loss) return false
  return h.cur_price <= h.stop_loss || h.cur_price >= h.take_profit
}

async function load() {
  try {
    const st = await api.fetchStatus()
    api.setLastSession(st.session)
    const data = await api.fetchHoldings()
    if (data) {
      holdings.value = data.holdings || []
      availableBalance.value = data.available_balance || 0
    }
  } catch (_) {}
}

async function saveHoldings() {
  try {
    await api.updateHoldings({ holdings: holdings.value, available_balance: availableBalance.value })
  } catch (e) { alert('保存失败: ' + e.message) }
}

async function onCodeInput() {
  clearTimeout(codeTimer)
  const code = formCode.value.trim()
  if (code.length < 5) { lookupName.value = ''; return }
  codeTimer = setTimeout(async () => {
    try {
      const data = await api.fetchStockLookup(code)
      if (data && data.name) {
        lookupName.value = data.name
        lookupPrice.value = data.price || 0
      } else {
        lookupName.value = '未找到'
        lookupPrice.value = 0
      }
    } catch (_) { lookupName.value = '' }
  }, 300)
}

async function confirmAdd() {
  const code = formCode.value.trim()
  if (!code || !formCost.value || !formQty.value) { alert('请填写完整信息'); return }
  const item = {
    code,
    name: lookupName.value || code,
    quantity: formQty.value,
    cost_price: formCost.value,
    cur_price: lookupPrice.value || 0,
    pnl_pct: 0,
    change_pct: 0,
    take_profit_pct: formTp.value || 8,
    stop_loss_pct: formSl.value || 5,
  }
  if (editingIdx.value >= 0) {
    holdings.value[editingIdx.value] = { ...holdings.value[editingIdx.value], quantity: formQty.value, cost_price: formCost.value,
      take_profit_pct: formTp.value, stop_loss_pct: formSl.value }
  } else {
    holdings.value.push(item)
  }
  await saveHoldings()
  await load()
  showAdd.value = false
  editingIdx.value = -1
  resetForm()
}

function editHolding(h) {
  editingIdx.value = holdings.value.indexOf(h)
  formCode.value = h.code
  formCost.value = h.cost_price
  formQty.value = h.quantity
  lookupName.value = h.name
  lookupPrice.value = h.cur_price
  formTp.value = h.take_profit_pct || 8
  formSl.value = h.stop_loss_pct || 5
  showAdd.value = true
}

async function removeHolding(h) {
  if (!confirm(`确认删除持仓 ${h.code} ${h.name}？`)) return
  holdings.value = holdings.value.filter(x => x.code !== h.code)
  await saveHoldings()
}

function resetForm() {
  formCode.value = ''
  formCost.value = 0
  formQty.value = 0
  lookupName.value = ''
  lookupPrice.value = 0
}

let timer = null
onMounted(() => { load(); timer = setInterval(load, 3000) })
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<style scoped>
.positions-page { max-width: 1200px; padding-bottom: 60px; }
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 16px; }
.page-header h2 { font-size: 18px; font-weight: 600; }
.header-right { display: flex; align-items: center; gap: 12px; }
.balance { font-size: 16px; color: #4caf50; font-weight: 600; cursor: pointer; }
.balance-editing input { width: 150px; padding: 6px 10px; border-radius: 6px; border: 1px solid #4caf50; background: #0f0f23; color: #4caf50; font-size: 16px; font-weight: 600; text-align: right; outline: none; }
.btn-add {
  padding: 8px 16px; border-radius: 6px; border: none;
  background: #FF4D4F; color: #fff; font-size: 16px; cursor: pointer;
}
.positions-table { background: #1a1a2e; border-radius: 10px; overflow: hidden; font-size: 16px; }
.table-scroll { overflow-x: auto; }
.table-inner { min-width: 800px; }
.table-header, .table-inner > div {
  display: flex; align-items: center; min-height: 46px; padding: 4px 12px; gap: 0;
  border-bottom: 1px solid #1a1a26;
}
.table-header { background: #2a2a3e; color: #888; font-weight: 600; }
.table-inner > div:last-child { border-bottom: none; }
.table-inner > div.hl-strong { background: rgba(255,77,79,0.10); }
.table-inner > div.hl-watch { background: rgba(250,173,20,0.08); }
.table-inner > div.hl-signal { background: rgba(79,195,247,0.08); }
.table-inner > div.hl-danger { background: rgba(250,173,20,0.15); }

.col-code { flex: 1; font-family: monospace; color: #4fc3f7; text-align: center; }
.col-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.col-num  { flex: 1; text-align: center; }
.col-price{ flex: 1; text-align: center; }
.col-chg  { flex: 1; text-align: center; }
.col-chg.up   { color: #FF4D4F; font-weight: 700; }
.col-chg.down { color: #4caf50; font-weight: 700; }
.col-sig  { flex: 1; text-align: center; }
.col-sig.dim { color: #333; }
.col-score{ flex: 1; text-align: center; }
.col-score.strong { color: #FF4D4F; font-weight: 700; }
.col-score.watch  { color: #FAAD14; }
.col-sl   { flex: 1; text-align: center; white-space: nowrap; }
.sl-tp { color: #FF4D4F; }
.sl-div{ color: #333; margin: 0 2px; }
.sl-sel{ color: #4caf50; }
.positions-table .table-inner > div { cursor: pointer; }
.positions-table .table-inner > div:active { background: rgba(255,255,255,0.05); }

/* 操作面板（底部弹出） */
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
.sheet-danger { background: #FF4D4F; color: #fff; }
.sheet-cancel { background: #2a2a3e; color: #999; }

/* 底部操作栏 */
.bottom-bar {
  position: fixed; bottom: 68px; left: 0; right: 0;
  display: flex; align-items: center; gap: 12px;
  padding: 10px 16px;
  background: #1a1a2e; border-top: 1px solid #2a2a3e;
  z-index: 110;
}
.bottom-balance { flex: 1; font-size: 16px; color: #4caf50; font-weight: 600; cursor: pointer; }
.balance-editing input { width: 100%; padding: 8px 12px; border-radius: 6px; border: 1px solid #4caf50; background: #0f0f23; color: #4caf50; font-size: 16px; font-weight: 600; outline: none; }
.btn-add {
  padding: 10px 20px; border-radius: 8px; border: none;
  background: #FF4D4F; color: #fff; font-size: 16px; cursor: pointer; flex-shrink: 0;
}
.empty { text-align: center; padding: 60px; color: #555; font-size: 16px; }
.hint { color: #444; font-size: 16px; margin-top: 8px; }

/* modal */
.modal-overlay {
  position: fixed; top: 0; left: 0; width: 100%; height: 100%;
  background: rgba(0,0,0,0.6); display: flex; align-items: center; justify-content: center; z-index: 100;
}
.modal {
  background: #1a1a2e; border-radius: 10px; padding: 24px; width: 360px;
}
.modal-title { font-size: 16px; font-weight: 600; color: #e0e0e0; margin-bottom: 16px; }
.form-row { margin-bottom: 12px; display: flex; align-items: center; gap: 8px; }
.form-row label { width: 56px; color: #888; font-size: 16px; flex-shrink: 0; }
.form-row input {
  flex: 1; padding: 8px 12px; border-radius: 6px; border: 1px solid #333;
  background: #0f0f23; color: #e0e0e0; font-size: 16px; outline: none;
}
.form-row input:focus { border-color: #FF4D4F; }
.lookup-result { font-size: 16px; color: #4caf50; white-space: nowrap; }
.modal-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 16px; }
.btn-cancel {
  padding: 8px 20px; border-radius: 6px; border: 1px solid #333;
  background: transparent; color: #888; font-size: 16px; cursor: pointer;
}
.btn-confirm {
  padding: 8px 20px; border-radius: 6px; border: none;
  background: #FF4D4F; color: #fff; font-size: 16px; cursor: pointer;
}
.legend {
  margin-top: 12px; padding: 6px 12px; font-size: 16px; color: #666;
  background: #1a1a2e; border-radius: 6px; display: flex; align-items: center; gap: 12px; flex-wrap: wrap;
}
.lg-sep { color: #333; }
.lg-item { color: #666; }
.lg-dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 3px; vertical-align: middle; }
.lg-dot.up { background: #FF4D4F; }
.lg-dot.warn { background: #FAAD14; }
</style>
