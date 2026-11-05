<template>
  <el-dialog
    :model-value="!!password" :title="title" width="min(480px, 96vw)" append-to-body
    :close-on-click-modal="false" :close-on-press-escape="false" @update:model-value="onToggle"
  >
    <div class="space-y-3" data-testid="agent-password-dialog">
      <div class="alert alert-warning text-sm font-semibold" role="alert">
        这个密码只显示这一次，关闭后无法再次查看。请现在复制并转交给代理商；代理商首次登录必须改密码。
      </div>
      <div v-if="username" class="text-sm">登录名：<b class="mono">{{ username }}</b></div>
      <div class="flex items-center gap-2">
        <input class="input mono flex-1" readonly :value="password" @focus="($event.target as HTMLInputElement).select()" />
        <el-button type="primary" @click="copy">复制</el-button>
      </div>
      <span v-if="copied" class="text-sm">已复制</span>
    </div>
    <template #footer>
      <el-button @click="close">我已保存，关闭</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { copyToClipboard } from '../../../lib/clipboard'
import { dialog } from '../../../lib/dialog'

// 密码只放在父组件的内存里：关闭时通知父组件清掉；不写 localStorage、console、URL。
const props = defineProps<{ password: string; username?: string; title?: string }>()
const emit = defineEmits<{ (e: 'close'): void }>()
const copied = ref(false)

watch(() => props.password, () => { copied.value = false })

async function copy() {
  if (await copyToClipboard(props.password)) copied.value = true
  else dialog.toast('复制失败，请手动选中文本框复制', 'err')
}
function close() { copied.value = false; emit('close') }
function onToggle(v: boolean) { if (!v) close() }
onBeforeUnmount(() => emit('close'))
</script>
