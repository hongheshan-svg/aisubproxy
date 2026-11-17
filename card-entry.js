'use strict';

const CARD_FIELDS = {
    number: 'input[autocomplete="cc-number"], input[name="cardnumber"], input[name="number"], input[data-elements-stable-field-name="cardNumber"]',
    expiry: 'input[autocomplete="cc-exp"], input[name="exp-date"], input[name="expiry"], input[data-elements-stable-field-name="cardExpiry"]',
    cvc: 'input[autocomplete="cc-csc"], input[name="cvc"], input[name="cardCvc"], input[data-elements-stable-field-name="cardCvc"]'
};
const CARD_LABEL = /^(Card|Credit or debit card|Credit card|Debit card|信用卡|银行卡|銀行卡|卡|カード|Karte|Kreditkarte|Carte|Carte bancaire|Tarjeta|Tarjeta de crédito|Cartão|Cartão de crédito)$/i;

function paymentFrames(page) {
    return page.frames().filter(frame => {
        if (frame === page.mainFrame()) return true;
        try {
            const url = new URL(frame.url());
            return url.protocol === 'https:' && /(^|\.)stripe\.com$/i.test(url.hostname)
                && !/captcha|challenge/i.test(url.pathname);
        } catch (_) { return false; }
    });
}

async function firstUsable(locator, editable = false) {
    const count = await locator.count().catch(() => 0);
    for (let i = 0; i < Math.min(count, 20); i += 1) {
        const item = locator.nth(i);
        if (!await item.isVisible().catch(() => false)) continue;
        if (!await item.isEnabled().catch(() => false)) continue;
        if (editable && !await item.isEditable().catch(() => false)) continue;
        return item;
    }
    return null;
}

async function findCardEntryFields(page) {
    const fields = {};
    for (const frame of paymentFrames(page)) {
        for (const [key, selector] of Object.entries(CARD_FIELDS)) {
            if (!fields[key]) fields[key] = await firstUsable(frame.locator(selector), true);
        }
    }
    return fields.number && fields.expiry && fields.cvc ? fields : null;
}

// 只有新卡三个输入框可见且可编辑才成功；点击完成或 aria-selected 不是成功条件。
async function ensureCardEntryReady(page, { timeoutMs = 30000, pollMs = 400 } = {}) {
    const deadline = Date.now() + timeoutMs;
    const attempts = new WeakMap();
    do {
        if (page.isClosed()) throw new Error('支付页面已关闭，无法打开 Card 表单');
        if (/\/auth\/|accounts\.google\.com|auth\.openai\.com/.test(page.url())) {
            throw new Error('Session 已失效，请重新登录后打开 Card 表单');
        }
        const fields = await findCardEntryFields(page);
        if (fields) return fields;
        for (const frame of paymentFrames(page)) {
            const attempt = attempts.get(frame) || { count: 0, time: 0 };
            if (attempt.count >= 3 || (attempt.count && Date.now() - attempt.time < 1500)) continue;
            const candidates = [frame.locator('[data-testid="card-tab"], [data-testid="CARD-tab"], [data-testid="payment-method-card"], [data-payment-method="card"]'),
                ...['tab', 'radio', 'button'].map(role => frame.getByRole(role, { name: CARD_LABEL }))];
            let card = null;
            for (const locator of candidates) {
                card = await firstUsable(locator);
                if (card) break;
            }
            if (!card) continue;
            const selected = await card.getAttribute('aria-selected').catch(() => '') === 'true'
                || await card.getAttribute('aria-checked').catch(() => '') === 'true';
            if (selected) continue;
            attempts.set(frame, { count: attempt.count + 1, time: Date.now() });
            await card.click({ timeout: Math.max(1, Math.min(3000, deadline - Date.now())) }).catch(() => {});
        }
        if (Date.now() < deadline) await page.waitForTimeout(Math.min(pollMs, deadline - Date.now()));
    } while (Date.now() < deadline);
    const error = new Error('无法打开 Card 新卡表单：卡号、有效期和安全码输入框未就绪，已停止后续付款');
    error.code = 'card_entry_unavailable';
    throw error;
}

module.exports = { ensureCardEntryReady, findCardEntryFields, paymentFrames };
