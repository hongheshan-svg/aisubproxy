export type DetectedLocale = 'zh' | 'en'

export const STORAGE_KEY = 'locale'

/**
 * 选择界面语言：用户手动保存过的优先，否则一律默认中文。
 * 默认中文是有意为之（升级前用户看到的就是中文，不改变可见行为），
 * 不跟随浏览器语言；想看英文的用户在页面上手动切换，选择会保存下来。
 */
export function pickLocale(saved: string | null | undefined): DetectedLocale {
  if (saved === 'zh' || saved === 'en') return saved
  return 'zh'
}

export function readSavedLocale(): string | null {
  try {
    return localStorage.getItem(STORAGE_KEY)
  } catch {
    return null
  }
}
