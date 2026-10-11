// 顶栏、公告弹窗等全站外壳交互（与具体页面无关）
(function () {
    var body = document.body;

    // 窄屏汉堡菜单
    var burger = document.querySelector('.tokyo-burger');
    if (burger) burger.addEventListener('click', function () {
        var open = body.classList.toggle('tokyo-menu-open');
        burger.setAttribute('aria-expanded', String(open));
    });

    // 主题设置里的“顶栏附加链接”：每行 名称|链接|图标
    var links = document.querySelector('.tokyo-nav__links');
    if (links && links.dataset.links) {
        var anchor = links.querySelector('.tokyo-notice-open');
        anchor = anchor ? anchor.parentNode : null;
        links.dataset.links.split(/\r?\n/).forEach(function (line) {
            var parts = line.split('|').map(function (s) { return s.trim(); });
            var name = parts[0], url = parts[1] || '', icon = parts[2] || 'fa-link';
            // 只接受站内路径或 http(s)，挡住 javascript: 之类
            if (!name || !/^(\/|https?:\/\/)/i.test(url)) return;
            var li = document.createElement('li');
            var a = document.createElement('a');
            a.className = 'tokyo-nav__link';
            a.href = url;
            if (/^https?:\/\//i.test(url) && url.indexOf(location.host) === -1) {
                a.target = '_blank';
                a.rel = 'noopener';
            }
            if (location.pathname !== '/' && url.indexOf(location.pathname) === 0) a.classList.add('is-active');
            var i = document.createElement('i');
            i.className = 'fa-duotone fa-regular tokyo-nav__icon ' + icon.replace(/[^\w\- ]/g, '');
            var span = document.createElement('span');
            span.textContent = name;
            a.appendChild(i);
            a.appendChild(span);
            li.appendChild(a);
            links.insertBefore(li, anchor);
        });
    }

    // 公告弹窗：点“我已阅读”后 1 小时内不再自动弹出
    var modal = document.getElementById('tokyo-notice');
    if (!modal) return;
    var KEY = 'tokyo-notice-ack';
    function show() { modal.hidden = false; body.classList.add('tokyo-modal-open'); }
    function hide() { modal.hidden = true; body.classList.remove('tokyo-modal-open'); }

    document.querySelectorAll('.tokyo-notice-open').forEach(function (el) {
        el.addEventListener('click', function (e) { e.preventDefault(); body.classList.remove('tokyo-menu-open'); show(); });
    });
    modal.querySelector('.tokyo-modal__shade').addEventListener('click', hide);
    modal.querySelector('.tokyo-notice-close').addEventListener('click', hide);
    modal.querySelector('.tokyo-notice-ack').addEventListener('click', function () {
        try { localStorage.setItem(KEY, String(Date.now())); } catch (e) {}
        hide();
    });
    document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && !modal.hidden) hide(); });

    var isHome = !!document.querySelector('.tokyo-shop');
    var last = 0;
    try { last = parseInt(localStorage.getItem(KEY) || '0', 10) || 0; } catch (e) {}
    if (isHome && modal.dataset.popup === '1' && Date.now() - last > 3600 * 1000) show();
})();
