// F2-21：批量兑换页的进度列表和导入预览里的邮箱打码，和单张兑换页用同一个 maskEmail；导出的成功表格用买家自己
// 提供的完整邮箱（条目的 ownEmail），不用服务端回的打码邮箱（FE2 审查重要 2）。数据都是假的。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'
import ExcelImportBlock from '../../components/ExcelImportBlock.vue'
import { ownEmailOf, successRows, unknownRows } from '../../lib/batch-redeem'

const read = (p: string) => readFileSync(resolve(__dirname, '../..', p), 'utf8')
const page = read('views/user/BatchRedeemView.vue')

async function renderImport(emails: string[]) {
  const app = createSSRApp({
    render: () =>
      h(ExcelImportBlock, {
        sessionPool: emails.map((email) => ({ email, session: '{"sessionToken":"x"}', accessToken: '', gptPassword: '', emailPassword: '' })),
        importMsg: '',
        importing: false,
      }),
  })
  app.use(createI18n({ legacy: false, globalInjection: true, locale: 'zh', fallbackLocale: 'zh', messages: { zh, en } }))
  return renderToString(app)
}

describe('批量兑换页邮箱打码（F2-21）', () => {
  it('导入预览列表：只显示打码后的邮箱', async () => {
    const html = await renderImport(['buyer@example.com', 'ab@example.org'])
    expect(html).toContain('bu***@example.com')
    expect(html).toContain('a***@example.org')
    expect(html).not.toContain('buyer@example.com')
    expect(html).not.toContain('ab@example.org')
  })
  it('页面上「将使用账号」、进度列表的账号都过 maskEmail', () => {
    expect(page).toContain("import { maskEmail } from '../../lib/sessionCheck'")
    expect(page).toContain('{{ maskEmail(sessionPool[currentExcelIdx].email) }}')
    expect(page).toContain('{{ maskEmail(mailboxPool[currentExcelIdx].email) }}')
    expect(page).toContain("{{ t('batch.account') }}：{{ maskEmail(it.email) }}")
    expect(page).not.toMatch(/\{\{\s*(it|sessionPool\[currentExcelIdx\]|mailboxPool\[currentExcelIdx\])\.email\s*\}\}/)
  })
  it('导入成功提示里举例的邮箱也打码', () => {
    expect(page).toContain('`，如 ${maskEmail(sessions[0].email)}`')
    expect(page).not.toContain('`，如 ${sessions[0].email}`')
  })
  it('导出的成功表格保持原值，不打码', () => {
    const rows = successRows([{ status: 'success', email: 'buyer@example.com', ownEmail: 'buyer@example.com', gptPassword: '', emailPassword: '', accessToken: '' }])
    expect(rows[0][0]).toBe('buyer@example.com')
  })
})

/** 取 `function name(...) {` 的函数体（按花括号配对）。 */
function body(name: string): string {
  const m = new RegExp(`function\\s+${name}\\s*\\([^)]*\\)[^{]*\\{`).exec(page)
  if (!m) throw new Error(`找不到函数 ${name}`)
  let depth = 1
  let i = m.index + m[0].length
  const start = i
  for (; i < page.length && depth > 0; i++) {
    if (page[i] === '{') depth++
    else if (page[i] === '}') depth--
  }
  return page.slice(start, i - 1)
}

describe('导出里的邮箱来源（FE2 审查重要 2）', () => {
  // 兑换回包和查询结果里的 account_email 是后端打过码的（local_cdk.go localOrderPublic 用 maskEmailForAdmin）
  const served = 'bu***@example.com'
  const item = (status: string, ownEmail: string) => ({
    status, cardKey: 'SXC-AAAA-BBBB', email: served, ownEmail, gptPassword: 'gp', emailPassword: 'ep', accessToken: 'at',
  })
  it('买家自己提供了完整邮箱，服务端回的打码邮箱覆盖了显示用的 email：导出仍是完整邮箱', () => {
    expect(successRows([item('success', 'buyer@example.com')])[0][0]).toBe('buyer@example.com')
    expect(unknownRows([item('unknown', 'buyer@example.com')], '结果未知')[0][1]).toBe('buyer@example.com')
  })
  it('页面从头到尾拿不到完整邮箱（比如只有加密的登录信息）：导出服务端给的值，不硬造', () => {
    expect(successRows([item('success', '')])[0][0]).toBe(served)
  })
  it('ownEmailOf 只收完整邮箱：空的、没有 @ 的、打过码的都跳过', () => {
    expect(ownEmailOf('', undefined, null, 'buyer@example.com')).toBe('buyer@example.com')
    expect(ownEmailOf(' buyer@example.com ')).toBe('buyer@example.com')
    expect(ownEmailOf(served, 'not-an-email', 'buyer@example.com')).toBe('buyer@example.com')
    expect(ownEmailOf(served)).toBe('')
    expect(ownEmailOf()).toBe('')
  })
  it('页面只从买家自己提供的地方写 ownEmail：粘贴或导入的登录信息、邮箱密码、核对账号时后端从买家登录信息读出的', () => {
    const s = body('submitOne')
    expect(s).toContain('ownEmail: ownEmailOf(check.email, imported?.email)')
    expect(s).toContain('ownEmail: ownEmailOf(email)')
    const r = body('runPreflightRedeem')
    const preflightEmail = r.indexOf('ownEmailOf(fallbackEmail, body.email, body.account_email)')
    expect(preflightEmail).toBeGreaterThan(r.indexOf("'/api/v1/public/cdk/preflight'"))
    expect(preflightEmail).toBeLessThan(r.indexOf("'/api/v1/public/cdk/redeem'"))
    // 兑换回包之后、查询结果、结果未知：只改显示用的 email，不碰 ownEmail
    expect(r.slice(r.indexOf("'/api/v1/public/cdk/redeem'"))).not.toContain('ownEmail')
    expect(body('applyPollResult')).not.toContain('ownEmail')
    expect(body('markResultUnknown')).not.toContain('ownEmail')
    // 写 ownEmail 的只有这几处（类型声明那一行不算）
    expect((page.match(/ownEmail: (?!string\b)/g) || []).length).toBe(4)
    // 新条目从空开始
    expect(body('handleVerifyBatch')).toContain("ownEmail: '',")
  })
  it('屏幕上仍只显示打码邮箱，不显示 ownEmail', () => {
    expect(page).not.toMatch(/\{\{[^}]*ownEmail[^}]*\}\}/)
  })
})
