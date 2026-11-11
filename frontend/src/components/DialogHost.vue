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
  <!-- 用户端页面（不在 UserShell 里）的弹窗：外面套一层 user-ui 取 --u-* 配色，display: contents 不影响布局 -->
  <div v-if="d" class="dlg-wrap" :class="{ 'user-ui': userSide }">
  <div class="modal-mask fixed inset-0 z-[9998] flex items-center justify-center p-4" @click.self="onCancel">
    <div ref="panelEl" :class="userSide ? 'u-card dlg-user w-full max-w-md' : 'card card-solid w-full max-w-md'" role="dialog" aria-modal="true" tabindex="-1"
      @keydown.enter="onKeyEnter" @keydown.esc="onCancel" @keydown.tab="onTab">
      <h3 :class="userSide ? 'dlg-title' : 'text-lg font-semibold text-ink mb-2'">{{ d.title }}</h3>
      <div v-if="d.message" :class="userSide ? 'dlg-msg whitespace-pre-line' : 'text-sm text-muted whitespace-pre-line mb-4'">{{ d.message }}</div>

      <input v-if="d.kind === 'prompt'" ref="inputEl" v-model="d.value" :placeholder="d.placeholder"
        :class="userSide ? 'u-input dlg-input' : 'input w-full mb-4'" @keydown.enter.prevent="onEnter" />

      <div v-if="d.kind === 'select'" class="flex flex-col gap-2 mb-4">
        <button v-for="(o, i) in d.options" :key="i"
          class="text-left px-4 py-3 rounded-lg border border-[color:var(--brd)] hover:border-[color:var(--primary)] hover:bg-[color:var(--primary-soft)] transition"
          @click="resolveActive(o.value)">
          <div class="text-ink font-medium">{{ o.label }}</div>
          <div v-if="o.desc" class="text-xs text-muted mt-0.5">{{ o.desc }}</div>
        </button>
      </div>

      <div class="flex justify-end gap-2" :class="{ 'dlg-actions': userSide }">
        <button v-if="d.kind !== 'alert'" ref="cancelEl" :class="userSide ? 'u-btn-secondary' : 'btn-secondary'" @click="onCancel">{{ d.cancelText || '取消' }}</button>
        <button v-if="d.kind === 'alert' || d.kind === 'confirm' || d.kind === 'prompt'" ref="okEl"
          :class="userSide ? ['u-btn-primary', { 'dlg-danger': d.danger }] : (d.danger ? 'btn-danger' : 'btn-primary')" @click="onEnter">{{ d.okText || '确定' }}</button>
      </div>
    </div>
  </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch, nextTick } from 'vue'
import { useRoute } from 'vue-router'
import { dialogState, resolveActive } from '../lib/dialog'
import { createFocusReturn, enterConfirms, initialFocus, nextFocusIndex } from '../lib/dialogFocus'
import { isAdminPath } from '../router/routes'

const route = useRoute()
const userSide = computed(() => !isAdminPath(route.path) && !route.path.startsWith('/agent'))

const d = computed(() => dialogState.active)
const panelEl = ref<HTMLElement | null>(null)
const inputEl = ref<HTMLInputElement | null>(null)
const cancelEl = ref<HTMLButtonElement | null>(null)
const okEl = ref<HTMLButtonElement | null>(null)
// 弹出前的焦点（通常是点开这个框的按钮），关闭后还回去
const focusReturn = createFocusReturn(() => (typeof document === 'undefined' ? null : (document.activeElement as HTMLElement | null)))

// 弹出后焦点移到框内：确认、选择默认在「取消」上，提示在「确定」上，输入在输入框上
watch(d, async (val, old) => {
  if (val && !old) focusReturn.save()
  if (val) {
    await nextTick()
    const target = initialFocus(val.kind)
    if (target === 'input') { inputEl.value?.focus(); inputEl.value?.select() }
    else (target === 'cancel' ? cancelEl.value : okEl.value)?.focus()
    if (typeof document !== 'undefined' && !panelEl.value?.contains(document.activeElement)) panelEl.value?.focus()
  } else if (old) focusReturn.restore()
})

// Tab 在框内循环，不跑到底下的页面上
function onTab(e: KeyboardEvent) {
  const panel = panelEl.value
  if (!panel) return
  const items = Array.from(panel.querySelectorAll<HTMLElement>('button, input, select, textarea, [href]')).filter((el) => !el.hasAttribute('disabled'))
  const i = nextFocusIndex(items.length, items.indexOf(document.activeElement as HTMLElement), e.shiftKey)
  e.preventDefault()
  if (i >= 0) items[i].focus()
}

// 焦点在按钮上按回车交给按钮自己（在「取消」上按回车不能变成确定）
function onKeyEnter(e: KeyboardEvent) {
  if (!enterConfirms(e.target as HTMLElement | null)) return
  onEnter()
}

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
.dlg-wrap { display: contents; }
.dlg-user { padding: 20px; }
.dlg-title { margin: 0 0 8px; font-size: 20px; font-weight: 700; line-height: 1.4; color: var(--u-ink); }
.dlg-msg { margin: 0 0 16px; font-size: 16px; line-height: 1.6; color: var(--u-muted); }
.dlg-input { margin-bottom: 16px; }
.dlg-actions > button { flex: 1 1 0; font-size: 16px; }
/* 窄屏两个按钮上下排，主操作在上，字不换行 */
@media (max-width: 480px) {
  .dlg-actions { flex-direction: column-reverse; }
  .dlg-actions > button { flex: none; width: 100%; }
}
.dlg-actions > .dlg-danger { background: var(--u-error-ink); border-color: var(--u-error-ink); color: var(--u-surface); }
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
