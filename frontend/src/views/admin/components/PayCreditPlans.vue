<template>
  <div v-if="creditPlans.length" class="space-y-4" data-testid="credit-plans">
    <div>
      <div class="font-semibold">Codex 点数</div>
      <p class="text-xs text-muted mt-1">
        点数按用户账号的计费币种付款，码的地区必须和账号计费币种一致。预期金额只用来核对配置；
        付款时支付页金额低于最低金额、或超过上限，都会在注资前放弃。最低金额不能高于预期金额或上限，上限不能低于预期金额。
        {{ BUILTIN_NOTE }}注资金额没有默认值，请按汇率换算后填写。
      </p>
    </div>
    <div v-for="p in creditPlans" :key="p.key" class="space-y-2" :data-plan="p.key">
      <div class="font-semibold flex flex-wrap items-center gap-2">
        {{ p.label || p.key }}
        <el-tag size="small" :type="p.rows.some((r) => r.enabled) ? 'success' : 'info'">已启用 {{ p.rows.filter((r) => r.enabled).length }}/{{ p.rows.length }}</el-tag>
        <el-button v-for="r in builtinRegions(p, builtin)" :key="r" size="small" link type="primary" :data-testid="`builtin-${p.key}-${r}`" @click="applyBuiltinPrice(p, r, builtin)">{{ r }} 恢复内置价格</el-button>
      </div>
      <el-table :data="visibleRows(p)" size="small" border>
        <el-table-column label="地区" width="70" prop="region" />
        <el-table-column label="启用" width="70">
          <template #default="{ row }"><el-switch v-model="row.enabled" /></template>
        </el-table-column>
        <el-table-column label="币种" width="110">
          <template #default="{ row }"><el-input v-model="row.currency" size="small" maxlength="3" autocomplete="off" /></template>
        </el-table-column>
        <el-table-column label="预期金额" min-width="160">
          <template #default="{ row }">
            <el-input-number v-model="row.expect" size="small" :min="0" :precision="currencyDigits(row.currency)" :controls="false" style="width: 130px" />
          </template>
        </el-table-column>
        <el-table-column label="最低金额" min-width="170">
          <template #default="{ row }">
            <el-input-number v-model="row.floor" size="small" :min="0" :precision="currencyDigits(row.currency)" :controls="false" style="width: 130px" />
            <div v-if="rowFloorProblem(row)" class="text-xs text-warn">{{ rowFloorProblem(row) }}</div>
          </template>
        </el-table-column>
        <el-table-column label="应付金额上限" min-width="170">
          <template #default="{ row }">
            <el-input-number v-model="row.cap" size="small" :min="0" :precision="currencyDigits(row.currency)" :controls="false" style="width: 130px" />
            <div v-if="rowCapBelow(row)" class="text-xs text-warn">上限低于预期金额，保存会被拒绝</div>
          </template>
        </el-table-column>
        <el-table-column label="注资（美元）" min-width="150">
          <template #default="{ row }">
            <el-input-number v-model="row.fund" size="small" :min="0" :precision="2" :controls="false" style="width: 110px" />
          </template>
        </el-table-column>
      </el-table>
      <el-button v-if="showAll[p.key] || hiddenCount(p) > 0" size="small" link type="primary" @click="showAll[p.key] = !showAll[p.key]">
        {{ showAll[p.key] ? '收起未启用的地区' : `展开未启用的地区（${hiddenCount(p)} 个）` }}
      </el-button>
    </div>
  </div>
</template>

<script setup lang="ts">
// 付款配置里的 Codex 点数档：每档每个地区可配预期金额、最低金额、上限和注资。编辑的是父组件的表单（同一份 plansForm），随「保存」一起提交。
import { computed, reactive } from 'vue'
import {
  BUILTIN_NOTE, applyBuiltinPrice, builtinRegions, capBelowExpect, currencyDigits, floorProblem, isCreditPlan, toMinor,
  type BuiltinPrices, type FormPlan, type FormRow,
} from '../../../lib/pay'

const plans = defineModel<FormPlan[]>({ required: true })
const props = defineProps<{ builtin?: BuiltinPrices }>()
const creditPlans = computed(() => plans.value.filter((p) => isCreditPlan(p)))

// 未启用的地区默认折叠；启用过的行不会因为临时关掉开关而消失（与订阅套餐的表格一致）
const showAll = reactive<Record<string, boolean>>({})
function visibleRows(p: FormPlan) {
  return p.rows.filter((r) => showAll[p.key] || r.enabled || r.initEnabled || !!props.builtin?.[p.key]?.[r.region])
}
function hiddenCount(p: FormPlan) {
  return p.rows.length - visibleRows(p).length
}

function rowCapBelow(row: FormRow) {
  const cur = row.currency.trim().toUpperCase()
  return capBelowExpect(toMinor(row.cap || 0, cur), toMinor(row.expect || 0, cur))
}

function rowFloorProblem(row: FormRow) {
  const cur = row.currency.trim().toUpperCase()
  return floorProblem(toMinor(row.floor || 0, cur), toMinor(row.expect || 0, cur), toMinor(row.cap || 0, cur))
}
</script>
