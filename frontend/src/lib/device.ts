// 按 UA 粗分设备，用来决定显示哪套操作说明。纯函数。
export function detectDevice(ua: string, maxTouchPoints: number): 'ios' | 'android' | 'desktop' {
  if (/iPhone|iPad|iPod/i.test(ua)) return 'ios'
  // iPadOS 13+ 的 Safari 默认报 Macintosh，靠触点数区分
  if (/Macintosh/i.test(ua) && maxTouchPoints > 1) return 'ios'
  if (/Android/i.test(ua)) return 'android'
  return 'desktop'
}
