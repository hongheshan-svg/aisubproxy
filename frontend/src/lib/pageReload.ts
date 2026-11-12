// 整页刷新、整页跳转的小封装：单独放一个文件，测试里可以直接 mock，不用碰 window.location。

/** 刷新当前页面。 */
export function reloadPage(): void {
  window.location.reload()
}

/** 整页跳到指定地址（会重新下载 index.html 和入口脚本）。 */
export function assignPage(url: string): void {
  window.location.assign(url)
}

type Loc = Pick<Location, 'href' | 'reload' | 'assign'>

/**
 * 整页跳转：目标地址和当前地址完全相同（含 hash）就刷新，否则 assign。
 * 后退、前进由 popstate 触发，守卫执行时地址栏已经是目标地址；只差 hash 的 assign 浏览器只会做页内锚点跳转、不重新加载文档，
 * 所以目标等于当前地址时必须用 reload。
 */
export function hardNavigate(href: string, loc: Loc = window.location): void {
  let abs = href
  try {
    abs = new URL(href, loc.href).href
  } catch {
    /* 解析不了就按原样 assign */
  }
  if (abs === loc.href) loc.reload()
  else loc.assign(href)
}
