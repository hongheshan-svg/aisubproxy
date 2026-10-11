!function () {
    const $cats = $(`.switch-category`), $list = $(`.item-list`), $title = $(`.tokyo-catalog__title`);
    const $search = $(`.tokyo-search`), $searchInput = $(`.tokyo-search__input`);
    const categoryId = getVar("CAT_ID");

    const esc = v => String(v == null ? '' : v).replace(/[&<>"']/g, c => ({
        '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    })[c]);
    // 商品名在后台可以带 HTML，列表里只要纯文本；DOMParser 解析不会执行其中的脚本/事件
    const plain = v => new DOMParser().parseFromString(String(v == null ? '' : v), 'text/html').body.textContent;

    function state(html) {
        $list.html(`<tr class="tokyo-table__state"><td colspan="4">${html}</td></tr>`);
    }

    function tags(item) {
        const list = Array.isArray(item && item.tags) ? item.tags : [];
        return list.map(t => {
            const text = String((t && t.text) || '').trim();
            return text ? `<span class="tokyo-pill tokyo-pill--${esc((t && t.color) || 'red')}">${esc(text)}</span>` : '';
        }).join('');
    }

    function render(data) {
        if (!data || data.length == 0) {
            state(`<i class="fa-duotone fa-regular fa-box-open"></i> ${i18n('没有商品')}`);
            return;
        }

        const symbol = esc(format.currencySymbol());
        $list.html(data.map(item => {
            const soldOut = item.stock_state !== undefined ? item.stock_state == 0 : item.stock == 0;
            const href = soldOut ? `javascript:void(0);` : `/item/${encodeURIComponent(item.id)}`;
            const price = `${symbol}${esc(item.price)}`;
            const cover = item.cover ? `<img src="${esc(item.cover)}" alt="" loading="lazy" decoding="async">` : `<i class="fa-duotone fa-regular fa-image"></i>`;

            return `<tr class="tokyo-row ${soldOut ? 'is-soldout' : ''}" data-href="${href}">
    <td class="tokyo-row__main">
        <a class="tokyo-row__anchor" href="${href}">
            <span class="tokyo-row__thumb">${cover}</span>
            <span class="tokyo-row__copy">
                <span class="tokyo-row__name">${esc(plain(i18n(item.name)))}</span>
                <span class="tokyo-row__tags">
                    <span class="tokyo-pill tokyo-pill--meta">${price}</span>
                    <span class="tokyo-pill tokyo-pill--meta">${i18n('库存')} ${esc(item.stock)}</span>
                    ${tags(item)}
                    <span class="tokyo-pill tokyo-pill--success">${item.delivery_way == 0 ? i18n('自动发货') : i18n('在线发货')}</span>
                    ${item.recommend == 1 ? `<span class="tokyo-pill tokyo-pill--primary">${i18n('推荐')}</span>` : ''}
                </span>
            </span>
        </a>
    </td>
    <td class="tokyo-row__price">${price}</td>
    <td class="tokyo-row__stock">${esc(item.stock)}</td>
    <td class="tokyo-row__action">${soldOut
                ? `<span class="tokyo-buy is-disabled"><i class="fa-duotone fa-regular fa-ban"></i>${i18n('售罄')}</span>`
                : `<a class="tokyo-buy" href="${href}"><i class="fa-duotone fa-regular fa-bag-shopping"></i>${i18n('购买')}</a>`}</td>
</tr>`;
        }).join(''));
    }

    function load(opt, title) {
        $title.text(title);
        state(`<i class="fa-duotone fa-regular fa-spinner-third icon-spin"></i> ${i18n('正在加载商品列表...')}`);
        trade.getCommodityList(Object.assign(opt, {done: render}));
    }

    function switchCategory(id, push = false) {
        const $cur = $cats.filter((_, el) => String($(el).data("id")) === String(id));
        $cats.removeClass("is-active");
        $cur.addClass("is-active");
        if (push) {
            history.pushState(null, '', `/cat/${id}`);
        }
        load({categoryId: id}, $cur.data("name") || i18n('全部商品'));
    }

    // 整行可点：点到行内空白处也进商品页
    $list.on("click", "tr.tokyo-row", function (e) {
        if (e.target.closest("a")) return;
        const href = $(this).data("href");
        if (href && href.indexOf("javascript") !== 0) location.href = href;
    });

    $cats.on("click", function () {
        if ($(this).hasClass("is-active")) return;
        switchCategory($(this).data("id"), true);
        // 窄屏下分类是横向滚动条，切换后把它滚进视野
        this.scrollIntoView({block: "nearest", inline: "center", behavior: "smooth"});
    });

    $search.on("submit", function (e) {
        e.preventDefault();
        const keywords = $searchInput.val().trim();
        if (keywords == '') {
            layer.msg(i18n("请输入要搜索的商品名称关键词"));
            return;
        }
        $cats.removeClass("is-active");
        load({keywords: keywords}, `${i18n('搜索')}：${keywords}`);
    });

    switchCategory(categoryId != 0 ? categoryId : $cats.first().data("id"));
}();
