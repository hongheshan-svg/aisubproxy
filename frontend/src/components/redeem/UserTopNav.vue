<template>
  <!-- 用户端顶部一行：Logo + 站名、（电脑布局）主导航、语言/明暗切换；窄屏可显示回首页链接 -->
  <div class="tn" :class="{ 'tn--desktop': isDesktop }">
    <div class="tn-inner">
      <router-link v-if="back && !isDesktop" to="/" class="u-link tn-back">{{ t('common.backHome') }}</router-link>
      <div class="tn-brand" :class="{ 'tn-brand--end': !(back && !isDesktop) }">
        <img v-if="logoSrc && !logoFailed" :src="logoSrc" alt="" class="tn-logo" @error="logoFailed = true" />
        <router-link to="/" class="tn-name">{{ brand.name || t('home.brand') }}</router-link>
      </div>
      <nav v-if="isDesktop" class="tn-links" :aria-label="t('redeemUi.nav.label')">
        <template v-for="l in links" :key="l.to">
          <!-- 首页内的 #help 是页内锚点，用普通 <a>，router-link 会把它解析成 /#help -->
          <a v-if="l.anchor" :href="l.to" class="tn-link">{{ l.label }}</a>
          <router-link
            v-else
            :to="l.to"
            class="tn-link"
            :class="{ 'tn-link--on': l.current }"
            :aria-current="l.current ? 'page' : undefined"
          >{{ l.label }}</router-link>
        </template>
      </nav>
      <div class="tn-tools">
        <LanguageToggle />
        <ThemeToggle />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import LanguageToggle from '../LanguageToggle.vue'
import ThemeToggle from '../ThemeToggle.vue'
import { siteBrand } from '../../theme'
import { isDesktop } from '../../lib/layoutMode'

const props = withDefaults(defineProps<{
  /** 在首页：常见问题链接用页内锚点 #help */
  home?: boolean
  /** 为真且是窄屏时左侧显示「返回首页」 */
  back?: boolean
}>(), { home: false, back: false })

const { t } = useI18n({ useScope: 'global' })
const route = useRoute()
const brand = siteBrand
const logoFailed = ref(false)
const logoSrc = computed(() => {
  const v = brand.value.logoVersion
  return v ? `/api/v1/public/site/logo?v=${encodeURIComponent(v)}` : ''
})

const links = computed(() => {
  const p = route.path
  return [
    { to: '/', label: t('redeemUi.nav.redeem'), current: p === '/' || p === '/recharge', anchor: false },
    { to: '/batch', label: t('redeemUi.home.footerBatch'), current: p === '/batch', anchor: false },
    { to: '/billing', label: t('redeemUi.home.footerBilling'), current: p === '/billing', anchor: false },
    { to: props.home ? '#help' : '/#help', label: t('redeemUi.home.footerFaq'), current: false, anchor: props.home },
  ]
})
</script>

<style scoped>
.tn-inner { display: flex; align-items: center; gap: 12px; min-height: 48px; min-width: 0; }
.tn-back { display: inline-flex; align-items: center; min-height: 48px; font-size: 15px; white-space: nowrap; }
.tn-brand { display: flex; align-items: center; gap: 8px; min-width: 0; flex: 1; justify-content: center; }
.tn-brand--end { justify-content: flex-start; }
.tn-logo { height: 28px; width: auto; max-width: 96px; object-fit: contain; flex: none; }
.tn-name { min-width: 0; font-size: 15px; font-weight: 700; color: var(--u-ink); text-decoration: none; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.tn-links { display: flex; align-items: center; gap: 8px; flex: none; }
.tn-link { display: inline-flex; align-items: center; min-height: 48px; padding: 0 12px; font-size: 15px; color: var(--u-muted); text-decoration: none; }
.tn-link:hover { color: var(--u-ink); }
.tn-link--on { font-weight: 700; color: var(--u-primary); }
.tn-tools { display: flex; align-items: center; gap: 4px; flex: none; margin-left: auto; }
.tn-links + .tn-tools { margin-left: 0; }
/* 语言、明暗两个按钮是各皮肤共用的 btn-ghost，在用户端改用 --u-* 配色并保证 48px 高 */
.tn-tools :deep(.btn-ghost) {
  box-sizing: border-box; min-height: 48px; min-width: 48px; padding: 0 12px;
  color: var(--u-ink); background: var(--u-surface); border: 1px solid var(--u-line); border-radius: var(--u-radius);
  font-size: 15px; display: inline-flex; align-items: center; justify-content: center; cursor: pointer;
}
/* 电脑布局：整条 surface 底 + 下分隔线，内层最宽 1120px */
.tn--desktop { background: var(--u-surface); border-bottom: 1px solid var(--u-line); }
.tn--desktop .tn-inner { box-sizing: border-box; max-width: 1120px; margin: 0 auto; padding: 8px 24px; }
.tn--desktop .tn-brand { flex: none; max-width: 260px; }
.tn--desktop .tn-links { flex: 1; justify-content: center; }
</style>
