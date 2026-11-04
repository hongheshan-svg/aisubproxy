export type DetectedLocale = 'zh' | 'en'

export const STORAGE_KEY = 'locale'

/**
 * 选择界面语言：用户手动保存过的优先；否则跟随浏览器，中文浏览器用中文，其余用英文。
 * 注意：「非中文默认英文」是有意为之，中英文文案都已提供（见 CHANGELOG「中英文都有」）。
 */
export function pickLocale(saved: string | null | undefined, browserLanguage: string | null | undefined): DetectedLocale {
  if (saved === 'zh' || saved === 'en') return saved
  return (browserLanguage || '').toLowerCase().startsWith('zh') ? 'zh' : 'en'
}

export function readSavedLocale(): string | null {
  try {
    return localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}
