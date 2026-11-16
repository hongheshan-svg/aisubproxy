(() => {
    'use strict';
    const fallback = { name: 'GW-LINK', logoUrl: '' };
    function apply(brand) {
        document.querySelectorAll('[data-brand-name]').forEach(node => { node.textContent = brand.name; });
        document.querySelectorAll('[data-brand-logo]').forEach(node => {
            node.replaceChildren();
            // Keep intrinsic image dimensions from escaping the icon, even with stale CSS.
            const size = Math.min(120, Math.max(16, Number(node.dataset.brandSize) || 36));
            Object.assign(node.style, {
                display: 'inline-grid', placeItems: 'center', width: `${size}px`, height: `${size}px`,
                minWidth: `${size}px`, maxWidth: `${size}px`, minHeight: `${size}px`, maxHeight: `${size}px`,
                flex: `0 0 ${size}px`, overflow: 'hidden'
            });
            node.classList.toggle('has-brand-image', Boolean(brand.logoUrl));
            node.style.background = brand.logoUrl ? 'transparent' : '';
            node.style.boxShadow = brand.logoUrl ? 'none' : '';
            if (brand.logoUrl) {
                const image = document.createElement('img');
                image.width = size;
                image.height = size;
                Object.assign(image.style, {
                    display: 'block', width: '100%', height: '100%', maxWidth: '100%', maxHeight: '100%',
                    objectFit: 'contain'
                });
                image.src = brand.logoUrl;
                image.alt = `${brand.name} Logo`;
                image.addEventListener('error', () => {
                    node.classList.remove('has-brand-image');
                    node.style.background = '';
                    node.style.boxShadow = '';
                    node.textContent = [...brand.name].slice(0, 2).join('');
                }, { once: true });
                node.append(image);
            } else node.textContent = [...brand.name].slice(0, 2).join('');
        });
        document.title = `${document.documentElement.dataset.brandTitle || '订阅中心'} | ${brand.name}`;
        let icon = document.querySelector('link[rel="icon"]');
        if (!icon) { icon = document.createElement('link'); icon.rel = 'icon'; document.head.append(icon); }
        icon.type = brand.logoUrl ? 'image/png' : 'image/svg+xml';
        icon.href = brand.logoUrl || '/favicon.svg';
        window.siteBrand = brand;
    }
    window.applySiteBrand = apply;
    window.siteBrandReady = (async () => {
        try {
            const res = await fetch('/api/public/branding', { cache: 'no-store', signal: AbortSignal.timeout(8000) });
            const data = await res.json();
            if (!res.ok || !data.success) throw new Error('Brand unavailable');
            apply(data.branding);
            return data.branding;
        } catch (_) { apply(fallback); return fallback; }
    })();
})();
