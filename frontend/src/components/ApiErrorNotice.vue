<template>
  <div v-if="message" class="aen" :class="m.config ? 'alert alert-warning' : 'alert alert-error'" role="alert">
    <div class="aen-row">
      <span class="aen-text">{{ m.text }}</span>
      <router-link v-if="m.link" :to="m.link.to" class="btn-secondary !py-1 !px-3 text-sm">{{ m.link.label }}</router-link>
    </div>
    <details v-if="m.mapped" class="aen-raw">
      <summary>原始信息（排障用）</summary>
      <code class="mono">{{ m.raw }}</code>
    </details>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { mapApiError } from '../lib/errors'

const props = defineProps<{ message?: string }>()
const m = computed(() => mapApiError(props.message || ''))
</script>

<style scoped>
.aen-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; flex-wrap: wrap; }
.aen-raw { margin-top: 6px; font-size: 12px; opacity: .85; }
.aen-raw summary { cursor: pointer; }
.aen-raw code { display: block; margin-top: 4px; word-break: break-all; }
</style>
