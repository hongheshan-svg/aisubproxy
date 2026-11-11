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

describe('结果未知的文案（F2-03 裁定 1）', () => {
  it('停查后的说明、状态标签、统计名，中英文都有', () => {
    expect(zh.batch.resultUnknownStopped).toBe('结果未知，请到卡密查询页核对，不要重复提交')
    expect(en.batch.resultUnknownStopped).toBe('Result unknown — check it on the CDK Status page and do not submit it again')
    expect(zh.batch.status.unknown).toBe('结果未知')
    expect(en.batch.status.unknown).toBe('Result unknown')
    expect(zh.batch.statUnknown).toBe('结果未知')
    expect(en.batch.statUnknown).toBe('Unknown')
  })
  it('不再有「查不到记录就判失败」的旧文案', () => {
    expect(JSON.stringify(zh.batch)).not.toContain('resultNotFound')
    expect(JSON.stringify(en.batch)).not.toContain('resultNotFound')
  })
  it('导出按钮有结果未知时说明另含几张', () => {
    expect(i18n.global.t('batch.exportOkUnknown', { n: 3, m: 2 }, { locale: 'zh' })).toBe('导出本批成功号（3），另含结果未知 2 张')
    expect(i18n.global.t('batch.exportOkUnknown', { n: 3, m: 2 }, { locale: 'en' })).toBe('Export successful accounts (3), plus 2 with unknown result')
  })
})

describe('重置按钮锁住时的说明（F2-04b 裁定 3）', () => {
  it('中英文都有', () => {
    expect(zh.batch.resetWaitUnknown).toBe('还有结果未知的卡密正在查询，查完才能重新开始')
    expect(en.batch.resetWaitUnknown).toBe('Some CDKs with unknown results are still being checked — you can start over once that finishes')
  })
})
