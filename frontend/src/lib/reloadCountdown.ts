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

/**
 * 在线更新结束、服务恢复后，判断版本是否真的变了。
 * load() 查一次版本，返回请求是否成功；第一次失败（例如网关还在回 502）再查一次。
 * 两次都失败、或查到的版本和更新前一样，都算没变（不自动刷新）。
 */
export async function resolveRecoveredVersion(o: {
  before: string | undefined
  load: () => Promise<boolean>
  current: () => string | undefined
}): Promise<{ changed: boolean; after: string | undefined }> {
  const ok = (await o.load()) || (await o.load())
  const after = ok ? o.current() : undefined
  return { changed: !!after && after !== o.before, after }
}
