'use strict';
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const zlib = require('zlib');
const VERSION_RE = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;
const MAX_BUNDLE = 20 * 1024 * 1024;
const MAX_EXPANDED = 48 * 1024 * 1024;
const sha256 = bytes => crypto.createHash('sha256').update(bytes).digest('hex');
function version(value) {
    const normalized = String(value || '').replace(/^v/, '');
    if (!VERSION_RE.test(normalized) || normalized.length > 32) throw new Error('版本号格式无效');
    return normalized;
}
function newer(a, b) {
    const left = version(a).split('.').map(BigInt), right = version(b).split('.').map(BigInt);
    for (let i = 0; i < 3; i++) if (left[i] !== right[i]) return left[i] > right[i];
    return false;
}
function readVersion(root) { return version(fs.readFileSync(path.join(root, 'VERSION'), 'utf8').trim()); }
function atomicJson(file, value) {
    fs.mkdirSync(path.dirname(file), { recursive: true, mode: 0o700 });
    const temporary = `${file}.${process.pid}.${crypto.randomBytes(8).toString('hex')}`;
    fs.writeFileSync(temporary, JSON.stringify(value, null, 2), { mode: 0o600 });
    fs.renameSync(temporary, file);
}
function readState(directory) {
    try { return JSON.parse(fs.readFileSync(path.join(directory, 'state.json'), 'utf8')); }
    catch (error) { if (error.code === 'ENOENT') return { phase: 'idle', active: null, previous: null, pending: null }; throw error; }
}
function writeState(directory, state) { atomicJson(path.join(directory, 'state.json'), { ...state, updatedAt: new Date().toISOString() }); }
function allowedFile(name) {
    if (typeof name !== 'string' || name.length > 180 || name.includes('..') || name.includes('\\')) return false;
    return /^(?:[a-zA-Z0-9_-]+\.(?:js|sql)|VERSION|LICENSE|package(?:-lock)?\.json|Dockerfile|docker-entrypoint\.sh|requirements-hcaptcha\.txt)$/.test(name) ||
        /^ota\/[a-zA-Z0-9_-]+\.js$/.test(name) || name === 'hcaptcha/solver.py' ||
        /^public\/(?:[a-zA-Z0-9_-]+\/)*[a-zA-Z0-9_.-]+\.(?:js|html|css|svg|png|webp|ico|LICENSE)$/.test(name);
}
function runtimeHash(root, read = name => fs.readFileSync(path.join(root, name))) {
    const lock = JSON.parse(read('package-lock.json'));
    delete lock.version;
    if (lock.packages?.['']) delete lock.packages[''].version;
    const names = ['Dockerfile', 'docker-entrypoint.sh', 'requirements-hcaptcha.txt', 'ota/launcher.js', 'ota/common.js'];
    return sha256(Buffer.concat([Buffer.from(JSON.stringify(lock)), ...names.map(name => Buffer.concat([Buffer.from(name + '\0'), Buffer.from(read(name))]))]));
}
function decodeBundle(bytes, expectedVersion, baseRoot) {
    if (!Buffer.isBuffer(bytes) || bytes.length > MAX_BUNDLE) throw new Error('更新包超过 20 MB 限制');
    const bundle = JSON.parse(zlib.gunzipSync(bytes, { maxOutputLength: MAX_EXPANDED }));
    if (bundle.format !== 'gw-link-ota-v1' || version(bundle.version) !== version(expectedVersion)) throw new Error('更新包版本不匹配');
    if (!Array.isArray(bundle.files) || !bundle.files.length || bundle.files.length > 500) throw new Error('更新包文件清单无效');
    const files = new Map();
    let size = 0;
    for (const file of bundle.files) {
        if (!allowedFile(file.path) || files.has(file.path) || typeof file.content !== 'string' || !/^[A-Za-z0-9+/]*={0,2}$/.test(file.content)) throw new Error('更新包含非法或重复文件');
        const data = Buffer.from(file.content, 'base64');
        size += data.length;
        if (size > MAX_EXPANDED || data.toString('base64') !== file.content || sha256(data) !== file.sha256) throw new Error('更新包文件校验失败');
        files.set(file.path, data);
    }
    for (const required of ['VERSION', 'server.js', 'mysql-store.js', 'mysql-schema.sql', 'public/index.html', 'public/admin.html', 'ota/common.js', 'ota/service.js', 'ota/launcher.js']) {
        if (!files.has(required)) throw new Error('更新包缺少必要文件：' + required);
    }
    if (files.get('VERSION').toString().trim() !== bundle.version) throw new Error('更新包版本文件不一致');
    const digest = runtimeHash(baseRoot);
    if (bundle.runtimeHash !== digest || runtimeHash('', name => { if (!files.has(name)) throw new Error('更新包缺少运行环境清单'); return files.get(name); }) !== digest) {
        throw new Error('此版本需要升级 Docker 运行环境，请先重新构建镜像');
    }
    return { version: bundle.version, files };
}
function releaseRoot(baseRoot, directory, entry) {
    if (!entry) return baseRoot;
    if (entry.directory === 'base' && entry.version === readVersion(baseRoot)) return baseRoot;
    if (!/^[0-9]+\.[0-9]+\.[0-9]+-[a-f0-9]{16}$/.test(entry.directory) || version(entry.version) !== entry.directory.split('-')[0]) throw new Error('本地版本路径无效');
    const resolved = path.join(directory, 'releases', entry.directory);
    if (fs.lstatSync(resolved).isSymbolicLink() || readVersion(resolved) !== entry.version) throw new Error('本地版本校验失败');
    return resolved;
}
module.exports = { version, newer, readVersion, sha256, atomicJson, readState, writeState, allowedFile, runtimeHash, decodeBundle, releaseRoot, MAX_BUNDLE };
