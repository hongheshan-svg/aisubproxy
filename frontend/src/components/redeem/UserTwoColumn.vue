<template>
  <!-- 电脑布局两栏：默认插槽是主栏，aside 是侧栏，wide 是两栏之后的通栏；手机上全部竖排 -->
  <div class="u-cols">
    <div class="u-col-main"><slot /></div>
    <aside v-if="$slots.aside" class="u-col-aside"><slot name="aside" /></aside>
  </div>
  <div v-if="$slots.wide" class="u-wide"><slot name="wide" /></div>
</template>

<style scoped>
.u-cols { display: block; }
.u-col-main { min-width: 0; }
.u-col-aside { min-width: 0; margin-top: 32px; }
@media (min-width: 1024px) {
  .u-cols { display: grid; grid-template-columns: minmax(0, 3fr) minmax(0, 2fr); gap: 32px; align-items: start; }
  .u-col-aside { margin-top: 0; }
}
</style>
