'use strict';
// Stable container supervisor; changing this file requires rebuilding the runtime image.
const fs = require('fs');
const path = require('path');
const http = require('http');
const crypto = require('crypto');
const { spawn } = require('child_process');
const { readState, writeState, releaseRoot, readVersion } = require('./common');

class Launcher {
    constructor({ baseRoot = path.resolve(__dirname, '..'), directory = process.env.OTA_DATA_DIR || path.join(baseRoot, 'data', 'ota'),
        restoreMaintenance, healthTimeout = 90000 } = {}) {
        Object.assign(this, { baseRoot, directory, restoreMaintenance, healthTimeout });
        this.child = null; this.stopping = false; this.switching = false;
    }
    patch(values) { writeState(this.directory, { ...readState(this.directory), ...values }); }
    async restore() {
        const state = readState(this.directory);
        if (state.restoreMaintenance) {
            await this.restoreMaintenance(state.restoreMaintenance);
            this.patch({ restoreMaintenance: null });
        }
    }
    spawn(entry) {
        const root = releaseRoot(this.baseRoot, this.directory, entry);
        const runId = crypto.randomUUID();
        const child = spawn(process.execPath, [path.join(root, 'server.js')], {
            cwd: root, detached: process.platform !== 'win32', stdio: ['inherit', 'inherit', 'inherit', 'ipc'],
            env: { ...process.env, OTA_MANAGED: '1', OTA_BASE_DIR: this.baseRoot, OTA_DATA_DIR: this.directory, OTA_RUN_ID: runId,
                HCAPTCHA_SOLVER_SCRIPT: !process.env.HCAPTCHA_SOLVER_SCRIPT || process.env.HCAPTCHA_SOLVER_SCRIPT === path.join(this.baseRoot, 'hcaptcha/solver.py')
                    ? path.join(root, 'hcaptcha/solver.py') : process.env.HCAPTCHA_SOLVER_SCRIPT }
        });
        child.runId = runId; this.child = child;
        child.once('error', error => { console.error('[OTA] 启动失败:', error.message); });
        child.on('message', message => {
            if (message?.type === 'ota:activate' && child === this.child && !this.switching && !this.stopping) {
                this.switching = true;
                this.activate().catch(error => { console.error('[OTA] 切换失败:', error.message); process.exitCode = 1; this.stop().then(() => process.exit(1)); }).finally(() => { this.switching = false; });
            }
        });
        child.once('exit', () => {
            if (!this.stopping && !this.switching && child === this.child) {
                // Let Docker restart the supervisor; interrupted switches are recovered on boot.
                process.exitCode = 1;
                setTimeout(() => process.exit(1), 100).unref();
            }
        });
        return child;
    }
    async healthy(child, expectedVersion) {
        const until = Date.now() + this.healthTimeout;
        let consecutive = 0;
        while (Date.now() < until && !this.stopping) {
            if (child.exitCode !== null || child.signalCode) throw new Error('新版本进程已退出');
            const ok = await new Promise(resolve => {
                const req = http.get(`http://127.0.0.1:${process.env.PORT || 3000}/api/public/version`, { timeout: 1500 }, res => {
                    let body = ''; res.on('data', chunk => { body += chunk; if (body.length > 8192) req.destroy(); });
                    res.on('end', () => { try { const value = JSON.parse(body); resolve(value.version === expectedVersion && value.runId === child.runId); } catch (_) { resolve(false); } });
                    res.on('error', () => resolve(false));
                });
                req.on('timeout', () => req.destroy()); req.on('error', () => resolve(false));
            });
            consecutive = ok ? consecutive + 1 : 0;
            if (consecutive >= 3) return;
            await new Promise(resolve => setTimeout(resolve, 1000));
        }
        throw new Error('新版本健康检查未通过');
    }
    async killChild() {
        const child = this.child;
        if (!child || child.exitCode !== null || child.signalCode) return;
        const signal = name => { try { if (process.platform === 'win32') child.kill(name); else process.kill(-child.pid, name); } catch (_) { /* already stopped */ } };
        await new Promise(resolve => {
            const timer = setTimeout(() => signal('SIGKILL'), 8000);
            child.once('exit', () => { clearTimeout(timer); resolve(); }); signal('SIGTERM');
        });
    }
    async activate() {
        const state = readState(this.directory);
        if (state.phase !== 'restarting' || !state.candidate) throw new Error('没有已确认的版本切换');
        const old = state.active || { directory: 'base', version: readVersion(this.baseRoot) };
        await this.killChild();
        try {
            const next = this.spawn(state.candidate);
            await this.healthy(next, state.candidate.version);
            this.patch({ active: state.candidate, previous: old, pending: null, candidate: null, phase: 'completed', message: `当前已运行 v${state.candidate.version}` });
            await this.restore();
        } catch (error) {
            await this.killChild();
            const restored = this.spawn(old);
            await this.healthy(restored, old.version);
            this.patch({ active: old, candidate: null, pending: null, phase: 'rolled_back', message: `${error.message}，已自动恢复 v${old.version}` });
            await this.restore();
        }
    }
    async start() {
        let state = readState(this.directory);
        const interrupted = ['downloading', 'preparing', 'backing_up', 'restarting'].includes(state.phase);
        if (interrupted) this.patch({ phase: 'failed', candidate: null, message: '上次更新被中断，继续运行原版本' });
        fs.rmSync(path.join(this.directory, 'operation.lock'), { recursive: true, force: true });
        state = readState(this.directory);
        this.switching = true;
        const child = this.spawn(state.active);
        await this.healthy(child, state.active?.version || readVersion(this.baseRoot));
        await this.restore();
        this.switching = false;
    }
    async stop() { this.stopping = true; await this.killChild(); }
}
module.exports = { Launcher };
if (require.main === module) {
    const baseRoot = path.resolve(__dirname, '..');
    const launcher = new Launcher({ baseRoot, restoreMaintenance: async value => {
        const store = require(path.join(baseRoot, 'mysql-store'));
        await store.setMaintenanceModeState(value.enabled, value.drain);
    } });
    for (const event of ['SIGINT', 'SIGTERM']) process.on(event, () => launcher.stop().finally(() => process.exit(0)));
    launcher.start().catch(error => { console.error('[OTA]', error.message); launcher.stop().finally(() => process.exit(1)); });
}
