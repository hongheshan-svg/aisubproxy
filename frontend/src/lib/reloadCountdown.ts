// 升级成功后的刷新倒计时：从 seconds 倒数到 0，每秒回调一次剩余秒数，到 0 时执行 onDone。
// cancel() 之后不再回调（离开页面、点「立即刷新」时调用）。
export function createReloadCountdown(o: { seconds: number; onTick: (left: number) => void; onDone: () => void }) {
  let timer: ReturnType<typeof setInterval> | undefined
  const cancel = () => {
    if (timer !== undefined) clearInterval(timer)
    timer = undefined
  }
  const start = () => {
    cancel()
    let left = o.seconds
    o.onTick(left)
    timer = setInterval(() => {
      left -= 1
      o.onTick(left)
      if (left <= 0) {
        cancel()
        o.onDone()
      }
    }, 1000)
  }
  return { start, cancel }
}
