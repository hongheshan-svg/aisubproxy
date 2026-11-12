// 「页面正忙」标记：有进行中的操作、或有未提交的内容时，不要弹「系统已更新，点此刷新」的提示条，免得用户点了丢掉进度。
// 各页面用 useBusy 登记；页面卸载时自动清掉自己的标记。
import { computed, onScopeDispose, ref, watch } from 'vue'

const keys = ref<string[]>([])

export function setBusy(key: string, on: boolean): void {
  const has = keys.value.includes(key)
  if (on && !has) keys.value = [...keys.value, key]
  else if (!on && has) keys.value = keys.value.filter((k) => k !== key)
}

export const isBusy = computed(() => keys.value.length > 0)

/** 组件里（setup 同步阶段）用：source 为真时登记忙碌；组件卸载（作用域销毁）时清掉。 */
export function useBusy(key: string, source: () => boolean): void {
  watch(source, (v) => setBusy(key, !!v), { immediate: true })
  onScopeDispose(() => setBusy(key, false))
}
