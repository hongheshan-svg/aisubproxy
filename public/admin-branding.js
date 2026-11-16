(() => {
    'use strict';
    const form = document.getElementById('brandingForm');
    const name = document.getElementById('brandName');
    const file = document.getElementById('brandLogoFile');
    const preview = document.getElementById('brandLogoPreview');
    const status = document.getElementById('brandingStatus');
    const save = document.getElementById('saveBrandingButton');
    const remove = document.getElementById('removeBrandLogo');
    let logoUrl = '', draft = null, objectUrl = '', selection = 0, dirty = false;
    function showPreview(url) { preview.src = url || ''; preview.hidden = !url; }
    function resetObjectUrl() { if (objectUrl) URL.revokeObjectURL(objectUrl); objectUrl = ''; }
    async function load() {
        const brand = await window.siteBrandReady;
        if (dirty) return;
        name.value = brand.name;
        logoUrl = brand.logoUrl;
        showPreview(logoUrl);
    }
    name.addEventListener('input', () => { dirty = true; });
    file.addEventListener('change', async () => {
        const version = ++selection;
        dirty = true;
        const selected = file.files[0];
        if (!selected) return;
        save.disabled = true;
        status.textContent = '正在处理图片…';
        try {
            if (!['image/png', 'image/jpeg', 'image/webp'].includes(selected.type) || selected.size > 5 * 1024 * 1024) {
                throw new Error('请选择 5 MB 以内的 PNG、JPG 或 WebP 图片');
            }
            const bitmap = await createImageBitmap(selected);
            const canvas = document.createElement('canvas');
            const scale = Math.min(1, 512 / Math.max(bitmap.width, bitmap.height));
            canvas.width = Math.max(1, Math.round(bitmap.width * scale));
            canvas.height = Math.max(1, Math.round(bitmap.height * scale));
            canvas.getContext('2d').drawImage(bitmap, 0, 0, canvas.width, canvas.height);
            bitmap.close();
            const blob = await new Promise(resolve => canvas.toBlob(resolve, 'image/png'));
            if (!blob || blob.size > 512 * 1024) throw new Error('图片过大，请换用更简洁的 Logo');
            if (version !== selection) return;
            draft = blob;
            resetObjectUrl();
            objectUrl = URL.createObjectURL(blob);
            showPreview(objectUrl);
            status.textContent = '图片已准备好，点击保存后生效。';
        } catch (error) {
            if (version !== selection) return;
            file.value = '';
            status.textContent = error.message || '无法读取图片，请更换文件';
        } finally { if (version === selection) save.disabled = false; }
    });
    remove.addEventListener('click', () => {
        selection += 1;
        dirty = true;
        logoUrl = ''; draft = null; file.value = '';
        resetObjectUrl(); showPreview('');
        save.disabled = false;
        status.textContent = '点击保存后移除 Logo，使用品牌文字标识。';
    });
    form.addEventListener('submit', async event => {
        event.preventDefault();
        if (save.disabled) return;
        save.disabled = true; file.disabled = true; remove.disabled = true; name.disabled = true;
        status.textContent = '正在保存…';
        try {
            const brandName = name.value.trim();
            if (!brandName) throw new Error('请输入品牌名称');
            if (draft) {
                const res = await authFetch('/api/admin/branding/logo', { method: 'POST', headers: { 'Content-Type': 'image/png' }, body: draft });
                const data = await res.json();
                if (!res.ok || !data.success) throw new Error(data.message || 'Logo 上传失败');
                logoUrl = data.logoUrl;
            }
            const res = await authFetch('/api/admin/branding', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: brandName, logoUrl }) });
            const data = await res.json();
            if (!res.ok || !data.success) throw new Error(data.message || '品牌设置保存失败');
            window.applySiteBrand(data.branding);
            draft = null; file.value = ''; dirty = false;
            resetObjectUrl(); showPreview(data.branding.logoUrl);
            status.textContent = '已保存。首页、后台和登录页将在打开或刷新时显示新品牌。';
        } catch (error) { status.textContent = error.message || '保存失败，请重试'; }
        finally { save.disabled = false; file.disabled = false; remove.disabled = false; name.disabled = false; }
    });
    load();
})();
