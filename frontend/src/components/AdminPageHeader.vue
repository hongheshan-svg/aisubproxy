<template>
  <header class="aph">
    <div class="aph-text">
      <h1 class="aph-title">{{ title }}</h1>
      <p v-if="description || $slots.description" class="aph-desc"><slot name="description">{{ description }}</slot></p>
    </div>
    <div v-if="$slots.actions" class="aph-actions"><slot name="actions" /></div>
  </header>
</template>

<script setup lang="ts">
defineProps<{ title: string; description?: string }>()
</script>

<style scoped>
.aph {
  display: flex; align-items: flex-start; justify-content: space-between;
  gap: 12px 16px; flex-wrap: wrap; margin-bottom: 18px;
}
.aph-text { min-width: 0; flex: 1 1 320px; }
.aph-title {
  font-size: 22px; font-weight: 600; line-height: 1.3; color: var(--ink);
  font-family: var(--font-display, var(--font-serif));
}
.aph-desc { font-size: 13px; color: var(--ink-2); margin-top: 4px; line-height: 1.55; }
.aph-actions { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
</style>
