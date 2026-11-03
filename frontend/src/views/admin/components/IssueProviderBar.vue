<template>
  <div class="space-y-2" data-testid="issue-provider-bar">
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
      <label class="text-sm flex items-center gap-2">产品
        <select :value="product" class="input !w-40" :disabled="busy" data-testid="product-select" @change="onProduct(($event.target as HTMLSelectElement).value)">
          <option v-for="p in products" :key="p.key" :value="p.key" :selected="p.key === product">{{ p.label }}</option>
        </select>
      </label>
      <span class="text-sm text-muted">发码服务商</span>
      <el-radio-group :model-value="provider" size="small" :disabled="busy" data-testid="provider-radio" @change="(v: any) => emit('update:provider', String(v))">
        <el-radio-button v-for="p in reg?.providers || []" :key="p.name" :value="p.name" :disabled="!p.products.includes(product)">
          {{ p.display_name || p.name }}
        </el-radio-button>
      </el-radio-group>
      <span v-if="error" class="text-xs text-warn">服务商列表读取失败：{{ error }}</span>
      <span v-else-if="reg" class="text-xs text-muted" data-testid="current-default">
        {{ productLabel(reg, product) }}当前默认：{{ providerLabel(reg.by_product[product] || reg.default) }}
        <router-link to="/ops/providers" class="underline ml-1">去调整</router-link>
      </span>
    </div>
    <div v-if="notice" class="alert alert-info text-sm" role="status" data-testid="provider-notice">{{ notice }}</div>
  </div>
</template>

<script setup lang="ts">
import { productLabel, providerLabel, type ProductInfo, type Registry } from '../../../lib/provider'

defineProps<{
  reg: Registry | null
  products: ProductInfo[]
  provider: string
  product: string
  notice?: string
  error?: string
  busy?: boolean
}>()
const emit = defineEmits<{
  (e: 'update:provider', v: string): void
  (e: 'update:product', v: string): void
  (e: 'product-change'): void
}>()

function onProduct(v: string) {
  emit('update:product', v)
  emit('product-change')
}
</script>
