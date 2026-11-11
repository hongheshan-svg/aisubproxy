<template>
  <div class="space-y-4">
    <AdminPageHeader class="!mb-0" title="外观与品牌" description="站点名称、副标题、Logo、默认亮暗模式和皮肤。保存后用户端、后台和代理商后台一起生效；没保存的皮肤只是预览，刷新或离开本页会还原。" />

    <el-card shadow="never">
      <el-form label-width="120px" class="max-w-4xl">
        <el-form-item label="站点名称">
          <el-input v-model="form.brand_name" maxlength="40" show-word-limit class="max-w-xl" />
        </el-form-item>
        <el-form-item label="副标题">
          <el-input v-model="form.brand_sub" maxlength="80" show-word-limit class="max-w-xl" />
        </el-form-item>
        <el-form-item label="Logo">
          <div class="w-full">
            <img v-if="logoVersion" :src="`/api/v1/public/site/logo?v=${logoVersion}`" alt="当前 Logo" class="h-12 max-w-[200px] object-contain mb-2" />
            <div class="flex items-center gap-2 flex-wrap">
              <input type="file" :accept="LOGO_ACCEPT" :disabled="logoBusy" @change="onLogoPick" />
              <el-button v-if="logoVersion" :disabled="logoBusy" @click="removeLogo">删除 Logo</el-button>
            </div>
            <div class="text-xs text-subtle mt-1">PNG、JPG 或 WebP，不超过 256 KB。没有 Logo 时用户端页头只显示站名。</div>
          </div>
        </el-form-item>
        <el-form-item label="皮肤（用户端和后台一起换）">
          <div class="w-full">
            <SkinPicker compact />
          </div>
        </el-form-item>
        <el-form-item label="明暗">
          <el-radio-group :model-value="themeMode" :disabled="themeLocked" @update:model-value="(v: any) => setTheme(v as ThemeMode)">
            <el-radio-button value="light">亮色</el-radio-button>
            <el-radio-button value="dark">暗色</el-radio-button>
            <el-radio-button value="auto">跟随系统</el-radio-button>
          </el-radio-group>
          <div class="text-xs text-subtle w-full mt-1">保存的明暗是访客的默认值；每个人仍可在自己的浏览器里切换。<template v-if="themeLocked">当前皮肤只有暗色，明暗不可选。</template></div>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" data-testid="save-brand" :loading="saving" @click="saveBrand">保存并对所有访客生效</el-button>
          <span v-if="dirty" class="text-xs ml-3" style="color: var(--warn)">皮肤尚未保存</span>
        </el-form-item>
      </el-form>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import AdminPageHeader from '../../components/AdminPageHeader.vue'
import { siteBrand, setSiteBrand, setSkin, setTheme, siteSkin, themeMode, themeLocked, applyThemeAll, type SkinId, type ThemeMode } from '../../theme'
import SkinPicker from '../../components/SkinPicker.vue'
import { authFetch } from '../../lib/api'
import { dialog } from '../../lib/dialog'
import { applySettings, buildSavePayload, checkLogoFile, LOGO_ACCEPT } from '../../lib/siteAppearance'

const form = reactive({
  brand_name: siteBrand.value.name,
  brand_sub: siteBrand.value.sub,
})
const logoVersion = ref(siteBrand.value.logoVersion || '')
const logoBusy = ref(false)
const saving = ref(false)
// 服务端已保存的皮肤：离开本页时，未保存的预览要还原
const savedSkin = ref<SkinId>(siteSkin.value)
const dirty = computed(() => siteSkin.value !== savedSkin.value)

async function load() {
  try {
    const r = await authFetch('/api/v1/admin/settings')
    if (!r.ok) return
    const d = await r.json()
    const next = applySettings(d, { ...form, logoVersion: logoVersion.value })
    form.brand_name = next.brand_name
    form.brand_sub = next.brand_sub
    logoVersion.value = next.logoVersion
    setSiteBrand({ name: form.brand_name, sub: form.brand_sub, logoVersion: next.logoVersion })
    if (next.skin) {
      savedSkin.value = next.skin as SkinId
      setSkin(next.skin as SkinId)
    }
    // 只有服务端存了明暗才改；没存时不动本机偏好，页面默认选中 auto 靠 themeMode 的初始值
    if (next.theme) setTheme(next.theme as ThemeMode)
  } catch { /* ignore */ }
}
onMounted(load)

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
      body: JSON.stringify(buildSavePayload({ ...form, logoVersion: logoVersion.value }, siteSkin.value, themeMode.value)),
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

async function errText(r: Response, fallback: string) {
  const d = await r.json().catch(() => ({}))
  return d.error || fallback
}

async function onLogoPick(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file || logoBusy.value) return
  const bad = checkLogoFile(file)
  if (bad) {
    dialog.toast(bad, 'err')
    input.value = ''
    return
  }
  logoBusy.value = true
  try {
    const fd = new FormData()
    fd.append('file', file)
    const r = await authFetch('/api/v1/admin/settings/logo', { method: 'POST', body: fd })
    if (!r.ok) {
      dialog.toast(await errText(r, '上传失败'), 'err')
      return
    }
    const d = await r.json().catch(() => ({}))
    logoVersion.value = d.logo_version || ''
    setSiteBrand({ logoVersion: logoVersion.value })
    dialog.toast('Logo 已更新', 'ok')
  } catch {
    dialog.toast('上传失败，请稍后再试', 'err')
  } finally {
    logoBusy.value = false
    input.value = ''
  }
}

async function removeLogo() {
  if (logoBusy.value) return
  if (!(await dialog.confirm('删除后用户端页头只显示站名。确定删除 Logo 吗？', { title: '删除 Logo', danger: true }))) return
  logoBusy.value = true
  try {
    const r = await authFetch('/api/v1/admin/settings/logo', { method: 'DELETE' })
    if (!r.ok) {
      dialog.toast(await errText(r, '删除失败'), 'err')
      return
    }
    logoVersion.value = ''
    setSiteBrand({ logoVersion: '' })
    dialog.toast('Logo 已删除', 'ok')
  } catch {
    dialog.toast('删除失败，请稍后再试', 'err')
  } finally {
    logoBusy.value = false
  }
}
</script>
