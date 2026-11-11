// 颜色工具：对比度、侧栏颜色推导、混色。输出的色号一律大写 #RRGGBB。

const HEX6 = /^#[0-9A-Fa-f]{6}$/

type RGB = [number, number, number]

function parse(hex: string): RGB {
  return [parseInt(hex.slice(1, 3), 16), parseInt(hex.slice(3, 5), 16), parseInt(hex.slice(5, 7), 16)]
}

function format(rgb: RGB): string {
  return '#' + rgb.map((c) => Math.max(0, Math.min(255, Math.round(c))).toString(16).padStart(2, '0')).join('').toUpperCase()
}

function luminance(hex: string): number {
  const [r, g, b] = parse(hex).map((c) => {
    const s = c / 255
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4)
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

/** WCAG 对比度，范围 1–21。 */
export function contrastRatio(a: string, b: string): number {
  const la = luminance(a)
  const lb = luminance(b)
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05)
}

/** 必须是 #RRGGBB，且白字压在上面的对比度不低于 4.5。 */
export function isUsablePrimary(hex: string): boolean {
  return typeof hex === 'string' && HEX6.test(hex) && contrastRatio('#FFFFFF', hex) >= 4.5
}

function rgbToHsl([r, g, b]: RGB): [number, number, number] {
  r /= 255; g /= 255; b /= 255
  const max = Math.max(r, g, b)
  const min = Math.min(r, g, b)
  const l = (max + min) / 2
  if (max === min) return [0, 0, l]
  const d = max - min
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min)
  let h: number
  if (max === r) h = (g - b) / d + (g < b ? 6 : 0)
  else if (max === g) h = (b - r) / d + 2
  else h = (r - g) / d + 4
  return [h / 6, s, l]
}

function hslToRgb(h: number, s: number, l: number): RGB {
  if (s === 0) return [l * 255, l * 255, l * 255]
  const q = l < 0.5 ? l * (1 + s) : l + s - l * s
  const p = 2 * l - q
  const f = (t: number) => {
    if (t < 0) t += 1
    if (t > 1) t -= 1
    if (t < 1 / 6) return p + (q - p) * 6 * t
    if (t < 1 / 2) return q
    if (t < 2 / 3) return p + (q - p) * (2 / 3 - t) * 6
    return p
  }
  return [f(h + 1 / 3) * 255, f(h) * 255, f(h - 1 / 3) * 255]
}

/** a 占 weightOfA，b 占其余。 */
export function mixHex(a: string, b: string, weightOfA: number): string {
  const pa = parse(a)
  const pb = parse(b)
  return format([0, 1, 2].map((i) => pa[i] * weightOfA + pb[i] * (1 - weightOfA)) as RGB)
}

/**
 * 侧栏颜色：由当前主色推出。side-bg 保持主色的色相和饱和度，亮度每次降 0.02，
 * 直到白字压在上面不低于 10:1；菜单字、分组标题是白色混入 side-bg（起点 78% / 60%，不达标逐步提高）。
 */
export function sideColorsFrom(primary: string): { bg: string; ink: string; muted: string } {
  const [h, s, l0] = rgbToHsl(parse(primary))
  let bg = format(hslToRgb(h, s, l0))
  for (let l = l0; contrastRatio('#FFFFFF', bg) < 10 && l > 0; ) {
    l = Math.max(0, l - 0.02)
    bg = format(hslToRgb(h, s, l))
  }
  // 菜单字、分组标题：白色占比起点 78% / 60%，和 side-bg 的对比度不到 4.5 就每次加 2%，最多到纯白
  const lift = (start: number): string => {
    let w = start
    let c = mixHex('#FFFFFF', bg, w)
    while (contrastRatio(c, bg) < 4.5 && w < 1) {
      w = Math.min(1, Math.round((w + 0.02) * 100) / 100)
      c = mixHex('#FFFFFF', bg, w)
    }
    return c
  }
  return { bg, ink: lift(0.78), muted: lift(0.6) }
}
