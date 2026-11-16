'use strict';
(() => {
    const $ = id => document.getElementById(id);
    const { requestJson, createPoller } = window.AppClient;
    const visible = () => !document.hidden && navigator.onLine;
    const PLAN_LABELS = { plus: 'ChatGPT Plus', pro_5x: 'ChatGPT Pro 5x', pro_20x: 'ChatGPT Pro 20x' };
    const STORAGE_KEY = 'kc.pendingActivation';
    const terminal = new Set(['success', 'failed', 'manual', 'maintenance']);
    let verifiedCdk = '', jobKey = '', busy = false, completed = false, uncertain = false;
    let socket = null, reconnectTimer = null, heartbeatTimer = null, reconnectCount = 0, lastMessageAt = 0;
    let progressValue = 0, validationTimer = null;

    function remember() {
        try { sessionStorage.setItem(STORAGE_KEY, JSON.stringify({ cdk: verifiedCdk, jobKey })); } catch (_) { }
    }
    function forget() { try { sessionStorage.removeItem(STORAGE_KEY); } catch (_) { } }
    function status(message, kind = 'info') {
        $('statusMessage').textContent = message;
        $('statusMessage').className = `status-message status-${kind}`;
        $('statusMessage').style.display = 'block';
    }
    function loading(button, value, text) {
        button.disabled = value;
        button.setAttribute('aria-busy', String(value));
        button.querySelector('.loader').style.display = value ? 'block' : 'none';
        if (text) button.querySelector('.btn-text').textContent = text;
    }
    function lock(value) {
        busy = value;
        $('tokenInput').disabled = value;
        $('backToStep1').disabled = value;
        document.querySelectorAll('.tab-btn').forEach(button => { button.disabled = value; });
        loading($('startBtn'), value, value ? '正在处理，请稍候' : '确认账号并开通');
    }
    function flow(step, state) {
        const labels = { pending: '等待兑换', cdk_ok: '确认账号', running: '正在开通', success: '开通完成', failed: '未完成', manual: '待核实', maintenance: '维护中' };
        $('flowStatusBadge').textContent = labels[state] || labels.pending;
        $('flowStatusBadge').className = `flow-badge ${state}`;
        document.querySelectorAll('.step-item').forEach(item => {
            const n = Number(item.dataset.step);
            item.classList.toggle('active', n === step);
            item.classList.toggle('done', n < step || state === 'success');
            if (n === step) item.setAttribute('aria-current', 'step');
            else item.removeAttribute('aria-current');
        });
    }
    function progress(value, message, state) {
        if (Number.isFinite(Number(value))) progressValue = Math.max(progressValue, Math.min(100, Number(value)));
        $('taskProgress').style.display = 'block';
        $('taskProgressBar').style.width = `${progressValue}%`;
        $('taskProgressBar').classList.toggle('error', state === 'failed');
        $('taskProgressPercent').textContent = `${Math.round(progressValue)}%`;
        $('taskProgressText').textContent = message || '正在处理…';
        document.querySelector('[role="progressbar"]').setAttribute('aria-valuenow', String(progressValue));
    }
    function switchTab(tab) {
        document.querySelectorAll('.tab-btn').forEach(button => {
            const selected = button.dataset.tab === tab;
            button.classList.toggle('active', selected);
            button.setAttribute('aria-pressed', String(selected));
        });
        $('redeemSection').style.display = tab === 'redeem' ? 'block' : 'none';
        $('querySection').style.display = tab === 'query' ? 'block' : 'none';
        $('statusMessage').style.display = 'none';
        if (tab === 'query') {
            $('queryInput').value = verifiedCdk || $('cdkInput').value;
            $('queryInput').focus();
        }
    }
    function showAccountStep(plan) {
        $('step1').style.display = 'none';
        $('step2').style.display = 'block';
        $('redeemCdkInfo').textContent = `已验证兑换码　${verifiedCdk}`;
        $('redeemCdkInfo').style.display = 'block';
        $('redeemPlanInfo').textContent = plan ? `开通套餐　${plan}` : '兑换任务已创建，可在下方查看进度';
        $('redeemPlanInfo').style.display = 'block';
    }
    function parseSession() {
        const value = $('tokenInput').value.trim();
        if (!value) throw new Error('请先粘贴完整 Session JSON');
        let session;
        try { session = JSON.parse(value); } catch (_) { throw new Error('Session 格式不完整，请重新复制完整 JSON'); }
        const token = session?.accessToken || session?.access_token;
        if (typeof token !== 'string' || token.split('.').length !== 3) throw new Error('Session 缺少有效的 accessToken，请重新获取');
        let payload;
        try { payload = JSON.parse(atob(token.split('.')[1].replace(/-/g, '+').replace(/_/g, '/'))); }
        catch (_) { throw new Error('Session 无法解析，请重新获取'); }
        if (!Number.isFinite(Number(payload.exp)) || Number(payload.exp) * 1000 <= Date.now()) throw new Error('Session 已过期，请重新获取');
        return { raw: value, email: session.user?.email || '' };
    }
    function stopSocket() {
        clearTimeout(reconnectTimer);
        clearInterval(heartbeatTimer);
        if (socket) {
            const old = socket;
            socket = null;
            old.onclose = old.onmessage = old.onerror = old.onopen = null;
            old.close();
        }
    }
    function applyTask(task) {
        if (!task || completed || (task.jobKey && task.jobKey !== jobKey)) return;
        const state = task.status || 'running';
        flow(3, state);
        progress(state === 'success' ? 100 : task.progress, task.message, state);
        if (!terminal.has(state)) return;
        completed = true;
        taskPoller.stop();
        stopSocket();
        forget();
        lock(false);
        $('tokenInput').value = '';
        const isSuccess = state === 'success';
        status(task.message || (isSuccess ? '订阅已开通' : '处理未完成，请查询状态或联系管理员'), isSuccess ? 'success' : state === 'manual' ? 'info' : 'error');
        $('startBtn').querySelector('.btn-text').textContent = isSuccess ? '开通完成' : state === 'manual' ? '等待管理员核实' : '重新确认并开通';
        $('startBtn').disabled = isSuccess || state === 'manual';
        runtimePoller.refresh();
    }
    function connectSocket() {
        if (!jobKey || completed || !navigator.onLine) return;
        if (socket && [WebSocket.OPEN, WebSocket.CONNECTING].includes(socket.readyState)) return;
        const current = new WebSocket(`${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}`);
        socket = current;
        // A stalled handshake also needs a deadline.
        lastMessageAt = Date.now();
        clearInterval(heartbeatTimer);
        heartbeatTimer = setInterval(() => {
            if (socket !== current) return;
            if (Date.now() - lastMessageAt >= 45000) { current.close(); return; }
            if (current.readyState === WebSocket.OPEN) current.send(JSON.stringify({ type: 'ping', ts: Date.now() }));
        }, 15000);
        current.onopen = () => {
            if (socket !== current) return;
            reconnectCount = 0;
            current.send(JSON.stringify({ type: 'subscribe', jobKey }));
        };
        current.onmessage = event => {
            if (socket !== current) return;
            lastMessageAt = Date.now();
            try {
                const data = JSON.parse(event.data);
                if (['snapshot', 'progress', 'status'].includes(data.type)) applyTask(data);
            } catch (_) { /* HTTP polling still verifies the persisted state. */ }
        };
        current.onclose = () => {
            if (socket !== current) return;
            socket = null;
            clearInterval(heartbeatTimer);
            if (completed || !jobKey) return;
            $('taskProgressText').textContent = '实时连接中断，正在通过状态查询同步；无需重复提交。';
            taskPoller.refresh();
            reconnectTimer = setTimeout(connectSocket, Math.min(30000, 1000 * 2 ** Math.min(reconnectCount++, 5)));
        };
        current.onerror = () => { current.close(); };
    }
    async function queryCode(cdk) {
        const result = await requestJson(`/api/cdk/query?cdk=${encodeURIComponent(cdk)}`);
        if (!result.success) throw new Error(result.message || '查询失败');
        return result.data;
    }
    const taskPoller = createPoller(async () => {
        if (!jobKey || completed) return;
        const expected = jobKey;
        try {
            const data = await queryCode(verifiedCdk);
            if (jobKey === expected && data.task?.jobKey === expected) applyTask(data.task);
        } catch (error) {
            if (jobKey === expected && !completed) $('taskProgressText').textContent = '暂时无法同步进度，将自动重试。任务可能仍在处理，请勿重复提交。';
            throw error;
        }
    }, { interval: () => socket?.readyState === WebSocket.OPEN ? 20000 : 5000, shouldRun: visible });
    function track(key, snapshot) {
        stopSocket();
        jobKey = key;
        uncertain = false;
        completed = false;
        remember();
        switchTab('redeem');
        showAccountStep();
        lock(true);
        flow(3, 'running');
        progress(3, '正在同步任务进度…');
        if (snapshot) applyTask(snapshot);
        if (!completed) { taskPoller.start(); connectSocket(); }
    }
    async function recover() {
        const data = await queryCode(verifiedCdk);
        if (data.task) { track(data.task.jobKey, data.task); return; }
        uncertain = false;
        forget();
        lock(false);
        flow(2, 'cdk_ok');
        status('尚未查询到任务。请稍后再次查询，确认没有处理中订单后再提交。');
    }
    $('redeemBtn').addEventListener('click', async () => {
        const cdk = $('cdkInput').value.trim();
        if (!cdk) { status('请输入兑换码', 'error'); $('cdkInput').focus(); return; }
        if ($('redeemBtn').disabled) return;
        loading($('redeemBtn'), true, '正在验证…');
        try {
            const result = await requestJson('/api/verify-cdk', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ cdk }) });
            if (!result.success) throw new Error(result.message || '兑换码无效');
            verifiedCdk = cdk;
            if (result.data?.jobKey) { track(result.data.jobKey); return; }
            showAccountStep(PLAN_LABELS[result.data?.plan_type] || PLAN_LABELS.plus);
            flow(2, 'cdk_ok');
            $('statusMessage').style.display = 'none';
            $('tokenInput').focus();
        } catch (error) { status(error.message || '验证失败，请检查网络', 'error'); }
        finally { loading($('redeemBtn'), false, '验证兑换码'); }
    });
    $('startBtn').addEventListener('click', async () => {
        if (busy) return;
        if (uncertain) {
            loading($('startBtn'), true, '正在查询…');
            try { await recover(); } catch (error) { status(error.message, 'error'); }
            finally { if (uncertain) loading($('startBtn'), false, '查询提交结果'); }
            return;
        }
        clearTimeout(validationTimer);
        let session;
        try { session = parseSession(); } catch (error) { status(error.message, 'error'); return; }
        if (!verifiedCdk) { status('请先验证兑换码', 'error'); return; }
        completed = false;
        progressValue = 0;
        lock(true);
        flow(3, 'running');
        progress(3, '正在提交开通请求…');
        uncertain = true;
        jobKey = '';
        remember();
        try {
            const result = await requestJson('/api/run-process', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ session: session.raw, cdk: verifiedCdk }) });
            if (!result.success || !result.jobKey) throw new Error(result.message || '未能确认任务是否创建');
            $('tokenInput').value = '';
            track(result.jobKey);
        } catch (error) {
            lock(false);
            if (error.status >= 400 && error.status < 500) {
                uncertain = false;
                forget();
                flow(2, 'failed');
                status(error.message, 'error');
            } else {
                status('提交结果暂未确认，正在查询已有任务，请勿重复提交。');
                try { await recover(); }
                catch (_) {
                    loading($('startBtn'), false, '查询提交结果');
                    status('网络暂不可用。恢复连接后点击「查询提交结果」，或刷新本页继续查询。');
                }
            }
        }
    });
    $('queryBtn').addEventListener('click', async () => {
        const cdk = $('queryInput').value.trim();
        if (!cdk) { status('请输入兑换码', 'error'); return; }
        if ($('queryBtn').disabled) return;
        loading($('queryBtn'), true, '查询中…');
        try {
            const data = await queryCode(cdk);
            if (data.task) { verifiedCdk = cdk; progressValue = 0; track(data.task.jobKey, data.task); }
            else status(`兑换码状态：${data.status}${data.usedAt ? `，使用时间：${data.usedAt}` : ''}`, 'info');
        } catch (error) { status(error.message, 'error'); }
        finally { loading($('queryBtn'), false, '查询状态'); }
    });
    $('backToStep1').addEventListener('click', () => {
        if (busy) return;
        clearTimeout(validationTimer);
        stopSocket(); taskPoller.stop(); forget();
        jobKey = verifiedCdk = ''; completed = uncertain = false; progressValue = 0;
        lock(false);
        $('tokenInput').value = '';
        $('step1').style.display = 'block'; $('step2').style.display = 'none';
        $('taskProgress').style.display = $('statusMessage').style.display = 'none';
        flow(1, 'pending'); $('cdkInput').focus();
    });
    $('tokenInput').addEventListener('input', () => {
        clearTimeout(validationTimer);
        validationTimer = setTimeout(() => {
            if (busy) return;
            try { const session = parseSession(); status(session.email ? `即将为 ${session.email} 开通，请确认账号。` : 'Session 格式有效，请确认这是要开通的账号。', 'success'); }
            catch (error) { status(error.message, 'error'); }
        }, 250);
    });
    document.querySelectorAll('.tab-btn').forEach(button => button.addEventListener('click', () => { if (!busy) switchTab(button.dataset.tab); }));
    [['cdkInput', 'redeemBtn'], ['queryInput', 'queryBtn']].forEach(([input, button]) => $(input).addEventListener('keydown', event => { if (event.key === 'Enter') $(button).click(); }));

    const runtimePoller = createPoller(async () => {
        try {
            const result = await requestJson('/api/public/runtime');
            if (!result.success) throw new Error('runtime unavailable');
            const active = Math.max(0, Number(result.runtime?.active_foreground_jobs) || 0);
            const max = Math.max(1, Number(result.runtime?.max_foreground_jobs) || 1);
            const full = active >= max;
            const maintenance = result.runtime?.maintenance;
            $('publicForegroundSlots').textContent = `${active} / ${max}`;
            $('publicForegroundSlotsMeta').textContent = maintenance ? '系统维护中，暂不接受新任务。' : full ? '当前繁忙，请稍后提交；已提交任务继续处理。' : '服务可用，可以提交兑换。';
            $('publicRuntimeCard').className = `public-runtime-panel ${maintenance || full ? 'load-high' : 'load-low'}`;
            $('publicForegroundSlotGrid').replaceChildren();
            for (let i = 0; i < Math.min(max, 24); i++) {
                const slot = document.createElement('span');
                slot.className = `public-slot-box ${i / Math.min(max, 24) < active / max ? 'occupied' : 'idle'}`;
                $('publicForegroundSlotGrid').append(slot);
            }
        } catch (error) {
            $('publicForegroundSlots').textContent = '—';
            $('publicForegroundSlotsMeta').textContent = '连接暂不可用，正在自动重试。';
            $('publicRuntimeCard').className = 'public-runtime-panel';
            throw error;
        }
    }, { interval: 15000, shouldRun: visible });
    function wake() {
        if (!visible()) return;
        runtimePoller.refresh(); taskPoller.refresh(); connectSocket();
    }
    document.addEventListener('visibilitychange', wake);
    window.addEventListener('online', wake);
    window.addEventListener('offline', () => {
        $('publicForegroundSlotsMeta').textContent = '网络已断开，恢复后将自动同步。';
        if (busy) $('taskProgressText').textContent = '网络已断开，任务在服务器上继续处理。';
    });
    window.addEventListener('pagehide', () => { stopSocket(); taskPoller.stop(); runtimePoller.stop(); });
    window.addEventListener('pageshow', event => { if (event.persisted) { runtimePoller.start(); if (jobKey && !completed) taskPoller.start(); wake(); } });
    flow(1, 'pending');
    runtimePoller.start();
    try {
        const saved = JSON.parse(sessionStorage.getItem(STORAGE_KEY));
        if (saved?.cdk) {
            verifiedCdk = saved.cdk;
            $('cdkInput').value = verifiedCdk;
            if (saved.jobKey) track(saved.jobKey);
            else {
                uncertain = true;
                showAccountStep();
                recover().catch(() => { loading($('startBtn'), false, '查询提交结果'); status('发现上次提交记录，请查询结果后继续。'); });
            }
        }
    } catch (_) { }
})();
