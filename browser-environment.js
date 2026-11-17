'use strict';

const { clampNum, detectUaInfo, buildUaBrands } = require('./session-environment');
const pagePreparations = new WeakMap();

// 用浏览器协议同时设置网络 UA 和 navigator.userAgentData，避免两套手写值分离。
function buildBrowserEnvironment(env, { realUserAgent, locale, timezoneId }) {
    const client = env || {};
    const suppliedUa = String(client.userAgent || '');
    const userAgent = /^Mozilla\/5\.0 [^\r\n]{1,500}$/.test(suppliedUa)
        ? suppliedUa : String(realUserAgent || '').replace('HeadlessChrome/', 'Chrome/');
    const info = detectUaInfo(userAgent);
    const width = Math.round(clampNum(client.screen?.width, 240, 7680, info.isMobile ? 390 : 1920));
    const height = Math.round(clampNum(client.screen?.height, 240, 4320, info.isMobile ? 844 : 1080));
    const viewport = {
        width: Math.round(clampNum(client.window?.innerWidth, 240, width, info.isMobile ? width : Math.round(width * .9))),
        height: Math.round(clampNum(client.window?.innerHeight, 240, height, info.isMobile ? height : Math.round(height * .85)))
    };
    const maxTouchPoints = Math.round(clampNum(client.maxTouchPoints, 0, 20, info.isMobile ? 5 : 0));
    const languages = [...new Set([locale, locale.split('-')[0]])];
    const uaOverride = { userAgent, platform: client.platform || info.platformWin, acceptLanguage: languages.join(',') };
    const chrome = userAgent.match(/(?:Chrome|Chromium)\/(\d+)([\d.]*)/);
    if (chrome) {
        const hints = client.userAgentData || {};
        // Reduced UA 无法推断真实系统版本/CPU 架构；没有采集值时保持未知。
        const text = key => typeof hints[key] === 'string' && !/[\r\n]/.test(hints[key]) ? hints[key].slice(0, 100) : '';
        const metadata = {
            brands: buildUaBrands(userAgent, chrome[1]),
            platform: info.uaPlatform,
            platformVersion: text('platformVersion'),
            architecture: text('architecture'),
            model: text('model'),
            mobile: /\bMobile\b/.test(userAgent) || (info.isMobile && !/Android/.test(userAgent)),
            bitness: text('bitness'),
            wow64: hints.wow64 === true
        };
        const validBrands = list => Array.isArray(list) && list.length > 0 && list.length < 10
            && list.every(b => typeof b?.brand === 'string' && /^[\w .;()/?=-]{1,80}$/.test(b.brand)
                && /^\d+(\.\d+){0,3}$/.test(String(b.version)));
        if (validBrands(hints.brands)) metadata.brands = hints.brands;
        const fullVersion = (chrome[1] + chrome[2]).split('.');
        while (fullVersion.length < 4) fullVersion.push('0');
        metadata.fullVersion = fullVersion.join('.');
        metadata.fullVersionList = validBrands(hints.fullVersionList) ? hints.fullVersionList
            : metadata.brands.map(b => ({ brand: b.brand, version: /Chromium|Chrome|Edge/.test(b.brand)
                ? (b.brand === 'Microsoft Edge' ? userAgent.match(/Edg\/([\d.]+)/)?.[1] || metadata.fullVersion : metadata.fullVersion)
                : `${b.version}.0.0.0` }));
        uaOverride.userAgentMetadata = metadata;
    }
    const contextOptions = {
        userAgent, viewport, screen: { width, height }, locale, timezoneId,
        deviceScaleFactor: clampNum(client.devicePixelRatio, .5, 4, info.isMobile ? 3 : 1),
        isMobile: info.isMobile, hasTouch: maxTouchPoints > 0
    };
    return {
        contextOptions, uaOverride, languages,
        overrides: {
            maxTouchPoints,
            hardwareConcurrency: clampNum(client.hardwareConcurrency, 1, 128, null),
            deviceMemory: clampNum(client.deviceMemory, .25, 8, null)
        }
    };
}

async function installBrowserEnvironment(context, profile) {
    // 仅覆盖用户确实提供的硬件值；屏幕尺寸交给 Playwright，不修改 Canvas / GPU。
    await context.addInitScript(({ overrides, languages }) => {
        const proto = Object.getPrototypeOf(navigator);
        for (const [key, value] of Object.entries({ ...overrides, languages, language: languages[0] })) {
            if (value !== null && value !== undefined) {
                Object.defineProperty(proto, key, { configurable: true, get: () => value });
            }
        }
    }, { overrides: profile.overrides, languages: profile.languages });
    const prepare = page => {
        if (!pagePreparations.has(page)) {
            const promise = (async () => {
                const cdp = await context.newCDPSession(page);
                await cdp.send('Emulation.setUserAgentOverride', profile.uaOverride);
                // 立即 detach 会使 Chromium 撤销覆盖；保持到该页面关闭。
                page.once('close', () => { cdp.detach().catch(() => {}); });
            })();
            // 事件回调不能 await；所有主动导航前通过 prepareBrowserPage 等待并传播错误。
            promise.catch(() => {});
            pagePreparations.set(page, promise);
        }
        return pagePreparations.get(page);
    };
    context.on('page', prepare);
    await Promise.all(context.pages().map(prepare));
}

async function prepareBrowserPage(page) {
    await pagePreparations.get(page);
    return page;
}

module.exports = { buildBrowserEnvironment, installBrowserEnvironment, prepareBrowserPage };
