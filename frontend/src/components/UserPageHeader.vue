<template>
  <!-- 用户端页头：顶部一行见 UserTopNav；下方是页面标题和说明。
       只放在 UserShell 里使用，颜色走 --u-*。 -->
  <header class="uh" :class="{ 'uh--desktop': isDesktop }" :style="maxWidth ? { maxWidth: `${maxWidth}px` } : undefined">
    <UserTopNav :home="home" :back="back" />
    <div class="uh-hero">
      <h1 class="u-title">{{ title }}</h1>
      <p v-if="subtitle" class="u-help uh-sub">{{ subtitle }}</p>
    </div>
  </header>
</template>

<script setup lang="ts">
import UserTopNav from './redeem/UserTopNav.vue'
import { isDesktop } from '../lib/layoutMode'

withDefaults(defineProps<{
  title: string
  subtitle?: string
  /** 为真时窄屏左侧显示「返回首页」 */
  back?: boolean
  /** 在首页：导航里的常见问题用页内锚点 */
  home?: boolean
  /** 页面正文比默认 520px 宽时传入同样的宽度（px），让页头和正文左边对齐 */
  maxWidth?: number
}>(), { subtitle: '', back: false, home: false, maxWidth: 0 })
</script>

<style scoped>
.uh { box-sizing: border-box; width: 100%; max-width: 520px; margin: 0 auto; padding: 16px 16px 0; text-align: left; }
.uh-hero { padding-top: 16px; }
.uh-sub { margin: 0; }
/* 电脑布局：顶部条整宽一条（不限宽、不加内边距），标题区放宽到 1120px 并与内容区左右各留 24px */
@media (min-width: 1024px) {
  .uh--desktop { max-width: none; padding: 0; }
  .uh--desktop .uh-hero { box-sizing: border-box; max-width: 1120px; margin: 0 auto; padding: 16px 24px 0; }
}
</style>
