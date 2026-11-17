const { executePaymentWithRetry } = require('./payment-retry');
const { openPricingCheckout } = require('./pricing-checkout');
const { openApiCheckout } = require('./chatgpt');
const { ensureCardPaymentTabSelected, uncheckSavePaymentDetails } = require('./stripe-payment');
const store = require('./mysql-store');
const { getRegionConfig, getRegionBrowserProfile } = require('./region-config');
const { installChatGptSession, bootstrapChatGptSession } = require('./session-auth');
const { connectTaskBrowser, applyCdpEnv, closeTaskBrowser } = require('./browser-runtime');
const { preparePlaywrightProxy } = require('./playwright-proxy');
const { lookupIpGeo, localeForCountry, maskIp } = require('./ip-geo');
const {
    resolveClientEnvironment,
    timezoneFromClientEnv,
    maskId
} = require('./session-environment');
const { buildBrowserEnvironment, installBrowserEnvironment, prepareBrowserPage } = require('./browser-environment');
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

    try {
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

        const environment = buildBrowserEnvironment(clientEnv, { realUserAgent, locale, timezoneId });
        const contextOptions = environment.contextOptions;
        console.log(`[环境] UA=${clientEnv?.userAgent ? 'Session' : '运行浏览器'} · ${timezoneId} · ${locale}`
            + ` · ${contextOptions.viewport.width}x${contextOptions.viewport.height}@${contextOptions.deviceScaleFactor}x`);
        if (clientEnv) {
            console.log(`[环境] device=${maskId(clientEnv.deviceId) || '-'} · account=${maskId(clientEnv.accountId) || '-'}；未采集的硬件信息不推断为用户真实配置`);
        }
        // APIRequestContext 不经过页面 CDP；显式复用最终 UA/语言与同一身份字段。
        const requestEnv = { ...clientEnv, locale, userAgent: contextOptions.userAgent, userAgentData: environment.uaOverride.userAgentMetadata };
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
        await installBrowserEnvironment(context, environment);

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
        // UA / 平台 / 设备 ID 使用已有 Session 信息；语言保留当前出口策略。
        // IP 不是必选条件，只做提示，不阻断、不改写 Session 内容。
        if (proxyGeo && proxyGeo.ip) {
            const sessionIp = String(clientEnv?.ip || '').trim();
            if (sessionIp && sessionIp !== proxyGeo.ip) {
                console.log(`ℹ️ [指纹] IP 不一致（非必选）: session=${maskIp(sessionIp)} · egress=${maskIp(proxyGeo.ip)}`
                    + '；使用已提供的环境字段，继续执行');
            } else if (sessionIp) {
                console.log('✅ [指纹] 出口 IP 与 Session 记录一致');
            }
        }
        const installResult = await installChatGptSession(context, sessionRaw, { clientEnv: requestEnv });
        const sessionData = installResult?.sessionData || installResult;
        const cookieVerified = Boolean(installResult?.cookieVerified);

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

        // 所有已知环境字段均在首次页面导航前设置；
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

        page = await prepareBrowserPage(await context.newPage());
        page.on('close', () => {
            console.warn(`⚠️ [系统] 当前页面已关闭，关闭前最后 URL: ${page.url()}`);
        });

        const loginInfo = await bootstrapChatGptSession(page, sessionRaw, { sessionData, cookieVerified, clientEnv: requestEnv });
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
                    clientEnv: requestEnv,
                    verifyPage: true
                });
                checkoutOpened = true;
            } catch (apiError) {
                console.warn(`[Warn] API Checkout 失败: ${apiError.message}`);
                if (planType === 'pro_500') throw apiError;
                if (debugOnly) {
                    console.warn('[调试] API 路径不可用，尝试通过网页升级入口打开支付页...');
                } else if (checkoutMode === 'api') {
                    console.log('[Info] 正在回退到 UI 定价页流程...');
                } else {
                    throw apiError;
                }
            }
        }

        if (!checkoutOpened) {
            if (planType === 'pro_500') throw new Error('Pro 500 仅支持已配置的 Checkout API，不能回退到其他档位');
            console.log('🧭 [步骤] 正在打开定价页并选择升级套餐...');
            await openPricingCheckout(page, {
                region: billingCountry,
                planType
            });
            checkoutResult = { checkoutUrl: page.url() };
        }

        // API 与网页升级两条路径共用此关卡，调试和正式任务都必须进入新卡表单。
        await ensureCardPaymentTabSelected(page);
        await uncheckSavePaymentDetails(page, 4000);
        console.log('CARD_ENTRY_READY');

        if (debugOnly) {
            const checkoutUrl = checkoutResult?.checkoutUrl || page.url();
            console.log(`🔗 [调试] 支付链接: ${checkoutUrl}`);

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
        await context?.close().catch(() => {});
        await closeTaskBrowser(browserSession, browser);
        await proxyCleanup();
    }
}

run().finally(() => {
    // 录像已在 run() 的 finally 中落盘；此处兜底强制退出，避免 mysql 连接池保持事件循环导致子进程不退出
    setTimeout(() => process.exit(process.exitCode || 0), 800).unref();
});
