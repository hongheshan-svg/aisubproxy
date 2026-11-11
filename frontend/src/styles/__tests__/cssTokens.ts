// 只给测试用：把样式表里的 CSS 变量按「根 / 皮肤亮色 / 皮肤暗色」拆出来，再解析成十六进制色。

export type TokenMap = Record<string, string>
export interface ParsedTokens {
  root: TokenMap
  skins: Record<string, { light: TokenMap; dark: TokenMap }>
}

const SKIN_LIGHT = /^html\[data-skin=["']([\w-]+)["']\]$/
const SKIN_DARK = /^html\.dark\[data-skin=["']([\w-]+)["']\]$/

/** 取出一个声明块里所有 `--xxx: 值` */
function declsOf(body: string): TokenMap {
  const out: TokenMap = {}
  for (const part of body.split(';')) {
    const i = part.indexOf(':')
    if (i < 0) continue
    const name = part.slice(0, i).trim()
    if (!name.startsWith('--')) continue
    out[name] = part.slice(i + 1).trim()
  }
  return out
}

export function parseSkinTokens(css: string): ParsedTokens {
  const src = css.replace(/\/\*[\s\S]*?\*\//g, '')
  const parsed: ParsedTokens = { root: {}, skins: {} }
  const slot = (id: string) => (parsed.skins[id] ??= { light: {}, dark: {} })

  let depth = 0
  let selector = ''
  let start = 0
  let buf = ''
  for (let i = 0; i < src.length; i++) {
    const c = src[i]
    if (c === '{') {
      if (depth === 0) {
        selector = buf.trim()
        start = i + 1
      }
      depth++
      buf = ''
    } else if (c === '}') {
      depth--
      if (depth === 0) {
        // 只看顶层规则；@media 等块的选择器以 @ 开头，直接跳过
        if (!selector.startsWith('@')) {
          const decls = declsOf(src.slice(start, i))
          for (const sel of selector.split(',').map((s) => s.trim())) {
            let m: RegExpMatchArray | null
            if (sel === ':root') Object.assign(parsed.root, decls)
            else if ((m = sel.match(SKIN_LIGHT))) Object.assign(slot(m[1]).light, decls)
            else if ((m = sel.match(SKIN_DARK))) Object.assign(slot(m[1]).dark, decls)
          }
        }
        buf = ''
      }
    } else if (depth === 0) {
      buf += c
    }
  }
  return parsed
}

/** 亮色 = root 叠皮肤亮色块；暗色 = 亮色再叠皮肤暗色块 */
export function effectiveTokens(parsed: ParsedTokens, skin: string, mode: 'light' | 'dark'): TokenMap {
  const s = parsed.skins[skin] ?? { light: {}, dark: {} }
  return mode === 'light' ? { ...parsed.root, ...s.light } : { ...parsed.root, ...s.light, ...s.dark }
}

export type RGBA = [number, number, number, number]

/** 顺着 var(--x) 和 var(--x, 兜底) 解析到底，输出大写 #RRGGBB；解析不到不透明的颜色就抛错 */
export function resolveHex(tokens: TokenMap, name: string, seen: string[] = []): string {
  const c = resolveRgba(tokens, name, seen)
  if (c[3] !== 1) throw new Error(`${name} 解析不到十六进制色：${tokens[name]}`)
  return toHex(c)
}

/**
 * 解析成 [r, g, b, a]（r/g/b 为 0–255 的数，a 为 0–1）。支持 #rgb、#rrggbb、rgb()/rgba()、var() 链与兜底，
 * 以及 color-mix(in srgb, A p%, B)（两个都不透明时按 sRGB 线性插值，和浏览器一致）。
 */
export function resolveRgba(tokens: TokenMap, name: string, seen: string[] = []): RGBA {
  if (seen.includes(name)) throw new Error(`${name} 循环引用：${[...seen, name].join(' -> ')}`)
  const raw = tokens[name]
  if (raw === undefined) throw new Error(`${name} 未定义`)
  return resolveValue(tokens, raw, name, [...seen, name])
}

/** 半透明色叠在不透明底色上，得到实际看到的颜色（#RRGGBB）。 */
export function composite(top: RGBA, bottomHex: string): string {
  const b = hexToRgb(bottomHex)
  const a = top[3]
  return toHex([0, 1, 2].map((i) => top[i] * a + b[i] * (1 - a)).concat(1) as RGBA)
}

function hexToRgb(hex: string): [number, number, number] {
  const h = hex.replace('#', '')
  const full = h.length === 3 ? h.split('').map((c) => c + c).join('') : h
  return [0, 2, 4].map((i) => parseInt(full.slice(i, i + 2), 16)) as [number, number, number]
}

function toHex(c: RGBA): string {
  return '#' + c.slice(0, 3).map((v) => Math.round(v).toString(16).padStart(2, '0')).join('').toUpperCase()
}

/** 按顶层逗号拆参数（括号里的逗号不拆）。 */
function splitArgs(s: string): string[] {
  const out: string[] = []
  let depth = 0
  let cur = ''
  for (const ch of s) {
    if (ch === '(') depth++
    if (ch === ')') depth--
    if (ch === ',' && depth === 0) { out.push(cur.trim()); cur = '' } else cur += ch
  }
  if (cur.trim()) out.push(cur.trim())
  return out
}

function resolveValue(tokens: TokenMap, raw: string, name: string, seen: string[]): RGBA {
  const v = raw.replace(/\s*!important\s*$/, '').trim()
  const hex = v.match(/^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/)
  if (hex) return [...hexToRgb(hex[1]), 1]
  const rgb = v.match(/^rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)\s*(?:,\s*([\d.]+)\s*)?\)$/)
  if (rgb) return [Number(rgb[1]), Number(rgb[2]), Number(rgb[3]), rgb[4] === undefined ? 1 : Number(rgb[4])]
  const ref = v.match(/^var\(\s*(--[\w-]+)\s*(?:,\s*([\s\S]+))?\)$/)
  if (ref) {
    if (tokens[ref[1]] !== undefined) return resolveRgba(tokens, ref[1], seen)
    if (ref[2] !== undefined) return resolveValue(tokens, ref[2], name, seen)
    throw new Error(`${name} 引用的 ${ref[1]} 未定义`)
  }
  const mix = v.match(/^color-mix\(\s*in\s+srgb\s*,([\s\S]+)\)$/)
  if (mix) {
    const [a, b] = splitArgs(mix[1])
    const pa = a?.match(/^([\s\S]+?)\s+([\d.]+)%$/)
    if (!pa || !b || /%$/.test(b)) throw new Error(`${name} 只支持 color-mix(in srgb, A p%, B)：${v}`)
    const ca = resolveValue(tokens, pa[1], name, seen)
    const cb = resolveValue(tokens, b, name, seen)
    if (ca[3] !== 1 || cb[3] !== 1) throw new Error(`${name} 的 color-mix 两边都要不透明：${v}`)
    const p = Number(pa[2]) / 100
    return [0, 1, 2].map((i) => ca[i] * p + cb[i] * (1 - p)).concat(1) as RGBA
  }
  throw new Error(`${name} 解析不到颜色：${v}`)
}
