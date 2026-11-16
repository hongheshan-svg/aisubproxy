'use strict';
const fs = require('fs');
const os = require('os');
const path = require('path');
const { execFileSync } = require('child_process');
let dir;
beforeEach(() => { dir = fs.mkdtempSync(path.join(os.tmpdir(), 'admin-secret-')); });
afterEach(() => { fs.rmSync(dir, { recursive: true, force: true }); });
function readSecret(file = path.join(dir, 'secret'), override = '') {
    return execFileSync(process.execPath, ['-e', 'process.stdout.write(require("./admin-auth").resolveAdminTokenSecret())'], {
        cwd: path.join(__dirname, '..'), encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'],
        env: { ...process.env, ADMIN_TOKEN_SECRET: override, ADMIN_TOKEN_SECRET_FILE: file }
    });
}
it('uses an unpredictable persistent secret with restricted file permissions', () => {
    const file = path.join(dir, 'secret');
    const first = readSecret(file);
    expect(first).toMatch(/^[a-f0-9]{64}$/);
    expect(readSecret(file)).toBe(first);
    expect(readSecret(path.join(dir, 'other'))).not.toBe(first);
    expect(fs.statSync(file).mode & 0o777).toBe(0o600);
});
it('honors explicit configuration and fails closed for a damaged persisted secret', () => {
    expect(readSecret(path.join(dir, 'absent'), 'configured-secret-for-test')).toBe('configured-secret-for-test');
    expect(fs.existsSync(path.join(dir, 'absent'))).toBe(false);
    fs.writeFileSync(path.join(dir, 'short'), 'short');
    expect(() => readSecret(path.join(dir, 'short'))).toThrow();
});
