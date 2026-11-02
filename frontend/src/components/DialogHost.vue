<template>
  <!-- Toast 队列 -->
  <div class="toast-stack fixed z-[9999] flex flex-col gap-2 pointer-events-none">
    <transition-group name="toast">
      <div v-for="t in dialogState.toasts" :key="t.id" class="toast" :class="`toast-${t.type}`" role="status" :title="t.detail ? `原始信息：${t.detail}` : undefined">
        <span class="toast-icon">{{ t.type === 'err' ? '✕' : t.type === 'warn' ? '!' : t.type === 'info' ? 'i' : '✓' }}</span>
        <span>
          {{ t.message }}
          <small v-if="t.detail" class="toast-detail">原始信息：{{ t.detail }}</small>
        </span>
      </div>
    </transition-group>
  </div>

  <!-- 弹窗 -->
  <div v-if="d" class="modal-mask fixed inset-0 z-[9998] flex items-center justify-center p-4" @click.self="onCancel">
    <div class="card card-solid w-full max-w-md" role="dialog" aria-modal="true" @keydown.enter="onEnter" @keydown.esc="onCancel">
      <h3 class="text-lg font-semibold text-ink mb-2">{{ d.title }}</h3>
      <div v-if="d.message" class="text-sm text-muted whitespace-pre-line mb-4">{{ d.message }}</div>

      <input v-if="d.kind === 'prompt'" ref="inputEl" v-model="d.value" :placeholder="d.placeholder"
        class="input w-full mb-4" @keydown.enter.prevent="onEnter" />

      <div v-if="d.kind === 'select'" class="flex flex-col gap-2 mb-4">
        <button v-for="(o, i) in d.options" :key="i"
          class="text-left px-4 py-3 rounded-lg border border-[color:var(--brd)] hover:border-[color:var(--primary)] hover:bg-[color:var(--primary-soft)] transition"
          @click="resolveActive(o.value)">
          <div class="text-ink font-medium">{{ o.label }}</div>
          <div v-if="o.desc" class="text-xs text-muted mt-0.5">{{ o.desc }}</div>
        </button>
      </div>

      <div class="flex justify-end gap-2">
        <button v-if="d.kind !== 'alert'" class="btn-secondary" @click="onCancel">{{ d.cancelText || '取消' }}</button>
        <button v-if="d.kind === 'alert' || d.kind === 'confirm' || d.kind === 'prompt'"
          :class="d.danger ? 'btn-danger' : 'btn-primary'" @click="onEnter">{{ d.okText || '确定' }}</button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch, nextTick } from 'vue'
import { dialogState, resolveActive } from '../lib/dialog'

const d = computed(() => dialogState.active)
const inputEl = ref<HTMLInputElement | null>(null)

watch(d, async (val) => {
  if (val && val.kind === 'prompt') {
    await nextTick()
    inputEl.value?.focus()
    inputEl.value?.select()
  }
})

function onEnter() {
  const a = d.value
  if (!a) return
  if (a.kind === 'alert' || a.kind === 'confirm') resolveActive(true)
  else if (a.kind === 'prompt') resolveActive(a.value)
}
function onCancel() {
  const a = d.value
  if (!a) return
  resolveActive(a.kind === 'confirm' || a.kind === 'alert' ? false : null)
}
</script>

<style scoped>
.toast {
  pointer-events: auto;
  display: flex;
  align-items: center;
  gap: 10px;
  max-width: 24rem;
  padding: 10px 14px;
  border-radius: var(--radius-md);
  font-size: 13px;
  word-break: break-all;
  color: var(--ink);
  background: var(--surface-solid);
  border: 1px solid var(--brd);
  border-left: 3px solid var(--tc);
  box-shadow: var(--shadow);
}
.toast-icon {
  flex: none;
  display: inline-grid;
  place-items: center;
  width: 20px;
  height: 20px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 800;
  color: var(--tc);
  background: color-mix(in srgb, var(--tc) 16%, transparent);
}
.toast-ok { --tc: var(--good); }
.toast-err { --tc: var(--err); }
.toast-warn { --tc: var(--warn); }
.toast-info { --tc: var(--info); }
.toast-enter-active, .toast-leave-active { transition: opacity .25s ease, transform .25s ease; }
.toast-enter-from, .toast-leave-to { opacity: 0; transform: translateX(20px); }
</style>

<style>
.toast-stack { top: calc(var(--topbar-h, 56px) + 12px); right: 16px; max-width: calc(100vw - 32px); }
.toast-detail { display: block; margin-top: 2px; font-size: 11px; opacity: .75; word-break: break-all; font-family: var(--font-mono); }
@media (max-width: 640px) {
  .toast-stack { left: 12px; right: 12px; top: auto; bottom: 16px; max-width: none; }
}
</style>
