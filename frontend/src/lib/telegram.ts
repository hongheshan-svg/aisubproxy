// Telegram 告警配置（GET/PUT /api/v1/admin/notify/telegram 的返回）
export interface TelegramConfig {
  token_configured: boolean
  token_hint: string
  token_error: string
  chat_id: string
  env_configured: boolean
  env_chat_id: string
  source: 'settings' | 'env' | 'none'
  effective_chat_id: string
  master_key_ok: boolean
}

// 生效来源对应的标签文字和颜色
export function telegramSourceTag(source?: string): { text: string; type: 'success' | 'warning' | 'info' } {
  switch (source) {
    case 'settings':
      return { text: '后台配置', type: 'success' }
    case 'env':
      return { text: '环境变量', type: 'warning' }
    case 'none':
      return { text: '未配置', type: 'info' }
    default:
      return { text: '读取中', type: 'info' }
  }
}
