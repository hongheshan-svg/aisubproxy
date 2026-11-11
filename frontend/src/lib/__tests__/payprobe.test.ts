import { describe, expect, it } from 'vitest'
import {
  PROBE_CHECKS, pollProbe, probeBlockReason, probeCounts, probeErrorText, probeNeedsBrowser, probeStatusLabel, probeToText,
} from '../payprobe'
import { PAGE_TABS } from '../adminui'

describe('payprobe lib', () => {
  it('探测项与后端 key 一一对应，共 9 项', () => {
    expect(PROBE_CHECKS.map((c) => c.key)).toEqual([
      'account', 'subscriptions', 'checkout_modes', 'credit_checkout', 'payment_methods',
      'upgrade_preview', 'customer_portal', 'chatgpt_browser', 'credit_balance',
    ])
  })

  it('提交前检查：没有会话、没选探测项、正在运行都不能提交', () => {
    expect(probeBlockReason('', ['account'], false)).toContain('会话')
    expect(probeBlockReason('   ', ['account'], false)).toContain('会话')
    expect(probeBlockReason('{"a":1}', [], false)).toContain('探测项')
    expect(probeBlockReason('{"a":1}', ['account'], true)).toContain('运行')
    expect(probeBlockReason('{"a":1}', ['account'], false)).toBe('')
  })

  it('浏览器探测项识别', () => {
    expect(probeNeedsBrowser(['account'])).toBe(false)
    expect(probeNeedsBrowser(['account', 'customer_portal'])).toBe(true)
    expect(probeNeedsBrowser(['chatgpt_browser'])).toBe(true)
  })

  it('状态文字与计数', () => {
    expect(probeStatusLabel('ok')).toBe('成功')
    expect(probeStatusLabel('fail')).toBe('失败')
    expect(probeStatusLabel('skipped')).toBe('已跳过')
    const rs: any[] = [{ status: 'ok' }, { status: 'ok' }, { status: 'fail' }, { status: 'skipped' }]
    expect(probeCounts(rs)).toEqual({ ok: 2, fail: 1, skipped: 1 })
  })

  it('409 提示已有探测在运行', () => {
    expect(probeErrorText(409, { error: '已有探测正在运行，请等它结束再试' })).toContain('已有探测')
    expect(probeErrorText(409, {})).toContain('已有探测')
    expect(probeErrorText(0, {})).toBe('网络错误')
    expect(probeErrorText(400, { error: '会话无效' })).toBe('会话无效')
  })

  it('复制为文本：包含各项结论与详情，不含多余内容', () => {
    const txt = probeToText({
      id: 7, created_at: 1700000000, region: 'PH',
      results: [
        { key: 'account', title: '账号概况', status: 'ok', summary: 'plan_type=plus', details: { email: 'r***@e***.com' } },
        { key: 'checkout_modes', title: '开通结账会话形态', status: 'skipped', summary: '账号已有生效订阅' },
      ],
    })
    expect(txt).toContain('免付款探测 #7')
    expect(txt).toContain('地区 PH')
    expect(txt).toContain('[成功] account 账号概况')
    expect(txt).toContain('r***@e***.com')
    expect(txt).toContain('[已跳过] checkout_modes')
  })

  it('页内标签里有免付款探测', () => {
    const tabs = PAGE_TABS['/ops/marswallex-pay']
    expect(tabs.map((t) => t.name)).toContain('probe')
    expect(tabs.find((t) => t.name === 'probe')?.label).toBe('免付款探测')
  })

  describe('pollProbe', () => {
    const noSleep = () => Promise.resolve()

    it('轮询直到 done，逐次回调，跑完后不再请求', async () => {
      const seq = [
        { status: 'running', results: [] },
        { status: 'running', results: [{ key: 'account' }] },
        { status: 'done', results: [{ key: 'account' }, { key: 'subscriptions' }] },
      ]
      let calls = 0
      const seen: number[] = []
      const how = await pollProbe(
        async () => ({ ok: true, data: seq[Math.min(calls++, seq.length - 1)] }),
        (rec) => seen.push(rec.results.length),
        () => false,
        noSleep,
      )
      expect(how).toBe('finished')
      expect(calls).toBe(3)
      expect(seen).toEqual([0, 1, 2])
    })

    it('failed 也算结束', async () => {
      const how = await pollProbe(async () => ({ ok: true, data: { status: 'failed', error: '超时', results: [] } }), () => {}, () => false, noSleep)
      expect(how).toBe('finished')
    })

    it('每轮间隔 2 秒', async () => {
      const waits: number[] = []
      await pollProbe(async () => ({ ok: true, data: { status: 'done', results: [] } }), () => {}, () => false, async (ms) => { waits.push(ms) })
      expect(waits).toEqual([2000])
    })

    it('页面离开（shouldStop）后立刻停止，不再请求', async () => {
      let stop = false
      let calls = 0
      const how = await pollProbe(
        async () => { calls++; stop = true; return { ok: true, data: { status: 'running', results: [] } } },
        () => {},
        () => stop,
        noSleep,
      )
      expect(how).toBe('stopped')
      expect(calls).toBe(1)
      // 一开始就要求停止：一次都不请求
      calls = 0
      expect(await pollProbe(async () => { calls++; return { ok: true, data: {} } }, () => {}, () => true, noSleep)).toBe('stopped')
      expect(calls).toBe(0)
    })

    it('连续 3 次读取失败后停止；中间成功会清零计数', async () => {
      let calls = 0
      expect(await pollProbe(async () => { calls++; return { ok: false, data: {} } }, () => {}, () => false, noSleep)).toBe('error')
      expect(calls).toBe(3)
      const oks = [false, false, true, false, false, true]
      let i = 0
      const how = await pollProbe(
        async () => { const ok = oks[i++]; return { ok, data: { status: i >= 6 ? 'done' : 'running', results: [] } } },
        () => {}, () => false, noSleep,
      )
      expect(how).toBe('finished')
    })
  })
})
