(() => {
    'use strict';
    const badges = [...document.querySelectorAll('[data-ota-toggle]')];
    const panel = document.getElementById('otaPanel');
    const el = name => document.getElementById('ota' + name);
    let status = null, timer, requesting = false, anchor, waitingUntil = 0, versionBefore = null;
    const activePhases = ['downloading', 'preparing', 'backing_up', 'restarting'];
    async function api(route, body) {
        const response = await authFetch('/api/admin/system/' + route, { method: body ? 'POST' : 'GET',
            ...(body ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}), signal: AbortSignal.timeout(18000) });
        const data = await response.json();
        if (!response.ok || !data.success) throw new Error(data.message || '请求失败，请重试');
        return data;
    }
    function message(text, error = false) { el('Message').textContent = text; el('Message').dataset.error = String(error); }
    function render(data) {
        status = data;
        const state = data.state, busy = requesting || activePhases.includes(state.phase);
        badges.forEach(node => {
            node.querySelector('[data-app-version]').textContent = 'v' + data.version;
            node.classList.toggle('has-update', data.hasUpdate || Boolean(state.pendingVersion));
            node.setAttribute('aria-label', `当前版本 v${data.version}，查看系统更新${data.hasUpdate ? '，有可用更新' : ''}`);
        });
        el('Current').textContent = 'v' + data.version;
        el('Latest').textContent = data.latest ? 'v' + data.latest.version : '待检查';
        const fallback = data.hasUpdate ? '发现新版本，可下载并校验更新包。' : data.latest ? '当前已是最新版本。' : '点击检查更新，获取最新版本。';
        const operationMessage = activePhases.includes(state.phase) || state.pendingVersion || ['failed', 'rolled_back'].includes(state.phase);
        message(operationMessage ? state.message || fallback : data.warning || fallback, ['failed', 'rolled_back'].includes(state.phase) || Boolean(data.warning && !operationMessage));
        el('NotesSection').hidden = !data.latest;
        el('Notes').textContent = data.latest?.notes || '此版本没有提供更新说明。';
        if (data.latest) el('ReleaseLink').href = data.latest.url;
        el('Check').disabled = busy;
        el('Download').hidden = !data.hasUpdate || Boolean(state.pendingVersion);
        el('Download').disabled = busy || !data.managed || !data.latest?.asset || !data.latest?.checksum;
        el('Apply').hidden = !state.pendingVersion;
        el('Apply').textContent = '重启并应用 v' + state.pendingVersion;
        el('Apply').disabled = busy || !data.managed;
        el('Rollback').hidden = !state.previousVersion;
        el('Rollback').textContent = '回滚至 v' + state.previousVersion;
        el('Rollback').disabled = busy || !data.managed;
        el('Hint').textContent = !data.managed ? '当前服务未使用 OTA 启动器，在线更新不可用。' : data.hasUpdate && !data.latest?.asset ? '该版本尚未提供 OTA 更新包。' : '切换前自动备份数据库；有开通任务运行时暂停切换。重启期间页面会短暂断开。';
    }
    function schedule() { clearTimeout(timer); if (!panel.hidden || activePhases.includes(status?.state.phase) || waitingUntil) timer = setTimeout(poll, 2000); }
    async function poll() {
        try {
            const data = await api('status');
            render(data);
            if (waitingUntil && !activePhases.includes(data.state.phase)) {
                waitingUntil = 0;
                if (versionBefore && versionBefore !== data.version) { location.reload(); return; }
            }
        } catch (error) {
            if (waitingUntil > Date.now()) message('服务正在重启，正在等待重新连接…');
            else { waitingUntil = 0; message('暂时无法连接服务，请稍后检查或刷新页面。', true); }
        }
        schedule();
    }
    function close() { panel.hidden = true; badges.forEach(node => node.setAttribute('aria-expanded', 'false')); schedule(); }
    badges.forEach(button => button.addEventListener('click', async () => {
        if (!panel.hidden && anchor === button) { close(); return; }
        anchor = button; panel.hidden = false;
        const rect = button.getBoundingClientRect();
        panel.style.left = Math.max(12, Math.min(rect.left, innerWidth - Math.min(350, innerWidth - 24) - 12)) + 'px';
        panel.style.top = Math.min(rect.bottom + 10, innerHeight - 150) + 'px';
        badges.forEach(node => node.setAttribute('aria-expanded', String(node === button)));
        el('Close').focus();
        await poll();
        if (!status?.checkedAt) check();
    }));
    el('Close').addEventListener('click', () => { close(); anchor?.focus(); });
    document.addEventListener('pointerdown', event => { if (!panel.contains(event.target) && !badges.some(b => b.contains(event.target))) close(); });
    document.addEventListener('keydown', event => { if (event.key === 'Escape' && !panel.hidden) { close(); anchor?.focus(); } });
    async function check() {
        if (requesting) return;
        requesting = true; if (status) render(status); message('正在检查 GitHub 最新版本…');
        try { render(await api('check-updates?force=1')); }
        catch (error) { message(error.message, true); }
        finally { requesting = false; if (status) { const text = el('Message').textContent, error = el('Message').dataset.error === 'true'; render(status); message(text, error); } schedule(); }
    }
    el('Check').addEventListener('click', check);
    async function action(name) {
        if (requesting || !status) return;
        if (name !== 'update') {
            const accepted = await showAdminConfirm(name === 'rollback' ? `备份数据库并回滚到 v${status.state.previousVersion}？服务会短暂重启。` : `备份数据库并应用 v${status.state.pendingVersion}？服务会短暂重启。`, '版本切换');
            if (!accepted) return;
        }
        requesting = true; versionBefore = status.version; render(status);
        if (name !== 'update') waitingUntil = Date.now() + 180000;
        try { render(await api(name, name === 'update' ? { version: status.latest.version } : {})); }
        catch (error) { message(error.message, true); waitingUntil = 0; }
        finally { requesting = false; if (status) { const text = el('Message').textContent, error = el('Message').dataset.error === 'true'; render(status); message(text, error); } schedule(); }
    }
    el('Download').addEventListener('click', () => action('update'));
    el('Apply').addEventListener('click', () => action('restart'));
    el('Rollback').addEventListener('click', () => action('rollback'));
    api('check-updates').then(render).then(schedule).catch(() => {});
})();
