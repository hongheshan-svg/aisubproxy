<template>
  <el-form label-position="top" autocomplete="off" @submit.prevent>
    <div class="grid-form">
      <el-form-item label="英文名（first name）"><el-input v-model="form.first_name" maxlength="32" autocomplete="off" /></el-form-item>
      <el-form-item label="英文姓（last name）"><el-input v-model="form.last_name" maxlength="32" autocomplete="off" /></el-form-item>
      <el-form-item label="中文名（可选）"><el-input v-model="form.chinese_first_name" maxlength="32" autocomplete="off" /></el-form-item>
      <el-form-item label="中文姓（可选）"><el-input v-model="form.chinese_last_name" maxlength="32" autocomplete="off" /></el-form-item>
      <el-form-item label="出生日期（YYYY-MM-DD）"><el-input v-model="form.date_of_birth" placeholder="1990-01-31" autocomplete="off" /></el-form-item>
      <el-form-item label="国籍（两位国家码）"><el-input v-model="form.country_code" maxlength="2" placeholder="US" autocomplete="off" /></el-form-item>
      <el-form-item label="手机区号"><el-input v-model="form.zone_number" maxlength="11" placeholder="86" autocomplete="off" /></el-form-item>
      <el-form-item label="手机号"><el-input v-model="form.mobile" maxlength="50" autocomplete="off" /></el-form-item>
      <el-form-item label="邮箱"><el-input v-model="form.email" maxlength="100" autocomplete="off" /></el-form-item>
      <el-form-item label="证件类型">
        <el-select v-model="form.id_type" style="width: 100%">
          <el-option label="护照" value="PASSPORT" />
          <el-option label="身份证" value="IDCARD" />
          <el-option label="其他证件" value="OTHER_CERTIFICATE" />
        </el-select>
      </el-form-item>
      <el-form-item label="证件号"><el-input v-model="form.id_no" maxlength="64" autocomplete="off" /></el-form-item>
      <el-form-item label="证件生效日（YYYY-MM-DD）"><el-input v-model="form.id_period_begin" autocomplete="off" /></el-form-item>
      <el-form-item label="证件到期日（长期有效填 9999-12-31）"><el-input v-model="form.id_period_end" autocomplete="off" /></el-form-item>
      <el-form-item v-if="isUS" label="SSN（###-##-####）"><el-input v-model="form.ssn" maxlength="11" autocomplete="off" /></el-form-item>
    </div>

    <el-form-item :label="`证件照（1–2 张，jpg / png / pdf，单张不超过 7MB）`">
      <div class="w-full">
        <div v-for="(id, i) in form.id_photo_file_ids" :key="id + i" class="flex items-center gap-2 mb-1">
          <span class="mono text-sm flex-1 truncate">{{ id }}</span>
          <el-button size="small" text type="danger" @click="removePhoto(i)">删除</el-button>
        </div>
        <el-upload
          v-if="form.id_photo_file_ids.length < 2"
          :show-file-list="false"
          accept=".jpg,.jpeg,.png,.pdf"
          :http-request="uploadPhoto"
          :before-upload="beforePhoto"
          :disabled="uploading"
        >
          <el-button size="small" :loading="uploading">上传证件照</el-button>
        </el-upload>
        <p v-if="uploadError" class="field-error mt-1">{{ uploadError }}</p>
      </div>
    </el-form-item>

    <div class="text-sm font-semibold text-ink mb-1">居住地址</div>
    <div class="grid-form">
      <el-form-item label="国家码"><el-input v-model="form.residential_address.country" maxlength="2" placeholder="US" autocomplete="off" /></el-form-item>
      <el-form-item label="州 / 省"><el-input v-model="form.residential_address.state" maxlength="64" autocomplete="off" /></el-form-item>
      <el-form-item label="城市"><el-input v-model="form.residential_address.city" maxlength="64" autocomplete="off" /></el-form-item>
      <el-form-item label="详细地址"><el-input v-model="form.residential_address.address" maxlength="256" autocomplete="off" /></el-form-item>
      <el-form-item label="邮编"><el-input v-model="form.residential_address.post_code" maxlength="10" autocomplete="off" /></el-form-item>
    </div>

    <el-checkbox v-model="form.billing_same" class="mb-2">账单地址与居住地址相同</el-checkbox>
    <template v-if="!form.billing_same">
      <div class="text-sm font-semibold text-ink mb-1">账单地址</div>
      <div class="grid-form">
        <el-form-item label="国家码"><el-input v-model="form.billing_address.country" maxlength="2" autocomplete="off" /></el-form-item>
        <el-form-item label="州 / 省"><el-input v-model="form.billing_address.state" maxlength="64" autocomplete="off" /></el-form-item>
        <el-form-item label="城市"><el-input v-model="form.billing_address.city" maxlength="64" autocomplete="off" /></el-form-item>
        <el-form-item label="详细地址"><el-input v-model="form.billing_address.address" maxlength="256" autocomplete="off" /></el-form-item>
        <el-form-item label="邮编"><el-input v-model="form.billing_address.post_code" maxlength="10" autocomplete="off" /></el-form-item>
      </div>
    </template>

    <el-alert v-if="errors.length" type="error" :closable="false" class="mb-3" title="请先修正以下问题">
      <ul class="err-list"><li v-for="e in errors" :key="e">{{ e }}</li></ul>
    </el-alert>

    <div class="flex justify-end gap-2">
      <el-button @click="emit('cancel')">取消</el-button>
      <el-button type="primary" :loading="submitting" :disabled="uploading" @click="submit">
        {{ mode === 'edit' ? '保存修改' : '提交新建' }}
      </el-button>
    </div>
  </el-form>
</template>

<script setup lang="ts">
// 持卡人资料表单。资料只存在于本组件内存里：不写 localStorage / sessionStorage，卸载（对话框关闭）时清空。
import { computed, onBeforeUnmount, reactive, ref } from 'vue'
import {
  emptyCardholderForm, poolApi, errText, toCardholderPayload, validateCardholderForm,
  type CardholderFormValue,
} from '../../../lib/cardpool'

const props = defineProps<{ mode: 'create' | 'edit'; initial?: CardholderFormValue; submitting?: boolean }>()
const emit = defineEmits<{
  (e: 'submitted', payload: Record<string, unknown>): void
  (e: 'cancel'): void
}>()

const MAX_PHOTO = 7 * 1024 * 1024
const form = reactive<CardholderFormValue>(
  props.initial ? (JSON.parse(JSON.stringify(props.initial)) as CardholderFormValue) : emptyCardholderForm(),
)
const errors = ref<string[]>([])
const uploading = ref(false)
const uploadError = ref('')

const isUS = computed(() => form.country_code.trim().toUpperCase() === 'US')

function localToday(): string {
  const d = new Date()
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

function beforePhoto(file: File): boolean {
  if (file.size > MAX_PHOTO) {
    uploadError.value = '文件超过 7MB'
    return false
  }
  if (form.id_photo_file_ids.length >= 2) {
    uploadError.value = '最多 2 张证件照'
    return false
  }
  return true
}

async function uploadPhoto(opt: { file: File }) {
  uploadError.value = ''
  uploading.value = true
  try {
    const fd = new FormData()
    fd.append('file', opt.file)
    const r = await poolApi<{ file_id: string }>('/cardholders/photos', 'POST', fd)
    if (!r.ok || !r.data.file_id) {
      uploadError.value = r.status === 413 ? '文件超过 7MB' : errText(r, '上传失败')
      return
    }
    if (form.id_photo_file_ids.length < 2) form.id_photo_file_ids.push(r.data.file_id)
  } finally {
    uploading.value = false
  }
}

function removePhoto(i: number) {
  form.id_photo_file_ids.splice(i, 1)
}

function submit() {
  errors.value = validateCardholderForm(form, localToday())
  if (errors.value.length) return
  emit('submitted', toCardholderPayload(form))
}

onBeforeUnmount(() => {
  Object.assign(form, emptyCardholderForm())
  errors.value = []
})
</script>

<style scoped>
.grid-form {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  column-gap: 16px;
}
.field-error {
  font-size: 13px;
}
.err-list {
  margin: 4px 0 0 16px;
  list-style: disc;
  font-size: 13px;
}
</style>
