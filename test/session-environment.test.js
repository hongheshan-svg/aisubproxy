'use strict';
const { resolveClientEnvironment, timezoneFromClientEnv, buildSessionApiHeaders } = require('../session-environment');
const { buildBrowserEnvironment } = require('../browser-environment');
const { collectCookieSpecs, toPlaywrightCookie, expandSessionTokenCookies, installChatGptSession,
    adoptVerifiedSession, installSessionCookieGuard } = require('../session-auth');

const macUa = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36';
const defaults = { realUserAgent: macUa, locale: 'en-US', timezoneId: 'America/New_York' };
const fixture = () => ({ accessToken: 'old-token', sessionToken: 'e'.repeat(4768), user: { id: 'user-fixture', email: 'test@example.com' },
    account: { id: 'account-fixture', planType: 'free' }, statsigContext: { userAgent: macUa, locale: 'zh-CN', country: 'US', regionCode: 'NJ', deviceId: 'device-fixture' } });
beforeEach(() => { vi.spyOn(console, 'log').mockImplementation(() => {}); });
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllEnvs(); });

it('merges every available field across environment sources and skips malformed values', () => {
    const env = resolveClientEnvironment({
        __env: { userAgent: '\r\ninvalid', hardwareConcurrency: null, screen: { width: 1440 }, maxTouchPoints: 0, timezone: 'not-a-timezone' },
        clientEnvironment: { hardwareConcurrency: 12, deviceMemory: 8, screen: { height: 900 }, viewport: { width: 1280, height: 720 }, timezoneId: 'Asia/Tokyo' },
        environment: { navigator: { platform: 'MacIntel' } },
        statsigContext: { userAgent: macUa, locale: 'ja-JP', deviceId: 'different-device' }
    });
    expect(env).toMatchObject({ userAgent: macUa, hardwareConcurrency: 12, deviceMemory: 8, screen: { width: 1440, height: 900 },
        window: { innerWidth: 1280, innerHeight: 720 }, maxTouchPoints: 0, timezone: 'Asia/Tokyo', platform: 'MacIntel', deviceId: 'different-device' });
});

it('keeps supplied hardware even if no UA, IP or device identity was exported', () => {
    expect(resolveClientEnvironment({ __env: { screen: { width: 1366, height: 768 }, hardwareConcurrency: 16 } }))
        .toMatchObject({ screen: { width: 1366, height: 768 }, hardwareConcurrency: 16 });
});

it('aligns each session independently without reusing the previous account or device', () => {
    const first = resolveClientEnvironment(fixture());
    const second = resolveClientEnvironment({ access_token: 'fixture-only', account: { id: 'second-account' },
        statsigContext: { deviceId: 'second-device', userAgent: 'Mozilla/5.0 (Linux; Android 14) Chrome/155.0.0.0 Mobile Safari/537.36', country: 'JP' } });
    expect(second).toMatchObject({ deviceId: 'second-device', accountId: 'second-account', country: 'JP' });
    expect(buildBrowserEnvironment(second, defaults).uaOverride.userAgentMetadata).toMatchObject({ platform: 'Android', mobile: true });
    expect(first).toMatchObject({ deviceId: 'device-fixture', accountId: 'account-fixture', userAgent: macUa });
    expect(resolveClientEnvironment('{}')).toBeNull();
});

it.each(['__env', 'clientEnv', 'clientEnvironment', 'environment', 'statsigContext'])('reads environment from %s in JSON and object input', key => {
    const input = { [key]: { userAgent: macUa, deviceId: 'per-session-device', country: 'US' } };
    expect(resolveClientEnvironment(input)).toEqual(resolveClientEnvironment(JSON.stringify(input)));
    expect(resolveClientEnvironment(input)).toMatchObject({ userAgent: macUa, deviceId: 'per-session-device', country: 'US' });
});

it('parses the supplied session shape without inventing missing hardware or OS versions', () => {
    const env = resolveClientEnvironment(JSON.stringify(fixture()));
    expect(env).toMatchObject({ deviceId: 'device-fixture', accountId: 'account-fixture', userAgent: macUa });
    expect(timezoneFromClientEnv(env)).toBe('America/New_York');
    const profile = buildBrowserEnvironment(env, defaults);
    expect(profile.uaOverride).toMatchObject({ userAgent: macUa, platform: 'MacIntel',
        userAgentMetadata: { platform: 'macOS', platformVersion: '', architecture: '' } });
    expect(profile.overrides.hardwareConcurrency).toBeNull();
    expect(profile.overrides.deviceMemory).toBeNull();
});

it('uses captured dimensions and does not clamp phones to desktop size', () => {
    const profile = buildBrowserEnvironment({ userAgent: 'Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 Chrome/157.0.0.0 Mobile Safari/537.36',
        screen: { width: 393, height: 852 }, window: { innerWidth: 393, innerHeight: 730 }, devicePixelRatio: 2.75, maxTouchPoints: 5 }, defaults);
    expect(profile.contextOptions).toMatchObject({ viewport: { width: 393, height: 730 }, screen: { width: 393, height: 852 }, deviceScaleFactor: 2.75, isMobile: true, hasTouch: true });
    expect(profile.uaOverride.userAgentMetadata.mobile).toBe(true);
});

it('preserves captured UA hints and desktop touchscreen values', () => {
    const profile = buildBrowserEnvironment({ userAgent: macUa, maxTouchPoints: 2, userAgentData: {
        architecture: 'arm', platformVersion: '15.3.0', bitness: '64',
        fullVersionList: [{ brand: 'Google Chrome', version: '157.0.8123.1' }]
    } }, defaults);
    expect(profile.uaOverride.userAgentMetadata).toMatchObject({ architecture: 'arm', platformVersion: '15.3.0', bitness: '64', fullVersionList: [{ brand: 'Google Chrome', version: '157.0.8123.1' }] });
    expect(profile.contextOptions.hasTouch).toBe(true);
    expect(profile.overrides.maxTouchPoints).toBe(2);
});

it('does not add Chrome Client Hints for a Safari session', () => {
    const profile = buildBrowserEnvironment({ userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1' }, defaults);
    expect(profile.uaOverride.userAgentMetadata).toBeUndefined();
    expect(profile.contextOptions.viewport.width).toBe(390);
});

it('retains account identity even when the session has no statsigContext', () => {
    const payload = Buffer.from(JSON.stringify({ 'https://api.openai.com/auth': { chatgpt_account_id: 'jwt-account' } })).toString('base64url');
    expect(resolveClientEnvironment({ access_token: `header.${payload}.signature` }).accountId).toBe('jwt-account');
    expect(resolveClientEnvironment({ account: { id: 'account-only' } }).accountId).toBe('account-only');
});

it('uses the same exported device ID in JS-readable cookies and API headers', () => {
    const input = fixture();
    input.__env = { deviceId: 'env-device' };
    input.cookies = [{ name: 'oai-did', value: 'cookie-device', domain: '.chatgpt.com' }];
    const env = resolveClientEnvironment(input);
    expect(buildSessionApiHeaders(env)['oai-device-id']).toBe('cookie-device');
    const cookie = collectCookieSpecs(input, input).find(c => c.name === 'oai-did');
    expect(toPlaywrightCookie(cookie)).toMatchObject({ value: 'cookie-device', httpOnly: false });
    expect(collectCookieSpecs({ cookies: [{ name: 'oai-did', value: 'wrong', domain: 'evilchatgpt.com' }] }, null)).toEqual([]);
});

it('passes final UA, language and client hints to API requests', () => {
    const env = resolveClientEnvironment(fixture());
    const profile = buildBrowserEnvironment(env, defaults);
    const headers = buildSessionApiHeaders({ ...env, locale: defaults.locale, userAgentData: profile.uaOverride.userAgentMetadata });
    expect(headers).toMatchObject({ 'oai-language': 'en-US', 'user-agent': macUa, 'sec-ch-ua-platform': '"macOS"', 'chatgpt-account-id': 'account-fixture' });
    expect(headers['sec-ch-ua']).toContain('v="157"');
    expect(profile.uaOverride.userAgentMetadata.fullVersionList.filter(b => /Chrome|Chromium/.test(b.brand))
        .every(b => b.version === '157.0.0.0')).toBe(true);
});

it('splits a 4768-character session into reversible secure cookies', () => {
    const chunks = expandSessionTokenCookies('s'.repeat(4768));
    expect(chunks.map(c => c.name)).toEqual(['__Secure-next-auth.session-token.0', '__Secure-next-auth.session-token.1']);
    expect(chunks.map(c => c.value).join('')).toBe('s'.repeat(4768));
    expect(chunks.map(toPlaywrightCookie).every(c => c.secure && c.httpOnly)).toBe(true);
});

function fakeContext(live = { accessToken: 'fresh-token', user: fixture().user }) {
    const stored = [];
    return { stored, addCookies: vi.fn(async cookies => stored.push(...cookies)), cookies: vi.fn(async () => stored),
        on: vi.fn(), route: vi.fn(), addInitScript: vi.fn(),
        newPage: vi.fn(async () => ({ goto: async () => ({ status: () => 200, headers: () => ({}) }),
            waitForTimeout: async () => {}, evaluate: async () => JSON.stringify(live), close: async () => {} })) };
}

it('uses verified refreshed tokens without faking session/CSRF responses or overriding frontend tokens', async () => {
    vi.stubEnv('CHATGPT_SESSION_COOKIE', 'another-account-cookie');
    const context = fakeContext();
    const installed = await installChatGptSession(context, fixture());
    expect(installed.sessionData.accessToken).toBe('fresh-token');
    expect(context.stored.filter(c => c.name.startsWith('__Secure-next-auth.session-token')).map(c => c.value).join('')).toBe(fixture().sessionToken);
    expect(context.addInitScript).not.toHaveBeenCalled();
    expect(context.route).toHaveBeenCalledTimes(1);
    expect(context.route.mock.calls[0][0]).toBe('https://chatgpt.com/backend-api/**');
    const handler = context.route.mock.calls[0][1];
    const makeRoute = (headers = {}, url = 'https://chatgpt.com/backend-api/payments/checkout') => ({ request: () => ({ url: () => url, headers: () => headers }), continue: vi.fn() });
    const first = makeRoute(); await handler(first);
    expect(first.continue.mock.calls[0][0].headers.authorization).toBe('Bearer fresh-token');
    const second = makeRoute({ authorization: 'Bearer newer-frontend-token' }); await handler(second);
    expect(second.continue.mock.calls[0][0].headers.authorization).toBe('Bearer newer-frontend-token');
    const external = makeRoute({}, 'https://chatgpt.com.evil.example/backend-api/me'); await handler(external);
    expect(external.continue).toHaveBeenCalledWith();
});

it('rejects a cookie for another account before opening Checkout', async () => {
    await expect(installChatGptSession(fakeContext({ accessToken: 'other-token', user: { id: 'other-user' } }), fixture())).rejects.toThrow('身份不一致');
});

it('rejects missing cookies and empty server sessions without fabricating login state', async () => {
    await expect(installChatGptSession(fakeContext(), { accessToken: 'token' })).rejects.toThrow('缺少登录 Cookie');
    expect(() => adoptVerifiedSession(fixture(), {})).toThrow('未返回有效登录会话');
});

it('observes session rotation and does not restore a cookie cleared by the server', async () => {
    const context = fakeContext();
    context.stored.push({ name: '__Secure-next-auth.session-token', value: 'old' });
    const rotate = vi.fn();
    const guard = installSessionCookieGuard(context, context.stored, { onRotate: rotate });
    const handler = context.on.mock.calls[0][1];
    const respond = () => handler({ url: () => 'https://chatgpt.com/api/auth/session', headersArray: async () => [{ name: 'set-cookie', value: '__Secure-next-auth.session-token=new; Secure' }] });
    context.stored[0].value = 'new'; respond();
    await new Promise(setImmediate);
    expect(rotate).toHaveBeenCalledWith('new');
    context.stored.length = 0; respond();
    await new Promise(setImmediate);
    expect(guard.stats()).toMatchObject({ cleared: true, restoreCount: 0 });
    expect(context.addCookies).not.toHaveBeenCalled();
});

it.each([false, 'login_required'])('does not report a checkout URL as success when its page is not ready (%s)', async state => {
    const human = require('../human-verification');
    vi.spyOn(human, 'waitForCheckoutPaymentReady').mockResolvedValue(state);
    const page = { url: () => 'https://chatgpt.com/checkout/openai_llc/test',
        waitForLoadState: async () => {}, waitForTimeout: async () => {}, textContent: async () => 'Loading' };
    await expect(require('../pricing-checkout').waitForCheckoutPage(page)).rejects.toThrow(/未加载|重新登录/);
});

it('requires an actual payment control, not a price summary or Subscribe button', async () => {
    const human = require('../human-verification');
    const main = { locator: () => ({ first: () => ({ isVisible: async () => false }), evaluateAll: async () => false }) };
    const page = { mainFrame: () => main, frames: () => [main], getByText: () => ({ first: () => ({ isVisible: async () => true }) }) };
    expect(await human.isCheckoutPaymentReady(page)).toBe(false);
    const stripe = { url: () => 'https://js.stripe.com/v3/elements-inner-card', locator: () => ({ first: () => ({ isVisible: async () => true }) }) };
    page.frames = () => [main, stripe];
    expect(await human.isCheckoutPaymentReady(page)).toBe(true);
    stripe.url = () => 'https://js.stripe.com.evil.example/';
    expect(await human.isCheckoutPaymentReady(page)).toBe(false);
});

it('accepts a loaded Saved/Card payment switcher without requiring new card inputs', async () => {
    const human = require('../human-verification');
    const frame = { locator: () => ({ first: () => ({ isVisible: async () => false }),
        evaluateAll: async fn => fn(['Saved', 'Card'].map(textContent => ({ textContent, getClientRects: () => [{}] }))) }) };
    expect(await human.isCheckoutPaymentReady({ frames: () => [frame], mainFrame: () => frame })).toBe(true);
});
