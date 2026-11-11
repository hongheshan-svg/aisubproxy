// 后台「外观与品牌」页的纯逻辑：Logo 文件检查、保存内容、设置转表单。
import type { SkinId, ThemeMode } from '../theme'

export const LOGO_MAX_BYTES = 256 * 1024
export const LOGO_ACCEPT = 'image/png,image/jpeg,image/webp'
export const LOGO_TOO_BIG = 'Logo 不能超过 256 KB，请换一张小一点的图片'
export const LOGO_BAD_TYPE = '没有收到可用的图片，请选择一张 PNG、JPG 或 WebP 图片再上传'
export const LOGO_EMPTY = '这个文件是空的，请换一张图片'

/** 上传前检查大小和类型；通过返回空串，否则返回提示。后端还会按文件头再验一遍。 */
export function checkLogoFile(f: { size: number; type: string }): string {
  if (!f) return LOGO_BAD_TYPE
  if (!LOGO_ACCEPT.split(',').includes(f.type)) return LOGO_BAD_TYPE
  if (f.size <= 0) return LOGO_EMPTY
  if (f.size > LOGO_MAX_BYTES) return LOGO_TOO_BIG
  return ''
}

export interface FormState { brand_name: string; brand_sub: string; logoVersion: string }

/**
 * 把服务端设置转成表单状态。
 * theme、skin 只有服务端真的返回了才有值：没返回就不要去改本机的亮暗偏好。
 */
export function applySettings(d: Record<string, any>, current: FormState) {
  return {
    brand_name: d.brand_name || current.brand_name,
    brand_sub: d.brand_sub || current.brand_sub,
    logoVersion: (d.logo_version || '') as string,
    skin: (d.skin || undefined) as string | undefined,
    theme: (d.theme_mode || undefined) as string | undefined,
  }
}

/** 保存时发给后端的内容：站名、副标题、皮肤、明暗。 */
export function buildSavePayload(form: FormState, skin: SkinId, mode: ThemeMode): { brand_name: string; brand_sub: string; skin: SkinId; theme_mode: ThemeMode } {
  return { brand_name: form.brand_name, brand_sub: form.brand_sub, skin, theme_mode: mode }
}
