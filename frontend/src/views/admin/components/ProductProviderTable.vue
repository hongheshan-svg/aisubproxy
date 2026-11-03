<template>
  <div class="space-y-2" data-testid="product-provider-table">
    <div class="overflow-x-auto">
      <table class="w-full text-sm product-table">
        <thead>
          <tr class="text-left text-muted">
            <th class="py-1.5 pr-3 font-normal whitespace-nowrap">产品</th>
            <th class="py-1.5 pr-3 font-normal">发码服务商</th>
            <th class="py-1.5 font-normal">实际生效</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in reg.products" :key="p.key" :data-product="p.key" class="align-top border-t" style="border-color: var(--brd)">
            <td class="py-2 pr-3 whitespace-nowrap font-medium text-ink">{{ p.label }}</td>
            <td class="py-2 pr-3">
              <select class="input !w-full sm:!w-56" :disabled="disabled" :value="modelValue[p.key] || ''"
                :aria-label="`${p.label} 的发码服务商`" @change="onPick(p.key, ($event.target as HTMLSelectElement).value)">
                <option value="" :selected="!modelValue[p.key]">跟随全局默认（{{ providerName(defaultName) }}）</option>
                <option v-for="s in optionsFor(p.key)" :key="s.name" :value="s.name" :selected="modelValue[p.key] === s.name">{{ s.display_name || s.name }}</option>
              </select>
              <div v-if="staleValue(p.key)" class="text-xs text-warn mt-1">已选的 {{ staleValue(p.key) }} 不在可选列表里，保存时会提示。</div>
            </td>
            <td class="py-2">
              <span data-testid="effective">{{ providerName(effectiveOf(p.key)) }}</span>
              <div v-if="gaps(p.key).length" class="text-xs text-muted mt-1" data-testid="unsupported">
                不支持：<span v-for="(g, i) in gaps(p.key)" :key="g.plan"><template v-if="i > 0">、</template>{{ g.comment || g.plan }}</span>
              </div>
              <div v-else-if="effectiveOf(p.key) && !supportsProduct(effectiveOf(p.key), p.key)" class="text-xs text-err mt-1">
                {{ providerName(effectiveOf(p.key)) }} 不支持这个产品，发码会被拒绝
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import {
  findProvider, unsupportedPlans, type ProviderInfo, type Registry,
} from '../../../lib/provider'

const props = defineProps<{
  reg: Registry
  /** 当前编辑中的全局默认与按产品选择（未保存的草稿） */
  defaultName: string
  modelValue: Record<string, string>
  disabled?: boolean
}>()
const emit = defineEmits<{ (e: 'update:modelValue', v: Record<string, string>): void }>()

const providerName = (n: string) => findProvider(props.reg, n)?.display_name || n || '—'
const optionsFor = (product: string): ProviderInfo[] => props.reg.providers.filter((s) => s.products.includes(product))
const effectiveOf = (product: string) => props.modelValue[product] || props.defaultName
const supportsProduct = (name: string, product: string) => !!findProvider(props.reg, name)?.products.includes(product)
const gaps = (product: string) => {
  const n = effectiveOf(product)
  return n && supportsProduct(n, product) ? unsupportedPlans(props.reg, n, product) : []
}
// 库里存的服务商名已经不在列表里（后端会按「跟随全局默认」处理并在 warnings 说明）
const staleValue = (product: string) => {
  const v = props.modelValue[product]
  return v && !optionsFor(product).some((s) => s.name === v) ? v : ''
}
function onPick(product: string, v: string) {
  emit('update:modelValue', { ...props.modelValue, [product]: v })
}
</script>
