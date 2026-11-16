'use strict';

/**
 * Session 环境对齐
 *
 * 用户在自己电脑登录 ChatGPT 后导出的 Session JSON 里，statsigContext 记录了
 * 真实浏览器的 UA、locale、IP、country/region 和 deviceId。登录页与支付页
 * 必须使用同一套环境，否则 ChatGPT 会把会话判定为“异地/换设备”，触发
 * token_revoked 或 checkout 的 session expired。
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
    const raw = String(sessionRaw || '').trim();
    if (!raw.startsWith('{')) {
        return null;
    }
    try {
        const data = JSON.parse(raw);
        const env = (data && (data.__env || data.clientEnv || data.environment)) || {};
        const statsig = (data && data.statsigContext) || {};
        const account = (data && data.account) || {};
        const user = (data && data.user) || {};
        const merged = {
            userAgent: String(env.userAgent || statsig.userAgent || '').trim(),
            locale: String(env.locale || statsig.locale || '').trim(),
            languages: Array.isArray(env.languages) && env.languages.length ? env.languages : [],
            timezone: String(env.timezone || '').trim(),
            ip: String(env.ip || statsig.ip || '').trim(),
            country: String(env.country || statsig.country || '').trim().toUpperCase(),
            region: String(env.region || statsig.region || '').trim(),
            regionCode: String(env.regionCode || statsig.regionCode || '').trim().toUpperCase(),
            deviceId: String(env.deviceId || statsig.deviceId || data.deviceId || data.device_id || '').trim(),
            accountId: String(env.accountId || account.id || data.accountId || '').trim(),
            email: String(env.email || user.email || '').trim(),
            planType: String(account.planType || '').trim(),
            screen: env.screen || null,
            window: env.window || null,
            devicePixelRatio: env.devicePixelRatio,
            hardwareConcurrency: env.hardwareConcurrency,
            deviceMemory: env.deviceMemory,
            maxTouchPoints: env.maxTouchPoints
        };
        if (!merged.userAgent && !merged.locale && !merged.ip && !merged.deviceId) {
            return null;
        }
        return merged;
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
    const n = Number(value);
    if (!Number.isFinite(n)) {
        return fallback;
    }
    return Math.min(max, Math.max(min, n));
}

function detectUaInfo(userAgent) {
    const ua = String(userAgent || '');
    if (/iPhone|iPad/i.test(ua)) {
        return { isMobile: true, uaPlatform: 'iOS', platformWin: 'iPhone', platformVersion: '17.0.0', arch: 'arm' };
    }
    if (/Android/i.test(ua)) {
        return { isMobile: true, uaPlatform: 'Android', platformWin: 'Linux armv8l', platformVersion: '13.0.0', arch: 'arm' };
    }
    if (/Macintosh|Mac OS X/i.test(ua)) {
        return { isMobile: false, uaPlatform: 'macOS', platformWin: 'MacIntel', platformVersion: '14.0.0', arch: 'arm' };
    }
    if (/Linux/i.test(ua)) {
        return { isMobile: false, uaPlatform: 'Linux', platformWin: 'Linux x86_64', platformVersion: '6.0.0', arch: 'x86' };
    }
    return { isMobile: false, uaPlatform: 'Windows', platformWin: 'Win32', platformVersion: '15.0.0', arch: 'x86' };
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
    headers.origin = 'https://chatgpt.com';
    headers.referer = 'https://chatgpt.com/';
    return headers;
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
