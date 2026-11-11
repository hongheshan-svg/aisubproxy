<template>
  <div class="auth-page">
    <div class="auth-theme"><ThemeToggle /></div>
    <div class="auth-card" :class="{ 'is-wide': wide }">
      <div class="auth-brand">
        <img v-if="logoSrc && !logoFailed" :src="logoSrc" alt="" class="auth-logo" @error="logoFailed = true" />
        <span class="auth-name">{{ brand.name }}</span>
      </div>
      <h1 class="auth-title">{{ title }}</h1>
      <p v-if="subtitle" class="auth-subtitle">{{ subtitle }}</p>
      <div class="auth-body"><slot /></div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import ThemeToggle from './ThemeToggle.vue'
import { siteBrand } from '../theme'

// 登录类页面（管理员登录、注册、首次安装、代理商登录）共用的居中卡片
defineProps<{
  title: string
  subtitle?: string
  /** 内容较多的页面（首次安装）用更宽的卡片 */
  wide?: boolean
}>()

const brand = siteBrand
const logoFailed = ref(false)
const logoSrc = computed(() => {
  const v = brand.value.logoVersion
  return v ? `/api/v1/public/site/logo?v=${encodeURIComponent(v)}` : ''
})
// 换了 Logo 就给新图一次机会
watch(logoSrc, () => { logoFailed.value = false })
</script>

<style scoped>
.auth-page {
  position: relative;
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  /* 手机上卡片左右至少留 16px；底色由 html 上的皮肤底色透出 */
  padding: 40px 16px;
  box-sizing: border-box;
  background: transparent;
}
.auth-theme { position: absolute; top: 16px; right: 16px; }
.auth-card {
  width: 100%;
  max-width: 420px;
  min-width: 0;
  box-sizing: border-box;
  padding: 28px;
  background: var(--surface-solid);
  border: 1px solid var(--brd);
  border-radius: var(--radius-lg);
}
.auth-card.is-wide { max-width: 520px; }
.auth-brand { display: flex; align-items: center; justify-content: center; gap: 8px; min-width: 0; }
.auth-logo { height: 32px; width: auto; max-width: 96px; object-fit: contain; flex: none; }
.auth-name {
  min-width: 0;
  font-size: 15px;
  font-weight: 700;
  color: var(--ink);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.auth-title { margin: 20px 0 0; text-align: center; font-size: 24px; font-weight: 700; color: var(--ink); overflow-wrap: anywhere; }
.auth-subtitle { margin: 8px 0 0; text-align: center; font-size: 14px; color: var(--ink-2); overflow-wrap: anywhere; }
.auth-body { margin-top: 24px; display: flex; flex-direction: column; gap: 20px; min-width: 0; }

/*
  输入框和主按钮改成用户端同款，只在本卡片内生效。
  选择器 .auth-card[data-v] .x = (0,3,0)，高于皮肤里的 html[data-skin] .btn-primary (0,2,1)；
  悬停/按下规则另写同等或更高的写法，压住 skins.css 的 .btn-primary:hover:not(:disabled) (0,3,0) 和 zovo 的悬停光晕。
*/
.auth-card :deep(.input) {
  min-height: 48px;
  box-sizing: border-box;
  border: 3px solid var(--primary);
  border-radius: var(--radius-md);
  background: var(--surface-solid);
  color: var(--ink);
}
.auth-card :deep(.input:focus),
.auth-card :deep(.input:focus-visible) {
  border-color: var(--primary);
  background: var(--surface-solid);
  outline: 3px solid var(--primary);
  outline-offset: 2px;
  box-shadow: none;
}
.auth-card :deep(.btn-primary),
.auth-card :deep(.btn-primary:hover:not(:disabled)),
.auth-card :deep(.btn-primary:active:not(:disabled)) {
  min-height: 48px;
  border-radius: var(--radius-md);
  background: var(--primary);
  background-image: none;
  color: var(--primary-on);
  box-shadow: none;
  filter: none;
}
.auth-card :deep(.btn-primary:hover:not(:disabled)) { background: var(--primary-hover); }
.auth-card :deep(.btn-primary:active:not(:disabled)) { background: var(--primary-active); }
.auth-card :deep(.btn-primary:disabled) { opacity: 0.5; cursor: not-allowed; }
.auth-card :deep(.btn-primary:focus-visible) { outline: 3px solid var(--primary); outline-offset: 3px; }
</style>
