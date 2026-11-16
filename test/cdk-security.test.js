'use strict';
const { createCdks, createCdkRateLimiter } = require('../cdk-security');
const { getClientMeta } = require('../admin-auth');
function response() {
    return { statusCode: 200, headers: {}, set(k,v) { this.headers[k] = v; return this; }, status(code) { this.statusCode = code; return this; }, json(body) { this.body = body; return this; } };
}

describe('CDK generation and abuse controls', () => {
    it('generates unique GW codes with 100 random bits and bounded batch sizes', () => {
        const codes = createCdks(100);
        expect(new Set(codes).size).toBe(100);
        codes.forEach(code => expect(code).toMatch(/^GW-[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{20}$/));
        expect(createCdks(Infinity)).toHaveLength(100);
        expect(createCdks(-5)).toHaveLength(1);
        expect(createCdks(2.8)).toHaveLength(2);
    });
    it('shares a per-IP budget across endpoints and ignores forged forwarded headers', () => {
        let time = 1000;
        const limiter = createCdkRateLimiter({ now: () => time, capacity: 2 });
        const next = vi.fn();
        for (const path of ['/api/verify-cdk', '/api/cdk/query', '/api/run-process']) {
            const res = response();
            limiter({ ip: '1.2.3.4', path, headers: { 'x-forwarded-for': Math.random().toString() } }, res, next);
            if (path === '/api/run-process') { expect(res.statusCode).toBe(429); expect(res.headers['Retry-After']).toBe('1'); }
        }
        expect(next).toHaveBeenCalledTimes(2);
        time += 1000;
        limiter({ ip: '1.2.3.4' }, response(), next);
        expect(next).toHaveBeenCalledTimes(3);
    });
    it('bounds total traffic and memory even when client IPs rotate', () => {
        const limiter = createCdkRateLimiter({ globalCapacity: 2, now: () => 0 });
        const next = vi.fn();
        for (let i = 0; i < 3; i++) {
            const res = response(); limiter({ ip: `ip-${i}` }, res, next);
            if (i === 2) expect(res.statusCode).toBe(429);
        }
        expect(next).toHaveBeenCalledTimes(2);
        const bounded = createCdkRateLimiter({ maxEntries: 1, now: () => 0 });
        bounded({ ip: 'a' }, response(), next);
        const res = response(); bounded({ ip: 'b' }, res, next);
        expect(res.statusCode).toBe(429);
    });
    it('uses the resolved Express address for administrator login limits too', () => {
        expect(getClientMeta({ ip: '1.2.3.4', headers: { 'x-forwarded-for': 'forged', 'x-real-ip': 'forged' }, socket: {} }).ip).toBe('1.2.3.4');
    });
});
