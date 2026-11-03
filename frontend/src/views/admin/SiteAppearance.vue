<template>
  <div class="space-y-4">
    <AdminPageHeader class="!mb-0" title="外观与品牌" description="站点名称、副标题和整站皮肤。保存后对所有访客生效；未保存的皮肤只是预览，刷新或离开本页会还原。" />

    <el-card shadow="never">
      <el-form label-width="120px" class="max-w-4xl">
        <el-form-item label="站点名称">
          <el-input v-model="form.brand_name" maxlength="40" show-word-limit class="max-w-xl" />
        </el-form-item>
        <el-form-item label="副标题">
          <el-input v-model="form.brand_sub" maxlength="80" show-word-limit class="max-w-xl" />
        </el-form-item>
        <el-form-item label="整站皮肤">
          <div class="w-full">
            <SkinPicker compact />
          </div>
        </el-form-item>
        <el-form-item label="明暗">
          <el-radio-group :model-value="themeMode" @update:model-value="(v: any) => setTheme(v as ThemeMode)">
            <el-radio-button value="light">亮色</el-radio-button>
            <el-radio-button value="dark">暗色</el-radio-button>
            <el-radio-button value="auto">跟随系统</el-radio-button>
          </el-radio-group>
          <div class="text-xs text-subtle w-full mt-1">保存的明暗是访客的默认值；每个人仍可在自己的浏览器里切换。</div>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="saving" @click="saveBrand">保存并对所有访客生效</el-button>
          <span v-if="dirty" class="text-xs ml-3" style="color: var(--warn)">皮肤尚未保存</span>
        </el-form-item>
      </el-form>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import AdminPageHeader from '../../components/AdminPageHeader.vue'
import { siteBrand, setSiteBrand, setSkin, setTheme, siteSkin, themeMode, applyThemeAll, type SkinId, type ThemeMode } from '../../theme'
import SkinPicker from '../../components/SkinPicker.vue'
import { authFetch } from '../../lib/api'
import { dialog } from '../../lib/dialog'

const form = reactive({
  brand_name: siteBrand.value.name,
  brand_sub: siteBrand.value.sub,
})
const saving = ref(false)
// 服务端已保存的皮肤：离开本页时，未保存的预览要还原
const savedSkin = ref<SkinId>(siteSkin.value)
const dirty = computed(() => siteSkin.value !== savedSkin.value)

onMounted(async () => {
  try {
    const r = await authFetch('/api/v1/admin/settings')
    if (!r.ok) return
    const d = await r.json()
    form.brand_name = d.brand_name || form.brand_name
    form.brand_sub = d.brand_sub || form.brand_sub
    if (d.skin) {
      savedSkin.value = d.skin as SkinId
      setSkin(d.skin as SkinId)
    }
    if (d.theme_mode) setTheme(d.theme_mode as ThemeMode)
    setSiteBrand({ name: form.brand_name, sub: form.brand_sub })
  } catch { /* ignore */ }
})

onBeforeUnmount(() => {
  if (dirty.value) setSkin(savedSkin.value)
})

async function saveBrand() {
  saving.value = true
  try {
    setSiteBrand({ name: form.brand_name, sub: form.brand_sub })
    applyThemeAll()
    const r = await authFetch('/api/v1/admin/settings', {
      method: 'PUT',
      body: JSON.stringify({
        brand_name: form.brand_name,
        brand_sub: form.brand_sub,
        skin: siteSkin.value,
        theme_mode: themeMode.value,
      }),
    })
    if (!r.ok) {
      const d = await r.json().catch(() => ({}))
      dialog.toast(d.error || '保存失败', 'err')
      return
    }
    savedSkin.value = siteSkin.value
    dialog.toast('已保存，对所有访客生效', 'ok')
  } finally {
    saving.value = false
  }
}
</script>
