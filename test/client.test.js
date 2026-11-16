'use strict';
const { requestJson, createPoller } = require('../public/client');
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });
it('reports HTTP errors and never retries a POST', async () => {
    const fetch = vi.fn().mockResolvedValue({ ok: false, status: 429, json: async () => ({ message: '任务已满' }) });
    vi.stubGlobal('fetch', fetch);
    await expect(requestJson('/start', { method: 'POST' })).rejects.toMatchObject({ status: 429, message: '任务已满' });
    expect(fetch).toHaveBeenCalledTimes(1);
});
it('times out while waiting for the response body as well as headers', async () => {
    vi.useFakeTimers();
    vi.stubGlobal('fetch', vi.fn(async (_, { signal }) => ({
        ok: true, json: () => new Promise((_, reject) => signal.addEventListener('abort', () => reject(new Error('aborted'))))
    })));
    const request = requestJson('/slow', { timeoutMs: 100 });
    const assertion = expect(request).rejects.toThrow('请求超时');
    await vi.advanceTimersByTimeAsync(100);
    await assertion;
    expect(vi.getTimerCount()).toBe(0);
});
it('does not overlap polls and cancels future work on stop', async () => {
    vi.useFakeTimers();
    let finish;
    const action = vi.fn(() => new Promise(resolve => { finish = resolve; }));
    const poller = createPoller(action, { interval: 1000 });
    const first = poller.start();
    poller.refresh();
    await vi.advanceTimersByTimeAsync(10000);
    expect(action).toHaveBeenCalledTimes(1);
    poller.stop(); finish(); await first;
    await vi.advanceTimersByTimeAsync(10000);
    expect(action).toHaveBeenCalledTimes(1);
});
it('backs off errors and skips hidden or offline pages', async () => {
    vi.useFakeTimers();
    let visible = true;
    const action = vi.fn().mockRejectedValue(new Error('offline'));
    const poller = createPoller(action, { interval: 1000, shouldRun: () => visible });
    await poller.start();
    await vi.advanceTimersByTimeAsync(1999);
    expect(action).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(action).toHaveBeenCalledTimes(2);
    visible = false;
    await vi.advanceTimersByTimeAsync(8000);
    expect(action).toHaveBeenCalledTimes(2);
    poller.stop();
});
