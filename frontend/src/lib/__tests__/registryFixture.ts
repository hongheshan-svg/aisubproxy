// 三家服务商的模拟数据：acme（上游）、marswallex（供卡）、testcard（假的供卡型，证明前端不写死两家）。
export const registryFixture = () => ({
  providers: [
    { name: 'acme', display_name: 'Acme', kind: 'upstream', kind_text: '上游发码', products: ['gpt', 'x', 'grok', 'claude'], code_list: '' },
    { name: 'marswallex', display_name: 'MarsWallex', kind: 'card_supply', kind_text: '供卡·本站付款', products: ['gpt'], code_prefix: 'MW-', code_list: 'local' },
    { name: 'testcard', display_name: 'TestCard', kind: 'card_supply', kind_text: '供卡·本站付款', products: ['gpt', 'x'], code_prefix: 'TC-', code_list: 'local' },
  ],
  products: [
    { key: 'gpt', label: 'ChatGPT' }, { key: 'x', label: 'X Premium' }, { key: 'grok', label: 'Grok' }, { key: 'claude', label: 'Claude' },
  ],
  default: 'acme',
  by_product: { gpt: 'marswallex' } as Record<string, string>,
  effective: { gpt: 'marswallex', x: 'acme', grok: 'acme', claude: 'acme' },
  warnings: [] as string[],
  capabilities: [
    { product: 'gpt', plan: 'plus', comment: 'Plus', support: { acme: true, marswallex: true, testcard: true } },
    { product: 'gpt', plan: 'credit*', comment: 'Codex 点数', support: { acme: true, marswallex: false, testcard: false } },
    { product: 'x', plan: 'premium', comment: 'X Premium', support: { acme: true, testcard: true } },
  ],
})

export const readinessFixture = () => ({
  default: 'acme',
  by_product: { gpt: 'marswallex' },
  providers: {
    acme: {
      provider: 'acme', ok: true, problems: [], checks: [{ key: 'zz_acme_check', ok: true, detail: '已配置' }],
      extras: [{ label: 'Acme 余额', value: '123.45 USD' }], warnings: [],
    },
    marswallex: {
      provider: 'marswallex', ok: false, problems: ['还没有真实付款'],
      checks: [{ key: 'first_real_payment', ok: false, detail: '还没有一笔真实付款' }],
      extras: [], warnings: ['主钱包余额偏低'],
      combos: [{ plan: 'plus', label: 'Plus', region: 'US', currency: 'USD', cap_minor: 3000, fund_cents: 2500, cards: 4, ok: true, problems: [] }],
    },
    testcard: {
      provider: 'testcard', ok: true, problems: [], checks: [{ key: 'zz_custom_check', ok: true, detail: '自定义检查通过' }],
      extras: [{ label: '测试额度', value: '9 张' }], warnings: [],
    },
  },
  capabilities: registryFixture().capabilities,
})
