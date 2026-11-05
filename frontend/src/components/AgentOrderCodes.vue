<template>
  <div v-if="items.length" class="space-y-2">
    <p v-if="notice" class="text-sm text-amber-600" role="status">{{ notice }}</p>
    <ul v-if="items.length" class="text-xs font-mono space-y-0.5 max-h-60 overflow-auto">
      <li v-for="c in items" :key="c.id" :class="isStaleCode(c) ? 'opacity-50' : ''" :data-stale="isStaleCode(c) ? '1' : '0'">
        <span class="break-all">{{ c.unavailable ? UNAVAILABLE_TEXT : c.code }}</span>
        <span v-if="isStaleCode(c)" class="ml-2">{{ CODE_STATUS_LABELS[c.status || ''] || c.status }}<template v-if="c.replaced_by">（已换码，新码 #{{ c.replaced_by }}）</template></span>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { UNAVAILABLE_TEXT, isStaleCode, staleCodes, staleCodesNotice, type ExportedCode } from '../lib/agentExport'
import { CODE_STATUS_LABELS } from '../lib/agentViews'

const props = defineProps<{ codes: ExportedCode[] }>()
const items = computed(() => staleCodes(props.codes))
const notice = computed(() => staleCodesNotice(props.codes))
</script>
