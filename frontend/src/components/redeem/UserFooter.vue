<script setup lang="ts">
import { useI18n } from 'vue-i18n'

// home 为真时在首页内平滑滚动到常见问题；其他页面跳到首页的 #help
defineProps<{ home?: boolean }>()
const { t } = useI18n({ useScope: 'global' })

function toFaq(e: Event) {
  e.preventDefault()
  document.getElementById('help')?.scrollIntoView({ behavior: 'smooth' })
}
</script>

<template>
  <footer class="u-foot">
    <a v-if="home" href="#help" class="u-link" @click="toFaq">{{ t('redeemUi.home.footerFaq') }}</a>
    <router-link v-else to="/#help" class="u-link">{{ t('redeemUi.home.footerFaq') }}</router-link>
    <router-link to="/batch" class="u-link">{{ t('redeemUi.home.footerBatch') }}</router-link>
    <router-link to="/billing" class="u-link">{{ t('redeemUi.home.footerBilling') }}</router-link>
  </footer>
</template>

<style scoped>
.u-foot { margin-top: 32px; display: flex; flex-wrap: wrap; gap: 8px 20px; font-size: 14px; }
.u-foot a { display: inline-flex; align-items: center; min-height: 32px; }
</style>
