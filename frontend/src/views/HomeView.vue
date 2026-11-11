<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import UserShell from '../components/redeem/UserShell.vue'
import UserPageHeader from '../components/UserPageHeader.vue'
import CodeEntry from '../components/redeem/CodeEntry.vue'
import UserFooter from '../components/redeem/UserFooter.vue'
import CodeStatusCard from '../components/redeem/CodeStatusCard.vue'
import FaqList from '../components/redeem/FaqList.vue'
import { useCodeLookup } from '../composables/useCodeLookup'
import { homeRoute } from '../lib/homeRoute'

const { t } = useI18n({ useScope: 'global' })
const router = useRouter()
const { busy, result, error, submit, clear } = useCodeLookup()
const code = ref('')

async function onSubmit() {
  await submit(code.value)
  if (!result.value) return
  const route = homeRoute(result.value)
  if (route.kind === 'goto') router.push(route.to)
}

function onReset() {
  code.value = ''
  clear()
}

function goRedeem(c: string) {
  router.push(`/recharge?code=${encodeURIComponent(c)}`)
}
</script>

<template>
  <UserShell>
    <UserPageHeader home :title="t('redeemUi.home.title')" :subtitle="t('redeemUi.home.subtitle')" />
    <main class="u-page home">
      <CodeEntry v-model="code" :busy="busy" :error="error" @submit="onSubmit" />

      <CodeStatusCard
        v-if="result"
        class="home-gap"
        :result="result"
        context="home"
        @redeem="goRedeem"
        @reset="onReset"
      />
      <section v-else class="u-card home-gap" data-testid="code-where">
        <h2 class="home-where-title">{{ t('redeemUi.home.whereTitle') }}</h2>
        <p class="u-help home-where-body">{{ t('redeemUi.home.whereBody') }}</p>
      </section>

      <FaqList />

      <UserFooter home />
    </main>
  </UserShell>
</template>

<style scoped>
.home-gap { margin-top: 16px; }
.home-where-title { margin: 0 0 4px; font-size: 16px; font-weight: 700; }
.home-where-body { margin: 0; }
</style>
