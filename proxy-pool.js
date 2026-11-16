'use strict';

const crypto = require('crypto');
const axios = require('axios');
const { ProxyAgent } = require('proxy-agent');

const PROBE_URLS = [
    'https://api.ipify.org/?format=text',
    'https://ifconfig.me/ip'
];

function substituteProxySession(rawProxy) {
    if (!rawProxy) {
        return rawProxy;
    }
    if (!/\{session\}/i.test(rawProxy)) {
        return rawProxy;
    }
    const sid = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`;
    return rawProxy.replace(/\{session\}/gi, sid);
}

function maskProxyUrl(url) {
    return String(url || '').replace(/\/\/([^:@/]+):([^@/]+)@/i, '//$1:***@');
}

function hashProxyUrl(url) {
    return crypto.createHash('sha256').update(String(url || '').trim()).digest('hex');
}

function parseProxyMeta(url) {
    try {
        const parsed = new URL(String(url || '').trim());
        return {
            protocol: String(parsed.protocol || '').replace(':', '').toLowerCase(),
            host: parsed.hostname || ''
        };
    } catch (_) {
        return { protocol: '', host: '' };
    }
}

function normalizeProxyLines(input) {
    const lines = Array.isArray(input)
        ? input
        : String(input || '').split(/\r?\n/);
    return [...new Set(lines.map((line) => String(line || '').trim()).filter(Boolean))];
}

// 通过同一代理查询出口 IP 的国家码（ISO alpha-2，大写），用于给代理打地区标签。
// 失败返回 ''（未知），不影响代理检测本身。
async function lookupProxyRegion(agent, timeout) {
    try {
        const resp = await axios.get('http://ip-api.com/json/?fields=status,countryCode', {
            httpsAgent: agent,
            httpAgent: agent,
            proxy: false,
            timeout: Math.min(Math.max(3000, Number(timeout) || 8000), 8000),
            validateStatus: () => true
        });
        if (resp.status === 200 && resp.data && resp.data.status === 'success') {
            return String(resp.data.countryCode || '').trim().toUpperCase();
        }
    } catch (_) { /* 地区探测失败不影响检测结果 */ }
    return '';
}

async function testProxyUrl(raw, options = {}) {
    const proxyUrl = substituteProxySession(String(raw || '').trim());
    const t0 = Date.now();
    if (!proxyUrl) {
        return { ok: false, error: '代理 URL 为空', latencyMs: 0 };
    }

    let agent;
    try {
        agent = new ProxyAgent({ getProxyForUrl: () => proxyUrl });
    } catch (error) {
        return { ok: false, error: `代理 URL 解析失败: ${error.message}`, latencyMs: Date.now() - t0 };
    }

    const timeout = Math.max(3000, Number(options.timeoutMs) || 12000);
    let lastErr = '';
    for (const probeUrl of PROBE_URLS) {
        try {
            const response = await axios.get(probeUrl, {
                httpsAgent: agent,
                httpAgent: agent,
                proxy: false,
                timeout,
                validateStatus: () => true
            });
            if (response.status === 200) {
                const ip = String(response.data || '').trim().split(/\s+/)[0];
                const region = await lookupProxyRegion(agent, timeout);
                return {
                    ok: true,
                    ip,
                    region,
                    latencyMs: Date.now() - t0,
                    probedVia: probeUrl
                };
            }
            lastErr = `HTTP ${response.status} via ${probeUrl}`;
        } catch (error) {
            lastErr = `${error.code || ''} ${error.message}`.trim();
        }
    }

    return {
        ok: false,
        error: lastErr || '未知错误',
        latencyMs: Date.now() - t0
    };
}

module.exports = {
    PROBE_URLS,
    substituteProxySession,
    maskProxyUrl,
    hashProxyUrl,
    parseProxyMeta,
    normalizeProxyLines,
    testProxyUrl,
    lookupProxyRegion
};
