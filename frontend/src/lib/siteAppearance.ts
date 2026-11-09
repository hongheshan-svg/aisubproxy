// 后台「外观与品牌」页的纯逻辑：主色能不能保存、Logo 文件检查。
import { contrastRatio, isUsablePrimary } from './brandColor'

export const PRESET_COLORS = [
  { name: '深青', hex: '#0F6B5F' },
  { name: '海蓝', hex: '#1E4FA8' },
  { name: '墨紫', hex: '#5B3FA0' },
  { name: '枣红', hex: '#A3283B' },
  { name: '松绿', hex: '#2F6B2F' },
  { name: '石墨', hex: '#36454F' },
] as const

export const LOGO_MAX_BYTES = 256 * 1024
export const LOGO_ACCEPT = 'image/png,image/jpeg,image/webp'
export const LOGO_TOO_BIG = 'Logo 不能超过 256 KB，请换一张小一点的图片'
export const LOGO_BAD_TYPE = '没有收到可用的图片，请选择一张 PNG、JPG 或 WebP 图片再上传'
export const PRIMARY_TOO_LIGHT = '这个颜色太浅，按钮上的白字看不清，请选深一点的颜色'
export const PRIMARY_BAD_FORMAT = '颜色格式要写成 #RRGGBB，例如 #0F6B5F'

export interface PrimaryCheck {
  canSave: boolean
  ratio: number | null
  ratioText: string
  message: string
}

/** 输入主色，输出能否保存、白字对比度文字和提示。 */
export function checkPrimary(hex: string): PrimaryCheck {
  if (typeof hex !== 'string' || !/^#[0-9A-Fa-f]{6}$/.test(hex)) {
    return { canSave: false, ratio: null, ratioText: '', message: PRIMARY_BAD_FORMAT }
  }
  const ratio = contrastRatio(hex, '#FFFFFF')
  const ok = isUsablePrimary(hex)
  return { canSave: ok, ratio, ratioText: `白字对比度 ${ratio.toFixed(2)}:1`, message: ok ? '' : PRIMARY_TOO_LIGHT }
}

/** 上传前检查大小和类型；通过返回空串，否则返回提示。后端还会按文件头再验一遍。 */
export function checkLogoFile(f: { size: number; type: string }): string {
  if (!f || f.size <= 0) return LOGO_BAD_TYPE
  if (!LOGO_ACCEPT.split(',').includes(f.type)) return LOGO_BAD_TYPE
  if (f.size > LOGO_MAX_BYTES) return LOGO_TOO_BIG
  return ''
}
