<template>
  <span>{{ t('sessionHelp.before') }}<a class="app-link" href="https://chatgpt.com/api/auth/session" target="_blank" rel="noopener noreferrer">chatgpt.com/api/auth/session</a>{{ t('sessionHelp.after') }}</span>
</template>

<script setup lang="ts">
// ChatGPT Session 的获取说明：兑换页、批量页、账单页和首页常见问题共用同一段文字。
import { useI18n } from 'vue-i18n'

const { t } = useI18n({ useScope: 'global' })
</script>
