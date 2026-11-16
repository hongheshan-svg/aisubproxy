'use strict';
const fs = require('fs');
const os = require('os');
const net = require('net');
const path = require('path');
const { Launcher } = require('../ota/launcher');
const { readState, writeState } = require('../ota/common');
let root, directory, launcher;
function writeApp(target, version, broken = false) {
    fs.mkdirSync(target, { recursive: true }); fs.writeFileSync(path.join(target, 'VERSION'), version + '\n');
    fs.writeFileSync(path.join(target, 'server.js'), broken ? 'process.exit(2);' : `require('http').createServer((req,res)=>{res.setHeader('Content-Type','application/json');res.end(JSON.stringify({version:${JSON.stringify(version)},runId:process.env.OTA_RUN_ID}));}).listen(Number(process.env.PORT),'127.0.0.1');`);
}
beforeEach(async () => {
    root = fs.mkdtempSync(path.join(os.tmpdir(), 'gw-launch-test-')); directory = path.join(root, 'data/ota');
    const port = await new Promise(resolve => { const s = net.createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => resolve(p)); }); });
    vi.stubEnv('PORT', String(port)); writeApp(root, '0.0.1');
    launcher = new Launcher({ baseRoot: root, directory, restoreMaintenance: vi.fn().mockResolvedValue(), healthTimeout: 15000 });
});
afterEach(async () => { if (launcher) await launcher.stop(); fs.rmSync(root, { recursive: true, force: true }); vi.unstubAllEnvs(); });
async function switchTo(entry) {
    launcher.patch({ phase: 'restarting', candidate: entry, pending: entry, restoreMaintenance: { enabled: false, drain: false } });
    launcher.switching = true;
    try { await launcher.activate(); } finally { launcher.switching = false; }
}
it('switches real processes, persists the active version, and rolls back to the base', async () => {
    await launcher.start();
    const next = { directory: '0.0.2-aaaaaaaaaaaaaaaa', version: '0.0.2' };
    writeApp(path.join(directory, 'releases', next.directory), '0.0.2');
    await switchTo(next);
    expect(readState(directory)).toMatchObject({ phase: 'completed', active: next, previous: { directory: 'base', version: '0.0.1' } });
    expect(launcher.restoreMaintenance).toHaveBeenCalledWith({ enabled: false, drain: false });
    const old = readState(directory).previous;
    await launcher.stop();
    launcher = new Launcher({ baseRoot: root, directory, restoreMaintenance: vi.fn(), healthTimeout: 15000 });
    await launcher.start(); expect(readState(directory).active.version).toBe('0.0.2');
    await switchTo(old); expect(readState(directory).active.version).toBe('0.0.1');
}, 30000);
it('automatically restores the old process after a broken update', async () => {
    await launcher.start();
    const next = { directory: '0.0.2-bbbbbbbbbbbbbbbb', version: '0.0.2' };
    writeApp(path.join(directory, 'releases', next.directory), '0.0.2', true);
    await switchTo(next);
    expect(readState(directory)).toMatchObject({ phase: 'rolled_back', active: { directory: 'base', version: '0.0.1' }, candidate: null, pending: null });
    const res = await fetch(`http://127.0.0.1:${process.env.PORT}/api/public/version`).then(r => r.json());
    expect(res.version).toBe('0.0.1');
});
it('recovers an interrupted switch, removes its stale lock and restores maintenance', async () => {
    fs.mkdirSync(path.join(directory, 'operation.lock'), { recursive: true });
    writeState(directory, { phase: 'restarting', active: null, candidate: { version: '0.0.2' }, restoreMaintenance: { enabled: true, drain: false } });
    await launcher.start();
    expect(readState(directory)).toMatchObject({ phase: 'failed', active: null, candidate: null, restoreMaintenance: null });
    expect(fs.existsSync(path.join(directory, 'operation.lock'))).toBe(false);
    expect(launcher.restoreMaintenance).toHaveBeenCalledWith({ enabled: true, drain: false });
});
