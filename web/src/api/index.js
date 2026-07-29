const BASE = ''
const STORAGE_KEY = 'liangzai_token'
const STORAGE_SERVER = 'liangzai_server_url'
const STORAGE_ACCOUNT = 'liangzai_account'

function baseUrl() {
  return localStorage.getItem(STORAGE_SERVER) || ''
}

function getToken() {
  return localStorage.getItem(STORAGE_KEY)
}

function storeAuth(token, account, expiresAt) {
  localStorage.setItem(STORAGE_KEY, token)
  localStorage.setItem(STORAGE_ACCOUNT, account || '')
}

export function clearAuth() {
  localStorage.removeItem(STORAGE_KEY)
  localStorage.removeItem(STORAGE_ACCOUNT)
}

export function isLoggedIn() {
  return !!getToken()
}

export function getAccount() {
  return localStorage.getItem(STORAGE_ACCOUNT) || ''
}

export function getStoredServer() {
  return localStorage.getItem(STORAGE_SERVER) || ''
}

export function setStoredServer(url) {
  localStorage.setItem(STORAGE_SERVER, url)
}

async function request(path, opts = {}) {
  const url = baseUrl() + path
  const headers = { 'Content-Type': 'application/json', ...opts.headers }
  const token = getToken()
  if (token) headers['Authorization'] = 'Bearer ' + token

  const res = await fetch(url, {
    method: opts.method || 'GET',
    headers,
    body: opts.data ? JSON.stringify(opts.data) : undefined,
  })

  if (!res.ok) {
    if (res.status === 401) {
      clearAuth()
      throw new Error('登录已过期')
    }
    const body = await res.json().catch(() => ({}))
    throw new Error(body.error || `请求失败(${res.status})`)
  }
  return res.json()
}

export async function login(username, password) {
  const url = baseUrl() + '/api/auth/login'
  const res = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  if (!res.ok) {
    const err = await res.json().catch(() => ({}))
    throw new Error(err.error || '登录失败')
  }
  const data = await res.json()
  storeAuth(data.token, data.account, data.expires_at)
  return data
}

export async function fetchSignals() {
  return request('/api/signals')
}

export async function fetchStatus() {
  return request('/api/status')
}

export async function fetchAlerts() {
  return request('/api/alerts')
}

export async function fetchHoldings() {
  return request('/api/holdings')
}

export async function updateHoldings(data) {
  return request('/api/holdings', { method: 'POST', data })
}

export async function fetchSectorHot() {
  return request('/api/sector/hot')
}

export async function fetchSnapshot() {
  return request('/api/snapshot')
}

export async function fetchHotSnapshot() {
  return request('/api/snapshot/hot')
}

export async function fetchEvaluations() {
  return request('/api/evaluations')
}

export async function fetchIPOCalendar() {
  return request('/api/ipo/calendar')
}

export async function fetchStockLookup(code) {
  return request('/api/stock/lookup?code=' + encodeURIComponent(code))
}

export async function fetchNews(all) {
  return request(all ? '/api/news?all=true' : '/api/news')
}

export async function fetchWatchlist() {
  return request('/api/watchlist')
}

export async function addWatchlist(code) {
  return request('/api/watchlist', { method: 'POST', data: { code } })
}

export async function removeWatchlist(code) {
  return request('/api/watchlist', { method: 'DELETE', data: { code } })
}

export async function actionSignal(code, action) {
  return request('/api/action', { method: 'POST', data: { code, action } })
}

export async function testNotification() {
  return request('/api/notify-test', { method: 'POST' })
}

// ── 市场时段追踪（非交易时段仅首次加载） ──

let _lastSession = -1

export function getLastSession() { return _lastSession }

export function setLastSession(s) { _lastSession = s }

export function isNewSession(session) {
  if (session === _lastSession) return false
  _lastSession = session
  return true
}

export function isTradingSession(session) {
  return session === 1 || session === 3 // SessionMorningTrade=1, SessionAfternoonTrade=3
}

// ── SSE ──

let sse = null
let sseCallbacks = []

export function onSSE(fn) {
  sseCallbacks.push(fn)
  return () => { sseCallbacks = sseCallbacks.filter(f => f !== fn) }
}

export function connectSSE() {
  if (sse) return
  const token = getToken()
  if (!token) return
  sse = new EventSource(baseUrl() + '/api/events?token=' + encodeURIComponent(token))
  sse.onmessage = (e) => {
    try {
      const msg = JSON.parse(e.data)
      sseCallbacks.forEach(fn => fn(msg))
    } catch (_) {}
  }
  sse.onerror = () => {
    disconnectSSE()
    setTimeout(connectSSE, 3000)
  }
}

export function disconnectSSE() {
  if (sse) { sse.close(); sse = null }
}
