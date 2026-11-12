<template>
  <Transition name="nv-banner">
    <div v-if="showNewVersionBanner" class="nv-banner" role="status" aria-live="polite">
      <button type="button" class="nv-btn" @click="reloadPage">
        <span class="nv-dot" aria-hidden="true"></span>
        {{ t('newVersion.banner') }}
      </button>
    </div>
  </Transition>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { showNewVersionBanner } from '../lib/newVersionWatch'
import { reloadPage } from '../lib/pageReload'

const { t } = useI18n()
</script>

<style scoped>
/* 层级 1600：高于手机侧栏抽屉（1500），低于 Element Plus 弹出层（2001 起）和项目自己的确认框 / 提示（9998 / 9999），
   这样提示条不会盖住弹窗和下拉，也不会被抽屉挡住。 */
.nv-banner {
  position: fixed;
  left: 50%;
  bottom: calc(16px + env(safe-area-inset-bottom, 0px));
  transform: translateX(-50%);
  z-index: 1600;
  max-width: calc(100vw - 32px);
}
.nv-btn {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  max-width: 100%;
  padding: 8px 16px;
  font-size: 13px;
  line-height: 1.4;
  color: var(--ink);
  background: var(--surface-solid);
  border: 1px solid var(--primary);
  border-radius: 999px;
  box-shadow: 0 6px 20px rgba(0, 0, 0, 0.18);
  cursor: pointer;
}
.nv-btn:hover { border-color: var(--ink-2); }
.nv-btn:focus-visible { outline: 2px solid var(--primary); outline-offset: 2px; }
.nv-dot {
  flex: none;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--primary);
}
.nv-banner-enter-active, .nv-banner-leave-active { transition: opacity 0.2s ease, transform 0.2s ease; }
.nv-banner-enter-from, .nv-banner-leave-to { opacity: 0; transform: translate(-50%, 8px); }
@media (prefers-reduced-motion: reduce) {
  .nv-banner-enter-active, .nv-banner-leave-active { transition: none; }
}
</style>
