'use strict';

const { getRegionConfig } = require('./region-config');

/**
 * 出口 IP 地理查询：让浏览器的地理指纹（时区 / 语言）与"系统设置的代理出口 IP"
 * 保持一致，避免出现"IP 在 A 国、时区/语言却在 B 国"的矛盾（这是 ChatGPT/Cloudflare
 * 触发风控登出 / token 吊销的典型信号）。
 *
 * 隐私：仅查询代理自己的出口 IP（非用户），不会把用户 session 里的 IP 或任何
 * 个人信息发往第三方。
 */

function maskIp(value) {
    return String(value || '').replace(/^(\d+)\.(\d+)\.(\d+)\.(\d+)$/, '***.***.$3.$4');
}

/**
 * 查询某 IP 的地理信息；ip 省略时查询调用方（即当前代理）的出口 IP。
 * 使用 ip-api.com 免费接口（HTTP、无需 key、45 req/min），直接返回 IANA 时区。
 * @param {import('playwright').APIRequestContext} requestContext 已挂代理的请求上下文
 * @param {string} [ip] 为空则查出口 IP 本身
 * @returns {Promise<{ip:string, countryCode:string, country:string, regionName:string, timezone:string}|null>}
 */
async function lookupIpGeo(requestContext, ip, { timeout = 10000 } = {}) {
    if (!requestContext) {
        return null;
    }
    const target = String(ip || '').trim();
    const fields = 'status,message,query,country,countryCode,regionName,timezone';
    const url = `http://ip-api.com/json/${target ? encodeURIComponent(target) : ''}?fields=${fields}`;
    try {
        const resp = await requestContext.get(url, { timeout });
        if (!resp.ok()) {
            return null;
        }
        const data = await resp.json().catch(() => null);
        if (!data || data.status !== 'success') {
            return null;
        }
        const countryCode = String(data.countryCode || '').trim().toUpperCase();
        if (!countryCode) {
            return null;
        }
        return {
            ip: String(data.query || target || '').trim(),
            countryCode,
            country: String(data.country || '').trim(),
            regionName: String(data.regionName || '').trim(),
            timezone: String(data.timezone || '').trim()
        };
    } catch (_) {
        return null;
    }
}

/**
 * 由国家码推导浏览器 locale：优先项目地区配置（en-PH 等），否则回退 en-<CC>。
 */
function localeForCountry(countryCode) {
    const cc = String(countryCode || '').trim().toUpperCase();
    if (!cc) {
        return '';
    }
    const cfg = getRegionConfig(cc);
    if (cfg && cfg.locale) {
        return cfg.locale;
    }
    return `en-${cc}`;
}

module.exports = {
    lookupIpGeo,
    localeForCountry,
    maskIp
};
