'use strict';
const store = require('../mysql-store');
const api = require('../gpt-api-client');
const { handleActivationRequest, runGptApiWorker, activeForegroundJobs, updater } = require('../server');

function request(cdk = 'test-code') {
    const encode = data => Buffer.from(JSON.stringify(data)).toString('base64url');
    const token = [encode({ typ: 'JWT', alg: 'RS256' }), encode({
        exp: Math.floor(Date.now() / 1000) + 3600, iss: 'https://auth.openai.com',
        aud: ['https://api.openai.com/v1'], scp: ['model.request'],
        'https://api.openai.com/auth': { chatgpt_account_id: 'test', chatgpt_user_id: 'test' }
    }), 'fake-signature'].join('.');
    return { body: { cdk, session: JSON.stringify({ accessToken: token }) }, headers: {}, socket: {}, path: '/api/run-process' };
}
function response() {
    return { code: 200, status(code) { this.code = code; return this; }, json(body) { this.body = body; return this; } };
}
const cfg = { enabled: true, api_key: 'test-only', country: 'PH' };
beforeEach(() => {
    updater.gate = false;
    activeForegroundJobs.clear();
    vi.spyOn(console, 'log').mockImplementation(() => {});
    vi.spyOn(console, 'error').mockImplementation(() => {});
    vi.spyOn(store, 'ensureReady').mockResolvedValue();
    vi.spyOn(store, 'getMaintenanceModeState').mockResolvedValue({ enabled: false });
    vi.spyOn(store, 'getMaxConcurrentActivations').mockResolvedValue(1);
    vi.spyOn(store, 'verifyCdkDetails').mockResolvedValue({ type: '自助', plan_type: 'plus' });
    vi.spyOn(store, 'getRunningTaskByCdk').mockResolvedValue(null);
    vi.spyOn(store, 'getActivationAttemptLimit').mockResolvedValue(null);
    vi.spyOn(store, 'getGptApiConfig').mockResolvedValue(cfg);
    vi.spyOn(store, 'createActivationTask').mockResolvedValue(null);
    vi.spyOn(store, 'markCdkUnused').mockResolvedValue();
    vi.spyOn(store, 'getTelegramConfig').mockResolvedValue({ enabled: false });
});
afterEach(() => { updater.gate = false; vi.restoreAllMocks(); vi.useRealTimers(); vi.unstubAllEnvs(); activeForegroundJobs.clear(); });

describe('activation admission', () => {
    it('does not consume a Pro 500 CDK if the provider has no enabled matching plan', async () => {
        store.verifyCdkDetails.mockResolvedValue({ type: '自助', plan_type: 'pro_500' });
        vi.spyOn(api, 'fetchPlans').mockResolvedValue({ success: true, plans: [{ key: 'pro20x', plan_name: 'chatgptpro200', enabled: 1 }] });
        const res = response(); await handleActivationRequest(request(), res);
        expect(res.code).toBe(503); expect(store.createActivationTask).not.toHaveBeenCalled();
        expect(store.markCdkUnused).not.toHaveBeenCalled(); expect(activeForegroundJobs.size).toBe(0);
    });
    it('rejects new admissions while OTA is switching versions', async () => {
        updater.gate = true;
        const res = response(); await handleActivationRequest(request(), res);
        expect(res.code).toBe(503); expect(store.createActivationTask).not.toHaveBeenCalled();
    });
    it('rechecks OTA after asynchronous preflight before reserving a slot', async () => {
        store.getMaxConcurrentActivations.mockImplementation(async () => { updater.gate = true; return 10; });
        const res = response(); await handleActivationRequest(request(), res);
        expect(res.code).toBe(503); expect(activeForegroundJobs.size).toBe(0);
        expect(store.createActivationTask).not.toHaveBeenCalled();
    });
    it('reserves capacity before a slow database insert completes', async () => {
        let finish, entered;
        const writing = new Promise(resolve => { entered = resolve; });
        store.createActivationTask.mockImplementation(() => { entered(); return new Promise(resolve => { finish = resolve; }); });
        const first = response(), second = response();
        const pending = handleActivationRequest(request('first'), first);
        await writing;
        expect(activeForegroundJobs.size).toBe(1);
        await handleActivationRequest(request('second'), second);
        expect(second.code).toBe(429);
        expect(store.createActivationTask).toHaveBeenCalledTimes(1);
        finish(null);
        await pending;
        expect(activeForegroundJobs.size).toBe(0);
    });
    it('recovers an existing job even at full capacity', async () => {
        activeForegroundJobs.add('running');
        store.getRunningTaskByCdk.mockResolvedValue({ job_key: 'running' });
        const res = response();
        await handleActivationRequest(request(), res);
        expect(res.body).toMatchObject({ success: true, jobKey: 'running' });
        expect(store.createActivationTask).not.toHaveBeenCalled();
    });
    it('never unlocks a code when a preflight query fails', async () => {
        store.verifyCdkDetails.mockRejectedValue(new Error('database unavailable'));
        const res = response();
        await handleActivationRequest(request(), res);
        expect(res.code).toBe(500);
        expect(store.markCdkUnused).not.toHaveBeenCalled();
    });
    it('releases the provisional slot on a failed transaction', async () => {
        store.createActivationTask.mockRejectedValue(new Error('insert failed'));
        await handleActivationRequest(request(), response());
        expect(activeForegroundJobs.size).toBe(0);
        expect(store.markCdkUnused).not.toHaveBeenCalled();
    });
    it('returns a concurrently created task without starting another worker', async () => {
        store.createActivationTask.mockResolvedValue({ jobKey: 'existing', existing: true });
        const inspect = vi.spyOn(api, 'inspectPay');
        const res = response();
        await handleActivationRequest(request(), res);
        expect(res.body.jobKey).toBe('existing');
        expect(inspect).not.toHaveBeenCalled();
        expect(activeForegroundJobs.size).toBe(0);
    });
});

describe('upstream outcome handling', () => {
    beforeEach(() => {
        vi.useFakeTimers();
        vi.stubEnv('GPT_API_MAX_POLLS', '1');
        vi.spyOn(store, 'updateTaskLog').mockResolvedValue();
        vi.spyOn(store, 'getActiveProxy').mockResolvedValue(null);
        vi.spyOn(store, 'reserveCard').mockResolvedValue({ id: 1, card_number: '4242424242424242', card_expiry: '12/30', card_cvc: '123' });
        vi.spyOn(store, 'releaseCard').mockResolvedValue();
        vi.spyOn(store, 'recordCardUsage').mockResolvedValue();
        vi.spyOn(store, 'resetCdkFailure').mockResolvedValue();
        vi.spyOn(api, 'inspectPay').mockResolvedValue({ success: true });
        vi.spyOn(api, 'submitPay').mockResolvedValue({ success: true, orderId: 12, taskId: 34, data: {} });
        vi.spyOn(api, 'queryTask').mockResolvedValue({ success: true, rawStatus: 'running', data: {} });
        vi.spyOn(api, 'queryOrder').mockResolvedValue({ success: true, rawStatus: 'running', data: {} });
    });
    async function run() {
        activeForegroundJobs.add('job');
        const work = runGptApiWorker({ task: { jobKey: 'job' }, token: 'test', cdk: 'code', planType: 'plus' });
        await vi.runAllTimersAsync();
        await work;
        expect(activeForegroundJobs.size).toBe(0);
    }
    it('keeps the code reserved when submission times out', async () => {
        api.submitPay.mockResolvedValue({ success: false, error: '请求超时' });
        await run();
        expect(store.updateTaskLog).toHaveBeenLastCalledWith('job', expect.objectContaining({ status: 'manual' }));
        expect(store.markCdkUnused).not.toHaveBeenCalled();
        expect(api.submitPay).toHaveBeenCalledTimes(1);
    });
    it('keeps the code reserved when polling reaches its limit', async () => {
        await run();
        expect(store.updateTaskLog).toHaveBeenLastCalledWith('job', expect.objectContaining({ status: 'manual' }));
        expect(store.markCdkUnused).not.toHaveBeenCalled();
    });
    it('rolls back a definitively rejected submission', async () => {
        api.submitPay.mockResolvedValue({ success: false, status: 422, error: 'invalid_session' });
        await run();
        expect(store.markCdkUnused).toHaveBeenCalledWith('code');
        expect(store.updateTaskLog).toHaveBeenLastCalledWith('job', expect.objectContaining({ status: 'failed' }));
    });
    it('does not turn a confirmed success into failure when accounting fails', async () => {
        api.queryTask.mockResolvedValue({ success: true, rawStatus: 'success', data: { result: { ok: true } } });
        store.recordCardUsage.mockRejectedValue(new Error('accounting unavailable'));
        await run();
        expect(store.updateTaskLog).toHaveBeenLastCalledWith('job', expect.objectContaining({ status: 'success' }));
        expect(store.markCdkUnused).not.toHaveBeenCalled();
    });
    it('does not use a task ID as an order ID', async () => {
        api.submitPay.mockResolvedValue({ success: true, taskId: 34, data: {} });
        api.queryTask.mockResolvedValue({ success: false, error: 'temporary network failure' });
        await run();
        expect(api.queryOrder).not.toHaveBeenCalled();
        expect(store.markCdkUnused).not.toHaveBeenCalled();
    });
});
