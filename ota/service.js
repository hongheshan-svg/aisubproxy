'use strict';
const fs = require('fs');
const fsp = require('fs/promises');
const path = require('path');
const { execFile, spawn } = require('child_process');
const { promisify } = require('util');
const { pipeline } = require('stream/promises');
const zlib = require('zlib');
const { GithubReleases } = require('./github');
const { readVersion, newer, sha256, decodeBundle, readState, writeState, releaseRoot } = require('./common');
const execFileAsync = promisify(execFile);
const conflict = message => Object.assign(new Error(message), { status: 409 });

async function backupDatabase(directory) {
    await fsp.mkdir(directory, { recursive: true, mode: 0o700 });
    const file = path.join(directory, `database-${Date.now()}.sql.gz`);
    const child = spawn('mysqldump', ['--single-transaction', '--skip-lock-tables', '--no-tablespaces', '--hex-blob', '--routines', '--events',
        '--host', process.env.DB_HOST || '127.0.0.1', '--port', process.env.DB_PORT || '3306',
        '--user', process.env.DB_USER || 'root', process.env.DB_NAME || 'gpt'], {
        env: { ...process.env, MYSQL_PWD: process.env.DB_PASSWORD || '' }, stdio: ['ignore', 'pipe', 'pipe']
    });
    let size = 0;
    child.stdout.on('data', chunk => { size += chunk.length; if (size > 512 * 1024 * 1024) child.kill('SIGKILL'); });
    child.stderr.resume();
    const timer = setTimeout(() => child.kill('SIGKILL'), 120000);
    try {
        const exited = new Promise((resolve, reject) => {
            child.once('error', () => reject(new Error('无法启动数据库备份，请检查 mysqldump 是否安装')));
            child.once('close', code => code === 0 ? resolve() : reject(new Error('数据库备份失败，已取消版本切换')));
        });
        await Promise.all([exited, pipeline(child.stdout, zlib.createGzip(), fs.createWriteStream(file, { mode: 0o600, flags: 'wx' }))]);
        return file;
    } catch (error) { child.kill('SIGKILL'); await fsp.rm(file, { force: true }); throw error; }
    finally { clearTimeout(timer); }
}

class UpdateService {
    constructor({ root = path.resolve(__dirname, '..'), baseRoot = process.env.OTA_BASE_DIR || root,
        directory = process.env.OTA_DATA_DIR || path.join(baseRoot, 'data', 'ota'), github = new GithubReleases(),
        store, activeJobs = () => 0, backup = backupDatabase, restart = () => process.send?.({ type: 'ota:activate' }),
        managed = process.env.OTA_MANAGED === '1' && Boolean(process.send) } = {}) {
        Object.assign(this, { root, baseRoot, directory, github, store, activeJobs, backup, restart, managed });
        this.currentVersion = readVersion(root); this.busy = false; this.gate = false; this.cached = null; this.checking = null;
    }
    status() {
        const state = readState(this.directory);
        return { version: this.currentVersion, managed: this.managed, repo: this.github.repo,
            latest: this.cached?.release || null, checkedAt: this.cached?.time || null, warning: this.cached?.warning || '',
            hasUpdate: Boolean(this.cached?.release && newer(this.cached.release.version, this.currentVersion)),
            state: { phase: state.phase, message: state.message || '', updatedAt: state.updatedAt,
                pendingVersion: state.pending?.version || null, previousVersion: state.previous?.version || null,
                backupCreated: Boolean(state.backup), candidateVersion: state.candidate?.version || null } };
    }
    async check(force = false) {
        const age = this.cached ? Date.now() - this.cached.time : Infinity;
        if (age < (force ? 30000 : 20 * 60 * 1000)) return this.status();
        if (!this.checking) this.checking = (async () => {
            try { this.cached = { release: await this.github.latest(), time: Date.now(), warning: '' }; }
            catch (error) { this.cached = { release: null, time: Date.now(), warning: error.message }; }
            return this.status();
        })().finally(() => { this.checking = null; });
        return this.checking;
    }
    acquire() {
        if (this.busy) throw conflict('已有更新操作正在进行');
        if (this.gate) throw conflict('版本切换尚未完成，请检查维护状态后重启服务');
        const state = readState(this.directory);
        if (['backing_up', 'restarting'].includes(state.phase)) throw conflict('版本切换正在进行');
        fs.mkdirSync(this.directory, { recursive: true, mode: 0o700 });
        try { fs.mkdirSync(path.join(this.directory, 'operation.lock')); }
        catch (error) { if (error.code === 'EEXIST') throw conflict('已有更新操作正在进行'); throw error; }
        this.busy = true;
    }
    release() { fs.rmSync(path.join(this.directory, 'operation.lock'), { recursive: true, force: true }); this.busy = false; }
    patch(values) { writeState(this.directory, { ...readState(this.directory), ...values }); }
    startUpdate(requestedVersion) {
        if (!this.managed) throw conflict('请使用 OTA 启动器运行服务后再执行在线更新');
        if (readState(this.directory).pending) throw conflict('已有待应用版本，请先应用或回滚');
        this.acquire();
        this.patch({ phase: 'downloading', message: '正在检查并下载更新包' });
        this.operation = this.prepare(requestedVersion).catch(error => this.patch({ phase: 'failed', message: error.message })).finally(() => this.release());
        return this.status();
    }
    async prepare(requestedVersion) {
        const release = await this.github.latest();
        if (!newer(release.version, this.currentVersion) || release.version !== requestedVersion) throw conflict('版本已变化或没有可用更新，请重新检查');
        const { bytes, digest } = await this.github.download(release);
        if (sha256(bytes) !== digest) throw new Error('更新包 SHA-256 校验失败，已拒绝安装');
        const bundle = decodeBundle(bytes, release.version, this.baseRoot);
        this.patch({ phase: 'preparing', message: '校验通过，正在准备新版本' });
        const releases = path.join(this.directory, 'releases');
        await fsp.mkdir(releases, { recursive: true, mode: 0o700 });
        const staging = await fsp.mkdtemp(path.join(releases, '.prepare-'));
        try {
            for (const [name, data] of bundle.files) {
                const file = path.join(staging, name);
                await fsp.mkdir(path.dirname(file), { recursive: true });
                await fsp.writeFile(file, data, { mode: name.endsWith('.sh') ? 0o755 : 0o644 });
            }
            for (const name of ['node_modules', 'data', 'debug_screenshots', 'product_files']) {
                await fsp.symlink(path.join(this.baseRoot, name), path.join(staging, name), 'dir');
            }
            for (const name of bundle.files.keys()) if (name.endsWith('.js') && !name.startsWith('public/')) {
                await execFileAsync(process.execPath, ['--check', path.join(staging, name)], { timeout: 10000 });
            }
            const entry = { version: bundle.version, directory: `${bundle.version}-${digest.slice(0, 16)}` };
            const destination = path.join(releases, entry.directory);
            // Immutable release directories are never overwritten.
            try { await fsp.rename(staging, destination); }
            catch (error) { if (error.code !== 'ENOTEMPTY' && error.code !== 'EEXIST') throw error; releaseRoot(this.baseRoot, this.directory, entry); }
            this.patch({ phase: 'ready', pending: entry, message: `v${bundle.version} 已准备好，重启后生效` });
        } finally { await fsp.rm(staging, { recursive: true, force: true }); }
    }
    startSwitch(kind = 'update') {
        if (!this.managed) throw conflict('当前启动方式不支持在线重启');
        const state = readState(this.directory);
        const target = kind === 'rollback' ? state.previous : state.pending;
        if (!target) throw conflict(kind === 'rollback' ? '没有可回滚的版本' : '没有待应用版本');
        releaseRoot(this.baseRoot, this.directory, target);
        this.acquire();
        this.gate = true; // Also checked immediately before activation reserves capacity.
        this.operation = (async () => {
            let maintenance;
            try {
                if (this.activeJobs() > 0) throw conflict('有开通任务或后台操作正在运行，请完成后再切换版本');
                const rows = await this.store.runQuery("SELECT COUNT(*) AS n FROM task_logs WHERE status = 'running'");
                if (Number(rows[0].n) > 0 || this.activeJobs() > 0) throw conflict('有开通任务或后台操作正在运行，请完成后再切换版本');
                maintenance = await this.store.getMaintenanceModeState();
                this.patch({ phase: 'backing_up', message: '正在备份数据库', restoreMaintenance: maintenance });
                await this.store.setMaintenanceModeState(true, true);
                const backup = await this.backup(path.join(this.directory, 'backups'));
                this.patch({ phase: 'restarting', candidate: target, switchKind: kind, backup, message: '正在切换版本并检查服务健康' });
                this.restart();
            } catch (error) {
                if (maintenance) {
                    try { await this.store.setMaintenanceModeState(maintenance.enabled, maintenance.drain); }
                    catch (_) {
                        this.patch({ phase: 'failed', candidate: null, message: `${error.message}；维护状态恢复失败，请检查数据库后重启服务` });
                        return;
                    }
                }
                this.gate = false;
                this.patch({ phase: 'failed', restoreMaintenance: null, candidate: null, message: error.message });
            } finally { this.release(); }
        })();
        return this.status();
    }
}
module.exports = { UpdateService, backupDatabase };
