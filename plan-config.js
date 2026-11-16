'use strict';
// Existing CDK identifiers remain valid; only checkout plan names change.
const PLAN_NAME_MAP = Object.freeze({ plus: 'chatgptplusplan', pro_5x: 'chatgptpro100', pro_20x: 'chatgptpro200', pro_500: 'chatgptpro500' });
const API_PLAN_MAP = Object.freeze({ plus: 'plus', pro_5x: 'pro5x', pro_20x: 'pro20x' });
const invalid = message => Object.assign(new Error(message), { status: 400 });
const unavailable = message => Object.assign(new Error(message), { status: 503 });
function planType(value = 'plus') {
    if (!Object.hasOwn(PLAN_NAME_MAP, value)) throw invalid('不支持的套餐类型');
    return value;
}
function resolvePlanName(type) { return PLAN_NAME_MAP[planType(type)]; }
function resolveCheckoutPlan(type, override) {
    const expected = resolvePlanName(type);
    const value = String(override || '').trim();
    if (type === 'pro_500' && value && value !== expected) throw invalid('Pro 500 必须使用 chatgptpro500 套餐');
    return value || expected;
}
async function resolvePaymentPlan(api, type, apiConfig) {
    type = planType(type);
    const planName = resolvePlanName(type);
    if (type !== 'pro_500') return { planName, apiPlanKey: API_PLAN_MAP[type] };
    if (apiConfig?.enabled && apiConfig.api_key) {
        const result = await api.fetchPlans(apiConfig);
        if (!result.success) throw unavailable('无法确认 Pro 500 上游套餐，请稍后重试');
        // API plan keys belong to the configured provider; never guess a purchase key.
        const matches = result.plans.filter(item => item.plan_name === planName && typeof item.key === 'string' && item.key.trim() && (item.enabled === true || Number(item.enabled) === 1));
        if (matches.length !== 1) throw unavailable('上游未提供唯一且已启用的 Pro 500 套餐，请联系管理员');
        return { apiPlanKey: matches[0].key, planName };
    }
    return { planName, apiPlanKey: '' };
}
module.exports = { PLAN_NAME_MAP, planType, resolvePlanName, resolveCheckoutPlan, resolvePaymentPlan };
