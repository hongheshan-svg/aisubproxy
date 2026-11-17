'use strict';

const fs = require('fs');
const { resolveClientEnvironment, buildSessionApiHeaders } = require('./session-environment');
const { prepareBrowserPage } = require('./browser-environment');

const CHATGPT_ORIGIN = 'https://chatgpt.com';
const {
    isLoginRedirectUrl,
    isHardLoginRedirectUrl,
    isCheckoutPageUrl,
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

    const exp = Number(decodeJwtPayload(token)?.exp || 0);
    const expires = base.expires || (exp > 0 ? new Date(exp * 1000).toISOString() : null);

    return {
        ...base,
        user,
        expires,
        accessToken: base.accessToken || base.access_token || token,
        authProvider: base.authProvider || 'openai'
    };
}

function parseSessionJson(raw) {
    if (raw && typeof raw === 'object' && !Array.isArray(raw)) return raw;
    const content = raw && typeof raw === 'object' ? JSON.stringify(raw) : String(raw || '').trim();
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
    return !value || /(^|\.)chatgpt\.com$/.test(value);
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
    const httpOnly = name === 'oai-did' ? false : spec.httpOnly !== false;

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
        sessionData?.sessionToken
        || sessionData?.session_token
        || sessionData?.['__Secure-next-auth.session-token']
        || process.env.CHATGPT_SESSION_COOKIE
        || process.env.CHATGPT_SESSION_TOKEN
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

    const deviceId = resolveClientEnvironment(sessionJson || sessionData)?.deviceId || '';
    if (deviceId && !seenNames.has('oai-did')) {
        push('oai-did', deviceId);
        console.log(`[Session] 已注入 oai-did（数据来自 session 设备指纹，长度 ${deviceId.length}）`);
    }

    return specs;
}

async function verifyRealSessionApi(context) {
    const probe = await prepareBrowserPage(await context.newPage());
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
    const content = raw && typeof raw === 'object' ? JSON.stringify(raw) : String(raw || '').trim();
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
    const content = raw && typeof raw === 'object' ? JSON.stringify(raw) : String(raw || '').trim();
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

/** 监听服务端轮换；服务端清除会话时保留清除结果，不能恢复已撤销的凭证。 */
function installSessionCookieGuard(context, cookieSpecs, options = {}) {
    let current = joinSessionTokenChunks(new Map((cookieSpecs || [])
        .filter(c => isSessionTokenCookieName(c.name)).map(c => [c.name, c.value])));
    let rotateCount = 0;
    let cleared = false;
    let pending = Promise.resolve();
    if (current) context.on('response', response => {
        pending = pending.then(async () => {
            let url;
            try { url = new URL(response.url()); } catch (_) { return; }
            if (url.origin !== CHATGPT_ORIGIN) return;
            const headers = await response.headersArray();
            const changes = headers.filter(h => h.name.toLowerCase() === 'set-cookie')
                .map(h => parseSetCookieLine(h.value)).filter(c => c && isSessionTokenCookieName(c.name));
            if (!changes.length) return;
            // Cookie 轮换可能同时删除旧分块、写入新分块；以最终浏览器 Cookie 集为准。
            const stored = await context.cookies(CHATGPT_ORIGIN);
            const next = joinSessionTokenChunks(new Map(stored
                .filter(c => isSessionTokenCookieName(c.name)).map(c => [c.name, c.value])));
            if (!next) { current = ''; cleared = true; return; }
            if (next !== current) {
                current = next; cleared = false; rotateCount += 1;
                if (options.onRotate) await options.onRotate(next);
            }
        }).catch(() => {});
    });
    return { active: Boolean(current), getSessionToken: () => current,
        stats: () => ({ restoreCount: 0, rotateCount, cleared }) };
}

// 只采纳真实服务端返回的新凭证，并防止误把另一个账号的 Cookie 用于当前任务。
function adoptVerifiedSession(sessionData, live) {
    if (!live || typeof live.accessToken !== 'string' || !live.accessToken.trim()) {
        throw new Error('ChatGPT 未返回有效登录会话，请重新导出完整 Session');
    }
    for (const key of ['id', 'email']) {
        const expected = String(sessionData.user?.[key] || '').toLowerCase();
        const actual = String(live.user?.[key] || '').toLowerCase();
        if (expected && actual && expected !== actual) {
            throw new Error('Session 的 Cookie 与账号身份不一致，请重新导出同一账号的完整 Session');
        }
    }
    sessionData.accessToken = live.accessToken;
    if (live.user) sessionData.user = live.user;
    if (live.expires) sessionData.expires = live.expires;
    return sessionData;
}

/**
 * 在浏览器上下文安装 Session：真实 Cookie + 服务端会话校验
 * 注意：__Secure-next-auth.session-token 是加密 cookie，不能用 accessToken 冒充
 */
async function installChatGptSession(context, sessionRaw, options = {}) {
    const resolved = resolveSessionInput(sessionRaw);
    if (!resolved?.accessToken) {
        throw new Error('缺少 Session：请粘贴完整 Session JSON（来自 chatgpt.com/api/auth/session）');
    }

    const { accessToken: token, sessionData, sessionJson } = resolved;
    const cookieSpecs = collectCookieSpecs(sessionData, sessionJson);

    const sessionTokenValue = joinSessionTokenChunks(new Map(cookieSpecs
        .filter(c => isSessionTokenCookieName(c.name)).map(c => [c.name, c.value])));

    if (sessionTokenValue && sessionTokenValue === token) {
        throw new Error(
            'sessionToken 与 accessToken 相同：请从浏览器 DevTools → Application → Cookies 复制 '
            + '__Secure-next-auth.session-token（不是 /api/auth/session JSON 里的 accessToken）'
        );
    }

    const injectResult = await injectChatGptCookies(context, cookieSpecs);

    const sessionTokenFailed = injectResult.failed?.filter((item) => isSessionTokenCookieName(item.name)) || [];

    if (sessionTokenFailed.length) {
        throw new Error(
            `session-token Cookie 注入失败: ${sessionTokenFailed.map((item) => `${item.name}(${item.error})`).join('; ')}`
        );
    }

    if (!injectResult.hasSessionToken) {
        throw new Error('缺少登录 Cookie：请提供包含 sessionToken 的完整 Session JSON，或附上 chatgpt.com 的 cookies[] / cookieHeader；只有 accessToken 无法恢复网页登录');
    } else {
        console.log(
            `[Session] 已注入 ${injectResult.injected} 个 Cookie（session-token ${sessionTokenValue ? `${sessionTokenValue.length} 字符` : '来自 cookies[]'}）`
        );
    }

    let cookieVerified = false;
    if (injectResult.hasSessionToken) {
        const apiCheck = await verifyRealSessionApi(context);
        if (apiCheck.ok) {
            adoptVerifiedSession(sessionData, apiCheck.data);
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

    // 监听正常轮换；服务端撤销或登出不能通过重新注入旧 Cookie 修复。
    const persistPath = String(options.sessionPersistPath || process.env.CHATGPT_SESSION_PERSIST_PATH || '').trim();
    const onRotate = typeof options.onSessionRotate === 'function'
        ? options.onSessionRotate
        : (persistPath
            ? async (newToken) => {
                try {
                    await fs.promises.writeFile(persistPath, newToken, { encoding: 'utf8', mode: 0o600 });
                    console.log(`[Session] 已将轮换后的 session-token 持久化到 ${persistPath}`);
                } catch (err) {
                    console.warn(`[Session] session-token 持久化失败: ${err.message}`);
                }
            }
            : null);
    const sessionGuard = installSessionCookieGuard(context, await context.cookies(CHATGPT_ORIGIN), { onRotate });

    const identityHeaders = buildSessionApiHeaders(options.clientEnv || resolveClientEnvironment(sessionData));
    await context.route('https://chatgpt.com/backend-api/**', async route => {
        const request = route.request();
        const url = new URL(request.url());
        if (url.origin !== CHATGPT_ORIGIN || !url.pathname.startsWith('/backend-api/')) {
            return route.continue();
        }
        // 不覆盖前端刚刷新的 Authorization，也不向登录/付款/第三方域名发送凭证。
        const headers = { ...identityHeaders, ...request.headers() };
        if (!headers.authorization && !headers.Authorization) headers.authorization = `Bearer ${sessionData.accessToken}`;
        await route.continue({ headers });
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
async function assertAccessTokenActive(requestContext, accessToken, clientEnv = null) {
    const token = String(accessToken || '').trim();
    if (!token) {
        return { ok: false, error: 'Session 缺少 accessToken，请重新导出完整 Session JSON' };
    }
    try {
        const resp = await requestContext.get(`${CHATGPT_ORIGIN}/backend-api/me`, {
            headers: { ...buildSessionApiHeaders(clientEnv), Authorization: `Bearer ${token}`, Accept: 'application/json' },
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

    await page.goto(`${CHATGPT_ORIGIN}/`, {
        waitUntil: 'domcontentloaded',
        timeout: 90000
    });
    await page.waitForTimeout(2500);

    const { clearHumanVerification } = require('./human-verification');
    const captchaResult = await clearHumanVerification(page, {
        phase: 'session-bootstrap',
        maxWaitMs: Number(process.env.CAPTCHA_CLEAR_TIMEOUT_MS || 180000),
        maxBypassRounds: 6
    });
    if (!captchaResult.cleared) {
        throw new Error('Cloudflare 人机验证未能通过，请换住宅代理 IP 或 HEADFUL=1 人工勾选');
    }

    // 使用未拦截的会话接口，真实刷新 token 后再校验和创建 Checkout。
    const response = await page.context().request.get(`${CHATGPT_ORIGIN}/api/auth/session`, { timeout: 20000 });
    const live = response.ok() ? await response.json().catch(() => null) : null;
    adoptVerifiedSession(sessionData, live);
    const tokenCheck = await assertAccessTokenActive(page.context().request, sessionData.accessToken, options.clientEnv);
    if (!tokenCheck.ok) {
        if (tokenCheck.revoked || tokenCheck.status === 401) throw new Error(tokenCheck.error);
        console.warn(`[Session] 登录校验暂不可用（HTTP ${tokenCheck.status || 'network'}），继续以真实 Cookie 和页面状态验证`);
    }

    if (await hasVisibleLoginChrome(page)) {
        await page.reload({ waitUntil: 'domcontentloaded', timeout: 90000 }).catch(() => {});
        await page.waitForTimeout(2000);
    }

    await assertChatGptLoggedIn(page, '首页');

    const cookieVerified = Boolean(live?.accessToken);
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
    adoptVerifiedSession,
    collectCookieSpecs,
    toPlaywrightCookie,
    expandSessionTokenCookies,
    isLoginRedirectUrl,
    isHardLoginRedirectUrl,
    isCheckoutPageUrl,
    isLoginPageContent
};
