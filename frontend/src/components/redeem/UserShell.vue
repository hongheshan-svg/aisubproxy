<script setup lang="ts">
import { brandColorVars } from '../../lib/brandColor'
import { siteBrand, themeMode } from '../../theme'
</script>

<template>
  <div class="user-ui min-h-screen" :data-theme="themeMode" :style="brandColorVars(siteBrand.primary)">
    <slot />
  </div>
</template>
