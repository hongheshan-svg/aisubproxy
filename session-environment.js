'use strict';

/**
 * Session 环境对齐
 *
 * 用户在自己电脑登录 ChatGPT 后导出的 Session JSON 里，statsigContext 记录了
 * 浏览器的 UA、locale、IP、country/region 和 deviceId。保持请求与页面一致，
 * 但这些信息不足以还原浏览器内核、硬件或服务端的会话状态。
 *
 * 本模块只做“解析 + 构造”，不发起网络请求：
 * - resolveClientEnvironment: 解析 Session 里的核心环境字段
 * - timezoneFromClientEnv: 用国家/州推导 IANA 时区（statsig 不含时区）
 * - detectUaInfo / buildUaBrands: 由 UA 推导平台与 sec-ch-ua
 * - buildSessionApiHeaders: 给脚本直连的 API 请求补 chatgpt-account-id /
 *   oai-device-id / oai-language 等前端同款请求头
 */

// 用户 session 的 statsigContext 不含 IANA 时区，用国家/州映射到最接近的时区
const COUNTRY_TIMEZONE = {
    US: 'America/New_York', PH: 'Asia/Manila', SG: 'Asia/Singapore', MY: 'Asia/Kuala_Lumpur',
    CN: 'Asia/Shanghai', TW: 'Asia/Taipei', HK: 'Asia/Hong_Kong', JP: 'Asia/Tokyo', KR: 'Asia/Seoul',
    GB: 'Europe/London', CA: 'America/Toronto', AU: 'Australia/Sydney', DE: 'Europe/Berlin',
    FR: 'Europe/Paris', NL: 'Europe/Amsterdam', IN: 'Asia/Kolkata', ID: 'Asia/Jakarta',
    TH: 'Asia/Bangkok', VN: 'Asia/Ho_Chi_Minh', BR: 'America/Sao_Paulo', MX: 'America/Mexico_City',
    RU: 'Europe/Moscow', AE: 'Asia/Dubai', SA: 'Asia/Riyadh', TR: 'Europe/Istanbul'
};

const US_STATE_TIMEZONE = {
    CA: 'America/Los_Angeles', OR: 'America/Los_Angeles', WA: 'America/Los_Angeles', NV: 'America/Los_Angeles',
    AZ: 'America/Phoenix', CO: 'America/Denver', UT: 'America/Denver', NM: 'America/Denver', MT: 'America/Denver',
    TX: 'America/Chicago', IL: 'America/Chicago', MN: 'America/Chicago', WI: 'America/Chicago',
    MO: 'America/Chicago', IA: 'America/Chicago', KS: 'America/Chicago', NE: 'America/Chicago',
    NY: 'America/New_York', FL: 'America/New_York', MA: 'America/New_York', PA: 'America/New_York',
    GA: 'America/New_York', NC: 'America/New_York', OH: 'America/New_York', MI: 'America/Detroit',
    VA: 'America/New_York', NJ: 'America/New_York', MD: 'America/New_York', CT: 'America/New_York',
    HI: 'Pacific/Honolulu', AK: 'America/Anchorage'
};

function maskId(value, keep = 6) {
    const text = String(value || '').trim();
    if (!text) return '';
    if (text.length <= keep * 2) return `${text.slice(0, keep)}…`;
    return `${text.slice(0, keep)}…${text.slice(-4)}`;
}

/**
 * 从 Session JSON 中解析出登录/支付要使用的环境。
 * 优先取用户采集的 __env，缺失时回退 statsigContext（ChatGPT 自带）。
 */
function resolveClientEnvironment(sessionRaw) {
    try {
        const raw = String(sessionRaw || '').trim();
        const data = typeof sessionRaw === 'object' && sessionRaw !== null ? sessionRaw
            : raw.startsWith('{') ? JSON.parse(raw) : { accessToken: raw };
        const record = value => value && typeof value === 'object' && !Array.isArray(value) ? value : {};
        if (data !== record(data)) return null;
        // 按字段合并，空对象/空值不能遮盖另一来源已提供的信息。
        const sources = [data.__env, data.clientEnv, data.clientEnvironment, data.environment, data.statsigContext]
            .map(record).flatMap(env => [env, record(env.navigator)]);
        const text = value => typeof value === 'string' && !/[\r\n\0]/.test(value) ? value.trim() : '';
        const choose = (keys, accept = value => text(value) !== '') => {
            for (const env of sources) for (const key of keys) if (accept(env[key])) return env[key];
            return undefined;
        };
        const number = value => (typeof value === 'number' || (typeof value === 'string' && value.trim() !== ''))
            && Number.isFinite(Number(value)) && Number(value) >= 0;
        const objectFields = (names, keys) => {
            const result = {};
            for (const key of keys) {
                for (const env of sources) {
                    const value = names.map(name => record(env[name])[key]).find(value => number(value) && Number(value) > 0);
                    if (value !== undefined) { result[key] = Number(value); break; }
                }
            }
            return Object.keys(result).length ? result : null;
        };
        const locale = choose(['locale', 'language'], value => {
            try { return Boolean(text(value) && Intl.getCanonicalLocales(text(value))[0]); } catch (_) { return false; }
        });
        const timezone = choose(['timezone', 'timezoneId', 'timeZone'], value => {
            try { if (!text(value)) return false; Intl.DateTimeFormat('en-US', { timeZone: value }); return true; } catch (_) { return false; }
        });
        const account = record(data.account), user = record(data.user);
        let auth = {};
        try {
            const part = String(data.accessToken || data.access_token || '').split('.')[1];
            auth = JSON.parse(Buffer.from(part, 'base64url').toString('utf8'))['https://api.openai.com/auth'] || {};
        } catch (_) { /* JWT 仅用于补齐字段；授权有效性由服务端校验 */ }
        const cookieDevice = Array.isArray(data.cookies) ? data.cookies.find(c => c?.name === 'oai-did'
            && text(c.value) && (!c.domain || /^\.?chatgpt\.com$/i.test(c.domain)))?.value : '';
        const headerDevice = String(data.cookieHeader || data.cookie_header || '').match(/(?:^|;)\s*oai-did=([^;]+)/)?.[1];
        const screen = objectFields(['screen'], ['width', 'height']);
        const window = objectFields(['window'], ['innerWidth', 'innerHeight']);
        const viewport = objectFields(['viewport'], ['width', 'height']);
        const dimensions = window || viewport ? {
            innerWidth: window?.innerWidth ?? viewport?.width,
            innerHeight: window?.innerHeight ?? viewport?.height
        } : null;
        const hints = {};
        for (const env of [...sources].reverse()) Object.assign(hints, record(env.userAgentData));
        const merged = {
            userAgent: text(choose(['userAgent', 'user_agent'], value => /^Mozilla\/5\.0 [^\r\n]{1,500}$/.test(text(value)))),
            locale: locale ? Intl.getCanonicalLocales(locale)[0] : '',
            languages: choose(['languages'], value => Array.isArray(value) && value.length > 0 && value.every(v => text(v))) || [],
            timezone: text(timezone),
            ip: text(choose(['ip'])),
            country: text(choose(['country'], value => /^[a-z]{2}$/i.test(text(value)))).toUpperCase(),
            region: text(choose(['region'])),
            regionCode: text(choose(['regionCode'])).toUpperCase(),
            deviceId: text(cookieDevice) || text(headerDevice) || text(choose(['deviceId', 'device_id']))
                || text(data.deviceId) || text(data.device_id) || text(data['oai-did']),
            accountId: text(account.id) || text(choose(['accountId'])) || text(data.accountId) || text(auth.chatgpt_account_id),
            email: text(user.email) || text(choose(['email'])),
            planType: text(account.planType),
            screen,
            window: dimensions,
            devicePixelRatio: choose(['devicePixelRatio'], value => number(value) && Number(value) > 0),
            hardwareConcurrency: choose(['hardwareConcurrency'], value => number(value) && Number(value) > 0),
            deviceMemory: choose(['deviceMemory'], value => number(value) && Number(value) > 0),
            maxTouchPoints: choose(['maxTouchPoints'], number),
            userAgentData: Object.keys(hints).length ? hints : null,
            platform: text(choose(['platform']))
        };
        return Object.values(merged).some(value => value !== undefined && value !== null && value !== ''
            && (!Array.isArray(value) || value.length)) ? merged : null;
    } catch (_) {
        return null;
    }
}

function timezoneFromClientEnv(env) {
    if (!env) {
        return '';
    }
    const regionCode = String(env.regionCode || '').toUpperCase();
    const country = String(env.country || '').toUpperCase();
    if (country === 'US' && US_STATE_TIMEZONE[regionCode]) {
        return US_STATE_TIMEZONE[regionCode];
    }
    if (COUNTRY_TIMEZONE[country]) {
        return COUNTRY_TIMEZONE[country];
    }
    return '';
}

function clampNum(value, min, max, fallback) {
    if (value === null || value === undefined || value === '') return fallback;
    const n = Number(value);
    if (!Number.isFinite(n)) {
        return fallback;
    }
    return Math.min(max, Math.max(min, n));
}

function detectUaInfo(userAgent) {
    const ua = String(userAgent || '');
    if (/iPhone|iPad/i.test(ua)) {
        return { isMobile: true, uaPlatform: 'iOS', platformWin: /iPad/i.test(ua) ? 'iPad' : 'iPhone' };
    }
    if (/Android/i.test(ua)) {
        return { isMobile: true, uaPlatform: 'Android', platformWin: 'Linux armv8l' };
    }
    if (/Macintosh|Mac OS X/i.test(ua)) {
        return { isMobile: false, uaPlatform: 'macOS', platformWin: 'MacIntel' };
    }
    if (/Linux/i.test(ua)) {
        return { isMobile: false, uaPlatform: 'Linux', platformWin: 'Linux x86_64' };
    }
    return { isMobile: false, uaPlatform: 'Windows', platformWin: 'Win32' };
}

function buildUaBrands(userAgent, chromeMajor) {
    const major = String(chromeMajor || 147);
    if (/Edg\//.test(userAgent)) {
        return [
            { brand: 'Not)A;Brand', version: '8' },
            { brand: 'Chromium', version: major },
            { brand: 'Microsoft Edge', version: major }
        ];
    }
    return [
        { brand: 'Not)A;Brand', version: '8' },
        { brand: 'Chromium', version: major },
        { brand: 'Google Chrome', version: major }
    ];
}

/**
 * 脚本直连 backend-api 时补上前端同款的身份头。
 * 页面内的请求由 ChatGPT 前端自己生成，不需要这些头。
 */
function buildSessionApiHeaders(env) {
    const headers = {};
    if (!env) {
        return headers;
    }
    if (env.accountId) {
        headers['chatgpt-account-id'] = env.accountId;
    }
    if (env.deviceId) {
        headers['oai-device-id'] = env.deviceId;
    }
    if (env.locale) {
        headers['oai-language'] = env.locale;
    }
    if (env.userAgent) headers['user-agent'] = env.userAgent;
    if (env.userAgentData?.brands) {
        headers['sec-ch-ua'] = env.userAgentData.brands.map(b => `"${b.brand}";v="${b.version}"`).join(', ');
        headers['sec-ch-ua-mobile'] = env.userAgentData.mobile ? '?1' : '?0';
        headers['sec-ch-ua-platform'] = `"${env.userAgentData.platform}"`;
    }
    headers.origin = 'https://chatgpt.com';
    headers.referer = 'https://chatgpt.com/';
    return Object.fromEntries(Object.entries(headers).filter(([, value]) => typeof value === 'string'
        && value.length < 2048 && !/[\r\n\0]/.test(value)));
}

module.exports = {
    COUNTRY_TIMEZONE,
    US_STATE_TIMEZONE,
    maskId,
    resolveClientEnvironment,
    timezoneFromClientEnv,
    clampNum,
    detectUaInfo,
    buildUaBrands,
    buildSessionApiHeaders
};
