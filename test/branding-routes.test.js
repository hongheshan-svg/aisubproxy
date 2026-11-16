'use strict';
const fs = require('fs');
const os = require('os');
const path = require('path');
const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'branding-routes-'));
process.env.BRANDING_DIR = directory;
const store = require('../mysql-store');
const auth = require('../admin-auth');
const { app, updater } = require('../server');
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jbaUAAAAASUVORK5CYII=', 'base64');
let server, base, token;
beforeAll(async () => {
    vi.spyOn(store, 'ensureReady').mockResolvedValue();
    vi.spyOn(store, 'getAdminPaths').mockResolvedValue({ loginPath: 'admin-login', panelPath: 'admin' });
    vi.spyOn(store, 'getAdminAuthConfig').mockResolvedValue({ passwordVersion: 1 });
    vi.spyOn(store, 'runExecute').mockResolvedValue({});
    vi.spyOn(store, 'runQuery').mockResolvedValue([]);
    token = auth.issueAdminToken(1, 'test@example.com').token;
    server = await new Promise((resolve, reject) => { const s = app.listen(0, '127.0.0.1', () => resolve(s)); s.once('error', reject); });
    base = `http://127.0.0.1:${server.address().port}`;
});
afterAll(async () => {
    if (server) await new Promise(resolve => server.close(resolve));
    vi.restoreAllMocks(); delete process.env.BRANDING_DIR;
    fs.rmSync(directory, { recursive: true, force: true });
});

it('requires authentication for both uploading logos and saving branding', async () => {
    for (const route of ['/api/admin/branding/logo', '/api/admin/branding']) {
        expect((await fetch(base + route, { method: 'POST' })).status).toBe(401);
    }
});
it('protects OTA controls and only exposes the current version publicly', async () => {
    for (const [route, method] of [['status', 'GET'], ['check-updates', 'GET'], ['update', 'POST'], ['restart', 'POST'], ['rollback', 'POST']]) {
        expect((await fetch(base + '/api/admin/system/' + route, { method })).status).toBe(401);
    }
    const publicVersion = await fetch(base + '/api/public/version');
    expect(publicVersion.headers.get('cache-control')).toBe('no-store');
    expect(await publicVersion.json()).toMatchObject({ version: require('../ota/common').readVersion(path.resolve(__dirname, '..')) });
    const status = await fetch(base + '/api/admin/system/status', { headers: { Authorization: `Bearer ${token}` } });
    expect(status.status).toBe(200);
    expect(await status.json()).toMatchObject({ success: true, version: require('../ota/common').readVersion(path.resolve(__dirname, '..')), managed: false });
});
it('blocks new admin writes during an OTA switch while allowing status reads', async () => {
    updater.gate = true;
    try {
        const headers = { Authorization: `Bearer ${token}` };
        expect((await fetch(base + '/api/admin/checkout/generate', { method: 'POST', headers })).status).toBe(503);
        expect((await fetch(base + '/api/admin/system/status', { headers })).status).toBe(200);
    } finally { updater.gate = false; }
});
it('uploads a logo and publishes the saved brand and correctly typed static image', async () => {
    const upload = await fetch(base + '/api/admin/branding/logo', { method: 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'image/png' }, body: png });
    expect(upload.status).toBe(200);
    const { logoUrl } = await upload.json();
    const save = await fetch(base + '/api/admin/branding', { method: 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: JSON.stringify({ name: '<b>Custom Brand</b>', logoUrl }) });
    expect(save.status).toBe(200);
    const result = await fetch(base + '/api/public/branding').then(r => r.json());
    expect(result.branding).toEqual({ name: '<b>Custom Brand</b>', logoUrl });
    const image = await fetch(base + logoUrl);
    expect(image.headers.get('content-type')).toContain('image/png');
    expect(image.headers.get('x-content-type-options')).toBe('nosniff');
    expect(Buffer.from(await image.arrayBuffer())).toEqual(png);
});
it('rejects forged image content and oversized uploads', async () => {
    for (const [body, expected] of [[Buffer.from('<svg onload="alert(1)"/>'), 400], [Buffer.alloc(513 * 1024), 413]]) {
        const res = await fetch(base + '/api/admin/branding/logo', { method: 'POST', headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'image/png' }, body });
        expect(res.status).toBe(expected);
        expect((await res.json()).success).toBe(false);
    }
});
it('continues accepting an existing KC code without imposing the new format on redemption', async () => {
    vi.spyOn(store, 'verifyCdkDetails').mockResolvedValue({ type: '自助', plan_type: 'plus' });
    vi.spyOn(store, 'getRunningTaskByCdk').mockResolvedValue(null);
    vi.spyOn(store, 'getActivationAttemptLimit').mockResolvedValue(null);
    const res = await fetch(base + '/api/verify-cdk', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ cdk: 'KC-ABCDEFGHJKLM234' }) });
    expect(res.status).toBe(200);
    expect((await res.json()).success).toBe(true);
});
it('limits invalid CDK guesses even when forwarded headers keep changing', async () => {
    vi.spyOn(store, 'verifyCdkDetails').mockResolvedValue(null);
    let limited = false;
    for (let i = 0; i < 65; i++) {
        const res = await fetch(base + '/api/verify-cdk', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Forwarded-For': `198.51.100.${i}` }, body: JSON.stringify({ cdk: 'invalid' }) });
        if (res.status === 429) { limited = true; expect(Number(res.headers.get('retry-after'))).toBeGreaterThan(0); break; }
    }
    expect(limited).toBe(true);
});
