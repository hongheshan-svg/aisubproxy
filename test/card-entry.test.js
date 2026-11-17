'use strict';
const { ensureCardEntryReady, paymentFrames } = require('../card-entry');

// Stateful controls: clicking a tab and loading its fields are separate events.
function fixture({ initiallyReady = false, selected = false, blocked = false, label = 'Card', clickFails = false, delayedCvc = false } = {}) {
    const state = { fields: initiallyReady, selected, ticks: 0, clicks: 0 };
    const input = key => ({ isVisible: async () => state.fields,
        isEnabled: async () => !blocked, isEditable: async () => !blocked && (key !== 'cvc' || !delayedCvc || state.ticks >= 2) });
    const button = { isVisible: async () => true, isEnabled: async () => true,
        getAttribute: async () => state.selected ? 'true' : 'false',
        click: async () => { state.clicks++; if (clickFails) throw new Error('detached'); state.selected = true; } };
    const list = items => ({ count: async () => items.length, nth: i => items[i] });
    const frame = {
        locator: selector => selector.startsWith('input[autocomplete="cc-number"') ? list([input('number')])
            : selector.startsWith('input[autocomplete="cc-exp"') ? list([input('expiry')])
                : selector.startsWith('input[autocomplete="cc-csc"') ? list([input('cvc')]) : list([]),
        getByRole: (_, { name }) => list(name.test(label) ? [button] : []),
        url: () => 'https://js.stripe.com/v3/elements-inner-accessory-target'
    };
    const main = { ...frame, locator: () => list([]), getByRole: () => list([]), url: () => 'https://chatgpt.com/checkout/test' };
    const page = { frames: () => [main, frame], mainFrame: () => main, isClosed: () => false, url: () => main.url(),
        waitForTimeout: async ms => { state.ticks++; if (state.selected && !blocked) state.fields = true; await vi.advanceTimersByTimeAsync(ms); } };
    return { page, state, frame };
}
beforeEach(() => { vi.useFakeTimers(); });
afterEach(() => { vi.useRealTimers(); });

it('switches from Saved to Card and waits for all three editable fields', async () => {
    const { page, state } = fixture({ delayedCvc: true });
    const fields = await ensureCardEntryReady(page);
    expect(Object.keys(fields).sort()).toEqual(['cvc', 'expiry', 'number']);
    expect(state.clicks).toBe(1);
    expect(state.ticks).toBeGreaterThanOrEqual(2);
});
it('accepts a hosted card form without a Card tab and performs no clicks', async () => {
    const { page, state } = fixture({ initiallyReady: true, label: 'Subscribe' });
    await ensureCardEntryReady(page);
    expect(state.clicks).toBe(0);
});
it('does not report a selected tab as success while its inputs are disabled', async () => {
    const { page, state } = fixture({ selected: true, blocked: true });
    await expect(ensureCardEntryReady(page, { timeoutMs: 800 })).rejects.toMatchObject({ code: 'card_entry_unavailable' });
    expect(state.clicks).toBe(0);
});
it('retries a failed click within a fixed deadline and stops before payment', async () => {
    const { page, state } = fixture({ clickFails: true });
    await expect(ensureCardEntryReady(page, { timeoutMs: 4200 })).rejects.toThrow('已停止后续付款');
    expect(state.clicks).toBe(3);
});
it.each(['Saved', 'Apple Pay', 'Google Pay', 'Subscribe'])('never selects the unrelated %s button', async label => {
    const { page, state } = fixture({ label });
    await expect(ensureCardEntryReady(page, { timeoutMs: 800 })).rejects.toMatchObject({ code: 'card_entry_unavailable' });
    expect(state.clicks).toBe(0);
});
it.each(['信用卡', 'Credit or debit card', 'カード'])('recognizes the %s card control', async label => {
    const { page, state } = fixture({ label });
    await ensureCardEntryReady(page);
    expect(state.clicks).toBe(1);
});
it('ignores unrelated frames even when they expose card-like inputs', async () => {
    const { page, frame } = fixture({ initiallyReady: true });
    frame.url = () => 'https://js.stripe.com.evil.example/';
    expect(paymentFrames(page)).toEqual([page.mainFrame()]);
    await expect(ensureCardEntryReady(page, { timeoutMs: 800 })).rejects.toMatchObject({ code: 'card_entry_unavailable' });
});
