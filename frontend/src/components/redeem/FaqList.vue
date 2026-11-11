<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import SessionHelp from '../SessionHelp.vue'

const { t } = useI18n({ useScope: 'global' })

// session 和 claude 的答案是固定说明（带链接或两段），在模板里单独渲染
const faqs = computed(() => ['entry', 'pending', 'session', 'claude', 'used', 'failed', 'agent'].map((key) => ({
  key,
  q: t(`home.faq.${key}.q`),
  a: key === 'session' || key === 'claude' ? '' : t(`home.faq.${key}.a`),
})))
</script>

<template>
  <section id="help" class="faq" aria-labelledby="faq-title">
    <h2 id="faq-title" class="faq-title">{{ t('home.faq.title') }}</h2>
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
</template>

<style scoped>
.faq { margin-top: 32px; display: flex; flex-direction: column; gap: 10px; }
.faq-title { margin: 0 0 4px; font-size: 20px; font-weight: 700; }
.faq-item { padding: 12px 16px; }
.faq-q { cursor: pointer; list-style: none; display: flex; justify-content: space-between; align-items: center; gap: 12px; min-height: 28px; font-size: 16px; font-weight: 600; }
.faq-q::-webkit-details-marker { display: none; }
.faq-q::after { content: '+'; flex-shrink: 0; font-size: 20px; line-height: 1; color: var(--u-primary); }
details[open] > .faq-q::after { content: '−'; }
.faq-a { margin-top: 10px; font-size: 15px; color: var(--u-muted); }
.faq-a p { margin: 0 0 8px; }
</style>
