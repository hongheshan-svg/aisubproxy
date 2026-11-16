'use strict';
const { EventEmitter } = require('events');
const childProcess = require('child_process');
const store = require('../mysql-store');
const pool = require('../browser-pool');
let worker, activeJobs, spawnSpy;
let output;
beforeAll(() => {
    spawnSpy = vi.spyOn(childProcess, 'spawn').mockImplementation(() => {
        const child = new EventEmitter();
        child.stdout = new EventEmitter(); child.stderr = new EventEmitter(); child.kill = vi.fn();
        queueMicrotask(() => { child.stdout.emit('data', Buffer.from(output)); child.emit('close', 0); });
        return child;
    });
    // Capture the fake subprocess function before importing the worker; never launch a payment process.
    delete require.cache[require.resolve('../server')];
    const server = require('../server');
    worker = server.spawnActivationWorker; activeJobs = server.activeForegroundJobs;
});
beforeEach(() => {
    output = 'PAYMENT_SUCCESS\n';
    vi.spyOn(console, 'log').mockImplementation(() => {});
    vi.spyOn(console, 'error').mockImplementation(() => {});
    vi.spyOn(pool, 'getRuntimeEnabled').mockReturnValue(false);
    vi.spyOn(store, 'getPaymentRegion').mockResolvedValue('PH');
    vi.spyOn(store, 'getActiveProxy').mockResolvedValue(null);
    vi.spyOn(store, 'getHcaptchaConfig').mockResolvedValue({});
    vi.spyOn(store, 'getMaintenanceModeState').mockResolvedValue({ enabled: false });
    vi.spyOn(store, 'getTelegramConfig').mockResolvedValue({ enabled: false });
    vi.spyOn(store, 'updateTaskLog').mockResolvedValue();
    vi.spyOn(store, 'resetCdkFailure').mockResolvedValue();
    vi.spyOn(store, 'markCdkUnused').mockResolvedValue();
    activeJobs.add('local-job');
});
afterEach(() => {
    for (const name of ['getPaymentRegion', 'getActiveProxy', 'getHcaptchaConfig', 'getMaintenanceModeState', 'getTelegramConfig', 'updateTaskLog', 'resetCdkFailure', 'markCdkUnused']) store[name].mockRestore();
    pool.getRuntimeEnabled.mockRestore(); console.log.mockRestore(); console.error.mockRestore();
    activeJobs.clear();
});
afterAll(() => spawnSpy.mockRestore());
async function run() {
    await worker({ task: { jobKey: 'local-job' }, token: 'fake', sessionRaw: '{}', cdk: 'local-code', cdkDetails: { plan_type: 'plus' } });
    expect(activeJobs.size).toBe(0);
}
it('retains the CDK if persisting an observed payment success fails once', async () => {
    let failed = false;
    store.updateTaskLog.mockImplementation(async (_, value) => {
        if (value.status === 'success' && !failed) { failed = true; throw new Error('temporary database failure'); }
    });
    await run();
    expect(store.markCdkUnused).not.toHaveBeenCalled();
    expect(store.updateTaskLog).toHaveBeenLastCalledWith('local-job', expect.objectContaining({ status: 'success' }));
});
it('does not downgrade success when clearing failure counters fails', async () => {
    store.resetCdkFailure.mockRejectedValue(new Error('database unavailable'));
    await run();
    expect(store.markCdkUnused).not.toHaveBeenCalled();
    expect(store.updateTaskLog).toHaveBeenLastCalledWith('local-job', expect.objectContaining({ status: 'success' }));
});
it('rolls back a failed local job only once', async () => {
    output = 'Session 无效\n';
    await run();
    expect(store.markCdkUnused).toHaveBeenCalledTimes(1);
});
it('releases capacity even when both task persistence and rollback fail', async () => {
    output = 'Session 无效\n';
    store.updateTaskLog.mockRejectedValue(new Error('database unavailable'));
    store.markCdkUnused.mockRejectedValue(new Error('database unavailable'));
    await expect(run()).rejects.toThrow('database unavailable');
    expect(activeJobs.size).toBe(0);
});
it('waits for slow progress writes before persisting the final status', async () => {
    let release, entered;
    const started = new Promise(resolve => { entered = resolve; });
    store.updateTaskLog.mockImplementation(async (_, value) => {
        if (value.status === 'running') { entered(); await new Promise(resolve => { release = resolve; }); }
    });
    const work = run();
    await started;
    expect(store.updateTaskLog.mock.calls.some(([, value]) => value.status === 'success')).toBe(false);
    release();
    await work;
    expect(store.updateTaskLog).toHaveBeenLastCalledWith('local-job', expect.objectContaining({ status: 'success' }));
});
