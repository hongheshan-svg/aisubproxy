<script setup lang="ts">
// 按设备给的「怎么拿到登录信息」教程（规格 §6.3）。三个标签可切换，默认显示 initial。
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

type Device = 'ios' | 'android' | 'desktop'
const props = defineProps<{ initial: Device }>()
const { t } = useI18n({ useScope: 'global' })
const devices: Device[] = ['ios', 'android', 'desktop']
const active = ref<Device>(props.initial)
</script>

<template>
  <section class="guide" data-testid="device-guide">
    <div class="guide-tabs" role="tablist">
      <button
        v-for="d in devices"
        :key="d"
        type="button"
        role="tab"
        class="guide-tab"
        :class="{ 'is-active': active === d }"
        :aria-selected="active === d ? 'true' : 'false'"
        :data-testid="`guide-tab-${d}`"
        @click="active = d"
      >{{ t(`redeemUi.guide.${d}.tab`) }}</button>
    </div>
    <ol class="guide-steps" role="tabpanel">
      <li>{{ t(`redeemUi.guide.${active}.step1`) }}</li>
      <li>{{ t(`redeemUi.guide.${active}.step2`) }}</li>
      <li>{{ t(`redeemUi.guide.${active}.step3`) }}</li>
    </ol>
    <a
      class="u-btn-secondary guide-open"
      href="https://chatgpt.com/api/auth/session"
      target="_blank"
      rel="noopener noreferrer"
      data-testid="guide-open"
    >{{ t('redeemUi.guide.open') }}</a>
    <p class="guide-safety">{{ t('redeemUi.guide.safety') }}</p>
  </section>
</template>

<style scoped>
.guide { display: flex; flex-direction: column; gap: 12px; }
.guide-tabs { display: flex; gap: 8px; }
.guide-tab {
  flex: 1; min-height: 48px; padding: 0 12px; border-radius: 14px;
  border: 1px solid var(--u-line); background: transparent; color: var(--u-ink);
  font: inherit; font-size: 16px; cursor: pointer;
}
.guide-tab.is-active { background: var(--u-primary); border-color: var(--u-primary); color: var(--u-on-primary); }
.guide-steps { margin: 0; padding-left: 22px; display: flex; flex-direction: column; gap: 8px; font-size: 16px; line-height: 1.6; color: var(--u-ink); }
.guide-open { text-decoration: none; text-align: center; }
.guide-safety { margin: 0; font-size: 14px; color: var(--u-muted); }
</style>
