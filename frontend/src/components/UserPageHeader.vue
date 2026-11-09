<template>
  <!-- 用户端页头：Logo（有就显示）、站名、语言/明暗切换；可选「返回首页」；下方是页面标题和说明。
       只放在 UserShell 里使用，颜色走 --u-*。 -->
  <header class="uh">
    <div class="uh-bar">
      <router-link v-if="back" to="/" class="u-link uh-back">{{ t('common.backHome') }}</router-link>
      <div class="uh-brand" :class="{ 'uh-brand--end': !back }">
        <img v-if="logoSrc" :src="logoSrc" alt="" class="uh-logo" />
        <span class="uh-name">{{ brand.name || t('home.brand') }}</span>
      </div>
      <div class="uh-tools">
        <LanguageToggle />
        <ThemeToggle />
      </div>
    </div>
    <div class="uh-hero">
      <h1 class="u-title">{{ title }}</h1>
      <p v-if="subtitle" class="u-help uh-sub">{{ subtitle }}</p>
    </div>
  </header>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import LanguageToggle from './LanguageToggle.vue'
import ThemeToggle from './ThemeToggle.vue'
import { siteBrand } from '../theme'

withDefaults(defineProps<{
  title: string
  subtitle?: string
  /** 为真时左侧显示「返回首页」 */
  back?: boolean
}>(), { subtitle: '', back: false })

const { t } = useI18n({ useScope: 'global' })
const brand = siteBrand
const logoSrc = computed(() => {
  const v = brand.value.logoVersion
  return v ? `/api/v1/public/site/logo?v=${encodeURIComponent(v)}` : ''
})
</script>

<style scoped>
.uh { box-sizing: border-box; width: 100%; max-width: 520px; margin: 0 auto; padding: 16px 16px 0; text-align: left; }
.uh-bar { display: flex; align-items: center; gap: 12px; min-height: 48px; }
.uh-back { display: inline-flex; align-items: center; min-height: 48px; font-size: 15px; white-space: nowrap; }
.uh-brand { display: flex; align-items: center; gap: 8px; min-width: 0; flex: 1; justify-content: center; }
.uh-brand--end { justify-content: flex-start; }
.uh-logo { height: 28px; width: auto; max-width: 96px; object-fit: contain; flex: none; }
.uh-name { font-size: 15px; font-weight: 700; color: var(--u-ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.uh-tools { display: flex; align-items: center; gap: 4px; flex: none; margin-left: auto; }
.uh-hero { padding-top: 16px; }
.uh-sub { margin: 0; }
</style>
