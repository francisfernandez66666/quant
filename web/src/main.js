import { createApp } from 'vue'
import { createRouter, createWebHashHistory } from 'vue-router'
import App from './App.vue'
import Dashboard from './pages/Dashboard.vue'
import Signals from './pages/Signals.vue'
import Watchlist from './pages/Watchlist.vue'
import Positions from './pages/Positions.vue'
import Hotspot from './pages/Hotspot.vue'
import MsgCenter from './pages/MsgCenter.vue'
import Settings from './pages/Settings.vue'

const routes = [
  { path: '/', redirect: '/dashboard' },
  { path: '/dashboard', component: Dashboard },
  { path: '/signals', component: Signals },
  { path: '/watchlist', component: Watchlist },
  { path: '/positions', component: Positions },
  { path: '/hotspot', component: Hotspot },
  { path: '/msgcenter', component: MsgCenter },
  { path: '/settings', component: Settings },
]

const router = createRouter({ history: createWebHashHistory(), routes })

const app = createApp(App)
app.use(router)
app.mount('#app')
