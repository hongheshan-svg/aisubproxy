'use strict';

const fs = require('fs');

const CHATGPT_ORIGIN = 'https://chatgpt.com';
const {
    isLoginRedirectUrl,
    isHardLoginRedirectUrl,
    isCheckoutPageUrl,
    shouldBlockLoginNavigation,
    isLoginPageContent,
    hasVisibleLoginChrome,
    hasLoggedInChatUi,
    waitForLoggedInChatUi,
    hasLoggedInSessionApi,
    buildSessionNotLoggedInError
} = require('./auth-page-detect');

function decodeJwtPayload(token) {
    const parts = String(token || '').split('.');
    if (parts.length !== 3) {
        return null;
    }
    try {
        const normalized = parts[1].replace(/-/g, '+').replace(/_/g, '/');
        const padded = normalized.padEnd(normalized.length + ((4 - normalized.length % 4) % 4), '=');
        return JSON.parse(Buffer.from(padded, 'base64').toString('utf8'));
    } catch (_) {
        return null;
    }
}

function extractProfileFromToken(accessToken) {
    const payload = decodeJwtPayload(accessToken);
    if (!payload) {
        return { email: '', accountId: '', userId: '' };
    }
    const authInfo = payload['https://api.openai.com/auth'] || {};
    const profile = payload['https://api.openai.com/profile'] || {};
    return {
        email: profile.email || '',
        accountId: authInfo.chatgpt_account_id || '',
        userId: authInfo.chatgpt_user_id || ''
    };
}

function buildSessionPayload(accessToken, sessionJson = null) {
    const token = String(accessToken || '').trim();
    const profile = extractProfileFromToken(token);
    const base = sessionJson && typeof sessionJson === 'object' ? { ...sessionJson } : {};
    const user = base.user || (profile.email || profile.userId ? {
        id: profile.userId || `user-${profile.accountId || 'session'}`,
        email: profile.email || undefined,
        name: profile.email || undefined
    } : null);

    const expires = base.expires
        || new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString();

    return {
        ...base,
        user,
        expires,
        accessToken: base.accessToken || base.access_token || token,
        authProvider: base.authProvider || 'google-oauth2'
    };
}

function parseSessionJson(raw) {
    const content = String(raw || '').trim();
    if (!content || !content.startsWith('{')) {
        return null;
    }
    try {
        const data = JSON.parse(content);
        if (data?.accessToken || data?.access_token || data?.user) {
            return data;
        }
    } catch (_) {
        return null;
    }
    return null;
}

const CHATGPT_COOKIE_URL = 'https://chatgpt.com';
const SESSION_TOKEN_BASE = '__Secure-next-auth.session-token';
/** 与 NextAuth SessionStore 一致：4096 - 160 */
const SESSION_COOKIE_CHUNK_SIZE = 3936;

function isSessionTokenCookieName(name) {
    return name === SESSION_TOKEN_BASE || name.startsWith(`${SESSION_TOKEN_BASE}.`);
}

/** 超长 sessionToken 按 NextAuth 规则拆成 .0 / .1 / … 多块 Cookie */
function expandSessionTokenCookies(value) {
    const token = sanitizeCookieValue(value);
    if (!token) {
        return [];
    }
    if (token.length <= SESSION_COOKIE_CHUNK_SIZE) {
        return [{ name: SESSION_TOKEN_BASE, value: token }];
    }
    const chunkCount = Math.ceil(token.length / SESSION_COOKIE_CHUNK_SIZE);
    const chunks = [];
    for (let i = 0; i < chunkCount; i++) {
        chunks.push({
            name: `${SESSION_TOKEN_BASE}.${i}`,
            value: token.substr(i * SESSION_COOKIE_CHUNK_SIZE, SESSION_COOKIE_CHUNK_SIZE)
        });
    }
    return chunks;
}

function hasStoredSessionToken(cookies) {
    return (cookies || []).some((item) => isSessionTokenCookieName(item.name));
}

function parseCookieHeader(header) {
    const pairs = [];
    for (const rawPart of String(header || '').split(';')) {
        const part = rawPart.trim();
        if (!part || !part.includes('=')) continue;
        const eq = part.indexOf('=');
        const name = part.slice(0, eq).trim();
        const value = part.slice(eq + 1).trim();
        if (name && value) {
            pairs.push({ name, value });
        }
    }
    return pairs;
}

function isChatGptCookieDomain(domain) {
    const value = String(domain || '').toLowerCase();
    return !value || value.includes('chatgpt.com');
}

function normalizeSameSite(raw) {
    const value = String(raw || '').trim().toLowerCase();
    if (value === 'no_restriction' || value === 'none') {
        return 'None';
    }
    if (value === 'strict') {
        return 'Strict';
    }
    return 'Lax';
}

function sanitizeCookieValue(value) {
    return String(value || '')
        .replace(/^["']+|["']+$/g, '')
        .replace(/[\r\n\0]/g, '')
        .trim();
}

function isValidCookieName(name) {
    return /^[!#$%&'*+\-.0-9A-Z^_`a-z|~]+$/.test(name);
}

function toPlaywrightCookie(spec) {
    if (!spec || typeof spec !== 'object') {
        return null;
    }
    const name = String(spec.name || '').trim();
    const value = sanitizeCookieValue(spec.value);
    if (!name || !value || !isValidCookieName(name)) {
        return null;
    }

    const sameSite = normalizeSameSite(spec.sameSite);
    const secure = sameSite === 'None' || name.startsWith('__Host-') || name.startsWith('__Secure-')
        ? true
        : spec.secure !== false;
    const httpOnly = spec.httpOnly !== false;

    const base = { name, value, secure, httpOnly, sameSite };

    // __Host- / __Secure- 前缀：只能用 url，不能带 domain/path
    if (name.startsWith('__Host-') || name.startsWith('__Secure-')) {
        return { ...base, url: CHATGPT_COOKIE_URL };
    }

    const hostOnly = spec.hostOnly === true;
    const rawDomain = String(spec.domain || 'chatgpt.com').trim().toLowerCase() || 'chatgpt.com';
    const bareDomain = rawDomain.replace(/^\./, '');
    const domain = hostOnly ? bareDomain : (rawDomain.startsWith('.') ? rawDomain : `.${bareDomain}`);
    const path = String(spec.path || '/').trim() || '/';

    return { ...base, domain, path };
}

function toPlaywrightCookieFallback(cookie) {
    if (!cookie?.name || !cookie?.value) {
        return null;
    }
    const { name, value, secure, httpOnly, sameSite } = cookie;
    if (name.startsWith('__Host-')) {
        return { name, value, path: '/', secure: true, httpOnly, sameSite, url: CHATGPT_COOKIE_URL };
    }
    if (name.startsWith('__Secure-')) {
        return { name, value, domain: 'chatgpt.com', path: '/', secure: true, httpOnly, sameSite };
    }
    return null;
}

async function addCookieSafe(context, cookie) {
    try {
        await context.addCookies([cookie]);
        return null;
    } catch (primaryErr) {
        const fallback = toPlaywrightCookieFallback(cookie);
        if (!fallback) {
            return primaryErr.message;
        }
        try {
            await context.addCookies([fallback]);
            return null;
        } catch (fallbackErr) {
            return fallbackErr.message || primaryErr.message;
        }
    }
}

async function injectChatGptCookies(context, cookieSpecs) {
    if (!cookieSpecs.length) {
        return { injected: 0, hasSessionToken: false, failed: [] };
    }

    const warmup = await context.newPage();
    try {
        await warmup.goto(`${CHATGPT_COOKIE_URL}/`, {
            waitUntil: 'domcontentloaded',
            timeout: 90000
        });
    } catch (_) { /* 代理慢时仍尝试写 Cookie */ }

    const playwrightCookies = cookieSpecs
        .map((spec) => toPlaywrightCookie(spec))
        .filter(Boolean);

    const failed = [];
    let injected = 0;
    for (const cookie of playwrightCookies) {
        const error = await addCookieSafe(context, cookie);
        if (error) {
            failed.push({ name: cookie.name, error });
            console.warn(`[Session] Cookie 注入跳过 ${cookie.name}: ${error}`);
            continue;
        }
        injected += 1;
    }

    if (failed.length) {
        console.warn(`[Session] ${failed.length}/${playwrightCookies.length} 个 Cookie 注入失败`);
    }

    const stored = await context.cookies(CHATGPT_COOKIE_URL);
    const hasSessionToken = hasStoredSessionToken(stored);
    await warmup.close().catch(() => {});

    return {
        injected,
        hasSessionToken,
        storedNames: stored.map((c) => c.name),
        failed
    };
}

function collectCookieSpecs(sessionData, sessionJson) {
    const specs = [];
    const seen = new Set();
    const seenNames = new Set();

    const push = (name, value, extra = {}) => {
        const n = String(name || '').trim();
        const v = String(value || '').trim();
        if (!n || !v) return;
        const key = `${n}\0${v}`;
        if (seen.has(key)) return;
        seen.add(key);
        seenNames.add(n);
        specs.push({ name: n, value: v, ...extra });
    };

    for (const source of [sessionJson?.cookies, sessionData?.cookies]) {
        if (!Array.isArray(source)) continue;
        for (const item of source) {
            if (!item || typeof item !== 'object') continue;
            const name = String(item.name || '').trim();
            const value = String(item.value || '').trim();
            if (!name || !value) continue;
            if (!isChatGptCookieDomain(item.domain)) continue;
            push(name, value, {
                domain: item.domain,
                path: item.path,
                secure: item.secure,
                httpOnly: item.httpOnly,
                sameSite: item.sameSite,
                hostOnly: item.hostOnly
            });
        }
    }

    for (const header of [sessionData?.cookieHeader, sessionData?.cookie_header, sessionJson?.cookieHeader, sessionJson?.cookie_header]) {
        for (const pair of parseCookieHeader(header)) {
            push(pair.name, pair.value);
        }
    }

    const hasExportedSessionToken = [...seenNames].some((name) => isSessionTokenCookieName(name));

    const sessionToken = String(
        process.env.CHATGPT_SESSION_COOKIE
        || process.env.CHATGPT_SESSION_TOKEN
        || sessionData?.sessionToken
        || sessionData?.session_token
        || sessionData?.['__Secure-next-auth.session-token']
        || ''
    ).trim();

    if (sessionToken && !hasExportedSessionToken) {
        const chunks = expandSessionTokenCookies(sessionToken);
        if (chunks.length > 1) {
            console.log(`[Session] session-token 长度 ${sessionToken.length}，按 NextAuth 分 ${chunks.length} 块注入`);
        }
        for (const chunk of chunks) {
            push(chunk.name, chunk.value);
        }
    }

    const csrfToken = String(
        sessionData?.csrfToken
        || sessionData?.csrf_token
        || sessionData?.['__Host-next-auth.csrf-token']
        || ''
    ).trim();
    if (csrfToken && !seenNames.has('__Host-next-auth.csrf-token')) {
        push('__Host-next-auth.csrf-token', csrfToken);
    }

    const statsigContext = sessionData?.statsigContext || sessionJson?.statsigContext || {};
    const deviceId = String(
        sessionData?.deviceId
        || sessionData?.device_id
        || sessionData?.['oai-did']
        || statsigContext.deviceId
        || ''
    ).trim();
    if (deviceId && !seenNames.has('oai-did')) {
        push('oai-did', deviceId);
        console.log(`[Session] 已注入 oai-did（数据来自 session 设备指纹，长度 ${deviceId.length}）`);
    }

    return specs;
}

async function verifyRealSessionApi(context) {
    const probe = await context.newPage();
    try {
        // Cloudflare 对“首次直接访问 /api/auth/session”会返回空 403；
        // 先打开一次首页（即使首页本身 403）拿 cf_clearance，再查 session 即可 200。
        await probe.goto(CHATGPT_COOKIE_URL, {
            waitUntil: 'domcontentloaded',
            timeout: 60000
        }).catch(() => {});
        await probe.waitForTimeout(2500);
        const response = await probe.goto(`${CHATGPT_COOKIE_URL}/api/auth/session`, {
            waitUntil: 'domcontentloaded',
            timeout: 60000
        });
        const status = response?.status() || 0;
        const headers = response?.headers?.() || {};
        const headerText = Object.keys(headers).map((k) => `${k}:${headers[k]}`).join('\n').toLowerCase();
        const bodyText = await probe.evaluate(() => document.body?.innerText || '').catch(() => '');
        let data = null;
        try {
            data = JSON.parse(bodyText);
        } catch (_) {
            data = null;
        }
        if (data?.accessToken || data?.user?.email || data?.user?.id) {
            return { ok: true, data, status };
        }
        const isCloudflareChallenge = headerText.includes('cf-mitigated: challenge')
            || /just a moment|verify you are human/i.test(bodyText);
        if (isCloudflareChallenge) {
            return {
                ok: false,
                status,
                challenge: true,
                error: `Cloudflare 人机验证拦截（HTTP ${status}，cf-mitigated: challenge）`
                    + '。当前出口/代理 IP 被 ChatGPT 风控，请更换干净的住宅代理后重试'
            };
        }
        const snippet = String(bodyText || '').replace(/\s+/g, ' ').slice(0, 120);
        return {
            ok: false,
            status,
            error: `session-token 未被 ChatGPT 接受（/api/auth/session 无用户信息，HTTP ${status}${snippet ? `，响应: ${snippet}` : ''}）`
        };
    } catch (err) {
        return { ok: false, error: `无法验证 session Cookie: ${err.message}` };
    } finally {
        await probe.close().catch(() => {});
    }
}

function extractAccessTokenFromRaw(raw) {
    const content = String(raw || '').trim();
    if (!content) {
        return '';
    }

    const sessionJson = parseSessionJson(content);
    if (sessionJson) {
        return String(sessionJson.accessToken || sessionJson.access_token || '').trim();
    }

    const jwtMatch = content.match(/eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/);
    if (jwtMatch) {
        return jwtMatch[0];
    }

    return content;
}

/**
 * 统一解析用户输入：优先完整 Session JSON，其次裸 AccessToken
 */
function resolveSessionInput(raw) {
    const content = String(raw || '').trim();
    if (!content) {
        return null;
    }

    const sessionJson = parseSessionJson(content);
    if (sessionJson) {
        const accessToken = extractAccessTokenFromRaw(content);
        if (!accessToken) {
            return null;
        }
        return {
            accessToken,
            sessionJson,
            sessionData: buildSessionPayload(accessToken, sessionJson),
            rawJson: JSON.stringify(sessionJson)
        };
    }

    const accessToken = extractAccessTokenFromRaw(content);
    if (!accessToken) {
        return null;
    }

    const sessionData = buildSessionPayload(accessToken, null);
    return {
        accessToken,
        sessionJson: null,
        sessionData,
        rawJson: JSON.stringify(sessionData)
    };
}

async function assertChatGptLoggedIn(page, label = '页面') {
    const url = page.url();
    if (isLoginRedirectUrl(url)) {
        throw new Error(`Session 未生效：${label} 跳转到 Google/登录页 (${url.slice(0, 80)})`);
    }
    if (await isLoginPageContent(page)) {
        throw new Error(`Session 未生效：${label} 显示 Google 登录界面，请粘贴完整 Session JSON`);
    }
}

function attachLoginRedirectGuard(page) {
    page.on('framenavigated', async (frame) => {
        if (frame !== page.mainFrame() || page.isClosed()) {
            return;
        }
        const url = frame.url();
        const current = page.url();
        if (isCheckoutPageUrl(current) || isCheckoutPageUrl(url)) {
            return;
        }
        if (!isHardLoginRedirectUrl(url)) {
            return;
        }
        console.warn(`[Warn] 检测到登录页跳转，正在拉回 ChatGPT: ${url.slice(0, 80)}`);
        await page.goto(`${CHATGPT_ORIGIN}/`, {
            waitUntil: 'domcontentloaded',
            timeout: 60000
        }).catch(() => {});
    });
}

/**
 * 解析单条 Set-Cookie：取 cookie 名/值，并判断是否为"清除"动作
 * （跨 IP 风控登出的典型表现：空值 / Max-Age=0 / 过期时间置于 1970）。
 */
function parseSetCookieLine(setCookieLine) {
    const line = String(setCookieLine || '').trim();
    if (!line) return null;
    const eq = line.indexOf('=');
    if (eq <= 0) return null;
    const name = line.slice(0, eq).trim();
    const semi = line.indexOf(';', eq);
    const value = (semi === -1 ? line.slice(eq + 1) : line.slice(eq + 1, semi)).trim();
    const attrs = line.toLowerCase();
    const maxAgeNonPositive = /(?:^|;)\s*max-age\s*=\s*(?:0+|-\d+)\s*(?:;|$)/.test(attrs);
    const expiredDate = /expires\s*=\s*[^;]*(?:1970|1969)/.test(attrs);
    const cleared = value === '' || maxAgeNonPositive || expiredDate;
    return { name, value, cleared };
}

/**
 * 把分块 session-token（.0/.1/...）按序拼回完整字符串。
 */
function joinSessionTokenChunks(pinnedMap) {
    const entries = [...pinnedMap.entries()];
    const single = entries.find(([n]) => n === SESSION_TOKEN_BASE);
    if (single && entries.length === 1) {
        return single[1];
    }
    return entries
        .filter(([n]) => n.startsWith(`${SESSION_TOKEN_BASE}.`))
        .sort((a, b) => Number(a[0].split('.').pop()) - Number(b[0].split('.').pop()))
        .map(([, v]) => v)
        .join('');
}

/**
 * 跨 IP 的 session Cookie 守卫。
 *
 * ChatGPT/Cloudflare 在检测到出口 IP 与导出 IP 不一致时，会用 Set-Cookie 清除
 * __Secure-next-auth.session-token，浏览器随即丢弃原本有效的 Cookie 并跳转登录页
 * （表现为"账号退出"）。本守卫在每次 ChatGPT 响应后检查该 Cookie：
 *   - 被清除 → 用注入时的原值重新钉回，阻断登出；
 *   - 被服务端轮换成新的有效值 → 采纳为新基准，并回调持久化以供下次同账号复用。
 *
 * @returns {{active:boolean, getSessionToken:()=>string, stats:()=>object}}
 */
function installSessionCookieGuard(context, cookieSpecs, options = {}) {
    const onRotate = typeof options.onRotate === 'function' ? options.onRotate : null;
    const pinnedSpecs = (cookieSpecs || []).filter((s) => s && isSessionTokenCookieName(s.name) && s.value);
    if (!pinnedSpecs.length) {
        return { active: false, getSessionToken: () => '', stats: () => ({ restoreCount: 0, rotateCount: 0 }) };
    }

    const pinned = new Map(pinnedSpecs.map((s) => [s.name, String(s.value || '')]));
    const specByName = new Map(pinnedSpecs.map((s) => [s.name, s]));
    let restoreCount = 0;
    let rotateCount = 0;
    let busy = false;

    const getSessionToken = () => joinSessionTokenChunks(pinned);

    const isChatGptHost = (url) => {
        try {
            return /(^|\.)(chatgpt\.com|openai\.com)$/i.test(new URL(url).hostname);
        } catch (_) {
            return false;
        }
    };

    const handle = async (response) => {
        if (busy) return;
        let url = '';
        try { url = response.url(); } catch (_) { return; }
        if (!isChatGptHost(url)) return;

        let headers = [];
        try { headers = await response.headersArray(); } catch (_) { return; }
        const sessionSetCookies = headers
            .filter((h) => String(h.name || '').toLowerCase() === 'set-cookie')
            .map((h) => parseSetCookieLine(h.value))
            .filter((c) => c && isSessionTokenCookieName(c.name));
        if (!sessionSetCookies.length) return;

        const cleared = sessionSetCookies.filter((c) => c.cleared);
        const rotated = sessionSetCookies.filter(
            (c) => !c.cleared && pinned.has(c.name) && c.value && c.value !== pinned.get(c.name)
        );
        if (!cleared.length && !rotated.length) return;

        busy = true;
        try {
            if (cleared.length) {
                for (const [name, spec] of specByName) {
                    const good = pinned.get(name);
                    if (!good) continue;
                    const cookie = toPlaywrightCookie({ ...spec, value: good });
                    if (cookie) await addCookieSafe(context, cookie);
                }
                restoreCount += 1;
                console.warn(
                    `⚠️ [Session] 检测到 session-token 被清除（疑似跨 IP 风控登出），已重新钉回原 Cookie（第 ${restoreCount} 次）`
                );
            } else if (rotated.length) {
                for (const c of rotated) {
                    pinned.set(c.name, c.value);
                }
                rotateCount += 1;
                console.log(`[Session] 服务端轮换了 session-token，已采纳新值（第 ${rotateCount} 次）`);
                if (onRotate) {
                    try { await onRotate(getSessionToken()); } catch (_) { /* 持久化失败不阻断 */ }
                }
            }
        } catch (err) {
            console.warn(`[Session] Cookie 守卫处理失败: ${err.message}`);
        } finally {
            busy = false;
        }
    };

    context.on('response', (response) => { handle(response).catch(() => {}); });
    console.log(`[Session] 已启用跨 IP session-token Cookie 守卫（守护 ${pinnedSpecs.length} 个分块）`);

    return { active: true, getSessionToken, stats: () => ({ restoreCount, rotateCount }) };
}

/**
 * 在浏览器上下文安装 Session：拦截 auth API + 注入 fetch 补丁
 * 注意：__Secure-next-auth.session-token 是加密 cookie，不能用 accessToken 冒充
 */
async function installChatGptSession(context, sessionRaw, options = {}) {
    const resolved = resolveSessionInput(sessionRaw);
    if (!resolved?.accessToken) {
        throw new Error('缺少 Session：请粘贴完整 Session JSON（来自 chatgpt.com/api/auth/session）');
    }

    const { accessToken: token, sessionData, sessionJson } = resolved;
    const sessionBody = JSON.stringify(sessionData);
    const cookieSpecs = collectCookieSpecs(sessionData, sessionJson);

    const sessionTokenValue = String(
        process.env.CHATGPT_SESSION_COOKIE
        || process.env.CHATGPT_SESSION_TOKEN
        || sessionData?.sessionToken
        || sessionData?.session_token
        || sessionData?.['__Secure-next-auth.session-token']
        || cookieSpecs.find((c) => c.name === SESSION_TOKEN_BASE)?.value
        || ''
    ).trim();

    if (sessionTokenValue && sessionTokenValue === token) {
        throw new Error(
            'sessionToken 与 accessToken 相同：请从浏览器 DevTools → Application → Cookies 复制 '
            + '__Secure-next-auth.session-token（不是 /api/auth/session JSON 里的 accessToken）'
        );
    }

    const injectResult = await injectChatGptCookies(context, cookieSpecs);

    const sessionTokenFailed = injectResult.failed?.filter((item) => isSessionTokenCookieName(item.name)) || [];

    if (sessionTokenValue && sessionTokenFailed.length) {
        throw new Error(
            `session-token Cookie 注入失败: ${sessionTokenFailed.map((item) => `${item.name}(${item.error})`).join('; ')}`
        );
    }

    if (!injectResult.hasSessionToken) {
        console.warn('[Session] 未提供 session-token Cookie，Checkout 会停留在登录页；'
            + '请导出 __Secure-next-auth.session-token，或粘贴完整 cookies[] / cookieHeader');
    } else {
        console.log(
            `[Session] 已注入 ${injectResult.injected} 个 Cookie（session-token ${sessionTokenValue ? `${sessionTokenValue.length} 字符` : '来自 cookies[]'}）`
        );
    }

    let cookieVerified = false;
    if (injectResult.hasSessionToken) {
        const apiCheck = await verifyRealSessionApi(context);
        if (apiCheck.ok) {
            cookieVerified = true;
            const email = apiCheck.data?.user?.email || '';
            console.log(`[Session] Cookie 校验通过${email ? `: ${email}` : ''}`);
        } else if (apiCheck.error && !/cloudflare|challenge|captcha/i.test(apiCheck.error)) {
            throw new Error(
                `${apiCheck.error}。请确认 Cookie 未过期，并尽量粘贴浏览器全部 chatgpt.com Cookies（cookies[] 或 cookieHeader）`
            );
        } else {
            console.warn(`[Session] Cookie 在线校验跳过: ${apiCheck.error || 'unknown'}`);
        }
    }

    // 跨 IP 风控登出防护：监听 Set-Cookie，清除时重新钉回、轮换时采纳并持久化
    const persistPath = String(options.sessionPersistPath || process.env.CHATGPT_SESSION_PERSIST_PATH || '').trim();
    const onRotate = typeof options.onSessionRotate === 'function'
        ? options.onSessionRotate
        : (persistPath
            ? async (newToken) => {
                try {
                    await fs.promises.writeFile(persistPath, newToken, 'utf8');
                    console.log(`[Session] 已将轮换后的 session-token 持久化到 ${persistPath}`);
                } catch (err) {
                    console.warn(`[Session] session-token 持久化失败: ${err.message}`);
                }
            }
            : null);
    const sessionGuard = installSessionCookieGuard(context, cookieSpecs, { onRotate });

    await context.addInitScript((payload) => {
        const sessionStr = JSON.stringify(payload);
        try {
            window.__CHATGPT_BOOTSTRAP_SESSION__ = payload;
            localStorage.setItem('oai/apps/chat/bootstrap-session', sessionStr);
        } catch (_) { /* ignore */ }

        const originalFetch = window.fetch.bind(window);
        window.fetch = async function patchedFetch(input, init) {
            const url = typeof input === 'string' ? input : (input && input.url) || '';
            if (url.includes('/api/auth/session')) {
                return new Response(sessionStr, {
                    status: 200,
                    headers: { 'Content-Type': 'application/json' }
                });
            }
            if (url.includes('/api/auth/csrf')) {
                return new Response(JSON.stringify({ csrfToken: 'bootstrap-csrf-token' }), {
                    status: 200,
                    headers: { 'Content-Type': 'application/json' }
                });
            }
            return originalFetch(input, init);
        };
    }, sessionData);

    await context.route(/\/api\/auth\/session(\?.*)?$/, async (route) => {
        await route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: sessionBody
        });
    });

    await context.route(/\/api\/auth\/csrf(\?.*)?$/, async (route) => {
        await route.fulfill({
            status: 200,
            contentType: 'application/json',
            body: JSON.stringify({ csrfToken: 'bootstrap-csrf-token' })
        });
    });

    await context.route('**/*', async (route) => {
        const url = route.request().url();
        const resourceType = route.request().resourceType();
        if (shouldBlockLoginNavigation(url, resourceType)) {
            await route.abort();
            return;
        }
        if (/\/api\/auth\/(session|csrf)/.test(url)) {
            await route.continue();
            return;
        }
        if (url.includes('auth.openai.com') || url.includes('chatgpt.com') || url.includes('openai.com') || url.includes('pay.openai.com')) {
            await route.continue({
                headers: {
                    ...route.request().headers(),
                    Authorization: `Bearer ${token}`
                }
            });
            return;
        }
        await route.continue();
    });

    return { sessionData, cookieVerified, sessionGuard };
}

/**
 * @deprecated 使用 installChatGptSession(context, sessionRaw)
 */
function setupChatGptSessionAuth(context, accessToken) {
    return installChatGptSession(context, accessToken);
}

/**
 * 打开 ChatGPT 并验证 Session 在浏览器 UI 中生效
 */

/**
 * 校验 OAuth accessToken 是否仍有效（未被吊销）。
 * 说明：NextAuth 的 /api/auth/session 在 token 被吊销后仍可能返回 200 JSON，
 * 必须用 /backend-api/me 才能真正验证，否则会一直卡到 Checkout 超时。
 * @param {import('playwright').APIRequestContext} requestContext
 * @param {string} accessToken
 * @returns {Promise<{ok: boolean, revoked?: boolean, status?: number, error?: string}>}
 */
async function assertAccessTokenActive(requestContext, accessToken) {
    const token = String(accessToken || '').trim();
    if (!token) {
        return { ok: false, error: 'Session 缺少 accessToken，请重新导出完整 Session JSON' };
    }
    try {
        const resp = await requestContext.get(`${CHATGPT_ORIGIN}/backend-api/me`, {
            headers: { Authorization: `Bearer ${token}`, Accept: 'application/json' },
            timeout: 20000
        });
        const status = resp.status();
        if (status === 200) {
            return { ok: true };
        }
        const text = await resp.text().catch(() => '');
        if (/token_revoked|invalidated oauth token/i.test(text)) {
            return {
                ok: false,
                revoked: true,
                status,
                error: 'Session 的 OAuth accessToken 已被吊销（token_revoked）：请在浏览器重新登录该 ChatGPT 账号，'
                    + '重新导出完整 Session JSON（新的 session-token Cookie），并尽量在同一代理/IP 下导出与使用'
            };
        }
        return { ok: false, status, error: `accessToken 校验失败 HTTP ${status}: ${text.slice(0, 150)}` };
    } catch (error) {
        return { ok: false, error: `accessToken 校验请求失败: ${error.message}` };
    }
}

async function bootstrapChatGptSession(page, sessionRaw, options = {}) {
    const resolved = resolveSessionInput(sessionRaw);
    if (!resolved?.accessToken) {
        throw new Error('缺少 Session：请粘贴完整 Session JSON');
    }

    const sessionData = options.sessionData || resolved.sessionData;
    const email = sessionData?.user?.email || extractProfileFromToken(resolved.accessToken).email || '';

    if (!email && !sessionData?.user?.id) {
        throw new Error('Session 无效：JSON 中缺少 user 信息，请从 chatgpt.com/api/auth/session 复制完整内容');
    }

    console.log('🔐 [步骤] 正在使用 Session 登录 ChatGPT...');
    attachLoginRedirectGuard(page);

    await page.goto(`${CHATGPT_ORIGIN}/`, {
        waitUntil: 'domcontentloaded',
        timeout: 90000
    });
    await page.waitForTimeout(2500);

    // 深度校验：/api/auth/session 在 token 被吊销后仍可能返回 200，
    // 必须尽早用 /backend-api/me 验证，避免后续卡在定价页/Checkout 超时
    let liveToken = resolved.accessToken;
    try {
        const sessResp = await page.context().request.get(`${CHATGPT_ORIGIN}/api/auth/session`, { timeout: 20000 });
        if (sessResp.ok()) {
            const sessData = await sessResp.json().catch(() => null);
            if (sessData && sessData.accessToken) {
                liveToken = sessData.accessToken;
            }
        }
    } catch (_) { /* 使用原始 token 继续校验 */ }

    const tokenCheck = await assertAccessTokenActive(page.context().request, liveToken);
    if (!tokenCheck.ok) {
        // accessToken 校验失败（如异地触发的间歇性 token_revoked）不中止：
        // checkout 走 UI + session-token cookie + 前端 Sentinel，不依赖 OAuth accessToken。
        console.warn(`[Session] accessToken 校验未通过，继续走 UI 流程（checkout 不依赖 accessToken）：${String(tokenCheck.error || '').slice(0, 80)}`);
    }

    const { clearHumanVerification } = require('./human-verification');
    const captchaResult = await clearHumanVerification(page, {
        phase: 'session-bootstrap',
        maxWaitMs: Number(process.env.CAPTCHA_CLEAR_TIMEOUT_MS || 180000),
        maxBypassRounds: 6
    });
    if (!captchaResult.cleared) {
        throw new Error('Cloudflare 人机验证未能通过，请换住宅代理 IP 或 HEADFUL=1 人工勾选');
    }

    if (await hasVisibleLoginChrome(page)) {
        await page.reload({ waitUntil: 'domcontentloaded', timeout: 90000 }).catch(() => {});
        await page.waitForTimeout(2000);
    }

    await assertChatGptLoggedIn(page, '首页');

    const cookieVerified = options.cookieVerified === true;
    const uiReady = await waitForLoggedInChatUi(page, cookieVerified ? 12000 : 8000);
    const apiReady = cookieVerified ? await hasLoggedInSessionApi(page) : false;

    if (await hasVisibleLoginChrome(page)) {
        throw new Error(buildSessionNotLoggedInError('ChatGPT 首页'));
    }
    if (!uiReady && !apiReady) {
        throw new Error(buildSessionNotLoggedInError('ChatGPT 首页'));
    }
    if (!uiReady && apiReady) {
        console.log('[Session] 首页 UI 探针未命中，但 /api/auth/session 已确认登录，继续流程');
    }

    const resolvedEmail = sessionData?.user?.email || email || extractProfileFromToken(resolved.accessToken).email;
    console.log(`✅ [步骤] Session 登录成功: ${resolvedEmail}`);
    return {
        email: resolvedEmail,
        session: sessionData,
        hasSessionCookie: Boolean(
            process.env.CHATGPT_SESSION_COOKIE
            || process.env.CHATGPT_SESSION_TOKEN
            || sessionData.sessionToken
            || sessionData.session_token
            || sessionData['__Secure-next-auth.session-token']
            || (Array.isArray(sessionData.cookies) && sessionData.cookies.length > 0)
            || sessionData.cookieHeader
            || sessionData.cookie_header
        )
    };
}

function extractSessionPreview(raw) {
    const resolved = resolveSessionInput(raw);
    if (!resolved) {
        return String(raw || '').slice(0, 32);
    }
    const email = resolved.sessionData?.user?.email;
    if (email) {
        return email;
    }
    return `${resolved.accessToken.slice(0, 12)}...`;
}

function extractEmailFromSession(raw) {
    const resolved = resolveSessionInput(raw);
    if (!resolved) {
        return '';
    }
    return String(
        resolved.sessionData?.user?.email
        || resolved.sessionJson?.user?.email
        || extractProfileFromToken(resolved.accessToken).email
        || ''
    ).trim();
}

module.exports = {
    CHATGPT_ORIGIN,
    installChatGptSession,
    installSessionCookieGuard,
    setupChatGptSessionAuth,
    bootstrapChatGptSession,
    assertAccessTokenActive,
    assertChatGptLoggedIn,
    parseSessionJson,
    resolveSessionInput,
    extractAccessTokenFromRaw,
    extractProfileFromToken,
    extractSessionPreview,
    extractEmailFromSession,
    buildSessionPayload,
    isLoginRedirectUrl,
    isHardLoginRedirectUrl,
    isCheckoutPageUrl,
    isLoginPageContent
};
