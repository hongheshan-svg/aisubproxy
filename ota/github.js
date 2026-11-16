'use strict';
const { MAX_BUNDLE, version } = require('./common');
const REPO_RE = /^[a-zA-Z0-9_.-]+\/[a-zA-Z0-9_.-]+$/;
const HOSTS = new Set(['api.github.com', 'github.com', 'objects.githubusercontent.com', 'release-assets.githubusercontent.com']);
class GithubReleases {
    constructor(repo = process.env.OTA_GITHUB_REPO || 'hongheshan-svg/aisubproxy', fetcher = fetch) {
        if (!REPO_RE.test(repo)) throw new Error('OTA GitHub 仓库名称无效');
        this.repo = repo; this.fetcher = fetcher;
    }
    async get(url, { limit = 2 * 1024 * 1024, binary = false } = {}) {
        let current = new URL(url);
        const signal = AbortSignal.timeout(binary ? 120000 : 15000);
        for (let hop = 0; hop < 5; hop++) {
            if (current.protocol !== 'https:' || !HOSTS.has(current.hostname) || current.port || current.username || current.password) throw new Error('更新地址不在允许范围内');
            const headers = { Accept: binary ? 'application/octet-stream' : 'application/vnd.github+json', 'User-Agent': 'GW-LINK-OTA' };
            if (current.hostname === 'api.github.com' && process.env.OTA_GITHUB_TOKEN) headers.Authorization = `Bearer ${process.env.OTA_GITHUB_TOKEN}`;
            const response = await this.fetcher(current.href, { headers, signal, redirect: 'manual' });
            if ([301, 302, 303, 307, 308].includes(response.status)) {
                const location = response.headers.get('location');
                await response.body?.cancel();
                if (!location) throw new Error('更新地址重定向无效');
                current = new URL(location, current); continue;
            }
            if (!response.ok) { await response.body?.cancel(); const error = new Error(response.status === 404 ? '仓库还没有正式版本，或更新源暂不可访问' : `GitHub 请求失败 (${response.status})`); error.status = response.status; throw error; }
            if (Number(response.headers.get('content-length')) > limit) { await response.body?.cancel(); throw new Error('下载文件超过大小限制'); }
            const chunks = []; let size = 0;
            for await (const chunk of response.body) { size += chunk.length; if (size > limit) throw new Error('下载文件超过大小限制'); chunks.push(chunk); }
            return Buffer.concat(chunks);
        }
        throw new Error('更新地址重定向次数过多');
    }
    async latest() {
        const release = JSON.parse(await this.get(`https://api.github.com/repos/${this.repo}/releases/latest`));
        if (release.draft || release.prerelease) throw new Error('更新源未提供正式版本');
        const latest = version(release.tag_name);
        const name = `gw-link-v${latest}.ota.gz`;
        const find = filename => {
            const asset = release.assets?.find(item => item.name === filename);
            return asset && Number.isSafeInteger(asset.id) && asset.id > 0 ? { id: asset.id, size: asset.size } : null;
        };
        return { version: latest, name: String(release.name || `v${latest}`).slice(0, 200), notes: String(release.body || '').slice(0, 15000), publishedAt: release.published_at, url: `https://github.com/${this.repo}/releases/tag/v${latest}`, assetName: name, asset: find(name), checksum: find(name + '.sha256') };
    }
    async download(release) {
        if (!release.asset || !release.checksum || release.asset.size > MAX_BUNDLE) throw new Error('该版本没有兼容的 OTA 更新包');
        const checksum = (await this.get(`https://api.github.com/repos/${this.repo}/releases/assets/${release.checksum.id}`, { binary: true, limit: 512 })).toString().trim();
        const parts = checksum.split(/\s+/);
        if (!/^[a-f0-9]{64}$/.test(parts[0]) || parts[1] !== release.assetName) throw new Error('更新包校验文件无效');
        const bytes = await this.get(`https://api.github.com/repos/${this.repo}/releases/assets/${release.asset.id}`, { binary: true, limit: MAX_BUNDLE });
        return { bytes, digest: parts[0] };
    }
}
module.exports = { GithubReleases };
