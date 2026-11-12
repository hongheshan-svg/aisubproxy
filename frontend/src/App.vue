<template>
  <div id="app" class="min-h-screen">
    <router-view />
    <DialogHost />
    <NewVersionBanner />
  </div>
</template>

<script setup lang="ts">
import DialogHost from './components/DialogHost.vue'
import NewVersionBanner from './components/NewVersionBanner.vue'
</script>
