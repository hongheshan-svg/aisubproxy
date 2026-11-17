'use strict';

/**
 * 浏览器池客户端：子进程通过 CDP 接入 server 预热的 Chromium 槽位
 */

const { chromium } = require('playwright-extra');
const StealthPlugin = require('puppeteer-extra-plugin-stealth');
const { probeUserAgent, DEFAULT_UA } = require('./browser-standalone');

const _clientStealth = StealthPlugin();
// 环境由 browser-environment 统一设置；禁止插件填入另一套 UA/硬件值。
for (const key of ['user-agent-override', 'navigator.hardwareConcurrency', 'navigator.languages', 'webgl.vendor', 'window.outerdimensions']) {
    _clientStealth.enabledEvasions.delete(key);
}
chromium.use(_clientStealth);

/**
 * @param {object} options
 * @param {string} options.poolCdpUrl
 * @param {string|number} [options.poolSlotId]
 */
async function connectPoolBrowser(options = {}) {
    const poolCdpUrl = String(options.poolCdpUrl || '').trim();
    if (!poolCdpUrl) {
        throw new Error('浏览器池模式缺少 BROWSER_POOL_CDP_URL');
    }

    const slotLabel = options.poolSlotId != null ? String(options.poolSlotId) : '?';
    console.log(`♻️ [Browser/pool] 接入 slot=${slotLabel} (${poolCdpUrl})`);

    const browser = await chromium.connectOverCDP(poolCdpUrl);

    return {
        mode: 'pool',
        browser,
        ownsBrowser: false,
        cdpUrl: poolCdpUrl,
        poolSlotId: slotLabel,
        realUserAgent: await probeUserAgent(browser, DEFAULT_UA)
    };
}

module.exports = {
    connectPoolBrowser
};
