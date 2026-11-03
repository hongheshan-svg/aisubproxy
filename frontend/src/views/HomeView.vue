<template>
  <div class="u-page">
    <UserPageHeader :eyebrow="t('hero.redeemEyebrow')" :title="t('home.heroTitle')" :subtitle="t('home.heroSub')" badges />
    <div class="u-main max-w-6xl space-y-16">
      <!-- 三步充值 -->
      <section class="card animate-slideInUp" data-testid="quick-steps" aria-labelledby="quick-title">
        <div class="text-center">
          <h2 id="quick-title" class="text-2xl font-bold text-ink">{{ t('home.quick.title') }}</h2>
          <p class="mt-2 text-sm text-muted">{{ t('home.quick.sub') }}</p>
        </div>
        <ol class="mt-8 grid gap-6 md:grid-cols-3">
          <li v-for="(s, i) in quickSteps" :key="s.title" class="space-y-2">
            <div class="flex items-center gap-3">
              <span class="grid h-8 w-8 shrink-0 place-items-center rounded-full text-sm font-bold" style="background: var(--primary-soft); color: var(--primary)">{{ i + 1 }}</span>
              <h3 class="font-bold text-ink">{{ s.title }}</h3>
            </div>
            <p class="text-sm text-muted leading-relaxed">{{ s.desc }}</p>
            <ul v-if="s.creds" class="space-y-1 text-sm text-muted">
              <li v-for="c in s.creds" :key="c" class="flex gap-2">
                <span aria-hidden="true" style="color: var(--primary)">•</span>
                <span>{{ c }}</span>
              </li>
            </ul>
          </li>
        </ol>
        <div class="mt-8 flex flex-wrap justify-center gap-3">
          <router-link to="/recharge" class="btn-primary">{{ t('home.quick.ctaSingle') }}</router-link>
          <router-link to="/batch" class="btn-secondary">{{ t('home.quick.ctaBatch') }}</router-link>
        </div>
      </section>

      <!-- 入口 -->
      <section aria-labelledby="services-title">
        <h2 id="services-title" class="text-2xl font-bold text-ink mb-6 text-center">{{ t('home.servicesTitle') }}</h2>
        <div class="grid md:grid-cols-2 lg:grid-cols-4 gap-6">
          <router-link
            v-for="svc in services"
            :key="svc.to"
            :to="svc.to"
            class="card card-hover group svc-card"
          >
            <span class="grid h-12 w-12 place-items-center rounded-2xl" style="background: var(--primary-soft); color: var(--primary)" v-html="svc.icon" />
            <h3 class="mt-4 text-xl font-bold text-ink">{{ svc.title }}</h3>
            <p class="mt-1 text-sm text-subtle">{{ svc.en }}</p>
            <div class="mt-5 space-y-2 text-sm text-muted">
              <div v-for="line in svc.points" :key="line" class="flex items-center gap-2">
                <svg xmlns="http://www.w3.org/2000/svg" class="h-4 w-4 shrink-0" style="color: var(--primary)" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12" /></svg>
                <span>{{ line }}</span>
              </div>
            </div>
            <div class="mt-6 text-sm font-medium app-link">{{ svc.cta }} →</div>
          </router-link>
        </div>
      </section>

      <!-- 兑换后怎么查 -->
      <section class="pt-12 border-t bd" data-testid="query-guide" aria-labelledby="query-title">
        <div class="text-center mb-8">
          <h2 id="query-title" class="text-2xl font-bold text-ink">{{ t('home.query.title') }}</h2>
          <p class="mt-2 text-sm text-muted">{{ t('home.query.sub') }}</p>
        </div>
        <div class="grid gap-6 md:grid-cols-3">
          <div v-for="q in queries" :key="q.title" class="card flex flex-col">
            <h3 class="font-bold text-ink">{{ q.title }}</h3>
            <p class="mt-2 flex-1 text-sm text-muted leading-relaxed">{{ q.desc }}</p>
            <router-link :to="q.to" class="mt-4 text-sm font-medium app-link">{{ q.cta }} →</router-link>
          </div>
        </div>
      </section>

      <!-- 常见问题 -->
      <section id="help" class="pt-12 border-t bd" aria-labelledby="faq-title">
        <div class="text-center mb-8">
          <h2 id="faq-title" class="text-2xl font-bold text-ink">{{ t('home.faq.title') }}</h2>
          <p class="mt-2 text-sm text-muted">{{ t('home.faq.sub') }}</p>
        </div>
        <div class="mx-auto max-w-3xl space-y-3">
          <details v-for="item in faqs" :key="item.key" class="card faq-item">
            <summary class="faq-q font-semibold text-ink">{{ item.q }}</summary>
            <div class="mt-3 space-y-2 text-sm text-muted leading-relaxed">
              <template v-if="item.key === 'session'">
                <p><SessionHelp /></p>
                <p>{{ t('sessionHelp.tokenRule') }}</p>
              </template>
              <template v-else-if="item.key === 'claude'">
                <p>{{ t('claude.hint') }}</p>
                <p>{{ t('claude.findOrg') }}</p>
              </template>
              <p v-else>{{ item.a }}</p>
            </div>
          </details>
        </div>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import UserPageHeader from '../components/UserPageHeader.vue'
import SessionHelp from '../components/SessionHelp.vue'

const { t } = useI18n({ useScope: 'global' })

const quickSteps = computed(() => [
  { title: t('home.quick.s1.title'), desc: t('home.quick.s1.desc') },
  {
    title: t('home.quick.s2.title'),
    desc: t('home.quick.s2.desc'),
    creds: [t('home.quick.creds.gpt'), t('home.quick.creds.claude'), t('home.quick.creds.grok'), t('home.quick.creds.x')],
  },
  { title: t('home.quick.s3.title'), desc: t('home.quick.s3.desc') },
])

const services = computed(() => [
  {
    to: '/recharge',
    title: t('home.services.recharge.title'),
    en: t('home.services.recharge.en'),
    cta: t('home.services.recharge.cta'),
    icon: '<svg xmlns="http://www.w3.org/2000/svg" class="h-6 w-6" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/><path d="M9 14l2 2 4-4"/></svg>',
    points: [t('home.services.recharge.p1'), t('home.services.recharge.p2'), t('home.services.recharge.p3')],
  },
  {
    to: '/batch',
    title: t('home.services.batch.title'),
    en: t('home.services.batch.en'),
    cta: t('home.services.batch.cta'),
    icon: '<svg xmlns="http://www.w3.org/2000/svg" class="h-6 w-6" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="7"/><rect x="14" y="3" width="7" height="7"/><rect x="14" y="14" width="7" height="7"/><rect x="3" y="14" width="7" height="7"/></svg>',
    points: [t('home.services.batch.p1'), t('home.services.batch.p2'), t('home.services.batch.p3')],
  },
  {
    to: '/history',
    title: t('home.services.lookup.title'),
    en: t('home.services.lookup.en'),
    cta: t('home.services.lookup.cta'),
    icon: '<svg xmlns="http://www.w3.org/2000/svg" class="h-6 w-6" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>',
    points: [t('home.services.lookup.p1'), t('home.services.lookup.p2'), t('home.services.lookup.p3')],
  },
  {
    to: '/billing',
    title: t('home.services.billing.title'),
    en: t('home.services.billing.en'),
    cta: t('home.services.billing.cta'),
    icon: '<svg xmlns="http://www.w3.org/2000/svg" class="h-6 w-6" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="20" x2="18" y2="10"/><line x1="12" y1="20" x2="12" y2="4"/><line x1="6" y1="20" x2="6" y2="14"/></svg>',
    points: [t('home.services.billing.p1'), t('home.services.billing.p2'), t('home.services.billing.p3')],
  },
])

const queries = computed(() => [
  { to: '/recharge', title: t('home.query.progress.title'), desc: t('home.query.progress.desc'), cta: t('home.query.progress.cta') },
  { to: '/history', title: t('home.query.status.title'), desc: t('home.query.status.desc'), cta: t('home.query.status.cta') },
  { to: '/billing', title: t('home.query.billing.title'), desc: t('home.query.billing.desc'), cta: t('home.query.billing.cta') },
])

// session 和 claude 的答案是固定说明（带链接或两段），在模板里单独渲染
const faqs = computed(() => ['entry', 'pending', 'session', 'claude', 'used', 'failed', 'agent'].map((key) => ({
  key,
  q: t(`home.faq.${key}.q`),
  a: key === 'session' || key === 'claude' ? '' : t(`home.faq.${key}.a`),
})))
</script>

<style scoped>
.faq-item { padding-top: 1rem; padding-bottom: 1rem; }
.faq-q { cursor: pointer; list-style: none; display: flex; justify-content: space-between; align-items: center; gap: 1rem; }
.faq-q::-webkit-details-marker { display: none; }
.faq-q::after { content: '+'; flex-shrink: 0; font-size: 1.25rem; line-height: 1; color: var(--primary); }
details[open] > .faq-q::after { content: '−'; }
</style>
