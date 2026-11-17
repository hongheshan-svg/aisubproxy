'use strict';
const assert = require('node:assert/strict');
const { resolveClientEnvironment } = require('../session-environment');
require('../browser-runtime');
const { chromium } = require('playwright-extra');
const { buildBrowserEnvironment, installBrowserEnvironment, prepareBrowserPage } = require('../browser-environment');
(async () => {
 const browser = await chromium.launch({ executablePath: process.env.PLAYWRIGHT_TEST_EXECUTABLE || undefined, headless: true });
 try {
  for (const item of [
   { name: 'mac', userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36', screen: { width: 1512, height: 982 }, window: { innerWidth: 1400, innerHeight: 850 }, devicePixelRatio: 2 },
   { name: 'android', userAgent: 'Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Mobile Safari/537.36', screen: { width: 393, height: 852 }, window: { innerWidth: 393, innerHeight: 730 }, devicePixelRatio: 3 },
   { name: 'safari', userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1' }
  ]) {
   const profile = buildBrowserEnvironment(resolveClientEnvironment({ statsigContext: { userAgent: item.userAgent }, __env: item }), { realUserAgent: item.userAgent, locale: 'en-US', timezoneId: 'America/New_York' });
   const context = await browser.newContext(profile.contextOptions);
   await installBrowserEnvironment(context, profile);
   let headers;
   await context.route('https://example.test/**', async route => {
    headers = await route.request().allHeaders();
    await route.fulfill({ contentType: 'text/html', body: '<meta name="viewport" content="width=device-width, initial-scale=1"><body>Local environment check</body>' });
   });
   const page = await prepareBrowserPage(await context.newPage());
   await page.goto('https://example.test/');
   const observed = await page.evaluate(async () => ({ ua: navigator.userAgent, platform: navigator.platform, screen: { width: screen.width, height: screen.height }, width: innerWidth, height: innerHeight, dpr: devicePixelRatio,
    uaData: navigator.userAgentData ? await navigator.userAgentData.getHighEntropyValues(['architecture','platformVersion','fullVersionList','bitness']) : null,
    touch: navigator.maxTouchPoints, timezone: Intl.DateTimeFormat().resolvedOptions().timeZone }));
   assert.equal(observed.ua, item.userAgent); assert.equal(headers['user-agent'], item.userAgent);
   assert.deepEqual(observed.screen, profile.contextOptions.screen);
   assert.equal(observed.width, profile.contextOptions.viewport.width); assert.equal(observed.dpr, profile.contextOptions.deviceScaleFactor);
   assert.equal(observed.platform, profile.uaOverride.platform);
   if (item.name !== 'safari') {
    assert.equal(observed.uaData.platform, profile.uaOverride.userAgentMetadata.platform);
    assert.equal(observed.uaData.platformVersion, ''); assert.equal(observed.uaData.architecture, '');
    assert.equal(headers['sec-ch-ua-platform'], '"'+observed.uaData.platform+'"');
    assert.ok(headers['sec-ch-ua'].includes('157'));
    assert.ok(observed.uaData.fullVersionList.filter(b => /Chrome|Chromium/.test(b.brand)).every(b => b.version.startsWith('157.')));
    assert.ok(observed.uaData.fullVersionList.every(b => !b.brand.includes('Headless')));
   } else { assert.ok(!headers['sec-ch-ua']); }
   console.log(JSON.stringify({ case: item.name, passed: true, observed, hints: { platform: headers['sec-ch-ua-platform'], mobile: headers['sec-ch-ua-mobile'] } }));
   await context.close();
  }
 } finally { await browser.close(); }
})().catch(e => { console.error(e); process.exitCode = 1; });
