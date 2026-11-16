'use strict';
const crypto = require('crypto');
const CHARSET = 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789'; // 32 symbols; excludes 0/O/1/I.

function createCdks(count) {
    const target = Math.max(1, Math.min(Math.floor(Number(count)) || 1, 100));
    const codes = new Set();
    while (codes.size < target) {
        let suffix = '';
        for (let i = 0; i < 20; i += 1) suffix += CHARSET[crypto.randomInt(CHARSET.length)];
        codes.add(`GW-${suffix}`);
    }
    return [...codes];
}

// Single-process token buckets: shared by verification, status lookup and activation.
// Bounded memory and a global budget also limit rotating-IP request floods.
function createCdkRateLimiter({ now = Date.now, capacity = 60, refillPerSecond = 1, globalCapacity = 600, globalRefill = 10, maxEntries = 10000 } = {}) {
    const buckets = new Map();
    const global = { tokens: globalCapacity, time: now() };
    function refill(bucket, limit, rate, time) {
        bucket.tokens = Math.min(limit, bucket.tokens + Math.max(0, time - bucket.time) * rate / 1000);
        bucket.time = time;
    }
    return function limit(req, res, next) {
        const time = now();
        // Express req.ip honors only explicitly trusted proxies, never arbitrary forwarded headers.
        const key = String(req.ip || req.socket?.remoteAddress || 'unknown').replace(/^::ffff:/, '');
        refill(global, globalCapacity, globalRefill, time);
        let bucket = buckets.get(key);
        if (!bucket) {
            if (buckets.size >= maxEntries) {
                for (const [ip, entry] of buckets) if (time - entry.time > 600000) buckets.delete(ip);
                if (buckets.size >= maxEntries) return deny(60);
            }
            bucket = { tokens: capacity, time };
            buckets.set(key, bucket);
        }
        refill(bucket, capacity, refillPerSecond, time);
        if (bucket.tokens < 1 || global.tokens < 1) {
            return deny(Math.max(1, Math.ceil((1 - bucket.tokens) / refillPerSecond), Math.ceil((1 - global.tokens) / globalRefill)));
        }
        bucket.tokens -= 1;
        global.tokens -= 1;
        return next();
        function deny(seconds) {
            res.set('Retry-After', String(seconds));
            return res.status(429).json({ success: false, message: `请求过于频繁，请 ${seconds} 秒后再试` });
        }
    };
}

module.exports = { createCdks, createCdkRateLimiter };
