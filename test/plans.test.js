'use strict';
const plans = require('../plan-config');
const store = require('../mysql-store');
const mysql = require('mysql2/promise');
const ChatGPTService = require('../chatgpt');
const { getPlanTypeLabel } = require('../region-config');
const { clickPlanUpgrade } = require('../pricing-checkout');
let query;
beforeAll(() => { query = vi.fn().mockResolvedValue([{ affectedRows: 1 }]); vi.spyOn(mysql, 'createPool').mockReturnValue({ query }); });
afterAll(() => vi.restoreAllMocks());
beforeEach(() => { query.mockClear(); });
it.each([
    ['plus', 'chatgptplusplan', 'ChatGPT Plus'],
    ['pro_5x', 'chatgptpro100', 'ChatGPT Pro 100'],
    ['pro_20x', 'chatgptpro200', 'ChatGPT Pro 200'],
    ['pro_500', 'chatgptpro500', 'ChatGPT Pro 500']
])('preserves %s CDKs and sends the exact checkout plan', async (type, name, label) => {
    const request = { post: vi.fn().mockResolvedValue({ status: () => 200, text: async () => JSON.stringify({ url: 'https://chatgpt.com/checkout/openai_llc/cs_test_fixture' }) }) };
    const client = new ChatGPTService(request, 'test-token');
    expect((await client.createCheckoutSession(type, 'US', 'USD')).checkoutUrl).toBeTruthy();
    expect(request.post.mock.calls[0][1].data.plan_name).toBe(name);
    expect(getPlanTypeLabel(type)).toBe(label);
    await store.insertCdks(['GW-test-fixture'], { plan_type: type });
    expect(query.mock.calls[0][1][0][0][3]).toBe(type);
});
it('rejects an unknown CDK plan instead of silently issuing Plus', async () => {
    await expect(store.insertCdks(['GW-test-fixture'], { plan_type: 'unknown' })).rejects.toThrow('不支持');
    expect(query).not.toHaveBeenCalled();
});
it('prevents Pro 500 from selecting another checkout plan or a generic Pro button', async () => {
    expect(() => plans.resolveCheckoutPlan('pro_500', 'chatgptpro100')).toThrow('Pro 500');
    await expect(clickPlanUpgrade({}, 'pro_500')).rejects.toThrow('禁止');
    expect(new ChatGPTService({}, 'test-token').getPlanAmount('pro_500', 'USD')).toBe(500);
});
it('uses the exact enabled provider key for Pro 500 without guessing it', async () => {
    const api = { fetchPlans: vi.fn().mockResolvedValue({ success: true, plans: [{ key: 'provider-pro500-live', plan_name: 'chatgptpro500', enabled: 1 }] }) };
    expect(await plans.resolvePaymentPlan(api, 'pro_500', { enabled: true, api_key: 'test-only' })).toEqual({ planName: 'chatgptpro500', apiPlanKey: 'provider-pro500-live' });
});
it.each([
    { success: false },
    { success: true, plans: [] },
    { success: true, plans: [{ key: 'pro500', plan_name: 'chatgptpro100', enabled: 1 }] },
    { success: true, plans: [{ key: 'pro500', plan_name: 'chatgptpro500', enabled: 0 }] },
    { success: true, plans: [{ key: 'one', plan_name: 'chatgptpro500', enabled: 1 }, { key: 'two', plan_name: 'chatgptpro500', enabled: 1 }] }
])('refuses unavailable, mismatched or ambiguous Pro 500 provider plans', async result => {
    const api = { fetchPlans: vi.fn().mockResolvedValue(result) };
    await expect(plans.resolvePaymentPlan(api, 'pro_500', { enabled: true, api_key: 'test-only' })).rejects.toMatchObject({ status: 503 });
});
it('uses the supplied local Pro 500 identifier without depending on a provider', async () => {
    expect(await plans.resolvePaymentPlan({}, 'pro_500', { enabled: false })).toEqual({ planName: 'chatgptpro500', apiPlanKey: '' });
});
