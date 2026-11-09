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

  // 每次查询一个序号；clear() 作废在途的请求，它返回后不再写结果
  let seq = 0

  async function submit(raw: string) {
    // 回车加点按钮、网速慢时连点：只认第一次
    if (busy.value || !raw.trim()) return
    const mine = ++seq
    busy.value = true
    error.value = ''
    result.value = null
    let status = 0
    let ok = false
    try {
      const r = await lookupAftersale(
        raw,
        { notFound: t('homeNotFound'), network: t('homeNetwork') },
        async (...a) => {
          const resp = await doFetch(...a)
          status = resp.status
          ok = resp.ok
          return resp
        },
      )
      if (mine !== seq) return
      result.value = r.result ?? null
      error.value = r.error ? errorText(r.error, status, ok) : ''
    } finally {
      if (mine === seq) busy.value = false
    }
  }

  // 后端的错误文字是中文，英文界面也会照搬，所以按状态码统一换成语言包的说法
  function errorText(fallback: string, status: number, ok: boolean): string {
    if (status === 0 || status === 404) return fallback // 网络异常、找不到：lookupAftersale 已用语言包文字
    if (status === 400) return t('homeFormat')
    if (status === 429) return t('rateLimited')
    if (status >= 500) return t('homeNetwork')
    return ok ? fallback : t('badReply')
  }

  function clear() {
    seq++
    busy.value = false
    result.value = null
    error.value = ''
  }

  return { busy, result, error, submit, clear }
}
