// 整页刷新、整页跳转的小封装：单独放一个文件，测试里可以直接 mock，不用碰 window.location。

/** 刷新当前页面。 */
export function reloadPage(): void {
  window.location.reload()
}

/** 整页跳到指定地址（会重新下载 index.html 和入口脚本）。 */
export function assignPage(url: string): void {
  window.location.assign(url)
}
