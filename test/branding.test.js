'use strict';
const fs = require('fs/promises');
const os = require('os');
const path = require('path');
const { createBrandingService, validateBranding, validateLogo } = require('../branding');
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jbaUAAAAASUVORK5CYII=', 'base64');

describe('persistent branding', () => {
    let dir, store, saved;
    beforeEach(async () => {
        dir = await fs.mkdtemp(path.join(os.tmpdir(), 'branding-test-'));
        saved = null;
        store = {
            runQuery: vi.fn(async () => saved ? [{ config_value: saved }] : []),
            runExecute: vi.fn(async (_sql, args) => { saved = args[1]; })
        };
    });
    afterEach(async () => { await fs.rm(dir, { recursive: true, force: true }); });
    it('keeps a custom name and uploaded logo across a new service instance', async () => {
        const service = createBrandingService(store, dir);
        const logoUrl = await service.upload(png);
        expect(await service.upload(png)).toBe(logoUrl);
        const brand = await service.save({ name: '  GW-LINK  ', logoUrl });
        const restarted = createBrandingService(store, dir);
        expect(await restarted.get()).toEqual(brand);
        expect(await fs.readFile(path.join(dir, path.basename(logoUrl)))).toEqual(png);
    });
    it('uses one database read for concurrent visitors and makes saved changes visible immediately', async () => {
        const service = createBrandingService(store, dir);
        await Promise.all([service.get(), service.get(), service.get()]);
        expect(store.runQuery).toHaveBeenCalledTimes(1);
        await service.save({ name: '另一品牌', logoUrl: '' });
        expect((await service.get()).name).toBe('另一品牌');
    });
    it('rejects external URLs, traversal paths, nonexistent logos, and invalid names', async () => {
        for (const logoUrl of ['https://example.com/x.svg', 'javascript:alert(1)', '/api/public/brand-logo/../../secret']) {
            expect(() => validateBranding({ name: 'GW', logoUrl })).toThrow();
        }
        for (const name of ['', 'x'.repeat(41), 'a\nb']) expect(() => validateBranding({ name })).toThrow();
        await expect(createBrandingService(store, dir).save({ name: 'GW', logoUrl: '/api/public/brand-logo/' + 'a'.repeat(64) + '.png' })).rejects.toThrow();
        expect(store.runExecute).not.toHaveBeenCalled();
    });
    it('rejects executable content, oversized files and oversized dimensions', () => {
        expect(() => validateLogo(Buffer.from('<svg onload="alert(1)"></svg>'))).toThrow();
        expect(() => validateLogo(Buffer.alloc(512 * 1024 + 1))).toThrow();
        const huge = Buffer.from(png); huge.writeUInt32BE(100000, 16);
        expect(() => validateLogo(huge)).toThrow();
        expect(() => validateLogo(png)).not.toThrow();
    });
});
