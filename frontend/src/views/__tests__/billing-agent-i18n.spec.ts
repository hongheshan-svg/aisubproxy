// 账单查询页、代理换码页的文字全部走语言包：中文与改版前逐字相同，英文界面没有中文；后端的每个错误码在两种语言里都有文案。
import { describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { createSSRApp, defineComponent, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'

// 页头会引入主题和切换语言（碰 document / localStorage），只关心页头拿到的标题和说明
vi.mock('../../components/UserPageHeader.vue', () => ({
  default: defineComponent({ props: ['title', 'subtitle'], setup: (p: any) => () => h('header', [p.title, p.subtitle]) }),
}))
vi.mock('../../i18n', () => ({ i18n: { global: { t: (k: string) => k } } }))

import AgentSwapView from '../user/AgentSwapView.vue'
import BillingCheckView from '../user/BillingCheckView.vue'

const SRC = join(__dirname, '..', '..')

async function render(comp: any, locale: 'zh' | 'en') {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
  router.push('/')
  await router.isReady()
  const app = createSSRApp(defineComponent({ render: () => h(comp) }))
  app.use(router)
  app.use(createI18n({ legacy: false, globalInjection: true, locale, fallbackLocale: 'zh', messages: { zh, en } }))
  return renderToString(app)
}

// 逐字符跳过 <...> 取正文，同时取出 placeholder
function visible(html: string) {
  let out = ''
  let inTag = false
  for (const ch of html) {
    if (ch === '<') inTag = true
    else if (ch === '>') inTag = false
    else if (!inTag) out += ch
  }
  const placeholders = [...html.matchAll(/placeholder="([^"]*)"/g)].map((m) => m[1])
  return (out + ' ' + placeholders.join(' ')).replace(/\s+/g, ' ').trim()
}

const CJK = /[一-鿿]/
// 模板换行改写后标签之间的空白会不同（版式上看不出来），逐字比较时忽略空白
const noSpace = (s: string) => s.replace(/\s+/g, '')

// 两页中文首屏的可见文字（页头、正文、输入框占位、页脚），逐字。代理换码页与改版前相同；账单页的说明在凭卡密查改成本站开通记录时改过
const SWAP_ZH = '卡密补发仅限充值失败且未扣款的卡密。验证通过后将作废旧码并下发一张同套餐新码。 代理密码 显示 失败的完整卡密 换发新卡密常见问题批量兑换账单查询 管理员下发的个人密码 粘贴完整 CDK'
const BILL_ZH = '账单查询用卡密查看本站开通记录，或粘贴登录信息查询 ChatGPT 的订阅状态与账单。卡密查询登录信息查询 输入兑换用的卡密，查看本站开通它时的记录：开通时间、付款金额、到期时间。要看 ChatGPT 的实时账单，请改用「登录信息查询」。 CDK 卡密 查询订阅与账单常见问题批量兑换账单查询 SXC-XXXX-XXXX-XXXX-XXXX'

describe('两页渲染', () => {
  it('代理换码页：中文与改版前逐字相同', async () => {
    expect(noSpace(visible(await render(AgentSwapView, 'zh')))).toBe(noSpace(SWAP_ZH))
  })
  it('账单查询页：中文首屏逐字相同', async () => {
    expect(noSpace(visible(await render(BillingCheckView, 'zh')))).toBe(noSpace(BILL_ZH))
  })
  it('英文界面：两页可见文字里没有中文', async () => {
    for (const comp of [AgentSwapView, BillingCheckView]) {
      const s = visible(await render(comp, 'en'))
      expect(s).not.toMatch(CJK)
      expect(s.length).toBeGreaterThan(40)
    }
  })
  it('英文界面：关键文字是英文', async () => {
    const swap = visible(await render(AgentSwapView, 'en'))
    expect(swap).toContain('Reissue code')
    const bill = visible(await render(BillingCheckView, 'en'))
    expect(bill).toContain('Billing')
  })
})

// 页面里还有只在点击后才出现的文字（结果区、报错、toast）：SSR 看不到，改查源码——模板和脚本（去掉注释）里不能再有中文，
// 用到的每个 t('…') 键两种语言都要有。
// cutBlocks 删掉所有「open … close」整段（含两端）；没有 close 时删到末尾。按位置截取、反复到没有为止，不用正则替换
function cutBlocks(src: string, open: string, close: string) {
  let out = src
  for (let i = out.indexOf(open); i !== -1; i = out.indexOf(open)) {
    const j = out.indexOf(close, i + open.length)
    out = out.slice(0, i) + (j === -1 ? '' : out.slice(j + close.length))
  }
  return out
}
function stripComments(src: string) {
  return cutBlocks(src, '<!--', '-->').replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
}
function lookup(o: any, path: string): unknown {
  return path.split('.').reduce((a, k) => (a == null ? a : a[k]), o)
}

for (const [name, file, ns] of [
  ['代理换码页', 'views/user/AgentSwapView.vue', 'agentSwap'],
  ['账单查询页', 'views/user/BillingCheckView.vue', 'billingCheck'],
] as const) {
  describe(name + '源码', () => {
    const src = stripComments(readFileSync(join(SRC, file), 'utf8'))
    const code = cutBlocks(src, '<style', '</style>')
    it('模板和脚本里没有写死的中文', () => {
      expect(code.split('\n').filter((l) => CJK.test(l))).toEqual([])
    })
    it(`用到的 ${ns}.* 键在中英文语言包里都有`, () => {
      const keys = [...code.matchAll(/\bt\(\s*'([\w.]+)'/g)].map((m) => m[1]).filter((k) => k.startsWith(ns + '.'))
      expect(keys.length).toBeGreaterThan(5)
      for (const k of new Set(keys)) {
        expect(typeof lookup(zh, k), `zh ${k}`).toBe('string')
        expect(typeof lookup(en, k), `en ${k}`).toBe('string')
      }
    })
  })
}

// 后端错误码表（写死在这里；改了后端的码，这里要同步）
const SWAP_CODES = [
  'rate_limited', 'disabled', 'invalid_request', 'locked', 'wrong_password', 'code_required', 'provider_unsupported',
  'code_not_found', 'no_failed_record', 'conflict', 'daily_limit', 'unavailable',
  // 本站码（MarsWallex）路径和分派层新增的码
  'code_unrecognized', 'service_unavailable', 'busy', 'not_eligible_unpaid',
  // 两个接口共用的限流中间件（public_guard.go）
  'body_too_large',
]
const BILLING_CODES = ['invalid_request', 'input_required', 'session_invalid', 'lookup_failed', 'cdk_not_found', 'cdk_not_redeemed', 'cdk_replaced', 'rate_limited', 'body_too_large']

describe('错误码都有中英文文案', () => {
  const i18n = (locale: 'zh' | 'en') => createI18n({ legacy: false, locale, messages: { zh, en } }).global
  for (const [ns, codes] of [['agentSwap', SWAP_CODES], ['billingCheck', BILLING_CODES]] as const) {
    for (const locale of ['zh', 'en'] as const) {
      it(`${ns}.errors 在 ${locale} 下每个码都有文案`, () => {
        const g = i18n(locale)
        for (const c of codes) {
          expect(g.te(`${ns}.errors.${c}`), `${locale} ${ns}.errors.${c}`).toBe(true)
          expect(String(g.t(`${ns}.errors.${c}`, { retry_after_minutes: 5, limit: 3 })).trim().length).toBeGreaterThan(0)
        }
      })
    }
  }
  it('代理换码：冲突按状态分文案', () => {
    for (const c of ['conflict_pending', 'conflict_unknown']) {
      expect(typeof lookup(zh, `agentSwap.errors.${c}`)).toBe('string')
      expect(typeof lookup(en, `agentSwap.errors.${c}`)).toBe('string')
    }
  })
  it('中文文案与后端原文一致（换码口令锁定、额度用完带数字）', () => {
    const g = i18n('zh')
    expect(g.t('agentSwap.errors.locked', { retry_after_minutes: 37 })).toBe('换码口令错误次数过多，已暂停换码，请 37 分钟后再试')
    expect(g.t('agentSwap.errors.daily_limit', { limit: 20 })).toBe('代理换码已达今日上限（20 张），请联系管理员')
    expect(g.t('agentSwap.errors.wrong_password')).toBe('密码错误')
    expect(g.t('agentSwap.errors.code_unrecognized')).toBe('无法识别的卡密')
    expect(g.t('agentSwap.errors.busy')).toBe('该卡密正在处理中，请勿重复提交')
    expect(g.t('agentSwap.errors.not_eligible_unpaid')).toBe('不符合换码条件（需充值失败且未发起支付）')
    // 凭卡密查账单：和后端 gpt_billing.go 的原文逐字相同
    expect(g.t('billingCheck.errors.cdk_not_found')).toBe('未找到这张卡密')
    expect(g.t('billingCheck.errors.cdk_not_redeemed')).toBe('这张卡密还没开通成功')
    expect(g.t('billingCheck.errors.cdk_replaced')).toBe('这张卡密已换发新码，请用新码查询')
    expect(g.t('billingCheck.errors.lookup_failed')).toBe('查询失败，请稍后再试')
  })
  it('英文文案没有中文', () => {
    const walk = (o: any, p: string): string[] => Object.entries(o).flatMap(([k, v]) => (typeof v === 'string' ? (CJK.test(v) ? [`${p}.${k}`] : []) : walk(v, `${p}.${k}`)))
    expect([...walk((en as any).agentSwap, 'agentSwap'), ...walk((en as any).billingCheck, 'billingCheck')]).toEqual([])
  })
})
