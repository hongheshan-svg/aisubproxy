// 整页刷新、整页跳转的小封装：单独放一个文件，测试里可以直接 mock，不用碰 window.location。

/** 刷新当前页面。 */
export function reloadPage(): void {
  window.location.reload()
}

/** 整页跳到指定地址（会重新下载 index.html 和入口脚本）。 */
export function assignPage(url: string): void {
  window.location.assign(url)
}

type Loc = Pick<Location, 'href' | 'reload' | 'assign' | 'replace'>

const withoutHash = (u: string) => {
  const i = u.indexOf('#')
  return i < 0 ? u : u.slice(0, i)
}

/**
 * 整页跳转：保证一定重新加载文档。
 * - 目标和当前地址完全相同：reload。后退、前进由 popstate 触发，守卫执行时地址栏已经是目标地址。
 * - 只差 hash：assign 浏览器只会做页内锚点跳转、不重新加载文档，所以先 replace 换掉 hash（不多压历史），再 reload。
 * - 其他：assign。
 */
export function hardNavigate(href: string, loc: Loc = window.location): void {
  let abs = href
  try {
    abs = new URL(href, loc.href).href
  } catch {
    /* 解析不了就按原样 assign */
  }
  if (abs === loc.href) {
    loc.reload()
  } else if (withoutHash(abs) === withoutHash(loc.href)) {
    loc.replace(abs)
    loc.reload()
  } else {
    loc.assign(href)
  }
}
