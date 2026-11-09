<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import UserShell from '../components/redeem/UserShell.vue'
import UserPageHeader from '../components/UserPageHeader.vue'
import CodeEntry from '../components/redeem/CodeEntry.vue'
import UserFooter from '../components/redeem/UserFooter.vue'
import CodeStatusCard from '../components/redeem/CodeStatusCard.vue'
import SessionHelp from '../components/SessionHelp.vue'
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

// session 和 claude 的答案是固定说明（带链接或两段），在模板里单独渲染
const faqs = computed(() => ['entry', 'pending', 'session', 'claude', 'used', 'failed', 'agent'].map((key) => ({
  key,
  q: t(`home.faq.${key}.q`),
  a: key === 'session' || key === 'claude' ? '' : t(`home.faq.${key}.a`),
})))
</script>

<template>
  <UserShell>
    <UserPageHeader :title="t('redeemUi.home.title')" :subtitle="t('redeemUi.home.subtitle')" />
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

      <section id="help" class="home-faq" aria-labelledby="faq-title">
        <h2 id="faq-title" class="home-faq-title">{{ t('home.faq.title') }}</h2>
        <details v-for="item in faqs" :key="item.key" class="u-card faq-item">
          <summary class="faq-q">{{ item.q }}</summary>
          <div class="faq-a">
            <template v-if="item.key === 'session'">
              <p><SessionHelp /></p>
            </template>
            <template v-else-if="item.key === 'claude'">
              <p>{{ t('claude.hint') }}</p>
              <p>{{ t('claude.findOrg') }}</p>
            </template>
            <p v-else>{{ item.a }}</p>
          </div>
        </details>
      </section>

      <UserFooter home />
    </main>
  </UserShell>
</template>

<style scoped>
.home-gap { margin-top: 16px; }
.home-where-title { margin: 0 0 4px; font-size: 16px; font-weight: 700; }
.home-where-body { margin: 0; }
.home-faq { margin-top: 32px; display: flex; flex-direction: column; gap: 10px; }
.home-faq-title { margin: 0 0 4px; font-size: 20px; font-weight: 700; }
.faq-item { padding: 12px 16px; }
.faq-q { cursor: pointer; list-style: none; display: flex; justify-content: space-between; align-items: center; gap: 12px; min-height: 28px; font-size: 16px; font-weight: 600; }
.faq-q::-webkit-details-marker { display: none; }
.faq-q::after { content: '+'; flex-shrink: 0; font-size: 20px; line-height: 1; color: var(--u-primary); }
details[open] > .faq-q::after { content: '−'; }
.faq-a { margin-top: 10px; font-size: 15px; color: var(--u-muted); }
.faq-a p { margin: 0 0 8px; }
</style>
