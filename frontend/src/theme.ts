// 整站主题包：颜色 + 字体 + 圆角 + 密度 + 导航 + 布局壳（原子应用，避免半套换肤）
import { ref, computed } from 'vue'
import { sideColorsFrom } from './lib/brandColor'

export type ThemeMode = 'light' | 'dark' | 'auto'
export type SkinId =
  | 'teal'
  | 'zovo'
  | 'terracotta'
  | 'ember'
  | 'ocean'
  | 'cyber'
  | 'forest'
  | 'violet'
  | 'slate'
  | 'rose'
  | 'noir'
  | 'paper'

/** 没记过皮肤、或记的编号不存在时用它 */
export const DEFAULT_SKIN: SkinId = 'teal'

const MODE_KEY = 'theme-mode'
/** 后台设的站点默认明暗：和访客自己的选择（MODE_KEY）分开存，管理员改了才能传到没选过的访客 */
const SITE_MODE_KEY = 'site-theme-mode'
const SKIN_KEY = 'site-skin'
const BRAND_KEY = 'site-brand'
// 与后端 /public/site 的默认站名一致（backend/internal/handler/settings.go）
export const DEFAULT_BRAND_NAME = 'Recharge Portal'
export const DEFAULT_BRAND_SUB = 'Account Upgrade Service'

export interface SiteBrand {
  name: string
  sub: string
  /** Logo 版本号，空串表示没有 Logo */
  logoVersion?: string
}

/** 一个皮肤在某种亮暗下的主色和按钮字色 */
export interface SkinTone {
  primary: string
  on: string
}

export interface SkinMeta {
  id: SkinId
  label: string
  labelEn: string
  swatch: string
  swatch2: string
  blurb: string
  heading: 'serif' | 'sans' | 'display' | 'mono'
  density: 'comfy' | 'compact' | 'airy'
  nav: 'pill' | 'underline' | 'block'
  /** 是否默认走暗色（如 cyber/ember/noir） */
  preferDark?: boolean
  /** 亮色下的主色与按钮字色（取自 style.css 该皮肤亮色块；用于 Element Plus、侧栏） */
  light: SkinTone
  /** 暗色下的主色与按钮字色（取自 style.css 该皮肤暗色块；没有暗色块的与 light 相同） */
  dark: SkinTone
  /** 该皮肤用到的 Google 字体（css2 family 参数），切换皮肤时按需加载 */
  fonts: string[]
}

// 各皮肤共用的等宽字体（金额、卡密、表格数字）
const MONO_FONT = 'JetBrains+Mono:wght@400;500;600'
const SERIF_SC = 'Noto+Serif+SC:wght@500;600;700'

export const SKINS: SkinMeta[] = [
  {
    id: 'teal',
    label: '深青自助',
    labelEn: 'Teal Kiosk',
    swatch: '#0F6B5F',
    swatch2: '#EEF5F3',
    blurb: '深青自助 · 大字大按钮',
    heading: 'sans',
    density: 'comfy',
    nav: 'block',
    light: { primary: '#0F6B5F', on: '#FFFFFF' },
    dark: { primary: '#3FB8A5', on: '#0C1A18' },
    // 系统字体，不加载 Google 字体
    fonts: [],
  },
  {
    // 夜间=曜夜（深黑 + 雾金），日间=流金（象牙 + 暗金）
    id: 'zovo',
    label: '曜夜雾金',
    labelEn: 'Night Gold',
    swatch: '#cbb079',
    swatch2: '#0c0c11',
    blurb: '曜夜雾金 · 玻璃面板',
    heading: 'display',
    density: 'comfy',
    nav: 'pill',
    light: { primary: '#8e7036', on: '#fffdf7' },
    dark: { primary: '#cbb079', on: '#07070a' },
    fonts: ['Manrope:wght@400;500;600;700', 'Sora:wght@500;600;700', 'Cormorant+Garamond:wght@500;600;700', SERIF_SC],
  },
  {
    id: 'terracotta',
    label: '赤陶奶油',
    labelEn: 'Terracotta',
    swatch: '#c0563a',
    swatch2: '#f2ede4',
    blurb: '衬线标题 · 釉面胶囊按钮 · 奶油纸面',
    heading: 'serif',
    density: 'comfy',
    nav: 'pill',
    light: { primary: '#b85238', on: '#ffffff' },
    dark: { primary: '#d9744f', on: '#1a130d' },
    fonts: ['Inter:wght@400;500;600;700', 'Source+Serif+4:opsz,wght@8..60,500;8..60,600;8..60,700', SERIF_SC],
  },
  {
    id: 'cyber',
    label: '赛博科技',
    labelEn: 'Cyber',
    swatch: '#22d3ee',
    swatch2: '#0b1020',
    blurb: '切角按钮 · HUD 角标面板 · 等宽大写 · 霓虹',
    heading: 'mono',
    density: 'compact',
    nav: 'block',
    preferDark: true,
    light: { primary: '#22d3ee', on: '#041016' },
    dark: { primary: '#22d3ee', on: '#041016' },
    fonts: ['DM+Sans:wght@400;500;600;700', 'Space+Grotesk:wght@500;600;700'],
  },
  {
    id: 'ocean',
    label: '苹果玻璃',
    labelEn: 'Ocean',
    swatch: '#2563eb',
    swatch2: '#eef4fb',
    blurb: '大圆角白卡 · 渐变胶囊按钮 · iOS 分段页签',
    heading: 'sans',
    density: 'compact',
    nav: 'block',
    light: { primary: '#0070e1', on: '#ffffff' },
    dark: { primary: '#0a84ff', on: '#0b1220' },
    fonts: ['Inter:wght@400;500;600;700', 'Outfit:wght@500;600;700'],
  },
  {
    id: 'ember',
    label: '熔岩霓光',
    labelEn: 'Ember',
    swatch: '#ff854a',
    swatch2: '#140b08',
    blurb: '熔岩渐变按钮 · 外发光掠光 · 渐变描边面板',
    heading: 'display',
    density: 'comfy',
    nav: 'pill',
    preferDark: true,
    light: { primary: '#ff854a', on: '#1a0c08' },
    dark: { primary: '#ff854a', on: '#1a0c08' },
    fonts: ['Inter:wght@400;500;600;700', 'Space+Grotesk:wght@500;600;700'],
  },
  {
    id: 'forest',
    label: '薄荷清新',
    labelEn: 'Forest',
    swatch: '#059669',
    swatch2: '#eef6f1',
    blurb: '薄荷渐变底 · 左侧色条卡片 · 实心翠绿按钮',
    heading: 'sans',
    density: 'airy',
    nav: 'pill',
    light: { primary: '#05825b', on: '#ffffff' },
    dark: { primary: '#34d399', on: '#0c1612' },
    fonts: ['Inter:wght@400;500;600;700', 'Outfit:wght@500;600;700'],
  },
  {
    id: 'violet',
    label: '靛紫重磅',
    labelEn: 'Violet',
    swatch: '#7c3aed',
    swatch2: '#f4f0fb',
    blurb: '极粗标题 · 墨蓝主按钮 · 方块步骤号',
    heading: 'display',
    density: 'comfy',
    nav: 'underline',
    light: { primary: '#7c3aed', on: '#ffffff' },
    dark: { primary: '#a78bfa', on: '#120e1c' },
    fonts: ['Inter:wght@400;500;600;700', 'Space+Grotesk:wght@500;600;700'],
  },
  {
    id: 'slate',
    label: '黑白控制台',
    labelEn: 'Slate',
    swatch: '#475569',
    swatch2: '#f1f5f9',
    blurb: '黑色胶囊按钮 · 等宽小标签 · 超大标题',
    heading: 'mono',
    density: 'compact',
    nav: 'block',
    light: { primary: '#0d0d0d', on: '#ffffff' },
    dark: { primary: '#f4f4f1', on: '#0b0b0b' },
    fonts: ['DM+Sans:wght@400;500;600;700'],
  },
  {
    id: 'rose',
    label: '玫瑰杂志',
    labelEn: 'Rose',
    swatch: '#e11d48',
    swatch2: '#fdf2f4',
    blurb: '斜体衬线大标题 · 方角大写按钮 · 下划线页签',
    heading: 'serif',
    density: 'airy',
    nav: 'underline',
    light: { primary: '#d81c45', on: '#ffffff' },
    dark: { primary: '#fb7185', on: '#180a0e' },
    fonts: ['Inter:wght@400;500;600;700', 'Instrument+Serif:ital@0;1', 'Source+Serif+4:opsz,wght@8..60,500;8..60,600;8..60,700', SERIF_SC],
  },
  {
    id: 'noir',
    label: '粗野主义',
    labelEn: 'Noir',
    swatch: '#e5e5e5',
    swatch2: '#0a0a0a',
    blurb: '粗描边 · 硬投影 · 按下回弹',
    heading: 'display',
    density: 'compact',
    nav: 'block',
    preferDark: true,
    light: { primary: '#fafafa', on: '#0a0a0a' },
    dark: { primary: '#fafafa', on: '#0a0a0a' },
    fonts: ['DM+Sans:wght@400;500;600;700', 'Space+Grotesk:wght@500;600;700'],
  },
  {
    id: 'paper',
    label: '纸感笔记',
    labelEn: 'Paper',
    swatch: '#8b7355',
    swatch2: '#f7f3ea',
    blurb: '横线纸底 · 胶带面板 · 印章按钮',
    heading: 'serif',
    density: 'comfy',
    nav: 'underline',
    light: { primary: '#8b5e34', on: '#fffaf0' },
    dark: { primary: '#d4a574', on: '#1c1812' },
    fonts: ['Source+Serif+4:opsz,wght@8..60,400;8..60,500;8..60,600;8..60,700', SERIF_SC],
  },
]

function readSkin(): SkinId {
  try {
    const v = localStorage.getItem(SKIN_KEY) as SkinId
    if (SKINS.some((s) => s.id === v)) return v
  } catch { /* 取不到本机记录，用默认皮肤 */ }
  return DEFAULT_SKIN
}

function isMode(v: unknown): v is ThemeMode {
  return v === 'light' || v === 'dark' || v === 'auto'
}

/** 访客自己选过的明暗；没选过返回 null */
function readOwnMode(): ThemeMode | null {
  try {
    const v = localStorage.getItem(MODE_KEY)
    if (isMode(v)) return v
  } catch { /* 取不到本机记录 */ }
  return null
}

// 访客自己的选择 > 上次记下的站点默认 > 跟随系统
function readMode(): ThemeMode {
  const own = readOwnMode()
  if (own) return own
  try {
    const v = localStorage.getItem(SITE_MODE_KEY)
    if (isMode(v)) return v
  } catch { /* 取不到本机记录，按站点设置 */ }
  return 'auto'
}

// 默认跟随系统（后台 theme_mode 没设过也是 auto）；默认皮肤是深青自助
export const themeMode = ref<ThemeMode>(readMode())
export const siteSkin = ref<SkinId>(readSkin())

function loadBrand(): SiteBrand {
  try {
    const raw = localStorage.getItem(BRAND_KEY)
    if (raw) {
      const o = JSON.parse(raw)
      if (o?.name) {
        return {
          name: String(o.name),
          sub: String(o.sub || ''),
          logoVersion: o.logoVersion ? String(o.logoVersion) : undefined,
        }
      }
    }
  } catch { /* ignore */ }
  return { name: DEFAULT_BRAND_NAME, sub: DEFAULT_BRAND_SUB }
}

export const siteBrand = ref<SiteBrand>(loadBrand())

function systemPrefersDark(): boolean {
  return !!(typeof window !== 'undefined' && window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches)
}

/** 当前是否按暗色呈现：只有暗色的皮肤（preferDark）永远算暗，.dark 类、data-tone、配色三处同一个说法 */
export function isDark(): boolean {
  return effectiveDark(themeMode.value, currentSkinMeta.value, systemPrefersDark())
}

/** 这个皮肤在当前模式下是否按暗色呈现（preferDark 皮肤永远算暗；data-tone、侧栏、Element Plus 都以它为准） */
export function effectiveDark(mode: ThemeMode, skin: SkinMeta, systemDark: boolean): boolean {
  return mode === 'dark' || (mode === 'auto' && systemDark) || !!skin.preferDark
}

export function skinTone(skin: SkinMeta, dark: boolean): SkinTone {
  return dark ? skin.dark : skin.light
}

export const currentSkinMeta = computed(() => SKINS.find((s) => s.id === siteSkin.value) || SKINS[0])

/** 当前皮肤只有暗色：明暗切换没有意义，界面上把切换按钮藏起来或置灰 */
export const themeLocked = computed(() => !!currentSkinMeta.value.preferDark)

/** 由主色生成 EP light/dark 阶梯 */
function hexToRgb(hex: string): [number, number, number] | null {
  const h = hex.replace('#', '').trim()
  if (h.length === 3) {
    return [parseInt(h[0] + h[0], 16), parseInt(h[1] + h[1], 16), parseInt(h[2] + h[2], 16)]
  }
  if (h.length !== 6) return null
  return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)]
}

function mix(a: number, b: number, t: number) {
  return Math.round(a + (b - a) * t)
}

function mixHex(hex: string, toward: 'white' | 'black', t: number): string {
  const rgb = hexToRgb(hex)
  if (!rgb) return hex
  const target = toward === 'white' ? 255 : 0
  const [r, g, b] = rgb.map((c) => mix(c, target, t))
  return `#${[r, g, b].map((x) => x.toString(16).padStart(2, '0')).join('')}`
}

function syncElementPlus(primary: string, primaryOn: string, dark: boolean) {
  const root = document.documentElement
  const set = (k: string, v: string) => root.style.setProperty(k, v)
  // 「浅色阶梯」是标签/选中行等的底色：暗色下要往黑混，往白混会在深底上冒出一块块近白色
  const toward = dark ? 'black' : 'white'
  set('--el-color-primary', primary)
  set('--el-color-primary-light-3', mixHex(primary, toward, 0.3))
  set('--el-color-primary-light-5', mixHex(primary, toward, 0.5))
  set('--el-color-primary-light-7', mixHex(primary, toward, 0.7))
  set('--el-color-primary-light-8', mixHex(primary, toward, 0.8))
  set('--el-color-primary-light-9', mixHex(primary, toward, 0.9))
  set('--el-color-primary-dark-2', mixHex(primary, dark ? 'white' : 'black', 0.2))
  // 按钮跟主色，禁止写死赤陶
  set('--el-button-bg-color', primary)
  set('--el-button-border-color', primary)
  set('--el-button-hover-bg-color', mixHex(primary, 'white', 0.12))
  set('--el-button-hover-border-color', mixHex(primary, 'white', 0.12))
  set('--el-button-active-bg-color', mixHex(primary, 'black', 0.12))
  set('--el-button-active-border-color', mixHex(primary, 'black', 0.12))
  set('--el-button-text-color', primaryOn)
  set('--el-color-white', primaryOn)
  set('--el-font-family', 'var(--font-sans)')
  set('--el-border-radius-base', 'var(--radius-md)')
  // 同步语义色给 EP
  set('--el-bg-color', 'var(--surface)')
  set('--el-bg-color-page', 'var(--bg)')
  // 弹层（下拉/弹窗/提示）必须不透明：玻璃质感皮肤的 --surface 是半透明的
  set('--el-bg-color-overlay', 'var(--surface-solid)')
  set('--el-fill-color-blank', 'var(--surface)')
  set('--el-text-color-primary', 'var(--ink)')
  set('--el-text-color-regular', 'var(--ink-2)')
  set('--el-text-color-secondary', 'var(--ink-3)')
  set('--el-border-color', 'var(--brd)')
  set('--el-border-color-light', 'var(--brd)')
  set('--el-border-color-lighter', 'var(--brd)')
  set('--el-mask-color', 'rgba(0,0,0,.45)')
}

function applyMode() {
  document.documentElement.classList.toggle('dark', isDark())
}

/** 浏览器地址栏颜色跟着当前皮肤的底色走（取样式表里算出来的 --bg，取不到就不动） */
function syncThemeColor(root: HTMLElement) {
  try {
    const meta = document.querySelector('meta[name="theme-color"]')
    if (!meta || typeof window.getComputedStyle !== 'function') return
    const bg = window.getComputedStyle(root).getPropertyValue('--bg').trim()
    if (/^#[0-9a-fA-F]{3,8}$/.test(bg)) meta.setAttribute('content', bg)
  } catch { /* 地址栏颜色只是锦上添花 */ }
}

function applySkin() {
  const meta = currentSkinMeta.value
  const root = document.documentElement
  // 原子写入：同一帧内改完所有属性，减少半套样式
  root.setAttribute('data-skin', meta.id)
  root.setAttribute('data-heading', meta.heading)
  root.setAttribute('data-density', meta.density)
  root.setAttribute('data-nav', meta.nav)
  const dark = isDark()
  const tone = skinTone(meta, dark)
  root.setAttribute('data-tone', dark ? 'dark' : 'light')
  syncElementPlus(tone.primary, tone.on, dark)
  const side = sideColorsFrom(tone.primary)
  root.style.setProperty('--side-bg', side.bg)
  root.style.setProperty('--side-ink', side.ink)
  root.style.setProperty('--side-muted', side.muted)
  root.style.setProperty('--side-active', tone.primary)
  root.style.setProperty('--side-active-on', tone.on)
  root.style.colorScheme = dark ? 'dark' : 'light'
  syncThemeColor(root)
  if (skinFontsAllowed) ensureSkinFonts(meta)
}

// Google 字体只在后台页面加载；用户端不拉。路由切换时由 router 调 setSkinFontsAllowed。
let skinFontsAllowed = false
export function setSkinFontsAllowed(on: boolean): void {
  const turnedOn = on && !skinFontsAllowed
  skinFontsAllowed = on
  if (turnedOn) ensureSkinFonts(currentSkinMeta.value)
}

// 字体按皮肤按需加载：原来 index.html 一次拉 9 个字族（含中文 Noto Sans/Serif SC）且阻塞渲染。
// 不阻塞首屏：字体到了再换（display=swap），没到之前用系统字体。
const loadedFontHref = new Set<string>()
function ensureSkinFonts(meta: SkinMeta) {
  if (meta.fonts.length === 0) return // 系统字体皮肤：连等宽字体也不拉
  const families = [...meta.fonts, MONO_FONT].map((f) => `family=${f}`).join('&')
  const href = `https://fonts.googleapis.com/css2?${families}&display=swap`
  if (loadedFontHref.has(href) || document.querySelector(`link[data-skin-fonts="${meta.id}"]`)) {
    loadedFontHref.add(href)
    return
  }
  loadedFontHref.add(href)
  const link = document.createElement('link')
  link.rel = 'stylesheet'
  link.href = href
  link.dataset.skinFonts = meta.id
  document.head.appendChild(link)
}

/** 统一入口：皮肤 + 明暗 一次刷完 */
export function applyThemeAll() {
  applyMode()
  applySkin()
}

export function setTheme(mode: ThemeMode) {
  themeMode.value = mode
  try { localStorage.setItem(MODE_KEY, mode) } catch { /* 存不了就只在本次生效 */ }
  applyThemeAll()
}

/**
 * 站点默认明暗（后台「外观」里设的）：只在访客自己没选过时生效，且不写进访客自己的偏好。
 * 单独存一份，下次打开页面首屏就能先用上，不会先亮后暗地闪。
 */
export function applySiteThemeMode(mode: ThemeMode) {
  if (!isMode(mode)) return
  try { localStorage.setItem(SITE_MODE_KEY, mode) } catch { /* 存不了就只在本次生效 */ }
  if (readOwnMode()) return
  themeMode.value = mode
  applyThemeAll()
}

export function toggleTheme() {
  if (themeLocked.value) return
  setTheme(isDark() ? 'light' : 'dark')
}

export function setSkin(id: SkinId) {
  if (!SKINS.some((s) => s.id === id)) return
  siteSkin.value = id
  try { localStorage.setItem(SKIN_KEY, id) } catch { /* 存不了就只在本次生效 */ }
  // 只有暗色的皮肤由 isDark() 一律按暗色呈现，不再改写访客本机的明暗选择
  applyThemeAll()
}

export function setSiteBrand(patch: Partial<SiteBrand>) {
  siteBrand.value = {
    name: (patch.name ?? siteBrand.value.name).trim() || DEFAULT_BRAND_NAME,
    sub: (patch.sub ?? siteBrand.value.sub).trim(),
    logoVersion: patch.logoVersion ?? siteBrand.value.logoVersion,
  }
  try { localStorage.setItem(BRAND_KEY, JSON.stringify(siteBrand.value)) } catch { /* 存不了就只在本次生效 */ }
}

export function initTheme() {
  applyThemeAll()
  if (window.matchMedia) {
    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
      if (themeMode.value === 'auto') applyThemeAll()
    })
  }
}
