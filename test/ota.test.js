'use strict';
const fs = require('fs');
const os = require('os');
const path = require('path');
const zlib = require('zlib');
const { build } = require('../scripts/build-ota');
const { readVersion, newer, sha256, decodeBundle, readState, writeState, releaseRoot } = require('../ota/common');
const { GithubReleases } = require('../ota/github');
const { UpdateService } = require('../ota/service');
const root = path.resolve(__dirname, '..');
const CURRENT = readVersion(root);
const parts = CURRENT.split('.'); parts[2] = String(BigInt(parts[2]) + 1n);
const NEXT = parts.join('.');
let directory, original;
beforeAll(() => { original = JSON.parse(zlib.gunzipSync(build(root).bytes)); });
beforeEach(() => { directory = fs.mkdtempSync(path.join(os.tmpdir(), 'gw-ota-test-')); });
afterEach(() => { fs.rmSync(directory, { recursive: true, force: true }); vi.unstubAllEnvs(); vi.restoreAllMocks(); });
function bundle(edit = () => {}) {
    const data = structuredClone(original); data.version = NEXT;
    const ver = data.files.find(file => file.path === 'VERSION');
    ver.content = Buffer.from(NEXT + '\n').toString('base64'); ver.sha256 = sha256(Buffer.from(NEXT + '\n'));
    edit(data); return zlib.gzipSync(Buffer.from(JSON.stringify(data)));
}
function service(extra = {}) {
    const bytes = bundle();
    return new UpdateService({ root, baseRoot: root, directory, managed: true,
        github: { repo: 'test/repo', latest: vi.fn().mockResolvedValue({ version: NEXT }), download: vi.fn().mockResolvedValue({ bytes, digest: sha256(bytes) }) },
        store: { runQuery: vi.fn().mockResolvedValue([{ n: 0 }]), getMaintenanceModeState: vi.fn().mockResolvedValue({ enabled: false, drain: false }), setMaintenanceModeState: vi.fn().mockResolvedValue() },
        backup: vi.fn().mockResolvedValue('/backup.sql.gz'), restart: vi.fn(), ...extra });
}
it('compares numeric versions and rejects non-release tags', () => {
    expect(newer('v0.0.10', '0.0.9')).toBe(true);
    expect(newer(CURRENT, CURRENT)).toBe(false);
    for (const bad of ['../1', '0.1', '01.0.0', '0.0.2-beta']) expect(() => newer(bad, CURRENT)).toThrow();
});
it('builds a reproducible bundle without runtime data or credentials', () => {
    const a = build(root), b = build(root);
    expect(a.digest).toBe(b.digest);
    const result = decodeBundle(a.bytes, CURRENT, root);
    expect([...result.files.keys()].some(name => /^(data\/|\.env|node_modules\/)/.test(name))).toBe(false);
    expect(result.files.has('public/ota.js')).toBe(true);
    expect(result.files.get('public/admin.html').toString()).toContain(`/workspace.css?v=${CURRENT}`);
});
it.each(['../server.js', '/tmp/evil.js', 'public/../../evil.js', 'data/secret.js', 'public/evil\\file.js'])('rejects unsafe update path %s', name => {
    const bytes = bundle(data => { data.files[0].path = name; });
    expect(() => decodeBundle(bytes, NEXT, root)).toThrow();
});
it('rejects modified files, duplicate entries, invalid versions and runtime changes', () => {
    expect(() => decodeBundle(bundle(data => { data.files[0].sha256 = '0'.repeat(64); }), NEXT, root)).toThrow(/校验/);
    expect(() => decodeBundle(bundle(data => { data.files.push(data.files[0]); }), NEXT, root)).toThrow(/重复/);
    expect(() => decodeBundle(bundle(), '0.0.0', root)).toThrow(/版本/);
    expect(() => decodeBundle(bundle(data => { data.runtimeHash = '0'.repeat(64); }), NEXT, root)).toThrow(/Docker/);
});
it('rejects inflated archives and symlinked local releases', () => {
    expect(() => decodeBundle(zlib.gzipSync(Buffer.alloc(49 * 1024 * 1024)), NEXT, root)).toThrow();
    fs.mkdirSync(path.join(directory, 'releases'));
    fs.symlinkSync(root, path.join(directory, `releases/${CURRENT}-aaaaaaaaaaaaaaaa`));
    expect(() => releaseRoot(root, directory, { version: CURRENT, directory: `${CURRENT}-aaaaaaaaaaaaaaaa` })).toThrow();
    expect(releaseRoot(root, directory, { version: CURRENT, directory: 'base' })).toBe(root);
});
it('downloads once, persists a verified release and rejects concurrent operations', async () => {
    const updater = service();
    expect(updater.startUpdate(NEXT).state.phase).toBe('downloading');
    expect(() => updater.startUpdate(NEXT)).toThrow(/正在进行/);
    await updater.operation;
    expect(updater.status().state).toMatchObject({ phase: 'ready', pendingVersion: NEXT });
    expect(fs.readFileSync(path.join(releaseRoot(root, directory, readState(directory).pending), 'VERSION'), 'utf8').trim()).toBe(NEXT);
    expect(() => updater.startUpdate(NEXT)).toThrow(/待应用/);
    expect(updater.restart).not.toHaveBeenCalled();
});
it('rejects checksum mismatch without publishing a release', async () => {
    const updater = service(); updater.github.download.mockResolvedValue({ bytes: bundle(), digest: '0'.repeat(64) });
    updater.startUpdate(NEXT); await updater.operation;
    expect(readState(directory)).toMatchObject({ phase: 'failed' });
    expect(readState(directory).pending).toBeFalsy();
});
it.each(['memory', 'database'])('blocks switching while tasks run in %s', async source => {
    const updater = service({ activeJobs: () => source === 'memory' ? 1 : 0 });
    if (source === 'database') updater.store.runQuery.mockResolvedValue([{ n: 1 }]);
    writeState(directory, { phase: 'ready', pending: { directory: 'base', version: CURRENT } });
    updater.startSwitch(); await updater.operation;
    expect(updater.status().state.message).toContain('开通任务'); expect(updater.gate).toBe(false);
    expect(updater.backup).not.toHaveBeenCalled(); expect(updater.restart).not.toHaveBeenCalled();
});
it('backs up before restart and saves the original maintenance state', async () => {
    const updater = service();
    writeState(directory, { phase: 'ready', pending: { directory: 'base', version: CURRENT } });
    updater.startSwitch(); expect(updater.gate).toBe(true); await updater.operation;
    expect(updater.backup).toHaveBeenCalledOnce(); expect(updater.restart).toHaveBeenCalledOnce();
    expect(readState(directory)).toMatchObject({ phase: 'restarting', restoreMaintenance: { enabled: false, drain: false } });
});
it('restores maintenance and refuses restart if the backup fails', async () => {
    const updater = service({ backup: vi.fn().mockRejectedValue(new Error('backup failed')) });
    writeState(directory, { phase: 'ready', pending: { directory: 'base', version: CURRENT } });
    updater.startSwitch(); await updater.operation;
    expect(updater.store.setMaintenanceModeState).toHaveBeenLastCalledWith(false, false);
    expect(updater.gate).toBe(false); expect(updater.restart).not.toHaveBeenCalled(); expect(readState(directory).phase).toBe('failed');
});
it('deduplicates release checks and shows network failure instead of claiming latest', async () => {
    const updater = service();
    await Promise.all([updater.check(), updater.check()]); await updater.check(true);
    expect(updater.github.latest).toHaveBeenCalledOnce();
    updater.cached = null; updater.github.latest.mockRejectedValue(new Error('GitHub unavailable'));
    expect(await updater.check()).toMatchObject({ latest: null, hasUpdate: false, warning: 'GitHub unavailable' });
});
it('only downloads from GitHub HTTPS and strips credentials on CDN redirects', async () => {
    vi.stubEnv('OTA_GITHUB_TOKEN', 'test-token');
    const mock = vi.fn().mockResolvedValueOnce(new Response(null, { status: 302, headers: { location: 'https://release-assets.githubusercontent.com/test' } })).mockResolvedValueOnce(new Response('content'));
    const github = new GithubReleases('test/repo', mock);
    expect((await github.get('https://api.github.com/test', { binary: true })).toString()).toBe('content');
    expect(mock.mock.calls[0][1].headers.Authorization).toBe('Bearer test-token');
    expect(mock.mock.calls[1][1].headers.Authorization).toBeUndefined();
    for (const url of ['http://github.com/test', 'https://localhost/test', 'https://github.com:444/test']) await expect(github.get(url)).rejects.toThrow(/允许/);
    mock.mockResolvedValueOnce(new Response(null, { status: 302, headers: { location: 'http://127.0.0.1/secret' } }));
    await expect(github.get('https://api.github.com/test')).rejects.toThrow(/允许/);
});
it('rejects oversized download streams', async () => {
    const github = new GithubReleases('test/repo', vi.fn().mockResolvedValue(new Response('too big')));
    await expect(github.get('https://api.github.com/test', { limit: 2 })).rejects.toThrow(/大小/);
});
