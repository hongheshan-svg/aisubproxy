'use strict';
const fs = require('fs/promises');
const path = require('path');
const crypto = require('crypto');

const DEFAULT_BRANDING = { name: 'GW-LINK', logoUrl: '' };
const LOGO_PATH = /^\/api\/public\/brand-logo\/([a-f0-9]{64}\.png)$/;
const MAX_LOGO_BYTES = 512 * 1024;

function validateBranding(value) {
    const name = typeof value?.name === 'string' ? value.name.trim() : '';
    const logoUrl = typeof value?.logoUrl === 'string' ? value.logoUrl : '';
    if (!name || [...name].length > 40 || /[\u0000-\u001f\u007f]/.test(name)) {
        throw new Error('品牌名称须为 1–40 个字符，且不能包含控制字符');
    }
    if (logoUrl && !LOGO_PATH.test(logoUrl)) throw new Error('请上传有效的网站 Logo');
    return { name, logoUrl };
}

function validateLogo(buffer) {
    if (!Buffer.isBuffer(buffer) || buffer.length < 45 || buffer.length > MAX_LOGO_BYTES) {
        throw new Error('Logo 文件须为不超过 512 KB 的 PNG 图片');
    }
    if (!buffer.subarray(0, 8).equals(Buffer.from('89504e470d0a1a0a', 'hex')) ||
        buffer.readUInt32BE(8) !== 13 || buffer.toString('ascii', 12, 16) !== 'IHDR' ||
        !buffer.subarray(-12).equals(Buffer.from('0000000049454e44ae426082', 'hex'))) {
        throw new Error('Logo 不是有效的 PNG 图片');
    }
    const width = buffer.readUInt32BE(16), height = buffer.readUInt32BE(20);
    if (!width || !height || width > 2048 || height > 2048) throw new Error('Logo 尺寸须在 2048 × 2048 以内');
}

function createBrandingService(store, directory = path.join(__dirname, 'data', 'branding')) {
    let cached = null, expiresAt = 0, pending = null;
    async function get() {
        if (cached && Date.now() < expiresAt) return cached;
        if (!pending) {
            pending = store.runQuery('SELECT config_value FROM app_config WHERE config_key = ?', ['site_branding'])
                .then(rows => {
                    let value = DEFAULT_BRANDING;
                    if (rows[0]?.config_value) {
                        try { value = validateBranding(JSON.parse(rows[0].config_value)); } catch (_) { /* fallback */ }
                    }
                    cached = { ...value };
                    expiresAt = Date.now() + 30000;
                    return cached;
                }).finally(() => { pending = null; });
        }
        return pending;
    }
    async function save(input) {
        const value = validateBranding(input);
        if (value.logoUrl) await fs.access(path.join(directory, value.logoUrl.match(LOGO_PATH)[1]));
        // Finish any older read before publishing the newly persisted value.
        if (pending) await pending;
        await store.runExecute('INSERT INTO app_config (config_key, config_value) VALUES (?, ?) ON DUPLICATE KEY UPDATE config_value = VALUES(config_value)', ['site_branding', JSON.stringify(value)]);
        cached = value;
        expiresAt = Date.now() + 30000;
        return value;
    }
    async function upload(buffer) {
        validateLogo(buffer);
        const file = crypto.createHash('sha256').update(buffer).digest('hex') + '.png';
        await fs.mkdir(directory, { recursive: true });
        await fs.writeFile(path.join(directory, file), buffer, { flag: 'wx', mode: 0o644 }).catch(error => {
            if (error.code !== 'EEXIST') throw error;
        });
        return `/api/public/brand-logo/${file}`;
    }
    return { get, save, upload, directory };
}

module.exports = { createBrandingService, validateBranding, validateLogo, MAX_LOGO_BYTES };
