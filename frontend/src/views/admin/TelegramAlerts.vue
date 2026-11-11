<template>
  <div class="space-y-4">
    <AdminPageHeader
      class="!mb-0"
      title="Telegram 告警"
      description="系统告警（付款转人工、熔断、浏览器故障、卡池资金上限、换码异常、每日待办等）推送到 Telegram。后台配置优先，没配时沿用环境变量 TELEGRAM_BOT_TOKEN、TELEGRAM_CHAT_ID。"
    />

    <div v-if="cfg && !cfg.master_key_ok" class="alert alert-error font-semibold">
      服务器未配置 APP_MASTER_KEY，不能保存机器人令牌（令牌要加密保存）。
    </div>
    <div v-if="cfg?.token_error" class="alert alert-error font-semibold">{{ cfg.token_error }}</div>

    <!-- 当前状态 -->
    <div class="card space-y-2">
      <div class="flex flex-wrap items-center gap-2 text-sm">
        <span class="text-muted">当前生效</span>
        <el-tag :type="sourceTag.type" effect="plain">{{ sourceTag.text }}</el-tag>
        <span v-if="cfg?.effective_chat_id" class="mono text-xs text-subtle">chat id {{ cfg.effective_chat_id }}</span>
      </div>
      <div v-if="cfg?.source === 'none'" class="text-xs text-warn">
        现在告警只写进服务日志，不会推送。
      </div>
      <div v-if="cfg?.source === 'env'" class="text-xs text-subtle">
        正在使用环境变量里的配置。在下面保存后台配置后，以后台配置为准。
      </div>
    </div>

    <!-- 配置 -->
    <div class="card space-y-4">
      <h3 class="ops-step-title"><span class="ops-step-no">1</span>机器人与接收方</h3>
      <el-form label-position="top" class="tg-field" @submit.prevent>
        <el-form-item label="机器人令牌">
          <el-input
            v-model="form.token"
            type="password"
            show-password
            autocomplete="off"
            class="mono"
            :placeholder="cfg?.token_hint ? `已保存 ${cfg.token_hint}` : '@BotFather 给的令牌，形如 123456789:AAH…'"
          />
          <div class="text-xs text-subtle mt-1">只写不读：保存后页面只显示机器人 ID 和末 4 位；已保存过的，留空表示不修改。</div>
        </el-form-item>
        <el-form-item label="Chat ID">
          <el-input v-model="form.chat_id" class="mono" placeholder="私聊填你的数字 ID；群组是以 - 开头的数字" />
          <div class="text-xs text-subtle mt-1">
            私聊：在 Telegram 里问 @userinfobot 拿到自己的数字 ID。群组：先把机器人拉进群，群组 ID 一般以 -100 开头。
          </div>
        </el-form-item>
        <div class="flex flex-wrap gap-2">
          <el-button type="primary" :loading="saving" @click="save">保存</el-button>
          <el-button v-if="cfg?.token_configured || cfg?.chat_id" :loading="clearing" @click="clearConfig">清除后台配置</el-button>
        </div>
      </el-form>
    </div>

    <!-- 测试 -->
    <div class="card space-y-3">
      <h3 class="ops-step-title"><span class="ops-step-no">2</span>发送测试消息</h3>
      <p class="text-xs text-subtle tg-field">
        机器人不能主动给没跟它说过话的人发消息：第一次使用前，先在 Telegram 里打开这个机器人，给它发一条消息。
      </p>
      <div class="flex flex-wrap items-center gap-3">
        <el-button :loading="testing" :disabled="!cfg || cfg.source === 'none'" @click="sendTest">发送测试消息</el-button>
        <span v-if="testResult" class="text-sm" :class="testResult.ok ? 'text-ok' : 'text-err'">{{ testResult.text }}</span>
      </div>
      <p class="text-xs text-subtle tg-field">
        服务器要能访问 api.telegram.org。所在网络访问不了时，设置环境变量 TELEGRAM_PROXY（如 http://127.0.0.1:7890 或 socks5://主机:端口）后重启服务；这个代理只给 Telegram 用，不影响其他接口。
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import AdminPageHeader from '../../components/AdminPageHeader.vue'
import { authFetch } from '../../lib/api'
import { dialog } from '../../lib/dialog'
import { telegramSourceTag, type TelegramConfig } from '../../lib/telegram'

const API = '/api/v1/admin/notify/telegram'

const cfg = ref<TelegramConfig | null>(null)
const form = reactive({ token: '', chat_id: '' })
const saving = ref(false)
const clearing = ref(false)
const testing = ref(false)
const testResult = ref<{ ok: boolean; text: string } | null>(null)

const sourceTag = computed(() => telegramSourceTag(cfg.value?.source))

function apply(d: TelegramConfig) {
  cfg.value = d
  form.token = ''
  form.chat_id = d.chat_id || ''
}

async function load() {
  try {
    const r = await authFetch(API)
    const d = await r.json().catch(() => ({}))
    if (!r.ok) {
      dialog.toast(d.error || '读取配置失败', 'err')
      return
    }
    apply(d)
  } catch {
    dialog.toast('读取配置失败', 'err')
  }
}

async function save() {
  saving.value = true
  try {
    const body: Record<string, string> = { chat_id: form.chat_id.trim() }
    if (form.token.trim()) body.token = form.token.trim()
    const r = await authFetch(API, { method: 'PUT', body: JSON.stringify(body) })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) {
      dialog.toast(d.error || '保存失败', 'err')
      return
    }
    apply(d)
    testResult.value = null
    dialog.toast('已保存，可以发一条测试消息确认', 'ok')
  } catch {
    dialog.toast('网络错误，不确定是否已保存，请刷新页面核对', 'err')
  } finally {
    saving.value = false
  }
}

async function clearConfig() {
  const ok = await dialog.confirm('清除后台保存的机器人令牌和 Chat ID？之后如有环境变量就用环境变量，否则告警只写日志。', {
    title: '清除 Telegram 配置',
    okText: '清除',
    danger: true,
  })
  if (!ok) return
  clearing.value = true
  try {
    const r = await authFetch(API, { method: 'PUT', body: JSON.stringify({ clear: true }) })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) {
      dialog.toast(d.error || '清除失败', 'err')
      return
    }
    apply(d)
    testResult.value = null
    dialog.toast('已清除后台配置', 'ok')
  } catch {
    dialog.toast('网络错误，不确定是否已保存，请刷新页面核对', 'err')
  } finally {
    clearing.value = false
  }
}

async function sendTest() {
  testing.value = true
  testResult.value = null
  try {
    const r = await authFetch(`${API}/test`, { method: 'POST' })
    const d = await r.json().catch(() => ({}))
    testResult.value = r.ok
      ? { ok: true, text: `已发送到 chat id ${d.chat_id}，请到 Telegram 查看` }
      : { ok: false, text: d.error || '发送失败' }
  } catch {
    testResult.value = { ok: false, text: '请求失败' }
  } finally {
    testing.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.tg-field { max-width: 720px; width: 100%; }
</style>
