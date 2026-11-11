// F2-05：粘贴超过上限的卡密时，输入框下面的提示和点「开始验证」的报错说同一句话，数字取实际上限常量。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'
import { BATCH_MAX_KEYS } from '../../lib/batch-session'

const src = readFileSync(resolve(__dirname, '../../views/user/BatchRedeemView.vue'), 'utf8')
const i18n = createI18n({ legacy: false, locale: 'zh', fallbackLocale: 'zh', messages: { zh, en } })
const tr = (locale: 'zh' | 'en') => i18n.global.t('batch.cdkOverLimit', { max: BATCH_MAX_KEYS }, { locale })

describe('超出张数上限的提示（F2-05）', () => {
  it('中英文各一句，数字是 BATCH_MAX_KEYS', () => {
    expect(tr('zh')).toBe(`超出 ${BATCH_MAX_KEYS} 张，请分批提交`)
    expect(tr('en')).toBe(`Over ${BATCH_MAX_KEYS} CDKs — please submit them in separate batches`)
  })
  it('旧的「将分批排队处理」词条删掉', () => {
    expect(JSON.stringify(zh.batch)).not.toContain('cdkQueueHint')
    expect(JSON.stringify(en.batch)).not.toContain('cdkQueueHint')
    expect(src).not.toContain('cdkQueueHint')
  })
  it('输入框下的提示按上限常量判断，不写死 100', () => {
    expect(src).toContain('v-if="parsedCdks.length > BATCH_MAX_KEYS"')
    expect(src).not.toMatch(/parsedCdks\.length > 100/)
    expect(src).toContain("t('batch.cdkOverLimit', { max: BATCH_MAX_KEYS })")
  })
  it('点「开始验证」超限时报同一句话', () => {
    const verify = src.slice(src.indexOf('async function handleVerifyBatch'), src.indexOf('async function submitOne'))
    expect(verify).toContain("sessionError.value = t('batch.cdkOverLimit', { max: BATCH_MAX_KEYS })")
    expect(verify).not.toContain('单次最多')
  })
})
