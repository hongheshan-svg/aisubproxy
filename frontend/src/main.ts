import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { installElement } from './element'
import App from './App.vue'
import router from './router'
import './style.css'
import './styles/skins.css'
import './styles/user-theme.css'
import { useAuthStore } from './stores/auth'
import { initTheme, setSiteBrand, setSkin, applySiteThemeMode, type SkinId, type ThemeMode } from './theme'
import { i18n } from './i18n'
import { startNewVersionWatch } from './lib/newVersionWatch'
import { initLayoutMode } from './lib/layoutMode'

initTheme()
initLayoutMode()

async function loadPublicSite() {
  try {
    const r = await fetch('/api/v1/public/site')
    if (!r.ok) return
    const d = await r.json()
    // 品牌 + 整站主题：管理员在后台设定，对所有用户生效
    if (d.brand_name) setSiteBrand({ name: d.brand_name, sub: d.brand_sub || '' })
    // Logo 版本（没有 Logo 时 logo_version 为空串）
    setSiteBrand({ logoVersion: d.logo_version || '' })
    if (d.skin) setSkin(d.skin as SkinId)
    // 明暗：访客本机的选择优先；没选过就跟站点默认（只记站点默认，不当成访客的选择）
    if (d.theme_mode) applySiteThemeMode(d.theme_mode as ThemeMode)
  } catch {
    /* offline / first boot */
  }
}

const app = createApp(App)
const pinia = createPinia()

app.use(pinia)
app.use(router)
app.use(i18n)
installElement(app)

const authStore = useAuthStore(pinia)
authStore.restore()

// 不等站点配置：先按本机缓存的皮肤渲染，配置返回后再原子切换（首屏不再被一次网络请求卡住）。
app.mount('#app')
void loadPublicSite()
startNewVersionWatch()
