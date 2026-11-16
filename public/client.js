/* Shared network primitives. No automatic retries of mutations. */
(function (root) {
    'use strict';
    async function requestJson(url, options = {}) {
        const { timeoutMs = 20000, ...init } = options;
        const controller = new AbortController();
        const abort = () => controller.abort();
        const timer = setTimeout(abort, timeoutMs);
        init.signal?.addEventListener('abort', abort, { once: true });
        if (init.signal?.aborted) abort();
        try {
            const response = await fetch(url, { ...init, signal: controller.signal, cache: 'no-store' });
            let data;
            try { data = await response.json(); }
            catch (error) {
                if (controller.signal.aborted) throw error;
                throw new Error('服务响应异常，请稍后重试');
            }
            if (!response.ok) {
                const error = new Error(data.message || `请求失败（${response.status}）`);
                error.status = response.status;
                throw error;
            }
            return data;
        } catch (error) {
            if (controller.signal.aborted) throw new Error('请求超时，请检查网络后重试');
            throw error;
        } finally {
            clearTimeout(timer);
            init.signal?.removeEventListener('abort', abort);
        }
    }

    // Schedule after completion, so slow connections never accumulate parallel requests.
    function createPoller(action, { interval = 10000, maxInterval = 60000, shouldRun = () => true } = {}) {
        let timer = null, running = false, stopped = true, failures = 0;
        async function tick() {
            if (stopped || running) return;
            clearTimeout(timer);
            running = true;
            try {
                if (shouldRun()) { await action(); failures = 0; }
            } catch (_) { failures = Math.min(failures + 1, 6); }
            finally {
                running = false;
                if (!stopped) {
                    const base = typeof interval === 'function' ? interval() : interval;
                    timer = setTimeout(tick, Math.min(maxInterval, base * 2 ** failures));
                }
            }
        }
        return {
            start() { stopped = false; return tick(); },
            stop() { stopped = true; clearTimeout(timer); },
            refresh() { return tick(); }
        };
    }
    const api = { requestJson, createPoller };
    if (typeof module !== 'undefined' && module.exports) module.exports = api;
    else root.AppClient = api;
})(typeof window !== 'undefined' ? window : globalThis);
