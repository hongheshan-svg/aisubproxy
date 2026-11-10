<template>
  <div class="pb-2 space-y-4">
    <AdminPageHeader title="CDK 卡密" class="!mb-0">
      <template #actions>
        <el-popover placement="bottom-end" :width="420" trigger="click">
          <template #reference>
            <el-button size="small">代理换码</el-button>
          </template>
          <div class="space-y-2">
            <div class="text-sm font-medium text-ink">代理换码页</div>
            <p class="text-xs text-muted">失败且未扣款的卡密可换新码，代理密码和每日上限在「代理换码」标签里设置。</p>
            <div class="mono text-xs break-all">{{ agentSwapUrl }}</div>
            <div class="flex gap-2">
              <el-button type="primary" size="small" @click="copyText(agentSwapUrl)">复制链接</el-button>
              <el-button size="small" @click="openAgentSwap">打开</el-button>
            </div>
          </div>
        </el-popover>
      </template>
    </AdminPageHeader>

    <el-tabs v-model="tab" class="cdk-tabs">
      <el-tab-pane label="卡密" name="codes" />
      <el-tab-pane label="代理换码" name="swaps" />
    </el-tabs>

    <IssueProviderBar v-if="tab === 'codes'" v-model:provider="provider" v-model:product="directProduct" :reg="registry"
      :products="productOptions" :notice="providerNotice" :error="registryError" @product-change="applyPreselect" />

    <LocalCdkPanel v-if="tab === 'codes' && codeListKind === 'local'" :key="provider" :provider="provider" :product="directProduct" />
    <section v-else-if="tab === 'codes' && provider" class="card text-sm text-muted" data-testid="no-code-list">
      这家服务商的码请在它自己的后台管理。
    </section>
    <div v-else-if="tab === 'swaps'" class="space-y-4">
      <AgentSwapSettings />
      <AgentExchangePanel />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AdminPageHeader from '../../components/AdminPageHeader.vue'
import LocalCdkPanel from './components/LocalCdkPanel.vue'
import AgentExchangePanel from './components/AgentExchangePanel.vue'
import AgentSwapSettings from './components/AgentSwapSettings.vue'
import IssueProviderBar from './components/IssueProviderBar.vue'
import { useIssueProvider } from '../../lib/useIssueProvider'
import { useRouteTab } from '../../lib/useRouteTab'
import { loadRegistry, providerCodeList, registry } from '../../lib/provider'
import { dialog } from '../../lib/dialog'
import { copyToClipboard } from '../../lib/clipboard'

const tab = useRouteTab('/ops/cdkeys')
const registryError = ref('')
const productOptions = computed(() => {
  const l = registry.value?.products
  return l && l.length ? l : [{ key: 'gpt', label: 'ChatGPT' }, { key: 'x', label: 'X Premium' }, { key: 'grok', label: 'Grok' }, { key: 'claude', label: 'Claude' }]
})

const directProduct = ref('gpt')
// 预选只看产品：本站码的档位由 LocalCdkPanel 自己选，这里按默认档位 plus 预选服务商
const { provider, notice: providerNotice, apply: applyPreselect } = useIssueProvider(directProduct, ref('plus'))
const codeListKind = computed(() => providerCodeList(registry.value, provider.value))

async function loadProviderRegistry() {
  const r = await loadRegistry(true)
  if (!r.ok) { registryError.value = (r.data && (r.data as any).error) || '网络错误'; return }
  registryError.value = ''
  applyPreselect()
}

/** 代理换码隐藏页（完整 URL，便于复制发给代理） */
const agentSwapUrl =
  typeof window !== 'undefined' ? `${window.location.origin}/partner/swap` : '/partner/swap'

function openAgentSwap() {
  window.open(agentSwapUrl, '_blank')
}

async function copyText(t: string) {
  const ok = await copyToClipboard(t)
  dialog.toast(ok ? '已复制' : '复制失败，请在文本框中全选手动复制', ok ? 'ok' : 'err')
}

/** 旧版本把完整码写进过 localStorage / sessionStorage。这里只清理残留，不再读回。 */
function purgeLegacyCaches() {
  try { localStorage.removeItem('cdk_full_code_cache_v1') } catch { /* ignore */ }
  try { sessionStorage.removeItem('cdk_recent_issued_v1') } catch { /* ignore */ }
}

onMounted(async () => {
  purgeLegacyCaches()
  await loadProviderRegistry()
  applyPreselect()
})
</script>
