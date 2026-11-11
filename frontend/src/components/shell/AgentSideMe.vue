<template>
  <!-- 代理商侧栏底部的账号和余额。展开：名称 + 余额；折叠成窄条：只留小号金额（鼠标悬停看全称），代理商不展开侧栏也能看到余额 -->
  <div v-if="!rail" class="sn-me">
    <div class="sn-me-name">{{ name }}</div>
    <div class="sn-me-bal">余额 {{ fmtUsd(balanceCents) }}</div>
  </div>
  <div v-else class="sn-me-rail" :title="`余额 ${fmtUsd(balanceCents)}`" data-testid="rail-balance">{{ fmtUsd(balanceCents) }}</div>
</template>

<script setup lang="ts">
import { fmtUsd } from '../../lib/agentMoney'

defineProps<{ name: string; balanceCents: number; rail: boolean }>()
</script>

<style scoped>
/* 颜色全用侧栏变量，不用浅色皮肤的变量 */
.sn-me { padding: 2px 8px 6px; color: var(--side-ink); font-size: 13px; min-width: 0; }
.sn-me-name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sn-me-bal { color: var(--side-muted); font-size: 12px; }
.sn-me-rail { padding: 2px 2px 6px; align-self: stretch; margin-inline: -8px; color: var(--side-muted); font-size: 11px; line-height: 1.2; text-align: center; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
