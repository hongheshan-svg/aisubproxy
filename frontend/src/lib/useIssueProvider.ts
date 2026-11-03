// 发码弹窗的服务商选择：按产品和档位预选，预选那家不支持时自动改选并提示。
import { ref, watch, type Ref } from 'vue'
import { pickIssueProvider, registry, supportsPlan } from './provider'

export function useIssueProvider(product: Ref<string>, plan: Ref<string>, planLabel: (plan: string) => string = (p) => p) {
  const provider = ref('')
  const notice = ref('')

  /** 重新预选：产品配置优先，其次全局默认；不支持当前档位时改选第一家支持的。 */
  function apply() {
    if (!registry.value) return
    const pick = pickIssueProvider(registry.value, product.value, plan.value, planLabel)
    provider.value = pick.provider
    notice.value = pick.notice
  }

  // 手动换了档位而当前服务商不支持它时，自动改选并提示；当前服务商支持就不动，免得打断人工选择
  watch(plan, (p) => {
    if (!registry.value || !provider.value) return
    if (!supportsPlan(registry.value, provider.value, product.value, p)) apply()
  })

  return { provider, notice, apply }
}
