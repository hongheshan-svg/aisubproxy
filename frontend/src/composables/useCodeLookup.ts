// 首页和兑换页第 ① 屏共用的卡密查询：去首尾空白、进行中不重复发、出错给买家看得懂的话。
import { ref } from 'vue'
import { i18n } from '../i18n'
import type { LookupResult } from '../lib/aftersale'
import { lookupAftersale } from '../lib/aftersaleClient'

export function useCodeLookup(deps: { fetchFn?: typeof fetch } = {}) {
  const busy = ref(false)
  const result = ref<LookupResult | null>(null)
  const error = ref('')
  const doFetch = deps.fetchFn ?? ((...a: Parameters<typeof fetch>) => fetch(...a))
  const t = (k: string) => i18n.global.t(`redeemUi.errors.${k}`) as string

  async function submit(raw: string) {
    // 回车加点按钮、网速慢时连点：只认第一次
    if (busy.value || !raw.trim()) return
    busy.value = true
    error.value = ''
    result.value = null
    let status = 0
    try {
      const r = await lookupAftersale(
        raw,
        { notFound: t('homeNotFound'), network: t('homeNetwork') },
        async (...a) => {
          const resp = await doFetch(...a)
          status = resp.status
          return resp
        },
      )
      result.value = r.result ?? null
      // 后端的 400 文字是中文，英文界面也会照搬，统一换成语言包里的说法
      error.value = r.error && status === 400 ? t('homeFormat') : r.error
    } finally {
      busy.value = false
    }
  }

  function clear() {
    result.value = null
    error.value = ''
  }

  return { busy, result, error, submit, clear }
}
