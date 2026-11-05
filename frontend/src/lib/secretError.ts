// 卡台密钥（API Key、Webhook Secret）已保存但后端解不开时，设置接口会带 `<键>_error`。
// 典型原因是 APP_MASTER_KEY 更换或丢失。页面要把它显示出来，提示管理员重新填写。

/** 取某个密钥项的解密错误文案；没有错误返回空串。 */
export function secretErrorText(resp: any, key: string): string {
  const v = resp?.[`${key}_error`]
  return typeof v === 'string' ? v.trim() : ''
}

/** 密钥项的输入框占位提示：解不开时不能显示成「已配置」。 */
export function secretPlaceholder(resp: any, key: string, emptyHint: string): string {
  if (secretErrorText(resp, key)) return '需要重新填写'
  const configured = resp?.[`${key}_configured`] ?? resp?.[`${key}_set`]
  if (!configured) return emptyHint
  return `已配置 ${resp?.[`${key}_hint`] || ''}`.trim()
}
