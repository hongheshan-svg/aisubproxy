// 批量兑换页（BatchRedeemView）的提交编排。纯函数：页面没有组件测试，判断提到这里单测，页面只接线。
//
// 资金安全：同一张卡密在一个批次里只提交一次；重置、换批次、离开页面之后不再提交还没开始的卡密，
// 已经发出的兑换请求不取消、不重发。

/**
 * 自动提交的并发循环：最多 concurrency 张同时在提交，按顺序取下一张，每张只取一次。
 * 每取一张之前先问 isLive()：重置、换批次或离开页面之后不再取新的；已经在提交的那一张照常等它返回。
 */
export async function runSubmitPool<T>(
  items: T[],
  concurrency: number,
  isLive: () => boolean,
  submit: (item: T) => Promise<unknown>,
): Promise<void> {
  let cursor = 0
  const runners = Array.from({ length: Math.min(concurrency, items.length) }, async () => {
    while (isLive()) {
      const i = cursor++
      if (i >= items.length) return
      await submit(items[i])
    }
  })
  await Promise.all(runners)
}
