'use strict';
const fs = require('fs');
const path = require('path');
const zlib = require('zlib');
const { readVersion, sha256, runtimeHash, allowedFile, decodeBundle } = require('../ota/common');
function build(root) {
    const version = readVersion(root), files = [];
    function walk(relative = '') {
        for (const item of fs.readdirSync(path.join(root, relative), { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name, 'en'))) {
            const name = relative ? `${relative}/${item.name}` : item.name;
            if (item.isDirectory() && (['public', 'ota', 'hcaptcha'].includes(name) || name.startsWith('public/'))) walk(name);
            if (!item.isFile() || !allowedFile(name)) continue;
            let content = fs.readFileSync(path.join(root, name));
            if (name.startsWith('public/') && name.endsWith('.html')) {
                content = Buffer.from(content.toString().replace(/((?:src|href)=["']\/?[\w/-]+\.(?:js|css))(?:\?[^"']*)?(["'])/g, `$1?v=${version}$2`));
            }
            files.push({ path: name, content: content.toString('base64'), sha256: sha256(content) });
        }
    }
    walk();
    const bytes = zlib.gzipSync(Buffer.from(JSON.stringify({ format: 'gw-link-ota-v1', version, runtimeHash: runtimeHash(root), files })), { level: 9 });
    decodeBundle(bytes, version, root);
    return { version, bytes, digest: sha256(bytes), count: files.length };
}
module.exports = { build };
if (require.main === module) {
    const root = path.resolve(__dirname, '..');
    const release = build(root);
    if (process.env.GITHUB_REF_TYPE === 'tag' && process.env.GITHUB_REF_NAME !== `v${release.version}`) throw new Error('Git 标签必须与 VERSION 一致');
    const output = path.resolve(process.argv[2] || path.join(root, 'dist'));
    fs.mkdirSync(output, { recursive: true });
    const name = `gw-link-v${release.version}.ota.gz`;
    fs.writeFileSync(path.join(output, name), release.bytes);
    fs.writeFileSync(path.join(output, name + '.sha256'), `${release.digest}  ${name}\n`);
    console.log(`${name}: ${release.count} files, ${release.bytes.length} bytes, SHA-256 ${release.digest}`);
}
