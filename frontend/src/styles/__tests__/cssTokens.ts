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

/** 顺着 var(--x) 和 var(--x, 兜底) 解析到底，输出大写 #RRGGBB；解析不到十六进制色就抛错 */
export function resolveHex(tokens: TokenMap, name: string, seen: string[] = []): string {
  if (seen.includes(name)) throw new Error(`${name} 循环引用：${[...seen, name].join(' -> ')}`)
  const raw = tokens[name]
  if (raw === undefined) throw new Error(`${name} 未定义`)
  return resolveValue(tokens, raw, name, [...seen, name])
}

function resolveValue(tokens: TokenMap, raw: string, name: string, seen: string[]): string {
  const v = raw.replace(/\s*!important\s*$/, '').trim()
  const hex = v.match(/^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/)
  if (hex) {
    const h = hex[1].length === 3 ? hex[1].split('').map((c) => c + c).join('') : hex[1]
    return '#' + h.toUpperCase()
  }
  const ref = v.match(/^var\(\s*(--[\w-]+)\s*(?:,\s*([\s\S]+))?\)$/)
  if (ref) {
    if (tokens[ref[1]] !== undefined) return resolveHex(tokens, ref[1], seen)
    if (ref[2] !== undefined) return resolveValue(tokens, ref[2], name, seen)
    throw new Error(`${name} 引用的 ${ref[1]} 未定义`)
  }
  throw new Error(`${name} 解析不到十六进制色：${v}`)
}
