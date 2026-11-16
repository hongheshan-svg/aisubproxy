const { executePaymentWithRetry } = require('./payment-retry');
const { openPricingCheckout } = require('./pricing-checkout');
const { openApiCheckout } = require('./chatgpt');
const store = require('./mysql-store');
const { getRegionConfig, getRegionBrowserProfile } = require('./region-config');
const { installChatGptSession, bootstrapChatGptSession } = require('./session-auth');
const { connectTaskBrowser, applyCdpEnv, closeTaskBrowser } = require('./browser-runtime');
const { preparePlaywrightProxy } = require('./playwright-proxy');
const { lookupIpGeo, localeForCountry, maskIp } = require('./ip-geo');
const {
    resolveClientEnvironment,
    timezoneFromClientEnv,
    clampNum,
    detectUaInfo,
    buildUaBrands,
    maskId
} = require('./session-environment');
const fs = require('fs');
const path = require('path');

/**
 * Stripe Card Pool Payment Activation Flow
 *
 * 核心流程：
 * 1. 代理连通性检查
 * 2. 从 CDK 记录获取 plan_type
 * 3. 创建 Stripe Checkout Session（根据 plan_type）
 * 4. 打开 Stripe Checkout 页面
 * 5. 使用 payment-retry.js（executePaymentWithRetry）完成信用卡支付
 *    - 自动从卡池分配卡片
 *    - 支持表单重试 + 换卡重试（最多 3 张卡）
 *    - 自动选取免税地址
 *    - 记录账单信息
 * 6. 检测支付结果：成功则继续，失败则标记 payment_failed
 *
 * 进度输出约定（供 server.js / product_activator.js 解析）：
 * - "正在检查代理连通性"  → 代理检查开始
 * - "代理连接成功! 代理公网 IP" → 代理检查通过
 * - "[1] 创建订单" → 订单创建开始
 * - "套餐类型:" → plan_type/region 已解析
 * - "定价页" / "Checkout 页面已打开" → UI 升级流程
 * - "[Stripe] Step" → 填写信用卡与账单地址
 * - "正在使用 Stripe 信用卡卡池支付流程" → 支付流程开始
 * - "最终校验：支付成功!" / "PAYMENT_SUCCESS" → 成功
 * - "支付失败 (payment_failed)" → 全部卡片均失败
 * - "支付失败 (stripe_card_declined)" → 卡被拒
 */

// 全部敏感配置请通过环境变量传入
const CONFIG = {
    chatgptToken: process.env.CHATGPT_TOKEN || "",
    chatgptSessionJson: process.env.CHATGPT_SESSION_JSON || "",
    cdkCode: process.env.CDK_CODE || "",
    email: process.env.ACTIVATION_EMAIL || "",
    planType: process.env.CDK_PLAN_TYPE || "",
    planNameOverride: process.env.PLAN_NAME_OVERRIDE || "",
    paymentRegionOverride: process.env.PAYMENT_REGION_OVERRIDE || "",
    proxy: process.env.PROXY || ""
};

// 录像开关（默认开启；设 RECORD_VIDEO=0 可关闭）与输出目录
const RECORD_VIDEO = String(process.env.RECORD_VIDEO || '1') !== '0';
const VIDEO_DIR = path.join(__dirname, 'debug_screenshots', 'videos');
const VIDEO_TAG = (process.env.JOB_KEY || process.env.CDK_CODE || `${Date.now()}`).replace(/[^a-zA-Z0-9_-]/g, '').slice(0, 32) || `${Date.now()}`;

function buildDebugScreenshotPath(prefix) {
    const subdir = process.env.CHECKOUT_DEBUG_ONLY === '1' ? 'checkout_debug' : 'activation';
    const screenshotDir = path.join(__dirname, 'debug_screenshots', subdir);
    fs.mkdirSync(screenshotDir, { recursive: true });
    return path.join(screenshotDir, `${prefix}_${Date.now()}.png`);
}

// 关闭 context 后把录像重命名为带任务标识的稳定文件名，并输出路径供 server 解析
async function finalizeVideo(context, page, options = {}) {
    const deleteOnSuccess = Boolean(options.deleteOnSuccess);
    if (!RECORD_VIDEO || !context) return null;
    try {
        const pages = (typeof context.pages === 'function' ? context.pages() : []) || [];
        const targetPage = (page && typeof page.video === 'function' && page.video()) ? page : pages.find((p) => p && p.video && p.video());
        const video = targetPage && targetPage.video ? targetPage.video() : null;

        await context.close().catch(() => { });

        if (!video) return null;
        const rawPath = await video.path().catch(() => null);
        if (!rawPath) return null;

        const finalPath = path.join(VIDEO_DIR, `payment_${VIDEO_TAG}_${Date.now()}.webm`);
        try {
            fs.renameSync(rawPath, finalPath);
        } catch (_) {
            try { fs.copyFileSync(rawPath, finalPath); fs.unlinkSync(rawPath); } catch (_) { return rawPath; }
        }

        if (deleteOnSuccess) {
            try {
                fs.unlinkSync(finalPath);
                console.log('🎬 [系统] 支付成功，录像已自动删除以节省磁盘');
                return null;
            } catch (e) {
                console.warn(`⚠️ [系统] 删除成功录像失败: ${e.message}`);
            }
        }

        console.log(`🎬 [系统] 自动化录像已保存: ${finalPath}`);
        console.log(`VIDEO_FILE: ${finalPath}`);
        return finalPath;
    } catch (e) {
        console.warn(`⚠️ [系统] 录像保存失败: ${e.message}`);
        return null;
    }
}

function getAvailableDebugPage(context, preferredPage) {
    if (preferredPage && !preferredPage.isClosed()) {
        return preferredPage;
    }
    if (!context || typeof context.pages !== 'function') {
        return null;
    }
    const alivePages = context.pages().filter((item) => item && !item.isClosed());
    return alivePages.length ? alivePages[alivePages.length - 1] : null;
}

async function captureDebugScreenshot(context, preferredPage, prefix, label = '异常截图') {
    const targetPage = getAvailableDebugPage(context, preferredPage);
    if (!targetPage) {
        console.warn(`⚠️ [系统] ${label}未保存：当前没有可用页面。`);
        return null;
    }

    const screenshotPath = buildDebugScreenshotPath(prefix);
    await targetPage.screenshot({ path: screenshotPath, fullPage: true });
    console.log(`📸 [系统] ${label}已保存: ${screenshotPath}`);
    return screenshotPath;
}

async function isCheckoutErrorPage(page) {
    try {
        const bodyText = String(await page.textContent('body', { timeout: 3000 }).catch(() => '') || '');
        return bodyText.includes('Something went wrong')
            || bodyText.includes('could not be found')
            || bodyText.includes('contact the merchant');
    } catch (_) {
        return false;
    }
}

async function isConnectionClosedPage(page) {
    try {
        const bodyText = String(await page.textContent('body', { timeout: 3000 }).catch(() => '') || '');
        return bodyText.includes('ERR_CONNECTION_CLOSED')
            || bodyText.includes('无法访问此网站')
            || bodyText.includes('意外终止了连接')
            || bodyText.includes('This site can\u2019t be reached')
            || bodyText.includes('This site cannot be reached');
    } catch (_) {
        return false;
    }
}

async function recoverConnectionClosed(page, fallbackUrl = '') {
    if (!(await isConnectionClosedPage(page))) {
        return false;
    }

    console.warn('[Warn] 检测到浏览器连接关闭错误页，正在尝试自动重载...');
    for (let attempt = 1; attempt <= 3; attempt++) {
        await page.reload({ waitUntil: 'domcontentloaded', timeout: 30000 }).catch(async () => {
            const nextUrl = fallbackUrl || page.url();
            if (nextUrl) {
                return page.goto(nextUrl, { waitUntil: 'domcontentloaded', timeout: 30000 }).catch(() => { });
            }
        });
        await page.waitForTimeout(3000);
        if (!(await isConnectionClosedPage(page))) {
            console.log(`[Info] 连接关闭错误页已恢复 (第 ${attempt} 次重载成功)。`);
            return true;
        }
    }

    return false;
}

/**
 * Main Automation logic
 */
async function run() {
    // 切到有头模式调试：HEADFUL=1 node server.js 或 HEADFUL=1 node index.js
    const DEBUG_HEADFUL = process.env.HEADFUL === '1';
    const CHROMIUM_CHANNEL = (process.env.CHROMIUM_CHANNEL || '').trim();

    let proxyCleanup = async () => { };
    let browserSession = null;
    let browser = null;
    let context = null;
    let page = null;
    let paymentSucceeded = false;

    const preparedProxy = await preparePlaywrightProxy(CONFIG.proxy);
    proxyCleanup = preparedProxy.cleanup;
    const proxyConfig = preparedProxy.proxyConfig;

    if (proxyConfig) {
        console.log(`🌐 [系统] 代理已配置${preparedProxy.relayed ? '（SOCKS→本地 HTTP 中继）' : ''}`);
    }

    browserSession = await connectTaskBrowser({
        proxyConfig,
        headful: DEBUG_HEADFUL,
        chromiumChannel: CHROMIUM_CHANNEL
    });
    applyCdpEnv(browserSession);
    browser = browserSession.browser;

    const realUserAgent = browserSession.realUserAgent;

    const paymentRegion = String(CONFIG.paymentRegionOverride || '').trim().toUpperCase()
        || await store.getPaymentRegion();
    const regionCfg = getRegionConfig(paymentRegion);
    const browserProfile = getRegionBrowserProfile(paymentRegion);

    // ===== 探测代理实际出口地区：让地理指纹（时区/语言）跟系统设置的代理 IP 一致 =====
    // 用一个临时 context（走同一代理出口）查询 ip-api，拿到出口 IP 的国家与 IANA 时区。
    // 失败则 proxyGeo=null，指纹回退到 session / 默认值。
    let proxyGeo = null;
    try {
        const probeOpts = {};
        if (proxyConfig) {
            probeOpts.proxy = proxyConfig;
        }
        const probeContext = await browser.newContext(probeOpts);
        try {
            proxyGeo = await lookupIpGeo(probeContext.request);
        } finally {
            await probeContext.close().catch(() => {});
        }
    } catch (geoErr) {
        console.warn(`⚠️ [指纹] 代理出口地区探测失败，地理指纹将回退 session/默认值: ${geoErr.message}`);
    }
    // 由出口地区推导地理 profile（时区来自 ip-api，locale 由国家码推导）
    const geoProfile = proxyGeo
        ? { locale: localeForCountry(proxyGeo.countryCode), timezoneId: proxyGeo.timezone }
        : null;
    if (proxyGeo) {
        console.log(`🌐 [指纹] 代理出口: ${maskIp(proxyGeo.ip)} · ${proxyGeo.countryCode}`
            + `${proxyGeo.regionName ? '/' + proxyGeo.regionName : ''} · ${proxyGeo.timezone || '时区未知'}`);
    }

    // ===== 对齐用户真实环境：读取用户提交的 session 里的 statsigContext / __env =====
    const sessionRawForEnv = CONFIG.chatgptSessionJson || CONFIG.chatgptToken;
    const clientEnv = resolveClientEnvironment(sessionRawForEnv);
    const envUa = String(clientEnv?.userAgent || '').trim();
    const envUaValid = /^Mozilla\/5\.0 /.test(envUa) && /(Chrome|Chromium|Edg|Firefox|Safari)\//.test(envUa);
    const userAgent = envUaValid ? envUa : realUserAgent;
    const uaInfo = detectUaInfo(userAgent);
    const chromeMatch = userAgent.match(/(?:Chrome|Chromium|Edg)\/(\d+)/);
    const chromeMajor = chromeMatch ? Number(chromeMatch[1]) : 147;
    const isChromium = /Chrome\/|Chromium\/|Edg\//.test(userAgent);

    const screenWidth = clampNum(clientEnv?.screen?.width, 800, 5120, 1920);
    const screenHeight = clampNum(clientEnv?.screen?.height, 600, 2880, 1080);
    const viewWidth = clampNum(clientEnv?.window?.innerWidth, 640, screenWidth, Math.round(screenWidth * 0.9));
    const viewHeight = clampNum(clientEnv?.window?.innerHeight, 480, screenHeight, Math.round(screenHeight * 0.85));
    const deviceScaleFactor = clampNum(clientEnv?.devicePixelRatio, 1, 3, 1);
    // 浏览器语言跟随出口 IP：US→en-US、PH→en-PH；页面按钮/文案语言与出口地区一致。
    // Session 的 locale 只在没有出口地理信息时兜底（UA/时区/设备仍以 Session 为准）。
    const egressLocale = proxyGeo ? String(localeForCountry(proxyGeo.countryCode) || '') : '';
    const localeFallback = (geoProfile || browserProfile).locale || 'en-US';
    const locale = String(
        egressLocale
        || clientEnv?.locale
        || (geoProfile ? geoProfile.locale : '')
        || localeFallback
    ).trim();
    const languages = [locale, String(locale).split('-')[0]]
        .map((item) => String(item || '').trim())
        .filter(Boolean)
        .slice(0, 4);
    if (egressLocale && clientEnv?.locale && egressLocale !== clientEnv.locale) {
        console.log(`[指纹] 浏览器语言跟随出口 ${proxyGeo.countryCode}: ${egressLocale}（Session 记录 ${clientEnv.locale}）`);
    }
    let timezoneId = String(
        clientEnv?.timezone
        || timezoneFromClientEnv(clientEnv)
        || (geoProfile ? geoProfile.timezoneId : '')
        || browserProfile.timezoneId || 'Asia/Manila'
    ).trim();
    try {
        Intl.DateTimeFormat('en-US', { timeZone: timezoneId });
    } catch (_) {
        console.warn(`⚠️ [指纹] 用户时区 ${timezoneId} 无效，回退到 ${browserProfile.timezoneId}`);
        timezoneId = browserProfile.timezoneId;
    }

    const fingerprint = {
        chromeMajor,
        isChromium,
        uaPlatform: uaInfo.uaPlatform,
        platformWin: uaInfo.platformWin,
        platformVersion: uaInfo.platformVersion,
        arch: uaInfo.arch,
        isMobile: uaInfo.isMobile,
        maxTouchPoints: uaInfo.isMobile ? clampNum(clientEnv?.maxTouchPoints, 1, 10, 5) : 0,
        languages,
        hardwareConcurrency: clampNum(clientEnv?.hardwareConcurrency, 2, 32, 8),
        deviceMemory: clampNum(clientEnv?.deviceMemory, 1, 32, 8),
        brands: buildUaBrands(userAgent, chromeMajor)
    };

    const viewport = { width: viewWidth, height: viewHeight };
    const contextOptions = {
        userAgent,
        viewport,
        locale,
        timezoneId,
        screen: { width: screenWidth, height: screenHeight },
        deviceScaleFactor,
        isMobile: uaInfo.isMobile,
        hasTouch: uaInfo.isMobile
    };
    if (isChromium) {
        contextOptions.extraHTTPHeaders = {
            'sec-ch-ua': fingerprint.brands.map((b) => `"${b.brand}";v="${b.version}"`).join(', '),
            'sec-ch-ua-mobile': uaInfo.isMobile ? '?1' : '?0',
            'sec-ch-ua-platform': `"${uaInfo.uaPlatform}"`
        };
    }
    if (clientEnv) {
        console.log('[指纹] 对齐策略：UA/平台/语言/时区/设备 ID 以 Session 为准；IP 仅提示（非必选）');
        console.log(`[指纹] 已对齐用户 session 环境（除 IP 外全部沿用）: ${uaInfo.uaPlatform} · ${timezoneId} · ${locale} · ${viewWidth}x${viewHeight}@${deviceScaleFactor}x`
            + `${proxyGeo ? ` · 出口 IP ${maskIp(proxyGeo.ip)}/${proxyGeo.countryCode}` : ''}`);
        console.log(`[指纹] 会话身份: device=${maskId(clientEnv.deviceId) || '-'} · account=${maskId(clientEnv.accountId) || '-'}`
            + ` · sessionIp=${maskIp(clientEnv.ip) || '-'} · country=${clientEnv.country || '-'}${clientEnv.regionCode ? '/' + clientEnv.regionCode : ''}`);
    } else {
        console.log(`[指纹] 未读取到用户环境(statsigContext/__env)，回退默认/代理地区: ${uaInfo.uaPlatform} · ${timezoneId} · ${locale} · ${viewWidth}x${viewHeight}`);
    }
    if (proxyConfig) {
        contextOptions.proxy = proxyConfig;
    }

    // 全程录像，便于回看自动化卡在哪一步（输出到挂载目录，前台可播放）
    if (RECORD_VIDEO) {
        contextOptions.recordVideo = {
            dir: VIDEO_DIR,
            size: { width: 1280, height: 720 }
        };
    }

    context = await browser.newContext(contextOptions);

    // ===== Sentinel 网络抓包（CAPTURE_SENTINEL=1）：捕获 checkout/sentinel/payment 请求响应 =====
    if (process.env.CAPTURE_SENTINEL === '1') {
        const capPath = path.join(VIDEO_DIR, '..', 'sentinel-capture.json');
        const captured = [];
        const interesting = (u) => /\/backend-api\/(payments|sentinel|checkout|subscriptions|billing)|chat-requirements|pay\.openai\.com|\/checkout|arkose|turnstile|proofofwork|stripe/i.test(u);
        const maskTok = (h) => {
            const o = {};
            for (const k of Object.keys(h || {})) {
                const v = String(h[k]);
                o[k] = /authorization|token|cookie|sentinel|proof/i.test(k) ? `${v.slice(0, 28)}…(len=${v.length})` : v;
            }
            return o;
        };
        const dump = () => { try { fs.writeFileSync(capPath, JSON.stringify(captured, null, 2)); } catch (_) {} };
        context.on('request', (req) => {
            if (!interesting(req.url())) return;
            captured.push({ dir: 'REQ', method: req.method(), url: req.url(), headers: maskTok(req.headers()), postData: String(req.postData() || '').slice(0, 800) });
            dump();
        });
        context.on('response', async (resp) => {
            if (!interesting(resp.url())) return;
            let body = '';
            try { body = (await resp.text()).slice(0, 2000); } catch (_) {}
            captured.push({ dir: 'RESP', status: resp.status(), url: resp.url(), headers: maskTok(resp.headers()), body });
            dump();
        });
        console.log(`[抓包] Sentinel 网络抓包已启用 → ${capPath}`);
    }

    const sessionRaw = CONFIG.chatgptSessionJson || CONFIG.chatgptToken;
    // 对齐原则：UA / 平台 / 语言 / 时区 / 设备 ID 一律以用户 Session 为准；
    // IP 不是必选条件，只做提示，不阻断、不改写 Session 内容。
    if (proxyGeo && proxyGeo.ip) {
        const sessionIp = String(clientEnv?.ip || '').trim();
        if (sessionIp && sessionIp !== proxyGeo.ip) {
            console.log(`ℹ️ [指纹] IP 不一致（非必选）: session=${maskIp(sessionIp)} · egress=${maskIp(proxyGeo.ip)}`
                + '；其余环境已按 Session 对齐，继续执行');
        } else if (sessionIp) {
            console.log('✅ [指纹] 出口 IP 与 Session 记录一致');
        }
    }
    const installResult = await installChatGptSession(context, sessionRaw);
    const sessionData = installResult?.sessionData || installResult;
    const cookieVerified = Boolean(installResult?.cookieVerified);

    // ============= 指纹伪装 =============
    await context.addInitScript((fp) => {
        const NavProto = Object.getPrototypeOf(navigator);
        const ScrProto = Object.getPrototypeOf(screen);
        const safeDefine = (obj, key, getter) => {
            try {
                Object.defineProperty(obj, key, { get: getter, configurable: true });
            } catch (_) { /* ignore */ }
        };

        // 隐藏 webdriver
        try { delete Object.getPrototypeOf(navigator).webdriver; } catch (_) { }
        safeDefine(NavProto, 'webdriver', () => undefined);
        try { Object.defineProperty(navigator, 'webdriver', { get: () => undefined, configurable: true }); } catch (_) { }

        // navigator.userAgentData（按用户真实 UA 对齐）
        try {
            if (fp.isChromium) {
                const uaData = {
                    brands: fp.brands,
                    mobile: fp.isMobile,
                    platform: fp.uaPlatform,
                    getHighEntropyValues: () => Promise.resolve({
                        architecture: fp.arch,
                        bitness: '64',
                        mobile: fp.isMobile,
                        model: '',
                        platform: fp.uaPlatform,
                        platformVersion: fp.platformVersion,
                        wow64: false
                    }),
                    toJSON: () => ({ brands: uaData.brands, mobile: uaData.mobile, platform: uaData.platform })
                };
                safeDefine(NavProto, 'userAgentData', () => uaData);
            } else {
                safeDefine(NavProto, 'userAgentData', () => undefined);
            }
        } catch (_) { }

        // plugins / mimeTypes
        try {
            const pdfMime = Object.create(MimeType.prototype);
            Object.defineProperties(pdfMime, {
                type: { get: () => 'application/pdf' },
                suffixes: { get: () => 'pdf' },
                description: { get: () => 'Portable Document Format' }
            });
            const pdfPlugin = Object.create(Plugin.prototype);
            Object.defineProperties(pdfPlugin, {
                name: { get: () => 'Chrome PDF Plugin' },
                filename: { get: () => 'internal-pdf-viewer' },
                description: { get: () => 'Portable Document Format' },
                length: { get: () => 1 },
                0: { get: () => pdfMime }
            });
            pdfPlugin.item = () => pdfMime;
            pdfPlugin.namedItem = () => pdfMime;

            const fakePlugins = Object.create(PluginArray.prototype);
            Object.defineProperties(fakePlugins, {
                length: { get: () => 1 },
                0: { get: () => pdfPlugin }
            });
            fakePlugins.item = () => pdfPlugin;
            fakePlugins.namedItem = (n) => n === pdfPlugin.name ? pdfPlugin : null;
            fakePlugins.refresh = () => { };

            const fakeMimeTypes = Object.create(MimeTypeArray.prototype);
            Object.defineProperties(fakeMimeTypes, {
                length: { get: () => 1 },
                0: { get: () => pdfMime }
            });
            fakeMimeTypes.item = () => pdfMime;
            fakeMimeTypes.namedItem = (n) => n === pdfMime.type ? pdfMime : null;

            safeDefine(NavProto, 'plugins', () => fakePlugins);
            safeDefine(NavProto, 'mimeTypes', () => fakeMimeTypes);
        } catch (_) { }

        // 语言、平台、硬件（按用户真实环境对齐）
        safeDefine(NavProto, 'languages', () => fp.languages);
        safeDefine(NavProto, 'language', () => fp.languages[0]);
        safeDefine(NavProto, 'platform', () => fp.platformWin);
        safeDefine(NavProto, 'hardwareConcurrency', () => fp.hardwareConcurrency);
        safeDefine(NavProto, 'deviceMemory', () => fp.deviceMemory);
        safeDefine(NavProto, 'maxTouchPoints', () => fp.maxTouchPoints);
        safeDefine(NavProto, 'vendor', () => 'Google Inc.');

        // navigator.connection
        try {
            const conn = { effectiveType: '4g', rtt: 100, downlink: 10, saveData: false };
            safeDefine(NavProto, 'connection', () => conn);
        } catch (_) { }

        // window.chrome
        try {
            const fakeChrome = {
                app: {
                    isInstalled: false,
                    InstallState: { DISABLED: 'disabled', INSTALLED: 'installed', NOT_INSTALLED: 'not_installed' },
                    RunningState: { CANNOT_RUN: 'cannot_run', READY_TO_RUN: 'ready_to_run', RUNNING: 'running' },
                    getDetails: () => null,
                    getIsInstalled: () => false
                },
                runtime: {
                    OnInstalledReason: { CHROME_UPDATE: 'chrome_update', INSTALL: 'install', SHARED_MODULE_UPDATE: 'shared_module_update', UPDATE: 'update' },
                    OnRestartRequiredReason: { APP_UPDATE: 'app_update', OS_UPDATE: 'os_update', PERIODIC: 'periodic' },
                    PlatformArch: { ARM: 'arm', ARM64: 'arm64', MIPS: 'mips', MIPS64: 'mips64', X86_32: 'x86-32', X86_64: 'x86-64' },
                    PlatformNaclArch: { ARM: 'arm', MIPS: 'mips', MIPS64: 'mips64', X86_32: 'x86-32', X86_64: 'x86-64' },
                    PlatformOs: { ANDROID: 'android', CROS: 'cros', LINUX: 'linux', MAC: 'mac', OPENBSD: 'openbsd', WIN: 'win' },
                    RequestUpdateCheckStatus: { NO_UPDATE: 'no_update', THROTTLED: 'throttled', UPDATE_AVAILABLE: 'update_available' },
                    connect: () => { },
                    sendMessage: () => { }
                },
                csi: () => ({ onloadT: Date.now(), pageT: Date.now() - 1000, startE: Date.now() - 2000, tran: 15 }),
                loadTimes: () => ({
                    requestTime: Date.now() / 1000 - 2,
                    startLoadTime: Date.now() / 1000 - 1.5,
                    commitLoadTime: Date.now() / 1000 - 1,
                    finishDocumentLoadTime: Date.now() / 1000 - 0.5,
                    finishLoadTime: Date.now() / 1000,
                    firstPaintTime: Date.now() / 1000 - 0.3,
                    firstPaintAfterLoadTime: 0,
                    navigationType: 'Other',
                    wasFetchedViaSpdy: true,
                    wasNpnNegotiated: true,
                    npnNegotiatedProtocol: 'h2',
                    wasAlternateProtocolAvailable: false,
                    connectionInfo: 'h2'
                })
            };
            Object.defineProperty(window, 'chrome', { value: fakeChrome, writable: true, configurable: true });
        } catch (_) { }

        // permissions.query
        try {
            const origQuery = navigator.permissions.query.bind(navigator.permissions);
            navigator.permissions.query = (params) => {
                if (params && params.name === 'notifications') {
                    return Promise.resolve({ state: typeof Notification !== 'undefined' ? Notification.permission : 'default', onchange: null });
                }
                return origQuery(params).catch(() => ({ state: 'prompt', onchange: null }));
            };
        } catch (_) { }

        // screen
        safeDefine(ScrProto, 'availHeight', () => 1032);
        safeDefine(ScrProto, 'availWidth', () => 1920);
        safeDefine(ScrProto, 'colorDepth', () => 24);
        safeDefine(ScrProto, 'pixelDepth', () => 24);
        safeDefine(ScrProto, 'width', () => 1920);
        safeDefine(ScrProto, 'height', () => 1080);

        // Canvas 微噪声
        try {
            const origToDataURL = HTMLCanvasElement.prototype.toDataURL;
            HTMLCanvasElement.prototype.toDataURL = function (...args) {
                const ctx = this.getContext('2d');
                if (ctx) {
                    try {
                        const w = this.width, h = this.height;
                        if (w > 0 && h > 0) {
                            const data = ctx.getImageData(0, 0, 1, 1);
                            data.data[3] = Math.max(1, data.data[3] - 1);
                            ctx.putImageData(data, 0, 0);
                        }
                    } catch (_) { }
                }
                return origToDataURL.apply(this, args);
            };
        } catch (_) { }

        // WebGL
        try {
            const fakeWebGL = (gl) => {
                const origGetParameter = gl.getParameter.bind(gl);
                gl.getParameter = function (param) {
                    if (param === 0x9245) return 'Google Inc. (Intel)';
                    if (param === 0x9246) return 'ANGLE (Intel, Intel(R) UHD Graphics 620 Direct3D11 vs_5_0 ps_5_0, D3D11)';
                    return origGetParameter(param);
                };
            };
            const origGetCtx = HTMLCanvasElement.prototype.getContext;
            HTMLCanvasElement.prototype.getContext = function (type, ...args) {
                const ctx = origGetCtx.call(this, type, ...args);
                if (ctx && (type === 'webgl' || type === 'webgl2' || type === 'experimental-webgl')) {
                    try { fakeWebGL(ctx); } catch (_) { }
                }
                return ctx;
            };
        } catch (_) { }

        // ChromeDriver 痕迹
        try {
            for (const key of Object.keys(window)) {
                if (/^(cdc_|\$cdc_|_phantom|callPhantom|webdriver-|driver-)/.test(key)) {
                    try { delete window[key]; } catch (_) { }
                }
            }
        } catch (_) { }

        // Notification.permission
        try {
            if (typeof Notification !== 'undefined') {
                Object.defineProperty(Notification, 'permission', { get: () => 'default', configurable: true });
            }
        } catch (_) { }
    }, fingerprint);

    try {
        // --- Phase 0: Proxy Connectivity Check ---
        let egressIp = '';
        if (proxyConfig) {
            console.log('正在检查代理连通性...');
            try {
                const probeResponse = await context.request.get("http://api.ipify.org/?format=text", {
                    timeout: 15000
                });
                if (probeResponse.ok()) {
                    const ip = (await probeResponse.text()).trim();
                    egressIp = ip;
                    const ipMasked = String(ip).replace(/^(\d+)\.(\d+)\.(\d+)\.(\d+)$/, '***.***.$3.$4');
                    console.log(`✅ [系统] 代理连接成功! 代理公网 IP: ${ipMasked}`);
                } else {
                    throw new Error(`代理响应异常: HTTP ${probeResponse.status()}`);
                }
            } catch (proxyError) {
                console.log("    [!] 请检查 PROXY 配置是否正确，或者账号余额是否充足。");
                throw proxyError;
            }
        }

        // 指纹（UA/时区/语言/设备 ID）已在创建 context 前以用户 Session 为准对齐；
        // IP 不是必选条件，仅走代理真实出口，这里不做一致性中断。

        // --- Phase 1: Resolve payment parameters ---
        const debugOnly = process.env.CHECKOUT_DEBUG_ONLY === '1';
        if (debugOnly) {
            console.log('[调试] 支付链接调试模式：浏览器注入 Session → Checkout API → 输出链接（不执行支付）');
        } else {
            console.log('[1] 准备自助充值流程...');
        }

        // Determine plan_type: prefer env variable, fallback to CDK lookup
        let planType = CONFIG.planType;
        const cdkCode = CONFIG.cdkCode;
        const email = CONFIG.email;

        if (cdkCode) {
            try {
                const cdkDetails = await store.verifyCdkDetails(cdkCode);
                if (cdkDetails && cdkDetails.plan_type) {
                    planType = cdkDetails.plan_type;
                }
            } catch (_) {
                // CDK lookup failed, use default
            }
        }
        if (!planType) {
            planType = 'plus';
        }

        // Resolve region and currency (与浏览器 profile 一致)
        const billingCountry = paymentRegion;
        const billingCurrency = regionCfg ? regionCfg.currency : 'USD';

        console.log(`[1] 套餐类型: ${planType}, 地区: ${paymentRegion}, 币种: ${billingCurrency}`);

        page = await context.newPage();
        page.on('close', () => {
            console.warn(`⚠️ [系统] 当前页面已关闭，关闭前最后 URL: ${page.url()}`);
        });

        const loginInfo = await bootstrapChatGptSession(page, sessionRaw, { sessionData, cookieVerified });
        if (loginInfo.email && !email) {
            console.log(`[Info] 账号邮箱: ${loginInfo.email}`);
        }

        // --- Phase 2: API 创建 Checkout（注入账单地区），失败时回退 UI 定价页 ---
        const checkoutMode = String(process.env.CHECKOUT_MODE || 'api').toLowerCase();
        let checkoutOpened = false;
        let checkoutResult = null;
        const planNameOverride = String(CONFIG.planNameOverride || '').trim() || undefined;

        if (checkoutMode !== 'ui') {
            try {
                checkoutResult = await openApiCheckout(page, {
                    accessToken: loginInfo.session?.accessToken || CONFIG.chatgptToken,
                    planType,
                    country: billingCountry,
                    currency: billingCurrency,
                    planNameOverride,
                    clientEnv,
                    verifyPage: !debugOnly
                });
                checkoutOpened = true;
            } catch (apiError) {
                console.warn(`[Warn] API Checkout 失败: ${apiError.message}`);
                if (debugOnly) {
                    console.warn('[调试] API 路径不可用（Checkout 接口新增 Sentinel 风控校验），改走 UI 定价页生成支付链接...');
                } else if (checkoutMode === 'api') {
                    console.log('[Info] 正在回退到 UI 定价页流程...');
                } else {
                    throw apiError;
                }
            }
        }

        if (!checkoutOpened) {
            console.log('🧭 [步骤] 正在打开定价页并选择升级套餐...');
            await openPricingCheckout(page, {
                region: billingCountry,
                planType
            });
            checkoutResult = { checkoutUrl: page.url() };
        }

        if (debugOnly) {
            const checkoutUrl = checkoutResult?.checkoutUrl || page.url();
            console.log(`🔗 [调试] 支付链接: ${checkoutUrl}`);

            // 调试模式：等付款界面真正渲染后再保持一段时间，让录像能看到完整页面。
            // 若默认选中 Saved（已保存卡），主动切到 Card 新卡，录像里能看到卡号输入界面。
            if (String(process.env.CHECKOUT_DEBUG_SELECT_CARD || '1') !== '0') {
                try {
                    const { ensureCardPaymentTabSelected, uncheckSavePaymentDetails } = require('./stripe-payment');
                    const cardSelected = await ensureCardPaymentTabSelected(page);
                    console.log(cardSelected
                        ? '[调试] 已切换到 Card 新卡标签（避免使用 Saved）'
                        : '[调试] 未检测到 Saved/Card 标签，保持当前支付方式');
                    await uncheckSavePaymentDetails(page, 4000);
                    await page.waitForTimeout(1500);
                } catch (cardErr) {
                    console.warn(`[调试] 切换 Card 标签失败: ${cardErr.message}`);
                }
            }
            const holdMs = Math.max(0, Number(process.env.CHECKOUT_DEBUG_HOLD_MS || 15000));
            if (holdMs > 0) {
                console.log(`[调试] 等待付款界面渲染，并保持 ${(holdMs / 1000).toFixed(1)}s（用于录像/截图）...`);
                await page.getByText(/Payment method|付款方式/i).first()
                    .waitFor({ state: 'visible', timeout: 20000 })
                    .catch(() => console.warn('[调试] 未在 20s 内检测到 Payment method 文案，继续等待录像'));
                await page.waitForTimeout(holdMs);
                try {
                    const shotDir = path.join(__dirname, 'debug_screenshots', 'checkout_debug');
                    fs.mkdirSync(shotDir, { recursive: true });
                    const shotPath = path.join(shotDir, `checkout_success_${Date.now()}.png`);
                    await page.screenshot({ path: shotPath, fullPage: false });
                    console.log(`[调试] 付款界面截图: ${shotPath}`);
                } catch (shotErr) {
                    console.warn(`[调试] 付款界面截图失败: ${shotErr.message}`);
                }
                console.log('CHECKOUT_DEBUG_HOLD_DONE');
            }

            console.log(`CHECKOUT_URL: ${checkoutUrl}`);
            console.log('CHECKOUT_DEBUG_SUCCESS');
            return;
        }

        console.log('✅ [步骤] Checkout 页面已打开，开始信用卡支付流程...');

        const stripeSessionMatch = String(page.url() || '').match(/(oaics_[a-f0-9]+)/i);
        const stripeSessionId = stripeSessionMatch ? stripeSessionMatch[1] : null;

        // --- Phase 4: Execute Payment with Card Pool Retry ---
        console.log("[步骤] 正在使用 Stripe 信用卡卡池支付流程（单次尝试）...");
        const paymentResult = await executePaymentWithRetry(page, {
            planType,
            cdkCode,
            email,
            stripeSessionId
        });

        if (paymentResult.success) {
            paymentSucceeded = true;
            console.log(`    [+] 最终校验：支付成功! (stripe_card_payment)`);
            console.log("PAYMENT_SUCCESS");
            for (const screenshotPath of paymentResult.screenshots || []) {
                console.log(`SUCCESS_SCREENSHOT: ${screenshotPath}`);
            }
        } else {
            const errorMsg = paymentResult.error || '支付失败（未知原因）';
            for (const screenshotPath of paymentResult.screenshots || []) {
                console.log(`FAILURE_SCREENSHOT: ${screenshotPath}`);
            }
            console.error(`❌ [支付失败] ${errorMsg} (manual_intervention)`);
            throw new Error(`支付失败 (manual_intervention): ${errorMsg}`);
        }

    } catch (e) {
        console.error("❌ [运行时错误]:", e.message);
        try {
            const errorShot = await captureDebugScreenshot(context, page, 'error');
            if (errorShot) {
                console.log(`FAILURE_SCREENSHOT: ${errorShot}`);
            }
        } catch (err) {
            console.error(`⚠️ [系统] 异常截图保存失败: ${err.message}`);
        }
        // 用 exitCode 而非 process.exit()，确保下方 finally 能完整执行（录像需 context 关闭后才落盘）
        process.exitCode = 1;
    } finally {
        console.log("👋 [系统] 流程结束，正在关闭浏览器...");
        // 先关闭 context 以 flush 录像并取回路径，再关浏览器
        await finalizeVideo(context, page, { deleteOnSuccess: paymentSucceeded }).catch(() => { });
        await closeTaskBrowser(browserSession, browser);
        await proxyCleanup();
    }
}

run().finally(() => {
    // 录像已在 run() 的 finally 中落盘；此处兜底强制退出，避免 mysql 连接池保持事件循环导致子进程不退出
    setTimeout(() => process.exit(process.exitCode || 0), 800).unref();
});
