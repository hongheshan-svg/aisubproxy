import { createI18n } from 'vue-i18n'
import zh from './locales/zh'
import en from './locales/en'
import { pickLocale, readSavedLocale, STORAGE_KEY } from './detect'

export type AppLocale = 'zh' | 'en'

function detectLocale(): AppLocale {
  return pickLocale(readSavedLocale())
}

const locale = detectLocale()
document.documentElement.lang = locale === 'zh' ? 'zh-CN' : 'en'

export const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale,
  fallbackLocale: 'zh',
  messages: { zh, en },
})

export function setLocale(next: AppLocale) {
  i18n.global.locale.value = next
  document.documentElement.lang = next === 'zh' ? 'zh-CN' : 'en'
  try { localStorage.setItem(STORAGE_KEY, next) } catch { /* 存不了就只在本次生效 */ }
}
