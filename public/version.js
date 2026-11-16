(() => {
    fetch('/api/public/version', { cache: 'no-store', signal: AbortSignal.timeout(10000) })
        .then(response => response.ok ? response.json() : Promise.reject())
        .then(data => document.querySelectorAll('[data-app-version]').forEach(node => { node.textContent = `v${data.version}`; }))
        .catch(() => {});
})();
