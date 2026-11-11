// F2-21：批量兑换页的进度列表和导入预览里的邮箱打码，和单张兑换页用同一个 maskEmail；导出的成功表格保持原值。
// 数据都是假的。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { createSSRApp, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'
import ExcelImportBlock from '../../components/ExcelImportBlock.vue'
import { successRows } from '../../lib/batch-redeem'

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
    const rows = successRows([{ status: 'success', email: 'buyer@example.com', gptPassword: '', emailPassword: '', accessToken: '' }])
    expect(rows[0][0]).toBe('buyer@example.com')
  })
})
